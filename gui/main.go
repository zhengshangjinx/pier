// Pier 图形界面入口。
//
// 单独一个二进制、单独一个包：命令行 Pier 与它共用 internal/ 下的全部逻辑，
// 但两者的生命周期完全不同——命令行跑完即退，界面要长期驻留并持有窗口。
//
// 三个平台共用这一份：webview 库在 macOS 走 WKWebView、Linux 走 GTK WebKit、
// Windows 走 WebView2，窗口动作里真正有平台差异的那几件（菜单栏、拖拽条、
// 剪贴板、角标）各自落在 window_<平台>.go 里，这里只按同样的顺序调一遍。
package main

import (
	"flag"
	"fmt"
	"os"

	webview "github.com/webview/webview_go"
)

func main() {
	var cfgFlag string
	flag.StringVar(&cfgFlag, "config", "", "只读打开一份 YAML 清单，不给则用 Pier 自己的数据")
	flag.Parse()

	a := newApp(cfgFlag)

	// webview 必须在主线程创建并运行，所以主 goroutine 全交给它；
	// 业务逻辑都在绑定回调和后台 worker 里跑。
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Pier")
	// 尺寸先定、样式后铺：webview 的 SetSize 会把窗口掩码整个换掉，
	// 早铺好的全尺寸内容视图会被它抹掉（见 window_darwin.go 的 resize）。
	width, height := defaultWindowSize()
	resize(w, width, height, webview.HintNone)
	styleWindow(w.Window())
	// 先按亮色刷一遍，页面挂载后会按实际主题再刷：不先刷的话，暗色启动的那一瞬
	// 标题栏是系统默认的灰，和页面对不上。（只在 macOS 上有效果，另外两个平台的
	// 底色露不出来，见各自的 applyChrome。）
	applyChrome(w.Window(), 0xF3/255.0, 0xF4/255.0, 0xF6/255.0, false)
	// 绑定回调本身就跑在主线程上，这里仍然走 Dispatch：AppKit 只认主线程，
	// 不把这个前提押在 webview 库的实现细节上。
	// 不能等 Dispatch 执行完再返回——回调占着主线程，等它就是死锁。
	// 所以格式校验放在前面同步做，出错能如实报回界面。
	a.chrome = func(hex string, dark bool) error {
		r, g, b, err := parseHex(hex)
		if err != nil {
			return err
		}
		w.Dispatch(func() { applyChrome(w.Window(), r, g, b, dark) })
		return nil
	}
	a.badge = func(text string) error {
		w.Dispatch(func() { setBadge(text) })
		return nil
	}
	a.bounce = func() error {
		w.Dispatch(func() { bounceIcon() })
		return nil
	}

	// 绑定失败会让界面上的按钮静默失效——那是最难排查的一类故障，
	// 所以这里失败必须留下痕迹。绑定清单见 app.go 的 bindings()，
	// 它与 ui.html 的一致性由测试守着。
	bind := func(name string, fn any) {
		if err := w.Bind(name, fn); err != nil {
			fmt.Fprintf(os.Stderr, "绑定 %s 失败：%v\n", name, err)
		}
	}
	for _, b := range a.bindings() {
		bind(b.name, b.fn)
	}

	w.Init(settingsScript())
	w.SetHtml(buildHTML())

	// 启动信息写到 stderr：从访达双击启动时看不到任何终端输出，
	// 出问题时这行是唯一能确认「它到底加载了哪份清单」的线索。
	fmt.Fprintln(os.Stderr, a.startupInfo())

	w.Run()
}
