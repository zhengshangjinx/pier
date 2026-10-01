// Package view 把 proc.Status 翻译成给人看的文案与状态键。
//
// 抽出来的理由：同一个服务状态现在有三个入口要展示——命令行 status、终端面板 ui、
// 以及图形界面。各写一套措辞的结果是同一种状态在不同入口说法不一样，
// 所以这里作为唯一事实源，各处只做排版。
package view

import (
	"fmt"
	"time"

	"github.com/zhengshangjinx/pier/internal/proc"
)

// 状态键。图形界面按它决定颜色和可用按钮，因此必须稳定，不随文案改动。
const (
	StateRunning  = "running"  // Pier 启动且已就绪（未配探针的视为就绪）
	StateStarting = "starting" // Pier 已启动，但健康探针还没通过
	StateExternal = "external" // 端口被 Pier 之外的进程占用（IDEA、别的终端）
	StateStale    = "stale"    // 状态文件里有记录，但进程已经不在了
	StateStopped  = "stopped"  // 未运行
)

var stateLabels = map[string]string{
	StateRunning:  "运行中",
	StateStarting: "启动中",
	StateExternal: "外部运行",
	StateStale:    "已退出",
	StateStopped:  "未启动",
}

// StateKey 归纳服务当前处于哪种状态。
//
// 判断顺序即优先级：「进程在跑但探针还没过」要盖过「进程在跑」，
// 否则编译型服务启动后漫长的初始化阶段会被误报成已经可用。
//
// 但等待是有期限的：等满一个窗口还没通过就不再算「启动中」，落回「运行中」，
// 由 NoteText 说明探针没过。没有这一条，一个健康地址填错的服务会永远显示成
// 「启动中」——它其实一直在正常服务，只是 Pier 探不进去，而「启动中」这个字
// 让人以为再等等就好，于是没人去查。见 proc.Status.ProbeExpired。
func StateKey(st proc.Status) string {
	switch {
	case st.Running && st.HasHealth && !st.Healthy && !st.ProbeExpired:
		return StateStarting
	case st.Running:
		return StateRunning
	case st.Stale:
		return StateStale
	case st.PortOpen:
		return StateExternal
	default:
		return StateStopped
	}
}

// StatusText 返回状态键对应的中文文案。
func StatusText(st proc.Status) string {
	return stateLabels[StateKey(st)]
}

// Dash 是「这一项没有」的占位符，端口、PID、运行时长在取不到值的时候都返回它。
//
// 单独起个名字是因为它同时也是**跨端的空值约定**：图形界面拿到的 uptime/pid
// 就是这几个字符串，而 "-" 在 JavaScript 里是真值，`if (s.uptime)` 判断不出来，
// 未启动的服务会渲染成「运行 -」。前端那边对应的是 app.js 的 hasVal()。
// 改这里的值必须同步改那边。
const Dash = "-"

// PortText 展示端口号，并附上「是否真的在监听」的提示。
func PortText(st proc.Status) string {
	if st.Service.Port <= 0 {
		return Dash
	}
	if st.Running && st.PortOpen {
		return fmt.Sprintf("%d ✓", st.Service.Port)
	}
	if st.PortOpen {
		return fmt.Sprintf("%d (被占)", st.Service.Port)
	}
	return fmt.Sprintf("%d", st.Service.Port)
}

// PIDText 展示进程号，未运行时为 Dash。
func PIDText(st proc.Status) string {
	if st.Running {
		return fmt.Sprintf("%d", st.PID)
	}
	return Dash
}

// UptimeText 展示运行时长，未运行时为 Dash。
func UptimeText(st proc.Status) string {
	if !st.Running {
		return Dash
	}
	return Duration(st.Uptime)
}

// NoteText 补充一句说明，解释「为什么是现在这个状态」。
func NoteText(st proc.Status) string {
	switch {
	case st.Stale:
		return "进程已不在，记录待清理"
	case st.Running && st.HasHealth && !st.Healthy && !st.ProbeExpired:
		return "尚未通过健康探针"
	// 服务在跑、探针却一直没过：说清楚是探针这一路没探通，不是服务没起来。
	// 要一路说到「怎么办」：大半是健康地址当初是猜的（见 config.DeriveFacts），
	// 或者这个服务压根没有健康接口——两种情况都不是等一等能好的，得去改配置。
	// 命令行那边的表格没有别的列能补这句话，所以这里必须自成一句。
	case st.Running && st.HasHealth && !st.Healthy:
		return "健康探针未通过：地址可能不对，或这个服务没有健康接口"
	case st.PortOpen && !st.Running:
		return "端口被 Pier 之外的进程占用"
	case st.Running:
		return st.Service.Health
	default:
		return ""
	}
}

// Bytes 把字节数压成「1.2 GB」这类紧凑形式，用于日志占用与清理回执。
//
// 服务的内存用量不走这里：那个数要和活动监视器里的读数对得上，
// 有它自己的一套（gui/app.js 的 fmtMem）。这里是给「磁盘上占了多少」用的，
// 只求一眼看出量级。
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	// 上了三位数就不留小数：「128 MB」比「128.4 MB」好读，而这时候小数也没意义。
	if v >= 100 || v == float64(int64(v)) {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// Duration 把时长压成「1h2m」「3m4s」「12s」这类紧凑形式。
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
