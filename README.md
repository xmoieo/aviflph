# aviflph — Motion Photo → AVIF Live Photo 转换工具

用 Go 实现：把手机拍摄的 **Motion Photo（JPEG 动态照片）** 转换为 **AVIF Live Photo**
（xomu 标准，可在小米/OPPO 等相册中作为动态照片识别与播放）。

```
Motion Photo JPEG ──► 静态图 ──进程内编码──► AVIF
                  └──► 视频   ──进程内重编码──► AV1 MP4 ──► 封装为 xomu Live Photo AVIF
```

输出为单个 `.avif` 文件，内含：

- `ftyp`：brands `avif, mif1, miaf, xomu`
- `meta`：`hdlr(pict)` / `pitm(1)` / `iloc` / `iinf` / `iref` / `iprp`
  - item 1：`av01` 静态图（含 `ispe/av1C/pixi/colr` 属性）
  - item 2：`xomu` 视频（auxl 引用 → 静态图）
  - item 3：`Exif`（含自定义标签 `XOMUPhoto=1`）
- `mdat`：`[AV1 静态图数据][AV1 视频 MP4][EXIF]`

完全不使用任何外部命令（avifenc 等不需要）。编码全在进程内完成：
静态图经 libavif（经 purego 调用原生库）编码；视频轨 HEVC → AV1 的
重编码为「h265 解码 → libavif 动画编码 → 自研 AV1+AAC MP4 封装」，
源视频为 AV1 时原样透传。

音频轨重编码保留 AAC 时调用系统 `ffmpeg`（仅 `--audio passthrough`）；
保留该选项是因为大多数源视频的 AAC 轨已经是兼容格式，但仍希望归一化
码率/采样率/声道数。`--audio none` 可完全跳过音频处理、不依赖 ffmpeg。

## 快速开始

```sh
# 构建 CLI（产物 dist/aviflph）
make cli

# 转换 Motion Photo
dist/aviflph convert -i 照片.jpg -o 照片.live.avif

# 查看 meta / 验证
dist/aviflph getmeta -i 照片.live.avif
avifdec 照片.live.avif out.png        # 静态图可正常解码
ffprobe 照片.live.avif                # 确认内嵌视频为 AV1+AAC
dist/aviflph demux -i 照片.live.avif -o 拆分后   # 还原 静态图+视频
```

## 性能

- 解码与编码均**按关键帧切段多线程并行**（解码用满所有核，编码并行段数上限 4 以兼顾码率）。
- 实测（1728×1296 HEVC，83 帧，16 核）：单线程约 27s；并行默认参数约 9.5s；
  `--crf 42 --video-preset 10 --quality 60` 约 3.9s。
- 注意：并行分片会略微增大码率（段间无参考）；文件大小主要受 `--crf` 控制。

## 命令行

```
aviflph convert  -i 输入 -o 输出 [选项]
aviflph getmeta  -i 输入 [--json]
aviflph demux    -i 输入 [-o 前缀]
aviflph version
```

convert 主要选项：

| 选项 | 默认 | 说明 |
|---|---|---|
| `-q, --quality` | 10 | AVIF 静态图质量（0-100） |
| `--speed` | 6 | 图像编码速度（0-10） |
| `--chroma` | 420 | 色度采样（420/444） |
| `--crf` | 50 | AV1 视频质量（0-63，越低越好；调高可大幅减小体积） |
| `--video-preset` | 6 | 视频编码速度（0-10） |
| `--audio` | passthrough | 音频处理（passthrough/none） |
| `--audio-bitrate` | 8k | AAC 码率（如 4k/8k/16k） |
| `--audio-samplerate` | 16000 | 采样率（Hz，0 保持源采样率） |
| `--audio-channels` | 1 | 声道数（0 保持源声道数） |
| `--xomu-tag-id/--xomu-tag-value` | 0x0002 / 1 | EXIF 自定义标签 |
| `--no-xomu-brand` | | 不加 xomu 品牌 |
| `--no-exif` | | 不写 EXIF |
| `--embed-still-avif/--embed-video-mp4` | | 使用预编码文件（跳过编码） |
| `--raw` | | 不重编码原图原视频，仅做格式封装 |
| `--tmpdir` | 系统临时 | 临时目录 |
| `--json` | | JSON 输出 |

`--audio`：`passthrough` 重编码保留源音频轨（调用 `ffmpeg`）；`none` 丢弃音频。

## C 动态库

```sh
make capi    # dist/libaviflph.so + dist/aviflph.h
```

```c
#include "aviflph.h"

int rc = lp_convert(src, src_len, NULL, 0, NULL, 0, 85, 28, &out, &out_len);
if (rc == 0) { /* 使用 out；用 lp_free(out) 释放 */ }
else fprintf(stderr, "%s\n", lp_last_error());
```

导出函数：`lp_version` / `lp_convert` / `lp_embed` / `lp_getmeta` / `lp_demux` /
`lp_extract_still` / `lp_extract_video` / `lp_free` / `lp_last_error`。
所有动态内存用 `lp_free` 释放；`lp_last_error` 返回内部缓冲，仅下次调用前有效。

## WebAssembly

```sh
make wasm    # dist/aviflph.wasm + dist/wasm_exec.js
```

```js
const go = new Go();
const { instance } = await WebAssembly.instantiateStreaming(fetch("aviflph.wasm"), go.importObject);
go.run(instance);

const meta = lp_getmeta(bytes);                 // JSON 文本
const { still, video } = lp_demux(bytes);       // Uint8Array
const out = lp_embed(still, video);             // 封装 Live Photo
```

WASM 环境的进程内编码依赖原生库（libavif/h265），故不提供 `lp_convert`；
封装请用 `lp_embed`（图片与视频需预先编码）。

## 依赖

- Go 1.26+
- 运行时（桌面/Android 构建产物）需要系统 libavif 与 h265 原生库
  （静态图/视频编码经 purego 调用）；解码验证与示例可用 `avifdec`/`ffprobe`

## 多平台打包

```sh
make cross   # 或 ./scripts/cross-build.sh [android-ndk 路径]
```

产物按平台分文件夹输出到 `dist/`：

```
dist/
├── linux-x86_64/     aviflph + libaviflph.so + aviflph.h
├── linux-aarch64/    aviflph + libaviflph.so + aviflph.h
├── android-aarch64/  aviflph + libaviflph.so + aviflph.h（NDK 构建）
├── windows-x86_64/   aviflph.exe（C 库需 mingw-w64 工具链）
├── windows-arm64/    aviflph.exe（同上）
├── darwin-arm64/     aviflph（C 库需 osxcross/macOS SDK）
└── wasm/             aviflph.wasm + wasm_exec.js
```

CLI 为纯 Go，全部平台可交叉编译；C 动态库依赖对应平台的 C 工具链
（Android 默认使用 `$ANDROID_NDK_HOME` 或 `/opt/developers/android/sdk/ndk`，
其余平台缺少工具链时脚本自动跳过并提示）。

## 目录结构

```
cmd/aviflph     CLI（convert/getmeta/demux/version）
cmd/lpshared    C 动态库构建入口
cmd/lpwasm      WASM 构建入口
capi/           C API 实现 + aviflph.h
wasm/           WASM 导出（syscall/js）
pkg/api         高层操作（Convert/Demux/GetMeta/Embed）
pkg/avif        AVIF 解析与 xomu 封装
pkg/bmff        BMFF 盒子解析/构建（地基）
pkg/motionphoto Motion Photo JPEG 检测与拆分
pkg/encoder     进程内编码（静态图/视频重编码，无外部命令）
pkg/video       视频管线（HEVC/AV1 解封装、解码、AV1+AAC MP4 封装）
pkg/exif        EXIF 构建与解析（XOMUPhoto 标签）
pkg/getmeta     meta 报告（文本/JSON）
```

## 说明

- 输出使用 `iloc` v0、绝对文件偏移（construction_method=0），视频 MP4 整体
  追加进 `mdat`，与 Xiaomi/OPPO 动态照片封装一致。
- `demux` 从任一输入（JPEG Motion Photo 或 Live Photo AVIF）提取静态图与视频。
- 已验证：`avifdec 1.4.2` 可解码产物；`ffprobe` 确认内嵌视频为
  AV1 (1296×1728@30fps) + AAC 音轨，且内嵌 MP4 完整解码无错误。