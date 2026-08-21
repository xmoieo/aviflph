//go:build cgo

// Package capi 导出 Live Photo 转换工具的 C 动态库接口。
//
// 构建（生成 libaviflph.so / libaviflph.dylib / aviflph.dll）：
//
//	go build -buildmode=c-shared -o libaviflph.so ./cmd/lpshared
//
// 调用方需包含本包提供的 aviflph.h 头文件，并通过 lp_free 释放
// 所有由本库返回的动态内存。
//
// 线程安全：除 lp_last_error 外，各函数均可并发调用。
// lp_last_error 返回的是内部缓冲，仅在下次调用前有效。
package capi

/*
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stddef.h>
*/
import "C"

import (
	"runtime"
	"unsafe"

	"aviflph/pkg/api"
	"aviflph/pkg/avif"
	"aviflph/pkg/encoder"
	"aviflph/pkg/exif"
)

const version = "1.0.0"

// 包级状态：错误信息缓冲（避免每次错误都泄漏一块 C 内存）。
var (
	errBuf = (*C.char)(C.malloc(1024))
)

// lp_version 返回版本号字符串（静态，无需释放）。
//
//export lp_version
func lp_version() *C.char {
	return C.CString(version)
}

// lp_setErr 把最近的错误写入内部缓冲。
func lp_setErr(msg string) {
	s := C.CString(msg)
	C.strncpy(errBuf, s, 1023)
	C.free(unsafe.Pointer(s))
}

// lp_last_error 返回最近一次错误的说明（内部缓冲，下次调用前有效）。
//
//export lp_last_error
func lp_last_error() *C.char {
	return errBuf
}

// lp_free 释放由本库分配的内存（lp_convert/lp_getmeta/lp_demux 等的输出）。
//
//export lp_free
func lp_free(ptr unsafe.Pointer) {
	if ptr != nil {
		C.free(ptr)
	}
}

// lp_convert 执行完整转换：
//   - src 为 JPEG Motion Photo 时自动拆分编码；
//   - src 为静态图 AVIF 时需提供 video_mp4；
//   - src 为视频 MP4 时需提供 still_avif；
//   - 也可同时提供 still_avif + video_mp4 而忽略 src。
//
// raw 非 0 时不压缩画质，保持原图原视频，只转格式封装。
//
// 返回 0 表示成功，out 指向分配的输出字节（用 lp_free 释放）；
// 返回 -1 表示失败，错误见 lp_last_error。
//
//export lp_convert
func lp_convert(
	src unsafe.Pointer, src_len C.size_t,
	still_avif unsafe.Pointer, still_avif_len C.size_t,
	video_mp4 unsafe.Pointer, video_mp4_len C.size_t,
	quality C.int, crf C.int,
	raw C.int,
	out *unsafe.Pointer, out_len *C.size_t,
) C.int {
	eo := exif.DefaultOptions()
	ao := avif.DefaultOptions()
	enc := encoder.DefaultOptions()
	if quality > 0 {
		enc.StillQuality = int(quality)
	}
	if crf > 0 {
		enc.VideoCRF = int(crf)
	}
	res, err := api.Convert(copyBytes(src, src_len), api.ConvertOptions{
		StillAVIF:          copyBytes(still_avif, still_avif_len),
		VideoMP4:           copyBytes(video_mp4, video_mp4_len),
		Enc:                enc,
		Avif:               ao,
		Exif:               &eo,
		CopyExifFromSource: true,
		Raw:                raw != 0,
	})
	if err != nil {
		lp_setErr(err.Error())
		return -1
	}
	if out != nil {
		*out = C.CBytes(res.Output)
	}
	if out_len != nil {
		*out_len = C.size_t(len(res.Output))
	}
	runtime.KeepAlive(res)
	return 0
}

// lp_embed 用已编码的静态图 AVIF 与视频 MP4 直接封装 Live Photo AVIF
// （不再调用外部编码器）。输出用 lp_free 释放。
//
//export lp_embed
func lp_embed(
	still unsafe.Pointer, still_len C.size_t,
	video unsafe.Pointer, video_len C.size_t,
	out *unsafe.Pointer, out_len *C.size_t,
) C.int {
	eo := exif.DefaultOptions()
	ao := avif.DefaultOptions()
	enc, err := avif.Embed(avif.EmbedInput{
		StillData: copyBytes(still, still_len),
		VideoData: copyBytes(video, video_len),
		Exif:      &eo,
		Opt:       &ao,
	})
	if err != nil {
		lp_setErr(err.Error())
		return -1
	}
	if out != nil {
		*out = C.CBytes(enc)
	}
	if out_len != nil {
		*out_len = C.size_t(len(enc))
	}
	return 0
}

// lp_getmeta 返回输入文件的 meta 报告文本。json 非 0 时返回 JSON。
// 返回值用 lp_free 释放。
//
//export lp_getmeta
func lp_getmeta(src unsafe.Pointer, src_len C.size_t, as_json C.int) *C.char {
	s, err := api.GetMeta(copyBytes(src, src_len), map[bool]string{false: "", true: "json"}[as_json != 0])
	if err != nil {
		lp_setErr(err.Error())
		return nil
	}
	return C.CString(s)
}

// lp_demux 从 JPEG Motion Photo 或 Live Photo AVIF 分解静态图与视频。
// still/video 输出用 lp_free 释放；扩展名字符串同样由 lp_free 释放。
// 返回 0 成功，-1 失败。
//
//export lp_demux
func lp_demux(
	src unsafe.Pointer, src_len C.size_t,
	still *unsafe.Pointer, still_len *C.size_t, still_ext **C.char,
	video *unsafe.Pointer, video_len *C.size_t, video_ext **C.char,
) C.int {
	res, err := api.Demux(copyBytes(src, src_len))
	if err != nil {
		lp_setErr(err.Error())
		return -1
	}
	if still != nil {
		*still = C.CBytes(res.Still)
	}
	if still_len != nil {
		*still_len = C.size_t(len(res.Still))
	}
	if still_ext != nil {
		*still_ext = C.CString(res.StillExt)
	}
	if video != nil {
		*video = C.CBytes(res.Video)
	}
	if video_len != nil {
		*video_len = C.size_t(len(res.Video))
	}
	if video_ext != nil {
		*video_ext = C.CString(res.VideoExt)
	}
	return 0
}

// lp_extract_still 从任意输入提取静态图。输出与扩展名用 lp_free 释放。
//
//export lp_extract_still
func lp_extract_still(
	src unsafe.Pointer, src_len C.size_t,
	out *unsafe.Pointer, out_len *C.size_t, ext **C.char,
) C.int {
	data, e, err := api.ExtractStill(copyBytes(src, src_len))
	if err != nil {
		lp_setErr(err.Error())
		return -1
	}
	if out != nil {
		*out = C.CBytes(data)
	}
	if out_len != nil {
		*out_len = C.size_t(len(data))
	}
	if ext != nil {
		*ext = C.CString(e)
	}
	return 0
}

// lp_extract_video 从任意输入提取视频。输出用 lp_free 释放。
//
//export lp_extract_video
func lp_extract_video(
	src unsafe.Pointer, src_len C.size_t,
	out *unsafe.Pointer, out_len *C.size_t,
) C.int {
	data, err := api.ExtractVideo(copyBytes(src, src_len))
	if err != nil {
		lp_setErr(err.Error())
		return -1
	}
	if out != nil {
		*out = C.CBytes(data)
	}
	if out_len != nil {
		*out_len = C.size_t(len(data))
	}
	return 0
}

// copyBytes 把 C 侧缓冲区拷贝为 Go 切片（避免 C 内存在 GC 后失效）。
func copyBytes(p unsafe.Pointer, n C.size_t) []byte {
	if p == nil || n == 0 {
		return nil
	}
	return C.GoBytes(p, C.int(n))
}
