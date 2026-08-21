//go:build !js

// Package encoder 负责把图像编码为 AVIF、把视频重编码为 AV1(MP4)。
//
// 全程进程内完成，不调用任何外部命令：
//   - 静态图：经 purego 调用系统 libavif 原生库编码
//     （github.com/gen2brain/avif）。
//   - 视频：HEVC 源用纯 Go 解码器（github.com/gen2brain/h265）解码，
//     再用进程内 libavif+aom 重编码为 AV1，并与源 AAC 音频重封装为 MP4。
//     （见 aviflph/pkg/video）
//
// 因此本库不携带任何外部可执行文件依赖，可在 Android/WASM 等
// 无进程执行能力的环境下工作（WASM 亦无法加载原生库，仅封装类操作可用）。
package encoder

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/gen2brain/avif"

	"aviflph/pkg/video"
)

// Options 是编码参数。
type Options struct {
	// ---- 静态图 (AVIF) ----
	StillQuality int    // 质量 0-100，越高越好，默认 15（等效 magick -quality 10 ≈191KB）
	StillSpeed   int    // 编码速度 0-10，越高越快但质量略降，默认 6
	StillChroma  string // 色度采样 420/422/444，默认 420

	// ---- 视频 (AV1 MP4) ----
	VideoCRF     int // 视频质量 0-63，越低质量越好，默认 40（等效 ffmpeg -crf 40）
	VideoPreset  int // 编码速度 0-10，默认 6
	VideoScaleW  int // 输出宽度，0 保持源尺寸
	VideoScaleH  int // 输出高度，0 保持源尺寸
	VideoFPS     int // 输出帧率，0 保持源帧率
	AudioMode    string // passthrough / none，默认 passthrough
	AudioBitrate string // AAC 码率，如 "16k"、"32k"，空串默认 "16k"
	AudioSampleRate int // 采样率（如 48000），0 保持源采样率
	AudioChannels  int  // 声道数，0 保持源声道数

	// ---- 通用 ----
	TmpDir string // 保留兼容（进程内编码不使用临时文件）
}

// DefaultOptions 返回默认编码参数。
func DefaultOptions() Options {
	return Options{
		StillQuality: 10,
		StillSpeed:   6,
		StillChroma:  "420",
		VideoCRF:     50,
		VideoPreset:  6,
		VideoScaleW:  720,
		VideoScaleH:  960,
		VideoFPS:     15,
		AudioMode:    "passthrough",
		AudioBitrate: "8k",
		AudioSampleRate: 16000,
		AudioChannels:  1,
	}
}

// EncodeStill 把源图像（JPEG/PNG/WebP 等任意可解码格式）编码为 AVIF 文件。
func EncodeStill(src []byte, o Options) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("encoder: decode image: %w", err)
	}
	var buf bytes.Buffer
	opts := avif.Options{Quality: o.StillQuality, Speed: o.StillSpeed}
	if opts.Quality <= 0 {
		opts.Quality = avif.DefaultQuality
	}
	if opts.Speed <= 0 {
		opts.Speed = avif.DefaultSpeed
	}
	switch o.StillChroma {
	case "444":
		opts.ChromaSubsampling = image.YCbCrSubsampleRatio444
	case "422":
		opts.ChromaSubsampling = image.YCbCrSubsampleRatio422
	default:
		opts.ChromaSubsampling = image.YCbCrSubsampleRatio420
	}
	// JPEG/PNG 源默认 BT.601 + sRGB 色彩描述，与 ImageMagick 行为一致。
	// MatrixCoefficients=6 (BT.601), ColorPrimaries=1 (BT.709), TransferCharacteristics=13 (sRGB/iec61966-2-1)
	opts.MatrixCoefficients = 6
	opts.ColorPrimaries = 1
	opts.TransferCharacteristics = 13
	if err := avif.Encode(&buf, img, opts); err != nil {
		return nil, fmt.Errorf("encoder: avif encode: %w", err)
	}
	return buf.Bytes(), nil
}

// EncodeVideo 把源视频重编码为 AV1 MP4（HEVC 源进程内转码，其余透传）。
func EncodeVideo(src []byte, o Options) ([]byte, error) {
	q := (63-o.VideoCRF)*100/63 + 1
	if q > 100 {
		q = 100
	}
	if q < 1 {
		q = 1
	}
	speed := o.VideoPreset
	if speed < 1 {
		speed = 1
	}
	if speed > 10 {
		speed = 10
	}
	return video.ReencodeOpts(src, q, speed, o.AudioMode != "none", o.VideoScaleW, o.VideoScaleH, o.VideoFPS, o.AudioBitrate, o.AudioSampleRate, o.AudioChannels)
}
