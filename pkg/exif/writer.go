package exif

import (
	"encoding/binary"
	"sort"
)

// 本文件负责构建一个最小可用的 TIFF/EXIF 块（大端序，与大多数相机一致）。
// 生成的块可直接用于：
//   - AVIF 的 Exif 条目（需在块前加 "Exif\x00\x00" 前缀，由调用方处理）
//   - JPEG 的 APP1 段
//
// 默认写入以下标签：
//   - Make / Model / Orientation / 分辨率（若提供）
//   - DateTimeOriginal（若提供）
//   - XOMUPhoto（默认 1，表示这是一张动态照片）
//
// 所有内容可配置，方便扩展。

// Entry 是待写入的一个 IFD 条目（写入前的描述）。
type Entry struct {
	ID    uint16
	Type  uint16
	Value uint64 // 内联整数值（SHORT/LONG）或 RATIONAL 的分子<<32|分母
	Str   string // ASCII 字符串值（用于类型 2）
}

// entryLoc 记录条目在构建过程中的位置信息。
type entryLoc struct {
	entry   Entry
	idx     int // 在 ifd0 或 exifIFD 中的下标
	inline  bool
	dataOff int
}

// Options 控制 EXIF 构建参数。
type Options struct {
	Make             string
	Model            string
	Orientation      uint16
	DateTimeOriginal string
	DateTime         string
	XResolution      uint32 // 默认 300
	YResolution      uint32 // 默认 300
	ResolutionUnit   uint16 // 默认 2（inch）
	// XOMUPhoto 自定义标签
	XOMUPhotoTagID uint16 // 默认 TagXOMUPhoto(0x0002)
	XOMUPhotoValue uint64 // 默认 1
	XOMUPhotoType  uint16 // 默认 TypeShort(3)
	// 是否写入分辨率标签
	WriteResolution bool
	// 是否写入 XOMUPhoto 标签
	WriteXOMU bool
}

// DefaultOptions 返回默认选项。
func DefaultOptions() Options {
	return Options{
		XResolution:     300,
		YResolution:     300,
		ResolutionUnit:  2,
		XOMUPhotoTagID:  TagXOMUPhoto,
		XOMUPhotoValue:  1,
		XOMUPhotoType:   TypeShort,
		WriteResolution: true,
		WriteXOMU:       true,
	}
}

// Build 生成一个 TIFF/EXIF 块（大端序 "MM"）。
// 结构：TIFF 头(8) + IFD0（含指向 Exif IFD 的指针）+ Exif IFD + 数据区。
// 数据区按需分配，所有值先计算偏移，再一次性写入。
func Build(opt Options) []byte {
	order := binary.BigEndian

	// 组装 IFD0 与 Exif IFD 的条目
	ifd0 := []Entry{
		{ID: TagMake, Type: TypeASCII, Str: opt.Make},
		{ID: TagModel, Type: TypeASCII, Str: opt.Model},
		{ID: TagOrientation, Type: TypeShort, Value: uint64(opt.Orientation)},
		{ID: TagDateTime, Type: TypeASCII, Str: opt.DateTime},
		{ID: TagExifIFDPtr, Type: TypeLong, Value: 0}, // 偏移稍后回填
	}
	if opt.WriteResolution {
		// 分辨率写成 RATIONAL（分子=分母=指定值）
		ifd0 = append(ifd0,
			Entry{ID: TagXResolution, Type: TypeRational, Value: uint64(opt.XResolution)<<32 | uint64(opt.XResolution)},
			Entry{ID: TagYResolution, Type: TypeRational, Value: uint64(opt.YResolution)<<32 | uint64(opt.YResolution)},
			Entry{ID: TagResolutionUnit, Type: TypeShort, Value: uint64(opt.ResolutionUnit)},
		)
	}
	if opt.WriteXOMU {
		ifd0 = append(ifd0, Entry{ID: opt.XOMUPhotoTagID, Type: opt.XOMUPhotoType, Value: opt.XOMUPhotoValue})
	}
	exifIFD := []Entry{
		{ID: TagDateTimeOriginal, Type: TypeASCII, Str: opt.DateTimeOriginal},
	}

	// 过滤空字符串
	ifd0 = filterEmpty(ifd0)
	exifIFD = filterEmpty(exifIFD)
	// EXIF 规范要求 IFD 内标签按 ID 升序
	sort.Slice(ifd0, func(i, j int) bool { return ifd0[i].ID < ifd0[j].ID })
	sort.Slice(exifIFD, func(i, j int) bool { return exifIFD[i].ID < exifIFD[j].ID })

	// 计算布局
	// TIFF 头 8 字节
	// IFD0: 2(数量) + 12*N0 + 4(next) = 6 + 12*N0
	// Exif IFD: 6 + 12*N1
	ifd0Bytes := 6 + 12*len(ifd0)
	exifIFDOff := 8 + ifd0Bytes
	exifIFDBytes := 6 + 12*len(exifIFD)
	dataStart := exifIFDOff + exifIFDBytes

	// 先计算每个条目的数据区需求
	dataSize := 0
	calc := func(es []Entry) []entryLoc {
		var locs []entryLoc
		for i, e := range es {
			sz := entryDataSize(e)
			l := entryLoc{entry: e, idx: i}
			if sz > 4 {
				l.dataOff = dataStart + dataSize
				dataSize += sz
			} else {
				l.inline = true
			}
			locs = append(locs, l)
		}
		return locs
	}
	locs0 := calc(ifd0)
	locsExif := calc(exifIFD)

	buf := make([]byte, dataStart+dataSize)
	// TIFF 头
	buf[0], buf[1] = 'M', 'M'
	order.PutUint16(buf[2:4], 42)
	order.PutUint32(buf[4:8], 8)

	// 写入 IFD0
	writeIFD(buf, 8, ifd0, locs0, order)
	// 回填 ExifIFDPtr
	for i, e := range ifd0 {
		if e.ID == TagExifIFDPtr {
			pos := 8 + 2 + i*12 + 8
			order.PutUint32(buf[pos:pos+4], uint32(exifIFDOff))
		}
	}

	// 写入 Exif IFD
	writeIFD(buf, exifIFDOff, exifIFD, locsExif, order)

	return buf
}

// filterEmpty 过滤值为空的 ASCII 条目。
func filterEmpty(es []Entry) []Entry {
	var out []Entry
	for _, e := range es {
		if e.Type == TypeASCII && e.Str == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// entryDataSize 返回条目值的存储大小（<=4 则内联，不占用数据区）。
func entryDataSize(e Entry) int {
	switch e.Type {
	case TypeASCII:
		return len(e.Str) + 1
	case TypeRational:
		return 8
	case TypeByte, TypeShort, TypeLong, TypeSLong:
		return int(typeSize(e.Type))
	}
	return 4
}

// writeIFD 把条目标题写入指定偏移，并把超长值写入数据区。
func writeIFD(buf []byte, ifdOff int, es []Entry, locs []entryLoc, order binary.ByteOrder) {
	order.PutUint16(buf[ifdOff:ifdOff+2], uint16(len(es)))
	for i, e := range es {
		pos := ifdOff + 2 + i*12
		order.PutUint16(buf[pos:pos+2], e.ID)
		order.PutUint16(buf[pos+2:pos+4], e.Type)
		order.PutUint32(buf[pos+4:pos+8], uint32(countValue(e)))
		loc := locs[i]
		if loc.inline {
			writeInline(buf[pos+8:pos+12], e, order)
		} else {
			// 写入数据区并填偏移
			dst := buf[loc.dataOff : loc.dataOff+entryDataSize(e)]
			writeRaw(dst, e, order)
			order.PutUint32(buf[pos+8:pos+12], uint32(loc.dataOff))
		}
	}
	// next IFD 偏移（无下一个 IFD）
	nextPos := ifdOff + 2 + 12*len(es)
	order.PutUint32(buf[nextPos:nextPos+4], 0)
}

// countValue 返回条目值的数量。
func countValue(e Entry) uint32 {
	if e.Type == TypeASCII {
		return uint32(len(e.Str) + 1)
	}
	return 1
}

// writeInline 写入长度<=4 的整数值。
func writeInline(dst []byte, e Entry, order binary.ByteOrder) {
	switch e.Type {
	case TypeShort:
		order.PutUint16(dst, uint16(e.Value))
	case TypeLong:
		order.PutUint32(dst, uint32(e.Value))
	case TypeASCII:
		copy(dst, append([]byte(e.Str), 0))
	}
}

// writeRaw 写入数据区中的值（ASCII 字符串或 RATIONAL）。
func writeRaw(dst []byte, e Entry, order binary.ByteOrder) {
	switch e.Type {
	case TypeASCII:
		copy(dst, append([]byte(e.Str), 0))
	case TypeRational:
		num := uint32(e.Value >> 32)
		den := uint32(e.Value & 0xFFFFFFFF)
		order.PutUint32(dst[0:4], num)
		order.PutUint32(dst[4:8], den)
	}
}
