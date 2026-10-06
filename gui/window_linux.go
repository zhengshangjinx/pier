//go:build linux

package main

import (
	"os/exec"
	"strings"
	"unsafe"

	webview "github.com/webview/webview_go"

	"github.com/zhengshangjinx/pier/internal/config"
)

// Linux 这一侧的窗口层。macOS 那边（window_darwin.go）要装菜单栏、垫原生拖拽条、
// 把内容铺满标题栏，是因为 WKWebView 不给这些；GTK 的窗口由窗口管理器画边框和标题栏，
// 拖拽、最小化、关闭都是窗口管理器的事，这里不需要补什么，几个函数是空的。

// nativeWindowChrome 为假：GTK 的窗口由窗口管理器画标题栏，页面从客户区顶上开始，
// 不留那一条（见 app.css 的 .dc-native）。
const nativeWindowChrome = false

// fileManagerName 是界面上「在 ✕ 中显示」里的那个名字。各家桌面环境叫法不一，
// 用最常见的通称。
const fileManagerName = "文件管理器"

// styleWindow 在窗口创建后、页面加载前调用一次。
//
// Linux 上没有对应的动作：标题栏与拖拽由窗口管理器负责，GTK 也不像 AppKit 那样
// 需要先装一份菜单栏才有 ⌘C / ⌘V（剪贴板快捷键由 GTK 与输入法直接处理）。
func styleWindow(_ unsafe.Pointer) {}

// resize 定窗口尺寸。与 macOS 那份保持同一个签名：那边的 SetSize 之后要补一次
// 窗口样式，这边没有这一步，直接调即可。
func resize(w webview.WebView, width, height int, hint webview.Hint) {
	w.SetSize(width, height, hint)
}

// screenVisible 拿主屏可用区域。
//
// 拿不到：webview 在 Linux 上只暴露窗口本身，屏幕尺寸要去问 GTK 或 X11，
// 为这一个数把 cgo 伸进 GTK 不划算。defaultWindowSize 会在拿到 0 时用默认那档尺寸。
func screenVisible() (float64, float64) { return 0, 0 }

// windowFrame 读窗口外框。Linux 这一份不做，只读不写，所以 watchWindowFrame
// 在这里会当场收工（见 gui/window.go）。两条理由，第二条是这一平台独有的：
//
// 一是判不了「上次那块屏还在不在」——多屏改过排列之后，记下来的坐标就是一块
// 够不着的地方，而这一层量不到屏幕（见上面 screenVisible），没法在照搬之前拦一道；
// 只记尺寸则成了「尺寸记着、位置每次回到正中」，一半对一半错。
//
// 二是 Wayland 下窗口位置根本不归客户端管：合成的窗口管理器会忽略移动请求
// （xdg-shell 里压根没有这一条），摆过去也不生效。真要在 X11 下做，还得先分清
// 跑在哪一套里——为一件只有一半会话能用的事写两套判断，不如等屏幕尺寸这件事
// 在这一层立起来之后一起做。
func windowFrame(_ webview.WebView) (config.WindowBox, bool) { return config.WindowBox{}, false }

// placeWindow 把窗口摆回上次的地方。同 windowFrame，这一层不做。
func placeWindow(_ unsafe.Pointer, _ config.WindowBox) {}

// applyChrome 刷窗口底色。GTK 的窗口底色由主题决定，网页自己铺满整块内容区，
// 露不出底色，这里不需要跟着页面主题走。
func applyChrome(_ unsafe.Pointer, r, g, b float64, dark bool) {}

// setBadge 在任务栏图标上挂角标。Linux 上没有统一的做法（GNOME 与 KDE 各一套，
// 且都要走 D-Bus 的专有接口），导航空着——「有几个服务要关注」在窗口标题里也读得到。
func setBadge(text string) {}

// bounceIcon 让任务栏图标跳一下。同上，没有统一做法，不做。
func bounceIcon() {}

// copyText 把一段文字放进系统剪贴板。
//
// 依次试 Wayland 与 X11 的剪贴板工具：GTK 的剪贴板要走 cgo 调 GTK，为一个复制
// 把界面绑死在 GTK 的头文件上不划算，而这几个小工具在各自的会话里几乎总是装着的。
// 都没有时什么也不做——界面上「已复制」的提示会照常弹出来，这是这一处的代价。
func copyText(text string) {
	for _, c := range []struct {
		bin  string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	} {
		bin, err := exec.LookPath(c.bin)
		if err != nil {
			continue
		}
		cmd := exec.Command(bin, c.args...)
		cmd.Stdin = strings.NewReader(text)
		if cmd.Run() == nil {
			return
		}
	}
}
