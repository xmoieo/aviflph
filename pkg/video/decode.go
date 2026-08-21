package video

import (
	"fmt"
	"image"
	"image/color"
	"runtime"
	"sync"

	"github.com/gen2brain/h265/hevc"
)

// DecodeHEVC 把 HEVC 视频轨样本解码为 RGBA 帧序列（支持 8/10 bit）。
// lengthSize 来自 hvcC；hvcC 中的 VPS/SPS/PPS 先喂给解码器。
func DecodeHEVC(samples [][]byte, hvcC []byte) ([]image.Image, error) {
	return decodeRange(samples, hvcC, 0, len(samples))
}

// DecodeHEVCParallel 按关键帧切段并行解码 HEVC 样本。
// sync 是 1-based 关键帧样本序号（无 stss 时全为关键帧）。
func DecodeHEVCParallel(samples [][]byte, hvcC []byte, keyframes []uint32) ([]image.Image, error) {
	segs := segmentRanges(len(samples), keyframes, runtime.GOMAXPROCS(0))
	out := make([][]image.Image, len(segs))
	errs := make([]error, len(segs))
	var wg sync.WaitGroup
	for i, seg := range segs {
		wg.Add(1)
		go func(i int, seg [2]int) {
			defer wg.Done()
			out[i], errs[i] = decodeRange(samples, hvcC, seg[0], seg[1])
		}(i, seg)
	}
	wg.Wait()
	var frames []image.Image
	for i := range segs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		frames = append(frames, out[i]...)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("video: hevc decode: no frames produced")
	}
	return frames, nil
}

// segmentRanges 把样本按关键帧切成若干 [start,end) 区间并聚合成约 workers 段，
// 每段以关键帧开头（可独立解码）。无关键帧信息时均匀切分。
func segmentRanges(total int, sync []uint32, workers int) [][2]int {
	if workers < 1 {
		workers = 1
	}
	// 段边界：0 + 每个关键帧样本处（1-based）+ total
	var bounds []int
	bounds = append(bounds, 0)
	if len(sync) > 0 {
		for _, s := range sync {
			if int(s) >= 1 && int(s) <= total {
				bounds = append(bounds, int(s)-1)
			}
		}
	} else {
		step := (total + workers - 1) / workers
		for i := step; i < total; i += step {
			bounds = append(bounds, i)
		}
	}
	bounds = append(bounds, total)
	bounds = dedupBounds(bounds)
	// 聚合成约 workers 段，帧数尽量均衡
	if len(bounds) > workers+1 {
		var merged [][2]int
		curStart := 0
		target := (total + workers - 1) / workers
		curLen := 0
		for i := 1; i < len(bounds); i++ {
			segLen := bounds[i] - bounds[i-1]
			if curLen > 0 && curLen+segLen > target && len(merged) < workers-1 {
				merged = append(merged, [2]int{curStart, bounds[i-1]})
				curStart = bounds[i-1]
				curLen = 0
			}
			curLen += segLen
		}
		merged = append(merged, [2]int{curStart, total})
		return merged
	}
	var segs [][2]int
	for i := 1; i < len(bounds); i++ {
		segs = append(segs, [2]int{bounds[i-1], bounds[i]})
	}
	return segs
}

func dedupBounds(b []int) []int {
	var out []int
	for _, v := range b {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// decodeRange 解码 samples[start:end)，独立解码器实例。
func decodeRange(samples [][]byte, hvcC []byte, start, end int) ([]image.Image, error) {
	var d hevc.Decoder
	var frames []image.Image
	if err := feedParameterSets(&d, hvcC); err != nil {
		return nil, err
	}
	for si := start; si < end; si++ {
		nals := hevc.SplitHVCC(samples[si], HvcCLengthSize(hvcC))
		for _, nal := range nals {
			out, err := d.DecodeNAL(nal)
			if err != nil {
				return nil, fmt.Errorf("video: hevc decode: sample %d: %w", si, err)
			}
			for _, p := range out {
				frames = append(frames, pictureToRGBA(p))
			}
		}
	}
	for _, p := range d.Flush() {
		frames = append(frames, pictureToRGBA(p))
	}
	return frames, nil
}

// feedParameterSets 解析 hvcC 的 NAL 数组（VPS/SPS/PPS）并喂给解码器。
// 注意：部分厂商的数组类型字节不可靠，只按长度切 NAL，不校验类型匹配。
func feedParameterSets(d *hevc.Decoder, hvcC []byte) error {
	if len(hvcC) < 23 {
		return nil
	}
	numArrays := int(hvcC[22])
	off := 23
	for i := 0; i < numArrays; i++ {
		if off+3 > len(hvcC) {
			return nil
		}
		numNalus := int(hvcC[off+1])<<8 | int(hvcC[off+2])
		off += 3
		for j := 0; j < numNalus; j++ {
			if off+2 > len(hvcC) {
				return nil
			}
			nalLen := int(hvcC[off])<<8 | int(hvcC[off+1])
			off += 2
			if off+nalLen > len(hvcC) {
				return nil
			}
			nalData := hvcC[off : off+nalLen]
			off += nalLen
			if nal, ok := hevc.ParseNAL(nalData); ok {
				if _, err := d.DecodeNAL(nal); err != nil {
					return fmt.Errorf("video: hevc decode: parameter set: %w", err)
				}
			}
		}
	}
	return nil
}

// HvcCLengthSize 从 hvcC 负载读取 lengthSizeMinusOne（字节 21 的低 2 位）。
func HvcCLengthSize(hvcC []byte) int {
	if len(hvcC) < 22 {
		return 4
	}
	return int(hvcC[21]&0x3) + 1
}

// pictureToRGBA 把 h265 解码出的 YUV 平面转换为 RGBA 图像。
// 8 位用 Y/Cb/Cr，10 位用 Y16/Cb16/Cr16（右移 2 位回到 8 位）。
func pictureToRGBA(p *hevc.Picture) image.Image {
	w, h := p.Width, p.Height
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	sy, sc := p.StrideY, p.StrideC
	// BT.2020 HLG 源直出 bt2020 编码值（容器 nclx 标注 bt2020/hlg，播放器自行 HDR→SDR）
	hlg := p.ColorTransfer == 18
	m := yuvMatrix(p.ColorMatrix)
	conv := func(y, cb, cr float64) color.NRGBA {
		if hlg {
			return yuvToRGBA(y, cb, cr, matrixBT2020)
		}
		return yuvToRGBA(y, cb, cr, m)
	}
	if len(p.Y16) > 0 {
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				yy := float64(p.Y16[j*sy+i] >> 2)
				cb := float64(p.Cb16[(j>>1)*sc+(i>>1)] >> 2)
				cr := float64(p.Cr16[(j>>1)*sc+(i>>1)] >> 2)
				img.SetNRGBA(i, j, conv(yy, cb, cr))
			}
		}
		return img
	}
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			yy := float64(p.Y[j*sy+i])
			cb := float64(p.Cb[(j>>1)*sc+(i>>1)])
			cr := float64(p.Cr[(j>>1)*sc+(i>>1)])
			img.SetNRGBA(i, j, conv(yy, cb, cr))
		}
	}
	return img
}

// yuvMatrix 按 ISO/IEC 23091-2 矩阵系数码点选有限范围逆矩阵。
type yuvMatrix int

const (
	matrixBT601   yuvMatrix = 5
	matrixBT709   yuvMatrix = 1
	matrixBT2020  yuvMatrix = 9
	matrixUnknown yuvMatrix = 0
)

// yuvToRGBA 按指定矩阵（有限范围）把 YCbCr 转为 RGBA，默认 BT.601。
func yuvToRGBA(y, cb, cr float64, m yuvMatrix) color.NRGBA {
	var r, g, b float64
	switch m {
	case matrixBT709:
		r = y + 1.5748*(cr-128)
		g = y - 0.1873*(cb-128) - 0.4681*(cr-128)
		b = y + 1.8556*(cb-128)
	case matrixBT2020:
		r = y + 1.4746*(cr-128)
		g = y - 0.16455*(cb-128) - 0.57135*(cr-128)
		b = y + 1.8814*(cb-128)
	default: // BT.601
		r = y + 1.402*(cr-128)
		g = y - 0.344136*(cb-128) - 0.714136*(cr-128)
		b = y + 1.772*(cb-128)
	}
	return color.NRGBA{R: clamp8(r), G: clamp8(g), B: clamp8(b), A: 255}
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
