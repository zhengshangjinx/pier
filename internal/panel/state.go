package panel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/diag"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// 本文件定义面板对外的数据形状。字段顺序、JSON 键名都属于对外合同，
// 改动会波及界面与命令行两边的消费方，只能增字段，不能改已有字段的含义。

// UsageOut 是一组进程合计的资源占用。
//
// 这是给界面直接渲染的形状，数字都已换算好：CPU 是百分比（单核满载 100），
// 内存是字节。界面不做单位换算——换算规则写两遍，迟早两边不一致。
type UsageOut struct {
	CPU      float64 `json:"cpu"`
	MemBytes int64   `json:"memBytes"`
	Procs    int     `json:"procs"`
}

// runtimesOf 列出服务要用的 SDK。形状与「将使用 X，依据 Y」共用一份，见 proc.ToolInfo。
func runtimesOf(sup *proc.Supervisor, svc *config.Service) []proc.ToolInfo {
	tools, _, err := sup.Tools(svc)
	return proc.ToolInfos(tools, err)
}

// ServiceOut 是面板上一个服务的全部展示字段。
type ServiceOut struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Dir  string `json:"dir"`
	// Port 是清单里写的端口，RunPort 是这次运行实际用的那个。
	//
	// 两个都给，是因为界面要用它们做两件相反的事：编辑表单预填「清单里写的那个」
	// （预填成运行时的值，用户一保存就把换过的端口写进清单了，而那次换端口
	// 本来就只是这一次的事）；显示要的是运行时那个。
	Port int `json:"port"`
	// RunPort 是这次运行实际用的端口，没在跑或没配端口时为 0。
	RunPort int `json:"runPort"`
	// PortNote 解释 RunPort 为什么和 Port 对不上，两边一样时为空串。
	PortNote   string `json:"portNote"`
	Group      string `json:"group"`
	StatusKey  string `json:"statusKey"`
	StatusText string `json:"statusText"`
	PortText   string `json:"portText"`
	PID        int    `json:"pid"`
	Uptime     string `json:"uptime"`
	Health     string `json:"health"`
	Healthy    bool   `json:"healthy"`
	HasHealth  bool   `json:"hasHealth"`
	// ProbeExpired 表示探针等满窗口还没通过（服务本身在跑）。界面靠它把
	// 「还在等就绪」和「等了也没通」分开：前者不用说，后者要给一句说明，
	// 并把它算进「需要关注」——大半是健康地址填错了。
	ProbeExpired bool   `json:"probeExpired"`
	Note         string `json:"note"`
	Running      bool   `json:"running"`
	PortOpen     bool   `json:"portOpen"`
	Stale        bool   `json:"stale"`
	LogPath      string `json:"logPath"`
	// UserNote 是清单里手写的备注，原样展示，Pier 不解释它的内容。
	UserNote string `json:"userNote"`
	// Run/Build/Module/Script 原样回传，供编辑表单预填。
	// 不回传的话，编辑一个已有服务会把用户手写的命令悄悄清掉。
	Run    string `json:"run"`
	Build  string `json:"build"`
	Module string `json:"module"`
	Script string `json:"script"`
	// Env 与 Toolchain 是高级设置里的环境变量、服务上指定的 SDK（按类别，值是 SDK 路径）。
	// 数据里有什么界面上就能看到、能改，不再有「只能手改文件」的隐藏字段。
	Env       map[string]string `json:"env"`
	Toolchain map[string]string `json:"toolchain"`
	// Runtimes 是启动时实际会用的 SDK 与选择依据，和真正启动走的是同一份解析结果。
	Runtimes []proc.ToolInfo `json:"runtimes"`
	// DependsOn 是启动顺序上的前置服务名，Restart 是重启策略，原样回传供编辑表单预填。
	DependsOn []string `json:"dependsOn"`
	Restart   string   `json:"restart"`
	// Manual 表示它不参与全部启停。列表上据此挂一个标记：这一行是「全部启动」
	// 按下去也不会起来的那几个之一，不标出来就只能靠用户记得自己标过。
	Manual bool `json:"manual"`
	// Watch 是这个服务正在盯的那些模式，没配则为空。
	//
	// 回传的是**实际生效的**那一份（`watch: true` 已经按类型展开过），不是清单里
	// 写的那个 true：界面要说的是「它盯着什么」，而 true 到模式那一步的换算
	// 只有后端做得出来，界面自己再推一遍迟早会推出第二份规矩。
	Watch []string `json:"watch"`
	// WatchAuto 说明上面那张单子是按类型来的默认，不是清单里点名的。
	//
	// 编辑表单要靠它：读出来的是展开过的那一份，照着它填回去就等于把「按类型」
	// 换成了「就这几个」——用户只是改了下端口再保存，默认就悄悄冻在这一版了。
	WatchAuto bool `json:"watchAuto"`
	// RestartNote 说明这个服务最近被自动重启过几次，没发生过则为空。
	//
	// 有它才看得出「它自己崩过又起来了」：不然界面只显示一个正常的「运行中」，
	// 而日志里那几段崩溃的痕迹没有任何东西解释。
	RestartNote string `json:"restartNote"`
	// DepNote 说明这次启动没能等到哪个前置就绪（depends_on 里写了 :healthy 的那些）。
	//
	// 等不到照旧起（见 panel.waitDeps）：服务已经在跑了，卡着不动的代价比
	// 「起了并说清楚」大得多。但这句话必须留在这儿——不然用户拿到的是一个
	// 「运行中」的服务，而它连的是还没起来的数据库，症状会在别处冒出来。
	DepNote string `json:"depNote"`
	// Editable 为真表示这条定义在 Pier 自己的数据文件里，界面能改也能删；
	// 命令行指定 YAML 清单时为假，那份文件 Pier 不改写。
	Editable bool `json:"editable"`
	// Occupant 是占着该端口的进程。端口开着又不是 Pier 起的时，
	// 界面靠它直接说出「被谁占着」，而不必等用户点开详情。
	Occupant *proc.Listener `json:"occupant"`
	// Op 是正在进行的动作文案，空表示空闲；OpErr 是上次操作失败的原因。
	Op    string `json:"op"`
	OpErr string `json:"opErr"`
	// OpKind 是进行中动作的类别（start / stop / restart），界面据此决定主按钮：
	// 启动中给一个能点的「停止」，停止中才是转圈的「停止中」。
	OpKind string `json:"opKind"`
	// CPU/MemBytes/Procs 是这个服务整棵进程树的资源占用，未运行时都留在 0。
	//
	// 嵌成一层而不是摊平成三个 cpu/memBytes/procs：这三个数永远一起出现、
	// 一起变化，摊平后每个消费方都得自己记得「它们是一组」。
	Usage UsageOut `json:"usage"`
	// Diag 是从日志尾部读出来的「一句原因 + 一句下一步」，只在出事时有：
	// 启动失败（OpErr 非空）或进程不见了（Stale）。
	//
	// 日志里写着的是工具链自己的行话，而用户要的是该去做什么，中间那一步
	// 由它补上。认不出来时为 nil（见 internal/diag）：宁可不说，也不能猜。
	Diag *diag.Hit `json:"diag"`
}

// OpInfo 是一个服务身上正在进行的动作，由面板翻译成人能读的一句话。
//
// 命令行没有这一组：它自己就是一次前台动作，没有「排队 / 编译 / 等就绪」这些
// 中间阶段可言，结束了才返回。所以只有面板会填。
type OpInfo struct {
	Label string // 空表示空闲
	Kind  string // start / stop / restart，界面据此决定主按钮
	Err   string // 上次操作失败的原因
}

// GroupOut 是侧栏里的一栏分组。
type GroupOut struct {
	Name string `json:"name"`
	// Count 是这个分组下的服务数，界面据此决定它是不是空的。
	Count int `json:"count"`
	// Builtin 为真表示这是「未分组」这类内置分组，不能改名也不能删。
	Builtin bool `json:"builtin"`
	// Usage 是组内所有服务加起来的占用，空分组是全 0。
	Usage UsageOut `json:"usage"`
}

// StateOut 是面板的一次完整快照。
type StateOut struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	ConfigPath string `json:"configPath"`
	ConfigDir  string `json:"configDir"`
	ConfigSrc  string `json:"configSource"`
	// ReadOnly 表示这次用的是命令行指定的 YAML 清单，界面上的编辑入口要收起来。
	ReadOnly bool       `json:"readOnly"`
	Groups   []GroupOut `json:"groups"`
	// SharedEnv 是清单顶层那组共享给所有服务的变量。界面在「偏好设置 · 数据」
	// 里编辑它——它属于这份清单，不属于某一个服务，而那一页是界面上唯一
	// 能改清单本身的地方。
	SharedEnv map[string]string `json:"sharedEnv,omitempty"`
	BusyCount int               `json:"busyCount"`
	Services  []ServiceOut      `json:"services"`
	// UngroupedName 是内置的「未分组」名字。由后端给出而不是让界面写死，
	// 免得将来改了名字界面上还留着旧的。
	UngroupedName string `json:"ungroupedName"`
	// Usage 是全部服务加起来的占用，Self 是 Pier 面板自身的占用。
	//
	// 两个数摆在一起，回答的是「机器变卡了，是面板在吃资源，还是面板起的程序在吃」。
	// 不给整机合计：那是活动监视器的事，摆在这里只会被拿去和服务的数字比，
	// 而全机 RSS 相加把共享库重复计入了几百遍，口径根本对不上。
	//
	// Usage 由后端加好而不是让界面把各分组加一遍：界面上不做任何推算，
	// 同一个数只有一个出处。
	//
	// Self 只含 Pier 自己的进程树。macOS 上 WebView 的渲染与网络进程由 launchd
	// 托管（父进程是 1），按进程树归不到这里，界面上要如实说明。
	Usage UsageOut `json:"usage"`
	Self  UsageOut `json:"self"`
	// MetricsErr 非空表示这次采样没取到数（ps 不在等），此时上面那些用量字段
	// 都是 0。界面必须靠它把「读不到」和「什么都不占」分开——
	// 两者在界面上长得一模一样，而含义正好相反。
	MetricsErr string `json:"metricsError"`
	// History 是最近若干次采样，概览那两格靠它画曲线（见 history.go）。
	//
	// 用指针是为了让 `pier status --json` 的输出原样不变：那份快照由包级的
	// Snapshot 直接产出，没有面板攒下来的这一段，也就不该多出一个只有界面
	// 用得上的键。
	History *HistoryOut `json:"history,omitempty"`
}

// LogOut 是日志尾部内容的返回结构。
type LogOut struct {
	OK        bool   `json:"ok"`
	Path      string `json:"path"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	// Date 是这次真正读到的那一天（年-月-日），Dates 是这个服务有日志的那些天，
	// 从新到旧。抽屉里的日期选择读的就是它们——能选的一定是真有东西的那天。
	Date  string   `json:"date"`
	Dates []string `json:"dates"`
	// Offset 是已经读到的文件字节数，界面下次带着它来只要新增的那一段。
	Offset int64 `json:"offset"`
	// Reset 为真表示 Text 是整段内容，界面要整个替换掉手上的，而不是往后接。
	// 换了一天、文件被清过、同一天里又重启过一次，都会是整段。
	Reset bool `json:"reset"`
}

// State 汇总一次完整快照。清单没加载好时也返回一个正常结构，
// 只是 OK 为假、Error 写明原因——界面据此显示引导页而不是白屏。
func (p *Panel) State() StateOut {
	p.mu.Lock()
	cfg, sup, cfgPath, cfgSrc, cfgErr := p.cfg, p.sup, p.cfgPath, p.cfgSrc, p.cfgErr
	ops := make(map[string]OpInfo, len(p.ops))
	for k, v := range p.ops {
		if v != nil {
			ops[k] = v.opInfo()
		}
	}
	p.mu.Unlock()
	out := Snapshot(cfg, sup, cfgPath, cfgSrc, cfgErr, ops, Notes{
		Restart: p.restartNote,
		Deps:    p.depNote,
	})
	// 曲线顺手记在这一趟里：它要与上面那些数字出自同一次采样，而且只有真的
	// 有人在读状态时才需要新点——没有哪个后台协程专门为它去跑一遍 ps。
	p.hist.record(out, time.Now())
	out.History = p.hist.snapshot()
	return out
}

// Notes 是只有面板才知道的那几句按服务说的说明。命令行没有它们（传零值），
// 它自己就是一次前台动作，那些话在它的输出里各有各的位置。
//
// 收成一个结构而不是继续往 Snapshot 上挂参数：这两样是同一类东西
// （「这次运行发生了什么」），再添一样时不必再动一次签名，
// 也不会出现「两个 func 参数传反了」这种编译器拦不住的错。
type Notes struct {
	// Restart 说明这个服务最近自动重启过几次。
	Restart func(string) string
	// Deps 列出这次启动没等到的前置服务名，见 view.DepMissed。
	Deps func(string) []string
}

// Snapshot 汇总一次完整快照。面板与命令行（`pier status --json`）共用这一份。
//
// 输入是清单、supervisor 与清单的来源说明，另加只有面板才有的两样：正在进行的
// 动作（ops）、那几句按服务说的说明（notes）。命令行这两样都没有，传零值。
//
// 分头组装是不行的：StateOut 上每一个 json 键名都是对外承诺，各写一份的话，
// 加一个字段就会漏掉一边，而两边的消费方（界面与脚本）都会以为自己看到的是全部。
func Snapshot(cfg *config.Config, sup *proc.Supervisor, cfgPath, cfgSrc, cfgErr string,
	ops map[string]OpInfo, notes Notes) StateOut {
	if cfg == nil || sup == nil {
		return StateOut{OK: false, Error: cfgErr, ConfigPath: cfgPath, ConfigSrc: cfgSrc}
	}

	// Status() 会做健康探测（单个探针最多等 2 秒），因此绝不能在持锁时调用，
	// 否则排队中的 worker 会被状态刷新堵住。
	list, err := sup.Status()
	if err != nil {
		return StateOut{OK: false, Error: err.Error(), ConfigPath: cfgPath, ConfigSrc: cfgSrc}
	}

	// 资源采样。和 Status() 挤在同一次调用里，是为了让「谁在跑」和「谁占了多少」
	// 出自同一瞬间——分两次取，界面就可能出现「服务已经停了、CPU 数字还挂着」。
	//
	// 一次 ps 覆盖全部服务，而不是每个服务各跑一次：这条路径界面每 5 秒走一遍，
	// 逐服务采样等于把开销乘以服务数。（实测 ps -axo 约 9ms CPU，
	// 比这里本来就要跑的 lsof 还便宜一半。）
	//
	// exclude 传的是正在跑的服务的 PID：它们是 Pier 的子进程，不摘掉的话
	// 「面板自身」会把所有服务的开销算进来，而这一项存在的意义恰恰是反过来的。
	exclude := make(map[int]bool, len(list))
	for _, st := range list {
		if st.Running {
			exclude[st.PID] = true
		}
	}
	metrics, metricsErr := proc.SampleMetrics(os.Getpid(), exclude)
	usageOf := func(pgid int) UsageOut {
		if metrics == nil {
			return UsageOut{}
		}
		u := metrics.Groups[pgid]
		return UsageOut{CPU: u.CPU, MemBytes: u.MemBytes, Procs: u.Procs}
	}

	// ConfigDir 给界面「在访达中显示」用：数据文件就是它所在的数据目录（~/.pier），
	// YAML 清单是清单所在目录。
	cfgDir := cfg.Dir()
	if cfg.IsStore() {
		cfgDir = filepath.Dir(cfg.Path)
	}
	out := StateOut{OK: true, ConfigPath: cfgPath, ConfigDir: cfgDir, ConfigSrc: cfgSrc,
		ReadOnly: !cfg.IsStore(), SharedEnv: cfg.Env, UngroupedName: config.UngroupedName}
	if metricsErr != nil {
		out.MetricsErr = metricsErr.Error()
	} else {
		out.Self = UsageOut{CPU: metrics.Self.CPU, MemBytes: metrics.Self.MemBytes, Procs: metrics.Self.Procs}
	}
	for _, g := range cfg.AllGroups() {
		out.Groups = append(out.Groups, GroupOut{
			Name:    g,
			Count:   cfg.CountInGroup(g),
			Builtin: g == config.UngroupedName,
		})
	}
	out.Services = make([]ServiceOut, 0, len(list))
	for _, st := range list {
		svc := st.Service
		item := ServiceOut{
			Name:         svc.Name,
			Kind:         svc.Kind,
			Dir:          svc.AbsDir(),
			Port:         svc.Port,
			RunPort:      st.RunPort(),
			PortNote:     view.PortNote(st),
			Group:        svc.GroupName(),
			StatusKey:    view.StateKey(st),
			StatusText:   view.StatusText(st),
			PortText:     view.PortText(st),
			PID:          st.PID,
			Uptime:       view.UptimeText(st),
			Health:       svc.Health,
			Healthy:      st.Healthy,
			HasHealth:    st.HasHealth,
			ProbeExpired: st.ProbeExpired,
			Note:         view.NoteText(st),
			Running:      st.Running,
			PortOpen:     st.PortOpen,
			Stale:        st.Stale,
			LogPath:      proc.LogFile(cfg, svc.Name),
			UserNote:     svc.Note,
			Run:          svc.Run,
			Build:        svc.Build,
			Module:       svc.Module,
			Script:       svc.Script,
			Env:          svc.Env,
			Toolchain:    svc.Toolchain,
			Runtimes:     runtimesOf(sup, svc),
			DependsOn:    svc.DependsOn,
			Restart:      svc.Restart,
			Manual:       svc.Manual,
			Watch:        svc.WatchPatterns(),
			WatchAuto:    svc.Watch.On && len(svc.Watch.Include) == 0,
			RestartNote:  noteText(notes.Restart, svc.Name),
			DepNote:      view.DepMissed(depNames(notes.Deps, svc.Name)),
			Editable:     cfg.IsStore(),
			Occupant:     st.Occupant,
		}
		// 只有真在跑的才有用量可谈。没跑却去取，取到的是「这个进程组号现在
		// 归谁」——而进程组号是会被人复用的，那会把无关进程的开销记到它头上。
		if st.Running {
			item.Usage = usageOf(st.PGID)
		}
		if op, ok := ops[svc.Name]; ok {
			item.Op, item.OpErr = op.Label, op.Err
			if item.Op != "" {
				item.OpKind = op.Kind
				out.BusyCount++
			}
		}
		// 只给出事的那几个读日志：认一次要读 64 KB 再逐行比对，而这条路径
		// 每两秒走一遍，好好跑着的服务不该为此付费。
		if item.Stale || item.OpErr != "" {
			if h, ok := diag.FromLog(cfg, svc.Name); ok {
				item.Diag = &h
			}
		}
		out.Services = append(out.Services, item)
	}

	// 分组用量由组内服务相加得到。这一段必须排在服务循环之后：分组列表在前面
	// 就建好了，那会儿还没有任何服务数字可用。
	byGroup := make(map[string]*UsageOut, len(out.Groups))
	for i := range out.Groups {
		byGroup[out.Groups[i].Name] = &out.Groups[i].Usage
	}
	for _, s := range out.Services {
		out.Usage.add(s.Usage)
		if g, ok := byGroup[s.Group]; ok {
			g.add(s.Usage)
		}
	}
	return out
}

// noteText 问一句这个服务此刻该显示什么重启说明。那份账本只有面板有
// （巡检归它跑），命令行传进来的 note 是 nil。
func noteText(note func(string) string, name string) string {
	if note == nil {
		return ""
	}
	return note(name)
}

// depNames 问一句这个服务这次启动没等到哪些前置。与 noteText 分开是因为
// 它给的是一串名字，措辞在 view.DepMissed 里统一（命令行也要说同一句话）。
func depNames(note func(string) []string, name string) []string {
	if note == nil {
		return nil
	}
	return note(name)
}

// add 把另一份用量并进来。
func (u *UsageOut) add(o UsageOut) {
	u.CPU += o.CPU
	u.MemBytes += o.MemBytes
	u.Procs += o.Procs
}

// Logs 返回服务日志的末尾若干行。
//
// date 为空表示「它此刻在写的那一份」：跨了零点还在跑的服务，写的一直是
// 它启动那天的文件；给一个日期就精确读那一天（历史日志）。
//
// since 是界面手上已经有的字节数。> 0 且还是同一份文件、文件也没缩水时只回
// 新增的那一段，界面接着往后拼——抽屉开着的时候每几秒就要拉一次，
// 长日志整份重读重解析纯属白费，还会把用户往上翻的位置冲掉。
func (p *Panel) Logs(name, date string, since int64) (LogOut, error) {
	_, cfg, err := p.Lookup(name)
	if err != nil {
		return LogOut{}, err
	}
	out := LogOut{OK: true, Dates: proc.LogDates(cfg.LogDirFor(name))}

	path := logPathFor(cfg, name, date)
	out.Path, out.Date = path, logDateOf(path)

	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		// 还没启动过（或今天还没写过）是正常情况，不是错误。
		if err == nil || os.IsNotExist(err) {
			// Reset 要立起来：抽屉正开着的时候日志被清掉（设置页的「清空」、
			// 手工删文件）走的就是这一条，界面得把手上那段已经不存在的正文换掉。
			out.Reset = true
			return out, nil
		}
		return LogOut{}, err
	}
	size := fi.Size()

	// 增量：接着上次读完的地方往后读。
	if since > 0 && since <= size {
		chunk, err := readRange(path, since, size-since)
		if err != nil {
			return LogOut{}, err
		}
		// 这一小段里出现了启动标记，说明服务在同一天里又起过一次，
		// 界面手上那些文字整个作废（见 proc.TrimToLastRun）：改走整段重读。
		if !strings.Contains(chunk, proc.LogStartMarker(name)) {
			out.Text, out.Offset = chunk, size
			return out, nil
		}
	}

	// 整段读。Offset 用的是读之前量到的 size：读完之后又写进来的那些字节
	// 留给下一次增量，不会重也不会漏。
	text, truncated, err := tailFrom(path, size, LogLines, LogBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // 量完大小之后被清掉了，当成没有
		}
		return LogOut{}, err
	}
	// 同一天里重启过就再截一刀，只看最后这一次运行的输出（见 proc.TrimToLastRun）。
	// 截短之后「只显示了末尾」这句话不再成立，那个提示要跟着撤掉，
	// 否则界面会一边显示完整的一次运行、一边说内容被截断了。
	if trimmed := proc.TrimToLastRun(text, name); trimmed != text {
		text, truncated = trimmed, false
	}
	out.Text, out.Truncated, out.Offset, out.Reset = text, truncated, size, true
	return out, nil
}

// logPathFor 按「服务 + 哪一天」算出日志文件的路径。
//
// date 为空表示「它此刻在写的那一份」。日期只用来拼文件名，所以先确认它是
// 「年-月-日」那个形状：认不出的串一律当作没给，免得随便传个 ../.. 就读到别处的文件去。
//
// 读日志（Logs）与导出整份（LogPath）共用这一条：两处各写一遍的话，
// 防越界的那一句迟早只在其中一处。
func logPathFor(cfg *config.Config, name, date string) string {
	if date != "" {
		if _, err := time.Parse(config.LogDateLayout, date); err == nil {
			return cfg.LogPathDate(name, date)
		}
	}
	return proc.LogFile(cfg, name)
}

// logDateOf 从 <年-月-日>.log 这个路径里取出那一天；认不出就是空串
// （还没有任何日志时后端按「今天那份」给出路径，那个日期是有的）。
func logDateOf(path string) string {
	base, ok := strings.CutSuffix(filepath.Base(path), ".log")
	if !ok {
		return ""
	}
	if _, err := time.Parse(config.LogDateLayout, base); err != nil {
		return ""
	}
	return base
}

// readRange 读文件里 [start, start+n) 这一段。
func readRange(path string, start, n int64) (string, error) {
	if n <= 0 {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf[:read]), nil
}

// LogServiceOut 是一个服务的日志占用，供「设置 · 日志」页按服务列出。
//
// 比 proc.LogServiceOut 只多一件东西：换算好的文字（Size）。换算是显示的事，
// 放进 proc 会让那个包开始关心措辞；而放到界面上，同一个数就会有两种写法——
// 命令行 `pier logs --size` 与界面必须说同一个「1.2 GB」。view.Bytes 是唯一
// 那份换算，Bytes 原样留着供界面排序与判断「有没有」。
type LogServiceOut struct {
	Name string `json:"name"`
	// Bytes 与 Files 是这个服务所有日志文件加起来的。
	Bytes int64  `json:"bytes"`
	Size  string `json:"size"`
	Files int    `json:"files"`
	// Oldest / Newest 是它覆盖的日期区间（取自文件名），没有日志时为空。
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
	// Biggest / BiggestSize 是写得最多的那一天与它的大小。
	Biggest      string `json:"biggest"`
	BiggestBytes int64  `json:"biggestBytes"`
	BiggestSize  string `json:"biggestSize"`
	// BigNote 是「这一天写得太多了」的那句话，没超线时为空。
	//
	// 话在后端写、界面照说，和 Diag / RestartNote 同一个做法：同一件事在界面上
	// 与 `pier logs --size` 上必须一字不差，而两处各写一句迟早会走样。
	BigNote string `json:"bigNote"`
}

// LogUsageOut 是日志目录的整体占用。
type LogUsageOut struct {
	OK    bool   `json:"ok"`
	Dir   string `json:"dir"`
	Bytes int64  `json:"bytes"`
	Size  string `json:"size"`
	Files int    `json:"files"`
	// KeepDays 由后端给出而不是让界面写死：界面上那句「保留最近 N 天」
	// 必须和真正在清理时用的天数出自同一个数。DayWarn 同理（单日提醒线）。
	KeepDays int    `json:"keepDays"`
	DayWarn  string `json:"dayWarn"`
	// Services 按占用从大到小排（proc 那边排好的，这里不动顺序）。
	Services []LogServiceOut `json:"services"`
}

// LogUsage 统计日志目录的占用，供「设置 · 日志」页展示。
func (p *Panel) LogUsage() (LogUsageOut, error) {
	cfg := p.Config()
	if cfg == nil {
		return LogUsageOut{}, manage.ErrNoConfig
	}
	return LogUsageOf(cfg), nil
}

// LogUsageOf 是 LogUsage 的本体：`pier logs --size --json` 与界面读的是同一份。
// 命令行另算一份的话，同一个数字迟早有两种说法（而它正是「清哪几个服务」的依据）。
func LogUsageOf(cfg *config.Config) LogUsageOut {
	raw := proc.LogUsage(cfg.LogDir(), proc.LogKeepDays, time.Now())
	out := LogUsageOut{
		OK: raw.OK, Dir: raw.Dir, Bytes: raw.Bytes, Size: view.Bytes(raw.Bytes),
		Files: raw.Files, KeepDays: raw.KeepDays, DayWarn: view.Bytes(proc.LogDayWarnBytes),
		Services: make([]LogServiceOut, 0, len(raw.Services)),
	}
	for _, s := range raw.Services {
		out.Services = append(out.Services, LogServiceOut{
			Name: s.Name, Bytes: s.Bytes, Size: view.Bytes(s.Bytes), Files: s.Files,
			Oldest: s.Oldest, Newest: s.Newest,
			Biggest: s.Biggest, BiggestBytes: s.BiggestBytes, BiggestSize: view.Bytes(s.BiggestBytes),
			BigNote: logBigNote(s, raw.KeepDays),
		})
	}
	return out
}

// logBigNote 说清「哪一天写得太多、再这么写下去是多少」，没超线时返回空串。
//
// 后半句才是重点：单说「单日 300 MB」还有人觉得无所谓，乘上保留期才是它真正的代价。
// 乘出来的是「按这个量写满 N 天的合计」——服务今天已经写了这么多，这不是预测，
// 是它眼下的速度。
func logBigNote(s proc.LogServiceOut, keepDays int) string {
	if s.Biggest == "" || s.BiggestBytes < proc.LogDayWarnBytes {
		return ""
	}
	note := fmt.Sprintf("%s 单日写了 %s", s.Biggest, view.Bytes(s.BiggestBytes))
	if keepDays > 0 {
		note += fmt.Sprintf("，按这个量写满 %d 天就是 %s", keepDays, view.Bytes(s.BiggestBytes*int64(keepDays)))
	}
	return note
}

// busyNames 返回此刻不该动日志的服务：正在跑的，以及排队中 / 编译中 / 启动中的。
//
// 跑着的服务手里的日志 fd 从启动那一刻就打开了（见 proc.Supervisor.StartContext），
// 删掉文件只是 unlink——进程照写不误，报出来的「释放了 X」是笔假账，日志也再找不回来。
// 编译中的服务还没写进状态文件，但它已经在往那份日志里写「--- 编译」了，
// 所以排队与在途的那几个（p.ops）也要算上。
func (p *Panel) busyNames() (map[string]bool, error) {
	out := map[string]bool{}
	p.mu.Lock()
	for name := range p.ops {
		out[name] = true
	}
	cfg := p.cfg
	p.mu.Unlock()
	if cfg == nil {
		return out, nil
	}
	running, err := proc.RunningNames(cfg.StatePath())
	if err != nil {
		// 读不出来就不能假装没有人在跑：那正是「删了正在写的日志」的入口。
		return nil, fmt.Errorf("读不了进程状态（%v），这会儿分不清哪些服务在跑，先不动日志", err)
	}
	for name := range running {
		out[name] = true
	}
	return out, nil
}

// busyNote 说明这次清理跳过了谁。只在「全部服务」的口径下提一句：
// 单独清一个服务时上面已经明说过为什么没动，不必再说第二遍。
func busyNote(busy map[string]bool, cfg *config.Config) string {
	names := make([]string, 0, len(busy))
	// 按清单里的顺序列，map 的顺序每次都不同，同一件事两回的措辞不该变。
	for _, svc := range cfg.Services {
		if busy[svc.Name] {
			names = append(names, svc.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "；" + strings.Join(names, "、") + " 正在启动或运行，它们的日志没动"
}

// PruneLogs 清理超期日志。name 为空表示所有服务。
//
// 回执里的文件数与字节数都由 proc 那边真正数出来，不是删之前的估算——
// 「没有可清理的」和「释放了 0 B」都必须是实话。
func (p *Panel) PruneLogs(name string) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	busy, err := p.busyNames()
	if err != nil {
		return "", err
	}
	if name != "" && busy[name] {
		return name + " 正在启动或运行，它的日志还在写，先停下再清理", nil
	}
	out := proc.PruneLogs(cfg.LogDir(), name, busy, proc.LogKeepDays, time.Now())
	msg := fmt.Sprintf("没有超过 %d 天的日志，未删除任何文件", proc.LogKeepDays)
	if out.Files > 0 {
		msg = fmt.Sprintf("已删除 %d 个日志文件，释放 %s", out.Files, view.Bytes(out.Bytes))
	}
	if name == "" {
		msg += busyNote(busy, cfg)
	}
	return msg, nil
}

// ClearLogs 清空日志。name 为空表示清掉全部服务的日志。护栏同 PruneLogs。
func (p *Panel) ClearLogs(name string) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	busy, err := p.busyNames()
	if err != nil {
		return "", err
	}
	if name != "" && busy[name] {
		return name + " 正在启动或运行，它的日志还在写，先停下再清空", nil
	}
	out := proc.ClearLogs(cfg.LogDir(), name, busy)
	what := "全部服务的日志"
	if name != "" {
		what = name + " 的日志"
	}
	msg := what + "本来就是空的"
	if out.Files > 0 {
		msg = fmt.Sprintf("已清空%s，释放 %s", what, view.Bytes(out.Bytes))
	}
	if name == "" {
		msg += busyNote(busy, cfg)
	}
	return msg, nil
}

// TailFile 读取文件末尾若干行，返回内容与「是否被截断」。
//
// 只读末尾一段再切行，而不是整读：这是界面每秒都会调用的路径。
// 截断发生在字节层面时，第一行多半是半截，必须丢掉，否则会显示一行乱码。
func TailFile(path string, maxLines int, maxBytes int64) (string, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	return tailFrom(path, fi.Size(), maxLines, maxBytes)
}

// tailFrom 是 TailFile 的本体，大小由调用方量好传进来。
//
// 之所以要分开：增量跟随需要「我读到的字节数」和「文件的字节数」严格是同一个值，
// 各自量一次就会多读一段（写的那头在这中间又写了），下一次增量再读一遍，
// 同一行字就会在界面上出现两次。
func tailFrom(path string, size int64, maxLines int, maxBytes int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	start := int64(0)
	truncated := false
	if size > maxBytes {
		start, truncated = size-maxBytes, true
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}

	text := string(buf)
	if truncated && start > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	// 行尾的换行先去干净再数行数，否则最后那个空串会白占一个名额；
	// 但返回的正文要把「文件本来就以换行结尾」带回去（下面再加回来）。
	//
	// 这一条是增量跟随的前提：界面手上那段正文必须和文件严格对齐，
	// 下一段接上来才对得上。少了这个换行，第二轮读到的「第三行」会直接
	// 粘在「第二行」后面变成一行——而它看上去只是「日志少了个换行」。
	trail := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}
	out := strings.Join(lines, "\n")
	if trail && out != "" {
		out += "\n"
	}
	return out, truncated, nil
}
