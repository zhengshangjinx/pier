package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 三种等不到要在「等」之前就说清楚：等下去也不会有结果，而盯着一个不会再变的
// 东西看三分钟，正是 wait 该替人省掉的事。
func TestWaitReportsWhyBeforeWaiting(t *testing.T) {
	tempHome(t)
	manifest := writeManifest(t)

	cases := []struct {
		name string
		args []string
		says string // 空表示只看退出码（那句话写在 stderr 上）
	}{
		{name: "没配健康探针", args: []string{"wait", "plain", "--config", manifest}, says: "没配健康探针"},
		{name: "没有在运行", args: []string{"wait", "api", "--config", manifest}, says: "没有在运行"},
		{name: "名字不存在", args: []string{"wait", "nope", "--config", manifest}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var code int
			raw := capture(t, func() { code = Run(c.args) })
			if code == 0 {
				t.Errorf("Run(%q) = 0，本该非 0；输出：%s", c.args, raw)
			}
			if c.says != "" && !strings.Contains(raw, c.says) {
				t.Errorf("输出里要说清为什么等不到（%q），实际是：%s", c.says, raw)
			}
		})
	}
}

// 探针通与不通各走一遍：等待那一段的成败全在退出码上，而它只有对
// 一个真在跑的服务才会走到。
func TestWaitForRunningServices(t *testing.T) {
	tempHome(t)
	dir := t.TempDir()

	// 探针必须真的能通：起一个本机 HTTP 服务来应答。
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer live.Close()
	// 不通的那一路用「刚关掉的服务器」：端口是真的没人听，不是随手挑一个号赌它空着。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n" +
		"  - name: ready\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    health: " + live.URL + "\n" +
		"  - name: stuck\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    health: " + deadURL + "\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, sup, err := setup(manifest)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"ready", "stuck"}
	for _, n := range names {
		svc, err := cfg.Find(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := sup.Start(svc); err != nil {
			t.Fatalf("起不来 %s：%v", n, err)
		}
	}
	defer func() {
		for _, n := range names {
			_ = sup.Stop(n)
		}
	}()

	t.Run("探针通了", func(t *testing.T) {
		var code int
		raw := capture(t, func() {
			code = Run([]string{"wait", "ready", "--config", manifest, "--timeout", "10s"})
		})
		if code != 0 {
			t.Errorf("退出码 = %d，想要 0；输出：%s", code, raw)
		}
		if !strings.Contains(raw, "已就绪") {
			t.Errorf("输出里没说就绪：%s", raw)
		}
	})

	t.Run("超时", func(t *testing.T) {
		var code int
		raw := capture(t, func() {
			code = Run([]string{"wait", "stuck", "--config", manifest, "--timeout", "1s"})
		})
		if code == 0 {
			t.Errorf("探针没通，退出码却是 0；输出：%s", raw)
		}
		if !strings.Contains(raw, "没等到") {
			t.Errorf("输出里没交代结果：%s", raw)
		}
	})

	t.Run("几个一起等", func(t *testing.T) {
		// 一个通一个不通：通的照旧报就绪，不通的照旧算没等到，退出码非 0。
		var code int
		raw := capture(t, func() {
			code = Run([]string{"wait", "ready", "stuck", "--config", manifest, "--timeout", "1s"})
		})
		if code == 0 {
			t.Errorf("有一个没等到，退出码却是 0；输出：%s", raw)
		}
		for _, want := range []string{"ready", "已就绪", "stuck", "没等到"} {
			if !strings.Contains(raw, want) {
				t.Errorf("输出里少了 %q：%s", want, raw)
			}
		}
	})
}
