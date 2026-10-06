//go:build !windows

// 这几条都要造一份「有进程正在跑」的记录，办法是借当前测试进程自己所在的进程组。
// 那是 unix 的认领口径（PID + PGID）；Windows 上没有进程组，改看进程创建时间，
// 等价的那几条测试要另写。

package proc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// withLiveEntry 给某个服务记一条「正在跑」的记录，进程号与组号都借当前测试进程。
func withLiveEntry(t *testing.T, cfg *config.Config, name string, port int) {
	t.Helper()
	self, pgid := os.Getpid(), syscall.Getpgrp()
	if err := UpdateState(cfg.StatePath(), func(st *State) error {
		st.Services[name] = &Entry{PID: self, PGID: pgid, Port: port, StartedAt: time.Now()}
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
}

// 一次调用要把四种情况分开报：在跑而探针通、在跑而探针不通、没在跑、没配探针。
//
// 合起来报「三个没就绪」是不行的：这四件事的下一步动作各不相同，而拿到结果的人
// 多半正要照着它决定下一步。
func TestWaitForClassifiesFourWays(t *testing.T) {
	pass := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer pass.Close()
	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer never.Close()

	cfg := waitFixture(t, map[string]string{
		"idle": pass.URL,
		"dead": never.URL,
		"live": pass.URL,
	})
	// live 与 dead 都在跑，只是一个探得通、一个探不通；idle 有探针却没在跑。
	withLiveEntry(t, cfg, "live", 0)
	withLiveEntry(t, cfg, "dead", 0)

	// 传进去的顺序乱着来：回来的顺序要跟传进去的一致，才轮到调用方按名字对位。
	out, err := WaitFor(cfg, []string{"dead", "noprobe", "live", "idle"}, 1500*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name  string
		ready bool
		why   string
	}{
		{"dead", false, WaitTimeout},
		{"noprobe", false, WaitNoProbe},
		{"live", true, ""},
		{"idle", false, WaitNotRunning},
	}
	if len(out) != len(want) {
		t.Fatalf("结果 %d 条，想要 %d 条：%+v", len(out), len(want), out)
	}
	for i, w := range want {
		if got := out[i]; got.Name != w.name || got.Ready != w.ready || got.Why != w.why {
			t.Errorf("第 %d 条：%+v，想要 name=%s ready=%v why=%q", i, got, w.name, w.ready, w.why)
		}
	}
	// 就绪的那条要报出这次实际探的地址：换过端口起的那一次，它与清单里写的不是同一个。
	if out[2].Probe != pass.URL {
		t.Errorf("就绪那条带的探针地址是 %q，想要 %q", out[2].Probe, pass.URL)
	}
}

// 在跑而探针一直不通，就得等满窗口——这是「等不到」里唯一一种「再等等可能就好了」，
// 早一秒宣布没等到都是在替用户放弃。
func TestWaitForTimesOutOnAProbeThatNeverPasses(t *testing.T) {
	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer never.Close()

	cfg := waitFixture(t, map[string]string{"live": never.URL})
	withLiveEntry(t, cfg, "live", 0)

	const window = 1200 * time.Millisecond
	start := time.Now()
	out, err := WaitFor(cfg, []string{"live"}, window, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Ready || out[0].Why != WaitTimeout {
		t.Errorf("探针一直不通：%+v，想要「等满窗口」", out[0])
	}
	if el := time.Since(start); el < window {
		t.Errorf("等了 %v 就回来了，没有等满 %v", el, window)
	}
}

// 当场就能答的那两条要先报出来。
//
// 命令行是边等边打的：把「没配探针」压到三分钟之后才说，用户就一直盯着一个
// 看起来卡住了的终端——而它其实早就知道答案了。
func TestWaitForReportsTheQuickOnesFirst(t *testing.T) {
	pass := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer pass.Close()

	cfg := waitFixture(t, map[string]string{"idle": pass.URL, "live": pass.URL})
	withLiveEntry(t, cfg, "live", 0)

	// report 由 WaitFor 在锁底下串行调用，这里不必自己加锁。
	var seen []string
	if _, err := WaitFor(cfg, []string{"live", "idle", "noprobe"}, 3*time.Second, func(r WaitResult) {
		seen = append(seen, r.Name)
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("报出来 %v，想要 3 条", seen)
	}
	if seen[0] != "idle" || seen[1] != "noprobe" {
		t.Errorf("报出来的顺序是 %v，当场能答的 idle、noprobe 该在最前面", seen)
	}
}

// 记录还在、进程早没了（上次没退干净），要当场说「没在跑」。
//
// 这条与上面那条「有记录就算在跑」是分开的两件事：认领看的是那个进程此刻还在不在
// （见 EntryAlive），不是记录还在不在。真去探一个谁的进程都不在的地址，等满窗口
// 之后报的是「探针没通」——把一个「压根没起来」说成「起来了但不健康」。
func TestWaitForIgnoresADeadEntry(t *testing.T) {
	pass := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer pass.Close()

	cfg := waitFixture(t, map[string]string{"idle": pass.URL})
	// 这个号在两套系统上都不可能分给谁（macOS 的 pid 上限是五位数，
	// Linux 是 2^22），而组号也照抄一份，两道门都过不去。
	if err := UpdateState(cfg.StatePath(), func(st *State) error {
		st.Services["idle"] = &Entry{PID: 1 << 30, PGID: 1 << 30, StartedAt: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	out, err := WaitFor(cfg, []string{"idle"}, 3*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Ready || out[0].Why != WaitNotRunning {
		t.Errorf("死记录：%+v，想要「没在跑」", out[0])
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("等了 %v 才答，这条当场就该答", el)
	}
}
