package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/panel"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// Tools 是交给模型的那七个动作。
//
// 名字与参数照着「模型要做什么」取，不是照着接口那张表搬：接口上「启动一个」
// 与「启动一批」是两条路，这里合成一个带选择的动作；返回的也不是 JSON，
// 是几行它能直接读懂的字——它要的是「起来了没有、卡在哪儿」，不是字段。
//
// 全部走 *panel.Panel，与界面、命令行、接口是同一份实现：启停这件事里边
// 有排队、有前置、有换端口，另写一份迟早对不上。
func Tools(p *panel.Panel) []Tool {
	return []Tool{
		bind("list_services", "列出服务",
			"列出这份清单里的全部服务，一行一个：状态、端口、运行时长、健康探针、"+
				"目录。先看这一眼，再决定动哪一个。",
			listSchema,
			func(_ context.Context, _ listArgs) (string, error) {
				return listText(p)
			}),

		bind("service_status", "看一个服务的现状",
			"一个服务的全部现状：在不在跑、端口、进程号、跑到哪儿了、健康探针通没通、"+
				"日志在哪、依赖谁。起不来的时候这里还有一句诊断与下一步。",
			statusSchema,
			func(_ context.Context, a statusArgs) (string, error) {
				return statusText(p, a.Name)
			}),

		bind("start_service", "启动服务",
			"把服务排进启动队列。带 names 起点名的、带 group 起一组、都不带给就是全部服务。"+
				"返回时它们多半还没起来（要编译、要等端口）——要等真的就绪，接着用 wait_ready。",
			selSchema("启动"),
			func(ctx context.Context, a selArgs) (string, error) {
				sel, err := selection(a)
				if err != nil {
					return "", err
				}
				msg, err := p.StartSet(sel)
				if err != nil {
					return "", err
				}
				out := "已把" + describe(sel) + "排入队列。"
				if msg != "" {
					out += "\n" + msg
				}
				return out + "\n启动是排队的，它回来时服务多半还没起来；要等就绪用 wait_ready。", nil
			}),

		bind("stop_service", "停止服务",
			"停下来，没跑着的那些什么也不做。正在启动的会被当场打断——不必等它。"+
				"带 names 停点名的、带 group 停一组、都不带给就是全部服务。",
			selSchema("停止"),
			func(ctx context.Context, a selArgs) (string, error) {
				sel, err := selection(a)
				if err != nil {
					return "", err
				}
				msg, err := p.StopSet(sel)
				if err != nil {
					return "", err
				}
				if msg == "" {
					msg = "已对" + describe(sel) + "发出停止"
				}
				return msg, nil
			}),

		bind("restart_service", "重启服务",
			"改了代码要它生效时用这个：一个服务一次「先停后起」。带 names 重启点名的、"+
				"带 group 重启一组、都不带给就是全部服务。",
			selSchema("重启"),
			func(ctx context.Context, a selArgs) (string, error) {
				sel, err := selection(a)
				if err != nil {
					return "", err
				}
				msg, err := p.RestartSet(sel)
				if err != nil {
					return "", err
				}
				if msg == "" {
					msg = "已把" + describe(sel) + "排入队列，依次重启"
				}
				return msg + "\n要等它真的起来用 wait_ready。", nil
			}),

		bind("read_logs", "读日志",
			"一个服务日志的末尾。没写 date 就是它此刻正在写的那一份；"+
				"想知道有哪几天可读，看回执里写出来的那些。",
			logsSchema,
			func(_ context.Context, a logsArgs) (string, error) {
				return logsText(p, a)
			}),

		bind("wait_ready", "等服务就绪",
			"等到选中的服务通过健康探针为止，服务起完就该接着调它。"+
				"等不到不算这次调用出错——回执里会写清楚每一个卡在哪一步。",
			waitSchema,
			func(ctx context.Context, a waitArgs) (string, error) {
				return waitText(ctx, p, a)
			}),
	}
}

// ── 参数 ───────────────────────────────────────────────────────────────────

type listArgs struct{}

type statusArgs struct {
	Name string `json:"name"`
}

// selArgs 是那三个批量动作的入参，与 panel.Selection 一一对应。
//
// 用切片而不是逗号分隔的字符串：模型写错一个分隔符就是一个不存在的服务名，
// 而它看不出区别。
type selArgs struct {
	Names []string `json:"names"`
	Group string   `json:"group"`
}

type logsArgs struct {
	Name  string `json:"name"`
	Lines int    `json:"lines"`
	Date  string `json:"date"`
}

type waitArgs struct {
	Names   []string `json:"names"`
	Group   string   `json:"group"`
	Timeout string   `json:"timeout"`
}

// selection 把入参翻成面板的选择。
func selection(a selArgs) (panel.Selection, error) {
	sel := panel.Selection{Names: a.Names, Group: a.Group}
	// 明明白白给了一个空数组（"names": []）在解码后是「有值但长度为零」，
	// 与「没给」分得开——而它多半是想说「全部」却说错了话。当成全部执行的话，
	// 一次手滑动的就是整份清单。
	if a.Names != nil && len(a.Names) == 0 {
		return sel, fmt.Errorf("names 给了但是空的：要全部服务就 names 与 group 都不给")
	}
	if err := sel.Validate(); err != nil {
		return sel, err
	}
	return sel, nil
}

// describe 把一次选择说成一句人话，用在回执里。
func describe(sel panel.Selection) string {
	if g := strings.TrimSpace(sel.Group); g != "" {
		return "「" + g + "」这一组"
	}
	if len(sel.Names) > 0 {
		return strings.Join(sel.Names, "、")
	}
	return "全部服务"
}

// ── 各工具的回执 ───────────────────────────────────────────────────────────

func listText(p *panel.Panel) (string, error) {
	st := p.State()
	if !st.OK {
		return "", fmt.Errorf("读不到清单：%s", nothing(st.Error))
	}
	var b strings.Builder
	live := 0
	for _, s := range st.Services {
		if s.Running {
			live++
		}
	}
	fmt.Fprintf(&b, "共 %d 个服务，%d 个在跑。清单：%s\n", len(st.Services), live, st.ConfigPath)
	for _, s := range st.Services {
		b.WriteString(serviceLine(s))
		b.WriteByte('\n')
	}
	if len(st.Services) == 0 {
		b.WriteString("（这份清单里还没有服务）\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// serviceLine 是清单里的一行。
//
// 只写「此刻要看的那几个」：名字、分组、状态、端口、跑了多久、健康探针。
// 详情在 service_status 里，一行一行都塞进来就没人看得出哪一行要紧。
func serviceLine(s panel.ServiceOut) string {
	parts := []string{s.Name}
	if s.Group != "" {
		parts = append(parts, s.Group)
	}
	if s.Manual {
		parts = append(parts, "不参与全部启停")
	}
	parts = append(parts, s.StatusText, "端口 "+s.PortText)
	if s.Running {
		parts = append(parts, "PID "+fmt.Sprint(s.PID), s.Uptime)
	}
	if h := healthOf(s); h.Key() != view.HealthNone {
		parts = append(parts, "探针 "+h.Text())
	}
	parts = append(parts, view.ShortPath(s.Dir))
	// 出事的那几句必须在这里就看得见：一个服务起不来，模型从这一行才知道
	// 该去问哪一个，不然它只能把整个清单的详情一个个读过来。
	if s.Op != "" {
		parts = append(parts, "→ "+s.Op)
	}
	if s.OpErr != "" {
		parts = append(parts, "上次起不来："+s.OpErr)
	} else if s.Stale {
		parts = append(parts, "进程已不在")
	}
	return strings.Join(parts, " | ")
}

func statusText(p *panel.Panel, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("要一个服务名")
	}
	st := p.State()
	if !st.OK {
		return "", fmt.Errorf("读不到清单：%s", nothing(st.Error))
	}
	var found *panel.ServiceOut
	for i := range st.Services {
		if st.Services[i].Name == name {
			found = &st.Services[i]
			break
		}
	}
	if found == nil {
		// 把现有的名字都列出来：模型多半是名字记岔了，而它看不到侧栏。
		names := make([]string, 0, len(st.Services))
		for _, s := range st.Services {
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("清单里没有名为「%s」的服务（有的是：%s）", name, joinOrNone(names))
	}
	return serviceDetail(*found), nil
}

// serviceDetail 是一个服务的详情块。
func serviceDetail(s panel.ServiceOut) string {
	var b strings.Builder
	line := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Fprintf(&b, "%s：%s\n", k, v)
		}
	}
	line("服务", s.Name)
	line("目录", s.Dir)
	line("分组", s.Group)
	line("状态", s.StatusText)
	// 端口那句已经把「这次实际用的是哪个」写在里面了（见 view.PortText），
	// 换过端口的解释跟在后面。
	port := "端口：" + s.PortText
	if s.PortNote != "" {
		port += "（" + s.PortNote + "）"
	}
	line("", port)
	if s.Running {
		line("进程号", fmt.Sprint(s.PID))
		line("运行时长", s.Uptime)
	}
	if h := healthOf(s); h.Key() != view.HealthNone {
		line("健康探针", h.Text()+healthURL(h))
	}
	line("日志", s.LogPath)
	line("启动方式", s.Run)
	line("依赖", strings.Join(s.DependsOn, "、"))
	line("重启策略", s.Restart)
	line("备注", s.UserNote)
	if s.Op != "" {
		line("正在做的事", s.Op)
	}
	if s.OpErr != "" {
		line("上次失败", s.OpErr)
	}
	if s.RestartNote != "" {
		line("自动重启", s.RestartNote)
	}
	if s.DepNote != "" {
		line("前置", s.DepNote)
	}
	if s.Note != "" {
		line("说明", s.Note)
	}
	if s.Occupant != nil {
		line("占着端口的是", occupantText(s.Occupant))
	}
	if s.Diag != nil {
		// 日志里写着的是工具自己那套行话，这里给的是「所以该去做什么」。
		line("原因", s.Diag.Reason)
		line("下一步", s.Diag.Next)
		line("日志原文", s.Diag.Line)
	}
	return strings.TrimRight(b.String(), "\n")
}

func logsText(p *panel.Panel, a logsArgs) (string, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", fmt.Errorf("要一个服务名")
	}
	date := strings.TrimSpace(a.Date)
	// 日期先自己认一遍形状：面板认不出来时会当成「没给」而读今天那一份，
	// 于是「10 月 5 号怎么了」这个问题的答案是今天的日志——一句错话，
	// 而且看上去完全正常。
	if date != "" {
		if _, err := time.Parse(config.LogDateLayout, date); err != nil {
			return "", fmt.Errorf("日期要写成 %s 这样", config.LogDateLayout)
		}
	}
	lines := a.Lines
	if lines < 0 {
		return "", fmt.Errorf("lines 要一个正数")
	}
	if lines == 0 {
		lines = defaultLogLines
	}
	if lines > panel.LogLines {
		lines = panel.LogLines
	}

	out, err := p.Logs(name, date, 0)
	if err != nil {
		return "", err
	}
	text := view.TailText(out.Text, lines)
	head := name
	if out.Date != "" {
		head += " " + out.Date
	}
	if text == "" {
		if date != "" {
			return fmt.Sprintf("%s 在 %s 那天没有日志。有日志的天：%s",
				name, date, joinOrNone(out.Dates)), nil
		}
		return fmt.Sprintf("%s 还没有日志（%s 不存在）——它可能还没起来过。", name, out.Path), nil
	}
	n := len(strings.Split(text, "\n"))
	head += fmt.Sprintf(" 的最后 %d 行 · %s", n, out.Path)
	if out.Truncated {
		head += "（这只是文件末尾的一段，前面还有）"
	}
	return head + "\n" + text, nil
}

func waitText(ctx context.Context, p *panel.Panel, a waitArgs) (string, error) {
	sel, err := selection(selArgs{Names: a.Names, Group: a.Group})
	if err != nil {
		return "", err
	}
	var timeout time.Duration
	if v := strings.TrimSpace(a.Timeout); v != "" {
		d, err := proc.ParseWaitTimeout(v)
		if err != nil {
			return "", fmt.Errorf("timeout %v", err)
		}
		timeout = d
	}
	if timeout <= 0 {
		timeout = proc.HealthWait
	}

	res, err := p.WaitSetCtx(ctx, sel, timeout)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	ready := 0
	for _, r := range res {
		if r.Ready {
			ready++
		}
	}
	fmt.Fprintf(&b, "%d / %d 就绪\n", ready, len(res))
	for _, r := range res {
		fmt.Fprintf(&b, "%s：%s\n", r.Name, waitOne(r, timeout))
	}
	if ready < len(res) {
		// 「没等到」是一个答案，不是这次调用出了错：所以它不是 isError，
		// 这句话只把散在上面几行里的原因收成一句，与接口的 ok:false 一个意思。
		fmt.Fprintf(&b, "没等到：%s", view.WaitMissed(res, timeout))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// waitOne 说一个服务等成什么样了，末尾带上这一步该做什么。
//
// 三种没等到分开说（判定在 proc 里，措辞在这里）：下一步各不相同，
// 合成一句「没就绪」的话，拿到它的人只知道接着等——而接着等正是这三件里最没用的。
func waitOne(r proc.WaitResult, timeout time.Duration) string {
	switch {
	case r.Ready:
		return "已就绪  " + r.Probe
	case r.Why == proc.WaitNoProbe:
		return "没配健康探针，等不到「就绪」这个信号（要在清单里给它加一个 health 地址）"
	case r.Why == proc.WaitNotRunning:
		return "没有在运行（先调 start_service 起它，或看 service_status 里上次失败的原因）"
	case r.Why == proc.WaitTimeout:
		return fmt.Sprintf("等满了 %s，探针还没通  %s（看它卡在哪一行：read_logs）", view.Duration(timeout), r.Probe)
	}
	return "没等到就绪"
}

// ── 小件 ───────────────────────────────────────────────────────────────────

// defaultLogLines 是没给 lines 时要多少行。
//
// 比命令行那一次（`pier logs` 默认一百行）多一倍：模型常常是「起不来，看看日志」，
// 而失败现场往往在最后那几屏的开头。
const defaultLogLines = 200

// healthOf 把面板给的三个字段摆成那一份判定（见 view.Health）。
func healthOf(s panel.ServiceOut) view.Health {
	return view.Health{Has: s.HasHealth, OK: s.Healthy, URL: s.Health}
}

func healthURL(h view.Health) string {
	if h.Key() == view.HealthNone || h.URL == "" {
		return ""
	}
	return "  " + h.URL
}

func occupantText(l *proc.Listener) string {
	who := fmt.Sprintf("%s（PID %d）", l.Command, l.PID)
	if l.Service != "" {
		return who + "，是 Pier 起的服务「" + l.Service + "」"
	}
	return who + "，不是 Pier 起的"
}

// nothing 保证一句话不会空着：空字符串摆在「读不到清单：」后面，
// 读的人只会以为输出坏了。
func nothing(s string) string {
	if strings.TrimSpace(s) == "" {
		return "清单没有加载起来"
	}
	return s
}

func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "一个也没有"
	}
	return strings.Join(names, "、")
}
