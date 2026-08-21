//go:build !js

package api

import (
	"errors"
	"fmt"

	"aviflph/pkg/avif"
	"aviflph/pkg/encoder"
	"aviflph/pkg/exif"
	"aviflph/pkg/getmeta"
	"aviflph/pkg/motionphoto"
)

// ConvertOptions 是转换操作的全部参数。
type ConvertOptions struct {
	StillAVIF []byte
	VideoMP4  []byte
	Enc       encoder.Options
	Avif      avif.Options
	Exif      *exif.Options
	CopyExifFromSource bool
	WorkDir  string
	// Raw 为真时不压缩任何画质，保持原图原视频，只转格式封装。
	// JPEG 源直接嵌入（不做 AVIF 编码），视频原样封装。
	Raw bool
}

// ConvertResult 是转换结果。
type ConvertResult struct {
	Output   []byte
	Still    []byte
	Video    []byte
	SourceIs bool
	Meta     *getmeta.Report
}

// Convert 执行完整转换流程。
func Convert(src []byte, opts ConvertOptions) (*ConvertResult, error) {
	res := &ConvertResult{}
	var stillSrc, videoSrc []byte

	switch {
	case motionphoto.IsAVIF(src):
		p, err := avif.ParseFile(src)
		if err != nil {
			return nil, fmt.Errorf("convert: %w", err)
		}
		if p.IsLivePhoto() {
			dm, err := avif.Demux(src)
			if err != nil {
				return nil, err
			}
			stillSrc = dm.StillAVIF
			videoSrc = dm.Video
			res.SourceIs = true
		} else {
			stillSrc = src
			if len(opts.VideoMP4) == 0 {
				return nil, errors.New("convert: input is a still AVIF without video; provide --embed-video-mp4")
			}
		}
	case motionphoto.IsMP4(src):
		videoSrc = src
		if len(opts.StillAVIF) == 0 {
			return nil, errors.New("convert: input is a video without a still; provide --embed-still-avif")
		}
	case motionphoto.IsJPEG(src):
		info, err := motionphoto.Detect(src)
		if err == nil && info.IsMotionPhoto {
			videoSrc, err = motionphoto.ExtractVideo(src)
			if err != nil {
				return nil, err
			}
			stillSrc, err = motionphoto.ExtractStill(src)
			if err != nil {
				return nil, err
			}
			res.SourceIs = true
		} else {
			stillSrc = src
			if len(opts.VideoMP4) == 0 {
				return nil, errors.New("convert: input JPEG has no embedded video; provide --embed-video-mp4")
			}
		}
	default:
		return nil, errors.New("convert: unsupported input format")
	}

	// Raw 模式：不压缩，保持原图原视频，只转格式封装。
	if opts.Raw {
		return convertRaw(stillSrc, videoSrc, opts, res)
	}

	stillAVIF := opts.StillAVIF
	if stillAVIF == nil {
		enc, err := encoder.EncodeStill(stillSrc, opts.Enc)
		if err != nil {
			return nil, err
		}
		stillAVIF = enc
	}
	res.Still = stillAVIF

	video := opts.VideoMP4
	if video == nil {
		enc, err := encoder.EncodeVideo(videoSrc, opts.Enc)
		if err != nil {
			return nil, err
		}
		video = enc
	}
	res.Video = video

	exifOpt := exif.DefaultOptions()
	if opts.Exif != nil {
		exifOpt = *opts.Exif
	}
	var srcDS *exif.DataSet
	if opts.CopyExifFromSource {
		if ds, err := exif.Extract(stillSrc); err == nil {
			srcDS = ds
			applySourceExif(&exifOpt, ds)
		}
	}

	out, err := avif.Embed(avif.EmbedInput{
		StillData:  stillAVIF,
		VideoData:  video,
		Exif:       &exifOpt,
		Opt:        &opts.Avif,
		SourceExif: srcDS,
	})
	if err != nil {
		return nil, err
	}
	res.Output = out

	if m, err := getmeta.Summarize(out); err == nil {
		res.Meta = m
	}
	return res, nil
}

// convertRaw 在 Raw 模式下封装：JPEG 源用 EmbedJPEGVideo，AVIF 源直接 Embed。
func convertRaw(stillSrc, videoSrc []byte, opts ConvertOptions, res *ConvertResult) (*ConvertResult, error) {
	video := opts.VideoMP4
	if video == nil {
		video = videoSrc
	}
	if len(video) == 0 {
		return nil, errors.New("raw: no video data")
	}

	var out []byte
	var err error

	// 判断静态图是 JPEG 还是 AVIF
	if !motionphoto.IsAVIF(stillSrc) {
		// JPEG/PNG → 直接嵌入（不编码为 AVIF）
		eo := exif.DefaultOptions()
		if opts.Exif != nil {
			eo = *opts.Exif
		}
		out, err = avif.EmbedJPEGVideo(stillSrc, video, &eo)
	} else {
		// AVIF → 直接嵌入
		eo := exif.DefaultOptions()
		if opts.Exif != nil {
			eo = *opts.Exif
		}
		out, err = avif.Embed(avif.EmbedInput{
			StillData: stillSrc,
			VideoData: video,
			Exif:      &eo,
			Opt:       &opts.Avif,
		})
	}
	if err != nil {
		return nil, err
	}
	res.Still = stillSrc
	res.Video = video
	res.Output = out

	if m, err := getmeta.Summarize(out); err == nil {
		res.Meta = m
	}
	return res, nil
}

func applySourceExif(opt *exif.Options, src *exif.DataSet) {
	if t := src.Get("ifd0", exif.TagMake); t != nil {
		opt.Make = t.TagString(src.ByteOrder)
	}
	if t := src.Get("ifd0", exif.TagModel); t != nil {
		opt.Model = t.TagString(src.ByteOrder)
	}
	if t := src.Get("ifd0", exif.TagOrientation); t != nil {
		if v, err := parseUint(t.TagString(src.ByteOrder)); err == nil {
			opt.Orientation = uint16(v)
		}
	}
	if t := src.Get("ifd0", exif.TagDateTime); t != nil {
		opt.DateTime = t.TagString(src.ByteOrder)
	}
	if t := src.Get("exif", exif.TagDateTimeOriginal); t != nil {
		opt.DateTimeOriginal = t.TagString(src.ByteOrder)
	}
}

func parseUint(s string) (uint64, error) {
	var v uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}
