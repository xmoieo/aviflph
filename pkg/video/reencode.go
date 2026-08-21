package video

import (
	"image"
	"image/color"
	"math"
)

// FpsOf 从轨道时长表估算帧率（向上取整到整数 fps）。
func FpsOf(v *Track) int {
	var count uint64
	var delta uint64
	for _, e := range v.Stts {
		count += uint64(e.Count)
		delta += uint64(e.Count) * uint64(e.Delta)
	}
	if count == 0 || delta == 0 || v.Timescale == 0 {
		return 0
	}
	return int((float64(v.Timescale) * float64(count) / float64(delta)) + 0.5)
}

// DecimateFrames 按整比降帧（保留每 n 帧中的第 1 帧），关键帧序号同步换算。
func DecimateFrames(frames []image.Image, srcFPS, dstFPS int, sync []uint32) ([]image.Image, []uint32) {
	if srcFPS <= dstFPS {
		return frames, sync
	}
	n := (srcFPS + dstFPS - 1) / dstFPS
	end := len(frames) / n * n
	out := make([]image.Image, 0, end/n)
	for i := 0; i < end; i += n {
		out = append(out, frames[i])
	}
	var nsync []uint32
	for _, s := range sync {
		idx := (int(s) - 1) / n
		if len(nsync) == 0 || nsync[len(nsync)-1] != uint32(idx+1) {
			nsync = append(nsync, uint32(idx+1))
		}
	}
	return out, nsync
}

// ScaleImage 双三次（Catmull-Rom 4-tap）缩放到目标尺寸。
func ScaleImage(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == w && sh == h {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sx := float64(sw) / float64(w)
	sy := float64(sh) / float64(h)
	rows := make([][]color.NRGBA, sh)
	for y := 0; y < sh; y++ {
		row := make([]color.NRGBA, sw)
		for x := 0; x < sw; x++ {
			row[x] = color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
		}
		rows[y] = row
	}
	cubic := func(v [4]float64, t float64) float64 {
		c0 := v[1] + (v[2]-v[0])/6.0*t
		c1 := (v[2]-v[1])/2.0 + (v[2]-v[0])/2.0*t
		c2 := (v[0]-2*v[1]+v[2])/2.0 + (v[2]-v[0])/2.0*t
		c3 := (v[3] - 3*v[2] + 3*v[1] - v[0]) / 6.0
		return c0 + c1*t + c2*t*t + c3*t*t*t
	}
	type hcoef struct {
		tx      float64
		coefs   [4][3]float64
		indices [4]int
	}
	hs := make([]hcoef, w)
	for i := 0; i < w; i++ {
		si := (float64(i)+0.5)*sx - 0.5
		xi := int(math.Floor(si))
		if xi < 0 {
			xi = 0
		}
		tx := si - float64(xi)
		idx := func(k int) int {
			if k < 0 {
				return 0
			}
			if k >= sw {
				return sw - 1
			}
			return k
		}
		hs[i].tx = tx
		for k := 0; k < 4; k++ {
			hs[i].indices[k] = idx(xi - 1 + k)
		}
	}
	inter := make([][]color.NRGBA, sh)
	for y := 0; y < sh; y++ {
		row := rows[y]
		line := make([]color.NRGBA, w)
		for i := 0; i < w; i++ {
			var v [4]float64
			for k := 0; k < 4; k++ {
				v[k] = float64(row[hs[i].indices[k]].R)
			}
			r := cubic(v, hs[i].tx)
			for k := 0; k < 4; k++ {
				v[k] = float64(row[hs[i].indices[k]].G)
			}
			g := cubic(v, hs[i].tx)
			for k := 0; k < 4; k++ {
				v[k] = float64(row[hs[i].indices[k]].B)
			}
			bv := cubic(v, hs[i].tx)
			line[i] = color.NRGBA{R: clamp8(r), G: clamp8(g), B: clamp8(bv), A: 255}
		}
		inter[y] = line
	}
	for j := 0; j < h; j++ {
		sj := (float64(j)+0.5)*sy - 0.5
		yi := int(math.Floor(sj))
		if yi < 0 {
			yi = 0
		}
		ty := sj - float64(yi)
		idx := func(k int) int {
			if k < 0 {
				return 0
			}
			if k >= sh {
				return sh - 1
			}
			return k
		}
		var taps [4]int
		for k := 0; k < 4; k++ {
			taps[k] = idx(yi - 1 + k)
		}
		for i := 0; i < w; i++ {
			var vr, vg, vb [4]float64
			for k := 0; k < 4; k++ {
				c := inter[taps[k]][i]
				vr[k] = float64(c.R)
				vg[k] = float64(c.G)
				vb[k] = float64(c.B)
			}
			out.SetNRGBA(i, j, color.NRGBA{
				R: clamp8(cubic(vr, ty)),
				G: clamp8(cubic(vg, ty)),
				B: clamp8(cubic(vb, ty)),
				A: 255,
			})
		}
	}
	return out
}

// RotationFromMatrix 从 tkhd 显示矩阵提取顺时针旋转角度（0/90/180/270）。
func RotationFromMatrix(m [9]uint32) int {
	a, b := m[0], m[1]
	c, d := m[3], m[4]
	one := uint32(0x00010000)
	neg := uint32(0xFFFF0000)
	switch {
	case a == one && b == 0 && c == 0 && d == one:
		return 0
	case a == 0 && b == one && c == neg && d == 0:
		return 90
	case a == neg && b == 0 && c == 0 && d == neg:
		return 180
	case a == 0 && b == neg && c == one && d == 0:
		return 270
	}
	return 0
}

// RotatedSize 返回顺时针旋转后的宽高。
func RotatedSize(w, h int, deg int) (int, int) {
	switch deg {
	case 90, 270:
		return h, w
	}
	return w, h
}

// CropImage 裁剪到指定尺寸。
func CropImage(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for j := 0; j < h && j < b.Dy(); j++ {
		for i := 0; i < w && i < b.Dx(); i++ {
			out.SetNRGBA(i, j, color.NRGBAModel.Convert(img.At(i, j)).(color.NRGBA))
		}
	}
	return out
}

// RotateImage 把图像顺时针旋转 deg 度（0/90/180/270）。
func RotateImage(img image.Image, deg int) image.Image {
	if deg == 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if deg == 180 {
		out := image.NewNRGBA(image.Rect(0, 0, w, h))
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				out.SetNRGBA(w-1-i, h-1-j, color.NRGBAModel.Convert(img.At(i, j)).(color.NRGBA))
			}
		}
		return out
	}
	nw, nh := h, w
	out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			p := color.NRGBAModel.Convert(img.At(i, j)).(color.NRGBA)
			if deg == 90 {
				out.SetNRGBA(nw-1-j, i, p)
			} else {
				out.SetNRGBA(j, nh-1-i, p)
			}
		}
	}
	return out
}
