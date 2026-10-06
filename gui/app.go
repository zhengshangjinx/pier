// Package main 实现 Pier 的图形界面。
//
// 界面层只做三件事：把面板内核的值序列化成 JSON、把窗口动作接上、把系统集成
// （访达、默认浏览器）接上。真正的编排在 internal/panel，它不依赖任何界面技术，
// 因此同一份内核既能喂这个 webview 界面，也能喂将来的原生界面。
//
// 这一层刻意保持「薄」：任何出现在这里的判断，都意味着另一份界面要重新实现一遍，
// 而两份实现迟早会分叉。
package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/panel"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/sysopen"
	"github.com/zhengshangjinx/pier/internal/update"
)

// app 是界面层的宿主状态。除了窗口句柄，它只剩一个面板内核和它的编辑入口。
type app struct {
	panel *panel.Panel
	// mgr 是清单编辑业务层，和内核共用同一个实例——两边各拿一个的话，
	// 编辑保存后触发的那次重载就只会刷新其中一个，界面显示的和实际能起的两份清单会对不上。
	mgr *manage.Manager
	// chrome 把窗口底色（透明标题栏露出来的那一条）刷成页面底色，由 main 注入。
	chrome func(hex string, dark bool) error
	// badge 在 Dock 图标上挂角标（空串清掉），bounce 让图标跳一下。也由 main 注入：
	// 两者都要碰 AppKit，只能在主线程上做。界面侧不直接调这两个，而是走下面
	// 那两个方法，理由与 chrome 同。
	badge  func(text string) error
	bounce func() error
	// guiClaim 是「有界面在跑」那把锁，由 main 在启动时领下，握到进程退出。
	// 它是命令行那边判断「现在能不能替换文件」的唯一依据；第二个开着界面的
	// 实例领不到它，于是那个实例没有「重启并安装」可用（见 internal/update 的 lock.go）。
	guiClaim *proc.Claim
	// up 是更新的那一整套状态：查到哪一版、下到哪儿了、上次换文件成没成。
	// 界面侧只做往返，判断全在 internal/update 里。
	up *update.Session
	// quit 关掉这扇窗口，由 main 注入（和 chrome / badge 一样要碰窗口）。
	// 只有「重启并安装」走到最后会用它：绑定先返回「更新已安排」，页面把这句话
	// 显示出来之后再关。
	quit func() error
	// notify 把一条通知交给系统（各平台一份，见 notify_<平台>.go）。
	//
	// 做成字段是为了能换掉：这一条真跑起来会弹出一个横幅，而用例要验的是
	// 「开关关掉之后还发不发」——那是判断，不是平台动作本身。
	// 只在 newApp 里写一次，之后只读（调用它的那条协程读的也是这个值）。
	notify func(title, body string) error
}

func newApp(flagPath string) *app {
	a := &app{
		panel:  panel.New(),
		up:     update.NewSession(update.New(update.Options{})),
		notify: sendNotify,
	}
	// 服务出事时叫一声（见 panel.say）。命令行与 pier api 挂的是同一个内核，
	// 但它们没有地方弹通知，也不该弹——只有界面这一份装上。
	a.panel.SetUserNotify(a.notifyService)
	a.mgr = a.panel.Manager()

	path, src, err := config.Resolve(flagPath)
	if err != nil {
		a.panel.SetLoadError(err.Error())
		return a
	}
	a.panel.SetSource(src)
	if err := a.panel.Load(path); err != nil {
		a.panel.SetLoadError(err.Error())
	}
	return a
}

// notifyService 弹一条系统通知，偏好里关掉时什么都不做。
//
// 开关每次现读一遍 settings.json：偏好是另一个进程外的文件，缓存一份就得再想
// 「什么时候该失效」，而这件事一天也发生不了几次，读一次文件是最省心的答案。
//
// **必须立刻返回**：调用它的是巡检那条协程（每三秒跑一遍），而弹一条通知要
// 起一个进程（osascript / powershell），慢起来是几百毫秒——压在这儿就等于
// 让整个巡检跟着一起等。所以真正发的那一下另起一条协程，失败了也不说：
// 用户关掉通知权限、机器上没装 notify-send，都不是 Pier 坏了。
func (a *app) notifyService(title, body string) {
	if !config.DefaultSettings().Notify {
		return
	}
	fn := a.notify
	if fn == nil {
		return
	}
	go func() {
		_ = fn(title, body)
	}()
}

// binding 是一个暴露给界面的后端入口。
type binding struct {
	name string
	fn   any
}

// bindings 列出界面能调用的全部入口。
//
// 集中成一份列表而不是散在 main 里逐个 bind，是为了能被测试拿来和 ui.html 比对：
// 两边名字对不上时，界面上的对应按钮会静默失效——不报错、不提示，只是点了没反应，
// 这是最难排查的一类故障。
//
// 名字统一带 Pier 前缀：webview 把绑定挂在 window 的扁平命名空间下，
// 不加前缀的话 "stop" 会覆盖 DOM 自带的 window.stop。
func (a *app) bindings() []binding {
	return []binding{
		{"pierState", a.state},
		{"pierStart", a.start},
		{"pierStop", a.stop},
		{"pierRestart", a.restart},
		// 换一个端口起：清单里那个被别的东西占着时，绕开它起这一次
		{"pierStartOnPort", a.startOnPort},
		{"pierStartAll", a.startAll},
		{"pierStopAll", a.stopAll},
		{"pierLogs", a.logs},
		// 日志的占用与清理（「设置 · 日志」页）
		{"pierLogUsage", a.logUsage},
		{"pierPruneLogs", a.pruneLogs},
		{"pierClearLogs", a.clearLogs},
		{"pierPrune", a.prune},
		{"pierReveal", a.reveal},
		{"pierRevealLog", a.revealLog},
		{"pierRevealLogs", a.revealLogs},
		{"pierRevealConfig", a.revealConfig},
		{"pierOpenHealth", a.openHealth},
		// 应用管理与端口占用
		{"pierPortOwner", a.portOwner},
		{"pierKillPortOwner", a.killPortOwner},
		{"pierInspectDir", a.inspectDir},
		{"pierSaveService", a.saveService},
		// 扫一个目录，认出里面的项目，勾选后一次加进来
		{"pierScanDir", a.scanDir},
		{"pierAddScanned", a.addScanned},
		{"pierDeleteService", a.deleteService},
		{"pierClearHealth", a.clearHealth},
		{"pierDuplicateService", a.duplicateService},
		// 拖动排序：传的是「这一页现在的顺序」
		{"pierMoveServices", a.moveServices},
		{"pierMoveGroups", a.moveGroups},
		// 让系统弹框选目录，以及一次列一批候选端口
		{"pierPickDirectory", a.pickDirectory},
		{"pierPortCandidates", a.portCandidates},
		// 扫一遍本机在听的端口，把它们收进清单
		{"pierPortScan", a.portScan},
		{"pierAdoptPort", a.adoptPort},
		// 分组的新增 / 重命名 / 删除
		{"pierCreateGroup", a.createGroup},
		{"pierRenameGroup", a.renameGroup},
		{"pierDeleteGroup", a.deleteGroup},
		// 清单顶层那组共享给所有服务的变量（「偏好设置 · 数据」里编辑）
		{"pierSaveSharedEnv", a.saveSharedEnv},
		// 分享与迁移：把清单变成一段能带走的文字，或者换一份清单来用
		{"pierServiceYAML", a.serviceYAML},
		{"pierConfigYAML", a.configYAML},
		{"pierExportConfig", a.exportConfig},
		{"pierOpenConfig", a.openConfig},
		{"pierUseLocalConfig", a.useLocalConfig},
		// 窗口外观与界面偏好
		{"pierSetChrome", a.setChrome},
		{"pierCopyText", a.copyText},
		// 服务出事时叫一声：Dock 角标 + 图标弹跳（见批次五的「通知」）
		{"pierBadge", a.setBadge},
		{"pierBounce", a.bounceIcon},
		{"pierSaveSettings", a.saveSettings},
		// SDK 管理，以及表单里「将使用 X，依据 Y」的那次预演
		{"pierSDKList", a.sdkList},
		{"pierSDKAdd", a.sdkAdd},
		{"pierSDKRemove", a.sdkRemove},
		{"pierSDKDefault", a.sdkDefault},
		{"pierSDKRescan", a.sdkRescan},
		{"pierToolchain", a.toolchain},
		// 更新（偏好设置 · 通用）：查、下、取消、换，以及跳过某一版
		{"pierUpdateStatus", a.updateStatus},
		{"pierUpdateCheck", a.updateCheck},
		{"pierUpdateDownload", a.updateDownload},
		{"pierUpdateCancel", a.updateCancel},
		{"pierUpdateApply", a.updateApply},
		{"pierUpdateSkip", a.updateSkip},
		{"pierUpdateNotes", a.updateNotes},
		{"pierUpdateClearResult", a.updateClearResult},
		// 关窗口。只有「重启并安装」用得上：它要等页面把那句话显示出来再关。
		{"pierQuit", a.quitApp},
	}
}

// startupInfo 汇总一行启动信息。从访达双击启动时看不到终端输出，
// 这行是排查「它加载了哪份清单、有没有加载成功」的唯一线索。
func (a *app) startupInfo() string {
	cfg := a.panel.Config()
	path, src, loadErr := a.panel.ConfigPath(), a.panel.Source(), a.panel.ConfigErr()

	if cfg == nil {
		switch {
		case loadErr != "":
			return "pier-gui 启动：配置加载失败 —— " + loadErr
		case path != "":
			return "pier-gui 启动：配置 " + path + " 无法使用"
		default:
			return "pier-gui 启动：没有可用的数据文件"
		}
	}
	return fmt.Sprintf("pier-gui 启动：配置=%s（%s）服务=%d 个", path, src, len(cfg.Services))
}

// ── 绑定给界面的入口（一律收发 JSON 字符串）────────────────────────────────
//
// JSON 是这一层的对外合同：webview 的绑定只能收发字符串。内核返回的是 Go 值，
// 到这里才被序列化，所以换成原生界面时这段封装可以整段丢掉，内核一行不用改。

type msgOut struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg"`
}

func okJSON(msg string) string  { return marshal(msgOut{OK: true, Msg: msg}) }
func errJSON(msg string) string { return marshal(msgOut{OK: false, Msg: msg}) }

func marshal(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return `{"ok":false,"msg":"内部错误：结果无法序列化"}`
	}
	return string(raw)
}

// wrap 把内核的 (消息, 错误) 变成界面约定的 JSON。
func wrap(msg string, err error) string {
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) state() string { return marshal(a.panel.State()) }

func (a *app) start(name string) string   { return wrap(a.panel.Start(name)) }
func (a *app) stop(name string) string    { return wrap(a.panel.Stop(name)) }
func (a *app) restart(name string) string { return wrap(a.panel.Restart(name)) }

// startOnPort 换一个端口启动一个服务，port 传 0 表示自己挑一个空闲的。
//
// 为「清单里那个端口被别的东西占着」而设：换的只影响这一次运行，不写回清单。
// 挑中的端口由后端写进运行记录，界面上那一列读的是 runPort，不是清单里那个。
func (a *app) startOnPort(name string, port int) string {
	return wrap(a.panel.StartOnPort(name, port))
}

func (a *app) startAll() string { return wrap(a.panel.StartAll()) }
func (a *app) stopAll() string  { return wrap(a.panel.StopAll()) }

func (a *app) prune() string { return wrap(a.panel.Prune()) }

// logs 的 date 为空表示「它此刻在写的那一份」，since 是界面手上已有的字节数
// （0 表示要整段）。
func (a *app) logs(name, date string, since int64) string {
	out, err := a.panel.Logs(name, date, since)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// logUsage 是「设置 · 日志」页的那份占用清单。
func (a *app) logUsage() string {
	out, err := a.panel.LogUsage()
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// pruneLogs / clearLogs 的 name 为空表示全部服务。
// 两者的区别只在看不看天数：prune 清超期的，clear 清干净。
func (a *app) pruneLogs(name string) string {
	msg, err := a.panel.PruneLogs(name)
	return wrap(msg, err)
}

func (a *app) clearLogs(name string) string {
	msg, err := a.panel.ClearLogs(name)
	return wrap(msg, err)
}

// clearHealth 去掉一个服务的健康检查地址，之后只看进程是否存活。
func (a *app) clearHealth(name string) string {
	msg, err := a.mgr.ClearHealth(name)
	return wrap(msg, err)
}

// ── 交给系统处理的几个动作 ───────────────────────────────────────────────
//
// 只接受服务名，路径一律由清单推导。界面传什么都无法让这里去 open 一个任意路径。

// open 交给系统默认程序处理。失败只回报，不重试。
// 具体怎么调系统命令在 internal/sysopen，与内核侧共用一份。
func open(target string, reveal bool) string {
	if err := sysopen.Run(target, reveal); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

func (a *app) reveal(name string) string {
	dir, err := a.panel.ServiceDir(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return open(dir, false)
}

func (a *app) revealLog(name string) string {
	path, err := a.panel.ServiceLogPath(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return open(path, true)
}

// revealLogs 打开日志的根目录（logs/ 下面一个服务一个子目录、按天分文件）。
// 与「⋯」里那个「打开数据目录」不是一回事：那个开的是 ~/.pier，
// 这一份只开日志那一层，是「日志」设置页上的入口。
func (a *app) revealLogs() string {
	cfg := a.panel.Config()
	if cfg == nil {
		return errJSON("尚未加载服务清单")
	}
	return open(cfg.LogDir(), false)
}

// revealConfig 在访达里打开数据目录（~/.pier）；命令行指定 YAML 时显示那份文件。
func (a *app) revealConfig() string {
	path := a.panel.ConfigPath()
	if path == "" {
		return errJSON("尚未加载服务清单")
	}
	if config.IsStorePath(path) {
		return open(filepath.Dir(path), false)
	}
	return open(path, true)
}

// settingsScript 生成一段在页面脚本之前执行的 JS，把已保存的偏好放到 window 上。
// 走注入而不是让页面加载后再来取：主题要在第一帧就定下来，晚一拍就是先闪一下亮色再变暗。
func settingsScript() string {
	// 默认值只在 config.defaultSettings 那一处写：这里再摆一份字面量的话，
	// 新加的开关（比如 updateCheck）第一次渲染就会是关的，而设置页上看着像
	// 用户自己关过——两处默认值迟早只剩一处是对的。
	raw, _ := json.Marshal(config.DefaultSettings())
	return "window.__PIER_SETTINGS__ = " + string(raw) + ";" + nativeScript()
}

// nativeScript 把「这次是在什么样的一扇窗口里跑」告诉页面。
//
// 两件事都只有宿主知道：一是页面要不要给原生标题栏让出顶上那一条
// （macOS 的窗口是铺满的，顶上 28px 是拖拽条，见 window_darwin.go），
// 二是文件管理器在这个系统上叫什么——「在访达中显示」到 Windows 上得是「资源管理器」。
func nativeScript() string {
	name, _ := json.Marshal(fileManagerName)
	return "window.__PIER_NATIVE__ = " + boolText(nativeWindowChrome) + ";" +
		"window.__PIER_FILEMGR__ = " + string(name) + ";"
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// settingsPatch 是界面能改的那几项偏好。
//
// 用指针是为了分清「没送这一项」与「送了一个零值」：主题传空串是错的，
// 而「关掉自动检查」送的正是 false，两者不能都当成没给。
type settingsPatch struct {
	Theme       *string `json:"theme"`
	UpdateCheck *bool   `json:"updateCheck"`
	Notify      *bool   `json:"notify"`
}

// saveSettings 保存界面偏好，接一个 JSON 对象（{"theme":"dark"} / {"updateCheck":false}）。
//
// 接对象而不是固定的位置参数：再加设置项时不必再加绑定，也不必每加一项就把
// 所有调用点改一遍。只改认得的键，其余键（界面比后端新时可能出现）忽略。
//
// 走 UpdateSettings 而不是整份写回：settings.json 里还有「SDK 管理」那一摊
// （手动添加的 SDK、各语言的全局默认），改主题时整份覆盖会把它们抹掉——
// 而改主题是这里最常见的动作。
func (a *app) saveSettings(patch string) string {
	var in settingsPatch
	if err := json.Unmarshal([]byte(patch), &in); err != nil {
		return errJSON("偏好内容无法解析：" + err.Error())
	}
	p, err := config.SettingsPath()
	if err != nil {
		return errJSON(err.Error())
	}
	err = config.UpdateSettings(p, func(s *config.Settings) {
		if in.Theme != nil {
			s.Theme = *in.Theme
		}
		if in.UpdateCheck != nil {
			s.UpdateCheck = *in.UpdateCheck
		}
		if in.Notify != nil {
			s.Notify = *in.Notify
		}
	})
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// ── SDK 管理 ─────────────────────────────────────────────────────────────
//
// 这一层不做判断：扫描到哪些 SDK、能不能加、默认值合不合法，全在 internal 里定，
// 这里只把结果序列化出去。界面层多一个判断，另一份界面就要重新实现一遍。

func (a *app) sdkList() string {
	out, err := a.mgr.SDKList()
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

func (a *app) sdkAdd(kind, path string) string {
	out, err := a.mgr.SDKAdd(kind, path)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

func (a *app) sdkRemove(kind, path string) string {
	msg, err := a.mgr.SDKRemove(kind, path)
	return wrap(msg, err)
}

// sdkDefault 设某一类别的全局默认；path 为空表示改回自动选。
func (a *app) sdkDefault(kind, path string) string {
	msg, err := a.mgr.SDKDefault(kind, path)
	return wrap(msg, err)
}

func (a *app) sdkRescan() string { return okJSON(a.mgr.SDKRescan()) }

// toolchain 用表单上还没保存的内容预演一次工具链解析，供表单下方显示
// 「将使用 X，依据 Y」。入参与保存时同一份，界面不必另拼一个。
func (a *app) toolchain(in string) string {
	var v manage.ServiceIn
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		return errJSON("表单内容无法解析：" + err.Error())
	}
	out, err := a.mgr.Toolchain(v)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

func (a *app) openHealth(name string) string {
	url, err := a.panel.HealthURL(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return open(url, false)
}

// copyText 把一段文字放进系统剪贴板（日志正文、「复制成 YAML」都用它）。
//
// 走宿主而不是网页的剪贴板 API，理由见 window_darwin.go 里的 pierCopyText。
func (a *app) copyText(text string) string {
	copyText(text)
	return okJSON("")
}

// setBadge 让 Dock 图标上的角标显示当前有几个服务要关注，0 就清掉。
//
// 界面只在那一格真的变了的时候调（见 app.js 里 attention 那一段）：不是怕慢，
// 而是每次推一个同样的值，Dock 图标会跟着闪一下。
func (a *app) setBadge(text string) string {
	if a.badge == nil {
		return okJSON("")
	}
	if err := a.badge(text); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// bounceIcon 让 Dock 图标跳一下。只在「本来没事、现在有事」的那一刻调一次——
// 每轮刷新都跳的话，这个图标会变成一直在弹的东西，很快就被无视了。
func (a *app) bounceIcon() string {
	if a.bounce == nil {
		return okJSON("")
	}
	if err := a.bounce(); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// setChrome 让窗口底色跟着界面主题走。标题栏是透明的，它露出来的就是这块底色，
// 两边不一致时标题栏和界面之间就是一道断层。
func (a *app) setChrome(hex string, dark bool) string {
	if a.chrome == nil {
		return okJSON("")
	}
	if err := a.chrome(hex, dark); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// ── 更新 ─────────────────────────────────────────────────────────────────
//
// 这里同样不做判断：什么算有新版本、下哪一份、什么时候不能换，全在
// internal/update/session.go 里定。界面层多一句判断，另一份界面就要重新实现一遍。
//
// 查与下这两件事在那边是后台跑的，绑定只负责触发、立刻返回——绑定回调走的是
// 界面线程，在这儿等一条 30 秒的请求，整个窗口会跟着卡住。

func (a *app) updateStatus() string { return marshal(a.up.Status()) }

func (a *app) updateCheck() string {
	a.up.Check()
	return okJSON("")
}

func (a *app) updateDownload() string {
	a.up.Download()
	return okJSON("")
}

func (a *app) updateCancel() string {
	a.up.Cancel()
	return okJSON("")
}

func (a *app) updateSkip(ver string) string {
	if err := a.up.Skip(ver); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// updateClearResult 收掉上次替换留下的那条消息（用户点掉之后才调）。
//
// 失败原因唯一的去处就是那一格：助手跑在 Pier 已经退出的空档里，没有窗口能报错。
func (a *app) updateClearResult() string {
	if err := a.up.DismissResult(); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}

// updateNotes 打开这次更新那一版的发布页。
//
// 只接受「打开这次这一版」，不接受界面传一个地址进来：那等于把界面上的一个参数
// 变成「用系统默认程序打开任意网址」。
func (a *app) updateNotes() string { return open(a.up.NotesURL(), false) }

// updateApply 把换文件交给助手。成功之后由页面过一会儿调 pierQuit——
// 顺序不能反：先关窗口的话，用户看到的就是「点了一下，窗口没了」，
// 而这次的安排、日志写在哪儿，一句都没来得及说。
func (a *app) updateApply() string {
	logPath, err := a.up.Apply()
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON("更新已经安排好了，换文件的过程写在 " + logPath + " 里。")
}

// quitApp 关掉这扇窗口。
func (a *app) quitApp() string {
	if a.quit == nil {
		return okJSON("")
	}
	if err := a.quit(); err != nil {
		return errJSON(err.Error())
	}
	return okJSON("")
}
