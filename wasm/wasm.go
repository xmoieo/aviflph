//go:build js && wasm

// Package wasm 导出 Live Photo 工具的 WebAssembly 接口。
//
// 导出的全局函数：
//
//	lp_version()                        -> string
//	lp_getmeta(bytes)                   -> string（JSON）
//	lp_demux(bytes)                     -> { still, video, stillExt, videoExt }
//	lp_extract_still(bytes)             -> { data, ext }
//	lp_extract_video(bytes)             -> Uint8Array
//	lp_encode_still(bytes, quality, speed) -> Uint8Array（AVIF）
//	lp_embed(stillBytes, videoBytes)    -> Uint8Array
package wasm

import (
	"bytes"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"syscall/js"

	"aviflph/pkg/api"
	avifpkg "github.com/gen2brain/avif"
)

// init 在模块加载时向全局注册导出函数。
func init() {
	g := js.Global()
	g.Set("lp_version", js.FuncOf(func(this js.Value, args []js.Value) any {
		return "1.0.0"
	}))
	g.Set("lp_getmeta", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			throw("lp_getmeta: missing src argument")
		}
		data := toBytes(args[0])
		s, err := api.GetMeta(data, "json")
		if err != nil {
			throw(err.Error())
		}
		return s
	}))
	g.Set("lp_demux", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			throw("lp_demux: missing src argument")
		}
		res, err := api.Demux(toBytes(args[0]))
		if err != nil {
			throw(err.Error())
		}
		return map[string]any{
			"still":    toJS(res.Still),
			"video":    toJS(res.Video),
			"stillExt": res.StillExt,
			"videoExt": res.VideoExt,
		}
	}))
	g.Set("lp_extract_still", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			throw("lp_extract_still: missing src argument")
		}
		data, ext, err := api.ExtractStill(toBytes(args[0]))
		if err != nil {
			throw(err.Error())
		}
		return map[string]any{"data": toJS(data), "ext": ext}
	}))
	g.Set("lp_extract_video", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			throw("lp_extract_video: missing src argument")
		}
		data, err := api.ExtractVideo(toBytes(args[0]))
		if err != nil {
			throw(err.Error())
		}
		return toJS(data)
	}))
	g.Set("lp_embed", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 2 {
			throw("lp_embed: missing still/video arguments")
		}
		out, err := api.EmbedOnly(toBytes(args[0]), toBytes(args[1]))
		if err != nil {
			throw(err.Error())
		}
		return toJS(out)
	}))

	// lp_encode_still(imageBytes, quality, speed) -> Uint8Array（AVIF）
	// 将 JPEG/PNG 图片编码为 AVIF 静态图。
	g.Set("lp_encode_still", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			throw("lp_encode_still: missing image argument")
		}
		imgBytes := toBytes(args[0])
		quality := 10
		speed := 6
		if len(args) >= 2 && args[1].Int() > 0 {
			quality = args[1].Int()
		}
		if len(args) >= 3 && args[2].Int() > 0 {
			speed = args[2].Int()
		}

		img, _, err := image.Decode(bytes.NewReader(imgBytes))
		if err != nil {
			throw("lp_encode_still: decode: " + err.Error())
			return nil
		}

		var buf bytes.Buffer
		err = avifpkg.Encode(&buf, img, avifpkg.Options{
			Quality: quality,
			Speed:   speed,
		})
		if err != nil {
			throw("lp_encode_still: encode: " + err.Error())
			return nil
		}
		return toJS(buf.Bytes())
	}))
}

// throw 设置全局错误变量，供 JS 端检查。
func throw(msg string) {
	js.Global().Set("__lp_error", msg)
}

// toBytes 把 JS 的 Uint8Array/ArrayBuffer 转为 Go 字节切片。
func toBytes(v js.Value) []byte {
	switch v.Type() {
	case js.TypeObject:
		if v.InstanceOf(js.Global().Get("ArrayBuffer")) || v.InstanceOf(js.Global().Get("Uint8Array")) {
			buf := make([]byte, v.Get("byteLength").Int())
			js.CopyBytesToGo(buf, v)
			return buf
		}
	}
	throw("expected Uint8Array or ArrayBuffer")
	return nil
}

// toJS 把 Go 字节切片包装为 JS 的 Uint8Array。
func toJS(data []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(arr, data)
	return arr
}
