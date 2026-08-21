// Command aviflph 是 livephoto 的命令行工具。
//
// 子命令：
//
//	convert   把 Motion Photo(JPEG) 或图片+视频转换为 xomu Live Photo AVIF
//	getmeta   查看文件的 meta 信息（AVIF/JPEG Motion Photo/MP4）
//	demux     从 JPEG Motion Photo 或 Live Photo AVIF 中分解静态图与视频
//
// 用法示例：
//
//	aviflph convert -i photo.jpg -o out.avif --quality 85 --crf 28
//	aviflph getmeta -i out.avif --tree
//	aviflph demux -i out.avif -o /tmp/live
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"aviflph/pkg/api"
	"aviflph/pkg/avif"
	"aviflph/pkg/encoder"
	"aviflph/pkg/exif"
	"aviflph/pkg/getmeta"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "convert", "c":
		err = runConvert(os.Args[2:])
	case "getmeta", "meta":
		err = runGetMeta(os.Args[2:])
	case "demux", "split":
		err = runDemux(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("aviflph %s\n", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`aviflph - AVIF+AV1 Live Photo 合成工具

用法:
  aviflph convert  -i 输入文件 [-o 输出] [选项]  把 Motion Photo 转换为 AVIF+AV1 动态照片
  aviflph getmeta  -i 输入文件 [--json] [--tree]  查看 meta 信息
  aviflph demux    -i 输入文件 -o 输出前缀 [--mode auto|jpeg|avif]  分解静态图与视频
  aviflph version                           显示版本

convert 主要选项:
  -i, --input <file>         输入文件（JPEG Motion Photo / AVIF / MP4）
  -o, --output <file>        输出 AVIF（默认 输入名.live.avif）
      --still-out <file>     保存中间静态图 AVIF
      --video-out <file>     保存中间视频 MP4
      --embed-still-avif <f> 使用预编码静态图（跳过图像编码）
      --embed-video-mp4 <f>  使用预编码视频（跳过视频编码）
      --quality <0-100>      静态图质量（默认 10）
      --speed <0-10>         图像编码速度（默认 6）
      --chroma <420|444>     色度采样（默认 420）
      --crf <0-63>           视频质量（默认 50，等效 ffmpeg -crf 50）
      --video-preset <0-13>  视频编码速度（默认 6）
      --video-scale-w <px>   视频输出宽度，0 保持源宽度（默认 720）
      --video-scale-h <px>   视频输出高度，0 保持源高度（默认 960）
      --video-fps <n>        视频输出帧率（默认 15，0 保持源帧率）
      --audio <passthrough|none>   音频处理：passthrough=重编码保留(默认)，none=剥离
      --audio-bitrate <kbps>       AAC 码率，如 4k/8k/16k(默认 8k)
      --audio-samplerate <hz>      采样率(默认 16000)
      --audio-channels <n>         声道数(默认 1，单声道)
      --xomu-tag-id <hex>     EXIF 中 XOMU 标签 ID（默认 0x0002）
      --xomu-tag-value <n>    EXIF 中 XOMU 标签值（默认 1）
      --no-xomu-brand        不向 ftyp 添加 xomu 兼容品牌
      --no-exif              不写入 EXIF 信息
      --no-copy-exif         不拷贝源照片的 EXIF 字段
      --raw                  不压缩画质，保持原图原视频，只转格式封装
      --tmpdir <dir>         临时目录（默认系统临时目录）
      --quiet                减少输出

getmeta 选项:
  -i, --input <file>  输入文件
      --json          输出 JSON 格式
      --tree          同时输出盒子树
      --tree-only     仅输出盒子树

demux 选项:
  -i, --input <file>   输入文件
  -o, --output <pfx>   输出前缀（生成 <pfx>_still.<ext> 与 <pfx>_video.mp4）
      --mode <auto|jpeg|avif>  强制输入类型（默认自动检测）
`)
}

// 解析通用选项辅助函数
type opts struct {
	fs *flag.FlagSet
}

func newFS(name string, args []string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// stringArg 读取字符串 flag 值。
func flagStr(fs *flag.FlagSet, name, def string) string {
	v := fs.Lookup(name)
	if v == nil {
		return def
	}
	return v.Value.String()
}

func runConvert(args []string) error {
	fs := newFS("convert", args)
	input := fs.String("i", "", "")
	fs.StringVar(input, "input", "", "")
	output := fs.String("o", "", "")
	fs.StringVar(output, "output", "", "")
	stillOut := fs.String("still-out", "", "")
	videoOut := fs.String("video-out", "", "")
	embedStill := fs.String("embed-still-avif", "", "")
	embedVideo := fs.String("embed-video-mp4", "", "")
	quality := fs.Int("quality", 10, "")
	speed := fs.Int("speed", 6, "")
	chroma := fs.String("chroma", "420", "")
	crf := fs.Int("crf", 50, "")
	vpreset := fs.Int("video-preset", 6, "")
	vscaleW := fs.Int("video-scale-w", 720, "")
	vscaleH := fs.Int("video-scale-h", 960, "")
	vfps := fs.Int("video-fps", 15, "")
	audio := fs.String("audio", "passthrough", "")
	audioBitrate := fs.String("audio-bitrate", "8k", "")
	audioSampleRate := fs.Int("audio-samplerate", 16000, "")
	audioChannels := fs.Int("audio-channels", 1, "")
	xomuTagID := fs.String("xomu-tag-id", "0x0002", "")
	xomuVal := fs.Int("xomu-tag-value", 1, "")
	noXomuBrand := fs.Bool("no-xomu-brand", false, "")
	noExif := fs.Bool("no-exif", false, "")
	noCopyExif := fs.Bool("no-copy-exif", false, "")
	tmpdir := fs.String("tmpdir", "", "")
	quiet := fs.Bool("quiet", false, "")
	asJSON := fs.Bool("json", false, "")
	raw := fs.Bool("raw", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("缺少 -i/--input 参数")
	}
	if *output == "" {
		base := filepath.Base(*input)
		ext := filepath.Ext(base)
		*output = strings.TrimSuffix(base, ext) + ".live.avif"
	}

	// 读取输入
	src, err := os.ReadFile(*input)
	if err != nil {
		return err
	}

	// 预编码数据
	var stillAVIF, videoMP4 []byte
	if *embedStill != "" {
		stillAVIF, err = os.ReadFile(*embedStill)
		if err != nil {
			return err
		}
	}
	if *embedVideo != "" {
		videoMP4, err = os.ReadFile(*embedVideo)
		if err != nil {
			return err
		}
	}

	// 解析 XOMU 标签 ID（支持 0x 前缀）
	tagID, err := strconv.ParseUint(*xomuTagID, 0, 16)
	if err != nil {
		return fmt.Errorf("无效的 --xomu-tag-id: %v", err)
	}

	enc := encoder.DefaultOptions()
	enc.StillQuality = *quality
	enc.StillSpeed = *speed
	enc.StillChroma = *chroma
	enc.VideoCRF = *crf
	enc.VideoPreset = *vpreset
	enc.VideoScaleW = *vscaleW
	enc.VideoScaleH = *vscaleH
	enc.VideoFPS = *vfps
	enc.AudioMode = *audio
	enc.AudioBitrate = *audioBitrate
	enc.AudioSampleRate = *audioSampleRate
	enc.AudioChannels = *audioChannels
	enc.TmpDir = *tmpdir

	avifOpt := avif.DefaultOptions()
	avifOpt.AddXomuBrand = !*noXomuBrand

	var exifOpt *exif.Options
	if !*noExif {
		eo := exif.DefaultOptions()
		eo.XOMUPhotoTagID = uint16(tagID)
		eo.XOMUPhotoValue = uint64(*xomuVal)
		exifOpt = &eo
	}

	res, err := api.Convert(src, api.ConvertOptions{
		StillAVIF:          stillAVIF,
		VideoMP4:           videoMP4,
		Enc:                enc,
		Avif:               avifOpt,
		Exif:               exifOpt,
		CopyExifFromSource: !*noCopyExif && exifOpt != nil,
		WorkDir:            *tmpdir,
		Raw:                *raw,
	})
	if err != nil {
		return err
	}

	if *stillOut != "" {
		if err := api.WriteFile(*stillOut, res.Still); err != nil {
			return err
		}
	}
	if *videoOut != "" {
		if err := api.WriteFile(*videoOut, res.Video); err != nil {
			return err
		}
	}
	if err := api.WriteFile(*output, res.Output); err != nil {
		return err
	}

	if *asJSON {
		// 输出 JSON 结果
		result := map[string]any{
			"output": *output,
			"bytes":  len(res.Output),
			"still":  len(res.Still),
			"video":  len(res.Video),
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(b))
	} else if !*quiet {
		fmt.Printf("已生成: %s (%.2f MB)\n", *output, float64(len(res.Output))/1048576)
		if res.Meta != nil {
			fmt.Println(res.Meta.Text())
		}
	}
	return nil
}

func runGetMeta(args []string) error {
	fs := newFS("getmeta", args)
	input := fs.String("i", "", "")
	fs.StringVar(input, "input", "", "")
	asJSON := fs.Bool("json", false, "")
	tree := fs.Bool("tree", false, "")
	treeOnly := fs.Bool("tree-only", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("缺少 -i/--input 参数")
	}
	src, err := os.ReadFile(*input)
	if err != nil {
		return err
	}

	if *treeOnly {
		fmt.Print(getmeta.BoxTree(src))
		return nil
	}
	s, err := api.GetMeta(src, map[bool]string{true: "json"}[*asJSON])
	if err != nil {
		return err
	}
	fmt.Println(s)
	if *tree {
		fmt.Println("\n[盒子树]")
		fmt.Print(getmeta.BoxTree(src))
	}
	return nil
}

func runDemux(args []string) error {
	fs := newFS("demux", args)
	input := fs.String("i", "", "")
	fs.StringVar(input, "input", "", "")
	output := fs.String("o", "", "")
	fs.StringVar(output, "output", "", "")
	mode := fs.String("mode", "auto", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = mode // 目前自动检测已足够；mode 保留用于未来强制指定
	if *input == "" {
		return fmt.Errorf("缺少 -i/--input 参数")
	}
	if *output == "" {
		base := filepath.Base(*input)
		ext := filepath.Ext(base)
		*output = strings.TrimSuffix(base, ext)
	}
	src, err := os.ReadFile(*input)
	if err != nil {
		return err
	}

	res, err := api.Demux(src)
	if err != nil {
		return err
	}
	stillPath := *output + "_still" + res.StillExt
	videoPath := *output + "_video" + res.VideoExt
	if err := api.WriteFile(stillPath, res.Still); err != nil {
		return err
	}
	if err := api.WriteFile(videoPath, res.Video); err != nil {
		return err
	}
	fmt.Printf("静态图: %s (%d 字节)\n", stillPath, len(res.Still))
	fmt.Printf("视频:   %s (%d 字节)\n", videoPath, len(res.Video))
	return nil
}
