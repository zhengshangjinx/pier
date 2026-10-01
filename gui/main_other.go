//go:build !darwin

// 图形界面依赖 macOS 的 WKWebView，目前只支持 macOS。
// 命令行部分（Pier）是纯 Go 的，各平台都能编译运行。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Pier 图形界面目前只支持 macOS。命令行功能请使用 pier 命令。")
	os.Exit(1)
}
