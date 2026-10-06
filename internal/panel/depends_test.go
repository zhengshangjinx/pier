//go:build !windows

// 这一份真的拉起替身进程（拿 sleep 当替身），替身就是 /bin/sh 那一套。
//
// 依赖要验的是**时序**，而时序只能拿真在跑的东西来验：探针什么时候通、进程
// 什么时候起来，两者之间的先后正是这个功能本身。与 portswap_test.go 同一套写法。

package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// depYAML 里 db 的探针是一道「文件在不在」的命令题。
//
// 用文件当就绪信号而不是端口：端口要在用例里真的打开一个 socket 才谈得上就绪，
// 而「什么时候算就绪」这件事由用例自己说了算才测得准——文件是我建的，
// 我建的这一刻才是那个瞬间。三条服务都用 sleep 当替身，不碰工作空间里任何东西。
//
// db 标了 manual：它不参与「全部启动」，于是 api 等的是一个这一趟压根不会被拉起来
// 的前置——这正是那句「等一个不会发生的事」的由来（见 Panel.depComing）。
const depYAML = `
services:
  - name: db
    dir: .
    kind: shell
    run: sleep 300
    manual: true
    health: "cmd: test -f %s"
  - name: api
    dir: .
    kind: shell
    run: sleep 300
    depends_on: [db:healthy]
  - name: plain
    dir: .
    kind: shell
    run: sleep 300
`

// depHarness 起一个面板，db 的就绪信号是一个还没建的文件。
func depHarness(t *testing.T) (*harness, string) {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(fmt.Sprintf(depYAML, marker)), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	h := &harness{t: t, dir: dir, base: base, p: p}
	// 用例怎么结束都要把这几个替身收掉：sleep 300 会一直活着，
	// 而收尾时那个还在等探针的协程也要被取消，否则 drain 会一直等下去。
	t.Cleanup(func() {
		for _, name := range []string{"db", "api", "plain"} {
			_, _ = h.p.Stop(name)
		}
	})
	return h, marker
}

// running 读出状态文件里这条服务的记录，没跑就是 nil。
func running(t *testing.T, h *harness, name string) *proc.Entry {
	t.Helper()
	state, err := proc.LoadState(h.p.Config().StatePath())
	if err != nil {
		t.Fatalf("读状态文件失败：%v", err)
	}
	e, ok := state.Services[name]
	if !ok {
		return nil
	}
	return e
}

// waitFor 轮询等一件事成立，超时就把当前的样子报出来。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等了 10 秒也没等到：%s", what)
}

// TestDepWaitHoldsStartUntilProbePasses 是这一条的主线：前置没就绪时它不起来，
// 就绪之后立刻起来，而且这次启动不带那句「没等到」。
func TestDepWaitHoldsStartUntilProbePasses(t *testing.T) {
	h, marker := depHarness(t)

	if _, err := h.p.Start("db"); err != nil {
		t.Fatalf("启动 db 失败：%v", err)
	}
	// db 自己也在等探针，这一步只是让它进入「正在被拉起来」——api 的等待
	// 要靠这一条才会真的等下去（见 Panel.depComing）。
	waitFor(t, "db 的替身起来", func() bool { return running(t, h, "db") != nil })

	if _, err := h.p.Start("api"); err != nil {
		t.Fatalf("启动 api 失败：%v", err)
	}
	// 前置没就绪期间，api 的进程压根不该被拉起来。要验的就是这一条：
	// 不是「状态显示成等待」就算数——它可能只是界面上那么写，进程早起来了。
	time.Sleep(600 * time.Millisecond)
	if e := running(t, h, "api"); e != nil {
		t.Fatalf("前置还没就绪，api 的进程已经起来了（pid %d）", e.PID)
	}
	if st := h.find(t, "api"); st.Op == "" {
		t.Errorf("等前置期间界面上应当写着正在忙什么，实际 Op 是空的")
	}

	// 建文件就是「db 就绪了」这一刻。
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "api 在 db 就绪之后起来", func() bool { return running(t, h, "api") != nil })

	// 等到了就不该留那句话：一句说错了的告警比没有更坏——用户会去找一个
	// 并不存在的问题，而真正出事的那个服务反倒没人看。
	if got := h.find(t, "api").DepNote; got != "" {
		t.Errorf("等到了却记着 %q", got)
	}

	drain(t, h.p)
	if got := h.find(t, "api").DepNote; got != "" {
		t.Errorf("收尾之后 DepNote = %q，想要空的", got)
	}
}

// TestDepMissedStillStarts 钉着「等不到照旧起，只记一句」。
//
// 这条是刻意选的：服务已经在跑，卡在那儿不动比「起了并说清楚」更难解释。
// 前置压根没在跑时也不该等满三分钟——等的是一个不会发生的事（见 depComing）。
func TestDepMissedStillStarts(t *testing.T) {
	h, _ := depHarness(t)

	start := time.Now()
	// 走「全部启动」而不是点名启动 api：点名会把前置一起排进队列（见 startOnPort），
	// 而 db 不参与全部启停，这一趟没人会去拉它。
	if _, err := h.p.StartAll(); err != nil {
		t.Fatalf("全部启动失败：%v", err)
	}
	drain(t, h.p)

	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("等了一个不会就绪的前置 %s——它压根没在跑，没什么可等的", d)
	}
	if running(t, h, "api") == nil {
		t.Fatal("没等到前置也应当照常起来")
	}
	st := h.find(t, "api")
	if st.OpErr != "" {
		t.Errorf("没等到前置不该记成失败：%q", st.OpErr)
	}
	want := "没有等到 db 就绪"
	if st.DepNote != want {
		t.Errorf("DepNote = %q，想要 %q", st.DepNote, want)
	}
}

// TestDepWaitDoesNotHoldTheQueue 钉着「总时长不被串行化」。
//
// 排队的是一个人（见 Panel.worker），等前置要是占着队列，一个声明了
// depends_on: [mysql:healthy] 的 Java 服务会把它后面每一个服务都压住——
// 而那条流水线本来只受编译速度限制。所以等的动作交给协程，队列照旧往下走。
//
// 这一条只能这么验：plain 排在一个正在等前置的服务后面，它必须在前置还没就绪的
// 时候就起来。队列一被占住，plain 就要等到最后那个文件的出现才动。
func TestDepWaitDoesNotHoldTheQueue(t *testing.T) {
	h, marker := depHarness(t)

	if _, err := h.p.Start("db"); err != nil {
		t.Fatalf("启动 db 失败：%v", err)
	}
	waitFor(t, "db 的替身起来", func() bool { return running(t, h, "db") != nil })

	if _, err := h.p.Start("api"); err != nil {
		t.Fatalf("启动 api 失败：%v", err)
	}
	// 紧跟着排进去。api 这会儿正卡在等 db 上，队列该当它不存在。
	if _, err := h.p.Start("plain"); err != nil {
		t.Fatalf("启动 plain 失败：%v", err)
	}

	waitFor(t, "plain 起来", func() bool { return running(t, h, "plain") != nil })
	// 关键的一刻：plain 已经起来了，而 db 还没就绪、api 还没起。
	// 队列要是被 api 的等待占着，这里等到的顺序正好反过来。
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("前置已经就绪了，这一条验的时序没成立")
	}
	if running(t, h, "api") != nil {
		t.Fatal("前置没就绪，api 已经起来了")
	}

	// 收尾：把前置放开，看 api 确实接着起来了——不然上面那些只能说明
	// 「它没起来」，说明不了「它在等」。
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "api 在 db 就绪之后起来", func() bool { return running(t, h, "api") != nil })
	drain(t, h.p)
}

// TestDepNoteClearsOnTheNextStart 钉着那句话只活到下一次启动。
//
// 前置后来起来了，用户又点了一次启动：这一次是真等到了，那句话必须消失。
// 「只在启动时写」的写法（见 setDepNote）就该给出这个结果，这里把它钉住。
func TestDepNoteClearsOnTheNextStart(t *testing.T) {
	h, marker := depHarness(t)

	// 第一次：db 没在跑，api 记一句。
	if _, err := h.p.StartAll(); err != nil {
		t.Fatalf("全部启动失败：%v", err)
	}
	drain(t, h.p)
	if got := h.find(t, "api").DepNote; !strings.Contains(got, "db") {
		t.Fatalf("第一次启动的 DepNote = %q，想要提到 db", got)
	}

	// 第二次：db 起来了，探针当场就通。
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.Start("db"); err != nil {
		t.Fatalf("启动 db 失败：%v", err)
	}
	drain(t, h.p)
	if _, err := h.p.Restart("api"); err != nil {
		t.Fatalf("重启 api 失败：%v", err)
	}
	drain(t, h.p)
	if got := h.find(t, "api").DepNote; got != "" {
		t.Errorf("这次等到了，DepNote 还留着 %q", got)
	}
}
