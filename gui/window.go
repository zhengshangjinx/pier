package main

import (
	"fmt"
	"math"
	"os"
	"time"

	webview "github.com/webview/webview_go"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 本文件放窗口尺寸、外框记忆与颜色这几件「算出来」的事，与平台无关；
// 真正要碰系统的那几个动作（建菜单、垫拖拽条、读写外框、写剪贴板）各平台一份，
// 见 window_darwin.go / window_linux.go / window_windows.go。

// defaultWindowSize 算初始窗口尺寸。
//
// 不写死一个数：写死的尺寸在小屏上顶满、在大屏上又显得局促，两头都不协调。
// 按主屏可用区域取一个比例，四周都留出边来，再收在上下限之间——下限是原来那套
// 尺寸（再小就摆不下侧栏加列表了），上限是免得在超宽屏上开出一扇太长的窗。
// 屏幕比下限还小时以屏幕为准，宁可挤一点，也别开出一扇比屏幕还大的窗。
//
// 屏幕可用区域由各平台自己量（screenVisible）。macOS 上拿的是扣掉菜单栏与 Dock
// 之后的那块，量得到；Linux / Windows 上量不到（GTK 与 WebView2 都没把屏幕尺寸
// 交出来），这时退回上下限里的那套尺寸——中间那一档在任何屏幕上都不会太离谱。
func defaultWindowSize() (int, int) {
	sw, sh := screenVisible()
	if sw <= 0 || sh <= 0 {
		return 1180, 780
	}
	w := int(math.Round(math.Min(sw*0.72, 1600)))
	h := int(math.Round(math.Min(sh*0.9, 1000)))
	w = max(w, 1180)
	h = max(h, 780)
	return min(w, int(sw)), min(h, int(sh))
}

// windowFloor 是记下来的窗口尺寸的下限。
//
// 比 defaultWindowSize 的 1180×780 松得多，因为它管的不是同一件事：那个数说的是
// 「默认开多大」，而记下来的是用户自己拖出来的尺寸——他乐意拖成一条窄边的，
// 重开时不该被顶回去。这里只挡住小到拿不住的（也顺手挡住坏值）。
const (
	windowFloorW = 480
	windowFloorH = 360
)

// restoreWindowBox 算出这次要照搬的那块外框；没记过（或者记的不成形）时 ok 为假。
//
// 尺寸在这一处收干净：小于下限的抬起来；屏幕量得到时不超过屏幕——与
// defaultWindowSize 里那两条是同一套说法（写死的尺寸在小屏上顶满、大屏上空旷）。
// 位置不在这儿收，因为那要知道屏幕都在哪儿，而那是平台的事（见 placeWindow）。
//
// 尺寸是不是外框、为什么只在原生侧读写，见 config.WindowBox 的说明。
func restoreWindowBox(saved config.WindowBox) (config.WindowBox, bool) {
	if saved.W <= 0 || saved.H <= 0 {
		return config.WindowBox{}, false
	}
	w, h := max(saved.W, windowFloorW), max(saved.H, windowFloorH)
	if sw, sh := screenVisible(); sw > 0 && sh > 0 {
		w, h = min(w, int(sw)), min(h, int(sh))
	}
	return config.WindowBox{W: w, H: h, X: saved.X, Y: saved.Y}, true
}

// windowWatchEvery 是看一眼窗口外框的间隔。拖完窗口到它落盘最多差这么久，
// 下一次启动读到的就是这个值；再密一点没有意义，写文件是有代价的。
const windowWatchEvery = 1500 * time.Millisecond

// watchWindowFrame 盯着窗口外框，挪动或缩放之后把它记进偏好，供下次启动照搬回来。
//
// 为什么是轮询而不是「退出时记一次」：这个进程没有一个可靠的收尾时刻。macOS 上
// 退出走的是 [NSApp terminate:]，它内部直接 exit(0)，w.Run() 根本不返回，defer
// 也轮不上；关窗、被系统结束这些路径更不必说。轮询是唯一一处「不管怎么退都记上了」
// 的办法，代价只是每 1.5 秒读一次尺寸——只读，不写。
//
// start 是这次启动摆上去的那块（没记过时是全零）。连着两次读到同一块才落盘：
// 拖拽一次能拖好几秒，中间每一个位置都是一个新值，照单全收就是每 1.5 秒写一次
// settings.json，而其中没有一个是用户最终想要的那个。
//
// stop 关闭即退出协程（与 main 里那个更新协程同一套写法）。
func watchWindowFrame(w webview.WebView, start config.WindowBox, stop <-chan struct{}) {
	// 先读一次：读不出外框的平台（见 window_windows.go / window_linux.go）
	// 就到此为止，不必留一条每 1.5 秒空转一圈的协程。
	box, ok := windowFrame(w)
	if !ok {
		return
	}
	tick := time.NewTicker(windowWatchEvery)
	defer tick.Stop()

	last, pending := start, config.WindowBox{}
	complained := false
	for {
		if box != last {
			if box == pending {
				last = box
				// 记不上就说一次。反复说没有意义（这条路径每隔几秒跑一次，
				// 磁盘满了会刷满整个 stderr），但一声不吭更坏：窗口位置记不住
				// 是那种「看着像系统的问题」的毛病。
				if err := saveWindowBox(box); err != nil && !complained {
					complained = true
					fmt.Fprintf(os.Stderr, "记窗口外框失败：%v\n", err)
				}
			} else {
				pending = box
			}
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		if box, ok = windowFrame(w); !ok {
			return
		}
	}
}

// saveWindowBox 把外框写进偏好里的 window 那一格。
//
// 走 UpdateSettings 而不是整份写回：同一份文件上还有别的写者（界面上的几个开关、
// 更新里跳过的版本、SDK 管理那一摊），各写各的一项，谁都不该把别人那一项抹掉。
func saveWindowBox(box config.WindowBox) error {
	p, err := config.SettingsPath()
	if err != nil {
		return err
	}
	return config.UpdateSettings(p, func(s *config.Settings) { s.Window = box })
}

// parseHex 解析 #RRGGBB。界面传来的是当前生效主题的 token，格式不对说明两边约定变了，要报出来。
func parseHex(hex string) (r, g, b float64, err error) {
	var R, G, B uint8
	if len(hex) != 7 {
		return 0, 0, 0, fmt.Errorf("底色 %q 不是 #RRGGBB 形式", hex)
	}
	if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &R, &G, &B); err != nil {
		return 0, 0, 0, fmt.Errorf("底色 %q 不是 #RRGGBB 形式", hex)
	}
	return float64(R) / 255, float64(G) / 255, float64(B) / 255, nil
}
