package cli

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

// 这一份真的拉起替身进程（拿 sleep 当替身），前置的就绪信号是一个文件。
// 与 wait_test.go 同一套写法：setup + Run，输出用 capture 接。

// depManifest 写一份三条服务的清单，返回清单路径与那个当就绪信号的文件。
//
// plain 的 depends_on 不写条件，只为把它排在 api 后面：这一条要验的是
// 「等前置不占住整条流水线」，所以必须有一个服务排在等待者的后面。
func depManifest(t *testing.T) (manifest, marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "ready")
	manifest = filepath.Join(dir, "pier.yaml")
	yaml := "services:\n" +
		"  - name: db\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    health: \"cmd: test -f " + marker + "\"\n" +
		"  - name: api\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    depends_on: [db:healthy]\n" +
		"  - name: plain\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    depends_on: [api]\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest, marker
}

// waitEntry 轮询状态文件，等这条服务出现在里面（也就是它的进程被拉起来了）。
func waitEntry(t *testing.T, manifest, name string, d time.Duration) bool {
	t.Helper()
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		state, err := proc.LoadState(cfg.StatePath())
		if err == nil {
			if e, ok := state.Services[name]; ok && proc.EntryAlive(e) {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestUpDoesNotSerializeOnDependencyWait 钉着「等前置不占住整条流水线」。
//
// up 的时序是先全部拉起、再统一等就绪；等前置要是跟在拉起那一半里同步地等，
// 一个声明了 depends_on: [mysql:healthy] 的服务会把它后面每一个都压住，
// 而那条流水线本来只受编译速度限制。
//
// 验法：plain 排在 api 后面，api 正卡在等 db 上——plain 必须在前置还没就绪的
// 时候就起来。同步等的话，它要等到那个文件出现才动。
func TestUpDoesNotSerializeOnDependencyWait(t *testing.T) {
	manifest, marker := depManifest(t)

	var code int
	raw := capture(t, func() {
		done := make(chan int, 1)
		go func() { done <- Run([]string{"up", "--config", manifest}) }()

		if !waitEntry(t, manifest, "plain", 10*time.Second) {
			t.Error("plain 一直没起来：等前置把整条流水线占住了")
		}
		// 关键的一刻：plain 已经起来，而前置还没就绪、api 还没起。
		if _, err := os.Stat(marker); err == nil {
			t.Error("前置已经就绪了，这一条验的时序没成立")
		}
		if waitEntry(t, manifest, "api", 10*time.Millisecond) {
			t.Error("前置没就绪，api 却已经起来了")
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}

		select {
		case code = <-done:
		case <-time.After(20 * time.Second):
			t.Error("up 一直没收尾")
			return
		}
	})
	defer capture(t, func() { Run([]string{"down", "--config", manifest}) })

	if code != 0 {
		t.Errorf("退出码 = %d，想要 0；输出：\n%s", code, raw)
	}
	if !waitEntry(t, manifest, "api", 2*time.Second) {
		t.Errorf("前置就绪之后 api 该起来了；输出：\n%s", raw)
	}
	// 等到了就不该有那一段：说了会让人去找一个并不存在的问题。
	if strings.Contains(raw, "没等到前置") {
		t.Errorf("等到了却说没等到：\n%s", raw)
	}
}

// TestUpSaysWhenTheDependencyIsNotComing 钉着「等不到照旧起，把话说清楚」。
//
// 只点名 api 时，db 这一趟压根没有人会去拉（点名不展开前置的闭包），
// 那条依赖这时候不可能被满足。等满三分半再落一句同样的话只是白等，
// 所以直接说出口——而服务照常起来，退出码也不动。
func TestUpSaysWhenTheDependencyIsNotComing(t *testing.T) {
	manifest, _ := depManifest(t)

	start := time.Now()
	var code int
	raw := capture(t, func() {
		code = Run([]string{"up", "api", "--config", manifest})
	})
	defer capture(t, func() { Run([]string{"down", "--config", manifest}) })

	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("等了一个不会就绪的前置 %s", d)
	}
	// 服务起来了，up 的承诺兑现了，所以退出码是 0。
	if code != 0 {
		t.Errorf("退出码 = %d，想要 0（没等到前置不算启动失败）；输出：\n%s", code, raw)
	}
	for _, want := range []string{"没有等到 db 就绪", "没等到前置"} {
		if !strings.Contains(raw, want) {
			t.Errorf("输出里少了 %q：\n%s", want, raw)
		}
	}
	if !waitEntry(t, manifest, "api", 2*time.Second) {
		t.Errorf("等不到前置也应当照常起来；输出：\n%s", raw)
	}
}

// TestComingNamesCountsRunners 钉着「它还会不会来」的判定。
//
// 三条口径各自都要钉住：这次要起的算「会来」，此刻在跑的也算，记录留着而进程
// 早没了的不算——进程组号会被系统复用，拿一条死记录当「它在跑」，
// 等的就是一个已经不存在的东西。
//
// db 那条真起一个替身进程，不手写记录：「在跑」是 PID 加进程组一起判的
// （见 proc.sameEntry），手写的记录要凑齐这两样才能算活着，而凑的过程正是
// 这份判定自己——用真进程才是在验它。
func TestComingNamesCountsRunners(t *testing.T) {
	manifest, _ := depManifest(t)
	cfg, sup, err := setup(manifest)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := pickServices(cfg, []string{"api"}, false)
	if err != nil {
		t.Fatal(err)
	}

	coming := comingNames(cfg, targets)
	if !coming["api"] {
		t.Error("这次要起的服务自己不在名单里")
	}
	if coming["db"] {
		t.Error("没起过的 db 不该算「会来」")
	}

	db, err := cfg.Find("db")
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(db); err != nil {
		t.Fatalf("起不来 db：%v", err)
	}
	defer func() { _ = sup.Stop("db") }()

	if err := proc.UpdateState(cfg.StatePath(), func(st *proc.State) error {
		st.Services["plain"] = &proc.Entry{PID: 1 << 30, StartedAt: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	coming = comingNames(cfg, targets)
	if !coming["db"] {
		t.Error("状态里活着的那条该算「会来」")
	}
	if coming["plain"] {
		t.Error("PID 这么大的进程不存在，不该算「会来」")
	}
}

// TestDepSuffixOnlyWhenMissed 钉着那句话只在该出现的时候出现。
func TestDepSuffixOnlyWhenMissed(t *testing.T) {
	if got := depSuffix(nil); got != "" {
		t.Errorf("等到了却说了 %q", got)
	}
	got := depSuffix([]string{"mysql"})
	want := fmt.Sprintf("（%s）", "没有等到 mysql 就绪")
	if got != want {
		t.Errorf("depSuffix = %q，想要 %q", got, want)
	}
}
