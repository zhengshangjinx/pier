//go:build windows

package main

import (
	"unsafe"

	webview "github.com/webview/webview_go"
)

// Windows 这一侧的窗口层。标题栏、拖拽、缩放都由系统画和管，
// 不需要 macOS 那边的一堆补救（见 window_darwin.go 的说明）。
// 剪贴板在 clipboard_windows.go。

// nativeWindowChrome 为假：标题栏由系统画，页面从客户区顶上开始，不留那一条。
const nativeWindowChrome = false

// fileManagerName 是界面上「在 ✕ 中显示」里的那个名字。
const fileManagerName = "资源管理器"

// styleWindow 在窗口创建后、页面加载前调用一次。Windows 上没有要补的东西。
func styleWindow(_ unsafe.Pointer) {}

// resize 定窗口尺寸。与 macOS 那份保持同一个签名：那边 SetSize 之后要补窗口样式，
// 这边没有这一步。
func resize(w webview.WebView, width, height int, hint webview.Hint) {
	w.SetSize(width, height, hint)
}

// screenVisible 拿主屏可用区域。
//
// 拿得到（GetSystemMetrics），但要扣掉任务栏、还要按每屏 DPI 换算，为一个初始尺寸
// 不值当——defaultWindowSize 拿到 0 时用的那档尺寸在任何屏幕上都不离谱。
func screenVisible() (float64, float64) { return 0, 0 }

// applyChrome 刷窗口底色。WebView2 把内容区铺满客户区，露不出窗口底色。
func applyChrome(_ unsafe.Pointer, r, g, b float64, dark bool) {}

// setBadge 在任务栏图标上挂角标。那要用 ITaskbarList3 的 SetOverlayIcon，
// 得先有一枚画好的图标，为一个数字去画图不值当；「有几个服务要关注」在标题里也读得到。
func setBadge(text string) {}

// bounceIcon 让任务栏图标闪一下（FlashWindowEx）。真要做需要窗口句柄与闪烁计数，
// 而「本来没事、现在有事」这一刻本来就还有角标与提示音，这里不做。
func bounceIcon() {}
