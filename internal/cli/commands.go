package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// logTailLines 是失败时回显的日志行数。
const logTailLines = 20

// extractConfig 从参数里摘出 --config，返回配置路径与其余参数。
func extractConfig(args []string) (string, []string) {
	var path string
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--config" && i+1 < len(args):
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			path = strings.TrimPrefix(a, "--config=")
		default:
			out = append(out, a)
		}
	}
	return path, out
}

// loadConfig 决定这次用哪份清单：给了 --config 就用它（YAML 只读，或者某份数据文件）；
// 没给就用 Pier 自己的数据文件，和界面是同一份。
func loadConfig(cfgPath string) (*config.Config, error) {
	if strings.TrimSpace(cfgPath) != "" {
		return config.Open(cfgPath)
	}
	return config.OpenDefault()
}

// setup 加载配置并构造 supervisor。
func setup(cfgPath string) (*config.Config, *proc.Supervisor, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	return cfg, proc.New(cfg), nil
}

// pickServices 按名字挑选服务；没有给名字则表示全部。
func pickServices(cfg *config.Config, names []string) ([]*config.Service, error) {
	if len(names) == 0 {
		return cfg.Services, nil
	}
	out := make([]*config.Service, 0, len(names))
	for _, n := range names {
		svc, err := cfg.Find(n)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, nil
}

// cmdUp 启动服务。先全部拉起，再统一等待就绪——否则一个 Java 服务的启动
// 会把后面所有服务的启动时间串行叠加。单个服务失败不阻断其余服务。
func cmdUp(args []string) int {
	cfgPath, rest := extractConfig(args)
	cfg, sup, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	targets, err := pickServices(cfg, rest)
	if err != nil {
		return fail("%v", err)
	}

	started := make([]*config.Service, 0, len(targets))
	failed := make([]string, 0)

	for _, svc := range targets {
		// 端口已被监听说明可能已经在 IDEA 或别的终端跑着，此时再起一个必然冲突。
		if svc.Port > 0 && proc.PortOpen(svc.Port) {
			fmt.Printf("  %-14s 跳过：端口 %d 已被占用（可能已在 IDEA 或其它终端运行）\n", svc.Name, svc.Port)
			continue
		}
		if err := sup.Start(svc); err != nil {
			fmt.Printf("  %-14s 启动失败：%v\n", svc.Name, err)
			tailLog(cfg, svc.Name, logTailLines)
			failed = append(failed, svc.Name)
			continue
		}
		fmt.Printf("  %-14s 已启动\n", svc.Name)
		started = append(started, svc)
	}

	// 没通过探针和没启动是两件事，分开记：前者进程已经在跑了，问题出在探针那一路
	// （地址填错、服务没有健康接口），要人去改的是配置，不是这次的启动。
	notReady := make([]string, 0)
	for _, svc := range started {
		if svc.Health == "" {
			continue
		}
		if proc.WaitHealthy(svc.Health, proc.HealthWait) {
			fmt.Printf("  %-14s 已就绪  %s\n", svc.Name, svc.Health)
			continue
		}
		fmt.Printf("  %-14s 已启动，但 %s 内没通过探针 %s（服务本身在运行）\n",
			svc.Name, proc.HealthWait, svc.Health)
		tailLog(cfg, svc.Name, logTailLines)
		notReady = append(notReady, svc.Name)
	}

	if len(failed) > 0 {
		fmt.Printf("\n未启动：%s\n", strings.Join(failed, "、"))
		return 1
	}
	if len(started) == 0 {
		fmt.Println("\n没有启动任何服务。")
		return 0
	}
	if len(notReady) > 0 {
		// 仍然算失败：up 的承诺是「等到就绪」，不是「进程拉起来了」。
		// 但措辞必须说清是哪一种——这两件事的下一步动作完全不同。
		fmt.Printf("\n已启动 %d 个服务，其中 %s 没通过健康探针。\n",
			len(started), strings.Join(notReady, "、"))
		fmt.Println("这些服务在运行，只是探针没探通：确认地址是否写对，或者不需要探针就在界面上点「不再检查健康」。")
		return 1
	}
	fmt.Printf("\n已启动 %d 个服务。查看状态：pier status\n", len(started))
	return 0
}

// cmdDown 停止服务。对「不是 Pier 启动的」与「已经退出」两种情况只作提示，
// 不算失败——重复执行 down 应当是安全的。
func cmdDown(args []string) int {
	cfgPath, rest := extractConfig(args)
	cfg, sup, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	targets, err := pickServices(cfg, rest)
	if err != nil {
		return fail("%v", err)
	}

	failed := make([]string, 0)
	for _, svc := range targets {
		err := sup.Stop(svc.Name)
		switch {
		case err == nil:
			fmt.Printf("  %-14s 已停止\n", svc.Name)
		case errors.Is(err, proc.ErrNotManaged):
			// 没有记录有两种情况，必须区分：本来就停着（正常，静默通过），
			// 或端口被 IDEA / 别的终端占着（要提醒，因为 Pier 动不了它）。
			if svc.Port > 0 && proc.PortOpen(svc.Port) {
				fmt.Printf("  %-14s 端口 %d 被 Pier 之外的进程占用，未做处理\n", svc.Name, svc.Port)
				continue
			}
			fmt.Printf("  %-14s 本就未运行\n", svc.Name)
		case errors.Is(err, proc.ErrAlreadyGone):
			fmt.Printf("  %-14s 已退出，清理了记录\n", svc.Name)
		default:
			fmt.Printf("  %-14s 停止失败：%v\n", svc.Name, err)
			failed = append(failed, svc.Name)
		}
	}

	if len(failed) > 0 {
		fmt.Printf("\n未能停止：%s\n", strings.Join(failed, "、"))
		return 1
	}
	return 0
}

// cmdRestart 先停后起。停止阶段的「未启动」不影响后续启动。
func cmdRestart(args []string) int {
	cfgPath, rest := extractConfig(args)
	if code := cmdDown(append([]string{"--config", cfgPath}, rest...)); code != 0 {
		// 停止失败通常意味着端口仍被占用，继续启动只会更混乱，直接中止。
		return code
	}
	fmt.Println()
	return cmdUp(append([]string{"--config", cfgPath}, rest...))
}

// cmdStatus 以表格展示所有服务的状态。
func cmdStatus(args []string) int {
	cfgPath, _ := extractConfig(args)
	cfg, sup, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	list, err := sup.Status()
	if err != nil {
		return fail("%v", err)
	}

	fmt.Printf("%s\n\n", cfg.Path)
	rows := make([][]string, 0, len(list))
	for _, st := range list {
		rows = append(rows, []string{
			st.Service.Name,
			view.StatusText(st),
			view.PortText(st),
			view.PIDText(st),
			view.UptimeText(st),
			view.NoteText(st),
		})
	}
	renderTable([]string{"服务", "状态", "端口", "PID", "运行时长", "说明"}, rows)
	return 0
}

// 状态、端口、PID、运行时长、说明各列的文案统一由 internal/view 提供，
// 命令行、终端面板与图形界面共用一套措辞。

// cmdLogs 看、跟随、清理服务日志。
//
//	pier logs <服务>              打印最近的日志
//	pier logs <服务> -f           持续跟随
//	pier logs --size              看日志占了多少
//	pier logs --clean [服务]      清理超过保留天数的日志
//	pier logs --clean --all [服务] 清空（不看天数）
func cmdLogs(args []string) int {
	cfgPath, rest := extractConfig(args)
	follow, clean, all, size := false, false, false, false
	names := make([]string, 0, len(rest))
	for _, a := range rest {
		switch a {
		case "-f", "--follow":
			follow = true
		case "--clean":
			clean = true
		case "--all":
			all = true
		case "--size":
			size = true
		default:
			names = append(names, a)
		}
	}
	// 清理可以不带服务名（全部），看日志必须指明是哪个。
	if !clean && !size && len(names) != 1 {
		return fail("logs 需要一个服务名，例如：pier logs demo-admin")
	}
	if len(names) > 1 {
		return fail("logs 一次只处理一个服务")
	}
	var name string
	if len(names) == 1 {
		name = names[0]
	}

	cfg, _, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}

	if size {
		return cmdLogSize(cfg)
	}
	if clean {
		return cmdLogClean(cfg, name, all)
	}

	svc, err := cfg.Find(name)
	if err != nil {
		return fail("%v", err)
	}

	// 读最新的一份而不是今天那份：跨了零点还在跑的服务写的一直是启动那天的文件。
	path := proc.LogFile(cfg, svc.Name)
	f, err := os.Open(path)
	if err != nil {
		return fail("读取 %s 的日志失败：%v", svc.Name, err)
	}
	defer f.Close()

	if err := printTail(f, logTailLines*5); err != nil {
		return fail("%v", err)
	}
	if !follow {
		fmt.Fprintf(os.Stderr, "\n（完整日志：%s　持续跟随：pier logs %s -f）\n", path, svc.Name)
		return 0
	}
	return followFile(path)
}

// cmdLogSize 列出日志目录的占用，按服务从大到小。
func cmdLogSize(cfg *config.Config) int {
	usage := proc.LogUsage(cfg.LogDir(), proc.LogKeepDays, time.Now())
	rows := make([][]string, 0, len(usage.Services))
	for _, s := range usage.Services {
		span := view.Dash
		if s.Newest != "" {
			span = s.Oldest + " ~ " + s.Newest
		}
		rows = append(rows, []string{s.Name, view.Bytes(s.Bytes), fmt.Sprintf("%d", s.Files), span})
	}
	if len(rows) == 0 {
		fmt.Println("还没有任何日志。")
		return 0
	}
	renderTable([]string{"服务", "占用", "文件数", "覆盖日期"}, rows)
	fmt.Printf("\n合计 %s（%d 个文件）　目录：%s\n", view.Bytes(usage.Bytes), usage.Files, usage.Dir)
	fmt.Printf("保留最近 %d 天；清理超期日志：pier logs --clean\n", usage.KeepDays)
	return 0
}

// cmdLogClean 清理日志。all 为真时不分日期一律清掉，否则只清超过保留天数的。
func cmdLogClean(cfg *config.Config, name string, all bool) int {
	what := "全部服务"
	if name != "" {
		if _, err := cfg.Find(name); err != nil {
			return fail("%v", err)
		}
		what = name
	}

	// 正在跑的服务跳过：它的日志 fd 由那个独立进程握着，删掉文件只是
	// 从目录里摘掉，进程照写不误，而释放的空间要等它退出才真的空出来。
	busy, err := proc.RunningNames(cfg.StatePath())
	if err != nil {
		busy = map[string]bool{}
	}
	if name != "" && busy[name] {
		fmt.Printf("  %s：正在运行，日志还在写，先停下再清理\n", name)
		return 0
	}

	var out proc.LogCleanOut
	if all {
		out = proc.ClearLogs(cfg.LogDir(), name, busy)
	} else {
		out = proc.PruneLogs(cfg.LogDir(), name, busy, proc.LogKeepDays, time.Now())
	}
	if out.Files == 0 {
		if all {
			fmt.Printf("  %s：日志本来就是空的\n", what)
		} else {
			fmt.Printf("  %s：没有超过 %d 天的日志\n", what, proc.LogKeepDays)
		}
		return 0
	}
	fmt.Printf("  %s：删除了 %d 个日志文件，释放 %s\n", what, out.Files, view.Bytes(out.Bytes))
	return 0
}

// cmdDoctor 见 doctor.go。

// tailLog 在启动失败时就地回显日志尾部，省去再敲一次 logs。
func tailLog(cfg *config.Config, name string, n int) {
	f, err := os.Open(proc.LogFile(cfg, name))
	if err != nil {
		return
	}
	defer f.Close()
	if err := printTail(f, n); err != nil {
		return
	}
}

// printTail 打印文件末尾 n 行。日志文件通常不大，整读后再切行比维护环形缓冲更简单。
func printTail(f *os.File, n int) error {
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		fmt.Println("    " + l)
	}
	return nil
}

// followFile 轮询追加内容，等价于 tail -f。
// 用轮询而非 inotify：日志由被启动的进程写，轮询足够且没有额外依赖。
func followFile(path string) int {
	var offset int64
	if fi, err := os.Stat(path); err == nil {
		offset = fi.Size()
	}
	for {
		f, err := os.Open(path)
		if err != nil {
			return fail("日志文件已不可读：%v", err)
		}
		if _, err := f.Seek(offset, 0); err != nil {
			f.Close()
			return fail("定位日志失败：%v", err)
		}
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				offset += int64(n)
				fmt.Print(string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
		f.Close()
		time.Sleep(300 * time.Millisecond)
	}
}

// renderTable 打印对齐的表格。中文是双宽字符，按显示宽度而非字节数补空格，
// 否则含中文的列会参差不齐。
func renderTable(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = runewidth.StringWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if i >= len(widths) {
				break
			}
			if w := runewidth.StringWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}

	printRow := func(cells []string) {
		var b strings.Builder
		for i, c := range cells {
			if i >= len(widths) {
				break
			}
			b.WriteString(c)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-runewidth.StringWidth(c)+2))
			}
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}

	printRow(headers)
	sep := make([]string, len(headers))
	for i := range sep {
		sep[i] = strings.Repeat("-", widths[i])
	}
	printRow(sep)
	for _, r := range rows {
		printRow(r)
	}
}
