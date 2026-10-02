//go:build !windows

package main

import webview "github.com/webview/webview_go"

// loadUI 把整份界面交给窗口。
//
// macOS 与 Linux 这条路一直是好的，也就一直保持这样：WKWebView 的 loadHTMLString
// 与 WebKitGTK 的 load_html 都是直接吃一整份字符串，没有 Windows 那边 2MB 的上限，
// 不必先落成文件再导航。分出来的原因见 load_windows.go。
func loadUI(w webview.WebView, html string) {
	w.SetHtml(html)
}
