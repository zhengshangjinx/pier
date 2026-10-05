package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// writeLog 给一个服务造一份今天的日志，内容是给定的几行。
func writeLog(t *testing.T, cfg *config.Config, name string, lines []string) string {
	t.Helper()
	dir := cfg.LogDirFor(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, time.Now().Format(config.LogDateLayout)+".log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 一次点几个服务是这条命令的常用姿势（查一个问题常常要连着看三个）。
// 「其中一个还没有日志」不该把其余的也拦下——一起看几个，正是为了看哪个没出声。
func TestLogsSeveralServices(t *testing.T) {
	tempHome(t)
	manifest := writeManifest(t)
	cfg, err := loadConfig(manifest)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("另一个没有日志", func(t *testing.T) {
		writeLog(t, cfg, "api", []string{"编译完成", "监听 8080"})
		var code int
		raw := capture(t, func() {
			code = Run([]string{"logs", "api", "plain", "--config", manifest})
		})
		if code == 0 {
			t.Errorf("有一个服务没有日志，退出码却是 0；输出：%s", raw)
		}
		if !strings.Contains(raw, "监听 8080") {
			t.Errorf("有日志的那个没打出来：%s", raw)
		}
		// 两个名字都要出现：一起看几个的时候，哪一段是谁的要一眼看见。
		for _, name := range []string{"api", "plain"} {
			if !strings.Contains(raw, name) {
				t.Errorf("输出里没有标出 %s：%s", name, raw)
			}
		}
	})

	t.Run("两个都有日志", func(t *testing.T) {
		writeLog(t, cfg, "plain", []string{"起来了"})
		var code int
		raw := capture(t, func() {
			code = Run([]string{"logs", "api", "plain", "--config", manifest})
		})
		if code != 0 {
			t.Errorf("两个都有日志，退出码 = %d，想要 0；输出：%s", code, raw)
		}
		if !strings.Contains(raw, "监听 8080") || !strings.Contains(raw, "起来了") {
			t.Errorf("两段日志没都打出来：%s", raw)
		}
	})
}

// --tail 的承诺就是「只看最后几行」：默认一百行，给了数字就按给的来。
func TestLogsTail(t *testing.T) {
	tempHome(t)
	manifest := writeManifest(t)
	cfg, err := loadConfig(manifest)
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		lines = append(lines, "行 "+strings.Repeat("x", i))
	}
	writeLog(t, cfg, "api", lines)

	var code int
	raw := capture(t, func() {
		code = Run([]string{"logs", "api", "--tail", "3", "--config", manifest})
	})
	if code != 0 {
		t.Fatalf("退出码 = %d，想要 0；输出：%s", code, raw)
	}
	if !strings.Contains(raw, "行 "+strings.Repeat("x", 10)) {
		t.Errorf("最后一行没打出来：%s", raw)
	}
	if strings.Contains(raw, "行 "+strings.Repeat("x", 7)+"\n") {
		t.Errorf("要的是最后 3 行，第 7 行不该出现：%s", raw)
	}
}
