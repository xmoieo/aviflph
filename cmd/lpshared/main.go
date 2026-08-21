// Package main 是 C 动态库（c-shared）的构建入口。
// 实际的导出函数在 aviflph/capi 包（//export 注释），
// 构建 c-shared 时会一并导出：
//
//	go build -buildmode=c-shared -o libaviflph.so ./cmd/lpshared
//
// 或通过 scripts/build.sh 一键构建。
package main

import _ "aviflph/capi"

func main() {}
