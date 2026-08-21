//go:build !js

package video

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/gen2brain/avif"
)

// Reencode 把源视频（MP4）进程内重编码为 AV1 MP4：
// HEVC 源 → 解码 → AV1 动画重编码 → 与源 AAC 音频重封装。
func Reencode(srcMP4 []byte, quality, speed int, keepAudio bool) ([]byte, error) {
	return ReencodeOpts(srcMP4, quality, speed, keepAudio, 0, 0, 0, "", 0, 0)
}

// ReencodeOpts 与 Reencode 相同，额外指定输出尺寸与帧率。
func ReencodeOpts(srcMP4 []byte, quality, speed int, keepAudio bool, scaleW, scaleH, fps int, audioBitrate string, audioSampleRate int, audioChannels int) ([]byte, error) {
	v, a, err := DemuxMP4(srcMP4)
	if err != nil {
		return nil, err
	}
	if v.Codec != "hevc" {
		return srcMP4, nil
	}
	rot := RotationFromMatrix(v.Matrix)
	frames, err := DecodeHEVCParallel(v.Samples, v.HvcC, v.Sync)
	if err != nil {
		return nil, err
	}
	for i, f := range frames {
		frames[i] = CropImage(f, v.Width, v.Height)
	}
	if rot != 0 {
		for i, f := range frames {
			frames[i] = RotateImage(f, rot)
		}
		v.Width, v.Height = RotatedSize(v.Width, v.Height, rot)
		v.Matrix = [9]uint32{}
	}
	if scaleW > 0 && scaleH == 0 {
		scaleH = v.Height * scaleW / v.Width
		if scaleH%2 != 0 {
			scaleH++
		}
	} else if scaleH > 0 && scaleW == 0 {
		scaleW = v.Width * scaleH / v.Height
		if scaleW%2 != 0 {
			scaleW++
		}
	}
	if scaleW > 0 && scaleH > 0 && (scaleW != v.Width || scaleH != v.Height) {
		for i, f := range frames {
			frames[i] = ScaleImage(f, scaleW, scaleH)
		}
		v.Width, v.Height = scaleW, scaleH
	}
	srcFPS := FpsOf(v)
	outFPS := srcFPS
	if fps > 0 {
		outFPS = fps
		if outFPS > srcFPS {
			outFPS = srcFPS
		}
	}
	if outFPS > 0 && outFPS < srcFPS {
		frames, v.Sync = DecimateFrames(frames, srcFPS, outFPS, v.Sync)
	}
	if outFPS > 0 {
		var gopSync []uint32
		for i := 1; i <= len(frames); i += 30 {
			gopSync = append(gopSync, uint32(i))
		}
		v.Sync = gopSync
	}
	t, err := EncodeAV1SegmentsFPS(frames, quality, speed, v.Sync, encodeWorkers(), outFPS)
	if err != nil {
		return nil, err
	}
	t.Matrix = v.Matrix
	t.ColorPrimaries = v.ColorPrimaries
	t.ColorTransfer = v.ColorTransfer
	t.ColorMatrix = v.ColorMatrix
	if outFPS > 0 && len(t.Samples) > 0 {
		t.Timescale = 90000
		t.Stts = []SttsEntry{{Count: uint32(len(t.Samples)), Delta: uint32(90000 / outFPS)}}
	}
	if !keepAudio {
		a = nil
	} else if a != nil && len(a.Samples) > 0 {
		audioOpts := AudioOpts{Bitrate: audioBitrate, SampleRate: audioSampleRate, Channels: audioChannels}
		a2, err := ReencodeAudioAACFromMP4(srcMP4, audioOpts)
		if err != nil {
			return nil, err
		}
		a = a2
	}
	return MuxTracks(t, a)
}

// AudioOpts 控制音频重编码参数。
type AudioOpts struct {
	Bitrate   string
	SampleRate int
	Channels  int
}

func (o AudioOpts) ffmpegArgs() []string {
	bitrate := "16k"
	if o.Bitrate != "" {
		bitrate = o.Bitrate
	}
	args := []string{"-vn", "-c:a", "aac", "-b:a", bitrate}
	if o.SampleRate > 0 {
		args = append(args, "-ar", fmt.Sprintf("%d", o.SampleRate))
	}
	if o.Channels > 0 {
		args = append(args, "-ac", fmt.Sprintf("%d", o.Channels))
	}
	return args
}

// ReencodeAudioAAC 用系统 ffmpeg 重编码音频。
func ReencodeAudioAAC(a *Track, opts AudioOpts) (*Track, error) {
	packed, err := PackAudioMP4(a)
	if err != nil {
		return nil, err
	}
	return reencodeAACFile(packed, opts)
}

// ReencodeAudioAACFromMP4 用系统 ffmpeg 从原始 MP4 重编码音频。
func ReencodeAudioAACFromMP4(srcMP4 []byte, opts AudioOpts) (*Track, error) {
	return reencodeAACFile(srcMP4, opts)
}

func reencodeAACFile(inData []byte, opts AudioOpts) (*Track, error) {
	tmp, err := os.CreateTemp("", "lph-audio-*.mp4")
	if err != nil {
		return nil, err
	}
	in := tmp.Name()
	defer os.Remove(in)
	if _, err := tmp.Write(inData); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	args := []string{"-hide_banner", "-loglevel", "error", "-i", in}
	args = append(args, opts.ffmpegArgs()...)
	args = append(args, "-movflags", "+faststart", "-f", "mp4", "-y", in+".out.mp4")
	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("video: ffmpeg audio reencode: %w", err)
	}
	defer os.Remove(in + ".out.mp4")
	outData, err := os.ReadFile(in + ".out.mp4")
	if err != nil {
		return nil, err
	}
	a2, err := DemuxMP4Audio(outData)
	if err != nil || a2 == nil || len(a2.Samples) == 0 {
		return nil, fmt.Errorf("video: ffmpeg audio reencode: parse output: %v", err)
	}
	return a2, nil
}

// EncodeAV1Animation 把帧序列编码为 AV1 动画 AVIF。
func EncodeAV1Animation(frames []image.Image, quality, speed, fps int) ([]byte, error) {
	anim := avif.AVIF{Image: frames}
	delay := 1.0 / 30
	if fps > 0 {
		delay = 1.0 / float64(fps)
	}
	for range frames {
		anim.Delay = append(anim.Delay, delay)
	}
	var buf bytes.Buffer
	opts := avif.Options{
		Quality:                quality,
		Speed:                  speed,
		ChromaSubsampling:      image.YCbCrSubsampleRatio420,
		MatrixCoefficients:     9,
		ColorPrimaries:         9,
		TransferCharacteristics: 18,
	}
	if quality <= 0 {
		opts.Quality = avif.DefaultQuality
	}
	if speed <= 0 {
		opts.Speed = avif.DefaultSpeed
	}
	if err := avif.EncodeAll(&buf, &anim, opts); err != nil {
		return nil, fmt.Errorf("video: av1 encode: %w", err)
	}
	return buf.Bytes(), nil
}

func encodeWorkers() int {
	w := runtime.GOMAXPROCS(0)
	if w > 4 {
		w = 4
	}
	return w
}

// EncodeAV1Segments 把帧序列按关键帧边界切成若干段并行编码 AV1。
func EncodeAV1Segments(frames []image.Image, quality, speed int, keyframes []uint32, workers int) (*Track, error) {
	return EncodeAV1SegmentsFPS(frames, quality, speed, keyframes, workers, 0)
}

// EncodeAV1SegmentsFPS 同 EncodeAV1Segments，可指定动画帧率。
func EncodeAV1SegmentsFPS(frames []image.Image, quality, speed int, keyframes []uint32, workers, fps int) (*Track, error) {
	segs := segmentRanges(len(frames), keyframes, workers)
	tracks := make([]*Track, len(segs))
	errs := make([]error, len(segs))
	var wg sync.WaitGroup
	for i, seg := range segs {
		wg.Add(1)
		go func(i int, start, end int) {
			defer wg.Done()
			anim, err := EncodeAV1Animation(frames[start:end], quality, speed, fps)
			if err != nil {
				errs[i] = err
				return
			}
			tracks[i], errs[i] = MainTrackFromAnimation(anim)
		}(i, seg[0], seg[1])
	}
	wg.Wait()
	var merged Track
	for i, t := range tracks {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if t == nil {
			return nil, fmt.Errorf("video: av1 encode: empty segment %d", i)
		}
		if i == 0 {
			merged.Codec = t.Codec
			merged.Width = t.Width
			merged.Height = t.Height
			merged.Timescale = t.Timescale
			merged.AV1C = t.AV1C
		}
		offset := uint32(len(merged.Samples))
		merged.Samples = append(merged.Samples, t.Samples...)
		merged.Stts = append(merged.Stts, t.Stts...)
		for _, s := range t.Sync {
			merged.Sync = append(merged.Sync, s+offset)
		}
		merged.Total += t.Total
	}
	if len(merged.Samples) == 0 {
		return nil, fmt.Errorf("video: av1 encode: no samples produced")
	}
	return &merged, nil
}
