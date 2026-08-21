package avif

import (
	"errors"

	"aviflph/pkg/bmff"
)

// 本文件实现 Live Photo AVIF 的分解（Demux）：
//   把 "xomu" Live Photo AVIF 还原为 静态图 AVIF + 视频 MP4。

// DemuxResult 是分解结果。
type DemuxResult struct {
	StillAVIF []byte // 静态图（重建为单图 AVIF 文件）
	Video     []byte // 视频（MP4 字节）
	VideoType string // 视频条目类型（通常为 xomu）
	ExifTIFF  []byte // 若存在 EXIF 条目，返回其 TIFF 块
}

// Demux 解析一个 Live Photo AVIF，分离静态图与视频。
// 静态图会重建为一个独立的单图 AVIF 文件（含其原有属性与 EXIF）。
func Demux(data []byte) (*DemuxResult, error) {
	p, err := ParseFile(data)
	if err != nil {
		return nil, err
	}
	if !p.IsLivePhoto() {
		return nil, errors.New("avif: not a live photo (no xomu item)")
	}

	// 视频
	video, err := p.VideoData()
	if err != nil {
		return nil, err
	}
	videoType := bmff.TypeXomu
	for _, it := range p.Items.Items {
		if it.Type == bmff.TypeXomu {
			videoType = it.Type
			break
		}
	}

	// 静态图
	still, err := p.PrimaryItemData()
	if err != nil {
		return nil, err
	}

	// EXIF（可选）
	var tiff []byte
	if exifRaw, err := p.ExifData(); err == nil {
		tiff = exifRaw
	}

	// 重建单图 AVIF
	props, indices, essential := p.propertyList()
	stillAVIF, err := buildSingleImageFile(still, tiff, props, indices, essential)
	if err != nil {
		return nil, err
	}

	return &DemuxResult{
		StillAVIF: stillAVIF,
		Video:     video,
		VideoType: videoType,
		ExifTIFF:  tiff,
	}, nil
}

// buildSingleImageFile 把一条图片数据重建为可独立解码的单图 AVIF 文件。
// props/indices/essential 描述图片条目的属性集合。
func buildSingleImageFile(still []byte, tiff []byte, props [][]byte, indices []uint16, essential []bool) ([]byte, error) {
	// 条目数据：静态图 + 可选 EXIF
	var exifItemData []byte
	if len(tiff) > 0 {
		exifItemData = bmff.BuildExifItem(tiff)
	}

	// 构建 meta：先占位计算尺寸
	buildMeta := func(stillOff, exifOff uint64) []byte {
		var iinf bmff.InfeEntry
		var ilocs []bmff.IlocEntry
		var refs []bmff.RefEntry
		var infs []bmff.InfeEntry

		infs = append(infs, bmff.InfeEntry{ID: ItemIDStill, Type: "av01", Name: stillItemName})
		ilocs = append(ilocs, bmff.IlocEntry{ID: ItemIDStill, Offset: stillOff, Length: uint64(len(still))})
		if len(exifItemData) > 0 {
			infs = append(infs, bmff.InfeEntry{ID: ItemIDExif, Type: "Exif", Name: ""})
			ilocs = append(ilocs, bmff.IlocEntry{ID: ItemIDExif, Offset: exifOff, Length: uint64(len(exifItemData))})
			refs = append(refs, bmff.RefEntry{Type: bmff.TypeCdsc, FromID: ItemIDStill, ToIDs: []uint32{ItemIDExif}})
		}
		_ = iinf
		iinfBox := bmff.BuildIinf(infs)
		ilocBox := bmff.BuildIloc(ilocs)
		iprpBox := bmff.BuildIprp(props, []bmff.IpmaEntry{{ItemID: ItemIDStill, PropertyIndices: indices, Essential: essential}})
		parts := [][]byte{
			bmff.BuildHdlr("pict"),
			bmff.BuildPitm(ItemIDStill),
			iinfBox,
			ilocBox,
		}
		if len(refs) > 0 {
			parts = append(parts, bmff.BuildIref(refs))
		}
		parts = append(parts, iprpBox)
		return bmff.BuildFullBox("meta", 0, 0, bmff.JoinBoxes(parts...))
	}

	placeholder := buildMeta(0, 0)
	ftyp := bmff.BuildFtyp("avif", []string{"mif1", "avif", "miaf"})
	dataStart := uint64(len(ftyp) + len(placeholder) + 8)
	stillOff := dataStart
	exifOff := stillOff + uint64(len(still))
	meta := buildMeta(stillOff, exifOff)

	// 组装文件
	out := make([]byte, 0, len(ftyp)+len(meta)+8+int(uint64(len(still))+uint64(len(exifItemData))))
	out = append(out, ftyp...)
	out = append(out, meta...)
	mdatSize := 8 + len(still) + len(exifItemData)
	mdat := make([]byte, 8)
	mdat[0] = byte(mdatSize >> 24)
	mdat[1] = byte(mdatSize >> 16)
	mdat[2] = byte(mdatSize >> 8)
	mdat[3] = byte(mdatSize)
	copy(mdat[4:8], "mdat")
	out = append(out, mdat...)
	out = append(out, still...)
	out = append(out, exifItemData...)
	return out, nil
}

// ExtractStillFromAVIF 从任意 AVIF 中提取主图像数据（不重建文件）。
func ExtractStillFromAVIF(data []byte) ([]byte, error) {
	p, err := ParseFile(data)
	if err != nil {
		return nil, err
	}
	return p.PrimaryItemData()
}

// ExtractVideoFromAVIF 从 Live Photo AVIF 中提取视频字节。
func ExtractVideoFromAVIF(data []byte) ([]byte, error) {
	p, err := ParseFile(data)
	if err != nil {
		return nil, err
	}
	return p.VideoData()
}
