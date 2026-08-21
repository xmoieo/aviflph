// Package getmeta 提供文件 meta 信息查看功能（CLI 的 getmeta 子命令与库 API）。
//
// 支持三种输入：
//  1. AVIF：显示盒子树、条目(iinf/iloc/iref/iprp)、EXIF、是否 Live Photo。
//  2. JPEG Motion Photo：显示 JPEG 结构、XMP 中的动态照片信息、内嵌视频位置。
//  3. MP4：显示 moov 轨道信息（编码/分辨率/时长）。
//
// 输出支持文本（默认）与 JSON 两种格式，便于程序解析。
package getmeta

import (
	"encoding/json"
	"fmt"
	"strings"

	"aviflph/pkg/avif"
	"aviflph/pkg/bmff"
	"aviflph/pkg/exif"
	"aviflph/pkg/motionphoto"
)

// FileType 是识别的文件类型。
type FileType string

const (
	TypeAVIF  FileType = "avif"
	TypeJPEG  FileType = "jpeg"
	TypeMP4   FileType = "mp4"
	TypeOther FileType = "other"
)

// Report 是 getmeta 的完整报告。
type Report struct {
	FileType    FileType  `json:"file_type"`
	Size        int64     `json:"size"`
	MotionPhoto *MPInfo   `json:"motion_photo,omitempty"`
	Avif        *AVIFInfo `json:"avif,omitempty"`
	MP4         *MP4Info  `json:"mp4,omitempty"`
	BoxTree     string    `json:"-"` // 文本形式的盒子树
}

// MPInfo 是 JPEG Motion Photo 的信息。
type MPInfo struct {
	IsMotionPhoto bool   `json:"is_motion_photo"`
	VideoOffset   int64  `json:"video_offset"`
	VideoLength   int64  `json:"video_length"`
	VideoPadding  int64  `json:"video_padding"`
	VideoMime     string `json:"video_mime"`
}

// AVIFInfo 是 AVIF 的 meta 信息。
type AVIFInfo struct {
	MajorBrand    string        `json:"major_brand"`
	Brands        []string      `json:"brands"`
	IsLivePhoto   bool          `json:"is_live_photo"`
	PrimaryItemID uint32        `json:"primary_item_id"`
	Items         []ItemInfo    `json:"items"`
	Refs          []RefInfo     `json:"refs,omitempty"`
	Exif          []ExifTagInfo `json:"exif,omitempty"`
	HasXOMUPhoto  bool          `json:"xomu_photo"`
	XOMUPhotoVal  uint64        `json:"xomu_photo_value"`
}

// ItemInfo 是条目的概要信息。
type ItemInfo struct {
	ID     uint32   `json:"id"`
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Offset int64    `json:"offset"`
	Length int64    `json:"length"`
	Props  []string `json:"props,omitempty"`
}

// RefInfo 是引用信息。
type RefInfo struct {
	Type string   `json:"type"`
	From uint32   `json:"from"`
	To   []uint32 `json:"to"`
}

// ExifTagInfo 是 EXIF 标签信息。
type ExifTagInfo struct {
	Ifd   string `json:"ifd"`
	ID    uint16 `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MP4Info 是 MP4 轨道信息。
type MP4Info struct {
	Tracks []TrackInfo `json:"tracks"`
}

// TrackInfo 是单个轨道信息。
type TrackInfo struct {
	Index        int     `json:"index"`
	Codec        string  `json:"codec"`
	Type         string  `json:"type"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	DurationSecs float64 `json:"duration_secs,omitempty"`
}

// Summarize 分析文件字节并生成报告。
func Summarize(data []byte) (*Report, error) {
	r := &Report{Size: int64(len(data))}

	switch {
	case motionphoto.IsAVIF(data):
		r.FileType = TypeAVIF
		ai, err := summarizeAVIF(data)
		if err != nil {
			return nil, err
		}
		r.Avif = ai
	case motionphoto.IsMP4(data):
		r.FileType = TypeMP4
		r.MP4 = summarizeMP4(data)
	case motionphoto.IsJPEG(data):
		r.FileType = TypeJPEG
		mp, err := summarizeJPEG(data)
		if err == nil {
			r.MotionPhoto = mp
		}
	default:
		r.FileType = TypeOther
	}
	// 盒子树（文本形式）在格式化时生成
	return r, nil
}

// summarizeAVIF 分析 AVIF 文件。
func summarizeAVIF(data []byte) (*AVIFInfo, error) {
	p, err := avif.ParseFile(data)
	if err != nil {
		return nil, err
	}
	ai := &AVIFInfo{
		MajorBrand:    p.FtypMajor,
		Brands:        p.Brands,
		IsLivePhoto:   p.IsLivePhoto(),
		PrimaryItemID: p.Items.PrimaryItemID,
	}
	// 条目
	for _, it := range p.Items.Items {
		info := ItemInfo{ID: it.ID, Type: it.Type, Name: it.Name}
		if loc, ok := p.ItemLocationByID(it.ID); ok {
			info.Offset = loc.Offset
			info.Length = loc.Length
		}
		if idxs, ok := p.Items.ItemProps[it.ID]; ok {
			for _, idx := range idxs {
				for _, prop := range p.Items.Properties {
					if prop.Index == idx {
						info.Props = append(info.Props, prop.Type)
					}
				}
			}
		}
		ai.Items = append(ai.Items, info)
	}
	// 引用
	for _, ref := range p.Items.Refs {
		ai.Refs = append(ai.Refs, RefInfo{Type: ref.Type, From: ref.FromID, To: ref.ToIDs})
	}
	// EXIF
	if ds, err := p.ParseExif(); err == nil {
		ai.Exif = dumpExif(ds)
		if t := ds.Get("ifd0", exif.TagXOMUPhoto); t != nil {
			ai.HasXOMUPhoto = true
			ai.XOMUPhotoVal = uint64(ds.ByteOrder.Uint16(t.Value))
		}
	}
	return ai, nil
}

// dumpExif 把 EXIF 数据转换为可读标签列表。
func dumpExif(ds *exif.DataSet) []ExifTagInfo {
	var out []ExifTagInfo
	add := func(ifd string, tags []exif.Tag) {
		for _, t := range tags {
			out = append(out, ExifTagInfo{
				Ifd:   ifd,
				ID:    t.ID,
				Name:  exif.TagName(t.ID),
				Value: t.TagString(ds.ByteOrder),
			})
		}
	}
	add("IFD0", ds.IFD0)
	add("Exif", ds.Exif)
	add("GPS", ds.GPS)
	return out
}

// summarizeMP4 分析 MP4 文件的轨道信息。
func summarizeMP4(data []byte) *MP4Info {
	info := &MP4Info{}
	boxes, err := bmff.Parse(data, 0, int64(len(data)), 0)
	if err != nil {
		return info
	}
	moov := bmff.Find(boxes, bmff.TypeMoov)
	if moov == nil {
		return info
	}
	// 收集轨道
	index := 0
	var walk func(*bmff.Box)
	walk = func(b *bmff.Box) {
		if b.Type == bmff.TypeTrak {
			track := parseTrack(data, b, index)
			info.Tracks = append(info.Tracks, track)
			index++
		}
		for _, c := range b.Children {
			walk(c)
		}
	}
	walk(moov)
	return info
}

// parseTrack 解析一个轨道（hdlr + stsd）。
func parseTrack(data []byte, trak *bmff.Box, index int) TrackInfo {
	t := TrackInfo{Index: index}
	var walk func(*bmff.Box)
	walk = func(b *bmff.Box) {
		switch b.Type {
		case "hdlr":
			if b.DataStart+12 <= b.Start+b.Size {
				t.Type = string(data[b.DataStart+8 : b.DataStart+12])
			}
		case "stsd":
			// 第一个 sample entry 的 4CC 就是编码类型
			for _, c := range b.Children {
				if c.Type == bmff.TypeHdlr {
					continue
				}
				// 跳过 fullbox 头，读取 sample entry type
				raw := data[c.DataStart : c.Start+c.Size]
				if len(raw) >= 8 {
					t.Codec = string(raw[0:4])
					// 视频条目：解析宽高（第 24-31 字节，sample entry 布局）
					if t.Type == "vide" && len(raw) >= 32 {
						t.Width = int(bmff.U16(raw[24:26]))
						t.Height = int(bmff.U16(raw[26:28]))
					}
				}
				break
			}
		case "mdhd":
			if b.DataStart+12 <= b.Start+b.Size {
				// mdhd: version+flags(4) + creation(4) + modification(4) + timescale(4) + duration(4/8)
				ver := data[b.DataStart]
				var duration, timescale uint64
				if ver == 1 {
					timescale = uint64(bmff.U32(data[b.DataStart+16 : b.DataStart+20]))
					duration = bmff.U64(data[b.DataStart+20 : b.DataStart+28])
				} else {
					timescale = uint64(bmff.U32(data[b.DataStart+12 : b.DataStart+16]))
					duration = uint64(bmff.U32(data[b.DataStart+16 : b.DataStart+20]))
				}
				if timescale > 0 {
					t.DurationSecs = float64(duration) / float64(timescale)
				}
			}
		}
		for _, c := range b.Children {
			walk(c)
		}
	}
	walk(trak)
	return t
}

// summarizeJPEG 分析 JPEG Motion Photo。
func summarizeJPEG(data []byte) (*MPInfo, error) {
	info, err := motionphoto.Detect(data)
	if err != nil {
		return nil, err
	}
	return &MPInfo{
		IsMotionPhoto: info.IsMotionPhoto,
		VideoOffset:   info.VideoOffset,
		VideoLength:   info.VideoLength,
		VideoPadding:  info.VideoPadding,
		VideoMime:     info.VideoMime,
	}, nil
}

// Text 生成人类可读的报告文本。
func (r *Report) Text() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("文件类型: %s\n", r.FileType))
	sb.WriteString(fmt.Sprintf("文件大小: %d 字节 (%.1f MB)\n", r.Size, float64(r.Size)/1048576))

	if r.MotionPhoto != nil {
		sb.WriteString("\n[Motion Photo]\n")
		mp := r.MotionPhoto
		if mp.IsMotionPhoto {
			sb.WriteString("  是动态照片（Motion Photo）\n")
			sb.WriteString(fmt.Sprintf("  视频偏移: %d\n", mp.VideoOffset))
			sb.WriteString(fmt.Sprintf("  视频长度: %d 字节\n", mp.VideoLength))
			sb.WriteString(fmt.Sprintf("  视频 MIME: %s\n", mp.VideoMime))
			if mp.VideoPadding > 0 {
				sb.WriteString(fmt.Sprintf("  视频后填充: %d\n", mp.VideoPadding))
			}
		} else {
			sb.WriteString("  普通 JPEG（无内嵌视频）\n")
		}
	}

	if r.Avif != nil {
		a := r.Avif
		sb.WriteString("\n[AVIF]\n")
		sb.WriteString(fmt.Sprintf("  主品牌: %s\n", a.MajorBrand))
		sb.WriteString(fmt.Sprintf("  兼容品牌: %s\n", strings.Join(a.Brands, ", ")))
		if a.IsLivePhoto {
			sb.WriteString("  类型: Live Photo（含 xomu 视频条目）\n")
		} else {
			sb.WriteString("  类型: 静态图\n")
		}
		sb.WriteString(fmt.Sprintf("  主条目: %d\n", a.PrimaryItemID))
		sb.WriteString("  条目:\n")
		for _, it := range a.Items {
			sb.WriteString(fmt.Sprintf("    [#%d] %s \"%s\" offset=%d len=%d props=%s\n",
				it.ID, it.Type, it.Name, it.Offset, it.Length, strings.Join(it.Props, ",")))
		}
		if len(a.Refs) > 0 {
			sb.WriteString("  引用:\n")
			for _, ref := range a.Refs {
				sb.WriteString(fmt.Sprintf("    %s %d -> %v\n", ref.Type, ref.From, ref.To))
			}
		}
		if a.HasXOMUPhoto {
			sb.WriteString(fmt.Sprintf("  XOMUPhoto: %d（Live Photo 标记）\n", a.XOMUPhotoVal))
		}
		if len(a.Exif) > 0 {
			sb.WriteString("  EXIF:\n")
			for _, t := range a.Exif {
				sb.WriteString(fmt.Sprintf("    [%s] 0x%04X %s = %s\n", t.Ifd, t.ID, t.Name, t.Value))
			}
		}
	}

	if r.MP4 != nil {
		sb.WriteString("\n[MP4]\n")
		for _, t := range r.MP4.Tracks {
			line := fmt.Sprintf("  轨道[%d] type=%s codec=%s", t.Index, t.Type, t.Codec)
			if t.Width > 0 {
				line += fmt.Sprintf(" %dx%d", t.Width, t.Height)
			}
			if t.DurationSecs > 0 {
				line += fmt.Sprintf(" %.3fs", t.DurationSecs)
			}
			sb.WriteString(line + "\n")
		}
		if len(r.MP4.Tracks) == 0 {
			sb.WriteString("  （未找到轨道信息）\n")
		}
	}
	return sb.String()
}

// JSON 生成 JSON 格式的报告。
func (r *Report) JSON() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BoxTree 生成盒子树的文本描述。
func BoxTree(data []byte) string {
	boxes, err := bmff.Parse(data, 0, int64(len(data)), 0)
	if err != nil {
		return "解析盒子失败: " + err.Error()
	}
	var sb strings.Builder
	var walk func(boxes []*bmff.Box, depth int)
	walk = func(boxes []*bmff.Box, depth int) {
		indent := strings.Repeat("  ", depth)
		for _, b := range boxes {
			size := b.Size
			unit := "B"
			if size >= 1<<20 {
				size = size >> 20
				unit = "MB"
			} else if size >= 1<<10 {
				size = size >> 10
				unit = "KB"
			}
			extra := ""
			if b.IsFullBox {
				extra = fmt.Sprintf(" v=%d", b.Version)
			}
			sb.WriteString(fmt.Sprintf("%s%s size=%d%s @0x%X%s\n", indent, b.Type, size, unit, b.Start, extra))
			if len(b.Children) > 0 {
				walk(b.Children, depth+1)
			}
		}
	}
	walk(boxes, 0)
	return sb.String()
}
