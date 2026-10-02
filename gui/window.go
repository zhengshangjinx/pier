package main

import (
	"fmt"
	"math"
)

// 本文件放窗口尺寸与颜色这两件「算出来」的事，与平台无关；
// 真正要碰系统的那几个动作（建菜单、垫拖拽条、写剪贴板）各平台一份，
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
