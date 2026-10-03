package proc

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// PortOwner 描述一个正占着端口的进程。
//
// 这是给「端口被谁占了」这个问题一个能落地的答案：光说「被 Pier 之外的进程占用」
// 等于没说，用户既没法判断是不是自己刚才在别的终端起的，也没法把它清掉。
type PortOwner struct {
	PID     int    `json:"pid"`
	UID     int    `json:"uid"`
	User    string `json:"user"`
	Command string `json:"command"` // 可执行文件名，如 node、java
	Args    string `json:"args"`    // 完整命令行，用于辨认到底是哪个服务
	Started string `json:"started"` // 进程启动时间原文，用于核对身份
	// Managed 为真表示这个 PID 正是 Pier 自己启动的某个服务。
	// 这种情况不该「结束进程」，而该走正常的「停止」，否则状态文件会留下一条假记录。
	Managed bool   `json:"managed"`
	Service string `json:"service"` // Managed 为真时对应的服务名
	// Origin 是沿父进程链查出来的「谁把它拉起来的」，认不出来时为 nil。
	//
	// 有它才谈得上判断该不该动手：同一个端口上蹲着的可能是刚在编辑器里起的服务，
	// 也可能是上一轮调试忘了关的终端，还可能是 Pier 面板自己拉起来的那份——
	// 三者的下一步动作完全不同，而命令行与进程名往往一模一样（都是 node、都是 java）。
	Origin *Origin `json:"origin,omitempty"`
}

// ErrNoOwner 表示端口上查不到监听进程（可能刚释放，或监听表读不到）。
var ErrNoOwner = errors.New("查不到占用该端口的进程")

// 结束他人进程时的护栏错误。这些情况下必须拒绝，且理由要能原样展示给用户。
var (
	ErrKillSelf    = errors.New("这是 Pier 自己的进程，不能结束")
	ErrKillInit    = errors.New("PID 1 是系统进程，不能结束")
	ErrNotYours    = errors.New("该进程属于其他用户，Pier 不会去动它")
	ErrSameGroup   = errors.New("该进程与 Pier 在同一个进程组里，结束它可能连带影响当前会话")
	ErrStalePID    = errors.New("进程信息已变化，该 PID 可能已被系统复用，已放弃这次操作")
	ErrKillManaged = errors.New("这是 Pier 启动的服务，请用「停止」而不是结束进程")
)

// KillExternal 结束一个不属于 Pier 的进程，用于清理占着端口的其它程序。
//
// expectStarted 是用户看到详情时那个进程的启动时间，必须原样回传。
// 这不是形式主义：从「看到详情」到「点下确认」之间隔的是人的反应时间，
// 这期间 PID 完全可能被系统回收再分配。只按 PID 下手，杀掉的可能是
// 一个刚巧拿到同一个号的无辜进程。核对启动时间才能确认「还是那一个」。
//
// 具体怎么发终止信号由平台层决定（见 sys_unix.go / sys_windows.go）：
// 「先礼后兵」这一段是共通的，礼和兵是什么，各平台自己说。
func KillExternal(pid int, expectStarted string) error {
	if pid <= 0 {
		return fmt.Errorf("无效的 PID：%d", pid)
	}
	if pid == 1 {
		return ErrKillInit
	}
	if pid == os.Getpid() {
		return ErrKillSelf
	}
	// 与 Pier 同组意味着可能是当前终端会话的一部分，结束它有可能把
	// 当前会话一起带走，一律拒绝。
	if inOurGroup(pid) {
		return ErrSameGroup
	}

	started, _, err := processTimes(pid)
	if err != nil {
		return fmt.Errorf("进程 %d 已不存在", pid)
	}
	if expectStarted != "" && started != expectStarted {
		return ErrStalePID
	}

	// 属主核对放在最后、动手之前：只有当前用户自己的进程才允许结束。
	if err := ownerErr(pid); err != nil {
		return err
	}

	if err := terminateGraceful(pid); err != nil {
		return fmt.Errorf("发送终止信号失败：%w", err)
	}
	// 先礼后兵：给它 5 秒自己收拾，多数开发服务器收到终止请求会正常退出。
	if WaitGone(pid, stopGrace) {
		return nil
	}
	if err := terminateForce(pid); err != nil {
		// 可能是刚好在这 5 秒里自己退了，这不算失败。
		if !ProcessAlive(pid) {
			return nil
		}
		return fmt.Errorf("强制结束进程 %d 失败：%w", pid, err)
	}
	if !WaitGone(pid, stopGrace) {
		return fmt.Errorf("进程 %d 在强制结束后仍未退出，请手工确认", pid)
	}
	return nil
}

// FreePort 从 from 开始找一个没被监听的端口，供新增应用时给个可用建议。
// used 是清单里已经写掉的端口，必须一并避开：两个服务配同一个端口，
// 单看系统当前状态是看不出来的（都还没启动）。
func FreePort(from int, used map[int]bool) int {
	if from < 1024 {
		from = 1024
	}
	listening := ListeningPorts()
	for p := from; p <= 65535; p++ {
		if used[p] {
			continue
		}
		if listening != nil && listening[p] {
			continue
		}
		if listening == nil && PortOpen(p) {
			continue
		}
		return p
	}
	return 0
}

// WorkDirs 查出这些进程各自的工作目录，拿不到的那些不在返回值里。
//
// 这是「这个端口背后是哪个项目」唯一靠谱的线索：命令行认不出来（node 起的
// 到处都是），端口号也认不出来（每个人给 dev server 配的都不一样），
// 而进程站在哪个目录里几乎是确定的。
//
// 一次问一批：一屏几十个监听进程，逐个问就是逐个 exec（见各平台的 cwdOf）。
func WorkDirs(pids []int) map[int]string { return cwdOf(pids) }

// WaitPortReleased 等待端口不再被监听，用于结束进程后确认端口真的空出来了。
func WaitPortReleased(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !PortOpen(port) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}
