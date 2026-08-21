// Package exif 提供最小化的 EXIF/TIFF 读写实现。
//
// 只实现 aviflph 需要的能力：
//  1. 从 JPEG 的 APP1 段中读取 IFD0/Exif IFD/GPS 的关键标签
//     （用于把原始照片的 Make/Model/Orientation/拍摄时间等带进生成的 AVIF）。
//  2. 构建一个极小的 TIFF/EXIF 块，写入 XOMUPhoto 等自定义标签。
//
// 不依赖任何第三方库，保持二进制体积最小。
package exif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 常用 EXIF 标签 ID。
const (
	TagMake             = 0x010F // 厂商
	TagModel            = 0x0110 // 型号
	TagOrientation      = 0x0112 // 方向（1=正常,6=旋转90°,8=旋转270°...）
	TagXResolution      = 0x011A // X 分辨率
	TagYResolution      = 0x011B // Y 分辨率
	TagResolutionUnit   = 0x0128 // 分辨率单位
	TagDateTime         = 0x0132 // 修改时间
	TagDateTimeOriginal = 0x9003 // 拍摄时间
	TagExifIFDPtr       = 0x8769 // Exif IFD 指针
	TagGPX              = 0x8825 // GPS IFD 指针
	TagXOMUPhoto        = 0x0002 // 自定义：动态照片(live photo)标记，默认值 1
)

// 标签类型。
const (
	TypeByte      = 1
	TypeASCII     = 2
	TypeShort     = 3
	TypeLong      = 4
	TypeRational  = 5
	TypeUndefined = 7
	TypeSLong     = 9
)

// 常用标签名称（用于显示）。
var tagNames = map[uint16]string{
	TagMake:             "Make",
	TagModel:            "Model",
	TagOrientation:      "Orientation",
	TagXResolution:      "XResolution",
	TagYResolution:      "YResolution",
	TagResolutionUnit:   "ResolutionUnit",
	TagDateTime:         "DateTime",
	TagDateTimeOriginal: "DateTimeOriginal",
	TagXOMUPhoto:        "XOMUPhoto",
	TagExifIFDPtr:       "ExifIFDPtr",
	TagGPX:              "GPSIFDPtr",
}

// TagName 返回标签的显示名称。
func TagName(id uint16) string {
	if n, ok := tagNames[id]; ok {
		return n
	}
	return fmt.Sprintf("0x%04X", id)
}

// Tag 表示一个解析出来的 IFD 标签。
type Tag struct {
	ID    uint16
	Type  uint16
	Count uint32
	Value []byte // 原始值字节（若内联则为 4 字节）
	Data  []byte // 若值超出 4 字节则指向的实际数据区
}

// TagString 返回标签值的可读字符串。
func (t *Tag) TagString(order binary.ByteOrder) string {
	if len(t.Data) > 0 {
		if t.Type == TypeASCII {
			return strings.TrimRight(string(t.Data), "\x00")
		}
		if t.Type == TypeRational && t.Count == 1 && len(t.Data) >= 8 {
			num := order.Uint32(t.Data[0:4])
			den := order.Uint32(t.Data[4:8])
			if den == 0 {
				return "0"
			}
			return fmt.Sprintf("%d/%d", num, den)
		}
		return fmt.Sprintf("(%d bytes)", t.Count)
	}
	switch t.Type {
	case TypeShort:
		if t.Count == 1 && len(t.Value) >= 2 {
			return strconv.FormatUint(uint64(order.Uint16(t.Value)), 10)
		}
	case TypeLong:
		if t.Count == 1 && len(t.Value) >= 4 {
			return strconv.FormatUint(uint64(order.Uint32(t.Value)), 10)
		}
	}
	return fmt.Sprintf("(%d values)", t.Count)
}

// DataSet 是解析结果：IFD0 与其他 IFD 的标签集合。
type DataSet struct {
	IFD0      []Tag
	Exif      []Tag
	GPS       []Tag
	ByteOrder binary.ByteOrder
	Raw       []byte // 源 TIFF 原始字节（含缩略图等完整数据，用于原样拷贝）
}

// Get 从指定 IFD 中取标签。
func (d *DataSet) Get(ifd string, id uint16) *Tag {
	list := d.IFD0
	switch ifd {
	case "exif":
		list = d.Exif
	case "gps":
		list = d.GPS
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// Extract 从任意字节流中提取第一个 EXIF 块并解析。
// 自动处理 "Exif\0\0" 前缀（JPEG APP1 或 AVIF Exif 条目）。
// JPEG 输入（FFD8 开头）时只取第一个 APP1 段，避免把整张图带进 Raw。
func Extract(data []byte) (*DataSet, error) {
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8 {
		if seg := jpegAPP1(data); seg != nil {
			data = seg
		}
	}
	off := 0
	idx := indexBytes(data, []byte("Exif\x00\x00"))
	if idx >= 0 {
		off = idx + 6
	}
	if off+8 > len(data) {
		return nil, errors.New("exif: not found")
	}
	ds, err := Parse(data[off:])
	if err != nil {
		return nil, err
	}
	ds.Raw = append([]byte(nil), data[off:]...)
	return ds, nil
}

// jpegAPP1 提取 JPEG 中第一个 APP1 段（含段头 FFE1+长度）。
func jpegAPP1(data []byte) []byte {
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xff {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xd8 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			i += 2
			continue
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return nil
		}
		if marker == 0xe1 && i+4 <= len(data) && string(data[i+4:i+10]) == "Exif\x00\x00" {
			return data[i : i+2+segLen]
		}
		i += 2 + segLen
	}
	return nil
}

// Parse 解析一个 TIFF 块（可含 "Exif\0\0" 前缀，自动跳过）。
func Parse(tiff []byte) (*DataSet, error) {
	off := 0
	if len(tiff) >= 6 && string(tiff[:4]) == "Exif" {
		off = 6
	}
	if len(tiff) < off+8 {
		return nil, errors.New("exif: tiff too short")
	}
	var order binary.ByteOrder
	switch string(tiff[off : off+2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, errors.New("exif: bad byte order marker")
	}
	if order.Uint16(tiff[off+2:off+4]) != 42 {
		return nil, errors.New("exif: bad magic")
	}
	ifdOff := int(order.Uint32(tiff[off+4 : off+8]))
	ds := &DataSet{ByteOrder: order}
	ds.IFD0 = parseIFD(tiff, off+ifdOff, order)
	if ex := findTag(ds.IFD0, TagExifIFDPtr); ex != nil {
		if ptr := tagValueUint(ex, order); ptr > 0 {
			ds.Exif = parseIFD(tiff, off+int(ptr), order)
		}
	}
	if gp := findTag(ds.IFD0, TagGPX); gp != nil {
		if ptr := tagValueUint(gp, order); ptr > 0 {
			ds.GPS = parseIFD(tiff, off+int(ptr), order)
		}
	}
	return ds, nil
}

// findTag 在标签列表中按 ID 查找。
func findTag(tags []Tag, id uint16) *Tag {
	for i := range tags {
		if tags[i].ID == id {
			return &tags[i]
		}
	}
	return nil
}

// tagValueUint 返回标签值的整数（适用于 SHORT/LONG 内联存储）。
func tagValueUint(t *Tag, order binary.ByteOrder) uint64 {
	if t.Type == TypeShort && t.Count == 1 {
		return uint64(order.Uint16(t.Value))
	}
	if t.Type == TypeLong && t.Count == 1 {
		return uint64(order.Uint32(t.Value))
	}
	return 0
}

// parseIFD 解析一个 IFD 的所有条目。
func parseIFD(tiff []byte, off int, order binary.ByteOrder) []Tag {
	if off < 0 || off+2 > len(tiff) {
		return nil
	}
	count := int(order.Uint16(tiff[off : off+2]))
	var tags []Tag
	p := off + 2
	for i := 0; i < count; i++ {
		if p+12 > len(tiff) {
			break
		}
		e := tiff[p : p+12]
		t := Tag{
			ID:    order.Uint16(e[0:2]),
			Type:  order.Uint16(e[2:4]),
			Count: order.Uint32(e[4:8]),
		}
		t.Value = e[8:12]
		sz := int(typeSize(t.Type) * t.Count)
		if sz > 4 {
			doff := int(order.Uint32(t.Value))
			if doff >= 0 && doff+sz <= len(tiff) {
				t.Data = tiff[doff : doff+sz]
			}
		}
		tags = append(tags, t)
		p += 12
	}
	return tags
}

// typeSize 返回某类型单个元素的字节数。
func typeSize(t uint16) uint32 {
	switch t {
	case TypeByte, TypeASCII, TypeUndefined:
		return 1
	case TypeShort:
		return 2
	case TypeLong, TypeSLong:
		return 4
	case TypeRational:
		return 8
	}
	return 1
}

// indexBytes 在 data 中查找子串 pattern 的起始位置。
func indexBytes(data, pattern []byte) int {
	if len(pattern) == 0 {
		return 0
	}
	for i := 0; i+len(pattern) <= len(data); i++ {
		if string(data[i:i+len(pattern)]) == string(pattern) {
			return i
		}
	}
	return -1
}
