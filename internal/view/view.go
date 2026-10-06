// Package view 把 proc.Status 翻译成给人看的文案与状态键。
//
// 抽出来的理由：同一个服务状态现在有三个入口要展示——命令行 status、终端面板 ui、
// 以及图形界面。各写一套措辞的结果是同一种状态在不同入口说法不一样，
// 所以这里作为唯一事实源，各处只做排版。
package view

import (
	"fmt"
	"os"
	"strings"
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
//
// 显示的是**这次运行实际用的**那个（st.RunPort()），不是清单里写的那个：
// 换过端口起的那一次，这两个值不一样，而用户要看的是服务此刻听在哪儿。
func PortText(st proc.Status) string {
	port := st.RunPort()
	if port <= 0 {
		return Dash
	}
	if st.Running && st.PortOpen {
		return fmt.Sprintf("%d ✓", port)
	}
	if st.PortOpen {
		return fmt.Sprintf("%d (被占)", port)
	}
	return fmt.Sprintf("%d", port)
}

// PortNote 说明这次运行的端口为什么和清单里写的不一样，两边一致时返回空串。
//
// 只写在这一个地方：命令行与界面都要说这句话，少说一次，用户看到的就是一个
// 与清单对不上的数字摆在界面上，没有任何解释。
func PortNote(st proc.Status) string {
	if st.Service == nil || st.Service.Port <= 0 {
		return ""
	}
	if run := st.RunPort(); run > 0 && run != st.Service.Port {
		return fmt.Sprintf("清单里写的是 %d，这次用的是 %d", st.Service.Port, run)
	}
	return ""
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
	// 换过端口这一条排在探针前面：端口那一列显示的就是换过之后的数字，和清单对不上，
	// 这句是唯一解释它的地方。探针那句排在后面是有代价的——这一行被占了，探针就没得说；
	// 但探针各有各的列写着「未通过」，而端口这一列只能靠这句话把自己说圆。
	case st.Running && PortNote(st) != "":
		return PortNote(st)
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
		// 用探针那一份的地址：换过端口的话，端口变了地址也跟着变了。
		return st.RunHealth()
	default:
		return ""
	}
}

// ShortPath 把路径开头的用户主目录缩成 ~。
//
// 端口那一屏里几乎每一行都是主目录底下的项目目录，原样写出来一列要占掉半屏，
// 有区分度的部分（~ 后面那几级）反倒被挤到看不见的地方。
//
// 分隔符两种都认：同一个 ~ 在两套系统上都得能用，而这件事上不需要知道自己在哪一边。
func ShortPath(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	// 缩的必须是整整一层目录：~/workspace 要缩，~/workspace2 不能缩成 ~2。
	if len(p) > len(home) && strings.HasPrefix(p, home) && (p[len(home)] == '/' || p[len(home)] == '\\') {
		return "~" + p[len(home):]
	}
	return p
}

// OriginText 把一次溯源的结果压成一句「谁把它拉起来的」，认不出来时为 Dash。
//
// 链上认出来的可能不止一个：编辑器里的集成终端再起一层 shell，最后才是服务。
// 从近到远连起来写，「当前位置 ← 再往外是谁」，读作「VS Code 里那个终端」。
// 只给最近的那一个是不够的——排查时用户记的是当时在哪个终端里敲的命令。
func OriginText(o *proc.Origin) string {
	if o == nil || !o.OK() || len(o.Chain) == 0 {
		return Dash
	}
	return strings.Join(o.Chain, " ← ")
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
