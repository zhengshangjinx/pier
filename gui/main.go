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

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/update"
)

func main() {
	// 更新助手是同一份二进制、另一个动词：它要在 Pier 已经退出的空档里把文件换掉，
	// 所以必须赶在碰 webview 之前分出去——这时候起窗口，换完文件的那个目录里
	// 还开着一个用着旧代码的界面进程。
	if len(os.Args) > 1 && os.Args[1] == update.ApplyVerb {
		os.Exit(update.RunHelper(os.Args[2:]))
	}

	var cfgFlag string
	flag.StringVar(&cfgFlag, "config", "", "只读打开一份 YAML 清单，不给则用 Pier 自己的数据")
	flag.Parse()

	a := newApp(cfgFlag)

	// 领一下「有界面在跑」这把锁，握到进程退出（有意不放开）。命令行那边靠它
	// 判断此刻能不能替换文件；两个界面同时开着时，后开的那个领不到，它就没有
	// 「重启并安装」可用。领不到不是错误：界面本来就可以开两个。
	if claim, ok, err := update.TryHoldGUI(); err == nil && ok {
		a.guiClaim = claim
	}

	// webview 必须在主线程创建并运行，所以主 goroutine 全交给它；
	// 业务逻辑都在绑定回调和后台 worker 里跑。
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("Pier")
	// 尺寸先定、样式后铺：webview 的 SetSize 会把窗口掩码整个换掉，
	// 早铺好的全尺寸内容视图会被它抹掉（见 window_darwin.go 的 resize）。
	//
	// 上次关窗时的外框（见 gui/window.go）在这一段里接回来。尺寸得赶在 resize
	// 之前收好，位置却要等 styleWindow 铺完再摆——它里面有一次居中，摆早了会被
	// 那次居中顶掉。读不出外框的平台（Windows / Linux）这一块整个是空的：
	// haveBox 为假，宽度高度还是 defaultWindowSize 算出来的。
	width, height := defaultWindowSize()
	box, haveBox := restoreWindowBox(config.DefaultSettings().Window)
	if haveBox {
		width, height = box.W, box.H
	}
	resize(w, width, height, webview.HintNone)
	styleWindow(w.Window())
	if haveBox {
		placeWindow(w.Window(), box)
	}
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
	a.quit = func() error {
		w.Dispatch(func() { w.Terminate() })
		return nil
	}

	// 自动检查更新：等一会儿再查第一次（不跟启动抢网络与磁盘），之后按时
	// 到点再查。窗口一关就停——那个协程里的每一次调用都会先把状态写进
	// 会话、再由页面来取，窗口没了就没人取了，留着也没有意义。
	stopUpdate := make(chan struct{})
	defer close(stopUpdate)
	go a.up.AutoCheck(update.AutoFirst, update.AutoEvery, stopUpdate)

	// 窗口外框每 1.5 秒看一眼，挪过就记进偏好。同一个窗口只有一个写者，
	// 关窗即止（这一条的说明见 watchWindowFrame）。
	stopFrame := make(chan struct{})
	defer close(stopFrame)
	go watchWindowFrame(w, box, stopFrame)

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
	// 投递方式三平台不同：Windows 上 SetHtml 有 2MB 上限，装不下整个界面，
	// 改走落盘 + 导航。见 load_windows.go / load_other.go。
	loadUI(w, buildHTML())

	// 启动信息写到 stderr：从访达双击启动时看不到任何终端输出，
	// 出问题时这行是唯一能确认「它到底加载了哪份清单」的线索。
	fmt.Fprintln(os.Stderr, a.startupInfo())

	w.Run()
}
