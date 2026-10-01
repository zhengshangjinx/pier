package proc

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 日志的按天布局与清理。这一摊全是日期算术，而日期算术错起来是安静的：
// 保留期多算一天、少算一天，界面上都还是「已清理 N 个文件」，没人看得出来。
// 所以每一条都按固定的 now 去摆文件，不依赖跑测试那天是几号。

// day 返回 now 往前 n 天的那一天，按 LogDateLayout 格式化。
func day(now time.Time, n int) string {
	return now.AddDate(0, 0, -n).Format(config.LogDateLayout)
}

// writeLog 在 root/<服务>/<日期>.log 里写一段内容，返回路径。
func writeLog(t *testing.T, root, svc, date, text string) string {
	t.Helper()
	dir := filepath.Join(root, svc)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, date+".log")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLogUsageCountsPerService(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)

	// alpha 三天、beta 一天。beta 单份更大，所以它排在前面——
	// 这一页是「谁占得多」，排序和大小必须一致。
	writeLog(t, root, "alpha", day(now, 0), "今天的")
	writeLog(t, root, "alpha", day(now, 1), "昨天的")
	writeLog(t, root, "alpha", day(now, 9), "九天前的")
	writeLog(t, root, "beta", day(now, 2), "两天天前的，但这一份最长，字最多")

	out := LogUsage(root, LogKeepDays, now)

	if !out.OK || out.Dir != root || out.KeepDays != LogKeepDays {
		t.Fatalf("整体字段不对：%+v", out)
	}
	if out.Files != 4 {
		t.Errorf("文件数 = %d，想要 4", out.Files)
	}
	if len(out.Services) != 2 || out.Services[0].Name != "beta" {
		t.Fatalf("服务应按占用从大到小排，实际 %+v", out.Services)
	}

	// 覆盖区间取的是文件名里的日期，不是 mtime：mtime 会被拷贝、解压改掉。
	a := out.Services[1]
	if a.Name != "alpha" || a.Files != 3 {
		t.Fatalf("alpha 这一条不对：%+v", a)
	}
	if a.Oldest != day(now, 9) || a.Newest != day(now, 0) {
		t.Errorf("alpha 的日期区间 = %s ~ %s，想要 %s ~ %s",
			a.Oldest, a.Newest, day(now, 9), day(now, 0))
	}

	var sum int64
	for _, s := range out.Services {
		sum += s.Bytes
	}
	if sum != out.Bytes {
		t.Errorf("合计 %d 与各服务之和 %d 对不上", out.Bytes, sum)
	}
}

func TestLogUsageOfMissingDirIsEmptyNotError(t *testing.T) {
	// 一个服务都没启动过时，logs/ 根本不存在。这是正常情况，不是错误。
	out := LogUsage(filepath.Join(t.TempDir(), "还没有"), LogKeepDays, time.Now())
	if !out.OK {
		t.Error("目录不存在时 OK 仍应为真")
	}
	if out.Bytes != 0 || out.Files != 0 || len(out.Services) != 0 {
		t.Errorf("目录不存在时应当什么都不报，实际 %+v", out)
	}
}

func TestPruneLogsKeepsRecent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	old := writeLog(t, root, "alpha", day(now, 15), "超期")
	edge := writeLog(t, root, "alpha", day(now, 14), "正好第 14 天，留着")
	fresh := writeLog(t, root, "alpha", day(now, 13), "留着")

	out := PruneLogs(root, "", nil, LogKeepDays, now)

	if out.Files != 1 {
		t.Errorf("应当只删掉超期的那一份，实际删了 %d 个", out.Files)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("15 天前的那份没有被删掉")
	}
	for _, p := range []string{edge, fresh} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s 不该被删：%v", filepath.Base(p), err)
		}
	}
}

func TestPruneLogsOnlyNamedService(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	mine := writeLog(t, root, "alpha", day(now, 30), "超期")
	other := writeLog(t, root, "beta", day(now, 30), "也超期，但没点名")

	out := PruneLogs(root, "alpha", nil, LogKeepDays, now)

	if out.Files != 1 {
		t.Fatalf("应当只删 alpha 的那一份，实际 %d 个", out.Files)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Error("alpha 超期的那份没有被删掉")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("没点名的服务不该被清：%v", err)
	}
}

func TestPruneLogsFallsBackToModTime(t *testing.T) {
	// 认不出日期名的文件（手工放进去的、别的工具写的）按 mtime 判，
	// 否则它们会永远堆在日志目录里，谁也不会去删。
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	dir := filepath.Join(root, "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(dir, "老文件.txt")
	live := filepath.Join(dir, "新文件.txt")
	for _, p := range []string{stale, live} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := now.AddDate(0, 0, -30)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	out := PruneLogs(root, "", nil, LogKeepDays, now)

	if out.Files != 1 {
		t.Fatalf("应当只删 mtime 超期的那一个，实际 %d 个", out.Files)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("mtime 超期的那份没有被删掉")
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("mtime 还新的一份不该被删：%v", err)
	}
}

func TestPruneLogsDropsEmptyServiceDir(t *testing.T) {
	// 清完之后空掉的服务目录要一并收掉，否则日志页上会留下一排
	// 「0 个文件」的空壳——那些服务其实早就没日志了。
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	writeLog(t, root, "alpha", day(now, 30), "超期")

	PruneLogs(root, "", nil, LogKeepDays, now)

	if _, err := os.Stat(filepath.Join(root, "alpha")); !os.IsNotExist(err) {
		t.Error("清空之后那个服务目录应当被收掉")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("日志根目录本身不该被删：%v", err)
	}
}

func TestPruneLogsDisabledWhenKeepDaysIsZero(t *testing.T) {
	// keepDays <= 0 是「不清理」而不是「全清」——这个判断写反的代价是
	// 一次启动把用户所有历史日志清光，所以单钉一条。
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	p := writeLog(t, root, "alpha", day(now, 300), "很旧")

	if out := PruneLogs(root, "", nil, 0, now); out.Files != 0 {
		t.Errorf("保留天数为 0 时应当什么都不删，实际删了 %d 个", out.Files)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("那一份不该被删：%v", err)
	}
}

func TestClearLogs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	writeLog(t, root, "alpha", day(now, 0), "今天")
	writeLog(t, root, "alpha", day(now, 300), "很旧，但清空不看日期")
	writeLog(t, root, "beta", day(now, 0), "别的服务")

	// 只清一个：另一个服务连目录一起留着。
	out := ClearLogs(root, "alpha", nil)
	if out.Files != 2 {
		t.Errorf("alpha 应当删掉 2 个文件，实际 %d 个", out.Files)
	}
	if _, err := os.Stat(filepath.Join(root, "alpha")); !os.IsNotExist(err) {
		t.Error("alpha 的目录应当被收掉")
	}
	if _, err := os.Stat(filepath.Join(root, "beta", day(now, 0)+".log")); err != nil {
		t.Errorf("beta 的日志不该被动：%v", err)
	}

	// 再清全部：什么都没了，但根目录还在。
	out = ClearLogs(root, "", nil)
	if out.Files != 1 {
		t.Errorf("应当再删掉 1 个文件，实际 %d 个", out.Files)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("日志根目录不该被删掉：%v", err)
	}
	if len(entries) != 0 {
		t.Errorf("根目录下应当什么都不剩，实际还有 %d 项", len(entries))
	}
}

func TestClearLogsCountsWhatItDeleted(t *testing.T) {
	// 回执里的字节数必须是真的：报一个没释放的数字比不报还糟。
	root := t.TempDir()
	p := writeLog(t, root, "alpha", "2026-10-01", "1234567890")

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	out := ClearLogs(root, "", nil)
	if out.Bytes != fi.Size() {
		t.Errorf("释放的字节数 = %d，想要 %d", out.Bytes, fi.Size())
	}
}

func TestLogDatesListsNewestFirst(t *testing.T) {
	// 抽屉里的日期选择读的就是它：只能选真有日志的那几天。
	root := t.TempDir()
	writeLog(t, root, "alpha", "2026-09-30", "昨天")
	writeLog(t, root, "alpha", "2026-10-01", "今天")
	writeLog(t, root, "alpha", "2026-08-12", "上个月")
	// 认不出日期的文件不算一天：它不是这个布局写出来的。
	if err := os.WriteFile(filepath.Join(root, "alpha", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "alpha", "2026-09-01.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := LogDates(filepath.Join(root, "alpha"))
	want := []string{"2026-10-01", "2026-09-30", "2026-08-12"}
	if len(got) != len(want) {
		t.Fatalf("LogDates = %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LogDates = %v，想要 %v（从新到旧）", got, want)
		}
	}

	// 一份日志都没有时给空列表，不是 nil：界面直接对它 .map，nil 会炸。
	if got := LogDates(filepath.Join(root, "没有这个服务")); got == nil || len(got) != 0 {
		t.Errorf("目录不存在时应当给空列表，实际 %#v", got)
	}
}

func TestPruneAndClearSkipBusyServices(t *testing.T) {
	// 正在跑的服务手里的日志 fd 是启动那一刻打开的：删掉文件只是 unlink，
	// 进程照样往里写，「释放了 X」就成了假账。整条链路上只有这一处护栏，
	// 所以两个入口都要钉。
	root := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	mine := writeLog(t, root, "alpha", day(now, 30), "超期，但 alpha 在跑")
	other := writeLog(t, root, "beta", day(now, 30), "超期，beta 没在跑")
	busy := map[string]bool{"alpha": true}

	out := PruneLogs(root, "", busy, LogKeepDays, now)
	if out.Files != 1 {
		t.Fatalf("应当只清掉没在跑的那一个，实际 %d 个", out.Files)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("正在运行的服务的日志不该被删：%v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Error("没在跑的那个服务，超期日志应当照清")
	}

	// 点名单个服务时同样跳过，而且什么都不删。
	if out := PruneLogs(root, "alpha", busy, LogKeepDays, now); out.Files != 0 {
		t.Errorf("点名的服务正在跑时不该删任何东西，实际删了 %d 个", out.Files)
	}
	if out := ClearLogs(root, "alpha", busy); out.Files != 0 {
		t.Errorf("清空同样要跳过，实际删了 %d 个", out.Files)
	}
	// 清全部时也跳过它，别的照清。
	writeLog(t, root, "beta", day(now, 0), "beta 重新写了一份")
	if out := ClearLogs(root, "", busy); out.Files != 1 {
		t.Errorf("清全部时应当只剩 beta 那一个，实际 %d 个", out.Files)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("清全部也不该动正在运行的服务：%v", err)
	}
}

// treeTotals 独立数一遍目录里有多少文件、多少字节。
// 刻意不复用 treeSize：拿被测的那把尺子去量被测的结果，量不出偏差。
func treeTotals(t *testing.T, root string) (int, int64) {
	t.Helper()
	n := 0
	var size int64
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, size
}

func TestClearLogsReportsWhatActuallyWentAway(t *testing.T) {
	// 删一半失败时两个数都要跟着缩水：报一个没释放的数字比不报还糟。
	// 这里造一次真的删不掉——只读目录里的文件 RemoveAll 收不走
	// （删一个条目要的是它所在目录的写权限，不是它自己的）。
	root := t.TempDir()
	writeLog(t, root, "alpha", "2026-10-01", "1234567890")
	writeLog(t, root, "beta", "2026-10-01", "abc")
	locked := filepath.Join(root, "beta", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "keep.txt"), []byte("删不掉"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)

	files0, bytes0 := treeTotals(t, root)
	out := ClearLogs(root, "", nil)
	files1, bytes1 := treeTotals(t, root)

	if files1 == 0 {
		t.Fatal("这次删除居然全成功了，这条测试就没在测「删一半」")
	}
	if out.Files != files0-files1 {
		t.Errorf("报的文件数 = %d，实际少了 %d 个", out.Files, files0-files1)
	}
	if out.Bytes != bytes0-bytes1 {
		t.Errorf("报的字节数 = %d，实际少了 %d", out.Bytes, bytes0-bytes1)
	}
}

func TestLatestLog(t *testing.T) {
	root := t.TempDir()
	if got := LatestLog(filepath.Join(root, "没有这个目录")); got != "" {
		t.Errorf("目录不存在时应当返回空串，实际 %q", got)
	}

	// 文件名就是日期，所以字典序等于时间序，比较字符串即可。
	writeLog(t, root, "alpha", "2026-09-30", "昨天")
	writeLog(t, root, "alpha", "2026-10-01", "今天")
	writeLog(t, root, "alpha", "2026-09-09", "更早")
	if err := os.WriteFile(filepath.Join(root, "alpha", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := LatestLog(filepath.Join(root, "alpha")), filepath.Join(root, "alpha", "2026-10-01.log"); got != want {
		t.Errorf("LatestLog = %q，想要 %q", got, want)
	}
}

func TestLogFilePrefersLatestAndFallsBackToToday(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIER_HOME", dir)

	cfg, err := config.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	// 没启动过：给今天那份的路径。文件还不存在，但「它会在哪」是有意义的答案
	// ——界面拿它去访达里定位。
	if got, want := LogFile(cfg, "alpha"), cfg.LogPath("alpha"); got != want {
		t.Errorf("没有日志时 LogFile = %q，想要今天那份 %q", got, want)
	}

	// 跨了零点还在跑的服务写的一直是启动那天的文件，所以「最新的一份」才是
	// 此刻真正在写的那个，不是「今天那份」。
	past := cfg.LogPathOn("alpha", time.Now().AddDate(0, 0, -3))
	if err := os.MkdirAll(filepath.Dir(past), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(past, []byte("三天前启动的那次运行"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LogFile(cfg, "alpha"); got != past {
		t.Errorf("LogFile = %q，想要最新的一份 %q", got, past)
	}
}

func TestTrimToLastRun(t *testing.T) {
	text := LogStartMarker("alpha") + " 09:00\n第一次运行的输出\n" +
		LogStartMarker("alpha") + " 10:30\n第二次运行的输出\n"

	got := TrimToLastRun(text, "alpha")
	if want := LogStartMarker("alpha") + " 10:30\n第二次运行的输出\n"; got != want {
		t.Errorf("应当从最后一次启动标记开始，实际：\n%s", got)
	}

	// 找不到标记（旧文件、手工放进去的文件）就原样返回，不能截成空。
	same := "没有任何标记的一段输出\n"
	if got := TrimToLastRun(same, "alpha"); got != same {
		t.Errorf("没有标记时应当原样返回，实际 %q", got)
	}

	// 标记是带服务名的：另一个服务的标记不能当成自己的。
	other := LogStartMarker("beta") + " 10:30\nbeta 的输出\n"
	if got := TrimToLastRun(other, "alpha"); got != other {
		t.Errorf("别的服务的标记不该被认成自己的，实际 %q", got)
	}
}

func TestLogStartMarkerIsWrittenOnStart(t *testing.T) {
	// 写的一方（supervisor）与读的一方（TrimToLastRun）靠同一串字符对上。
	// 这一条把两者的约定钉住：标记里必须有服务名和「启动于」。
	m := LogStartMarker("mock-payment")
	if want := "=== mock-payment 启动于"; m != want {
		t.Errorf("LogStartMarker = %q，想要 %q", m, want)
	}
}
