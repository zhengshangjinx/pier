package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempHome 把这一次用例的数据目录顶到临时目录里，绝不碰真实的 ~/.pier。
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)
	return home
}

// Plan 是发起方与助手之间唯一的合同：助手是个新起的进程，只会读到这个文件。
// 所以这里钉的是「写出去的 JSON 里有助手要的每一个字段」。
func TestPlanRoundTrip(t *testing.T) {
	p := Plan{
		Version:   "0.3.0",
		Dir:       "/tmp/cache/update",
		Stage:     "/tmp/cache/update/0.3.0/stage",
		Target:    "/Applications/Pier.app",
		Kind:      KindBundle,
		Relaunch:  "/Applications/Pier.app",
		ParentPID: 4242,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("写不出计划：%v", err)
	}
	var back Plan
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("读不回计划：%v", err)
	}
	if back != p {
		t.Errorf("来回一趟变了样：\n%+v\n%+v", p, back)
	}
	// 助手是从磁盘上读的，走的必须是同一套编解码。
	path := filepath.Join(t.TempDir(), PlanName)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadPlan(path)
	if err != nil {
		t.Fatalf("读计划文件失败：%v", err)
	}
	if got != p {
		t.Errorf("从文件里读回来的不一样：%+v", got)
	}
}

// 缺一样就不动手。这些判据必须在动手之前全部走完——换到一半才发现少个字段，
// 那时候已经没有干净的回退点了。
func TestPlanCheck(t *testing.T) {
	stage := t.TempDir()
	ok := Plan{Version: "0.3.0", Dir: t.TempDir(), Stage: stage, Target: "/x/Pier.app"}

	cases := []struct {
		name string
		mut  func(Plan) Plan
		want string
	}{
		{"没有版本号", func(p Plan) Plan { p.Version = ""; return p }, "版本号"},
		{"没有落脚目录", func(p Plan) Plan { p.Dir = ""; return p }, "落脚目录"},
		{"没说换哪儿", func(p Plan) Plan { p.Target = ""; return p }, "换哪儿"},
		{"解压的树不在", func(p Plan) Plan { p.Stage = filepath.Join(p.Stage, "没有这个目录"); return p }, "解压"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mut(ok).check()
			if err == nil {
				t.Fatal("这样的计划也放过去了")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("报的是 %q，看不出是 %s 的问题", err, c.want)
			}
		})
	}
	if err := ok.check(); err != nil {
		t.Errorf("这份计划是对的，却不让做：%v", err)
	}
}

// 结果文件是助手唯一能说话的通道：它跑在 Pier 已经退出的空档里，没有窗口能报错。
func TestResultRoundTrip(t *testing.T) {
	tempHome(t)
	want := ApplyResult{
		OK:         false,
		Message:    "装 Pier 的那个目录写不动",
		Version:    "0.3.0",
		RolledBack: true,
		When:       time.Now().Truncate(time.Second),
	}
	path, err := ResultPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadResult(); ok {
		t.Error("还没写过就读出了结果")
	}
	if err := writeResult(path, want); err != nil {
		t.Fatalf("写结果失败：%v", err)
	}
	got, ok := LoadResult()
	if !ok {
		t.Fatal("写了却读不回来")
	}
	if !got.When.Equal(want.When) {
		// 时刻用的是 Go 自己的编解码，比对时不带时区也能对上。
		t.Errorf("时刻变了：%v → %v", want.When, got.When)
	}
	got.When = want.When
	if got != want {
		t.Errorf("结果变了样：%+v → %+v", want, got)
	}
	if err := ClearResult(); err != nil {
		t.Fatalf("清结果失败：%v", err)
	}
	if _, ok := LoadResult(); ok {
		t.Error("清掉了还读得出来")
	}
	// 清一个本来就没有的不算错：界面每次启动都会顺手清一次。
	if err := ClearResult(); err != nil {
		t.Errorf("重复清结果报错：%v", err)
	}
}

// 读坏了、读到一半的都当没有：它不该拦住启动——启动被人拦住，用户连界面都看不到。
func TestLoadResultIgnoresGarbage(t *testing.T) {
	tempHome(t)
	path, err := ResultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadResult(); ok {
		t.Error("读坏的东西也认了")
	}
}

func TestCheckNotEmpty(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "空的")
	if err := os.WriteFile(empty, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	noExec := filepath.Join(dir, "没有执行位")
	if err := os.WriteFile(noExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "好的一份")
	if err := os.WriteFile(good, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := checkNotEmpty(filepath.Join(dir, "不在"), false); err == nil {
		t.Error("不在也算过")
	}
	if err := checkNotEmpty(dir, false); err == nil {
		t.Error("目录也算过")
	}
	if err := checkNotEmpty(empty, false); err == nil {
		t.Error("空文件也算过")
	}
	if err := checkNotEmpty(noExec, true); err == nil {
		t.Error("没有执行位也算过")
	}
	if err := checkNotEmpty(noExec, false); err != nil {
		t.Errorf("不要求执行位时不该拦：%v", err)
	}
	if err := checkNotEmpty(good, true); err != nil {
		t.Errorf("好的一份被拦下了：%v", err)
	}
}

// 压缩包根上只有一个目录，但摊在根上的那种也认——打包方式变一次，这里不该跟着变。
func TestStageRoot(t *testing.T) {
	t.Run("顶层就是那一层", func(t *testing.T) {
		stage := t.TempDir()
		if err := os.WriteFile(filepath.Join(stage, "install.sh"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := stageRoot(stage, "install.sh")
		if err != nil {
			t.Fatal(err)
		}
		if got != stage {
			t.Errorf("认成了 %q", got)
		}
	})
	t.Run("还得往下走一层", func(t *testing.T) {
		stage := t.TempDir()
		inner := filepath.Join(stage, "Pier-0.3.0-macos-universal")
		if err := os.MkdirAll(inner, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(inner, "pier"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := stageRoot(stage, "pier")
		if err != nil {
			t.Fatal(err)
		}
		if got != inner {
			t.Errorf("认成了 %q，该是 %q", got, inner)
		}
	})
	t.Run("找不到", func(t *testing.T) {
		if _, err := stageRoot(t.TempDir(), "install.sh"); err == nil {
			t.Error("找不到也算过")
		}
	})
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "源")
	if err := os.WriteFile(src, []byte("这些字节要原样过去"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "去处")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("复制失败：%v", err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "这些字节要原样过去" {
		t.Errorf("内容对不上：%q", raw)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// 执行位必须留住：换过去的是一份二进制，丢了执行位它就是块石头。
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("执行位丢了：%v", fi.Mode())
	}
	// 中间那个临时名字不该留下：它看起来像模像样，一旦被执行，报的错
	// 和这次更新毫无关系。
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Error("留下了一个 .tmp")
	}
}

// 换文件这件事同一时刻只该有一个人在干。这把锁在 unix 上还是「发起方已经退出」的
// 判据——见 waitForCallerExit。
func TestBeginApplyExcludesSecond(t *testing.T) {
	tempHome(t)
	first, err := BeginApply()
	if err != nil {
		t.Fatalf("头一次就领不到：%v", err)
	}
	if _, err := BeginApply(); err == nil {
		t.Error("第二个人也领到了同一把锁")
	} else if !strings.Contains(err.Error(), "进行中") {
		t.Errorf("报的是 %q，看不出是「已经有一次在做」", err)
	}
	first.Release()
	again, err := BeginApply()
	if err != nil {
		t.Fatalf("放开之后再领不到：%v", err)
	}
	again.Release()
}

// 助手等的就是那把锁被放开。这里不做成「等某个 PID」是有理由的：PID 会被系统
// 复用，而锁由内核在进程退出的那一刻放开，连还没被回收的僵尸也算已经退出。
func TestWaitForCallerExit(t *testing.T) {
	tempHome(t)
	claim, err := BeginApply()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		got, err := waitForCallerExit()
		if got != nil {
			got.Release()
		}
		done <- err
	}()

	// 放开之前不该回来。给它一点时间自己跑到轮询里去。
	select {
	case err := <-done:
		t.Fatalf("发起方还没退出，助手就动手了（%v）", err)
	case <-time.After(2 * pollInterval):
	}
	claim.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("等不到锁：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("锁放开了，助手还杵在那儿")
	}
}

// 计划不对时 Apply 什么都不做，只把原因写进结果里。
func TestApplyRefusesBadPlan(t *testing.T) {
	tempHome(t)
	res := Apply(Plan{Version: "0.3.0", Dir: t.TempDir(), Target: "/x/Pier.app"}, nil)
	if res.OK {
		t.Fatal("这样的计划也做了")
	}
	if !strings.Contains(res.Message, "解压") {
		t.Errorf("失败原因没说出来：%q", res.Message)
	}
	if res.Version != "0.3.0" {
		t.Errorf("结果里没有版本号：%+v", res)
	}
}

// 助手只认一个参数：计划文件在哪儿。别的都从它里面推。
func TestRunHelperWantsExactlyOneArg(t *testing.T) {
	tempHome(t)
	if got := RunHelper(nil); got != 2 {
		t.Errorf("不给参数时退出码是 %d，该是 2", got)
	}
	if got := RunHelper([]string{"a", "b"}); got != 2 {
		t.Errorf("参数给多了退出码是 %d，该是 2", got)
	}
}

// 计划文件读不出来时如实退出，不去猜。
func TestRunHelperBadPlanFile(t *testing.T) {
	tempHome(t)
	if got := RunHelper([]string{filepath.Join(t.TempDir(), "没有这个文件")}); got != 1 {
		t.Errorf("退出码是 %d，该是 1", got)
	}
}

// 日志落在 logs/update/<日期>.log 里，与服务日志同一个「一天一份」的规矩。
func TestLogPathIsDated(t *testing.T) {
	home := tempHome(t)
	got, err := logPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "logs", "update", time.Now().Format("2006-01-02")+".log")
	if got != want {
		t.Errorf("日志落在 %q，该是 %q", got, want)
	}
}

// NewLogger 传 nil 得到一个什么都不做的：调用方不必处处判空。
func TestNewLoggerNilWritesNothing(t *testing.T) {
	logf := NewLogger(nil)
	if logf == nil {
		t.Fatal("拿到一个空的记录口")
	}
	logf("这行不该让谁崩掉：%d", 1)
}

// 进度与日志都是写给人看的，格式变了没关系，但不能一次写成两行——
// 「一行一句」是这份日志唯一的规矩。
func TestNewLoggerOneLinePerCall(t *testing.T) {
	var b strings.Builder
	logf := NewLogger(&b)
	logf("第一句 %s", "话")
	logf("第二句")
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("写了 %d 行：%q", len(lines), b.String())
	}
	for _, l := range lines {
		if len(l) < 20 || l[4] != '-' {
			t.Errorf("这一行前面没有时刻：%q", l)
		}
	}
}
