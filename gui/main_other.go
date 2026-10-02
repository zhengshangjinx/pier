//go:build !darwin && !linux && !windows

// 图形界面依赖各平台的 WebView（macOS 的 WKWebView、Linux 的 GTK WebKit、
// Windows 的 WebView2），其它系统上没有可用的实现。
// 命令行部分（pier）是纯 Go 的，各平台都能编译运行。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "Pier 图形界面目前支持 macOS、Linux 与 Windows。命令行功能请使用 pier 命令。")
	os.Exit(1)
}
