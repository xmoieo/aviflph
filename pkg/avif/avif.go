// Package avif 实现 AVIF Live Photo（动态照片）的封装与分解。
//
// 核心能力：
//  1. Embed：把一张 AVIF 静态图 + 一段 MP4 视频，封装为标准的
//     "AVIF + xomu" Live Photo 文件：
//     - ftyp 增加 xomu 兼容品牌
//     - meta 的 iinf/iloc 中增加一个 item_type=xomu 的条目指向视频
//     - 视频字节追加在 mdat 中（静态图数据之后）
//     - 插入 Exif 条目并设置 XOMUPhoto=1
//  2. Demux：从 Live Photo AVIF 中分离静态图与视频。
//  3. 读取 AVIF 的条目/属性/EXIF 信息（供 getmeta 使用）。
package avif

import (
	"errors"
	"fmt"

	"aviflph/pkg/bmff"
	"aviflph/pkg/exif"
)

// 常量定义。
const (
	// 默认 item id 分配（与 cproject 兼容：1=图像, 2=EXIF, 3=xomu 视频）
	ItemIDStill = 1 // 静态图（av01）
	ItemIDExif  = 2 // EXIF 条目
	ItemIDVideo = 3 // 视频（xomu）
)

// Options 是封装 Live Photo 的参数。
type Options struct {
	// 视频条目的 item_type 名称（默认 xomu）
	VideoItemType string
	// 视频条目名称（默认 XOMU）
	VideoItemName string
	// 是否在 ftyp 中增加 xomu 品牌（默认 true）
	AddXomuBrand bool
	// 是否增加 auxl 引用（视频→图片）（默认 true）
	AddAuxRef bool // 已弃用：iref 现与 cproject 一致（EXIF cdsc 图像）
	// 视频在 mdat 中的排列方式：当前固定为直接追加
	// 是否输出 64 位 iloc（默认自动判断）
	Force64BitIloc bool
}

// DefaultOptions 返回默认封装参数。
func DefaultOptions() Options {
	return Options{
		VideoItemType: bmff.TypeXomu,
		VideoItemName: "XOMU",
		AddXomuBrand:  true,
		AddAuxRef:     true,
	}
}

// stillItemName 静态图条目名称（符合 AVIF 惯例）。
const stillItemName = "Color"

// ParsedFile 表示一个已解析的 AVIF 文件（供 embed/getmeta 使用）。
type ParsedFile struct {
	Data      []byte
	Boxes     []*bmff.Box
	Meta      *bmff.Box
	Mdat      *bmff.Box
	Items     *bmff.MetaItems
	FtypMajor string
	Brands    []string
}

// ParseFile 解析一个 AVIF 文件的顶层结构。
func ParseFile(data []byte) (*ParsedFile, error) {
	if len(data) < 12 || string(data[4:8]) != "ftyp" {
		return nil, errors.New("avif: not a bmff file (no ftyp)")
	}
	boxes, err := bmff.Parse(data, 0, int64(len(data)), 0)
	if err != nil {
		return nil, err
	}
	p := &ParsedFile{Data: data, Boxes: boxes}
	if ftyp := bmff.Find(boxes, bmff.TypeFtyp); ftyp != nil {
		raw := data[ftyp.DataStart : ftyp.Start+ftyp.Size]
		if len(raw) >= 8 {
			p.FtypMajor = string(raw[0:4])
			for i := 8; i+4 <= len(raw); i += 4 {
				p.Brands = append(p.Brands, string(raw[i:i+4]))
			}
		}
	}
	p.Meta = bmff.Find(boxes, bmff.TypeMeta)
	p.Mdat = bmff.Find(boxes, bmff.TypeMdat)
	if p.Meta != nil {
		items, err := bmff.ParseMetaItems(data, p.Meta)
		if err != nil {
			return nil, err
		}
		p.Items = items
	}
	return p, nil
}

// ItemLocationByType 返回指定 item_type 的位置。
func (p *ParsedFile) ItemLocationByType(typ string) (*bmff.ItemLocation, bool) {
	if p.Items == nil {
		return nil, false
	}
	for _, it := range p.Items.Items {
		if it.Type == typ {
			for i := range p.Items.Locations {
				if p.Items.Locations[i].ID == it.ID {
					return &p.Items.Locations[i], true
				}
			}
		}
	}
	return nil, false
}

// ItemLocationByID 返回指定 item id 的位置。
func (p *ParsedFile) ItemLocationByID(id uint32) (*bmff.ItemLocation, bool) {
	if p.Items == nil {
		return nil, false
	}
	for i := range p.Items.Locations {
		if p.Items.Locations[i].ID == id {
			return &p.Items.Locations[i], true
		}
	}
	return nil, false
}

// PrimaryItemData 返回主条目（静态图）的原始数据字节。
func (p *ParsedFile) PrimaryItemData() ([]byte, error) {
	if p.Items == nil {
		return nil, errors.New("avif: no meta")
	}
	id := p.Items.PrimaryItemID
	if id == 0 {
		// 没有 pitm 时取第一个条目
		if len(p.Items.Items) > 0 {
			id = p.Items.Items[0].ID
		}
	}
	loc, ok := p.ItemLocationByID(id)
	if !ok {
		return nil, fmt.Errorf("avif: primary item %d has no location", id)
	}
	return bmff.ItemData(p.Data, *loc)
}

// VideoData 返回视频（xomu 条目）的字节。
func (p *ParsedFile) VideoData() ([]byte, error) {
	loc, ok := p.ItemLocationByType(bmff.TypeXomu)
	if !ok {
		// 兼容：视频条目也可能是 mime 或直接是最后一个大条目
		return nil, errors.New("avif: no xomu video item found")
	}
	return bmff.ItemData(p.Data, *loc)
}

// ExifData 返回 Exif 条目的 TIFF 块（自动去除 "Exif\0\0" 前缀）。
func (p *ParsedFile) ExifData() ([]byte, error) {
	if p.Items == nil {
		return nil, errors.New("avif: no meta")
	}
	for _, it := range p.Items.Items {
		if it.Type == "Exif" {
			loc, ok := p.ItemLocationByID(it.ID)
			if !ok {
				continue
			}
			data, err := bmff.ItemData(p.Data, *loc)
			if err != nil {
				return nil, err
			}
			// 跳过 "Exif\0\0" 前缀
			if len(data) >= 6 && string(data[:4]) == "Exif" {
				data = data[6:]
			}
			return data, nil
		}
	}
	return nil, errors.New("avif: no exif item")
}

// ParseExif 解析 AVIF 中的 EXIF 数据。
func (p *ParsedFile) ParseExif() (*exif.DataSet, error) {
	raw, err := p.ExifData()
	if err != nil {
		return nil, err
	}
	return exif.Parse(raw)
}

// IsLivePhoto 判断 AVIF 是否为 Live Photo（存在 xomu 视频条目）。
func (p *ParsedFile) IsLivePhoto() bool {
	if p.Items == nil {
		return false
	}
	for _, it := range p.Items.Items {
		if it.Type == bmff.TypeXomu {
			return true
		}
	}
	return false
}
