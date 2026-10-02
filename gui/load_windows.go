//go:build windows

package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	webview "github.com/webview/webview_go"
	"github.com/zhengshangjinx/pier/internal/config"
)

// loadUI 把整份界面交给窗口。
//
// Windows 这一份不能走 SetHtml：它在 webview 库里落到 WebView2 的 NavigateToString，
// 而微软文档写明这个接口吃不下 2MB 以上的 HTML——超出的部分直接丢掉，也不返回错误。
// 整个界面拼出来约 2.2MB（antd 一个包就 1.8MB），正好越线，于是窗口开出来是一片白。
// 落成文件再导航过去就没有这条限制；WKWebView 的 loadHTMLString 与 WebKitGTK 的
// load_html 本来也没有，所以另外两个平台一直没事（见 load_other.go）。
//
// 换成 file:// 不影响界面与后端的通道：绑定注入的是 window.chrome.webview.postMessage，
// 它不挑来源——原先 SetHtml 走的是 about:blank，同样是不透明源。页面自己也不读写
// localStorage（主题由 settingsScript 在文档创建时注入），只有演示页会退回它，
// 而那几处都包着 try/catch。
func loadUI(w webview.WebView, html string) {
	path, err := writePage(html)
	if err != nil {
		// 落盘失败就只剩 SetHtml 这一条路：界面没超 2MB 时它仍然能显示，
		// 超了就只能空着。留一行痕迹，别让人对着白窗口猜。
		fmt.Fprintf(os.Stderr, "界面落盘失败（%v），改用 SetHtml\n", err)
		w.SetHtml(html)
		return
	}
	w.Navigate(pageURL(path))
}

// writePage 把整份界面写到数据目录下的 cache/ui/index.html，返回它的路径。
//
// 先写临时文件再改名：改名是原子的，读到一半的页面不会出现。两个 Pier 同时开着时
// 也安全——它们算出来的内容是同一份，谁先谁后都一样。
func writePage(html string) (string, error) {
	dirs, err := config.Dirs()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dirs.Cache, "ui")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "index.html.tmp")
	if err := os.WriteFile(tmp, []byte(html), 0o644); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "index.html")
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// pageURL 把本地路径转成 file:// 地址。
//
// 不能直接拼 "file://" + 路径：盘符、反斜杠、用户名里的空格与中文都要转义，
// 交给 net/url 去做，少一处疏漏就是又一次白屏。
func pageURL(path string) string {
	p := filepath.ToSlash(path)
	// 用户目录被域策略重定向到网络共享时，路径是 UNC（\\server\share\x），
	// 转出来是 //server/share/x——主机名要放进 URL 的 Host，不能当成路径的前导斜杠，
	// 否则拼出 file:////server/... 这种谁也不认的地址。
	if strings.HasPrefix(p, "//") {
		host, rest, _ := strings.Cut(p[2:], "/")
		return (&url.URL{Scheme: "file", Host: host, Path: "/" + rest}).String()
	}
	return (&url.URL{Scheme: "file", Path: "/" + p}).String()
}
