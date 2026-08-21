package bmff

import (
	"bytes"
	"encoding/binary"
)

// 本文件提供构建 BMFF 盒子的工具函数。
// 生成的盒子字节均可直接写入文件或嵌套到父盒子中。

// bePut 把值按大端写入到 buf 的指定偏移。
func bePut(buf []byte, off int, val uint64, n int) {
	for i := 0; i < n; i++ {
		buf[off+n-1-i] = byte(val >> (8 * i))
	}
}

// BuildBox 包装任意 payload 为一个标准盒子：size(4) + type(4) + payload。
// 若 payload 总大小超过 4GB 自动使用 64 位扩展 size。
func BuildBox(typ string, payload []byte) []byte {
	total := int64(8 + len(payload))
	if total >= 1<<32 {
		buf := make([]byte, 16+len(payload))
		binary.BigEndian.PutUint32(buf, 1)
		copy(buf[4:8], typ)
		binary.BigEndian.PutUint64(buf[8:16], uint64(total))
		copy(buf[16:], payload)
		return buf
	}
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(total))
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

// BuildFullBox 构造一个 FullBox：type(4) + version(1) + flags(3) + payload。
func BuildFullBox(typ string, ver uint8, flags uint32, payload []byte) []byte {
	head := make([]byte, 4)
	head[0] = ver
	bePut(head, 1, uint64(flags), 3)
	return BuildBox(typ, append(head, payload...))
}

// BuildFtyp 构建 ftyp 盒子。
// major 为主品牌，brands 为兼容品牌列表（每个 4 字节）。
func BuildFtyp(major string, brands []string) []byte {
	payload := make([]byte, 4+4*len(brands))
	copy(payload, major)
	for i, b := range brands {
		copy(payload[4+i*4:], b)
	}
	return BuildBox(TypeFtyp, payload)
}

// BuildHdlr 构建 handler 盒子，声明媒体处理器类型（pict=图片）。
func BuildHdlr(handlerType string) []byte {
	p := make([]byte, 24)
	// pre_defined(4) + handler_type(4) + reserved(12) + name
	copy(p[4:8], handlerType)
	return BuildFullBox(TypeHdlr, 0, 0, p)
}

// BuildPitm 构建 primary item 盒子，指定默认显示条目的 ID。
func BuildPitm(itemID uint32) []byte {
	if itemID > 0xFFFF {
		return BuildFullBox(TypePitm, 1, 0, beBytes(itemID, 4))
	}
	return BuildFullBox(TypePitm, 0, 0, beBytes(itemID, 2))
}

// BuildIloc 构建 item location 盒子，记录每个条目数据在文件中的位置。
// items 的 Offset 应为条目数据在文件中的绝对偏移（construction_method=0）。
// 若任何偏移或长度超过 32 位，则自动使用 64 位字段（iloc v1）。
func BuildIloc(items []IlocEntry) []byte {
	var ver byte
	osize, lsize := 1, 1
	need64 := false
	for _, it := range items {
		if it.Offset >= 1<<32 || it.Length >= 1<<32 {
			need64 = true
		}
	}
	if need64 {
		ver = 1
		osize, lsize = 8, 8
	} else {
		osize, lsize = 4, 4
	}
	// 与 cproject 一致的布局（前端/解码器按此解析）：
	// 前 2 字节：offset_size(4)+length_size(4) + base_offset_size(4)+index_size(4)
	// base_offset = 数据在文件中的绝对偏移，extent_offset 恒为 0。
	bsize := osize
	f := byte(osize<<4) | byte(lsize)
	g := byte(bsize<<4) | byte(0)
	payload := []byte{f, g}
	payload = append(payload, beBytes(uint64(len(items)), 2)...)
	for _, it := range items {
		if ver == 0 {
			payload = append(payload, beBytes(it.ID, 2)...)
			payload = append(payload, 0, 0) // data_reference_index
		} else {
			payload = append(payload, beBytes(it.ID, 4)...)
			payload = append(payload, 0, 0) // construction_method(12)+data_ref(4)
		}
		payload = append(payload, beBytes(it.Offset, bsize)...) // base_offset
		payload = append(payload, beBytes(1, 2)...)             // extent_count
		payload = append(payload, beBytes(0, osize)...)         // extent_offset
		payload = append(payload, beBytes(it.Length, lsize)...) // extent_length
	}
	return BuildFullBox(TypeIloc, ver, 0, payload)
}

// IlocEntry 是 iloc 中单个条目的位置描述。
// Offset 为数据在文件中的绝对偏移（写入 base_offset 字段）。
type IlocEntry struct {
	ID     uint32 // item id
	Offset uint64 // 数据在文件中的绝对偏移
	Length uint64 // 数据长度
}

// BuildIinf 构建 item info 盒子，声明所有条目。
func BuildIinf(entries []InfeEntry) []byte {
	var payload []byte
	payload = append(payload, beBytes(uint64(len(entries)), 2)...)
	for _, e := range entries {
		payload = append(payload, BuildInfe(e.ID, e.Type, e.Name)...)
	}
	return BuildFullBox(TypeIinf, 0, 0, payload)
}

// InfeEntry 描述一个 item 信息条目。
type InfeEntry struct {
	ID   uint32
	Type string // 4 字符条目类型，如 av01/xomu/Exif
	Name string // 条目名称
}

// BuildInfe 构建单个 item info entry（infe）。
// 使用 version 2 的 infe，支持 item_type 与 item_name 字段。
func BuildInfe(id uint32, typ, name string) []byte {
	var p []byte
	p = append(p, beBytes(id, 2)...) // item_ID
	p = append(p, 0, 0)              // item_protection_index
	p = append(p, []byte(typ)...)    // item_type
	p = append(p, []byte(name)...)   // item_name
	p = append(p, 0)                 // item_name 必须以 null 结尾（规范要求，avifdec 严格校验）
	return BuildFullBox(TypeInfe, 2, 0, p)
}

// BuildIref 构建 item reference 盒子。
// refs 描述所有引用关系；使用 version 0（16 位 item id）。
func BuildIref(refs []RefEntry) []byte {
	var payload []byte
	for _, r := range refs {
		// 单个类型引用：from_ID(2) + reference_count(2) + to_ID[]
		body := append(beBytes(r.FromID, 2), beBytes(uint64(len(r.ToIDs)), 2)...)
		for _, t := range r.ToIDs {
			body = append(body, beBytes(t, 2)...)
		}
		payload = append(payload, BuildBox(r.Type, body)...)
	}
	return BuildFullBox(TypeIref, 0, 0, payload)
}

// RefEntry 描述一个 item 引用：from 引用了若干 to。
type RefEntry struct {
	Type   string // 引用类型：cdsc / auxl / dimg ...
	FromID uint32
	ToIDs  []uint32
}

// BuildIprp 构建 item properties 盒子（iprp = ipco + ipma）。
// props 为按顺序排列的属性盒子字节（ipco 内容），assoc 描述每个条目的属性关联。
// 属性在 ipco 中的下标从 1 开始。
func BuildIprp(props [][]byte, assoc []IpmaEntry) []byte {
	// ipco: 属性容器
	var ipcoBody []byte
	for _, p := range props {
		ipcoBody = append(ipcoBody, p...)
	}
	ipco := BuildBox(TypeIpco, ipcoBody)
	ipma := BuildIpma(assoc)
	// iprp 不是 FullBox，无 version/flags
	return BuildBox(TypeIprp, append(ipco, ipma...))
}

// IpmaEntry 描述 item 与属性的关联。
type IpmaEntry struct {
	ItemID          uint32
	PropertyIndices []uint16 // 1-based 属性下标（对应 ipco 中的顺序）
	Essential       []bool   // 每个属性是否 essential
}

// BuildIpma 构建 item property association 盒子。
// 每个关联为 1 字节(essential(1)+index(7))；若任何下标超过 127，
// 使用 flags bit0 选择的 15 位索引形式(essential(1)+index(15))。
func BuildIpma(entries []IpmaEntry) []byte {
	needU15 := false
	for _, e := range entries {
		for _, idx := range e.PropertyIndices {
			if idx > 127 {
				needU15 = true
			}
		}
	}
	var p []byte
	p = append(p, beBytes(uint64(len(entries)), 4)...)
	for _, e := range entries {
		p = append(p, beBytes(e.ItemID, 2)...)      // item_ID
		p = append(p, byte(len(e.PropertyIndices))) // association_count
		for i, idx := range e.PropertyIndices {
			ess := byte(0)
			if i < len(e.Essential) && e.Essential[i] {
				ess = 0x80
			}
			if needU15 {
				// essential(1) + property_index(15)
				v := uint16(ess)<<8 | uint16(idx)
				p = append(p, beBytes(v, 2)...)
			} else {
				p = append(p, ess|byte(idx))
			}
		}
	}
	flags := uint32(0)
	if needU15 {
		flags = 1
	}
	return BuildFullBox(TypeIpma, 0, flags, p)
}

// BuildExifItem 构造 EXIF 条目的数据区：'Exif\0\0' + TIFF 数据。
// 这是 HEIF 规范中 Exif 条目的标准存储格式。
func BuildExifItem(tiff []byte) []byte {
	return append([]byte("Exif\x00\x00"), tiff...)
}

// BuildIspe 构建图像尺寸属性。
func BuildIspe(w, h uint32) []byte {
	return BuildFullBox(TypeIspe, 0, 0, append(beBytes(w, 4), beBytes(h, 4)...))
}

// BuildPixi 构建像素信息属性（每通道位数）。
func BuildPixi(channels ...uint8) []byte {
	var p []byte
	p = append(p, byte(len(channels)))
	for _, c := range channels {
		p = append(p, c)
	}
	return BuildFullBox(TypePixi, 0, 0, p)
}

// BuildColrNCLX 构建 nclx 色彩信息属性。
func BuildColrNCLX(primaries, transfer, matrix uint16, fullRange bool) []byte {
	flags := byte(0)
	if fullRange {
		flags = 1
	}
	body := append([]byte("nclx"), beBytes(primaries, 2)...)
	body = append(body, beBytes(transfer, 2)...)
	body = append(body, beBytes(matrix, 2)...)
	body = append(body, flags)
	return BuildBox(TypeColr, body)
}

// beBytes 把无符号整数编码为大端 n 字节（支持任意无符号类型）。
func beBytes[T ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~int | ~int64](v T, n int) []byte {
	b := make([]byte, n)
	bePut(b, 0, uint64(v), n)
	return b
}

// JoinBoxes 把多个盒子字节按顺序拼接。
func JoinBoxes(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}
