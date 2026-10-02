//go:build !windows

// 这一份用的是 lsof 与 Setsid，Windows 上端口归属改问系统、进程也不再分会话。

package proc

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// listenOn 在指定端口上开一个监听，返回端口号与关闭函数。
// 用端口 0 让系统随便分配，避免测试之间抢同一个端口。
func listenOn(t *testing.T, port int) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

func TestPortOwnerOfFindsListener(t *testing.T) {
	// 这里刻意用生产代码的解析器，而不是 exec.LookPath。
	// LookPath 只看 PATH，而 lsof 住在 /usr/sbin —— 测试进程的 PATH 里未必有它，
	// 于是这条用例会在 lsof 明明存在的情况下报「本机没有 lsof」跳过，
	// 把一个真实的回归伪装成环境问题。用 sysBin 才和运行时看到的是同一件事。
	if _, err := sysBin("lsof"); err != nil {
		t.Skipf("本机确实找不到 lsof，端口占用查询这条功能整体不可用：%v", err)
	}
	port, closeLn := listenOn(t, 0)
	defer closeLn()

	owner, err := PortOwnerOf(port)
	if err != nil {
		t.Fatalf("查占用进程失败：%v", err)
	}
	if owner.PID != os.Getpid() {
		t.Errorf("PID = %d，期望 %d（监听就是本测试进程开的）", owner.PID, os.Getpid())
	}
	if owner.User == "" {
		t.Error("User 为空，界面上就没法判断这是不是自己的进程")
	}
	if owner.Command == "" {
		t.Error("Command 为空，「进程名」那一栏就是「—」，用户认不出这是什么程序")
	}
	if owner.Started == "" {
		t.Error("Started 为空，防 PID 复用的核对就无从谈起")
	}
	if owner.Args == "" {
		t.Error("Args 为空，用户无法辨认这到底是哪个服务")
	}
}

func TestPortOwnerOfNoListener(t *testing.T) {
	// 先拿一个端口再立刻释放，这个端口上就没有监听进程。
	port, closeLn := listenOn(t, 0)
	closeLn()

	if _, err := PortOwnerOf(port); !errors.Is(err, ErrNoOwner) {
		t.Errorf("空端口应当返回 ErrNoOwner，实际是 %v", err)
	}
}

// 结束进程的三个硬护栏：任何一个失效都可能误伤系统或用户自己的会话。
func TestKillExternalGuards(t *testing.T) {
	if err := KillExternal(1, ""); !errors.Is(err, ErrKillInit) {
		t.Errorf("PID 1 应当被拒绝，实际是 %v", err)
	}
	if err := KillExternal(os.Getpid(), ""); !errors.Is(err, ErrKillSelf) {
		t.Errorf("结束自身应当被拒绝，实际是 %v", err)
	}
}

func TestKillExternalRefusesStalePID(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起测试进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	// 启动时间对不上，说明用户看到的已经不是这个进程了，必须放弃。
	if err := KillExternal(pid, "Thu Jan  1 00:00:00 1970"); !errors.Is(err, ErrStalePID) {
		t.Errorf("启动时间不符应当返回 ErrStalePID，实际是 %v", err)
	}
	if !ProcessAlive(pid) {
		t.Fatal("核对失败时不该真的把进程杀掉")
	}
}

// 与 Pier 同组的进程一律不动：那可能是当前终端会话的一部分。
func TestKillExternalRefusesSameGroup(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	// 不设 Setsid，子进程默认留在本进程组里。
	if err := cmd.Start(); err != nil {
		t.Fatalf("起测试进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	if err := KillExternal(pid, ""); !errors.Is(err, ErrSameGroup) {
		t.Errorf("同进程组应当被拒绝，实际是 %v", err)
	}
	if !ProcessAlive(pid) {
		t.Fatal("被拒绝的请求不该真的把进程杀掉")
	}
}

// 正常路径：独立会话的进程，带上正确的启动时间，应当被结束。
func TestKillExternalTerminatesProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起测试进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	started, _, err := psInfo(pid)
	if err != nil {
		t.Fatalf("读启动时间失败：%v", err)
	}
	if err := KillExternal(pid, started); err != nil {
		t.Fatalf("结束进程失败：%v", err)
	}
	if ProcessAlive(pid) {
		t.Error("返回成功但进程仍在")
	}
}

// FreePort 必须避开清单里已写掉的端口——那些端口此刻可能并没有在监听，
// 只按系统当前状态挑，会挑出一个一启动就撞车的端口。
func TestFreePortSkipsUsedAndListening(t *testing.T) {
	port, closeLn := listenOn(t, 0)
	defer closeLn()

	got := FreePort(port, map[int]bool{port + 1: true, port + 2: true})
	if got == port {
		t.Errorf("挑中了正在监听的端口 %d", port)
	}
	if got == port+1 || got == port+2 {
		t.Errorf("挑中了清单里已占用的端口 %d", got)
	}
	if got != port+3 {
		t.Errorf("期望顺延到 %d，实际 %d", port+3, got)
	}
}

func TestWaitPortReleased(t *testing.T) {
	port, closeLn := listenOn(t, 0)
	go func() {
		time.Sleep(200 * time.Millisecond)
		closeLn()
	}()
	if !WaitPortReleased(port, 5*time.Second) {
		t.Error("端口已释放但没等到")
	}
}
