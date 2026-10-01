package proc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
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
	Started string `json:"started"` // ps 的 lstart 原文，用于核对身份
	// Managed 为真表示这个 PID 正是 Pier 自己启动的某个服务。
	// 这种情况不该「结束进程」，而该走正常的「停止」，否则状态文件会留下一条假记录。
	Managed bool   `json:"managed"`
	Service string `json:"service"` // Managed 为真时对应的服务名
}

// ErrNoOwner 表示端口上查不到监听进程（可能刚释放，或 lsof 不可用）。
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

// KillExternal 结束一个不属于 Pier 的进程，用于清理占着端口的其它程序。
//
// expectStarted 是用户看到详情时那个进程的启动时间，必须原样回传。
// 这不是形式主义：从「看到详情」到「点下确认」之间隔的是人的反应时间，
// 这期间 PID 完全可能被系统回收再分配。只按 PID 下手，杀掉的可能是
// 一个刚巧拿到同一个号的无辜进程。核对启动时间才能确认「还是那一个」。
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
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == syscall.Getpgrp() {
		return ErrSameGroup
	}

	started, _, err := psInfo(pid)
	if err != nil {
		return fmt.Errorf("进程 %d 已不存在", pid)
	}
	if expectStarted != "" && started != expectStarted {
		return ErrStalePID
	}

	// 属主核对放在最后、动手之前：只有当前用户自己的进程才允许结束。
	if err := checkOwner(pid); err != nil {
		return err
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("发送终止信号失败：%w", err)
	}
	// 先礼后兵：给它 5 秒自己收拾，多数开发服务器收到 TERM 会正常退出。
	if WaitGone(pid, stopGrace) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
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
