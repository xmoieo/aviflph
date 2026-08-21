#!/usr/bin/env bash

# 跨平台打包：
#
#   Windows  x86_64 / arm64
#   Linux    x86_64 / aarch64
#   macOS    arm64
#   Android  aarch64
#   WASM
#
# 产物：
#
#   dist/
#     linux-x86_64/
#     linux-aarch64/
#     android-aarch64/
#     windows-x86_64/
#     windows-arm64/
#     darwin-arm64/
#     wasm/
#
# CLI：
#   纯 Go，可交叉编译
#
# C 动态库：
#
#   Linux x86_64
#       gcc
#
#   Linux ARM64
#       aarch64-linux-gnu-gcc
#
#   Android ARM64
#       Android NDK
#
#   Windows x86_64
#       LLVM-MinGW
#
#   Windows ARM64
#       LLVM-MinGW
#
#   macOS ARM64
#       osxcross + macOS SDK
#
#
# 用法：
#
#   ./scripts/cross-build.sh
#
# 或：
#
#   ./scripts/cross-build.sh /path/to/android-ndk
#
# 或：
#
#   ANDROID_NDK_HOME=/path/to/android-ndk ./scripts/cross-build.sh
#

set -euo pipefail


###############################################################################
# 基础目录
###############################################################################

cd "$(dirname "$0")/.."

ROOT="$(pwd)"
DIST="$ROOT/dist"
CROSS_TOOLS="$ROOT/.cross-tools"

mkdir -p "$DIST"
mkdir -p "$CROSS_TOOLS"

LDFLAGS="-s -w"


###############################################################################
# Android NDK
###############################################################################

NDK=""

if [ -n "${ANDROID_NDK_HOME:-}" ] &&
   [ -d "$ANDROID_NDK_HOME" ]; then

    NDK="$ANDROID_NDK_HOME"

elif [ -n "${1:-}" ] &&
     [ -d "$1" ]; then

    NDK="$1"

else

    NDK=$(
        ls -d /opt/developers/android/sdk/ndk/* 2>/dev/null |
            sort -V |
            tail -1 ||
            true
    )

fi


NDK_BIN=""
NDK_CC=""


if [ -n "$NDK" ] &&
   [ -d "$NDK/toolchains/llvm/prebuilt" ]; then

    NDK_BIN=$(
        find "$NDK/toolchains/llvm/prebuilt" \
            -maxdepth 1 \
            -type d \
            -name "*linux-x86_64" |
            head -1 ||
            true
    )

fi


if [ -n "$NDK_BIN" ] &&
   [ -d "$NDK_BIN/bin" ]; then

    NDK_CC=$(
        find "$NDK_BIN/bin" \
            -maxdepth 1 \
            -type f \
            -name "aarch64-linux-android*-clang" |
            sort -V |
            tail -1 ||
            true
    )

fi


###############################################################################
# Linux ARM64
###############################################################################

LINUX_ARM64_CC=""

if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
    LINUX_ARM64_CC="$(command -v aarch64-linux-gnu-gcc)"
fi


###############################################################################
# LLVM-MinGW
#
# 你的安装：
#
#   /opt/llvm-mingw/llvm-mingw-msvcrt/bin/clang
#
# LLVM-MinGW 是单一 clang 工具链，可以通过 target：
#
#   x86_64-w64-windows-gnu
#   aarch64-w64-windows-gnu
#
# 编译两个 Windows 架构。
#
###############################################################################

LLVM_MINGW_ROOT=""

if [ -x "/opt/llvm-mingw/llvm-mingw-msvcrt/bin/clang" ]; then

    LLVM_MINGW_ROOT="/opt/llvm-mingw/llvm-mingw-msvcrt"

elif [ -x "/opt/llvm-mingw/bin/clang" ]; then

    LLVM_MINGW_ROOT="/opt/llvm-mingw"

fi


LLVM_MINGW_CLANG=""

if [ -n "$LLVM_MINGW_ROOT" ] &&
   [ -x "$LLVM_MINGW_ROOT/bin/clang" ]; then

    LLVM_MINGW_CLANG="$LLVM_MINGW_ROOT/bin/clang"

fi


###############################################################################
# 创建 LLVM-MinGW wrapper
#
# 为什么需要 wrapper？
#
# Go external linker 在 Windows 下会检查：
#
#     clang --version
#
# 如果没有正确识别到 LLD，
# Go 可能额外传：
#
#     -Wl,-T,/tmp/go-link-xxxx/fix_debug_gdb_scripts.ld
#
# LLVM-MinGW 使用 LLD/COFF，
# 这个 GNU linker script 参数不能直接使用。
#
# wrapper 在 --version 时明确暴露 LLD，
# 让 Go 跳过这个 linker script。
#
###############################################################################

WIN_X64_CC=""
WIN_ARM64_CC=""

if [ -n "$LLVM_MINGW_CLANG" ]; then

    WIN_X64_CC="$CROSS_TOOLS/x86_64-w64-mingw32-clang"
    WIN_ARM64_CC="$CROSS_TOOLS/aarch64-w64-mingw32-clang"


    ###########################################################################
    # Windows x86_64 wrapper
    ###########################################################################

    cat > "$WIN_X64_CC" <<EOF
#!/usr/bin/env bash

REAL_CLANG="$LLVM_MINGW_CLANG"

if [ "\${1:-}" = "--version" ]; then
    echo "LLVM-MinGW clang with LLD"
    "\$REAL_CLANG" "\$@"
    exit \$?
fi

exec "\$REAL_CLANG" --target=x86_64-w64-windows-gnu "\$@"
EOF

    chmod +x "$WIN_X64_CC"


    ###########################################################################
    # Windows ARM64 wrapper
    ###########################################################################

    cat > "$WIN_ARM64_CC" <<EOF
#!/usr/bin/env bash

REAL_CLANG="$LLVM_MINGW_CLANG"

if [ "\${1:-}" = "--version" ]; then
    echo "LLVM-MinGW clang with LLD"
    "\$REAL_CLANG" "\$@"
    exit \$?
fi

exec "\$REAL_CLANG" --target=aarch64-w64-windows-gnu "\$@"
EOF

    chmod +x "$WIN_ARM64_CC"

fi


###############################################################################
# Windows toolchain状态
###############################################################################

HAVE_WIN_X64=0
HAVE_WIN_ARM64=0

if [ -n "$WIN_X64_CC" ] &&
   [ -x "$WIN_X64_CC" ]; then

    HAVE_WIN_X64=1

fi


if [ -n "$WIN_ARM64_CC" ] &&
   [ -x "$WIN_ARM64_CC" ]; then

    HAVE_WIN_ARM64=1

fi


###############################################################################
# macOS / osxcross
###############################################################################

DARWIN_CC=""

#
# 你的 osxcross 实际路径：
#
#   /usr/local/osx-ndk-x86/bin/oa64-clang
#

if [ -x "/usr/local/osx-ndk-x86/bin/oa64-clang" ]; then

    DARWIN_CC="/usr/local/osx-ndk-x86/bin/oa64-clang"

elif command -v oa64-clang >/dev/null 2>&1; then

    DARWIN_CC="$(command -v oa64-clang)"

fi


HAVE_DARWIN_CC=0

if [ -n "$DARWIN_CC" ] &&
   [ -x "$DARWIN_CC" ]; then

    HAVE_DARWIN_CC=1

fi


###############################################################################
# macOS strip
###############################################################################

DARWIN_STRIP=""

#
# 你实际拥有：
#
#   aarch64-apple-darwin20.2-strip
#   arm64-apple-darwin20.2-strip
#
# 并且它们最终指向：
#
#   arm64-apple-darwin20.2-wrapper
#

if [ -x "/usr/local/osx-ndk-x86/bin/aarch64-apple-darwin20.2-strip" ]; then

    DARWIN_STRIP="/usr/local/osx-ndk-x86/bin/aarch64-apple-darwin20.2-strip"

elif [ -x "/usr/local/osx-ndk-x86/bin/arm64-apple-darwin20.2-strip" ]; then

    DARWIN_STRIP="/usr/local/osx-ndk-x86/bin/arm64-apple-darwin20.2-strip"

elif command -v aarch64-apple-darwin20.2-strip >/dev/null 2>&1; then

    DARWIN_STRIP="$(command -v aarch64-apple-darwin20.2-strip)"

elif command -v arm64-apple-darwin20.2-strip >/dev/null 2>&1; then

    DARWIN_STRIP="$(command -v arm64-apple-darwin20.2-strip)"

elif command -v aarch64-apple-darwin-strip >/dev/null 2>&1; then

    DARWIN_STRIP="$(command -v aarch64-apple-darwin-strip)"

elif command -v arm64-apple-darwin-strip >/dev/null 2>&1; then

    DARWIN_STRIP="$(command -v arm64-apple-darwin-strip)"

elif command -v oa64-strip >/dev/null 2>&1; then

    DARWIN_STRIP="$(command -v oa64-strip)"

fi


###############################################################################
# Linux ARM64 strip
###############################################################################

LINUX_ARM64_STRIP=""

if command -v aarch64-linux-gnu-strip >/dev/null 2>&1; then

    LINUX_ARM64_STRIP="$(command -v aarch64-linux-gnu-strip)"

fi


###############################################################################
# 输出工具链
###############################################################################

echo
echo "========== Toolchains =========="

echo "Android NDK : ${NDK:-<none>}"
echo "Android CC  : ${NDK_CC:-<none>}"

echo

if [ -n "$LINUX_ARM64_CC" ]; then
    echo "Linux ARM64 : available"
    echo "  CC: $LINUX_ARM64_CC"
else
    echo "Linux ARM64 : missing"
fi

echo

if [ "$HAVE_WIN_X64" = "1" ]; then

    echo "Windows x64 : available"
    echo "  LLVM-MinGW: $LLVM_MINGW_CLANG"
    echo "  target: x86_64-w64-windows-gnu"

else

    echo "Windows x64 : missing"

fi

echo

if [ "$HAVE_WIN_ARM64" = "1" ]; then

    echo "Windows ARM64 : available"
    echo "  LLVM-MinGW: $LLVM_MINGW_CLANG"
    echo "  target: aarch64-w64-windows-gnu"

else

    echo "Windows ARM64 : missing"

fi

echo

if [ "$HAVE_DARWIN_CC" = "1" ]; then

    echo "macOS ARM64 : available"
    echo "  CC: $DARWIN_CC"

    if [ -n "$DARWIN_STRIP" ]; then
        echo "  strip: $DARWIN_STRIP"
    fi

else

    echo "macOS ARM64 : missing"

fi

echo "================================"
echo


###############################################################################
# build_dir
#
# 参数：
#
#   $1  dir
#   $2  cgo
#   $3  cc
#   $4  goos
#   $5  goarch
#   $6  cli extension
#   $7  library extension
#   $8  cli cgo
#   $9  cli cc
#   $10 strip tool
#
###############################################################################

build_dir() {

    local dir="$1"
    local cgo="$2"
    local cc="$3"
    local goos="$4"
    local goarch="$5"
    local ext="$6"
    local lib_ext="$7"
    local cli_cgo="${8:-0}"
    local cli_cc="${9:-}"
    local strip_tool="${10:-}"

    local out="$DIST/$dir"


    ###########################################################################
    # 清理旧产物
    #
    # 防止：
    #
    # windows-x86_64/
    #   libaviflph.dll
    #   libaviflph.so   <-- 旧文件
    #
    # 这种脏文件继续残留。
    ###########################################################################

    rm -rf "$out"
    mkdir -p "$out"


    echo
    echo "==> $dir"
    echo "    GOOS=$goos"
    echo "    GOARCH=$goarch"


    ###########################################################################
    # CLI
    ###########################################################################

    echo "    -> CLI"


    if [ "$cli_cgo" = "1" ]; then

        if [ -z "$cli_cc" ]; then

            echo "ERROR: CLI requires a C compiler"
            return 1

        fi


        GOOS="$goos" \
        GOARCH="$goarch" \
        CGO_ENABLED=1 \
        CC="$cli_cc" \
            go build \
                -trimpath \
                -ldflags "$LDFLAGS" \
                -o "$out/aviflph$ext" \
                ./cmd/aviflph

    else

        GOOS="$goos" \
        GOARCH="$goarch" \
        CGO_ENABLED=0 \
            go build \
                -trimpath \
                -ldflags "$LDFLAGS" \
                -o "$out/aviflph$ext" \
                ./cmd/aviflph

    fi


    ###########################################################################
    # C shared library
    ###########################################################################

    if [ "$cgo" = "1" ] &&
       [ -n "$cc" ]; then


        local lib="$out/libaviflph$lib_ext"


        echo "    -> C shared library"
        echo "       CC=$cc"
        echo "       OUT=$lib"


        GOOS="$goos" \
        GOARCH="$goarch" \
        CGO_ENABLED=1 \
        CC="$cc" \
            go build \
                -buildmode=c-shared \
                -trimpath \
                -ldflags "$LDFLAGS" \
                -o "$lib" \
                ./cmd/lpshared


        #######################################################################
        # strip
        #######################################################################

        if [ -n "$strip_tool" ] &&
           [ -x "$strip_tool" ]; then

            echo "    -> strip: $strip_tool"


            if "$strip_tool" \
                --strip-unneeded \
                "$lib" >/dev/null 2>&1; then

                :

            elif "$strip_tool" \
                "$lib" >/dev/null 2>&1; then

                :

            else

                echo "       strip failed, keeping unstripped library"

            fi

        else

            echo "    -> strip: unavailable, skipped"

        fi


        #######################################################################
        # header
        #######################################################################

        cp capi/aviflph.h "$out/aviflph.h"


    else

        echo "    -> C shared library: skipped"

    fi
}


###############################################################################
# Linux x86_64
###############################################################################

build_dir \
    linux-x86_64 \
    1 \
    "gcc" \
    linux \
    amd64 \
    "" \
    ".so" \
    0 \
    "" \
    "$(command -v strip 2>/dev/null || true)"


###############################################################################
# Linux ARM64
###############################################################################

if [ -n "$LINUX_ARM64_CC" ]; then

    build_dir \
        linux-aarch64 \
        1 \
        "$LINUX_ARM64_CC" \
        linux \
        arm64 \
        "" \
        ".so" \
        0 \
        "" \
        "$LINUX_ARM64_STRIP"

else

    build_dir \
        linux-aarch64 \
        0 \
        "" \
        linux \
        arm64 \
        "" \
        ".so"

    echo
    echo "    (提示: 安装 aarch64-linux-gnu-gcc 后可产出 Linux ARM64 的 libaviflph.so)"

fi


###############################################################################
# Android ARM64
###############################################################################

if [ -n "$NDK_CC" ]; then

    ANDROID_STRIP=""

    if [ -x "$NDK_BIN/bin/llvm-strip" ]; then
        ANDROID_STRIP="$NDK_BIN/bin/llvm-strip"
    fi


    build_dir \
        android-aarch64 \
        1 \
        "$NDK_CC" \
        android \
        arm64 \
        "" \
        ".so" \
        1 \
        "$NDK_CC" \
        "$ANDROID_STRIP"

else

    build_dir \
        android-aarch64 \
        0 \
        "" \
        android \
        arm64 \
        "" \
        ".so"

    echo
    echo "    (提示: 安装 Android NDK 后可产出 Android ARM64 的 libaviflph.so)"

fi


###############################################################################
# Windows x86_64
###############################################################################

if [ "$HAVE_WIN_X64" = "1" ]; then

    WIN_X64_STRIP=""

    if [ -x "$LLVM_MINGW_ROOT/bin/llvm-strip" ]; then
        WIN_X64_STRIP="$LLVM_MINGW_ROOT/bin/llvm-strip"
    fi


    build_dir \
        windows-x86_64 \
        1 \
        "$WIN_X64_CC" \
        windows \
        amd64 \
        ".exe" \
        ".dll" \
        0 \
        "" \
        "$WIN_X64_STRIP"

else

    build_dir \
        windows-x86_64 \
        0 \
        "" \
        windows \
        amd64 \
        ".exe" \
        ".dll"

    echo
    echo "    (提示: LLVM-MinGW 未找到，跳过 Windows x86_64 DLL)"

fi


###############################################################################
# Windows ARM64
###############################################################################

if [ "$HAVE_WIN_ARM64" = "1" ]; then

    WIN_ARM64_STRIP=""

    if [ -x "$LLVM_MINGW_ROOT/bin/llvm-strip" ]; then
        WIN_ARM64_STRIP="$LLVM_MINGW_ROOT/bin/llvm-strip"
    fi


    build_dir \
        windows-arm64 \
        1 \
        "$WIN_ARM64_CC" \
        windows \
        arm64 \
        ".exe" \
        ".dll" \
        0 \
        "" \
        "$WIN_ARM64_STRIP"

else

    build_dir \
        windows-arm64 \
        0 \
        "" \
        windows \
        arm64 \
        ".exe" \
        ".dll"

    echo
    echo "    (提示: LLVM-MinGW 未找到，跳过 Windows ARM64 DLL)"

fi


###############################################################################
# macOS ARM64
###############################################################################

if [ "$HAVE_DARWIN_CC" = "1" ]; then

    build_dir \
        darwin-arm64 \
        1 \
        "$DARWIN_CC" \
        darwin \
        arm64 \
        "" \
        ".dylib" \
        0 \
        "" \
        "$DARWIN_STRIP"

else

    build_dir \
        darwin-arm64 \
        0 \
        "" \
        darwin \
        arm64 \
        "" \
        ".dylib"

    echo
    echo "    (提示: 安装 osxcross + macOS SDK 后可产出 macOS ARM64 的 libaviflph.dylib)"

fi


###############################################################################
# WASM
###############################################################################

WASM_OUT="$DIST/wasm"

rm -rf "$WASM_OUT"
mkdir -p "$WASM_OUT"


echo
echo "==> wasm (js/wasm)"


GOOS=js \
GOARCH=wasm \
CGO_ENABLED=0 \
    go build \
        -trimpath \
        -ldflags "$LDFLAGS" \
        -o "$WASM_OUT/aviflph.wasm" \
        ./cmd/lpwasm


###############################################################################
# wasm_exec.js
###############################################################################

WASM_EXEC="$(go env GOROOT)/lib/wasm/wasm_exec.js"


if [ -f "$WASM_EXEC" ]; then

    cp "$WASM_EXEC" "$WASM_OUT/wasm_exec.js"

else

    echo "    (提示: 未找到 wasm_exec.js)"

fi


###############################################################################
# 完成
###############################################################################

echo
echo "========================================"
echo "完成。产物："
echo "========================================"


du -sh "$DIST"/*/ 2>/dev/null |
    sort -h ||
    true


echo
echo "文件列表："


find "$DIST" \
    -maxdepth 2 \
    -type f \
    -printf "  %p\n" 2>/dev/null |
    sort


echo
echo "========================================"
