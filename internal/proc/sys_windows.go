//go:build windows

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 平台层在 Windows 这一侧的实现，unix（macOS / Linux）那份在 sys_unix.go。
//
// 共享代码只认这里给出的符号，自己不碰任何平台 API。这一份要回答的是同一批问题，
// 但 Windows 上可用的东西几乎全不一样：
//
//	setsid / 进程组      → 没有。CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS 让服务
//	                       自成一路，收树时按父子链现场找回整棵
//	kill(-pgid, SIGTERM) → 没有信号。open process + TerminateProcess，一棵一棵来
//	Getpgid(pid)         → 没有进程组可查。「还是不是当初那个进程」改看创建时间
//	lsof                 → GetExtendedTcpTable，直接问系统，反而不需要外部命令
//	ps -axo …            → 进程快照 + GetProcessTimes + GetProcessMemoryInfo
//	flock                → LockFileEx
//
// 「PID 会被系统复用」这件事在 Windows 上尤其要紧：这里的 PID 复用比 unix 快得多，
// 而 TerminateProcess 一样是照着号下手。挡它的东西就是 Entry.Ident——进程创建时间。

// 信号常量。Windows 上没有信号，KillGroup 收到这两个值时做的是同一件事
// （TerminateProcess）；留着它们只是为了让共享代码不必分平台写字面量。
var (
	sigTerm = syscall.SIGTERM
	sigKill = syscall.SIGKILL
)

// stillActive 是 GetExitCodeProcess 对「还在跑」给出的那个值（STILL_ACTIVE）。
const stillActive = 259

// ── 进程存活、归属与信号 ───────────────────────────────────────────────────

// probeProcess 一次打开进程，同时拿到「还在跑吗」与「创建时间」两件事。
//
// 合成一次是有意的：创建时间就是这边认进程的那把凭据，先问活没活、再单独问一次
// 创建时间，中间那道缝里 PID 完全可能已经易主——那就正好在最需要它的地方失手。
//
// 打不开一律按「不在了」算：系统进程、受保护进程本来就打不开，而它们不会是
// Pier 起的服务，也轮不到 Pier 去结束。
func probeProcess(pid int) (alive bool, ident string) {
	if pid <= 0 {
		return false, ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false, ""
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return false, ""
	}
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return true, ""
	}
	return true, filetimeTicks(created)
}

// ProcessAlive 判断进程是否真的还在运行。
//
// 这里没有 unix 那份的回收副作用：Windows 上进程退出即消失，没有僵尸态，
// 也就没有「退出了却还占着 PID、kill 探测照样成功」那个坑。
func ProcessAlive(pid int) bool {
	alive, _ := probeProcess(pid)
	return alive
}

// SameGroup 判断 pid 是不是记录里那个进程组的首进程。
//
// Windows 上没有可查的进程组，Entry.PGID 写的就是首进程 PID，所以这里退化成比号。
// 这不是「够用就行」的凑合：它挡的是「PID 被复用后误杀」，而那个号本身就被复用了，
// 真正兜底的是 Entry.Ident（见 sameEntry）。**这一条不导出到包外**，
// 需要判断「某个 pid 是不是某条服务那棵树里的」走 inEntryGroup。
func SameGroup(pid, pgid int) bool {
	return pid > 0 && pid == pgid
}

// sameEntry 判断记录在案的那个进程是否还是当初那一个。
//
// 凭据是进程创建时间：PID 会被系统复用，创建时间不会。Ident 为空时退回只看
// 「还在不在」——正常写出来的记录不会没有它，走到那条路的只有手工改过的状态文件，
// 与其把好好的服务判成已死，不如按老口径认。
func sameEntry(e *Entry) bool {
	if e == nil || e.PID <= 0 {
		return false
	}
	alive, ident := probeProcess(e.PID)
	if !alive {
		return false
	}
	return e.Ident == "" || ident == e.Ident
}

// inEntryGroup 判断某个 pid 是否属于这条记录代表的那棵树。
//
// 端口常握在子进程手里（vite 的 node、spring-boot 的 JVM），光比 PID 认不出来。
// unix 按进程组一次问出来，这边只能顺着父子链往上爬——代价是每问一次要一份进程快照，
// 所以走带缓存的 processSnapshot。
func inEntryGroup(pid int, e *Entry) bool {
	if e == nil || pid <= 0 || !ProcessAlive(e.PID) {
		return false
	}
	if pid == e.PID {
		return true
	}
	return descendsFrom(pid, e.PID)
}

// inOurGroup 判断该进程是否在 Pier 自己这一路上。
//
// unix 挡的是「同进程组」——结束它可能把当前终端会话一起带走。Windows 没有进程组，
// 对应的东西是这条链：Pier 自己，以及它的祖先进程（终端、资源管理器）。
// 结束它们同样会波及当前会话。
func inOurGroup(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	rows, _ := processSnapshot()
	if rows == nil {
		return false
	}
	cur := os.Getpid()
	for hops := 0; hops <= len(rows); hops++ {
		r, ok := rows[cur]
		if !ok || r.ppid <= 0 || r.ppid == cur {
			return false
		}
		if r.ppid == pid {
			return true
		}
		cur = r.ppid
	}
	return false
}

// KillGroup 结束一棵服务树。
//
// unix 上向进程组发信号，内核把整棵收掉；Windows 上没有组，只能自己把树找出来
// 一个一个 TerminateProcess（也就是 taskkill /T 干的事，只是不另起一个进程）。
// sig 在这边没有区别：TerminateProcess 就是唯一的一档。
//
// 找树用的是**现取**的快照而不是缓存的那份：这一刻要杀的就得是这一刻还在的，
// 拿一份半秒前的名单去杀，中途新起的子进程会漏掉、已经退出的号可能已经易主。
func KillGroup(pgid, pid int, sig syscall.Signal) error {
	_ = pgid // Windows 上没有组号可用，收树一律按 pid 那棵算
	if pid <= 0 {
		return fmt.Errorf("无效的 PID：%d", pid)
	}
	targets := processTree(pid)
	if len(targets) == 0 {
		// 树没找着，可能它本来就已经退干净了。
		if !ProcessAlive(pid) {
			return nil
		}
		targets = []int{pid}
	}
	var firstErr error
	for _, t := range targets {
		if err := terminate(t); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// terminate 结束一个进程。已经退了的按成功算——收树的过程中，
// 前一个把父进程带走、后一个已经是空号，这是常态，不该报错。
func terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if !ProcessAlive(pid) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		if !ProcessAlive(pid) {
			return nil
		}
		return err
	}
	return nil
}

// processTree 返回以 root 为根的那棵进程树，叶子在前、root 在最后。
//
// 先叶子后根：服务多半是「壳盯着子进程」的结构（pnpm 盯着 vite），
// 反过来先杀壳的话，子进程会先被系统过继给别人，从这一刻起它就再也不在
// 这棵树里了——而它恰恰是占着端口的那一个。
func processTree(root int) []int {
	rows, kids := readProcessSnapshot()
	if rows == nil {
		return nil
	}
	var out []int
	seen := map[int]bool{root: true}
	var walk func(int)
	walk = func(p int) {
		for _, k := range kids[p] {
			if seen[k] {
				continue
			}
			seen[k] = true
			walk(k)
			out = append(out, k)
		}
	}
	walk(root)
	if _, ok := rows[root]; !ok {
		return out
	}
	return append(out, root)
}

// ── 进程创建与取消 ─────────────────────────────────────────────────────────

// namingByArgv0 说明这个平台上「把 argv[0] 改成服务名」有没有用。
//
// Windows 上没用：任务管理器、tasklist、`ps -o ucomm` 那一列显示的是可执行文件
// 自己的名字（也是这里认领端口时比对的那个 c 字段），argv[0] 不参与。
// 想改名只能像 unix 那边硬链 node 一样，把可执行文件本身换个文件名——而那条路
// 只有 node 走得通，在 Windows 上又无从验证，所以这里干脆不做：
// 服务起得来才是底线，改名是锦上添花。
const namingByArgv0 = false

// SetDetached 让进程起在自己的会话里：
// 不跟着 Pier 的控制台走，关掉终端或 Pier 退出都不会把它带走。
//
// DETACHED_PROCESS 对应 unix 那边的 setsid——新进程不继承父进程的控制台，
// 于是不会有黑窗弹出来（服务的 stdout/stderr 已经重定向进日志文件）。
// CREATE_NEW_PROCESS_GROUP 让它自成一个进程组的根；Windows 上这个组号查不出来，
// 但收树时得有个明确的起点。**故意不建 Job Object**：那东西默认会在句柄关闭时
// 把整组带走，等于给这里加上一个 stop-on-quit，而 Pier 的约定是退出不带走在跑的服务。
func SetDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// newSession 返回这次启动记在 Entry.PGID 里的号。Windows 上没有进程组，
// 写的就是首进程 PID；「还是不是那一个」另由 Entry.Ident 核对。
func newSession(pid int) int {
	return pid
}

// processIdent 给出「这个进程还是当初记录的那一个」的凭据，随 Entry 落盘。
//
// 取的是进程创建时间。这是这边唯一挡得住 PID 复用的东西：号会被系统重新发出去，
// 而两个进程不可能有同一个创建时刻。取不到就留空，sameEntry 会退回只看存活。
func processIdent(pid int) string {
	_, ident := probeProcess(pid)
	return ident
}

// setSyncGroup 把同步执行的编译进程放进自己的一组，并让取消时整棵一起结束：
// mvn 是个 .cmd，真正干活的是它派生的 java；只杀壳的话，java 会变成孤儿
// 继续编译、继续占着 CPU 和目标目录。
//
// CREATE_NO_WINDOW 是给编译这一步用的：它和界面在同一次交互里，不该在屏幕上
// 闪出一个黑窗。
func setSyncGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	cmd.Cancel = func() error { return KillGroup(0, cmd.Process.Pid, sigTerm) }
}

// killSyncTree 在取消之后补一记强收，收掉整组。
// Windows 上没有「一记 KILL」这种更重的档，与 setSyncGroup 里的取消走同一条路。
func killSyncTree(pid int) {
	_ = KillGroup(0, pid, sigKill)
}

// ── 结束一个不属于 Pier 的进程 ─────────────────────────────────────────────

// errNotOurs 是这边「不能动它」的说法。
//
// 与 unix 的 ErrNotYours 措辞不同，因为原因不同：那边跨用户会失败，
// 这边同会话的进程都是当前用户的，真正挡下来的是「对方以管理员身份在跑、
// 而 Pier 没有」。说不出这一句，用户只会看到一个语焉不详的「拒绝访问」。
var errNotOurs = errors.New("该进程属于其他用户或需要管理员权限，Pier 不会去动它")

// processTimes 取进程的启动时间与可执行文件全路径，供用户核对身份。
//
// 命令行取不到：Windows 上要读别的进程的 PEB 才拿得到（Process Explorer 那种做法），
// 那需要 PROCESS_VM_READ，对受保护进程会失败、被安全软件盯上也是常事。
// 这里给全路径，够辨认是哪个程序了。
func processTimes(pid int) (started, args string, err error) {
	alive, ident := probeProcess(pid)
	if !alive {
		return "", "", fmt.Errorf("进程 %d 不存在", pid)
	}
	return identText(ident), processImagePath(pid), nil
}

// ownerErr 确认这个进程是 Pier 能结束的。
//
// 直接拿 PROCESS_TERMINATE 试一把，比查属主更贴近问题本身，而且顺带把
// 「提权才能动」这种情况一并答了。**只试不发信号**：这里只是确认，动手在调用方。
func ownerErr(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if !ProcessAlive(pid) {
			return fmt.Errorf("进程 %d 已不存在", pid)
		}
		return errNotOurs
	}
	windows.CloseHandle(h)
	return nil
}

// Windows 没有信号这一档。terminate 走的是 TerminateProcess，
// 没有比它更温和的「请你退出」可用，于是「先礼后兵」在这边合并成一步。
func terminateGraceful(pid int) error { return terminate(pid) }
func terminateForce(pid int) error    { return terminate(pid) }

// ── 系统命令 ───────────────────────────────────────────────────────────────

// PortToolsAvailable 报告端口占用与进程信息查询所依赖的系统命令是否齐备。
//
// Windows 上这两件事都直接问系统（GetExtendedTcpTable 与进程快照），不经过任何
// 外部命令，所以永远齐备。unix 那边缺 lsof 会退化成 connect 探测的那种情况，
// 这边不存在。
func PortToolsAvailable() error { return nil }

// ── 进程快照 ───────────────────────────────────────────────────────────────

// winProc 是快照里的一行。
type winProc struct {
	pid, ppid int
	exe       string // 可执行文件名（不含路径），如 node.exe
}

// snapTTL 是快照的存活时间。
//
// 缓存是为了「认领端口归属」这一条路：状态刷新会为每个服务问一次
// ManagedName，而那边要判「这个 pid 是不是某条服务那棵树里的」——一条链爬一次、
// 一次一份快照的话，服务一多就是平方级的开销。半秒足够新：界面两秒刷一次，
// 而它回答的是「这个占着端口的进程是不是自家服务」，不是毫秒级的竞态判定。
//
// **收树（KillGroup）与采样（sampleProcesses）不走缓存**，那两处要的就是此刻的真实名单。
const snapTTL = 500 * time.Millisecond

var (
	snapMu   sync.Mutex
	snapAt   time.Time
	snapRows map[int]winProc
	snapKids map[int][]int
)

// processSnapshot 取一份带缓存的进程快照。
func processSnapshot() (map[int]winProc, map[int][]int) {
	snapMu.Lock()
	defer snapMu.Unlock()
	if snapRows != nil && time.Since(snapAt) < snapTTL {
		return snapRows, snapKids
	}
	rows, kids := readProcessSnapshot()
	if rows == nil {
		// 读不到时退回上一份。过期的名单总好过没有：它只影响「认不认得出自家服务」，
		// 认错一次的后果是界面上多给一个「结束进程」按钮，而 KillExternal 那边
		// 还有属主与创建时间两道核对。
		return snapRows, snapKids
	}
	snapRows, snapKids, snapAt = rows, kids, time.Now()
	return rows, kids
}

// readProcessSnapshot 现取一份进程快照，不做任何缓存。
func readProcessSnapshot() (map[int]winProc, map[int][]int) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil
	}
	defer windows.CloseHandle(h)

	rows := map[int]winProc{}
	kids := map[int][]int{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(h, &e); err == nil; err = windows.Process32Next(h, &e) {
		pid, ppid := int(e.ProcessID), int(e.ParentProcessID)
		rows[pid] = winProc{pid: pid, ppid: ppid, exe: windows.UTF16ToString(e.ExeFile[:])}
		kids[ppid] = append(kids[ppid], pid)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, kids
}

// descendsFrom 顺着父子链往上爬，看 pid 是不是 root 的后代。
func descendsFrom(pid, root int) bool {
	rows, _ := processSnapshot()
	if rows == nil {
		return false
	}
	cur := pid
	for hops := 0; hops <= len(rows); hops++ {
		r, ok := rows[cur]
		if !ok || r.ppid <= 0 || r.ppid == cur {
			return false
		}
		if r.ppid == root {
			return true
		}
		cur = r.ppid
	}
	return false
}

// processImagePath 取可执行文件的全路径；取不到返回空串。
func processImagePath(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

// processUser 取进程的属主账户名，取不到返回空串。
//
// 走的是令牌而不是「当前用户是谁」：监听 445、135 这些端口的常常是系统服务，
// 属主根本不是登录的那个人，直接把当前用户名填上去是错的。
func processUser(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return ""
	}
	defer token.Close()

	buf := make([]byte, 256)
	var need uint32
	err = windows.GetTokenInformation(token, windows.TokenUser, &buf[0], uint32(len(buf)), &need)
	if err != nil && errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		buf = make([]byte, need)
		err = windows.GetTokenInformation(token, windows.TokenUser, &buf[0], uint32(len(buf)), &need)
	}
	if err != nil {
		return ""
	}
	sid := (*windows.SID)(unsafe.Pointer(&buf[0]))

	name := make([]uint16, 256)
	nameLen := uint32(len(name))
	domain := make([]uint16, 256)
	domainLen := uint32(len(domain))
	var use uint32
	if err := windows.LookupAccountSid(nil, sid, &name[0], &nameLen, &domain[0], &domainLen, &use); err != nil {
		return ""
	}
	if d := windows.UTF16ToString(domain[:domainLen]); d != "" {
		return d + `\` + windows.UTF16ToString(name[:nameLen])
	}
	return windows.UTF16ToString(name[:nameLen])
}

// ── 端口占用 ───────────────────────────────────────────────────────────────

// GetExtendedTcpTable 没有进 x/sys 的封装，这里直接用它的裸过程。
var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const (
	tcpTableOwnerPidListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	afInet                   = 2
	afInet6                  = 23
)

// mibTCPRowOwnerPid / mibTCP6RowOwnerPid 是系统那张表的行，字段顺序即内存布局。
//
// IPv6 那一份的空隙不用补：C 里的 UCHAR[16] 也是 1 字节对齐，
// Go 的 [16]byte 同样，两边算出来都是 56 字节。
type mibTCPRowOwnerPid struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPid  uint32
}

type mibTCP6RowOwnerPid struct {
	State         uint32
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	OwningPid     uint32
}

// extendedTCPTable 取一份监听表原始字节。af 是地址族。
func extendedTCPTable(af uint32) ([]byte, error) {
	var size uint32
	// 第一次问「要多大」：缓冲区给 0，系统回一个 ERROR_INSUFFICIENT_BUFFER 并把
	// 需要的字节数写进 size。这次调用的返回值不看——不管它回什么，size 才是要的。
	_, _, _ = procGetExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(af), tcpTableOwnerPidListener, 0)
	if size == 0 {
		return nil, fmt.Errorf("读取监听端口表失败")
	}
	buf := make([]byte, size)
	ret, _, _ := procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPidListener, 0)
	if ret != 0 {
		return nil, windows.Errno(ret)
	}
	return buf[:size], nil
}

// tcpListeners 列出本机所有 LISTEN 端口及其归属进程。
//
// 表里的端口是网络字节序，读出来得换一下；同一台机器上 IPv4 与 IPv6 是两张表，
// 都要问：只问一张的话，绑在 :: 上的服务会整片看不见——而 Vite 默认就是这种
// （见 ListeningPorts 的注释）。
func tcpListeners() map[int]Listener {
	out := map[int]Listener{}

	if buf, err := extendedTCPTable(afInet); err == nil {
		rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPid{}))
		for _, row := range tableRows(buf, rowSize) {
			r := (*mibTCPRowOwnerPid)(unsafe.Pointer(&buf[row]))
			port := ntohs(r.LocalPort)
			if _, dup := out[port]; !dup {
				out[port] = Listener{Port: port, PID: int(r.OwningPid)}
			}
		}
	}
	if buf, err := extendedTCPTable(afInet6); err == nil {
		rowSize := int(unsafe.Sizeof(mibTCP6RowOwnerPid{}))
		for _, row := range tableRows(buf, rowSize) {
			r := (*mibTCP6RowOwnerPid)(unsafe.Pointer(&buf[row]))
			port := ntohs(r.LocalPort)
			if _, dup := out[port]; !dup {
				out[port] = Listener{Port: port, PID: int(r.OwningPid)}
			}
		}
	}

	if len(out) == 0 {
		return nil
	}
	// 补上进程名。一次快照问全，别每个端口问一次。
	rows, _ := processSnapshot()
	for p, l := range out {
		if r, ok := rows[l.PID]; ok {
			l.Command, l.User = trimExe(r.exe), processUser(l.PID)
		}
		out[p] = l
	}
	return out
}

// tableRows 给出一张系统表的每一行在字节里的起始下标。头 4 字节是行数。
func tableRows(buf []byte, rowSize int) []int {
	if len(buf) < 4 || rowSize <= 0 {
		return nil
	}
	n := int(*(*uint32)(unsafe.Pointer(&buf[0])))
	var offs []int
	for i := 0; i < n; i++ {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		offs = append(offs, off)
	}
	return offs
}

// ntohs 把系统表里按网络字节序存的端口换回主机序。
func ntohs(v uint32) int {
	return int(v&0xff)<<8 | int(v>>8&0xff)
}

// trimExe 去掉 .exe 后缀。系统表里叫 node.exe，界面上说「被 node 占用」更顺眼，
// 也和 unix 那边 lsof 给出的名字（不带后缀）对得上。
func trimExe(s string) string {
	return strings.TrimSuffix(s, ".exe")
}

// ListeningInfo 一次性列出本机所有 LISTEN 端口及其归属进程。
// 返回 nil 表示查不到监听表，调用方会退回 connect 探测。
func ListeningInfo() map[int]Listener {
	return tcpListeners()
}

// PIDsOnPort 返回占用指定端口的进程号。
func PIDsOnPort(port int) []int {
	if port <= 0 {
		return nil
	}
	info := tcpListeners() // 同一个端口可能被多个进程用 SO_REUSEPORT 监听着
	var pids []int
	for p, l := range info {
		if p == port && l.PID > 0 {
			pids = append(pids, l.PID)
		}
	}
	return pids
}

// PortOwnerOf 查出正监听指定端口的进程。
func PortOwnerOf(port int) (*PortOwner, error) {
	if port <= 0 {
		return nil, ErrNoOwner
	}
	info := tcpListeners()
	l, ok := info[port]
	if !ok || l.PID <= 0 {
		return nil, ErrNoOwner
	}

	o := &PortOwner{PID: l.PID, Command: l.Command, User: l.User}
	// 启动时间是防 PID 复用的凭据：从「看到详情」到「点下确认」之间隔着人的反应时间，
	// 这期间 PID 完全可能被系统回收再分配。核对创建时间才能确认「还是那一个」。
	if started, args, err := processTimes(o.PID); err == nil {
		o.Started, o.Args = started, args
	}
	if org := ProcessOrigin(o.PID); org.OK() {
		o.Origin = &org
	}
	return o, nil
}

// cwdOf 在 Windows 上取不到进程的工作目录。
//
// 进程快照里只有 PID、父进程与可执行文件名，工作目录不在这份数据里；
// 要拿它得走 WMI 的 Win32_Process.Directory，那既慢又要依赖 WMI 服务可用。
// 宁可返回空——调用方会退回用清单里写着的位置，那是它本来就有的信息。
func cwdOf(pids []int) map[int]string { return nil }

// ── 资源的读法 ─────────────────────────────────────────────────────────────

// GetProcessMemoryInfo 也没有进 x/sys 的封装。它住在 psapi.dll 里，
// 但 Win7 起 kernel32 也导出了同一份（K32GetProcessMemoryInfo），先试 kernel32。
var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	psapi                     = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo  = kernel32.NewProc("K32GetProcessMemoryInfo")
	procGetProcessMemoryInfo2 = psapi.NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters 对应 PROCESS_MEMORY_COUNTERS。
// 两个 DWORD 之后是 SIZE_T，64 位下中间有 4 字节填充——Go 的对齐规则与 C 一致，
// 这里不用手写占位。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// workingSetBytes 取进程当前的常驻内存。取不到返回 0。
func workingSetBytes(h windows.Handle) int64 {
	var mc processMemoryCounters
	mc.cb = uint32(unsafe.Sizeof(mc))
	proc := procGetProcessMemoryInfo
	if err := proc.Find(); err != nil {
		proc = procGetProcessMemoryInfo2
		if err := proc.Find(); err != nil {
			return 0
		}
	}
	r, _, _ := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.cb))
	if r == 0 {
		return 0
	}
	return int64(mc.workingSetSize)
}

// cpuSample 是上一次采样里一个进程的累计 CPU 时间。
type cpuSample struct {
	at    time.Time
	ticks uint64 // 内核态 + 用户态，单位 100ns
}

var (
	cpuMu        sync.Mutex
	cpuLast      = map[int]cpuSample{}
	lastSampleAt time.Time
)

// sampleProcesses 采一次全机进程列表，交给共享的汇总逻辑去聚合。
//
// ps 的 %cpu 是「最近一小段时间」的均值，Windows 给的是自启动以来的累计 CPU 时间
// （GetProcessTimes），所以要自己求差：拿这次与上次的累计值之差，除以两次采样的
// 间隔，换算成单核百分比——一个核满载就是 100，多线程服务在多核上可以超过 100，
// 与 ps / top 同一个口径。
//
// 第一次采样没有上一次可比，全部报 0：界面两秒采一次，两秒后就是真数了。
// 反过来若在这里编一个数出来，那才是真正会误导人的地方。
//
// 打不开的进程（系统进程、受保护进程）照样要出现在结果里，只是用量记 0：
// 它们是别人进程的父节点，从表里摘掉会把那棵树的父子关系切断，
// 「这一组有多少进程」跟着就不对了。
func sampleProcesses() ([]procRow, error) {
	snap, _ := readProcessSnapshot()
	if snap == nil {
		return nil, fmt.Errorf("读取进程列表失败")
	}

	now := time.Now()
	cpuMu.Lock()
	prev, prevAt := cpuLast, lastSampleAt
	cpuMu.Unlock()

	cur := make(map[int]cpuSample, len(snap))
	rows := make([]procRow, 0, len(snap))
	for pid, p := range snap {
		ticks, rssBytes := processUsage(pid)
		cur[pid] = cpuSample{at: now, ticks: ticks}

		// 计时器（内核 + 用户态）的读取失败与「真的没跑」在这里是同一个值，
		// 都记 0——分不出来，也不该编。
		var cpu float64
		if before, ok := prev[pid]; ok {
			if wall := now.Sub(prevAt).Seconds(); wall > 0 {
				// FILE 时间是 100ns 一格，一格里 1e7 格是 1 秒。
				if dt := float64(ticks-before.ticks) / 1e7; dt > 0 {
					cpu = dt / wall * 100
				}
			}
		}
		rows = append(rows, procRow{
			pid: pid, ppid: p.ppid,
			cpu: cpu, rssKB: rssBytes / 1024,
		})
	}

	cpuMu.Lock()
	cpuLast, lastSampleAt = cur, now
	cpuMu.Unlock()

	if len(rows) == 0 {
		return nil, fmt.Errorf("进程列表里没有任何记录")
	}
	return rows, nil
}

// processUsage 取一个进程的累计 CPU 时间（100ns）与常驻内存（字节）。
func processUsage(pid int) (ticks uint64, rssBytes int64) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, 0
	}
	defer windows.CloseHandle(h)

	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err == nil {
		ticks = filetimeValue(kernel) + filetimeValue(user)
	}
	return ticks, workingSetBytes(h)
}

// groupKey 决定一行进程该并进哪一组。
//
// Windows 上没有进程组，改按父子链认：从这一行往上爬，遇到的第一个「服务首进程」
// 就是它那一组的号；一路爬到顶都没遇到，就归到最顶上那个祖先进这一组。
//
// 对调用方是同一件事：state.json 里记着 Entry.PGID（这边就是首进程 PID），
// 拿它到 Groups 里取，取到的正是这条服务那棵树的合计——和 unix 按进程组取一样。
// 不会有「服务组号与无关进程的组号撞车」：爬到顶才会退到祖先，而祖先若在
// exclude 里，上面那一步就已经返回它了。
func groupKey(r procRow, byPID map[int]procRow, exclude map[int]bool) int {
	top := r.pid
	cur := r.pid
	for hops := 0; hops <= len(byPID); hops++ {
		p, ok := byPID[cur]
		if !ok {
			break
		}
		top = cur
		if exclude[cur] {
			return cur
		}
		if p.ppid <= 0 || p.ppid == cur {
			break
		}
		cur = p.ppid
	}
	return top
}

// ── 状态文件锁 ─────────────────────────────────────────────────────────────

// lockFile 对状态锁文件加排它锁。
//
// 用 LockFileEx 而不是 unix 那边的 flock：语义最接近，跟着句柄走，进程崩了由系统释放。
// 锁一整个字节就够——这里要的是「同一时刻只有一个 Pier 在读改写」。
func lockFile(f *os.File) error {
	var ov windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ov)
}

// tryLockFile 加锁，但被占着时不等待，直接返回 false。
//
// FAIL_IMMEDIATELY 就是 LockFileEx 上的那一档：拿不到立刻返回
// ERROR_LOCK_VIOLATION，而不是阻塞到别人放开为止。
func tryLockFile(f *os.File) (bool, error) {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	var ov windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}

// ── 硬链 ───────────────────────────────────────────────────────────────────

// hardlinkCount 返回文件的硬链接数。
//
// 用来判断 cache/bin 下那个位置上摆的是不是我们自己链出来的：nlink > 1 才敢替换，
// nlink 为 1 说明那是别人的正经文件——Go 服务的编译产物就摆在这个路径上，
// 删掉它服务就起不来了。
//
// 这里比 unix 多要一个 path：Windows 的文件信息里没有链接数（Win32FileAttributeData
// 只有时间戳和大小），得拿句柄问 GetFileInformationByHandle，而句柄只能按路径开。
func hardlinkCount(path string, fi os.FileInfo) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, false
	}
	return uint64(info.NumberOfLinks), true
}

// ── 时间 ───────────────────────────────────────────────────────────────────

// filetimeValue 把 FILETIME 的两个半字拼成 100ns 计数。
//
// 不用 Filetime.Nanoseconds()：那个会把值换算成 Unix 纪元以来的纳秒，
// 溢出与否取决于纪元的选取，而这里只需要一个能比大小、能求差的单调计数。
func filetimeValue(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func filetimeTicks(ft windows.Filetime) string {
	v := filetimeValue(ft)
	if v == 0 {
		return ""
	}
	return strconv.FormatUint(v, 10)
}

// windowsEpochToUnix 是 1601-01-01 到 1970-01-01 之间的秒数。
// FILETIME 的起点是前者，Go 的 time.Unix 用的是后者。
const windowsEpochToUnix = 11644473600

// identText 把凭据（100ns 计数）变成人能读的本地时间。
// KillExternal 拿它原样回传核对，所以同一个进程两次算出来必须一模一样。
func identText(ident string) string {
	ticks, err := strconv.ParseUint(ident, 10, 64)
	if err != nil || ticks == 0 {
		return ""
	}
	sec := int64(ticks/1e7) - windowsEpochToUnix
	return time.Unix(sec, 0).Local().Format("2006-01-02 15:04:05")
}
