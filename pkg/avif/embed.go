package avif

import (
	"errors"
	"fmt"

	"aviflph/pkg/bmff"
	"aviflph/pkg/exif"
)

// 本文件实现 Live Photo AVIF 的封装（Embed）：
//   静态图 AVIF + 视频 MP4  →  "xomu" Live Photo AVIF
//
// 生成的文件结构：
//   ftyp  (brands: avif, mif1, miaf, xomu)
//   meta
//     hdlr(pict)
//     pitm(1)                        -- 静态图为主条目
//     iinf: infe(1,av01) infe(2,xomu) infe(3,Exif)
//     iloc: item1->静态图, item2->视频, item3->EXIF（绝对文件偏移）
//     iref: cdsc(1->3) auxl(2->1)
//     iprp: ipco(属性) + ipma(item1->属性)
//   mdat: [静态图AV1数据][视频MP4数据][EXIF数据]

// EmbedInput 是封装操作的输入。
type EmbedInput struct {
	// StillData 是已编码的静态图 AVIF 文件字节
	StillData []byte
	// VideoData 是待嵌入的 MP4 视频字节
	VideoData []byte
	// Exif 是待写入的 EXIF 选项；为 nil 时使用默认（XOMUPhoto=1）
	Exif *exif.Options
	// Opt 是封装选项；为 nil 时使用默认值
	Opt *Options
	// 若提供，则完全替换源 AVIF 的 EXIF（从原始 JPEG 中提取的信息）
	SourceExif *exif.DataSet
}

// Embed 执行封装，返回生成的 Live Photo AVIF 字节。
func Embed(in EmbedInput) ([]byte, error) {
	if len(in.StillData) == 0 || len(in.VideoData) == 0 {
		return nil, errors.New("avif: still and video data required")
	}
	opt := DefaultOptions()
	if in.Opt != nil {
		opt = *in.Opt
	}
	exifOpt := exif.DefaultOptions()
	if in.Exif != nil {
		exifOpt = *in.Exif
	}
	// 若提供了源 EXIF 数据，优先原样拷贝完整 TIFF（含缩略图等），
	// 否则拷贝其关键字段
	var exifRaw []byte
	if in.SourceExif != nil {
		if len(in.SourceExif.Raw) > 0 {
			exifRaw = in.SourceExif.Raw
		} else {
			applySourceExif(&exifOpt, in.SourceExif)
		}
	}

	// 1. 解析源静态图 AVIF
	src, err := ParseFile(in.StillData)
	if err != nil {
		return nil, fmt.Errorf("avif: parse still: %w", err)
	}
	still, err := src.PrimaryItemData()
	if err != nil {
		return nil, fmt.Errorf("avif: read still data: %w", err)
	}

	// 2. 提取静态图的属性（ipco 按序复制，ipma 记录 item1 的关联）
	props, propIndices, propEssential := src.propertyList()

	// 3. 生成 EXIF 条目数据（"Exif\0\0" + TIFF）
	tiff := exif.Build(exifOpt)
	if exifRaw != nil {
		tiff = exifRaw
	}
	exifItemData := bmff.BuildExifItem(tiff)

	// 4. 组装盒子
	//    先决定 iloc 是否需要 64 位（总数据量接近 4GB 时）
	totalData := uint64(len(still)) + uint64(len(in.VideoData)) + uint64(len(exifItemData))
	opt.Force64BitIloc = opt.Force64BitIloc || totalData >= 1<<32

	// 构造 meta（临时，先计算尺寸）
	metaBytes := buildMeta(still, in.VideoData, exifItemData, props, propIndices, propEssential, opt)

	// 5. 组装最终文件
	ftyp := buildFtyp(opt)
	fileSize := len(ftyp) + len(metaBytes) + 8 + int(totalData)
	out := make([]byte, 0, fileSize)
	out = append(out, ftyp...)
	out = append(out, metaBytes...)

	// mdat 头
	mdat := make([]byte, 8)
	mdatSize := uint32(8 + int(totalData))
	mdat[0] = byte(mdatSize >> 24)
	mdat[1] = byte(mdatSize >> 16)
	mdat[2] = byte(mdatSize >> 8)
	mdat[3] = byte(mdatSize)
	copy(mdat[4:8], "mdat")
	out = append(out, mdat...)
	// 数据区（图像 + EXIF + 视频）
	out = append(out, still...)
	out = append(out, exifItemData...)
	out = append(out, in.VideoData...)
	return out, nil
}

// buildMeta 组装 meta 盒子。
// 由于 iloc 中的偏移依赖 meta 大小，这里先按占位偏移计算 meta 尺寸，
// 再以真实偏移重新构建。
func buildMeta(still, video, exifItem []byte, props [][]byte, propIndices []uint16, propEssential []bool, opt Options) []byte {
	// 辅助：给定数据偏移后构建 meta
	build := func(stillOff, videoOff, exifOff uint64) []byte {
		// iinf
		iinf := bmff.BuildIinf([]bmff.InfeEntry{
			{ID: ItemIDStill, Type: "av01", Name: stillItemName},
			{ID: ItemIDExif, Type: "Exif", Name: ""},
			{ID: ItemIDVideo, Type: opt.VideoItemType, Name: opt.VideoItemName},
		})
		// iloc
		iloc := bmff.BuildIloc([]bmff.IlocEntry{
			{ID: ItemIDStill, Offset: stillOff, Length: uint64(len(still))},
			{ID: ItemIDExif, Offset: exifOff, Length: uint64(len(exifItem))},
			{ID: ItemIDVideo, Offset: videoOff, Length: uint64(len(video))},
		})
		// iref: 与 cproject 一致 —— EXIF(item 2) 描述图像(item 1)
		refs := []bmff.RefEntry{{Type: bmff.TypeCdsc, FromID: ItemIDExif, ToIDs: []uint32{ItemIDStill}}}
		iref := bmff.BuildIref(refs)
		// iprp
		iprp := bmff.BuildIprp(props, []bmff.IpmaEntry{
			{ItemID: ItemIDStill, PropertyIndices: propIndices, Essential: propEssential},
		})
		children := bmff.JoinBoxes(
			bmff.BuildHdlr("pict"),
			bmff.BuildPitm(ItemIDStill),
			iloc,
			iinf,
			iref,
			iprp,
		)
		return bmff.BuildFullBox("meta", 0, 0, children)
	}

	// 占位构建，获取 meta 尺寸
	placeholder := build(0, 0, 0)
	ftyp := buildFtyp(opt)
	mdatStart := uint64(len(ftyp) + len(placeholder) + 8)

	// 真实偏移（数据区顺序与 cproject 一致：图像 + EXIF + 视频）
	stillOff := mdatStart
	exifOff := stillOff + uint64(len(still))
	videoOff := exifOff + uint64(len(exifItem))

	// 重建（注意：若开启 64 位，meta 尺寸与 32 位一致，偏移不变）
	real := build(stillOff, videoOff, exifOff)
	return real
}

// buildFtyp 构建 ftyp 盒子。
func buildFtyp(opt Options) []byte {
	brands := []string{"mif1", "avif", "miaf"}
	if opt.AddXomuBrand {
		brands = append(brands, bmff.TypeXomu)
	}
	return bmff.BuildFtyp("avif", brands)
}

// propertyList 从源 AVIF 提取属性列表与主条目的属性关联。
// 返回 (属性盒子列表, 属性下标, 是否 essential)。
func (p *ParsedFile) propertyList() ([][]byte, []uint16, []bool) {
	var props [][]byte
	var indices []uint16
	var essential []bool
	if p.Items == nil {
		return props, indices, essential
	}
	// 按顺序收集所有属性
	for _, prop := range p.Items.Properties {
		props = append(props, prop.Payload)
	}
	// 主条目的属性关联
	primaryID := p.Items.PrimaryItemID
	if primaryID == 0 && len(p.Items.Items) > 0 {
		primaryID = p.Items.Items[0].ID
	}
	if idxs, ok := p.Items.ItemProps[primaryID]; ok {
		for i, idx := range idxs {
			indices = append(indices, uint16(idx))
			ess := false
			if p.Items.ItemPropsEssential[primaryID] != nil && i < len(p.Items.ItemPropsEssential[primaryID]) {
				ess = p.Items.ItemPropsEssential[primaryID][i]
			}
			essential = append(essential, ess)
		}
	}
	return props, indices, essential
}

// applySourceExif 把源 JPEG/AVIF 的 EXIF 关键字段复制到构建选项。
func applySourceExif(opt *exif.Options, src *exif.DataSet) {
	if src == nil {
		return
	}
	if t := src.Get("ifd0", exif.TagMake); t != nil {
		opt.Make = t.TagString(src.ByteOrder)
	}
	if t := src.Get("ifd0", exif.TagModel); t != nil {
		opt.Model = t.TagString(src.ByteOrder)
	}
	if t := src.Get("ifd0", exif.TagOrientation); t != nil {
		if v, err := parseUint(t.TagString(src.ByteOrder)); err == nil {
			opt.Orientation = uint16(v)
		}
	}
	if t := src.Get("ifd0", exif.TagDateTime); t != nil {
		opt.DateTime = t.TagString(src.ByteOrder)
	}
	if t := src.Get("exif", exif.TagDateTimeOriginal); t != nil {
		opt.DateTimeOriginal = t.TagString(src.ByteOrder)
	}
}

// parseUint 简单整数解析。
func parseUint(s string) (uint64, error) {
	var v uint64
	var err error
	for _, c := range s {
		if c < '0' || c > '9' {
			err = errors.New("not a number")
			break
		}
		v = v*10 + uint64(c-'0')
	}
	return v, err
}

// EmbedJPEGVideo 从 JPEG 静态图 + MP4 视频直接构建 Live Photo AVIF，
// 不需要 AVIF 编码器。JPEG 数据作为 primary item 原样嵌入。
func EmbedJPEGVideo(jpegData, videoMP4 []byte, exifOpts *exif.Options) ([]byte, error) {
	if len(jpegData) == 0 || len(videoMP4) == 0 {
		return nil, errors.New("avif: jpeg and video data required")
	}

	opt := DefaultOptions()
	opt.AddXomuBrand = true

	// EXIF
	exifOpt := exif.DefaultOptions()
	if exifOpts != nil {
		exifOpt = *exifOpts
	}
	tiff := exif.Build(exifOpt)
	exifItemData := bmff.BuildExifItem(tiff)

	// 构建 meta
	// iinf: item1=JPEG, item2=Exif, item3=Video(xomu)
	iinf := bmff.BuildIinf([]bmff.InfeEntry{
		{ID: ItemIDStill, Type: "jpeg", Name: stillItemName},
		{ID: ItemIDExif, Type: "Exif", Name: ""},
		{ID: ItemIDVideo, Type: opt.VideoItemType, Name: opt.VideoItemName},
	})

	// iloc 偏移计算
	ftyp := buildFtyp(opt)
	// meta 用占位偏移算大小
 placeholderMeta := func() []byte {
		iloc := bmff.BuildIloc([]bmff.IlocEntry{
			{ID: ItemIDStill, Offset: 0, Length: uint64(len(jpegData))},
			{ID: ItemIDExif, Offset: 0, Length: uint64(len(exifItemData))},
			{ID: ItemIDVideo, Offset: 0, Length: uint64(len(videoMP4))},
		})
		iref := bmff.BuildIref([]bmff.RefEntry{
			{Type: bmff.TypeCdsc, FromID: ItemIDExif, ToIDs: []uint32{ItemIDStill}},
		})
		// 最小 iprp: ispe (宽高) + ispe for grid? 简化：空 iprp
		children := bmff.JoinBoxes(
			bmff.BuildHdlr("pict"),
			bmff.BuildPitm(ItemIDStill),
			iloc,
			iinf,
			iref,
		)
		return bmff.BuildFullBox("meta", 0, 0, children)
	}()
	mdatStart := uint64(len(ftyp) + len(placeholderMeta) + 8)
	stillOff := mdatStart
	exifOff := stillOff + uint64(len(jpegData))
	videoOff := exifOff + uint64(len(exifItemData))

	// 用真实偏移重建 meta
	iloc := bmff.BuildIloc([]bmff.IlocEntry{
		{ID: ItemIDStill, Offset: stillOff, Length: uint64(len(jpegData))},
		{ID: ItemIDExif, Offset: exifOff, Length: uint64(len(exifItemData))},
		{ID: ItemIDVideo, Offset: videoOff, Length: uint64(len(videoMP4))},
	})
	iref := bmff.BuildIref([]bmff.RefEntry{
		{Type: bmff.TypeCdsc, FromID: ItemIDExif, ToIDs: []uint32{ItemIDStill}},
	})
	children := bmff.JoinBoxes(
		bmff.BuildHdlr("pict"),
		bmff.BuildPitm(ItemIDStill),
		iloc,
		iinf,
		iref,
	)
	metaBytes := bmff.BuildFullBox("meta", 0, 0, children)

	// 组装文件
	totalData := len(jpegData) + len(exifItemData) + len(videoMP4)
	out := make([]byte, 0, len(ftyp)+len(metaBytes)+8+totalData)
	out = append(out, ftyp...)
	out = append(out, metaBytes...)

	// mdat
	mdatSize := uint32(8 + totalData)
	mdat := make([]byte, 8)
	mdat[0] = byte(mdatSize >> 24)
	mdat[1] = byte(mdatSize >> 16)
	mdat[2] = byte(mdatSize >> 8)
	mdat[3] = byte(mdatSize)
	copy(mdat[4:8], "mdat")
	out = append(out, mdat...)
	out = append(out, jpegData...)
	out = append(out, exifItemData...)
	out = append(out, videoMP4...)
	return out, nil
}
