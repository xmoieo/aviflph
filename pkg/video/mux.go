package video

import (
	"encoding/binary"
	"fmt"

	"aviflph/pkg/bmff"
)

// MuxMP4 把 AV1 动画 AVIF（gen2brain/avif EncodeAll 产物）的视频轨与
// 源音频轨（AAC）重封装为一个标准 MP4。无音频轨时 audio 为 nil。
func MuxMP4(animAVIF []byte, audio *Track) ([]byte, error) {
	v, err := MainTrackFromAnimation(animAVIF)
	if err != nil {
		return nil, err
	}
	return MuxTracks(v, audio)
}

// MuxTracks 把一条 AV1 视频轨与可选的 AAC 音频轨封装为 MP4。
func MuxTracks(v *Track, audio *Track) ([]byte, error) {
	if len(v.Samples) == 0 {
		return nil, fmt.Errorf("video: mux: no video samples")
	}

	videoDur := trackDuration(v)
	if videoDur == 0 {
		videoDur = uint64(len(v.Samples)) * uint64(v.Timescale) / 30
	}
	if v.Timescale == 0 {
		v.Timescale = 1000
	}

	var audioDur uint64
	var aTimescale uint32
	if audio != nil && len(audio.Samples) > 0 {
		// 优先用轨道自带 timescale（解析自 mdhd，权威）；esds ASC 的
		// samplingFrequencyIndex 可能与实际不符（如 ffmpeg 编码器输出）。
		aTimescale = audio.Timescale
		if aTimescale == 0 {
			aTimescale = audioSampleRate(audio.Esds)
		}
		if aTimescale == 0 {
			aTimescale = 44100
		}
		audioDur = trackDuration(audio) // 真实时长来自 stts 总和（帧长可能不均）
		if audioDur == 0 {
			audioDur = uint64(len(audio.Samples)) * 1024 // 回退：AAC LC 1024 样本/帧
		}
	}

	ftyp := bmff.BuildBox("ftyp", cat([]byte("isom"), u32(512), []byte("isomiso2mp41av01")))
	// chunk_offset 是文件绝对偏移；mdat 位于 ftyp+moov 之后。
	// 先以占位偏移构建 moov 确定长度，再以真实绝对偏移重建（结构相同，长度不变）。
	mdat := buildMdat(v.Samples, audioSamples(audio))
	moov0 := buildMoov(v, videoDur, audio, aTimescale, audioDur, 0)
	mdatStart := uint64(len(ftyp) + len(moov0) + 8)
	moov := buildMoov(v, videoDur, audio, aTimescale, audioDur, mdatStart)

	out := append(ftyp, moov...)
	out = append(out, mdat...)
	return out, nil
}

// MainTrackFromAnimation 解析 AV1 动画 AVIF，选总字节数最大的 av01 轨作为视频主轨。
func MainTrackFromAnimation(anim []byte) (*Track, error) {
	boxes, err := bmff.Parse(anim, 0, int64(len(anim)), 0)
	if err != nil {
		return nil, fmt.Errorf("video: mux: parse animation: %w", err)
	}
	moov := bmff.Find(boxes, "moov")
	if moov == nil {
		return nil, fmt.Errorf("video: mux: animation lacks moov")
	}
	var best *Track
	var bestTotal uint64
	for _, trak := range moov.Children {
		if trak.Type != "trak" {
			continue
		}
		t, err := parseTrack(anim, trak)
		if err != nil {
			return nil, fmt.Errorf("video: mux: track: %w", err)
		}
		if t.Codec != "av1" {
			continue
		}
		var total uint64
		for _, s := range t.Samples {
			total += uint64(len(s))
		}
		if best == nil || total > bestTotal {
			best = t
			bestTotal = total
		}
	}
	if best == nil {
		return nil, fmt.Errorf("video: mux: animation has no av01 track")
	}
	return best, nil
}

// buildMoov 组装 moov（mvhd + 各轨 trak）。
func buildMoov(v *Track, videoDur uint64, audio *Track, aTimescale uint32, audioDur uint64, mdatStart uint64) []byte {
	tracks := [][]byte{buildVideoTrak(v, videoDur, mdatStart)}
	if audio != nil && len(audio.Samples) > 0 {
		tracks = append(tracks, buildAudioTrak(audio, aTimescale, audioDur, mdatStart+uint64(videoTotal(v))))
	}
	moovDur := videoDur
	if audio != nil && len(audio.Samples) > 0 {
		aDur := audioDur * 1000 / uint64(aTimescale)
		if aDur > moovDur {
			moovDur = aDur
		}
	}
	mvhd := bmff.BuildFullBox("mvhd", 0, 0,
		cat(u32(0), u32(0), u32(1000), u32(u32Clamp(moovDur)),
			u32(0x00010000), u32(0x01000000), u32(0), u32(0),
			u32(0x00010000), u32(0), u32(0),
			u32(0), u32(0x00010000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			u32(0), u32(0), u32(0), u32(0), u32(0), u32(0),
			u32(2)))
	return bmff.BuildBox("moov", cat(mvhd, cat(tracks...)))
}

// PackAudioMP4 把单条音频轨打包为最小 MP4（仅音频，无视频），供外部转码用。
func PackAudioMP4(a *Track) ([]byte, error) {
	if a == nil || len(a.Samples) == 0 {
		return nil, fmt.Errorf("video: pack audio: empty track")
	}
	aTimescale := a.Timescale
	if aTimescale == 0 {
		aTimescale = audioSampleRate(a.Esds)
	}
	if aTimescale == 0 {
		aTimescale = 44100
	}
	audioDur := trackDuration(a) // 真实时长来自 stts 总和（帧长可能不均）
	if audioDur == 0 {
		audioDur = uint64(len(a.Samples)) * 1024
	}
	ftyp := bmff.BuildBox("ftyp", cat([]byte("isom"), u32(0x200), []byte("isomiso2mp41")))
	mdat := bmff.BuildBox("mdat", nil)
	chunkOff := uint64(len(ftyp) + len(moovOnly(a, aTimescale, audioDur)) + len(mdat))
	moov := buildAudioOnlyMoov(a, aTimescale, audioDur, chunkOff)
	out := make([]byte, 0, chunkOff+videoTotal(a))
	out = append(out, ftyp...)
	out = append(out, moov...)
	out = append(out, mdat...)
	for _, s := range a.Samples {
		out = append(out, s...)
	}
	return out, nil
}

// moovOnly 构建仅音频 moov（无 chunk 偏移依赖，仅用于尺寸预估）。
func moovOnly(a *Track, timescale uint32, duration uint64) []byte {
	return buildAudioOnlyMoov(a, timescale, duration, 0)
}

// buildAudioOnlyMoov 构建仅含音频轨的 moov。
func buildAudioOnlyMoov(a *Track, timescale uint32, duration uint64, chunkOff uint64) []byte {
	tracks := [][]byte{buildAudioTrak(a, timescale, duration, chunkOff)}
	aDur := duration * 1000 / uint64(timescale)
	mvhd := bmff.BuildFullBox("mvhd", 0, 0,
		cat(u32(0), u32(0), u32(1000), u32(u32Clamp(aDur)),
			u32(0x00010000), u32(0x01000000), u32(0), u32(0),
			u32(0x00010000), u32(0), u32(0),
			u32(0), u32(0x00010000), u32(0),
			u32(0), u32(0), u32(0x40000000),
			u32(0), u32(0), u32(0), u32(0), u32(0), u32(0),
			u32(2)))
	return bmff.BuildBox("moov", cat(mvhd, cat(tracks...)))
}

// videoTotal 计算样本字节总数。
func videoTotal(v *Track) uint64 {
	var t uint64
	for _, s := range v.Samples {
		t += uint64(len(s))
	}
	return t
}

func buildVideoTrak(v *Track, duration uint64, chunkOff uint64) []byte {
	stsd := bmff.BuildFullBox("stsd", 0, 0, cat(u32(1), buildAv01Entry(v)))
	stts := buildStts(v.Stts)
	stsc := bmff.BuildFullBox("stsc", 0, 0, cat(u32(1), u32(1), u32(uint32(len(v.Samples))), u32(1)))
	stsz := bmff.BuildFullBox("stsz", 0, 0, cat(u32(0), u32(uint32(len(v.Samples))), sampleSizes(v.Samples)))
	stco := bmff.BuildFullBox("stco", 0, 0, cat(u32(1), u32(uint32(chunkOff))))
	stbl := bmff.BuildBox("stbl", cat(stsd, stts, stsc, stsz, stco, buildStss(v)))
	dinf := bmff.BuildBox("dinf", bmff.BuildFullBox("dref", 0, 0, cat(u32(1), bmff.BuildFullBox("url ", 0, 1, nil))))
	minf := bmff.BuildBox("minf", cat(bmff.BuildFullBox("vmhd", 0, 1, cat(u32(0), u32(0))), dinf, stbl))
	hdlr := bmff.BuildFullBox("hdlr", 0, 0, cat(u32(0), []byte("vide"), u32(0), u32(0), u32(0)))
	mdhd := bmff.BuildFullBox("mdhd", 0, 0, cat(u32(0), u32(0), u32(v.Timescale), u32(u32Clamp(duration)), u16(0x55c4), u16(0)))
	mdia := bmff.BuildBox("mdia", cat(mdhd, hdlr, minf))
	tkhd := bmff.BuildFullBox("tkhd", 0, 7,
		cat(u32(0), u32(0), u32(1), u32(0), u32(u32Clamp(duration)), u32(0), u32(0),
			u16(0), u16(0), u16(0), u16(0),
			matrixFields(v.Matrix),
			u32(uint32(v.Width)), u32(uint32(v.Height))))
	return bmff.BuildBox("trak", cat(tkhd, mdia))
}

// matrixFields 返回 tkhd 的 9 个矩阵字段；零矩阵（未解析到）时用单位矩阵。
func matrixFields(m [9]uint32) []byte {
	unit := [9]uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	isZero := true
	for _, f := range m {
		if f != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		m = unit
	}
	out := make([]byte, 0, 36)
	for _, f := range m {
		out = append(out, u32(f)...)
	}
	return out
}

// buildAudioTrak 组装音频轨（mp4a 条目 + esds + 样本表）。
func buildAudioTrak(a *Track, timescale uint32, duration uint64, chunkOff uint64) []byte {
	stsd := bmff.BuildFullBox("stsd", 0, 0, cat(u32(1), buildMp4aEntry(a, timescale)))
	audioStts := a.Stts
	if len(audioStts) == 0 {
		audioStts = []SttsEntry{{Count: uint32(len(a.Samples)), Delta: 1024}}
	}
	stts := buildStts(audioStts)
	stsc := bmff.BuildFullBox("stsc", 0, 0, cat(u32(1), u32(1), u32(uint32(len(a.Samples))), u32(1)))
	stsz := bmff.BuildFullBox("stsz", 0, 0, cat(u32(0), u32(uint32(len(a.Samples))), sampleSizes(a.Samples)))
	stco := bmff.BuildFullBox("stco", 0, 0, cat(u32(1), u32(uint32(chunkOff))))
	stbl := bmff.BuildBox("stbl", cat(stsd, stts, stsc, stsz, stco))
	dinf := bmff.BuildBox("dinf", bmff.BuildFullBox("dref", 0, 0, cat(u32(1), bmff.BuildFullBox("url ", 0, 1, nil))))
	minf := bmff.BuildBox("minf", cat(bmff.BuildFullBox("smhd", 0, 0, cat(u16(0), u16(0))), dinf, stbl))
	hdlr := bmff.BuildFullBox("hdlr", 0, 0, cat(u32(0), []byte("soun"), u32(0), u32(0), u32(0)))
	mdhd := bmff.BuildFullBox("mdhd", 0, 0, cat(u32(0), u32(0), u32(timescale), u32(u32Clamp(duration)), u16(0x55c4), u16(0)))
	mdia := bmff.BuildBox("mdia", cat(mdhd, hdlr, minf))
	tkhd := bmff.BuildFullBox("tkhd", 0, 7,
		cat(u32(0), u32(0), u32(2), u32(0), u32(u32Clamp(duration)), u32(0), u32(0),
			u16(0), u16(0), u16(0x0100), u16(0),
			u32(0x00010000), u32(0), u32(0), u32(0x00010000), u32(0), u32(0), u32(0x40000000), u32(0), u32(0),
			u32(0), u32(0)))
	return bmff.BuildBox("trak", cat(tkhd, mdia))
}

// buildAv01Entry 构建 av01 视觉样本条目 + av1C（VisualSampleEntry，86 字节头）。
func buildAv01Entry(v *Track) []byte {
	entry := make([]byte, 86)
	copy(entry[4:8], "av01")
	binary.BigEndian.PutUint16(entry[14:], 1) // data_reference_index
	binary.BigEndian.PutUint16(entry[32:], uint16(v.Width))
	binary.BigEndian.PutUint16(entry[34:], uint16(v.Height))
	binary.BigEndian.PutUint32(entry[36:], 0x00480000) // horizresolution 72dpi
	binary.BigEndian.PutUint32(entry[40:], 0x00480000)
	binary.BigEndian.PutUint16(entry[48:], 1)      // frame_count
	binary.BigEndian.PutUint16(entry[82:], 0x0018) // depth 24
	entry = append(entry, bmff.BuildBox("av1C", v.AV1C)...)
	entry = append(entry, buildColr(v)...)
	binary.BigEndian.PutUint32(entry[0:], uint32(len(entry)))
	return entry
}

// buildColr 构建 colr box（nclx 色彩描述）；Track 未标色时返回空。
// primaries/transfer/matrix 为 ISO/IEC 23091-2 码点，range=0 表示有限范围。
func buildColr(v *Track) []byte {
	if v.ColorPrimaries == 0 {
		return nil
	}
	payload := []byte("nclx")
	payload = append(payload, u16(v.ColorPrimaries)...)
	payload = append(payload, u16(v.ColorTransfer)...)
	payload = append(payload, u16(v.ColorMatrix)...)
	payload = append(payload, 0)
	return bmff.BuildBox("colr", payload)
}

// buildMp4aEntry 构建 mp4a 音频样本条目 + esds（含 AAC 配置）。
func buildMp4aEntry(a *Track, timescale uint32) []byte {
	channels := uint16(2)
	if asc := esdsASC(a.Esds); len(asc) > 1 {
		if c := asc[1] & 0x0f; c > 0 {
			channels = uint16(c)
		}
	}
	entry := make([]byte, 36)
	copy(entry[4:8], "mp4a")
	binary.BigEndian.PutUint16(entry[14:], 1) // data_reference_index
	binary.BigEndian.PutUint16(entry[24:], channels)
	binary.BigEndian.PutUint16(entry[26:], 16) // samplesize
	binary.BigEndian.PutUint32(entry[32:], timescale<<16)
	entry = append(entry, bmff.BuildBox("esds", a.Esds)...)
	binary.BigEndian.PutUint32(entry[0:], uint32(len(entry)))
	return entry
}

// buildStss 构建同步样本表（无 stss 时省略，全为关键帧）。
func buildStss(v *Track) []byte {
	if len(v.Sync) == 0 {
		return nil
	}
	payload := u32(uint32(len(v.Sync)))
	for _, s := range v.Sync {
		payload = append(payload, u32(s)...)
	}
	return bmff.BuildFullBox("stss", 0, 0, payload)
}

// buildStts 从时长表构建 stts。
func buildStts(stts []SttsEntry) []byte {
	if len(stts) == 0 {
		return bmff.BuildFullBox("stts", 0, 0, u32(0))
	}
	payload := u32(uint32(len(stts)))
	for _, e := range stts {
		payload = append(payload, u32(e.Count)...)
		payload = append(payload, u32(e.Delta)...)
	}
	return bmff.BuildFullBox("stts", 0, 0, payload)
}

// sampleSizes 构建 stsz 的样本大小表。
func sampleSizes(samples [][]byte) []byte {
	out := make([]byte, 0, len(samples)*4)
	for _, s := range samples {
		out = append(out, u32(uint32(len(s)))...)
	}
	return out
}

// buildMdat 组装样本数据盒。
func buildMdat(video, audio [][]byte) []byte {
	var data []byte
	for _, s := range video {
		data = append(data, s...)
	}
	for _, s := range audio {
		data = append(data, s...)
	}
	return bmff.BuildBox("mdat", data)
}

func audioSamples(a *Track) [][]byte {
	if a == nil {
		return nil
	}
	return a.Samples
}

// trackDuration 从 stts 计算轨道总时长。
func trackDuration(t *Track) uint64 {
	var d uint64
	for _, e := range t.Stts {
		d += uint64(e.Count) * uint64(e.Delta)
	}
	return d
}

// esdsASC 从 esds 负载提取 AudioSpecificConfig（0x05 DecSpecificInfo 描述符）。
func esdsASC(esds []byte) []byte {
	// esds 负载: version+flags(4) + 描述符链（0x03 ES → 0x04 DecoderConfig → 0x05 ASC）
	i := 4
	for i < len(esds) {
		tag := esds[i]
		i++
		if i >= len(esds) {
			return nil
		}
		length := 0
		for k := 0; k < 4; k++ {
			if i >= len(esds) {
				return nil
			}
			b := esds[i]
			i++
			length = length<<7 | int(b&0x7f)
			if b&0x80 == 0 {
				break
			}
		}
		if tag == 0x05 {
			if i+length <= len(esds) {
				return esds[i : i+length]
			}
			return nil
		}
		i += length
	}
	return nil
}

// audioSampleRate 从 esds 的 AudioSpecificConfig 解析采样率。
func audioSampleRate(esds []byte) uint32 {
	asc := esdsASC(esds)
	if len(asc) < 2 {
		return 0
	}
	idx := (asc[0]&0x07)<<1 | asc[1]>>7
	rates := []uint32{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	if int(idx) < len(rates) {
		return rates[idx]
	}
	return 0
}

func u32Clamp(v uint64) uint32 {
	if v > 0xffffffff {
		return 0xffffffff
	}
	return uint32(v)
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// cat 拼接多个字节片段。
func cat(parts ...[]byte) []byte {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
