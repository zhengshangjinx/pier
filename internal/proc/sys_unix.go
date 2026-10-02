//go:build !windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/zhengshangjinx/pier/internal/execpath"
)

// 平台层在 unix（macOS / Linux）这一侧的实现，Windows 那份在 sys_windows.go。
//
// 共享代码（process.go / supervisor.go / state.go / metrics.go / portowner.go）
// 只认这里给出的符号，自己不碰任何 syscall：进程组、setsid、lsof、ps、flock
// 这些在 Windows 上要么没有、要么换了名字，把它们关进各自的文件，
// 改一个平台就碰不到另一个。
//
// 这一份里的函数绝大多数是从原 process.go / sysbin.go / metrics.go /
// portowner.go 原样搬过来的，只是换了个位置，行为一行没改。

// 信号常量。共享代码里不直接写 syscall.SIGTERM：Windows 上没有信号这个概念，
// KillGroup 收到这两个值时该怎么做由各平台自己解释。
var (
	sigTerm = syscall.SIGTERM
	sigKill = syscall.SIGKILL
)

// ── 进程存活、归属与信号 ───────────────────────────────────────────────────

// ProcessAlive 判断进程是否真的还在运行。
//
// 注意它带一个副作用：如果该进程是 Pier 的子进程且已经退出，这里会顺手把它回收掉。
// 这一步不能省——子进程退出后若没人 Wait，会以僵尸态继续占着 PID，
// 而 kill(pid, 0) 对僵尸依然返回成功。结果是「等服务退出」永远等不到，
// 停止一个服务要白白耗完宽限期再报一句「未能停止」。
// pier-gui 是常驻进程，它启动的服务正是它的子进程，这个坑一定会踩到。
//
// 不是自己子进程时 Wait4 返回 ECHILD，直接落到 kill 探测，语义不变。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	var ws syscall.WaitStatus
	if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err == nil && wpid == pid {
		return false // 刚刚回收掉的，就是它已经退出了
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// SameGroup 校验 pid 仍属于记录在案的进程组。
// 进程退出后 PID 可能被系统复用，仅凭 PID 发信号有误杀无关进程的风险，
// 因此动手前必须确认它还在原来的进程组里。
func SameGroup(pid, pgid int) bool {
	if pid <= 0 || pgid <= 0 {
		return false
	}
	g, err := syscall.Getpgid(pid)
	return err == nil && g == pgid
}

// KillGroup 向整个进程组发信号；进程组不可用时退化为只发给该进程。
// 服务实际是 mvn / pnpm / sh 这类会派生子进程的壳，只杀壳会留下孤儿占着端口。
func KillGroup(pgid, pid int, sig syscall.Signal) error {
	if pgid > 0 {
		if err := syscall.Kill(-pgid, sig); err == nil {
			return nil
		}
	}
	return syscall.Kill(pid, sig)
}

// sameEntry 判断记录在案的那个进程是否还是当初那一个。
//
// unix 的凭据就是进程组号：Setsid 之后整棵树的 PGID 等于首进程 PID，
// 而 PID 被系统复用时组号对不上。
func sameEntry(e *Entry) bool {
	return e != nil && ProcessAlive(e.PID) && SameGroup(e.PID, e.PGID)
}

// inEntryGroup 判断某个 pid 是否属于这条记录代表的那棵树。
// 端口常握在子进程手里，光比 PID 认不出来，得按组认（见 ManagedName）。
func inEntryGroup(pid int, e *Entry) bool {
	return e != nil && ProcessAlive(e.PID) && SameGroup(pid, e.PGID)
}

// ── 进程创建与取消 ─────────────────────────────────────────────────────────

// namingByArgv0 说明这个平台上「把 argv[0] 改成服务名」有没有用。
//
// unix 上有用：ps 的 COMMAND 列、pgrep、pkill -x、killall 认的都是它。
// Windows 上没用：那边进程显示成什么名字由被 exec 的那个文件自己决定，
// argv[0] 不参与（见 sys_windows.go），改了只是白改。
const namingByArgv0 = true

// setDetached 让服务起在自己的会话里（setsid）：
// 不再受当前终端牵制，关掉终端或 Pier 退出都不会把它带走。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// newSession 返回服务被 setsid 之后的进程组号，也就是首进程 PID。
// 换成别的平台时这个数由别的凭据替代，但「记录在案、事后按它认领」这件事不变。
func newSession(pid int) int {
	return pid
}

// processIdent 给出「这个进程还是当初记录的那一个」的辅助凭据，随 Entry 落盘。
//
// unix 上留空：进程组号（Entry.PGID）已经足够——PID 被系统复用时组号对不上，
// 而组号自己就是当初那个首进程的号，别人复用不到。多写一份等于多一个要维护的字段，
// 还会让 state.json 里多出一行 unix 上根本用不着的记录。
func processIdent(pid int) string {
	return ""
}

// setSyncGroup 把同步执行的编译进程放进自己的进程组，并让取消时整组收到 TERM：
// mvn 是个 shell 脚本，真正干活的是它派生的 java；只杀脚本本身的话，
// java 会变成孤儿继续编译、继续占着 CPU 和目标目录。
func setSyncGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// killSyncTree 在取消之后补一记 KILL，收掉整组。
func killSyncTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// ── 结束一个不属于 Pier 的进程 ─────────────────────────────────────────────

// inOurGroup 判断该进程是否与 Pier 同组。同组意味着可能是当前终端会话的一部分，
// 结束它有可能把当前会话一起带走，因此一律拒绝。
func inOurGroup(pid int) bool {
	pgid, err := syscall.Getpgid(pid)
	return err == nil && pgid == syscall.Getpgrp()
}

// processTimes 取进程的启动时间与完整命令行，供用户核对身份。
func processTimes(pid int) (started, args string, err error) {
	return psInfo(pid)
}

// ownerErr 确认进程属于当前用户。
func ownerErr(pid int) error {
	return checkOwner(pid)
}

func terminateGraceful(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func terminateForce(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// ── 系统命令 ───────────────────────────────────────────────────────────────
//
// 系统自带命令（lsof、ps）一律按绝对路径找，不依赖 PATH。
//
// 这一条是被「端口占用查不出来」逼出来的。原先这里写的是 exec.Command("lsof", ...)，
// 而 lsof 住在 /usr/sbin —— 这个目录在 PATH 里并不是理所当然的：
// Pier 打包成 .app 从访达启动时，继承的是 launchd 给的那份最小环境，
// PATH 里有什么全看系统版本和用户自己的 shell 配置，跟终端里跑完全是两回事。
//
// 失败的后果特别隐蔽。lsof 起不来 → err 非 nil → 上游把「查不到」当成
// 「端口上没人监听」，界面于是渲染出一排「—」，用户看着和「确实没人占用」
// 长得一模一样。这个工具的全部卖点就是「本身不需要什么环境」，
// 那它自己依赖的环境就得自己兜住，不能指望用户去配 PATH。
//
// ps 同理，它是「启动于」「命令行」两栏的来源，缺了同样是一片「—」。
var sysBinCand = map[string][]string{
	// /usr/sbin 在前：这是 macOS 上 lsof 的实际位置，先试它省掉一次 stat。
	"lsof": {"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof", "/usr/local/sbin/lsof"},
	"ps":   {"/bin/ps", "/usr/bin/ps", "/sbin/ps"},
}

// sysBin 返回系统命令的绝对路径：先按已知位置逐个试，都不在才回落到 PATH。
//
// 回落是留给非常规安装的，macOS 上正常走不到那里。
func sysBin(name string) (string, error) {
	for _, c := range sysBinCand[name] {
		if execpath.Is(c) {
			return c, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("找不到系统命令 %s（已试过 %v）", name, sysBinCand[name])
}

// sysOutput 执行系统命令并取其标准输出。
//
// 单独包一层是为了让调用点保持一行：这些命令的取法完全一致，
// 散在五处各写一遍容易漏改——而漏掉任何一处，那一处就退回成依赖 PATH 的老样子。
func sysOutput(name string, args ...string) ([]byte, error) {
	bin, err := sysBin(name)
	if err != nil {
		return nil, err
	}
	return exec.Command(bin, args...).Output()
}

// PortToolsAvailable 报告端口占用与进程信息查询所依赖的系统命令是否齐备。
//
// 导出是给调用方一个「先把话说明白」的机会。lsof 一旦缺失，端口占用查询会退化成
// connect 探测，而 connect 探测对绑在 IPv6 通配地址上的服务会漏判——实测本机的
// Vite 开发服务器就是这种，连不上，于是占着的端口被报成空闲（见 PortOpen 的注释）。
// 那种情况下界面照常渲染，只是一排「—」，用户没有任何线索知道是工具缺了。
func PortToolsAvailable() error {
	var missing []string
	for _, name := range []string{"lsof", "ps"} {
		if _, err := sysBin(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少系统命令 %s，端口占用与进程信息查询不可用", strings.Join(missing, "、"))
	}
	return nil
}

// ── 端口占用 ───────────────────────────────────────────────────────────────

// ListeningInfo 一次性列出本机所有 LISTEN 端口及其归属进程。
//
// 用 -F 字段模式而不是默认的表格：命令名里可能有空格，按列切会错位。
// -F 的输出是「字段字母 + 值」逐行排列，p 起一个新进程记录，
// 其后的 c / L 属于该进程，n 行给出该进程的一个监听地址。
//
// 状态刷新每两秒就会调一次，所以这里一次问全，调用方不必再为每个服务各跑一次 lsof。
// 返回 nil 表示 lsof 不可用（没装，或没有任何监听端口时它以非零码退出）。
func ListeningInfo() map[int]Listener {
	out, err := sysOutput("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcuLn")
	if err != nil {
		return nil
	}
	listeners := make(map[int]Listener)
	var cur Listener
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 1 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			cur = Listener{}
			if n, err := strconv.Atoi(val); err == nil {
				cur.PID = n
			}
		case 'c':
			cur.Command = val
		case 'L':
			cur.User = val
		case 'n':
			// 名字列形如 *:3106、127.0.0.1:20351、[::1]:8080、*:3106->*:5555。
			// 只取本地那一侧的端口：箭头右边是连过去的对端，不是监听端口。
			if i := strings.Index(val, "->"); i >= 0 {
				val = val[:i]
			}
			colon := strings.LastIndexByte(val, ':')
			if colon < 0 {
				continue
			}
			port, err := strconv.Atoi(val[colon+1:])
			if err != nil || port <= 0 || port > 65535 {
				continue
			}
			// 同一个端口被多个进程用 SO_REUSEPORT 监听时，保留先出现的那个：
			// 界面上一次只该对一个进程做决定。
			if _, dup := listeners[port]; !dup {
				l := cur
				l.Port = port
				listeners[port] = l
			}
		}
	}
	return listeners
}

// PIDsOnPort 返回占用指定端口的进程号，供状态提示「端口被谁占了」。
func PIDsOnPort(port int) []int {
	if port <= 0 {
		return nil
	}
	out, err := sysOutput("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t")
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// PortOwnerOf 查出正监听指定端口的进程。
//
// 用 lsof 的 -F 字段模式而不是默认的列表格：命令名和命令行里可能有空格，
// 按列切会在那些情况下错位，而 -F 每行一个「字段字母 + 值」，与内容无关。
func PortOwnerOf(port int) (*PortOwner, error) {
	if port <= 0 {
		return nil, ErrNoOwner
	}
	out, err := sysOutput("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-F", "pcuL")
	if err != nil {
		return nil, ErrNoOwner
	}

	o := &PortOwner{}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			// 只取第一个监听进程。SO_REUSEPORT 下可能有多个，但那种情况极罕见，
			// 而界面上一次只该对一个进程做决定。
			if o.PID == 0 {
				if n, err := strconv.Atoi(val); err == nil {
					o.PID = n
				}
			}
		case 'c':
			if o.Command == "" {
				o.Command = val
			}
		case 'u':
			if o.UID == 0 {
				if n, err := strconv.Atoi(val); err == nil {
					o.UID = n
				}
			}
		case 'L':
			if o.User == "" {
				o.User = val
			}
		}
	}
	if o.PID == 0 {
		return nil, ErrNoOwner
	}

	// lsof 不给启动时间，得再问一次 ps。启动时间是防 PID 复用的关键凭据：
	// 界面查到进程、用户看清内容、点了「结束」——这中间可能隔着几十秒，
	// 而 PID 恰恰可能在这段时间里被系统分配给了另一个毫不相干的进程。
	if started, args, err := psInfo(o.PID); err == nil {
		o.Started, o.Args = started, args
	}
	if o.User == "" {
		o.User = strconv.Itoa(o.UID)
	}
	return o, nil
}

// ── 资源的读法 ─────────────────────────────────────────────────────────────

// psInfo 取进程的启动时间与完整命令行。
// lstart 固定是 5 个词（星期 月 日 时刻 年），命令行是剩下的全部内容，
// 因此按词数切开，而不是按固定列宽——后者会被命令行里的空格带偏。
func psInfo(pid int) (started, args string, err error) {
	out, err := sysOutput("ps", "-p", strconv.Itoa(pid), "-o", "lstart=", "-o", "args=")
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", "", fmt.Errorf("进程 %d 不存在", pid)
	}

	rest := line
	for i := 0; i < 5; i++ {
		sp := strings.IndexAny(rest, " \t")
		if sp < 0 {
			return "", "", fmt.Errorf("无法解析 ps 输出：%q", line)
		}
		word := rest[:sp]
		if i == 4 {
			started = strings.TrimSpace(line[:len(line)-len(rest)+len(word)])
		}
		rest = strings.TrimLeft(rest[sp:], " \t")
	}
	return started, rest, nil
}

// checkOwner 确认进程属于当前用户。跨用户结束进程必然失败，与其让用户
// 看到一个语焉不详的「操作不允许」，不如在这里给出明确的原因。
func checkOwner(pid int) error {
	out, err := sysOutput("ps", "-p", strconv.Itoa(pid), "-o", "uid=")
	if err != nil {
		return fmt.Errorf("读取进程 %d 的属主失败：%w", pid, err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("无法解析进程 %d 的属主", pid)
	}
	if uid != os.Getuid() {
		return ErrNotYours
	}
	return nil
}

// procFields 是采样要的那几列。
//
// 每列都带 `=`：不带的话 ps 会自己加表头，而表头里有 %cpu 这样的名字，
// 解析时就得先判断「这行是不是表头」。带 `=` 直接就没有表头。
const procFields = "pid=,ppid=,pgid=,%cpu=,rss="

// sampleProcesses 采一次全机进程列表，交给共享的汇总逻辑去聚合。
//
// 一行都解析不出来说明不是「个别行异常」，而是 ps 的用法或输出格式变了。
// 这时候返回全零的采样比返回错误危险得多：界面上会是一片「0.0%」，
// 看着像「什么都不占」，与「读不到」完全不是一回事。
func sampleProcesses() ([]procRow, error) {
	out, err := sysOutput("ps", "-axo", procFields)
	if err != nil {
		return nil, fmt.Errorf("读取进程用量失败：%w", err)
	}
	rows := parseProcRows(out)
	if len(rows) == 0 {
		return nil, fmt.Errorf("ps 没有给出任何可解析的进程记录")
	}
	return rows, nil
}

// groupKey 决定一行进程该并进哪一组。
//
// unix 上就是它自己的进程组号：Setsid 之后整条服务树共用一个 PGID，
// 而那个号正是 state.json 里记着、启动时就写下的 Entry.PGID。
func groupKey(r procRow, _ map[int]procRow, _ map[int]bool) int {
	return r.pgid
}

// parseProcRows 解析 ps -axo procFields 的输出。
//
// 逐行按空白切，不按列宽切：命令行里有空格会带偏定宽解析，而这里取的又都是
// 最左边几列数字，按词切最稳。行数不对、数字解析不出来的一律跳过——少一行是
// 少一个进程的数，为它把整次采样作废没必要。
func parseProcRows(out []byte) []procRow {
	var rows []procRow
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		cpu, e4 := strconv.ParseFloat(f[3], 64)
		rss, e5 := strconv.ParseInt(f[4], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
			continue
		}
		rows = append(rows, procRow{pid: pid, ppid: ppid, pgid: pgid, cpu: cpu, rssKB: rss})
	}
	return rows
}

// ── 状态文件锁 ─────────────────────────────────────────────────────────────

// lockFile 对状态锁文件加排它锁。flock 跟着打开的文件描述符走，
// 进程崩了由内核释放，不会留下需要人工清理的残留。
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// ── 硬链 ───────────────────────────────────────────────────────────────────

// hardlinkCount 返回文件的硬链接数。
//
// 用来判断 cache/bin 下那个位置上摆的是不是我们自己链出来的：nlink > 1 才敢替换，
// nlink 为 1 说明那是别人的正经文件——Go 服务的编译产物就摆在这个路径上，
// 删掉它服务就起不来了。
//
// path 这一参数 unix 上用不着（stat 的结果里就带着链接数），是给 Windows 留的：
// 那边的文件信息里没有这一项，得拿着路径开句柄再问。
func hardlinkCount(path string, fi os.FileInfo) (uint64, bool) {
	_ = path
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}
