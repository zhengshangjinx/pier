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
	"github.com/zhengshangjinx/pier/internal/sysopen"
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
}

func newApp(flagPath string) *app {
	a := &app{panel: panel.New()}
	a.mgr = a.panel.Manager()

	path, src, err := resolveConfig(flagPath)
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
		{"pierDeleteService", a.deleteService},
		{"pierClearHealth", a.clearHealth},
		{"pierRenameService", a.renameService},
		{"pierDuplicateService", a.duplicateService},
		// 拖动排序：传的是「这一页现在的顺序」
		{"pierMoveServices", a.moveServices},
		{"pierMoveGroups", a.moveGroups},
		// 让系统弹框选目录，以及一次列一批候选端口
		{"pierPickDirectory", a.pickDirectory},
		{"pierPortCandidates", a.portCandidates},
		// 分组的新增 / 重命名 / 删除
		{"pierCreateGroup", a.createGroup},
		{"pierRenameGroup", a.renameGroup},
		{"pierDeleteGroup", a.deleteGroup},
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
	s := config.Settings{Theme: "system"}
	if p, err := config.SettingsPath(); err == nil {
		if loaded, err := config.LoadSettings(p); err == nil {
			s = loaded
		}
	}
	raw, _ := json.Marshal(s)
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

// saveSettings 保存主题。
//
// 走 UpdateSettings 而不是整份写回：settings.json 里还有「SDK 管理」那一摊
// （手动添加的 SDK、各语言的全局默认），换主题时整份覆盖会把它们抹掉——
// 而换主题是这里最常见的动作。
func (a *app) saveSettings(theme string) string {
	p, err := config.SettingsPath()
	if err != nil {
		return errJSON(err.Error())
	}
	if err := config.UpdateSettings(p, func(s *config.Settings) { s.Theme = theme }); err != nil {
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
