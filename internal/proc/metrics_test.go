package proc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseProcRows(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []procRow
	}{
		{
			"正常几行",
			"  1   0   1   0.0  12240\n332   1 332   1.5   9824\n",
			[]procRow{{1, 0, 1, 0, 12240}, {332, 1, 332, 1.5, 9824}},
		},
		// 进程名里有空格也不会影响这里：取的是最左边五列数字，按词切最稳。
		{
			"前后空白不影响",
			"  96845     1 96845   0.0   7504   \n",
			[]procRow{{96845, 1, 96845, 0, 7504}},
		},
		{
			"CPU 是小数",
			"100 1 100 98.8 204800\n",
			[]procRow{{100, 1, 100, 98.8, 204800}},
		},
		// 个别行异常就跳过它，不能让整次采样作废。
		{
			"列数不对的跳过",
			"1 0 1 0.0 100\n2 1\n3 1 3 0.0 300\n",
			[]procRow{{1, 0, 1, 0, 100}, {3, 1, 3, 0, 300}},
		},
		{
			"数字解析不出来的跳过",
			"1 0 1 0.0 100\nabc 1 2 0.0 200\n3 1 3 x 300\n",
			[]procRow{{1, 0, 1, 0, 100}},
		},
		{"空输入", "", nil},
		{"只有空行", "\n\n   \n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseProcRows([]byte(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("解析出 %d 行，想要 %d 行：%+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("第 %d 行 = %+v，想要 %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// startGroup 起一个自带会话的进程树，返回首进程 PID、进程组号和收尾函数。
//
// 用 /bin/sh 派生两个 sleep 而不是直接起一个 sleep，是为了复刻真实服务的形状：
// Pier 记录在案的 PID 是 mvn / pnpm 这类壳，真正吃资源的在它的子进程里。
// 只按 PID 采样会漏掉它们——那正是这个采集器要避免的错误。
func startGroup(t *testing.T) (pid, pgid int, stop func()) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起测试进程失败：%v", err)
	}
	pid, pgid = cmd.Process.Pid, cmd.Process.Pid
	stop = func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }

	// 等子进程真的派生出来再往下走。不等的后果是采样偶尔只看到 sh 自己，
	// 用例时灵时不灵——而那种失败最容易被当成「采集器有 bug」。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n := countGroupProcs(t, pgid); n >= 2 {
			return pid, pgid, stop
		}
		if !time.Now().Before(deadline) {
			stop()
			t.Fatalf("等不到进程组 %d 成形", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// countGroupProcs 直接问 ps 数一个进程组里有几个进程。
// 刻意不走 SampleMetrics：那是被测对象，用它来判定前置条件就成了自证。
func countGroupProcs(t *testing.T, pgid int) int {
	t.Helper()
	out, err := sysOutput("ps", "-g", strconv.Itoa(pgid), "-o", "pid=")
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range strings.Fields(string(out)) {
		if _, err := strconv.Atoi(f); err == nil {
			n++
		}
	}
	return n
}

// 一个服务整棵树的用量都要算进来，而不是只算记录在案的那个 PID。
func TestSampleMetricsSumsWholeProcessGroup(t *testing.T) {
	pid, pgid, stop := startGroup(t)
	defer stop()

	m, err := SampleMetrics(os.Getpid(), nil)
	if err != nil {
		t.Fatalf("采样失败：%v", err)
	}
	u, ok := m.Groups[pgid]
	if !ok {
		t.Fatalf("采样结果里没有进程组 %d（服务就是按进程组取数的）", pgid)
	}
	if want := countGroupProcs(t, pgid); u.Procs != want {
		t.Errorf("进程组 %d 采到 %d 个进程，ps 说有 %d 个", pgid, u.Procs, want)
	}
	if u.Procs < 2 {
		t.Errorf("只采到 %d 个进程；壳进程之外的子进程被漏掉了", u.Procs)
	}
	if u.MemBytes <= 0 {
		t.Error("内存合计为 0，说明 rss 那一列没被读进来")
	}
	// 单独核对一次：进程组号不等于首进程 PID 的话，按 PGID 取数就是错的。
	if g, err := syscall.Getpgid(pid); err != nil || g != pgid {
		t.Errorf("进程 %d 的进程组是 %d（err=%v），采样却按 %d 取数", pid, g, err, pgid)
	}
}

// 「面板自身」必须扣掉它启动的服务，否则这一项就答非所问。
//
// 这是这个采集器存在的全部理由：用户要分辨的是「面板在吃资源」还是「面板起的
// 程序在吃资源」。服务的父进程确实是 Pier，不摘掉的话两个数字会合成一个，
// 结论正好相反。
func TestSampleMetricsSelfExcludesServiceSubtree(t *testing.T) {
	pid, pgid, stop := startGroup(t)
	defer stop()

	all, err := SampleMetrics(os.Getpid(), nil)
	if err != nil {
		t.Fatalf("采样失败：%v", err)
	}
	kept, err := SampleMetrics(os.Getpid(), map[int]bool{pid: true})
	if err != nil {
		t.Fatalf("采样失败：%v", err)
	}

	// 节点数可能因为采集时那个 ps 子进程而有一两个的抖动，但它在两次采样里
	// 同样出现，相减就抵消了——差值应当正好是被摘掉的那棵子树的大小。
	want := all.Groups[pgid].Procs
	if want < 2 {
		t.Fatalf("进程组 %d 只有 %d 个进程，这条用例失去意义", pgid, want)
	}
	if got := all.Self.Procs - kept.Self.Procs; got != want {
		t.Errorf("摘掉服务后自身进程数少了 %d，应当正好少 %d 个", got, want)
	}
	// 摘除只影响「自身」：Groups 是给服务取数用的，必须原样还在。
	if kept.Groups[pgid].Procs != want {
		t.Errorf("摘除把 Groups 里的进程组 %d 也改掉了：%d → %d",
			pgid, want, kept.Groups[pgid].Procs)
	}
	if kept.Self.Procs < 1 {
		t.Error("自身进程数为 0：root 自己没被算进去")
	}
}
