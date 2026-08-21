// Package motionphoto 负责从 JPEG Motion Photo（动态照片）中分离静态图与视频。
//
// 支持两类主流格式：
//  1. Google/OnePlus 风格：JPEG 末尾追加一个 MP4，XMP（APP1 段）中
//     声明 `Item:Semantic="MotionPhoto"` 以及视频长度。
//  2. 通用扫描：直接在文件尾部搜索 ftyp 盒，提取 MP4。
//
// 主要入口：
//   - Detect：识别文件类型与内嵌结构
//   - ExtractVideo / ExtractStill：分离视频与静态图
package motionphoto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Info 描述一个 JPEG Motion Photo 的内部结构。
type Info struct {
	IsMotionPhoto bool  // 是否为动态照片
	JPEGEnd       int64 // 主 JPEG 图像结束位置（EOI 之后）
	VideoOffset   int64 // 视频起始位置（文件绝对偏移）
	VideoLength   int64 // 视频长度
	VideoPadding  int64 // 视频后的填充字节数
	VideoMime     string
	MPFOffset     int64 // MPF 头偏移（如有）
}

// ErrNotMotionPhoto 表示文件不是 Motion Photo（无内嵌视频）。
var ErrNotMotionPhoto = errors.New("motionphoto: not a motion photo")

// IsJPEG 判断数据是否为 JPEG（以 FFD8 开头）。
func IsJPEG(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8
}

// IsMP4 判断数据是否为 MP4（以 ftyp 开头）。
func IsMP4(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

// IsAVIF 判断数据是否为 AVIF（以 ftyp 开头且含 meta）。
func IsAVIF(data []byte) bool {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return false
	}
	// 遍历顶层盒子查找 meta
	off := int64(0)
	for off+8 <= int64(len(data)) {
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		hdr := int64(8)
		if size == 1 {
			size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		} else if size == 0 {
			size = int64(len(data)) - off
		}
		if size < hdr || off+size > int64(len(data)) {
			return false
		}
		if typ == "meta" {
			return true
		}
		off += size
	}
	return false
}

// xmpItemRe 匹配 XMP 中的 Container:Item 元素。
var xmpItemRe = regexp.MustCompile(`Item:Mime="([^"]+)"\s+Item:Semantic="([^"]+)"\s+Item:Length="(\d+)"\s+Item:Padding="(\d+)"`)

// scanJPEG 扫描 JPEG 段，返回主图像 EOI 位置与 XMP 文本。
func scanJPEG(data []byte) (int64, string) {
	p := int64(2)
	var eoi int64 = -1
	var xmp string
	for p+4 <= int64(len(data)) {
		if data[p] != 0xFF {
			return eoi, xmp
		}
		marker := data[p+1]
		switch {
		case marker == 0xD8: // SOI
			p += 2
			continue
		case marker == 0xD9: // EOI
			eoi = p
			return eoi, xmp
		case marker >= 0xD0 && marker <= 0xD7, marker == 0x01: // 无长度段
			p += 2
			continue
		case marker == 0xDA: // SOS：跳到 EOI
			p += 2
			// 扫描熵编码数据直到 FFD9
			i := p
			for i+1 < int64(len(data)) {
				if data[i] == 0xFF && data[i+1] == 0xD9 {
					eoi = i
					return eoi, xmp
				}
				i++
			}
			return eoi, xmp
		default: // 带长度段的段
			if p+4 > int64(len(data)) {
				return eoi, xmp
			}
			segLen := int64(binary.BigEndian.Uint16(data[p+2 : p+4]))
			if segLen < 2 {
				return eoi, xmp
			}
			segData := data[p+4 : p+2+segLen]
			// XMP 段：APP1 且内容以 "http://ns.adobe.com/xap/1.0/" 开头
			if marker == 0xE1 && bytes.HasPrefix(segData, []byte("http://ns.adobe.com/xap/1.0/")) {
				xmp = string(segData)
			}
			p += 2 + segLen
		}
	}
	return eoi, xmp
}

// parseXMPContainer 从 XMP 文本中解析 Container 条目信息。
// 返回 (length, padding, mime, ok)。
// XMP 中可能声明多个条目（Primary 图片 + MotionPhoto 视频），
// 只接受 Semantic="MotionPhoto" 的条目。
func parseXMPContainer(xmp string) (int64, int64, string, bool) {
	matches := xmpItemRe.FindAllStringSubmatch(xmp, -1)
	for _, m := range matches {
		// m[2] 是 Semantic 字段
		if m[2] != "MotionPhoto" {
			continue
		}
		length, err1 := strconv.ParseInt(m[3], 10, 64)
		padding, err2 := strconv.ParseInt(m[4], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, "", false
		}
		return length, padding, m[1], true
	}
	return 0, 0, "", false
}

// findTrailingMP4 从文件中寻找最可能的内嵌 MP4。
// 策略：扫描所有合法 ftyp 盒，沿盒子链向后走，选择盒子链终点
// 最接近文件末尾（或正好到达末尾）的候选。这能避免误选文件中间
// 嵌套的预览视频。
func findTrailingMP4(data []byte) int64 {
	n := int64(len(data))
	best := int64(-1)
	bestEnd := int64(-1)
	for off := int64(0); off+8 <= n; off++ {
		if !(data[off+4] == 'f' && data[off+5] == 't' && data[off+6] == 'y' && data[off+7] == 'p') {
			continue
		}
		// 校验 ftyp 盒合法
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		hdr := int64(8)
		if size == 1 {
			if off+16 > n {
				continue
			}
			size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		} else if size == 0 {
			size = n - off
		}
		if size < hdr || off+size > n {
			continue
		}
		// 沿盒子链向后走
		chainEnd := off
		c := off
		for c+8 <= n {
			bs := int64(binary.BigEndian.Uint32(data[c : c+4]))
			bt := string(data[c+4 : c+8])
			_ = bt
			bh := int64(8)
			if bs == 1 {
				if c+16 > n {
					break
				}
				bs = int64(binary.BigEndian.Uint64(data[c+8 : c+16]))
				bh = 16
			} else if bs == 0 {
				bs = n - c
			}
			if bs < bh || c+bs > n {
				break
			}
			c += bs
			chainEnd = c
		}
		// 选择终点最接近文件末尾的候选
		if chainEnd > bestEnd {
			bestEnd = chainEnd
			best = off
		}
	}
	return best
}

// Detect 解析 JPEG 数据，返回动态照片结构信息。
// 若不存在内嵌视频则返回 ErrNotMotionPhoto。
func Detect(data []byte) (*Info, error) {
	if !IsJPEG(data) {
		return nil, ErrNotMotionPhoto
	}
	info := &Info{}

	// 1. 扫描 JPEG 段，找到主图像 EOI 与 XMP APP1
	jpegEnd, xmp := scanJPEG(data)
	if jpegEnd < 0 {
		return nil, fmt.Errorf("motionphoto: malformed jpeg")
	}
	info.JPEGEnd = jpegEnd + 2 // EOI 两个字节

	// 2. 从 XMP 中解析 MotionPhoto 条目信息
	//    注意：XMP 可能包含多个 Container:Item（Primary 图片 + MotionPhoto 视频），
	//    只取 Semantic="MotionPhoto" 的那个。
	if xmp != "" {
		length, padding, mime, ok := parseXMPContainer(xmp)
		if ok {
			info.VideoLength = length
			info.VideoPadding = padding
			info.VideoMime = mime
			// 视频起始 = 文件末尾 - 长度 - 填充
			info.VideoOffset = int64(len(data)) - length - padding
			if info.VideoOffset > 0 && IsMP4(data[info.VideoOffset:]) {
				info.IsMotionPhoto = true
				return info, nil
			}
		}
	}

	// 3. 兜底：扫描 ftyp 盒（选择盒子链最接近文件末尾的候选）
	ftypOff := findTrailingMP4(data)
	if ftypOff >= 0 {
		info.VideoOffset = ftypOff
		info.VideoLength = int64(len(data)) - ftypOff
		info.IsMotionPhoto = true
		info.VideoMime = "video/mp4"
		return info, nil
	}

	return nil, ErrNotMotionPhoto
}

// ExtractVideo 从动态照片中提取视频字节。
func ExtractVideo(data []byte) ([]byte, error) {
	info, err := Detect(data)
	if err != nil {
		return nil, err
	}
	if info.VideoOffset < 0 || info.VideoOffset+info.VideoLength > int64(len(data)) {
		return nil, errors.New("motionphoto: video range out of file")
	}
	return data[info.VideoOffset : info.VideoOffset+info.VideoLength], nil
}

// ExtractStill 从动态照片中提取静态图字节（主 JPEG）。
func ExtractStill(data []byte) ([]byte, error) {
	info, err := Detect(data)
	if err != nil {
		return nil, err
	}
	if info.JPEGEnd <= 0 || info.JPEGEnd > int64(len(data)) {
		return nil, errors.New("motionphoto: bad jpeg end")
	}
	return data[:info.JPEGEnd], nil
}
