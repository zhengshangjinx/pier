//go:build darwin

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeApp 在 dir 里造一个长得像 .app 的目录：够 checkBundle 认，也够 ditto 拷。
// 真的去解一份几十兆的产物没必要——这里要验的是换文件那一段的判断。
func makeApp(t *testing.T, dir, name, content string) string {
	t.Helper()
	app := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", guiExeName), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

// readGUI 读出一份 .app 里那个界面二进制现在是什么内容。
func readGUI(t *testing.T, app string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(app, "Contents", "MacOS", guiExeName))
	if err != nil {
		t.Fatalf("读不出 %s：%v", app, err)
	}
	return string(raw)
}

// 认安装位：从可执行文件的位置往上找出那个 .app；不是整包就是裸命令行那一种。
func TestDetectInstallDarwin(t *testing.T) {
	parent := t.TempDir()
	app := makeApp(t, parent, "Pier.app", "旧的")
	exe := filepath.Join(app, "Contents", "MacOS", cliExeName)

	inst, ok := detectInstall(exe)
	if !ok {
		t.Fatal("整包没认出来")
	}
	if inst.Kind != KindBundle {
		t.Errorf("认成了 %v，该是整包", inst.Kind)
	}
	if inst.Target != app || inst.Relaunch != app {
		t.Errorf("安装位认成了 %+v", inst)
	}
	// 换的是整个 bundle，所以要在它的上一级目录里先放一份新的再改名——
	// /Applications 对非管理员账号就可能是只读的。
	if got := writableDir(inst); got != parent {
		t.Errorf("该检查 %q 写不写得动，问的是 %q", parent, got)
	}

	bare := filepath.Join(t.TempDir(), cliExeName)
	if err := os.WriteFile(bare, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst, ok = detectInstall(bare)
	if !ok {
		t.Fatal("裸命令行那份没认出来")
	}
	if inst.Kind != KindCLI || inst.Target != bare {
		t.Errorf("认成了 %+v", inst)
	}
}

// 裸命令行那份换的就是它自己：一个文件，改名过去就完了。
func TestApplyReplacesBareCLI(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "pier-0.3.0-macos-universal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "pier-0.3.0-macos-universal", cliExeName), []byte("新的"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, cliExeName)
	if err := os.WriteFile(target, []byte("旧的"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: target, Kind: KindCLI}, nil)
	if !res.OK {
		t.Fatalf("没换成：%s", res.Message)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "新的" {
		t.Errorf("换过去的是 %q", raw)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("执行位丢了：%v", fi.Mode())
	}
}

// 整包整个换：旧的先挪开、新的拷进去、成了才删旧的。ditto 走的是真的那个。
func TestApplyBundleReplacesApp(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "安装位")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	target := makeApp(t, parent, "Pier.app", "旧的")

	// 产物里的形状就是「解压出来一份 Pier.app」（见 package.sh），
	// 那份 .app 里装的是新的。
	stage := filepath.Join(dir, "stage")
	makeApp(t, stage, "Pier.app", "新的")

	res := Apply(Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: target, Kind: KindBundle}, nil)
	if !res.OK {
		t.Fatalf("没换成：%s", res.Message)
	}
	if got := readGUI(t, target); got != "新的" {
		t.Errorf("换过去的是 %q，该是新的那份", got)
	}
	if _, err := os.Stat(target + oldSuffix); !os.IsNotExist(err) {
		t.Error("旧的那份还留在旁边")
	}
	if err := checkBundle(target); err != nil {
		t.Errorf("换完之后这份包不完整：%v", err)
	}
}

// 一份坏包不能把能用的安装换成打不开的东西：动手之前先验一遍。
func TestApplyBundleRefusesBadSource(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "安装位")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	target := makeApp(t, parent, "Pier.app", "旧的")

	// 产物里那个 .app 缺了界面二进制。
	stage := filepath.Join(dir, "stage")
	bad := filepath.Join(stage, "Pier.app", "Contents")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "Info.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: target, Kind: KindBundle}, nil)
	if res.OK {
		t.Fatal("坏的包也换上去了")
	}
	if !strings.Contains(res.Message, guiExeName) {
		t.Errorf("失败原因没点出缺的是什么：%q", res.Message)
	}
	if got := readGUI(t, target); got != "旧的" {
		t.Errorf("安装位被动了：现在是 %q", got)
	}
}

// 拷到一半失败要能把旧的搬回来。这一步直接调那段回滚，是因为让 ditto 真失败
// 得先把安装目录弄成写不动的——那样连「旧的挪开」都做不成，走不到这里。
func TestRollbackBundle(t *testing.T) {
	dir := t.TempDir()
	target := makeApp(t, dir, "Pier.app", "旧的")
	old := target + oldSuffix
	// 现在的样子是「旧的已经挪开、新的拷了一半」。
	if err := os.Rename(target, old); err != nil {
		t.Fatal(err)
	}
	makeApp(t, dir, "Pier.app", "拷了一半")

	cause := errors.New("拷贝新的 .app 失败")
	rolled, err := rollbackBundle(Plan{Target: target}, old, cause, NewLogger(nil))
	if !rolled {
		t.Error("说没回滚，其实回了")
	}
	if !errors.Is(err, cause) {
		t.Errorf("原因丢了：%v", err)
	}
	if got := readGUI(t, target); got != "旧的" {
		t.Errorf("挪回来的不是旧的那份：%q", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("旧的挪回来了，旁边还留着一份")
	}
}

// 解压出来的树里该有且只有一个 .app。
func TestFindBundle(t *testing.T) {
	stage := t.TempDir()
	if _, err := findBundle(stage); err == nil {
		t.Error("空的解压目录也认了")
	}
	makeApp(t, stage, "Pier.app", "x")
	got, err := findBundle(stage)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(stage, "Pier.app") {
		t.Errorf("找到的是 %q", got)
	}
	makeApp(t, stage, "另外一个.app", "x")
	if _, err := findBundle(stage); err == nil {
		t.Error("有两个 .app 时不该猜一个")
	}
}

// 上个版本的 Pier 把自己的名字留在旁边过（.old），下一次要能认出来清掉；
// 但如果那个名字上是别的东西，宁可停下来说清楚，也不去删。
func TestApplyBundleRefusesForeignOld(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "安装位")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	target := makeApp(t, parent, "Pier.app", "旧的")
	if err := os.WriteFile(target+oldSuffix, []byte("用户自己建的文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	makeApp(t, stage, "Pier.app", "新的")

	res := Apply(Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: target, Kind: KindBundle}, nil)
	if res.OK {
		t.Fatal("来路不明的文件也删了")
	}
	if !strings.Contains(res.Message, oldSuffix) {
		t.Errorf("失败原因没说清挡住的是什么：%q", res.Message)
	}
	if _, err := os.Stat(target + oldSuffix); err != nil {
		t.Errorf("那个文件被动了：%v", err)
	}
	if got := readGUI(t, target); got != "旧的" {
		t.Errorf("安装位被动了：%q", got)
	}
}
