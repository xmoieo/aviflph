// Package video 提供纯 Go 的视频处理：MP4 解封装、HEVC 解码、
// AV1 动画重编码与 MP4 重封装。全程不调用任何外部命令。
package video

import (
	"fmt"

	"aviflph/pkg/bmff"
)

// Track 描述一条 MP4 轨道（视频或音频）。
type Track struct {
	Codec     string // hevc / av1 / mp4a（aac）
	Width     int
	Height    int
	Timescale uint32
	Duration  uint64
	Matrix    [9]uint32 // tkhd 显示矩阵（16.16 定点），默认单位矩阵；含旋转信息
	// 色彩描述（ISO/IEC 23091-2 码点）；全零表示未标注
	ColorPrimaries uint16
	ColorTransfer  uint16
	ColorMatrix    uint16
	Samples   [][]byte
	HvcC      []byte      // HEVC 视频配置（hvcC box 负载）
	AV1C      []byte      // AV1 视频配置（av1C box 负载）
	Esds      []byte      // AAC 音频配置（esds box 负载）
	Sync      []uint32    // 关键帧样本序号（stss，无则全为关键帧）
	Stts      []SttsEntry // 样本时长表
	Total     uint64      // 样本字节总数
}

// SttsEntry 是 stts 表的一行。
type SttsEntry struct{ Count, Delta uint32 }

// DemuxMP4 解析 MP4 文件，返回视频轨与音频轨（无音频轨时 audio 为 nil）。
func DemuxMP4(data []byte) (video, audio *Track, err error) {
	boxes, err := bmff.Parse(data, 0, int64(len(data)), 0)
	if err != nil {
		return nil, nil, fmt.Errorf("video: demux: %w", err)
	}
	moov := bmff.Find(boxes, "moov")
	if moov == nil {
		return nil, nil, fmt.Errorf("video: demux: no moov box")
	}
	mdat := bmff.Find(boxes, "mdat")
	if mdat == nil {
		return nil, nil, fmt.Errorf("video: demux: no mdat box")
	}
	for _, trak := range moov.Children {
		if trak.Type != "trak" {
			continue
		}
		t, err := parseTrack(data, trak)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case t.Codec == "hevc" || t.Codec == "av1":
			video = t
		case t.Codec == "mp4a":
			audio = t
		}
	}
	if video == nil {
		return nil, nil, fmt.Errorf("video: demux: no video track found")
	}
	return video, audio, nil
}

// DemuxMP4Audio 只解析音频轨（文件可能无视频轨，如纯音频 MP4）。
func DemuxMP4Audio(data []byte) (*Track, error) {
	boxes, err := bmff.Parse(data, 0, int64(len(data)), 0)
	if err != nil {
		return nil, fmt.Errorf("video: demux: %w", err)
	}
	moov := bmff.Find(boxes, "moov")
	if moov == nil {
		return nil, fmt.Errorf("video: demux: no moov box")
	}
	for _, trak := range moov.Children {
		if trak.Type != "trak" {
			continue
		}
		t, err := parseTrack(data, trak)
		if err != nil {
			return nil, err
		}
		if t.Codec == "mp4a" {
			return t, nil
		}
	}
	return nil, fmt.Errorf("video: demux: no audio track found")
}

// stscEntry 是 stsc 表的一行：first_chunk 起（1 基）的每 chunk 样本数。
type stscEntry struct{ firstChunk, samplesPerChunk, descIndex uint32 }

// parseTrack 解析单条轨道：stsd 编解码配置 + stts/stsc/stsz/stco 样本表。
func parseTrack(data []byte, trak *bmff.Box) (*Track, error) {
	mdia := bmff.Find(trak.Children, "mdia")
	if mdia == nil {
		return nil, fmt.Errorf("video: demux: trak without mdia")
	}
	t := &Track{}
	// tkhd：显示矩阵（含旋转）。v0: matrix 在载荷 36..72；v1: 在 48..84。
	if tkhd := bmff.Find(trak.Children, "tkhd"); tkhd != nil {
		pl := bmff.FullBoxPayload(data, tkhd)
		mo := 36
		if tkhd.Version == 1 {
			mo = 48
		}
		for i := 0; i < 9 && mo+i*4+4 <= len(pl); i++ {
			t.Matrix[i] = bmff.U32(pl[mo+i*4:])
		}
	}
	if mdhd := bmff.Find(mdia.Children, "mdhd"); mdhd != nil {
		pl := bmff.FullBoxPayload(data, mdhd)
		if mdhd.Version == 1 {
			t.Timescale = bmff.U32(pl[16:])
			t.Duration = bmff.U64(pl[20:])
		} else {
			t.Timescale = bmff.U32(pl[8:])
			t.Duration = uint64(bmff.U32(pl[12:]))
		}
	}
	minf := bmff.Find(mdia.Children, "minf")
	if minf == nil {
		return nil, fmt.Errorf("video: demux: mdia without minf")
	}
	stbl := bmff.Find(minf.Children, "stbl")
	if stbl == nil {
		return nil, fmt.Errorf("video: demux: minf without stbl")
	}

	// stsd：识别编码格式与配置
	stsd := bmff.Find(stbl.Children, "stsd")
	if stsd == nil {
		return nil, fmt.Errorf("video: demux: no stsd")
	}
	if err := parseStsd(data, stsd, t); err != nil {
		return nil, err
	}

	// stts：样本时长表
	stts := bmff.Find(stbl.Children, "stts")
	if stts == nil {
		return nil, fmt.Errorf("video: demux: no stts")
	}
	tsPl := bmff.FullBoxPayload(data, stts)
	tsCount := bmff.U32(tsPl[0:])
	for i := uint32(0); i < tsCount && i < 1<<16; i++ {
		o := 4 + int(i)*8
		if o+8 > len(tsPl) {
			break
		}
		t.Stts = append(t.Stts, SttsEntry{Count: bmff.U32(tsPl[o:]), Delta: bmff.U32(tsPl[o+4:])})
	}

	// stsz：样本大小表
	stsz := bmff.Find(stbl.Children, "stsz")
	if stsz == nil {
		return nil, fmt.Errorf("video: demux: no stsz")
	}
	szPl := bmff.FullBoxPayload(data, stsz)
	if len(szPl) < 8 {
		return nil, fmt.Errorf("video: demux: bad stsz")
	}
	uniformSize := bmff.U32(szPl[0:])
	sampleCount := bmff.U32(szPl[4:])
	// stsz 载荷：version+flags(4) + sample_size(4) + sample_count(4) + sizes...
	if sampleCount > 1<<24 {
		return nil, fmt.Errorf("video: demux: suspicious stsz sample count %d", sampleCount)
	}

	// stsc：样本到 chunk 映射
	stsc := bmff.Find(stbl.Children, "stsc")
	if stsc == nil {
		return nil, fmt.Errorf("video: demux: no stsc")
	}
	scPl := bmff.FullBoxPayload(data, stsc)
	entryCount := bmff.U32(scPl[0:])
	var entries []stscEntry
	for i := uint32(0); i < entryCount && i < 1024; i++ {
		o := 4 + int(i)*12
		if o+12 > len(scPl) {
			break
		}
		entries = append(entries, stscEntry{
			firstChunk:      bmff.U32(scPl[o:]),
			samplesPerChunk: bmff.U32(scPl[o+4:]),
			descIndex:       bmff.U32(scPl[o+8:]),
		})
	}

	// stco/co64：chunk 偏移表
	stco := bmff.Find(stbl.Children, "stco")
	co64 := bmff.Find(stbl.Children, "co64")
	var chunkOffsets []int64
	if stco != nil {
		pl := bmff.FullBoxPayload(data, stco)
		n := bmff.U32(pl[0:])
		for i := uint32(0); i < n && i < 1<<20; i++ {
			chunkOffsets = append(chunkOffsets, int64(bmff.U32(pl[4+i*4:])))
		}
	} else if co64 != nil {
		pl := bmff.FullBoxPayload(data, co64)
		n := bmff.U32(pl[0:])
		for i := uint32(0); i < n && i < 1<<20; i++ {
			chunkOffsets = append(chunkOffsets, int64(bmff.U64(pl[4+i*8:])))
		}
	}

	// 提取样本
	var sizes []uint32
	if uniformSize > 0 {
		for i := uint32(0); i < sampleCount; i++ {
			sizes = append(sizes, uniformSize)
		}
	} else {
		o := 8
		for i := uint32(0); i < sampleCount && o+4 <= len(szPl); i++ {
			sizes = append(sizes, bmff.U32(szPl[o:]))
			o += 4
		}
	}
	if len(sizes) == 0 || len(chunkOffsets) == 0 {
		return nil, fmt.Errorf("video: demux: empty sample table")
	}

	// stss：同步样本
	if stss := bmff.Find(stbl.Children, "stss"); stss != nil {
		pl := bmff.FullBoxPayload(data, stss)
		n := bmff.U32(pl[0:])
		for i := uint32(0); i < n && i < 1<<20; i++ {
			t.Sync = append(t.Sync, bmff.U32(pl[4+i*4:]))
		}
	}

	// 按 chunk 顺序遍历：样本索引全局递增
	sampleIdx := uint32(0)
	for ci, off := range chunkOffsets {
		spp := samplesPerChunk(entries, ci)
		if int64(sampleIdx)+int64(spp) > int64(len(sizes)) {
			spp = int(uint32(len(sizes)) - sampleIdx)
		}
		// chunk 内偏移：只累加本 chunk 内已取样本的大小
		inChunk := int64(0)
		for s := 0; s < int(spp); s++ {
			size := sizes[sampleIdx]
			pos := off + inChunk
			if pos+int64(size) > int64(len(data)) || pos < 0 {
				return nil, fmt.Errorf("video: demux: sample %d out of range (chunk %d, off %d, pos %d, size %d, filelen %d)", sampleIdx, ci, off, pos, size, len(data))
			}
			t.Samples = append(t.Samples, data[pos:pos+int64(size)])
			t.Total += uint64(size)
			inChunk += int64(size)
			sampleIdx++
		}
	}
	return t, nil
}

// samplesPerChunk 根据 stsc 表取第 ci 个 chunk 的样本数（chunk 从 0 起）。
func samplesPerChunk(entries []stscEntry, ci int) int {
	spp := entries[0].samplesPerChunk
	for _, e := range entries {
		if ci+1 >= int(e.firstChunk) {
			spp = e.samplesPerChunk
		}
	}
	return int(spp)
}

// parseStsd 解析 stsd 中的第一个样本条目，识别编码并提取 hvcC/av1C/esds。
func parseStsd(data []byte, stsd *bmff.Box, t *Track) error {
	pl := bmff.FullBoxPayload(data, stsd)
	if len(pl) < 8 {
		return fmt.Errorf("video: demux: bad stsd")
	}
	entryCount := bmff.U32(pl[0:])
	if entryCount == 0 {
		return fmt.Errorf("video: demux: empty stsd")
	}
	// 第一个条目：size(4)+type(4)+reserved(6)+dref(2)
	entry := pl[4:]
	if len(entry) < 16 {
		return fmt.Errorf("video: demux: bad sample entry")
	}
	entryType := string(entry[4:8])
	entryData := entry[16:]
	switch entryType {
	case "hvc1", "hev1":
		t.Codec = "hevc"
		if len(entryData) >= 20 {
			t.Width = int(bmff.U16(entryData[16:]))
			t.Height = int(bmff.U16(entryData[18:]))
		}
		if hvcC := findChildBox(entryData, "hvcC"); hvcC != nil {
			t.HvcC = append([]byte(nil), hvcC[8:]...)
		}
		parseColr(entryData, t)
	case "av01":
		t.Codec = "av1"
		if len(entryData) >= 20 {
			t.Width = int(bmff.U16(entryData[16:]))
			t.Height = int(bmff.U16(entryData[18:]))
		}
		if av1C := findChildBox(entryData, "av1C"); av1C != nil {
			t.AV1C = append([]byte(nil), av1C[8:]...)
		}
		parseColr(entryData, t)
	case "mp4a":
		t.Codec = "mp4a"
		// AudioSampleEntry 固定字段 36 字节（含 16 字节条目头），entryData 内从 20 起
		if esds := findChildBoxFrom(entryData, "esds", 20); esds != nil {
			t.Esds = append([]byte(nil), esds[8:]...)
		}
	default:
		return fmt.Errorf("video: demux: unsupported codec %q", entryType)
	}
	return nil
}

// parseColr 提取样本条目里的 colr/nclx 色彩描述（无则保持零值）。
func parseColr(entryData []byte, t *Track) {
	if c := findChildBox(entryData, "colr"); c != nil && len(c) >= 8+11 && string(c[8:12]) == "nclx" {
		t.ColorPrimaries = bmff.U16(c[12:])
		t.ColorTransfer = bmff.U16(c[14:])
		t.ColorMatrix = bmff.U16(c[16:])
	}
}

// findChildBox 在数据区中查找第一个指定类型的子 box（含 box 头）。
// 视觉样本条目的固定字段共 70 字节（VisualSampleEntry 除去 16 字节条目头），
// 子 box 从 offset 70 开始。
func findChildBox(entryData []byte, typ string) []byte {
	return findChildBoxFrom(entryData, typ, 70)
}

// findChildBoxFrom 从指定偏移起查找子 box。
func findChildBoxFrom(entryData []byte, typ string, start int) []byte {
	if start > len(entryData) {
		return nil
	}
	for off := start; off+8 <= len(entryData); {
		size := int(bmff.U32(entryData[off:]))
		boxType := string(entryData[off+4 : off+8])
		if size < 8 || off+size > len(entryData) {
			return nil
		}
		if boxType == typ {
			return entryData[off : off+size]
		}
		off += size
	}
	return nil
}
