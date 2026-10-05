package update

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helperEnv 把计划文件的位置传给「再起一份自己」的那个进程。
const helperEnv = "PIER_UPDATE_TEST_PLAN"

// TestHelperProcess 不是一个用例，是被起的那一份自己。
// 起它的那一份用 -test.run 指名道姓地跑它，别的用例碰不到它。
func TestHelperProcess(t *testing.T) {
	plan := os.Getenv(helperEnv)
	if plan == "" {
		return
	}
	os.Exit(RunHelper([]string{plan}))
}

// startHelper 起一份助手进程，让它在真正换文件的那条路上跑一遍。
//
// 走真的子进程而不是在同进程里调 RunHelper：这里要证的是「等到调用方退出才动手」，
// 而同进程里根本没有「另一个进程握着锁」这回事。
func startHelper(t *testing.T, p Plan) *exec.Cmd {
	t.Helper()
	if p.Dir == "" {
		t.Fatal("计划里得有个落脚目录")
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(p.Dir, PlanName)
	if err := os.WriteFile(planPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), helperEnv+"="+planPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("助手没起起来：%v", err)
	}
	return cmd
}

// writeStage 造一棵解压出来的树：一份裸命令行安装那种形状。
func writeStage(t *testing.T, dir, content string) string {
	t.Helper()
	inner := filepath.Join(dir, "pier-0.3.0-macos-universal")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, cliExeName), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 这一条比别的都值钱：助手跑在 Pier 已经退出的空档里，失败时没有窗口能报错，
// 所以「什么时候动手」和「离开时留下了什么」都得钉住。
func TestHelperWaitsThenSwaps(t *testing.T) {
	home := tempHome(t)
	dir := filepath.Join(home, "cache", "update")
	stage := writeStage(t, filepath.Join(home, "解压出来的"), "新的")
	target := filepath.Join(home, "安装位", cliExeName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("旧的"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 站在发起方的位置上：锁先握在自己手里，等同于「这个 Pier 还开着」。
	claim, err := BeginApply()
	if err != nil {
		t.Fatal(err)
	}
	cmd := startHelper(t, Plan{
		Version: "0.3.0",
		Dir:     dir,
		Stage:   stage,
		Target:  target,
		Kind:    KindCLI,
		// 命令行发起的那种不拉界面回来，顺手也把这条最省事的路径验了。
		Relaunch: "",
	})

	// 发起方还活着：安装目录一个字节都不该动。给足几轮轮询的时间。
	time.Sleep(4 * pollInterval)
	if got := readFile(t, target); got != "旧的" {
		t.Fatalf("发起方还没退出，助手就动了手：现在是 %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ResultName)); !os.IsNotExist(err) {
		t.Error("还没动手就先留下了结果")
	}

	// 退出：锁一放开，助手就该接着做。
	claim.Release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("助手没做完：%v", err)
	}
	if got := readFile(t, target); got != "新的" {
		t.Errorf("换完是 %q，该是新的那份", got)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("执行位丢了：%v", fi.Mode())
	}

	// 结果文件是用户下次启动唯一能看到的东西，它得在。
	res, ok := LoadResult()
	if !ok {
		t.Fatal("助手什么都没留下")
	}
	if !res.OK || res.Version != "0.3.0" || res.Message != "" {
		t.Errorf("结果不对：%+v", res)
	}
	if !res.When.After(time.Now().Add(-time.Hour)) {
		t.Errorf("时刻不对：%v", res.When)
	}
	// 过程写在日志里，理由是助手那会儿没有窗口能说话。
	logRaw, err := os.ReadFile(filepath.Join(home, "logs", "update", time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatalf("没有日志：%v", err)
	}
	if !strings.Contains(string(logRaw), "0.3.0") {
		t.Errorf("日志里看不出做的是哪一版：\n%s", logRaw)
	}
}

// 新版那份树是坏的：不能把能用的安装换掉，而且要把原因留在结果里。
func TestHelperRecordsFailure(t *testing.T) {
	home := tempHome(t)
	dir := filepath.Join(home, "cache", "update")
	// 解压目录在，但里面空空的——产物不完整。
	stage := filepath.Join(home, "解压出来的")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "安装位", cliExeName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("旧的"), 0o755); err != nil {
		t.Fatal(err)
	}

	claim, err := BeginApply()
	if err != nil {
		t.Fatal(err)
	}
	cmd := startHelper(t, Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: target, Kind: KindCLI})
	claim.Release()
	_ = cmd.Wait()

	if got := readFile(t, target); got != "旧的" {
		t.Errorf("坏的那份也换上去了：%q", got)
	}
	res, ok := LoadResult()
	if !ok {
		t.Fatal("失败时什么都没留下——那用户下次启动只会看到「什么都没发生」")
	}
	if res.OK {
		t.Error("明明是失败，结果里写着成功")
	}
	if !strings.Contains(res.Message, cliExeName) {
		t.Errorf("失败原因没说清缺的是什么：%q", res.Message)
	}
	// 回滚说的是「动过又放回去了」，这一次从头到尾没动手，所以它不是回滚。
	// 这两句话读的人拿到的是不同的判断：前者安装目录没变，后者只是变回来了。
	if res.RolledBack {
		t.Error("没动过安装目录，却说回滚了")
	}
}
