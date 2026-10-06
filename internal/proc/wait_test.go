package proc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// waitFixture 铺一份清单：四个服务，各配各的探针（没有的就不写 health）。
//
// 走 YAML 而不是数据文件：清单与状态都落在临时目录里（状态是清单旁边的
// .pier/state.json），一个字节都不碰真实数据，也不必设 PIER_HOME。
func waitFixture(t *testing.T, health map[string]string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	body := "services:\n"
	for _, name := range []string{"noprobe", "idle", "live", "dead"} {
		h := ""
		if v, ok := health[name]; ok {
			h = "\n    health: " + v
		}
		body += "  - name: " + name + "\n    dir: " + name + "\n    kind: shell" + h + "\n"
	}
	manifest := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatalf("清单没加载起来：%v", err)
	}
	return cfg
}

// TestParseWaitTimeout 钉着 `30` 与 `30s` 都认：脚本里最容易写出来的是前者，
// 为它回一句「请写成 30s」不值得。认不出来的必须报错，不能悄悄退回默认的 180s
// ——那会让一次写错的调用变成「等满三分钟然后失败」。
func TestParseWaitTimeout(t *testing.T) {
	good := map[string]time.Duration{
		"30":     30 * time.Second,
		"30s":    30 * time.Second,
		"2m":     2 * time.Minute,
		"1m30s":  90 * time.Second,
		" 30s  ": 30 * time.Second,
	}
	for in, want := range good {
		got, err := ParseWaitTimeout(in)
		if err != nil {
			t.Errorf("ParseWaitTimeout(%q) 报错：%v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseWaitTimeout(%q) = %v，想要 %v", in, got, want)
		}
	}
	for _, in := range []string{"0", "-5", "0s", "一会儿", "", " "} {
		if got, err := ParseWaitTimeout(in); err == nil {
			t.Errorf("ParseWaitTimeout(%q) = %v，本该报错", in, got)
		}
	}
}

// TestWaitForNoProbeAndNotRunning 钉着当场就能回答的那两种。
//
// 这两种等下去不会有结果（清单里根本没有「就绪」这个信号／没人去起它），
// 而盯着一个不会再变的东西看满三分钟，正是 wait 该替人省掉的事。
func TestWaitForNoProbeAndNotRunning(t *testing.T) {
	cfg := waitFixture(t, map[string]string{"idle": "http://127.0.0.1:1/health"})

	// idle 在清单里、状态里一条记录都没有；noprobe 连 health 都没写。
	out, err := WaitFor(cfg, []string{"idle", "noprobe"}, HealthWait, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("结果 %d 条，想要 2 条：%+v", len(out), out)
	}
	if out[0].Name != "idle" || out[0].Why != WaitNotRunning || out[0].Ready {
		t.Errorf("idle：%+v，想要「没在跑」", out[0])
	}
	if out[1].Name != "noprobe" || out[1].Why != WaitNoProbe || out[1].Ready {
		t.Errorf("noprobe：%+v，想要「没配探针」", out[1])
	}
	// 没在跑的那条也要报出探的地址：用户手里那张清单上写的就是这个地址，
	// 说清楚「探的是它」，才不必再回去翻一遍自己配了什么。
	if out[0].Probe == "" {
		t.Error("没在跑的服务也该带上它配的探针地址")
	}
}

// TestWaitForRejectsUnknownNames 钉着名单里有个不存在的名字就整个报错。
//
// 只等其余几个会把「名字写错了」这件事藏起来：退出码是正常的 0，
// 脚本据此认为一切都好。
func TestWaitForRejectsUnknownNames(t *testing.T) {
	cfg := waitFixture(t, nil)
	out, err := WaitFor(cfg, []string{"live", "不存在"}, time.Second, nil)
	if err == nil {
		t.Fatalf("写错的名字应当报错，却拿到 %+v", out)
	}
	if out != nil {
		t.Errorf("报错时不该还回一份结果：%+v", out)
	}
}

// TestWaitForCountsEachNameOnce 钉着同一个名字报两遍只探一次：两份结果一行一样，
// 读的人只会以为自己看花了。
func TestWaitForCountsEachNameOnce(t *testing.T) {
	cfg := waitFixture(t, map[string]string{"idle": "http://127.0.0.1:1/health"})
	out, err := WaitFor(cfg, []string{"idle", "idle"}, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("结果 %d 条，想要 1 条：%+v", len(out), out)
	}
}
