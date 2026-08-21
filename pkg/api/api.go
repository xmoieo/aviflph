// Package api 提供 aviflph 的高层操作，供命令行、C 动态库与 WASM 共用。
package api

import (
	"errors"
	"os"
	"path/filepath"

	"aviflph/pkg/avif"
	"aviflph/pkg/exif"
	"aviflph/pkg/getmeta"
	"aviflph/pkg/motionphoto"
)

// DemuxResult 是分解结果。
type DemuxResult struct {
	Still    []byte
	Video    []byte
	StillExt string
	VideoExt string
	ExifTIFF []byte
}

// Demux 从 JPEG Motion Photo 或 Live Photo AVIF 中分解静态图与视频。
func Demux(src []byte) (*DemuxResult, error) {
	switch {
	case motionphoto.IsAVIF(src):
		dm, err := avif.Demux(src)
		if err != nil {
			return nil, err
		}
		return &DemuxResult{
			Still:    dm.StillAVIF,
			Video:    dm.Video,
			StillExt: ".avif",
			VideoExt: ".mp4",
			ExifTIFF: dm.ExifTIFF,
		}, nil
	case motionphoto.IsJPEG(src):
		still, err := motionphoto.ExtractStill(src)
		if err != nil {
			return nil, err
		}
		video, err := motionphoto.ExtractVideo(src)
		if err != nil {
			return nil, err
		}
		return &DemuxResult{Still: still, Video: video, StillExt: ".jpg", VideoExt: ".mp4"}, nil
	default:
		return nil, errors.New("demux: input is not a jpeg motion photo or avif live photo")
	}
}

// EmbedOnly 用已编码的静态图 AVIF 与视频 MP4 直接封装 Live Photo AVIF。
func EmbedOnly(stillAVIF, videoMP4 []byte) ([]byte, error) {
	eo := exif.DefaultOptions()
	ao := avif.DefaultOptions()
	return avif.Embed(avif.EmbedInput{
		StillData: stillAVIF,
		VideoData: videoMP4,
		Exif:      &eo,
		Opt:       &ao,
	})
}

// GetMeta 查看 meta 信息。
func GetMeta(src []byte, format string) (string, error) {
	r, err := getmeta.Summarize(src)
	if err != nil {
		return "", err
	}
	if format == "json" {
		return r.JSON()
	}
	return r.Text(), nil
}

// ExtractStill 从任意二进制中提取静态图。
func ExtractStill(src []byte) ([]byte, string, error) {
	switch {
	case motionphoto.IsAVIF(src):
		d, err := avif.ExtractStillFromAVIF(src)
		if err != nil {
			return nil, "", err
		}
		return d, ".avif", nil
	case motionphoto.IsJPEG(src):
		d, err := motionphoto.ExtractStill(src)
		if err != nil {
			return nil, "", err
		}
		return d, ".jpg", nil
	default:
		return nil, "", errors.New("extract: unsupported input")
	}
}

// ExtractVideo 从任意二进制中提取视频。
func ExtractVideo(src []byte) ([]byte, error) {
	switch {
	case motionphoto.IsAVIF(src):
		return avif.ExtractVideoFromAVIF(src)
	case motionphoto.IsJPEG(src):
		return motionphoto.ExtractVideo(src)
	default:
		return nil, errors.New("extract: unsupported input")
	}
}

// WriteFile 便捷写文件工具。
func WriteFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}
