// Package proc 负责服务的启动、停止与状态维护。
//
// 与 IDEA 的行为对齐：启动一次后服务常驻，不占用当前终端，Pier 退出也不影响它；
// 停止时按进程组整体回收，避免 mvn / pnpm 派生的子进程变成占着端口的孤儿。
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/execpath"
	"github.com/zhengshangjinx/pier/internal/toolchain"
)

// ErrNotManaged 表示该服务不在 Pier 的状态记录里——可能是在 IDEA 或别的终端启动的。
// 这类进程 Pier 一律不碰，交由调用方决定是提示还是忽略。
var ErrNotManaged = errors.New("不是由 Pier 启动的")

// ErrAlreadyGone 表示记录里的进程已经不存在（服务自己退了，或被人手工 kill）。
var ErrAlreadyGone = errors.New("进程已不存在")

// Supervisor 按配置启停服务，并把进程信息落到状态文件。
type Supervisor struct {
	cfg *config.Config
	// resolvers 按服务缓存解析器：工具链覆盖是「全局 + 服务级」两层，
	// 不同服务可能需要不同 JDK，不能共用一份解析结果。
	resolvers map[string]*toolchain.Resolver
	// mu 保护 resolvers 与各解析器自己的缓存：界面刷新状态（显示用哪个 SDK）和
	// 后台启动服务会同时来解析。
	mu sync.Mutex
}

func New(cfg *config.Config) *Supervisor {
	return &Supervisor{cfg: cfg, resolvers: map[string]*toolchain.Resolver{}}
}

// ResolverFor 返回指定服务的工具链解析器，服务级覆盖优先于全局。
func (s *Supervisor) ResolverFor(svc *config.Service) *toolchain.Resolver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolverFor(svc)
}

func (s *Supervisor) resolverFor(svc *config.Service) *toolchain.Resolver {
	if r, ok := s.resolvers[svc.Name]; ok {
		return r
	}
	r := toolchain.New()
	// 项目声明（.nvmrc、.venv、mvnw、go.mod）按服务目录读；SDK 管理里手动添加的与全局默认
	// 来自 ~/.pier/settings.json。
	r.ProjectDir = svc.AbsDir()
	st := config.DefaultSettings()
	r.Manual, r.Defaults = map[toolchain.Kind][]string{}, map[toolchain.Kind]string{}
	for k, v := range st.SDKs {
		r.Manual[toolchain.Kind(k)] = v
	}
	for k, v := range st.SDKDefaults {
		r.Defaults[toolchain.Kind(k)] = v
	}
	apply := func(m map[string]string) {
		for k, v := range m {
			if strings.TrimSpace(v) != "" {
				r.Overrides[toolchain.Kind(k)] = v
			}
		}
	}
	apply(s.cfg.Toolchain)
	apply(svc.Toolchain)
	// 没有显式钉 JDK 的 Java 服务，按 pom 里要求的版本挑；钉了的以钉的为准（Overrides 优先）。
	kind := svc.Kind
	if kind == "" {
		kind, _ = config.DetectKind(svc.AbsDir())
	}
	if kind == config.KindJava {
		r.JavaMajor = svc.JavaMajor()
	}
	s.resolvers[svc.Name] = r
	return r
}

// Start 编译并启动服务。
func (s *Supervisor) Start(svc *config.Service) error {
	return s.StartContext(context.Background(), svc)
}

// StartContext 与 Start 相同，但可以被取消：编译阶段取消会连同 mvn / pnpm 派生的
// 整组子进程一起结束；编译完、拉起服务之前取消就不再拉起。面板上「启动中也能停止」靠它。
// 取消时返回的错误满足 errors.Is(err, context.Canceled)。
func (s *Supervisor) StartContext(ctx context.Context, svc *config.Service) error {
	state, err := LoadState(s.cfg.StatePath())
	if err != nil {
		return err
	}
	if e, ok := state.Services[svc.Name]; ok && ProcessAlive(e.PID) {
		return fmt.Errorf("服务 %s 已在运行（PID %d）", svc.Name, e.PID)
	}

	if err := os.MkdirAll(s.cfg.LogDir(), 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败：%w", err)
	}
	if err := os.MkdirAll(s.cfg.BinDir(), 0o755); err != nil {
		return fmt.Errorf("创建产物目录失败：%w", err)
	}

	// 顺手清一次这个服务的超期日志。放在这里而不是起一个定时任务：启动本来就是
	// 「日志目录要被用到」的时刻，不长住的工具没必要为清理多留一个常驻循环。
	// 顺手清一次这个服务的超期日志。skip 给空：启动的一瞬间它自己还没进
	// 状态文件（记录是在进程起来之后才写的），而这里清的又正是它自己那一个目录——
	// 按「在跑的都跳过」来判会把「顺手清一次」整个废掉。
	PruneLogs(s.cfg.LogDir(), svc.Name, nil, LogKeepDays, time.Now())

	// 日志先于一切准备步骤打开：本次尝试的任何失败都要落进日志。
	//
	// 按天分文件、同一天里追加（不是每次启动清空）：一天一份是让单个文件有上界，
	// 追加是让「今天重启了三次」这件事留得下来。追加带来的老问题——失败时看到的是
	// 上一次运行的旧日志——由每次启动都写的那行「=== 服务名 启动于 …」隔开，
	// 读日志的一方（panel.Logs）从那行开始截。
	if err := os.MkdirAll(s.cfg.LogDirFor(svc.Name), 0o755); err != nil {
		return fmt.Errorf("创建服务日志目录失败：%w", err)
	}
	logPath := s.cfg.LogPath(svc.Name)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}
	defer logFile.Close()

	// 今天已经跑过一次（或几次）就空一行再开新的一段：上一次的输出未必以换行结尾，
	// 紧挨着写会让「=== 」那行粘在上一条日志的尾巴上，看着像它的一部分。
	if fi, err := logFile.Stat(); err == nil && fi.Size() > 0 {
		fmt.Fprint(logFile, "\n")
	}
	fmt.Fprintf(logFile, "%s %s\n", LogStartMarker(svc.Name), time.Now().Format(time.RFC3339))
	fmt.Fprintf(logFile, "--- 工作目录：%s\n", svc.AbsDir())

	// failEarly 把准备阶段的失败同时写进日志并作为返回值，保证「终端提示」与「日志」一致。
	failEarly := func(err error) error {
		fmt.Fprintf(logFile, "!!! %v\n", err)
		return err
	}

	plan, err := svc.Plan(s.cfg)
	if err != nil {
		return failEarly(err)
	}
	env, err := s.buildEnv(svc, plan)
	if err != nil {
		return failEarly(err)
	}
	// 用了哪个 SDK、为什么用它，写在日志最前面：版本不对导致的编译失败，
	// 看这几行就知道是选错了 SDK 还是项目本身的问题。
	if tools, _, err := s.Tools(svc); err == nil {
		for _, t := range tools {
			fmt.Fprintf(logFile, "--- %s：%s（%s，依据：%s）\n", t.Kind, t.Label(), t.Bin, t.Reason)
			if t.Warn != "" {
				fmt.Fprintf(logFile, "!!! %s\n", t.Warn)
			}
		}
	}
	// 把命令名固化成绝对路径，避免受 Pier 自身 PATH 的影响。
	// 只替换执行用的副本，plan 保留原样，日志与状态里展示的命令才可读。
	buildArgv, runArgv := plan.Build, plan.Run
	if len(buildArgv) > 0 {
		exe, err := s.resolveExe(buildArgv[0], svc, plan)
		if err != nil {
			return failEarly(fmt.Errorf("服务 %s 编译阶段%w", svc.Name, err))
		}
		buildArgv = append([]string{exe}, buildArgv[1:]...)
	}
	runExe, err := s.resolveExe(runArgv[0], svc, plan)
	if err != nil {
		return failEarly(fmt.Errorf("服务 %s 启动阶段%w", svc.Name, err))
	}
	runArgv = append([]string{runExe}, runArgv[1:]...)
	// 展开脚本、换成叫服务名的那份可执行文件。只动执行用的这份 argv，
	// plan.Run 原样留着：日志与界面上的「启动方式」要的是人能读的命令。
	runArgv = s.resolveRunExec(runArgv, svc, plan)

	// 编译同步执行：失败必须立刻可见，而不是留下一个反复重启的空壳。
	if len(buildArgv) > 0 {
		fmt.Fprintf(logFile, "--- 编译：%s\n", displayCmd(plan.Build))
		if err := runSync(ctx, svc.AbsDir(), buildArgv, env, logFile); err != nil {
			if ctx.Err() != nil {
				fmt.Fprintf(logFile, "--- 已取消：编译被停止\n")
				return context.Canceled
			}
			// 报错里那句「详见日志」必须是真的。编译进程自己吐的原因在上面，
			// 但起不来有一半是根本没跑起来（目录不存在、可执行文件找不到、权限不对），
			// 那种情况下日志里只有上面那行「--- 编译：」，翻过去一行错都没有。
			fmt.Fprintf(logFile, "!!! %v\n", err)
			return fmt.Errorf("服务 %s 编译失败，详见 %s", svc.Name, logPath)
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintf(logFile, "--- 已取消：没有拉起服务\n")
		return context.Canceled
	}

	fmt.Fprintf(logFile, "--- 运行：%s\n", displayCmd(plan.Run))
	// 真正 exec 的东西常常和上面那行不一样（展开了 shebang、换成了硬链路径）。
	// 不写出来，事后没人知道活动监视器里那个名字是怎么来的。
	if real := strings.Join(runArgv, " "); real != strings.Join(plan.Run, " ") {
		fmt.Fprintf(logFile, "--- 实际执行：%s\n", real)
	}

	cmd := exec.Command(runArgv[0], runArgv[1:]...)
	// 进程名单独设：cmd.Path 是要 exec 的那个文件，cmd.Args[0] 才是进程名。
	// 两者能分开，正是「同一个 node 硬链成服务名」得以成立的原因。
	cmd.Args = namedArgv(cmd.Args, svc.Name)
	cmd.Dir = svc.AbsDir()
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// 独立会话：服务不再受当前终端牵制，关掉终端或 Pier 退出都不会把它带走。
	// 停止一律通过 pier down，走进程组信号。具体怎么脱离由平台层决定
	// （unix 是 setsid，见 sys_unix.go）。
	SetDetached(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s 失败：%w", svc.Name, err)
	}
	pid := cmd.Process.Pid
	// 记下这条服务那棵树的组号，事后按它认领（见 Entry.PGID）。
	pgid := newSession(pid)
	// 有意不 Wait：Pier 不是常驻监督进程，服务交给系统托管即可。
	_ = cmd.Process.Release()

	entry := &Entry{
		PID:       pid,
		PGID:      pgid,
		Command:   displayCmd(plan.Run),
		StartedAt: time.Now(),
		LogPath:   logPath,
		Ident:     processIdent(pid),
	}
	// 记状态与拉起进程必须同生共死：状态写不进去，这个进程就成了孤儿——界面看不见
	// 它，pier down 也找不着它，只能去活动监视器手工杀。宁可这次启动不成立，
	// 把刚拉起的整组收掉，让调用方拿到一个干净的「没启动成功」。
	//
	// 状态在锁里重读一遍而不是拿开头那份：编译期间可能已经有别人（另一个窗口、
	// 命令行）把它拉起来了，那种情况下要收的是自己这一个，而不是把别人的记录盖掉。
	if err := UpdateState(s.cfg.StatePath(), func(st *State) error {
		if e, ok := st.Services[svc.Name]; ok && e.PID != pid && ProcessAlive(e.PID) {
			return fmt.Errorf("服务 %s 已在运行（PID %d）", svc.Name, e.PID)
		}
		st.Services[svc.Name] = entry
		return nil
	}); err != nil {
		_ = KillGroup(pgid, pid, sigKill)
		_ = WaitGone(pid, stopGrace)
		return fmt.Errorf("服务 %s 已拉起但没能记入状态文件，已回收该进程：%w", svc.Name, err)
	}
	return nil
}

// Stop 停止服务。只处理 Pier 自己启动的服务，绝不碰 IDEA 或手工起的进程。
func (s *Supervisor) Stop(name string) error {
	// 取记录与判定放进锁里，发信号与等它退出留在锁外：后者最坏要耗掉两个宽限期，
	// 抱着状态锁做会把这段时间里别人的启停全堵在门外。
	var e *Entry
	gone := false
	if err := UpdateState(s.cfg.StatePath(), func(state *State) error {
		cur, ok := state.Services[name]
		if !ok {
			return fmt.Errorf("服务 %s %w", name, ErrNotManaged)
		}
		// 进程已不在，或 PID 已被系统复用：只清理记录，绝不发信号，
		// 否则可能误杀一个恰好复用该 PID 的无关进程。
		if !sameEntry(cur) {
			delete(state.Services, name)
			gone = true
			return nil
		}
		e = cur
		return nil
	}); err != nil {
		return err
	}
	if gone {
		return fmt.Errorf("服务 %s 的 %w，已清理其记录", name, ErrAlreadyGone)
	}

	if err := KillGroup(e.PGID, e.PID, sigTerm); err != nil {
		return fmt.Errorf("停止 %s 失败：%w", name, err)
	}
	// 宽限期内没退就强杀整组；两次都失败才报错，且保留记录便于人工处理。
	if !WaitGone(e.PID, stopGrace) {
		_ = KillGroup(e.PGID, e.PID, sigKill)
		if !WaitGone(e.PID, stopGrace) {
			return fmt.Errorf("服务 %s 未能停止，请手工确认 PID %d", name, e.PID)
		}
	}

	// 停干净了才销记录。再读一次盘：等待期间别的进程可能已经清理过它了。
	return UpdateState(s.cfg.StatePath(), func(state *State) error {
		delete(state.Services, name)
		return nil
	})
}

// Status 是单个服务的运行情况快照。
type Status struct {
	Service *config.Service
	// Running 表示 Pier 记录的进程仍在运行。
	Running bool
	PID     int
	// PGID 是记录在案的进程组号，Running 为真时才有意义。
	// 资源用量按进程组求和（见 SampleMetrics），这一项就是把服务和采样结果对上的那把钥匙。
	PGID   int
	Uptime time.Duration
	// PortOpen 表示端口已被监听（可能来自 IDEA 或其它终端起的实例）。
	PortOpen bool
	// Occupant 是占着该端口的进程，由同一次 lsof 一并带回，不额外开销。
	// 端口开着但又不是 Pier 起的时，界面靠它回答「那到底是谁在占」。
	Occupant *Listener
	// Stale 表示状态文件里有记录但进程已经不在。
	Stale bool
	// Healthy 表示健康探针通过；HasHealth 表示该服务配置了探针。
	// 两者要分开：没配探针和探针失败是不同的事，不能都显示成「不健康」。
	Healthy   bool
	HasHealth bool
	// ProbeExpired 表示探针已经等过了等待窗口还没通过。
	//
	// 探针是「就绪信号」，不是「成败判据」。进程活着、端口在听，服务就是起来了；
	// 探针不通只说明这一路探针探不通——地址填错了、这个服务根本没有健康接口、
	// 或者接口不返回 2xx。等满窗口之后状态从「启动中」落到「运行中」，
	// 说明那行保留「探针未通过」：既不假装它就绪了，也不假装它起失败了。
	//
	// 没配探针的服务这一项恒为假（那是「不需要探针」，不是「探针没过」）。
	ProbeExpired bool
}

// Status 汇总所有服务的运行情况，顺序与配置一致。
func (s *Supervisor) Status() ([]Status, error) {
	state, err := LoadState(s.cfg.StatePath())
	if err != nil {
		return nil, err
	}
	// 一次列出全部监听端口再逐个比对：Status 会被交互面板每两秒调一次，
	// 让每个服务各跑一次 lsof 纯属浪费。
	listening := ListeningInfo()
	out := make([]Status, 0, len(s.cfg.Services))
	for _, svc := range s.cfg.Services {
		st := Status{Service: svc}
		if svc.Port > 0 {
			if listening != nil {
				if l, ok := listening[svc.Port]; ok {
					st.PortOpen = true
					occ := l
					// 顺手认一下这是不是自家服务：端口多半握在子进程手里，
					// 界面上「被 node 占用」十有八九就是自己的服务。
					occ.Service = ManagedName(state, occ.PID)
					st.Occupant = &occ
				}
			} else {
				st.PortOpen = PortOpen(svc.Port)
			}
		}
		if e, ok := state.Services[svc.Name]; ok {
			if sameEntry(e) {
				st.Running = true
				st.PID = e.PID
				st.PGID = e.PGID
				st.Uptime = time.Since(e.StartedAt)
			} else {
				st.Stale = true
			}
		}
		// 进程在跑不等于服务可用：编译型服务起来后还要初始化，所以额外探一次健康。
		if svc.Health != "" && st.Running {
			st.HasHealth = true
			st.Healthy = ProbeHealth(svc.Health)
			// 超过等待窗口还没通过就不再算「启动中」，见 ProbeExpired 的说明。
			st.ProbeExpired = !st.Healthy && st.Uptime > HealthWait
		}
		out = append(out, st)
	}
	return out, nil
}

// buildEnv 组装服务的运行环境：先注入所需工具链，再叠加服务自定义变量。
// 注入 JAVA_HOME 是关键一步——本机 PATH 里 /usr/bin/java 是 macOS 的占位桩，
// 会遮蔽 sdkman 的真 JDK，导致 mvn 报 "Unable to locate a Java Runtime"。
// ResetToolchains 丢掉已解析的工具链，下次按最新的设置与磁盘重新选。
// 「SDK 管理」里改了默认、加减了 SDK 之后调用。
func (s *Supervisor) ResetToolchains() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolvers = map[string]*toolchain.Resolver{}
	toolchain.InvalidateDiscovery()
}

// Tools 返回服务启动时要用的全部工具链（按启动方案的顺序）。界面显示「用的是哪个 SDK」、
// 启动日志开头写明选择依据，都从这里取，保证和真正执行时是同一份结果。
func (s *Supervisor) Tools(svc *config.Service) ([]*toolchain.Tool, *config.Plan, error) {
	plan, err := svc.Plan(s.cfg)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.resolverFor(svc)
	var out []*toolchain.Tool
	for _, k := range plan.Tools {
		t, err := r.Resolve(k)
		if err != nil {
			return nil, plan, fmt.Errorf("服务 %s 需要 %s，但%w", svc.Name, k, err)
		}
		out = append(out, t)
	}
	return out, plan, nil
}

func (s *Supervisor) buildEnv(svc *config.Service, plan *config.Plan) ([]string, error) {
	env := os.Environ()
	tools, _, err := s.Tools(svc)
	if err != nil {
		return nil, err
	}
	// PATH 按工具顺序依次前置，互不覆盖：Java 的 bin 和 Maven 的 bin 都得在上面。
	var pathDirs []string
	for _, t := range tools {
		env = mergeEnv(env, t.Env)
		pathDirs = append(pathDirs, t.Path...)
	}
	if len(pathDirs) > 0 {
		env = mergeEnv(env, []string{"PATH=" + strings.Join(append(pathDirs, os.Getenv("PATH")), string(os.PathListSeparator))})
	}

	custom := make([]string, 0, len(svc.Env))
	for k, v := range svc.Env {
		custom = append(custom, k+"="+v)
	}
	// map 遍历顺序随机，排序后再合并，保证每次启动环境一致、日志可比对。
	sort.Strings(custom)
	return mergeEnv(env, custom), nil
}

// mergeEnv 把 overrides 合并进 base，同名键以 overrides 为准。
func mergeEnv(base, overrides []string) []string {
	idx := make(map[string]int, len(base))
	for i, kv := range base {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			idx[kv[:eq]] = i
		}
	}
	out := base
	for _, kv := range overrides {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if i, ok := idx[key]; ok {
			out[i] = kv
			continue
		}
		idx[key] = len(out)
		out = append(out, kv)
	}
	return out
}

// resolveExe 把命令名解析成绝对路径。
//
// 必须自己解析，不能依赖 exec.Command 的查找：exec.Command 用的是 Pier 进程自身的
// PATH，而不是我们注入给子进程的那份。若 Pier 从 Finder 等极简环境启动，
// 即使注入的 PATH 里mvn 可用，exec.Command 依然找不到它。
func (s *Supervisor) resolveExe(name string, svc *config.Service, plan *config.Plan) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil // 已经是路径，无需查找
	}
	tools, _, _ := s.Tools(svc)
	// 命令名正好对应某套工具链时，直接用选中的那个：mvn 可能被换成了项目的 mvnw，
	// python3 可能是虚拟环境里的 python，都不能再去 PATH 上找同名的。
	for _, t := range tools {
		if cmdKinds[name] == t.Kind {
			return t.Bin, nil
		}
	}
	// 否则（npm、npx、yarn、java 等）在所选工具链的目录里找，再退回 PATH。
	var dirs []string
	for _, t := range tools {
		dirs = append(dirs, t.Path...)
	}
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	if cand := execpath.First(dirs, name); cand != "" {
		return cand, nil
	}
	return "", fmt.Errorf("找不到可执行文件 %s，请确认已安装，或在「SDK 管理」里添加", name)
}

// cmdKinds 是「命令名 → 它就是哪套工具链」。
var cmdKinds = map[string]toolchain.Kind{
	"go": toolchain.Go, "mvn": toolchain.Maven, "node": toolchain.Node, "pnpm": toolchain.Pnpm,
	"python3": toolchain.Python, "python": toolchain.Python,
}

// runSync 同步执行编译等前置步骤，输出直接进日志，便于失败时排查。
//
// 编译进程放进自己的进程组，取消时向整组发信号：mvn 是个 shell 脚本，真正干活的是它
// 派生的 java；只杀脚本本身的话，java 会变成孤儿继续编译、继续占着 CPU 和目标目录。
// 先 SIGTERM，5 秒还没退就 SIGKILL。
func runSync(ctx context.Context, dir string, argv []string, env []string, logFile *os.File) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setSyncGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		killSyncTree(cmd.Process.Pid)
	}
	return err
}

// displayCmd 把 argv 还原成可读命令，交给 shell 的那种只展示真正的命令体。
func displayCmd(argv []string) string {
	return config.DisplayArgv(argv)
}

// ── 进程名：让服务在系统里叫它自己配置的那个名字 ─────────────────────────────
//
// macOS 上有两把互相独立的旋钮：
//
//   - argv[0]：ps 的 COMMAND 列、pgrep、pkill -x、killall 认它；
//   - 被 exec 的那个文件自己的文件名：活动监视器、top、ps -o ucomm 认它，
//     lsof 的 c 字段（界面上的「被 … 占用」）也是它。
//
// 第一把有个坎：exec 的是脚本时，中间的解释器会把 argv[0] 覆盖掉。pnpm 是
// `#!/usr/bin/env node`，env 用 argv[0]=node 再 exec node；mvn 是 `#!/bin/sh`，
// 脚本最后一句 `exec "$JAVACMD"` 又把进程换成 java。所以 node 脚本得自己展开成
// 「node + 脚本路径」，不能让 env 或内核经手。
//
// 第二把要求真的存在一个叫服务名的可执行文件。Go 天然满足（go build -o
// <BinDir>/<服务名>）；node 就地把同一份硬链过去——硬链是同一个 inode，
// 代码签名照旧有效；拷贝出来的副本签名与文件对不上，Apple Silicon 上会被系统
// 在 exec 时直接杀掉（实测退出码 137），所以只能硬链，不能拷。
//
// 硬链只对 node 做，因为「挪个位置照跑」是特例而不是常态（都实测过，别再试）：
//
//   - java：挪出 JDK 就死在 dyld 阶段（libjli 的 rpath 是 @executable_path/../lib），
//     只有放进 JDK 自己的 bin 里才加载得起来，那等于往用户的 SDK 目录里写文件。
//   - python：CPython 靠可执行文件旁边的 pyvenv.cfg 认 venv，硬链之后
//     sys.prefix 退回 base_prefix，venv 里的依赖一个都 import 不到。
//   - go：硬链之后 `go` 找不到 GOROOT（"binary is trimmed and GOROOT is not set"）。
//   - shell：/bin/sh 在只读的系统卷上，根本链不动。
//
// 其余类型（java 的 mvn、python、自定义 run）一律只设 argv[0]：ps / pgrep /
// killall 那一列照样是服务名，只是活动监视器里还是解释器自己的名字。
// 不为一颗旋钮去冒「换个位置就起不来」的风险——服务起得来才是底线。
//
// 名字长度不必迁就：实测 24 字符的英文名与中文名在 ps -o comm、pgrep -x、
// killall 那里都能整名命中（MAXCOMLEN 在现代 macOS 上不挡这里）。
// 识别一个进程是不是 Pier 起的，从头到尾只看 PID/PGID（见 ManagedName），
// 与它叫什么名字无关。

// namedArgv 返回把 argv[0] 换成服务名的那份 argv——ps、pgrep、killall 认的就是它。
//
// sh -c 要补一个 $0：-c 后面没有操作数时 $0 就是 argv[0]，换了名字，脚本里引用的
// $0 会从 /bin/sh 变成服务名。显式补一个，行为与改名之前一模一样。
func namedArgv(argv []string, name string) []string {
	if !namingByArgv0 || len(argv) == 0 {
		return argv
	}
	out := append([]string{}, argv...)
	if _, ok := config.ShellScript(out); ok {
		out = append(out, out[0])
	}
	out[0] = name
	return out
}

// resolveRunExec 把运行命令改成「叫服务名的那份」，只动执行用的这份 argv。
//
// 只有 node 会被改写，两种情况：
//
//   - node 脚本（pnpm / npm / yarn 都是 `#!/usr/bin/env node`）：展开成
//     「node 路径 + 脚本路径」，绕开会用 argv[0]=node 再 exec 一次的那个 env，
//     再把 node 硬链成 <BinDir>/<服务名>，两把旋钮一起生效。
//   - 直接 exec node（自定义 run 写成 `node xxx`）：硬链那一把。
//
// 其余一律原样返回：Go 的产物在 plan.Run 里本来就叫服务名，java / python 挪了
// 位置就起不来（见上面那段注释）。改名是锦上添花，服务起得来才是底线。
func (s *Supervisor) resolveRunExec(argv []string, svc *config.Service, plan *config.Plan) []string {
	if !namingByArgv0 || len(argv) == 0 {
		return argv
	}
	// 读文件头、硬链都是 Pier 自己在做，相对路径要按服务目录解：run 里的
	// ./xxx 是相对服务目录的，拿 Pier 自己的 cwd 去猜只会猜错。
	exe := argv[0]
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(svc.AbsDir(), exe)
	}
	if interp, ok := shebangInterp(exe); ok {
		if !isNode(interp) {
			return argv
		}
		// 解释器在已解析的工具链里找，理由同 resolveExe：界面从访达启动时，
		// Pier 自己的 PATH 上什么都没有。找不到就不展开——此时服务本来也起不来，
		// 那不是改名该管的事。
		resolved, err := s.resolveExe(interp, svc, plan)
		if err != nil {
			return argv
		}
		// argv[1] 仍写用户原来那个路径：相对路径交给 cmd.Dir（服务目录）去解，
		// 与改名之前一模一样。
		if link := s.linkAsService(resolved, svc.Name); link != "" {
			resolved = link
		}
		return append([]string{resolved, argv[0]}, argv[1:]...)
	}
	if !isNode(exe) {
		return argv
	}
	if link := s.linkAsService(exe, svc.Name); link != "" {
		argv = append([]string{link}, argv[1:]...)
	}
	return argv
}

// isNode 判断这个名字是不是 node 本身。它是唯一实测「挪个位置照跑」的运行时，
// 所以也是唯一敢硬链的——为什么别的都不行，见文件上面那段。
func isNode(name string) bool {
	return cmdKinds[filepath.Base(name)] == toolchain.Node
}

// shebangInterp 读出脚本头里的解释器名：`#!/usr/bin/env node` 与
// `#!/usr/local/bin/node` 都得到 node；不是脚本则返回 false。
//
// 只读文件头 128 字节：可执行文件动辄几十兆，为了认出它是不是脚本不该整个读进来。
func shebangInterp(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := io.ReadFull(f, buf) // 短文件会返回 ErrUnexpectedEOF，读到的字节仍然有效
	head := buf[:n]
	if !bytes.HasPrefix(head, []byte("#!")) {
		return "", false
	}
	line := head[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return "", false
	}
	// `#!/usr/bin/env node`：解释器是 env 后面那个词，env 自己的选项不算
	// （`env -S node --flag` 这种也认）。
	if filepath.Base(fields[0]) == "env" {
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				return filepath.Base(f), true
			}
		}
		return "", false
	}
	return filepath.Base(fields[0]), true
}

// linkAsService 把 exe 硬链成 <BinDir>/<服务名>，返回链接路径；做不到则返回空串。
//
// 链接落在 cache/bin —— Pier 自己的目录，不动用户的任何文件。这个目录同时也是
// Go 服务的编译产物目录，但两者不会撞名：一个服务只有一个类型，而 Go 的那份
// 本来就叫 <服务名>。
func (s *Supervisor) linkAsService(exe, name string) string {
	dir := s.cfg.BinDir()
	link := filepath.Join(dir, name)
	if filepath.Dir(exe) == dir && filepath.Base(exe) == name {
		return link // 已经叫这个名字了：Go 的编译产物就是
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	// 先看目标位置上已经有什么。这一段必须在动手之前问完：源文件存不存在
	// 会变，而「不许吃掉别人的文件」这条不取决于源文件。
	if fb, err := os.Stat(link); err == nil {
		if fa, err := os.Stat(exe); err == nil && os.SameFile(fa, fb) {
			return link // 已经是同一份硬链（运行时版本没变），不必重链
		}
		// 只有确实是硬链出来的（nlink > 1）才敢替换。nlink 为 1 说明那是别人的
		// 正经文件：Go 服务的编译产物就摆在这个路径上，删掉它服务就起不来了。
		if n, ok := hardlinkCount(link, fb); !ok || n < 2 {
			return ""
		}
	}
	// 指到别的 inode 说明运行时换过版本，重新链一份。
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		return ""
	}
	if err := os.Link(exe, link); err != nil {
		return "" // 系统卷只读、跨卷硬链不允许——静默退回，进程名只少一把旋钮
	}
	return link
}
