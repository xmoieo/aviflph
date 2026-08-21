#!/usr/bin/env bash
# 一键构建 aviflph 工具：CLI / C 动态库 / WASM。
# 产物输出到 dist/ 目录。
#
# 用法:
#   ./scripts/build.sh [cli|capi|wasm|all]
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"
DIST="$ROOT/dist"
mkdir -p "$DIST"

# CLI 路径
CLI_OUT="${CLI_OUT:-$DIST/aviflph}"

echo ">> 纯进程内编码：不依赖 avifenc/ffmpeg 等外部命令"

build_cli() {
  echo ">> building CLI -> $CLI_OUT"
  go build -trimpath -ldflags "-s -w" -o "$CLI_OUT" ./cmd/aviflph
  ls -la "$CLI_OUT"
}

build_capi() {
  echo ">> building C shared library -> $DIST"
  go build -buildmode=c-shared -o "$DIST/libaviflph.so" ./cmd/lpshared
  cp capi/aviflph.h "$DIST/aviflph.h"
  ls -la "$DIST/libaviflph.so" "$DIST/aviflph.h"
  echo "   OS X:  make -C scripts darwin    (产出 libaviflph.dylib)"
  echo "   Win:   make -C scripts windows   (产出 aviflph.dll)"
}

build_wasm() {
  echo ">> building WASM -> $DIST/aviflph.wasm"
  GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o "$DIST/aviflph.wasm" ./cmd/lpwasm
  # 复制 wasm_exec.js 运行时
  WASM_EXEC="$(go env GOROOT)/lib/wasm/wasm_exec.js"
  if [ -f "$WASM_EXEC" ]; then
    cp "$WASM_EXEC" "$DIST/wasm_exec.js"
  fi
  ls -la "$DIST/aviflph.wasm"
}

case "${1:-all}" in
  cli)   build_cli ;;
  capi)  build_capi ;;
  wasm)  build_wasm ;;
  all)   build_cli; build_capi; build_wasm ;;
  *) echo "unknown target: $1 (cli|capi|wasm|all)"; exit 1 ;;
esac

echo "done."
