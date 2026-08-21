package bmff

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// 本文件提供 meta 盒子的语义解析：
// 读取条目信息(iinf)、条目位置(iloc)、条目引用(iref)、
// 条目属性(iprp/ipco/ipma)以及 EXIF 条目。

// ItemInfo 描述一个条目的类型与名字。
type ItemInfo struct {
	ID          uint32
	Type        string
	Name        string
	ContentType string
}

// ItemLocation 描述一个条目数据在文件中的具体位置。
type ItemLocation struct {
	ID      uint32
	Offset  int64 // 数据绝对文件偏移
	Length  int64 // 数据长度
	Extents []Extent
}

// Extent 是条目数据的一个分片。
type Extent struct {
	Offset int64
	Length int64
}

// PropertyInfo 描述 ipco 中的一个属性盒子。
type PropertyInfo struct {
	Index   int    // 1-based 下标（ipma 使用）
	Type    string // 属性类型
	Payload []byte // 属性盒子的完整字节（含盒子头）
	Data    []byte // 属性盒子的数据区（不含头）
}

// MetaItems 汇总解析 meta 盒子的全部结果。
type MetaItems struct {
	PrimaryItemID uint32
	Items         []ItemInfo
	Locations     []ItemLocation
	Refs          []RefEntry
	Properties    []PropertyInfo
	// ItemProps[itemID] = 该条目关联的属性下标(1-based)
	ItemProps map[uint32][]int
	// ItemPropsEssential[itemID] 记录对应属性是否 essential
	ItemPropsEssential map[uint32][]bool
	// DataRefs 记录 itemID -> data_reference_index（通常为 0）
	DataRefs map[uint32]uint16
}

// ParseMetaItems 解析一个 meta 盒子，返回条目/位置/属性等语义信息。
// metaBox 可以是解析出的 meta 盒子或 meta 盒子的字节范围。
func ParseMetaItems(data []byte, meta *Box) (*MetaItems, error) {
	m := &MetaItems{
		ItemProps:          map[uint32][]int{},
		ItemPropsEssential: map[uint32][]bool{},
		DataRefs:           map[uint32]uint16{},
	}
	children := meta.Children
	if children == nil {
		// 尚未解析子盒子时手动解析
		var err error
		children, err = Parse(data, meta.DataStart+4, meta.Start+meta.Size, 8)
		if err != nil {
			return nil, err
		}
	}

	// pitm: 主条目
	if pitm := Find(children, TypePitm); pitm != nil {
		if pitm.Version == 0 && pitm.DataStart+6 <= pitm.Start+pitm.Size {
			m.PrimaryItemID = uint32(binary.BigEndian.Uint16(data[pitm.DataStart+4 : pitm.DataStart+6]))
		} else if pitm.Version >= 1 && pitm.DataStart+8 <= pitm.Start+pitm.Size {
			m.PrimaryItemID = binary.BigEndian.Uint32(data[pitm.DataStart+4 : pitm.DataStart+8])
		}
	}

	// iinf: 条目信息
	if iinf := Find(children, TypeIinf); iinf != nil {
		m.Items = parseIinf(data, iinf)
	}

	// iloc: 条目位置
	if iloc := Find(children, TypeIloc); iloc != nil {
		m.Locations, m.DataRefs = parseIloc(data, iloc)
	}

	// iref: 条目引用
	if iref := Find(children, TypeIref); iref != nil {
		m.Refs = parseIref(data, iref)
	}

	// iprp: 条目属性
	if iprp := Find(children, TypeIprp); iprp != nil {
		m.parseIprp(data, iprp)
	}

	return m, nil
}

// parseIinf 解析 iinf 盒子，返回所有条目。
func parseIinf(data []byte, box *Box) []ItemInfo {
	var out []ItemInfo
	// iinf 是 FullBox：version+flags(4) 后是 entry_count
	// （v0 为 2 字节，v1 为 4 字节），之后才是 infe 子盒子
	off := box.DataStart + 4
	if box.Version >= 1 {
		off += 4
	} else {
		off += 2
	}
	end := box.Start + box.Size
	var cursor int64 = off
	idx := 0
	for cursor+8 <= end {
		b, err := readBox(data, cursor, end)
		if err != nil || b.Type != TypeInfe {
			break
		}
		info := ItemInfo{}
		// infe 是 FullBox：跳过 version+flags 后才是字段
		raw := data[b.DataStart+4 : b.Start+b.Size]
		if b.Version >= 2 && len(raw) >= 8 {
			info.ID = uint32(binary.BigEndian.Uint16(raw[0:2]))
			// raw[2:4] = item_protection_index
			info.Type = string(raw[4:8])
			if len(raw) > 8 {
				name := raw[8:]
				// item_name 是 null 结尾字符串，截断到第一个 0
				if i := bytes.IndexByte(name, 0); i >= 0 {
					name = name[:i]
				}
				info.Name = string(name)
			}
		} else if len(raw) >= 4 {
			info.Type = string(raw[0:4])
		}
		if info.ID == 0 {
			info.ID = uint32(idx + 1)
		}
		out = append(out, info)
		idx++
		cursor = b.Start + b.Size
	}
	return out
}

// parseIloc 解析 iloc 盒子，返回所有条目的位置信息。
// 支持 version 0/1/2，支持 32/64 位偏移与长度。
func parseIloc(data []byte, box *Box) ([]ItemLocation, map[uint32]uint16) {
	var out []ItemLocation
	refs := map[uint32]uint16{}
	if box.DataStart+8 > box.Start+box.Size {
		return out, refs
	}
	ver := box.Version
	raw := data[box.DataStart+4 : box.Start+box.Size] // 跳过 version+flags
	if len(raw) < 6 {
		return out, refs
	}
	// 第 1 字节：offset_size(高4位)+length_size(低4位)
	// 第 2 字节：base_offset_size(高4位)+index_size(低4位)
	f0 := raw[0]
	f1 := raw[1]
	offsetSize := int((f0 >> 4) & 0xF)
	lengthSize := int(f0 & 0xF)
	baseOffsetSize := int((f1 >> 4) & 0xF)
	indexSize := int(f1 & 0xF)
	var p int = 2 // 已读取 2 字节的字段尺寸描述
	var count uint64
	if ver < 2 {
		count = uint64(binary.BigEndian.Uint16(raw[p : p+2]))
		p += 2
	} else {
		count = binary.BigEndian.Uint64(raw[p : p+8])
		p += 8
	}
	readInt := func(n int) uint64 {
		if n == 0 || p+n > len(raw) {
			return 0
		}
		v := uint64(0)
		for i := 0; i < n; i++ {
			v = v<<8 | uint64(raw[p+i])
		}
		p += n
		return v
	}
	for i := uint64(0); i < count; i++ {
		var loc ItemLocation
		if ver < 2 {
			loc.ID = uint32(readInt(2))
		} else {
			loc.ID = uint32(readInt(4))
		}
		if ver == 1 || ver == 2 {
			readInt(2) // construction_method(12) + data_reference_index(4)
		} else {
			refs[loc.ID] = uint16(readInt(2)) // data_reference_index
		}
		base := readInt(baseOffsetSize)
		extentCount := readInt(2)
		for e := uint64(0); e < extentCount; e++ {
			if ver == 1 || ver == 2 {
				readInt(indexSize) // extent_index
			}
			extOff := readInt(offsetSize)
			extLen := readInt(lengthSize)
			loc.Extents = append(loc.Extents, Extent{Offset: int64(base + extOff), Length: int64(extLen)})
		}
		// 汇总为一个连续范围（多分片时取首片；若为单分片则是最常见情况）
		if len(loc.Extents) > 0 {
			loc.Offset = loc.Extents[0].Offset
			loc.Length = loc.Extents[0].Length
			// 计算总长度（分片拼接的总长）
			var total int64
			var minOff int64 = -1
			var maxEnd int64
			for _, ex := range loc.Extents {
				if minOff == -1 || ex.Offset < minOff {
					minOff = ex.Offset
				}
				if ex.Offset+ex.Length > maxEnd {
					maxEnd = ex.Offset + ex.Length
				}
				total += ex.Length
			}
			if len(loc.Extents) == 1 {
				loc.Offset = loc.Extents[0].Offset
				loc.Length = loc.Extents[0].Length
			} else {
				loc.Offset = minOff
				loc.Length = maxEnd - minOff
			}
			_ = total
		}
		out = append(out, loc)
	}
	return out, refs
}

// parseIref 解析 iref 盒子，返回所有引用关系。
func parseIref(data []byte, box *Box) []RefEntry {
	var out []RefEntry
	children := box.Children
	if children == nil {
		var err error
		children, err = Parse(data, box.DataStart+4, box.Start+box.Size, 8)
		if err != nil {
			return nil
		}
	}
	for _, c := range children {
		raw := data[c.DataStart : c.Start+c.Size]
		if len(raw) < 4 {
			continue
		}
		from := uint32(binary.BigEndian.Uint16(raw[0:2]))
		cnt := uint32(binary.BigEndian.Uint16(raw[2:4]))
		re := RefEntry{Type: c.Type, FromID: from}
		step := 2
		if box.Version >= 1 {
			from = binary.BigEndian.Uint32(raw[0:4])
			cnt = binary.BigEndian.Uint32(raw[4:8])
			step = 4
		}
		p := 4
		if box.Version >= 1 {
			p = 8
		}
		for i := uint32(0); i < cnt && p+step <= len(raw); i++ {
			if step == 2 {
				re.ToIDs = append(re.ToIDs, uint32(binary.BigEndian.Uint16(raw[p:p+2])))
			} else {
				re.ToIDs = append(re.ToIDs, binary.BigEndian.Uint32(raw[p:p+4]))
			}
			p += step
		}
		out = append(out, re)
	}
	return out
}

// parseIprp 解析 iprp 盒子，提取属性列表与关联关系。
func (m *MetaItems) parseIprp(data []byte, box *Box) {
	// iprp 的子盒子是 ipco 与 ipma
	ipco := Find(box.Children, TypeIpco)
	ipma := Find(box.Children, TypeIpma)
	if ipco != nil {
		// 遍历 ipco 的每个子盒子，记录属性
		var idx int = 1
		for _, c := range ipco.Children {
			pi := PropertyInfo{
				Index:   idx,
				Type:    c.Type,
				Payload: data[c.Start : c.Start+c.Size],
				Data:    data[c.DataStart : c.Start+c.Size],
			}
			m.Properties = append(m.Properties, pi)
			idx++
		}
	}
	if ipma != nil {
		// ipma 条目布局：item_ID(2) + association_count(1) + 关联列表。
		// 关联为 essential(1bit)+index(7bit) 各 1 字节；
		// 若 flags bit0 置位则为 essential(1)+index(15) 各 2 字节。
		raw := data[ipma.DataStart+4 : ipma.Start+ipma.Size]
		if len(raw) < 4 {
			return
		}
		u15 := ipma.Flags&0x1 != 0
		var p int = 4
		count := binary.BigEndian.Uint32(raw[0:4])
		for i := uint32(0); i < count; i++ {
			if p+4 > len(raw) {
				break
			}
			var itemID uint32
			if ipma.Version == 0 {
				itemID = uint32(binary.BigEndian.Uint16(raw[p : p+2]))
				p += 2
			} else {
				itemID = binary.BigEndian.Uint32(raw[p : p+4])
				p += 4
			}
			if p+1 > len(raw) {
				break
			}
			assocCount := int(raw[p])
			p += 1
			var indices []int
			var essentials []bool
			for a := 0; a < assocCount; a++ {
				if u15 {
					if p+2 > len(raw) {
						break
					}
					v := binary.BigEndian.Uint16(raw[p : p+2])
					indices = append(indices, int(v&0x7FFF))
					essentials = append(essentials, v&0x8000 != 0)
					p += 2
				} else {
					if p+1 > len(raw) {
						break
					}
					b := raw[p]
					indices = append(indices, int(b&0x7F))
					essentials = append(essentials, b&0x80 != 0)
					p++
				}
			}
			m.ItemProps[itemID] = indices
			m.ItemPropsEssential[itemID] = essentials
		}
	}
}

// ItemData 根据条目位置信息从文件中读取条目的数据字节。
func ItemData(file []byte, loc ItemLocation) ([]byte, error) {
	if loc.Offset < 0 || loc.Length < 0 || loc.Offset+loc.Length > int64(len(file)) {
		return nil, fmt.Errorf("item data out of range: offset=%d len=%d filesize=%d", loc.Offset, loc.Length, len(file))
	}
	return file[loc.Offset : loc.Offset+loc.Length], nil
}

// GetProperty 返回某条目的指定类型属性（第一个匹配）。
func (m *MetaItems) GetProperty(itemID uint32, typ string) *PropertyInfo {
	indices := m.ItemProps[itemID]
	for _, idx := range indices {
		for _, prop := range m.Properties {
			if prop.Index == idx && prop.Type == typ {
				return &prop
			}
		}
	}
	return nil
}
