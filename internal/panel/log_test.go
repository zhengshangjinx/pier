package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// writeSized 造一个指定大小的日志文件。
//
// 用 Truncate 造稀疏文件：磁盘上几乎不占地方，而 stat 报的仍然是逻辑大小——
// 占用统计数的就是后者，这里要的正是它（真要写 64 MB 字节进 tempdir 也可以，
// 只是每跑一次测试都白写一遍，没有换来任何东西）。
func writeSized(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
}

// logHarness 起一个面板并给出它的日志目录。
func logHarness(t *testing.T) (*Panel, string) {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(restartYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return p, p.Config().LogDir()
}

// TestLogUsageReportsBiggestDay 钉着「写得最多的那一天」数得出来、并且说出来。
//
// 单看合计看不出问题：十四天分摊下来，一天 300 MB 和一天 3 MB 的合计可能差不多，
// 而要不妙得多的是前者——它还在按这个速度写。后端只说这一个数是哪一个文件
// （Biggest），话由 logBigNote 说。
func TestLogUsageReportsBiggestDay(t *testing.T) {
	p, logDir := logHarness(t)
	big := int64(proc.LogDayWarnBytes) + 1
	writeSized(t, filepath.Join(logDir, "alpha", "2026-10-05.log"), big)
	writeSized(t, filepath.Join(logDir, "alpha", "2026-10-06.log"), 1<<20)

	got, err := p.LogUsage()
	if err != nil {
		t.Fatalf("统计日志占用失败：%v", err)
	}
	if len(got.Services) != 1 {
		t.Fatalf("数出 %d 个服务，想要 1 个：%v", len(got.Services), got.Services)
	}
	s := got.Services[0]
	if s.Biggest != "2026-10-05" {
		t.Errorf("最大的那天 = %q，想要 2026-10-05", s.Biggest)
	}
	if s.BiggestSize != view.Bytes(big) {
		t.Errorf("最大那天的大小 = %q，想要 %q", s.BiggestSize, view.Bytes(big))
	}
	for _, want := range []string{"2026-10-05", view.Bytes(big), "14 天"} {
		if !strings.Contains(s.BigNote, want) {
			t.Errorf("提醒 = %q，没提到 %q", s.BigNote, want)
		}
	}
	if got.DayWarn != view.Bytes(proc.LogDayWarnBytes) {
		t.Errorf("提醒线 = %q，想要 %q", got.DayWarn, view.Bytes(proc.LogDayWarnBytes))
	}
}

// TestLogUsageSilentBelowWarn 钉着没超线时一个字都不说。
//
// 这条线和「诊断」「自动重启说明」是同一类东西：出现了就意味着要有人看一眼，
// 天天挂着的提醒等于没有提醒。
func TestLogUsageSilentBelowWarn(t *testing.T) {
	p, logDir := logHarness(t)
	writeSized(t, filepath.Join(logDir, "alpha", "2026-10-05.log"), 10<<20)

	got, err := p.LogUsage()
	if err != nil {
		t.Fatalf("统计日志占用失败：%v", err)
	}
	if len(got.Services) != 1 {
		t.Fatalf("数出 %d 个服务，想要 1 个", len(got.Services))
	}
	if s := got.Services[0]; s.BigNote != "" {
		t.Errorf("没超线却提醒了：%q", s.BigNote)
	}
	// 没超线也要知道最大的那天是哪一个：超没超只是这句话说不说的问题，
	// 数本身一样要摆出来。
	if s := got.Services[0]; s.Biggest != "2026-10-05" {
		t.Errorf("最大的那天 = %q，想要 2026-10-05", s.Biggest)
	}
}

// TestLogBigNoteWording 钉着那句话里有什么，尤其是保留期乘出来那一段。
//
// 单说「单日 300 MB」还有人觉得无所谓，乘上十四天才是它真正的代价。
func TestLogBigNoteWording(t *testing.T) {
	big := int64(proc.LogDayWarnBytes) * 3
	over := proc.LogServiceOut{Name: "dev", Biggest: "2026-10-05", BiggestBytes: big}
	if got := logBigNote(over, 14); !strings.Contains(got, view.Bytes(big*14)) {
		t.Errorf("提醒 = %q，没提到按这个量写满 14 天的合计 %q", got, view.Bytes(big*14))
	}
	// 保留期给 0 时只留前半句：乘出来那个数没有依据，宁可不写。
	if got := logBigNote(over, 0); strings.Contains(got, "写满") {
		t.Errorf("保留期为 0 时还算了一个合计出来：%q", got)
	}
	// 没有日期（日志文件名认不出是哪天）时也说不得：说不出是哪一天在刷屏，
	// 这句话就帮不上任何忙。
	if got := logBigNote(proc.LogServiceOut{BiggestBytes: big}, 14); got != "" {
		t.Errorf("认不出日期却提醒了：%q", got)
	}
}
