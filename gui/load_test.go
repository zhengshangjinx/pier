package main

import "testing"

// webView2SetHtmlLimit 是 WebView2 的 NavigateToString 能收下的字节数，微软文档写明的。
// gui/load_windows.go 就是为它存在的。
const webView2SetHtmlLimit = 2 * 1024 * 1024

// TestPageExceedsWebView2SetHtmlLimit 钉住 load_windows.go 那条路的前提。
//
// SetHtml 在 Windows 上落到 WebView2 的 NavigateToString，超过这个上限的部分被直接丢掉，
// 而且不返回错误——表现是窗口打开一片白，不查文档根本看不出是页面太大。
// 所以 Windows 上要先把页面落成文件再导航过去。
//
// 这条测试失败不代表哪里坏了：它是在说界面已经瘦到 SetHtml 装得下。那时可以去 Windows 上
// 实测一次，如果 SetHtml 能正常显示，load_windows.go 里落盘那一层就可以去掉，
// 这条测试也一并删掉。
func TestPageExceedsWebView2SetHtmlLimit(t *testing.T) {
	n := len(buildHTML())
	if n <= webView2SetHtmlLimit {
		t.Errorf("界面现在 %d 字节，已经低于 WebView2 的 %d 上限；"+
			"在 Windows 上确认 SetHtml 能显示之后，可以去掉 gui/load_windows.go 的落盘那一层",
			n, webView2SetHtmlLimit)
	}
}
