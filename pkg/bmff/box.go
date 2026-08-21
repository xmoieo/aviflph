// Package bmff 提供 ISO Base Media File Format (ISOBMFF) / HEIF / AVIF 容器的
// 底层盒子(Box)解析与构建工具。
//
// 这是整个 aviflph 项目的地基：AVIF 与 MP4 都属于 BMFF 容器，
// 无论是读取(exif/iloc/meta)还是写入(组装 xomu live photo)都基于本包。
package bmff

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 常用 4CC 盒子类型常量。
const (
	TypeFtyp = "ftyp" // 文件类型
	TypeMeta = "meta" // 元数据容器
	TypeMdat = "mdat" // 媒体数据（图片/视频裸数据）
	TypeFree = "free" // 空闲填充
	TypeSkip = "skip" // 跳过
	TypeHdlr = "hdlr" // handler 声明
	TypePitm = "pitm" // primary item
	TypeIloc = "iloc" // item location（条目数据在文件中的位置）
	TypeIinf = "iinf" // item info
	TypeInfe = "infe" // item info entry
	TypeIref = "iref" // item reference
	TypeIprp = "iprp" // item properties
	TypeIpco = "ipco" // item property container
	TypeIpma = "ipma" // item property association
	TypeIspe = "ispe" // image spatial extents
	TypePixi = "pixi" // pixel information
	TypeColr = "colr" // colour information
	TypeAv1C = "av1C" // AV1 配置
	TypeCdsc = "cdsc" // content describes（描述性引用，如 EXIF→图片）
	TypeAuxl = "auxl" // auxiliary（辅助引用，如视频→图片）
	TypeMoov = "moov" // MP4 的 movie 容器
	TypeTrak = "trak" // MP4 轨道
	TypeUdta = "udta" // 用户数据
	TypeExif = "Exif" // EXIF 条目类型（注意大小写）
	TypeXomu = "xomu" // 动态照片(live photo)视频条目类型（Xiaomi 约定）
)

// Box 描述文件中的一个盒子。
// Start/Size 均为绝对文件偏移，便于直接切片读取数据。
type Box struct {
	Type       string // 4 字符类型名
	Start      int64  // 盒子起始位置（含头部）
	Size       int64  // 盒子总大小（含头部）
	HeaderSize int64  // 头部大小（8 或 16，取决于 size==1 扩展）
	DataStart  int64  // 数据区起始位置（Start+HeaderSize）
	IsFullBox  bool   // 是否为 FullBox（带 version+flags 的 4 字节头）
	Version    uint8  // FullBox 的版本号
	Flags      uint32 // FullBox 的标志位（低 24 位有效）
	Children   []*Box // 子盒子（仅容器盒子有）
	Parent     *Box   // 父盒子指针
}

// ErrMalformed 表示文件结构不符合 BMFF 规范。
var ErrMalformed = errors.New("malformed bmff box")

// 常见的容器盒子：需要递归解析其子盒子。
// 注意 iloc/iinf/iref/iprp/pitm 等是 FullBox，解析时要先跳过
// version+flags 这 4 个字节，再到子盒子（或直接解析字段）。
var containerTypes = map[string]bool{
	TypeMeta: true,
	TypeIinf: true,
	TypeIref: true,
	TypeIprp: true,
	TypeIpco: true,
	TypeMoov: true,
	TypeTrak: true,
	"edts":   true,
	"mdia":   true,
	"minf":   true,
	"stbl":   true,
	"dinf":   true,
	"udta":   true,
	"mvex":   true,
	"moof":   true,
	"traf":   true,
	"stsd":   true,
	"grpl":   true,
	"mfra":   true,
}

// fullBoxTypes 是需要按 FullBox 语义解析的盒子。
var fullBoxTypes = map[string]bool{
	TypeMeta: true,
	TypePitm: true,
	TypeIloc: true,
	TypeIinf: true,
	TypeInfe: true,
	TypeIref: true,
	TypeIpma: true,
	TypeIspe: true,
	TypePixi: true,
	"mvhd":   true,
	"tkhd":   true,
	"mdhd":   true,
	"hdlr":   true,
	"smhd":   true,
	"vmhd":   true,
	"dref":   true,
	"stsd":   true,
	"stts":   true,
	"stsc":   true,
	"stsz":   true,
	"stco":   true,
	"co64":   true,
	"stss":   true,
	"ctts":   true,
	"stz2":   true,
	"ilst":   true,
	"keys":   true,
}

// Parse 解析整个文件的盒子树。
// 从 start 到 end 逐层读取；对容器盒子会递归展开子盒子。
// depth 用于限制递归深度，防止畸形文件导致栈溢出。
func Parse(data []byte, start, end int64, depth int) ([]*Box, error) {
	if depth > 32 {
		return nil, fmt.Errorf("%w: container nesting too deep", ErrMalformed)
	}
	var boxes []*Box
	off := start
	for off+8 <= end {
		b, err := readBox(data, off, end)
		if err != nil {
			return nil, err
		}
		// 盒子必须完整落在 [start,end) 内
		if b.Start+b.Size > end {
			return nil, fmt.Errorf("%w: box %q overflows region", ErrMalformed, b.Type)
		}
		boxes = append(boxes, b)
		// 若为容器，则递归解析子盒子
		if containerTypes[b.Type] {
			childStart := b.DataStart
			if fullBoxTypes[b.Type] {
				childStart = b.DataStart + 4 // 跳过 version+flags
			}
			if b.Type == TypeIinf {
				// iinf 在 version+flags 之后还有 entry_count 字段
				// （v0 为 2 字节，v1 为 4 字节），子盒从其后开始
				if b.Version >= 1 {
					childStart += 4
				} else {
					childStart += 2
				}
			}
			children, err := Parse(data, childStart, b.Start+b.Size, depth+1)
			if err != nil {
				// 子盒子解析失败不致命：保留父盒子，忽略子盒子
				b.Children = nil
			} else {
				for _, c := range children {
					c.Parent = b
				}
				b.Children = children
			}
		}
		off = b.Start + b.Size
	}
	return boxes, nil
}

// readBox 读取单个盒子头信息。
// 支持 32 位 size、64 位 size(==1) 以及 size==0（延伸到文件末尾）三种形式。
func readBox(data []byte, off, end int64) (*Box, error) {
	if off+8 > end {
		return nil, fmt.Errorf("%w: truncated box header at %d", ErrMalformed, off)
	}
	size := int64(binary.BigEndian.Uint32(data[off : off+4]))
	typ := string(data[off+4 : off+8])
	hdr := int64(8)
	if size == 1 {
		// 64 位扩展尺寸
		if off+16 > end {
			return nil, fmt.Errorf("%w: truncated largesize at %d", ErrMalformed, off)
		}
		size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		hdr = 16
	} else if size == 0 {
		// 0 表示延伸到文件末尾
		size = end - off
	}
	if size < hdr {
		return nil, fmt.Errorf("%w: box %q size %d smaller than header", ErrMalformed, typ, size)
	}
	b := &Box{
		Type:       typ,
		Start:      off,
		Size:       size,
		HeaderSize: hdr,
		DataStart:  off + hdr,
	}
	if fullBoxTypes[typ] && b.DataStart+4 <= off+size {
		b.IsFullBox = true
		raw := data[b.DataStart : b.DataStart+4]
		b.Version = raw[0]
		b.Flags = uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	}
	return b, nil
}

// Find 在盒子列表中按类型查找第一个匹配的盒子。
func Find(boxes []*Box, typ string) *Box {
	for _, b := range boxes {
		if b.Type == typ {
			return b
		}
	}
	return nil
}

// FindAll 在盒子列表中查找所有匹配的盒子。
func FindAll(boxes []*Box, typ string) []*Box {
	var out []*Box
	for _, b := range boxes {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}

// Walk 深度优先遍历盒子树（含自身），对每个盒子调用 fn。
func Walk(root *Box, fn func(*Box)) {
	fn(root)
	for _, c := range root.Children {
		Walk(c, fn)
	}
}

// U8/U16/U24/U32/U64 提供从 data 中读取大端无符号整数的小工具。
func U8(data []byte) uint8   { return data[0] }
func U16(data []byte) uint16 { return binary.BigEndian.Uint16(data) }
func U24(data []byte) uint32 { return uint32(data[0])<<16 | uint32(data[1])<<8 | uint32(data[2]) }
func U32(data []byte) uint32 { return binary.BigEndian.Uint32(data) }
func U64(data []byte) uint64 { return binary.BigEndian.Uint64(data) }
func BE(data []byte) uint64 {
	return binary.BigEndian.Uint64(append(make([]byte, 8-len(data)), data...))
}

// BoxPayload 返回盒子的裸数据区字节（不含盒子头；若为 FullBox 则含 version+flags）。
func BoxPayload(data []byte, b *Box) []byte {
	return data[b.DataStart : b.Start+b.Size]
}

// FullBoxPayload 返回 FullBox 去掉 version+flags 之后的数据。
func FullBoxPayload(data []byte, b *Box) []byte {
	return data[b.DataStart+4 : b.Start+b.Size]
}
