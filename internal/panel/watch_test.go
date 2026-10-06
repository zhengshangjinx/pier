//go:build !windows

package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// watchYAML 里 alpha 配了监视，beta 没配。
const watchYAML = `
services:
  - name: alpha
    dir: a
    kind: go
    watch: true
  - name: beta
    dir: b
    kind: go
`

// watchHarness 铺一份带监视的清单与目录，并把状态文件写成给的那几条。
func watchHarness(t *testing.T, entries map[string]*proc.Entry) *harness {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(watchYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("建服务目录失败：%v", err)
		}
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	cfg := p.Config()
	if err := proc.UpdateState(cfg.StatePath(), func(s *proc.State) error {
		s.Services = entries
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
	return &harness{t: t, dir: dir, base: base, p: p}
}

// liveEntry 起一个自带会话的替身进程当「正在跑的服务」，用例收尾时收掉。
//
// 不能拿测试进程自己顶替：这里排上的是真的重启，worker 会照着记录里的进程组
// 去停那个「服务」——而那个组号正是跑测试的这条 shell 的，一次用例会把整条
// 命令行带走（表现是 go test 被信号打死，一行输出都没有）。Setsid 之后组号
// 等于首进程 PID，收尸也只收得动自己那一棵。
func liveEntry(t *testing.T) *proc.Entry {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起替身进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("取不到替身进程的组号：%v", err)
	}
	t.Cleanup(func() {
		// 已经被 worker 停掉的话这里是 ESRCH，不算错：那正是用例要的结果。
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Process.Release()
	})
	return &proc.Entry{PID: pid, PGID: pgid, StartedAt: time.Now()}
}

// watchPending 报告这个服务身上排着的那次重启，没有就是 nil。
func watchPending(h *harness) *operation {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	return h.p.ops["alpha"]
}

// find 取出清单里那个服务，供直接调用 checkOne。
func mustFind(t *testing.T, cfg *config.Config, name string) *config.Service {
	t.Helper()
	svc, err := cfg.Find(name)
	if err != nil {
		t.Fatalf("清单里没有 %s：%v", name, err)
	}
	return svc
}

// TestWatchRestartSkipsServicesNotRunning 钉着「没在跑的服务不因为文件变了就自己起来」。
//
// 用户没让它跑。改文件不等于「请把这个服务拉起来」——一个正在编辑器里读代码的人
// 随手改了行注释，界面上忽然多出一个跑着的服务，那是另一件事。
func TestWatchRestartSkipsServicesNotRunning(t *testing.T) {
	pid := deadPID(t)
	h := watchHarness(t, map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
	})
	p := h.p
	p.checkWatch(p.Config(), time.Now())
	if op := watchPending(h); op != nil {
		t.Errorf("记录里那个进程已经死了，却排上了 %s", op.kind)
	}
}

// TestWatchRestartSkipsUnknownServices 钉着「从来没有起来过的不动」。
func TestWatchRestartSkipsUnknownServices(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{})
	p := h.p
	p.checkWatch(p.Config(), time.Now())
	if op := watchPending(h); op != nil {
		t.Errorf("状态里没有记录，却排上了 %s", op.kind)
	}
}

// TestWatchRestartQueuesForRunning 钉着真在跑的服务会被重来一次。
func TestWatchRestartQueuesForRunning(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{"alpha": liveEntry(t)})
	p := h.p
	p.watchRestart(p.Config(), mustFind(t, p.Config(), "alpha"), []string{"main.go"})

	op := watchPending(h)
	if op == nil {
		t.Fatal("文件变了，正在跑的服务没有排上重启")
	}
	if op.kind != "restart" {
		t.Errorf("排上的是 %s，想要 restart", op.kind)
	}
}

// TestCheckOneWaitsForChangesToSettle 钉着合并那个窗口。
//
// 编辑器保存一个大文件不是一次写完的（先清空再写、或者写临时文件再改名）。
// 见到第一个字节就重启的话，起来的进程读到的是一份写了一半的源码——
// 而那种失败看起来像服务自己有问题。
func TestCheckOneWaitsForChangesToSettle(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{"alpha": liveEntry(t)})
	p := h.p
	cfg := p.Config()
	svc := mustFind(t, cfg, "alpha")
	file := filepath.Join(h.dir, "a", "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("写源文件失败：%v", err)
	}

	now := time.Now()
	// 第一趟只建立基线：刚配上的监视不该因为「现在开始看了」就重启一次。
	p.checkOne(cfg, svc, now)
	if op := watchPending(h); op != nil {
		t.Fatalf("建立基线的那一趟就动手了：%s", op.kind)
	}

	// 改一个字节数不一样的内容：mtime 的精度在三个平台上并不一样，
	// 只改内容不改长度的话，这一条在某些文件系统上会变成一条时灵时不灵的用例。
	if err := os.WriteFile(file, []byte("package main // 改了\n"), 0o644); err != nil {
		t.Fatalf("改写源文件失败：%v", err)
	}
	p.checkOne(cfg, svc, now.Add(time.Second))
	if op := watchPending(h); op != nil {
		t.Fatalf("刚看到变化就动手了：%s", op.kind)
	}

	// 还在静默窗口里：这一趟什么都不该做。
	p.checkOne(cfg, svc, now.Add(time.Second+watchSettle/2))
	if op := watchPending(h); op != nil {
		t.Fatalf("静默窗口里动手了：%s", op.kind)
	}

	// 变化落定：动手。
	p.checkOne(cfg, svc, now.Add(time.Second+watchSettle))
	if op := watchPending(h); op == nil {
		t.Error("变化落定之后没有排上重启")
	}
}

// TestCheckOneSkipsUnwatchedServices 钉着没配监视的服务连树都不建。
//
// 建了就会扫：一棵几万文件的树每半秒扫一遍，是白烧的 CPU。
func TestCheckOneSkipsUnwatchedServices(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{"beta": liveEntry(t)})
	p := h.p
	p.checkWatch(p.Config(), time.Now())
	if _, ok := p.watching["beta"]; ok {
		t.Error("没配 watch 的 beta 也被盯上了")
	}
	if len(p.watching) != 1 {
		t.Errorf("盯着 %d 个服务，想要 1 个（只有 alpha 配了）", len(p.watching))
	}
}

// TestCheckWatchForgetsRemovedServices 钉着清单换过之后旧的状态会丢掉。
//
// 留着不会出错（下一轮不会再看它），但它记着的那棵树可能已经不在了，
// 而这份 map 会一直跟着进程。
func TestCheckWatchForgetsRemovedServices(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{})
	p := h.p
	p.watching["已经不在了"] = &watchState{key: "x"}
	p.checkWatch(p.Config(), time.Now())
	if _, ok := p.watching["已经不在了"]; ok {
		t.Error("清单里没有的服务还留在监视表里")
	}
}

// TestNoteWatchWritesToTodaysLog 钉着那条「为什么重启了」留在日志里。
//
// 想重启的这一刻就写：等重启完再追记的话，那一行会落在新一次运行里，
// 看起来像是刚起来的那个进程自己说的。
func TestNoteWatchWritesToTodaysLog(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{"alpha": liveEntry(t)})
	p := h.p
	cfg := p.Config()
	p.noteWatch(cfg, mustFind(t, cfg, "alpha"), []string{"a.go", "b.go"})

	raw, err := os.ReadFile(cfg.LogPath("alpha"))
	if err != nil {
		t.Fatalf("读日志失败：%v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "文件有改动") {
		t.Errorf("日志里没有那句原因：%q", got)
	}
	for _, f := range []string{"a.go", "b.go"} {
		if !strings.Contains(got, f) {
			t.Errorf("日志里没有提到 %s：%q", f, got)
		}
	}
}

// TestSummarizeFiles 钉着那一句人话：几个就列几个，多了就报个数。
//
// 一次重命名目录会带上几十个文件，全列出来会把日志头淹掉。
func TestSummarizeFiles(t *testing.T) {
	if got := summarizeFiles([]string{"a.go"}); got != "a.go" {
		t.Errorf("单个文件 = %q", got)
	}
	if got := summarizeFiles([]string{"a.go", "b.go"}); got != "a.go、b.go" {
		t.Errorf("两个文件 = %q", got)
	}
	got := summarizeFiles([]string{"a.go", "b.go", "c.go", "d.go", "e.go"})
	if !strings.Contains(got, "5") {
		t.Errorf("五个以上要报出总数：%q", got)
	}
}

// TestWatchOutsideCoversPierDirs 钉着 Pier 自己那几棵树一定在排除名单里。
//
// 日志与编译产物每次启动都在写。目录选得近的时候它们就在被监视的树里，
// 不排掉的话，配了监视的服务会自己重启自己，一直转下去。
func TestWatchOutsideCoversPierDirs(t *testing.T) {
	h := watchHarness(t, map[string]*proc.Entry{})
	cfg := h.p.Config()

	out := watchOutside(cfg)
	for _, want := range []string{cfg.LogDir(), cfg.BinDir()} {
		found := false
		for _, got := range out {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 不在排除名单里：%v", want, out)
		}
	}
}
