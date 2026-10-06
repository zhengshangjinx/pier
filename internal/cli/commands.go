package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/diag"
	"github.com/zhengshangjinx/pier/internal/panel"
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
	cfg, _, _, err := loadConfigSource(cfgPath)
	return cfg, err
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

// portSwap 是 --port 解析出来的结果。
type portSwap struct {
	port  int  // 换到哪个端口，0 表示自己挑一个空闲的
	given bool // 命令行上给没给 --port
}

// parsePortFlag 从参数里摘出 --port，返回结果与其余参数。
//
// 与 --config 一样先摘出来再交给 pickServices：留着它的话会被当成一个服务名，
// 报出来的是「找不到服务 --port」，与真正的问题隔着一层。
func parsePortFlag(args []string) (portSwap, []string, error) {
	var out portSwap
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		arg := ""
		switch {
		case a == "--port":
			if i+1 >= len(args) {
				return out, rest, errors.New("--port 后面要跟端口号，0 表示自己挑一个空闲的")
			}
			i++
			arg = args[i]
		case strings.HasPrefix(a, "--port="):
			arg = strings.TrimPrefix(a, "--port=")
		default:
			rest = append(rest, a)
			continue
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 0 || n > 65535 {
			return out, rest, fmt.Errorf("--port 要一个 0 到 65535 之间的端口号，这里是 %q（0 表示自己挑一个空闲的）", arg)
		}
		out = portSwap{port: n, given: true}
	}
	return out, rest, nil
}

// swapPort 给出「这一次改用 port 起」的那份服务定义，port 为 0 表示自己挑一个空闲的。
//
// 换的端口只对这一次运行有效，不写回清单：占用清单里那个端口的多半是用户动不得的
// 东西（另一个项目、他打不开的某个服务），而清单该写的是「这个服务平时听哪个端口」，
// 不该被一次临时的让路改掉。
func swapPort(svc *config.Service, port int, cfg *config.Config) (*config.Service, error) {
	if svc.Port <= 0 {
		return nil, fmt.Errorf("%s 没有配端口，没有可换的——先在清单里给它填一个端口", svc.Name)
	}
	if port == 0 {
		used := map[int]bool{}
		for _, p := range cfg.UsedPorts() {
			used[p] = true
		}
		// 从清单里那个端口往后找：就近取一个，比从 1024 起扫更像人挑的。
		port = proc.FreePort(svc.Port+1, used)
		if port <= 0 {
			return nil, errors.New("没找到空闲端口，先关掉一些服务再试")
		}
	}
	if port == svc.Port {
		return nil, fmt.Errorf("换的端口还是清单里那个（%d），不必换", port)
	}
	return config.WithPort(svc, port), nil
}

// cmdUp 启动服务。先全部拉起，再统一等待就绪——否则一个 Java 服务的启动
// 会把后面所有服务的启动时间串行叠加。单个服务失败不阻断其余服务。
func cmdUp(args []string) int {
	cfgPath, rest := extractConfig(args)
	swap, rest, err := parsePortFlag(rest)
	if err != nil {
		return fail("%v", err)
	}
	return upServices(cfgPath, rest, swap, nil)
}

// upServices 是 up 真正做的那件事。resume 是「这几个服务这次接着用哪个端口起」，
// 只有 restart 会给（它在停之前读出来，见 resumePorts）；up 自己从头开始，
// 服务该用清单里写的那个端口。
func upServices(cfgPath string, names []string, swap portSwap, resume map[string]int) int {
	cfg, sup, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	targets, err := pickServices(cfg, names)
	if err != nil {
		return fail("%v", err)
	}
	// 让路是「给这一个服务换」，对着一批服务说这句话没有意义，所以只认点名的情形。
	if swap.given {
		if len(targets) != 1 {
			return fail("--port 要指名一个服务，它换的是那一个的端口：pier up api --port 0")
		}
		manifest := targets[0].Port
		swapped, err := swapPort(targets[0], swap.port, cfg)
		if err != nil {
			return fail("%v", err)
		}
		targets[0] = swapped
		fmt.Printf("  清单里写的是 %d，这次用的是 %d（只这一次，不写回清单）\n", manifest, swapped.Port)
	}
	// 重启时接着上一次让路后的端口起。与上一条说的是同一件事，也照样要说出口：
	// 界面上一直写着这次实际用的是哪个端口，命令行这边不能只在 up 那一次说。
	for i, svc := range targets {
		p, ok := resume[svc.Name]
		if !ok {
			continue
		}
		targets[i] = config.WithPort(svc, p)
		fmt.Printf("  %-14s 清单里写的是 %d，这次用的是 %d（那个端口还被占着，接着上次让的路）\n",
			svc.Name, svc.Port, p)
	}

	started := make([]*config.Service, 0, len(targets))
	failed := make([]string, 0)
	skipped := make([]string, 0)

	for _, svc := range targets {
		// 端口已被监听说明可能已经在 IDEA 或别的终端跑着，此时再起一个必然冲突。
		if svc.Port > 0 && proc.PortOpen(svc.Port) {
			fmt.Printf("  %-14s 跳过：端口 %d 已被占用（可能已在 IDEA 或其它终端运行）\n", svc.Name, svc.Port)
			skipped = append(skipped, svc.Name)
			continue
		}
		if err := sup.Start(svc); err != nil {
			fmt.Printf("  %-14s 启动失败：%v\n", svc.Name, err)
			printDiag(cfg, svc.Name, "    ")
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
		printDiag(cfg, svc.Name, "    ")
		tailLog(cfg, svc.Name, logTailLines)
		notReady = append(notReady, svc.Name)
	}

	// 收尾分节说，因为每一节要人做的下一步都不同。三节里任意一节非空就不算成功，
	// **包括「跳过」**：up 的承诺是「这些服务在跑」，而端口被别人占着的时候它们不在，
	// 那正是要人去看一眼的时候。给 0 会让脚本以为全套都起来了——最坏的一种错。
	if len(failed) > 0 {
		fmt.Printf("\n未启动：%s\n", strings.Join(failed, "、"))
	}
	if len(skipped) > 0 {
		fmt.Printf("\n跳过：%s\n", strings.Join(skipped, "、"))
		fmt.Println("  端口已被占用，多半已经在 IDEA 或别的终端里跑着；看是谁占的：pier ports")
	}
	if len(notReady) > 0 {
		// 也算失败：up 的承诺是「等到就绪」，不是「进程拉起来了」。
		// 但措辞必须说清是哪一种——这两件事的下一步动作完全不同。
		fmt.Printf("\n探针没通：%s（服务在运行，只是 %s 内没探通）\n",
			strings.Join(notReady, "、"), proc.HealthWait)
		fmt.Println("  确认地址是否写对，或者不需要探针就在界面上点「不再检查健康」。")
	}
	if len(failed)+len(skipped)+len(notReady) > 0 {
		return 1
	}

	if len(started) == 0 {
		// 一个都没跳过、也没失败，只是清单里没有可起的服务。这不是出错。
		fmt.Println("没有启动任何服务。")
		return 0
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
//
// 上一次是从清单里那个端口让路出来的，这次接着让路后的那个起：点 restart 想要的是
// 「还是刚才那个服务」，而不是「回到清单里那个正被别人占着的端口上，起不来」。
// 这件事必须赶在停之前问——停完记录就销了，销完就没人记得上一次用的是哪个端口。
func cmdRestart(args []string) int {
	cfgPath, rest := extractConfig(args)
	resume := resumePorts(cfgPath, rest)
	if code := cmdDown(append([]string{"--config", cfgPath}, rest...)); code != 0 {
		// 停止失败通常意味着端口仍被占用，继续启动只会更混乱，直接中止。
		return code
	}
	fmt.Println()
	return upServices(cfgPath, rest, portSwap{}, resume)
}

// resumePorts 读出这几个服务这次该接着用哪个端口起，判定见 proc.ResumePort。
//
// 读不动（清单加载不起来、状态文件坏了）就当没有：接着要说这件事的是后面的
// stop 与 start，它们说话时手上有完整的原因；在这里抢着先报一句，用户看到的是一条
// 没头没尾的错误加上一条说清了原因的错误。
func resumePorts(cfgPath string, names []string) map[string]int {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil
	}
	state, err := proc.LoadState(cfg.StatePath())
	if err != nil || len(state.Services) == 0 {
		return nil
	}
	targets, err := pickServices(cfg, names)
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, svc := range targets {
		if p := proc.ResumePort(svc, state); p > 0 {
			out[svc.Name] = p
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cmdStatus 以表格展示所有服务的状态。
func cmdStatus(args []string) int {
	cfgPath, rest := extractConfig(args)
	jsonOut, rest := extractJSON(rest)
	if err := noExtra("status", rest); err != nil {
		return fail("%v", err)
	}

	if jsonOut {
		return statusJSON(cfgPath)
	}

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

	// 记录还在、进程没了的那几个补一块：表格里只有「已退出」三个字，
	// 而日志里写着它为什么退出。这一句正是 status 最该回答的问题。
	for _, st := range list {
		if !st.Stale {
			continue
		}
		h, ok := diag.FromLog(cfg, st.Service.Name)
		if !ok {
			continue
		}
		fmt.Printf("\n%s\n%s", st.Service.Name, diagLines(h, "  "))
	}
	return 0
}

// statusJSON 输出与界面同一份快照。
//
// 清单打不开时照样输出：StateOut 本来就能表达这件事（ok=false + error），
// 脚本看见的是一句能读的原因，而不是一屏中文报错加一个退出码。
// 退出码仍然非 0——这一条命令没做成它该做的事，别让 `pier status --json && …` 继续往下走。
func statusJSON(cfgPath string) int {
	cfg, path, src, cfgErr := loadConfigSource(cfgPath)
	var sup *proc.Supervisor
	if cfg != nil {
		sup = proc.New(cfg)
	}
	st := panel.Snapshot(cfg, sup, path, src, errText(cfgErr), nil, nil)
	printJSON(st)
	if !st.OK {
		return 1
	}
	return 0
}

// 状态、端口、PID、运行时长、说明各列的文案统一由 internal/view 提供，
// 命令行、终端面板与图形界面共用一套措辞。

// cmdLogs 看、跟随、清理服务日志。
//
//	pier logs <服务>...            打印最近的日志（几个服务就依次打印）
//	pier logs <服务> -f           持续跟随
//	pier logs <服务> --tail N     只看最后 N 行
//	pier logs --size [--json]     看日志占了多少
//	pier logs --clean [服务...]   清理超过保留天数的日志
//	pier logs --clean --all [...] 清空（不看天数）
func cmdLogs(args []string) int {
	cfgPath, rest := extractConfig(args)
	jsonOut, rest := extractJSON(rest)

	follow, clean, all, size, tailSet := false, false, false, false, false
	tail := logTailLines * 5
	names := make([]string, 0, len(rest))

	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case a == "--clean":
			clean = true
		case a == "--all":
			all = true
		case a == "--size":
			size = true
		case a == "--tail" || strings.HasPrefix(a, "--tail="):
			arg := strings.TrimPrefix(a, "--tail=")
			if a == "--tail" {
				if i+1 >= len(rest) {
					return fail("--tail 后面要跟行数，例如：pier logs demo-admin --tail 200")
				}
				i++
				arg = rest[i]
			}
			n, err := strconv.Atoi(arg)
			if err != nil || n <= 0 {
				return fail("--tail 要一个正整数行数，收到的是「%s」", arg)
			}
			tail, tailSet = n, true
		default:
			// 认不出来的开关当场报错。以前它们会被当成服务名，报的是
			// 「没有名为 --tial 的服务」——一句看起来像自己打错了的话。
			if strings.HasPrefix(a, "-") {
				return fail("logs 不认识参数 %s（看帮助：pier logs -h）", a)
			}
			names = append(names, a)
		}
	}

	// 几个开关会互相压掉，所以先把组合说清楚，不给「给了也没反应」的余地。
	if jsonOut && !size {
		return fail("--json 只配 --size 用（日志正文本身就是要原样读的，套一层 JSON 反而要多解一层）")
	}
	if all && !clean {
		return fail("--all 只在 --clean 下有意义，例如：pier logs --clean --all")
	}
	if tailSet && (size || clean) {
		return fail("--tail 是看日志用的，不能和 --size / --clean 一起用")
	}
	if size && len(names) > 0 {
		return fail("--size 统计的是全部服务。只看某一个：pier logs --size | grep %s", names[0])
	}
	if follow && clean {
		return fail("--follow 是跟着看的，不能和 --clean 一起用")
	}
	if follow && len(names) > 1 {
		return fail("--follow 一次只能跟一个服务")
	}
	// 清理可以不带服务名（全部），看日志必须指明是哪个。
	if !clean && !size && len(names) == 0 {
		return fail("logs 需要一个服务名，例如：pier logs demo-admin")
	}

	cfg, _, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}

	switch {
	case size:
		return cmdLogSize(cfg, jsonOut)
	case clean:
		return cmdLogClean(cfg, names, all)
	}

	svcs := make([]*config.Service, 0, len(names))
	for _, n := range names {
		svc, err := cfg.Find(n)
		if err != nil {
			return fail("%v", err)
		}
		svcs = append(svcs, svc)
	}
	// 一个服务读不成（比如它还没跑过、没有日志文件）不该把其余的也拦下：
	// 一起看几个服务，正是为了看「哪个没出声」。
	code := 0
	for i, svc := range svcs {
		if i > 0 {
			fmt.Println()
		}
		if len(svcs) > 1 {
			fmt.Printf("%s\n", svc.Name)
		}
		if c := showLog(cfg, svc, tail, follow); c != 0 {
			code = c
		}
	}
	return code
}

// showLog 打印一个服务的日志尾部，follow 为真时接着跟下去。
func showLog(cfg *config.Config, svc *config.Service, n int, follow bool) int {
	// 读最新的一份而不是今天那份：跨了零点还在跑的服务写的一直是启动那天的文件。
	path := proc.LogFile(cfg, svc.Name)
	f, err := os.Open(path)
	if err != nil {
		return fail("读取 %s 的日志失败：%v", svc.Name, err)
	}
	defer f.Close()

	if err := printTail(f, n); err != nil {
		return fail("%v", err)
	}
	if !follow {
		fmt.Fprintf(os.Stderr, "\n（完整日志：%s　持续跟随：pier logs %s -f）\n", path, svc.Name)
		return 0
	}
	return followFile(path)
}

// cmdLogSize 列出日志目录的占用，按服务从大到小。
//
// 表格与 --json 走同一份 panel.LogUsageOf：同一个数字在两种输出里必须是同一个说法，
// 各自算一遍迟早一个「23.6 MB」一个「24 MB」（而它正是「清哪几个」的依据）。
func cmdLogSize(cfg *config.Config, jsonOut bool) int {
	usage := panel.LogUsageOf(cfg)
	if jsonOut {
		return printJSON(usage)
	}
	rows := make([][]string, 0, len(usage.Services))
	for _, s := range usage.Services {
		span := view.Dash
		if s.Newest != "" {
			span = s.Oldest + " ~ " + s.Newest
		}
		rows = append(rows, []string{s.Name, s.Size, fmt.Sprintf("%d", s.Files), span})
	}
	if len(rows) == 0 {
		fmt.Println("还没有任何日志。")
		return 0
	}
	renderTable([]string{"服务", "占用", "文件数", "覆盖日期"}, rows)
	fmt.Printf("\n合计 %s（%d 个文件）　目录：%s\n", usage.Size, usage.Files, usage.Dir)
	fmt.Printf("保留最近 %d 天；清理超期日志：pier logs --clean\n", usage.KeepDays)

	// 单日写得太多的单挑出来说：这件事在表格里看不出来（合计可能很正常，
	// 而它正按那个速度往下写），却正是把磁盘吃掉的那一个。话由后端给
	// （见 panel.logBigNote），界面与这里说的是同一句。
	var loud []panel.LogServiceOut
	for _, s := range usage.Services {
		if s.BigNote != "" {
			loud = append(loud, s)
		}
	}
	if len(loud) > 0 {
		fmt.Printf("\n⚠ 单日写得太多（提醒线 %s）：\n", usage.DayWarn)
		for _, s := range loud {
			fmt.Printf("  %-14s %s\n", s.Name, s.BigNote)
		}
	}
	return 0
}

// cmdLogClean 清理日志。all 为真时不分日期一律清掉，否则只清超过保留天数的。
//
// 名字可以给几个，也可以一个不给（全部）。
func cmdLogClean(cfg *config.Config, names []string, all bool) int {
	for _, n := range names {
		if _, err := cfg.Find(n); err != nil {
			return fail("%v", err)
		}
	}

	// 正在跑的服务跳过：它的日志 fd 由那个独立进程握着，删掉文件只是
	// 从目录里摘掉，进程照写不误，而释放的空间要等它退出才真的空出来。
	busy, err := proc.RunningNames(cfg.StatePath())
	if err != nil {
		busy = map[string]bool{}
	}

	code := 0
	// 一次一个服务地走：谁没清成，必须一眼看见（退出码是给脚本看的，
	// 这一行是给人看的）。指到不存在的名字上面已经拦下了，这里不会再报错。
	for _, name := range names {
		if busy[name] {
			fmt.Printf("  %s：正在运行，日志还在写，先停下再清理\n", name)
			// 想清的那一份没清成。以前这里返回 0，脚本会以为清干净了。
			code = 1
			continue
		}
		if c := cleanOne(cfg, name, all, busy); c != 0 {
			code = c
		}
	}
	// 不点名就是全部。这时跳过正在跑的那些是正常结果（它们本来就不该被清），
	// 报出来的数字已经把它们排除在外，所以不算失败。
	if len(names) == 0 {
		if c := cleanOne(cfg, "", all, busy); c != 0 {
			code = c
		}
	}
	return code
}

// cleanOne 清理一个服务的日志；name 为空表示全部服务。busy 里的那些跳过不删。
func cleanOne(cfg *config.Config, name string, all bool, busy map[string]bool) int {
	what := "全部服务"
	if name != "" {
		what = name
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

// diagLines 把一条诊断铺成两行，缩进由调用方给。
//
// 原文必须一起给：端口号、模块名、缺的符号这些字只在原文里有，而它们恰恰是
// 「去改哪一处」的答案；只说一句概括，用户还得回去翻日志找那一行。
func diagLines(h diag.Hit, indent string) string {
	return fmt.Sprintf("%s%s：%s\n%s原文：%s\n", indent, h.Reason, h.Next, indent, h.Line)
}

// printDiag 认识不出来就什么都不打：空着比说错强（见 internal/diag）。
func printDiag(cfg *config.Config, name, indent string) {
	if h, ok := diag.FromLog(cfg, name); ok {
		fmt.Print(diagLines(h, indent))
	}
}

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
