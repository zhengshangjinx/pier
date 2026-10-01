package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 日志按天分文件、一个服务一个目录。布局本身是纯粹的路径拼接，
// 但它同时被读写两端依赖（supervisor 往这写、proc 与界面从这读），
// 所以把形状钉住。

func TestLogPathsArePerServiceAndPerDay(t *testing.T) {
	cfg := newStore(filepath.Join(t.TempDir(), "services.json"), t.TempDir())
	day := time.Date(2026, 10, 1, 23, 59, 0, 0, time.Local)

	if got, want := cfg.LogDirFor("alpha"), filepath.Join(cfg.LogDir(), "alpha"); got != want {
		t.Errorf("LogDirFor = %q，想要 %q", got, want)
	}
	// 一天一份，文件名就是那一天——人能一眼读出来，sort 出来也就是先后。
	if got, want := cfg.LogPathOn("alpha", day), filepath.Join(cfg.LogDir(), "alpha", "2026-10-01.log"); got != want {
		t.Errorf("LogPathOn = %q，想要 %q", got, want)
	}
	// 同一天的早先时刻还是同一份：分天的边界是零点，不是启动时刻。
	if a, b := cfg.LogPathOn("alpha", day), cfg.LogPathOn("alpha", day.Add(-23*time.Hour)); a != b {
		t.Errorf("同一天的两个时刻应当是同一份文件：%q / %q", a, b)
	}
	// 跨过零点就是另一份。
	if a, b := cfg.LogPathOn("alpha", day), cfg.LogPathOn("alpha", day.Add(2*time.Minute)); a == b {
		t.Error("跨了零点应当是两份文件")
	}
}

func TestLogPathIsToday(t *testing.T) {
	cfg := newStore(filepath.Join(t.TempDir(), "services.json"), t.TempDir())
	want := filepath.Join(cfg.LogDir(), "beta", time.Now().Format(LogDateLayout)+".log")
	if got := cfg.LogPath("beta"); got != want {
		t.Errorf("LogPath = %q，想要 %q", got, want)
	}
}

func TestLogDirFollowsPierHome(t *testing.T) {
	// PIER_HOME 一换，日志目录跟着走。测试靠的就是这条：绝不碰真实数据。
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)

	cfg, err := OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.LogDir(), home) {
		t.Errorf("日志目录 %q 不在 PIER_HOME（%q）之下", cfg.LogDir(), home)
	}
	if !strings.HasPrefix(cfg.LogPath("alpha"), filepath.Join(home, "logs", "alpha")) {
		t.Errorf("服务的日志路径 %q 不在 %q 之下", cfg.LogPath("alpha"), home)
	}
}

func TestLogDateLayoutSortsChronologically(t *testing.T) {
	// 布局的价值一半在「能 sort」：LatestLog 与过期判定都直接比字符串，
	// 前提是字典序等于时间序。换格式（比如改成 01-02-2006）会当场破坏它。
	days := []time.Time{
		time.Date(2026, 9, 9, 0, 0, 0, 0, time.Local),
		time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local),
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local),
		time.Date(2027, 1, 2, 0, 0, 0, 0, time.Local),
	}
	for i := 1; i < len(days); i++ {
		prev, cur := days[i-1].Format(LogDateLayout), days[i].Format(LogDateLayout)
		if prev >= cur {
			t.Errorf("字符串比较与时间先后不一致：%s 应当排在 %s 前面", prev, cur)
		}
	}
}
