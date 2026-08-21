//go:build js && wasm

// Package main 是 WebAssembly 的入口，实际逻辑在 aviflph/wasm 包。
package main

import _ "aviflph/wasm"

// main 保持运行：Go 的 wasm 运行时要求存在 main 函数，
// 实际的导出注册由 wasm 包的 init() 完成。
func main() {
	select {}
}
