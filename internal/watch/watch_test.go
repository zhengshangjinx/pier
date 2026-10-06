package watch

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, root string, rel string, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
	return p
}

// scanOK 扫一遍并要求没有错误。
func scanOK(t *testing.T, tr *Tree) []string {
	t.Helper()
	changed, err := tr.Scan()
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	return changed
}

// TestScanFirstPassIsBaseline 钉着第一趟只建立基线。
//
// 刚起的服务不该因为「现在开始看了」就重启一次——那一下会把用户手里
// 正在跑的现场清掉，而他什么都没改。
func TestScanFirstPassIsBaseline(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main")
	tr := New(dir, nil, nil)

	if !tr.Baseline() {
		t.Error("刚建出来的树不是基线状态")
	}
	if changed := scanOK(t, tr); len(changed) != 0 {
		t.Errorf("第一趟报出 %v，想要空", changed)
	}
	if tr.Baseline() {
		t.Error("扫过一趟之后还是基线状态")
	}
	if changed := scanOK(t, tr); len(changed) != 0 {
		t.Errorf("什么都没改却报出 %v", changed)
	}
}

// TestScanReportsAddModifyDelete 是这一包的主用例：新增、改动、删除各报一次，
// 报出来的名字是相对路径、用 `/` 分隔（清单里的模式就是这么写的）。
func TestScanReportsAddModifyDelete(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main")
	write(t, dir, "src/app.go", "package app")
	write(t, dir, "doomed.go", "package doomed")
	tr := New(dir, []string{"*.go"}, nil)
	scanOK(t, tr)

	write(t, dir, "src/app.go", "package app // 改过了，比原来长")
	write(t, dir, "src/new.go", "package new")
	if err := os.Remove(filepath.Join(dir, "doomed.go")); err != nil {
		t.Fatalf("删文件失败：%v", err)
	}

	want := []string{"doomed.go", "src/app.go", "src/new.go"}
	if got := scanOK(t, tr); !reflect.DeepEqual(got, want) {
		t.Errorf("变化 = %v，想要 %v", got, want)
	}
	// 报过之后回到安静：下一个 400ms 的窗口不该再动手。
	if got := scanOK(t, tr); len(got) != 0 {
		t.Errorf("同一批变化被报了两次：%v", got)
	}
}

// TestScanHonoursInclude 钉着模式表真的在筛。
func TestScanHonoursInclude(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main")
	tr := New(dir, []string{"*.py"}, nil)
	scanOK(t, tr)

	write(t, dir, "a.py", "print(1)")
	write(t, dir, "b.go", "package b")
	if got := scanOK(t, tr); !reflect.DeepEqual(got, []string{"a.py"}) {
		t.Errorf("变化 = %v，想要只有 a.py", got)
	}
}

// TestScanSkipsNoisyDirectories 钉着产物目录整个不看。
//
// 这是一条防自激的底线：编译产物就落在服务目录里，而重启要重新编译一遍——
// 少排掉一个，那个服务就会以「编译→产物落地→再重启」一直转下去。
func TestScanSkipsNoisyDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main")
	write(t, dir, "node_modules/pkg/index.js", "module.exports = 1")
	write(t, dir, "target/classes/App.class", "…")
	write(t, dir, ".git/HEAD", "ref: refs/heads/main")
	tr := New(dir, nil, nil)
	scanOK(t, tr)

	// 这些目录里发生什么都不该被看见，包括新建与改动。
	write(t, dir, "node_modules/pkg/other.js", "module.exports = 2")
	write(t, dir, "target/classes/App.class", "…又编译了一遍，比刚才长")
	write(t, dir, ".git/index", "…")
	if got := scanOK(t, tr); len(got) != 0 {
		t.Errorf("产物目录里的变化被算进来了：%v", got)
	}

	// 而源码改了照样看得见——排的是那几个目录名，不是「不再监视」。
	write(t, dir, "main.go", "package main // 改过了，长一点")
	if got := scanOK(t, tr); !reflect.DeepEqual(got, []string{"main.go"}) {
		t.Errorf("源码改动没被看见：%v", got)
	}
}

// TestScanSkipsOutsideTrees 钉着 Pier 自己的那几棵树不看。
//
// 服务的日志与编译产物写在数据目录里（~/.pier/logs、~/.pier/cache/bin），
// 每次启动都在写。目录选得近的时候它们就在被监视的树里，算进来的话
// 每个配了监视的服务都会自己重启自己。
func TestScanSkipsOutsideTrees(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, ".pier")
	write(t, dir, "main.go", "package main")
	write(t, out, "logs/svc/2026-10-06.log", "启动：…")
	tr := New(dir, nil, []string{out})
	scanOK(t, tr)

	write(t, out, "logs/svc/2026-10-06.log", "启动：…又跑了一次，比刚才长")
	if got := scanOK(t, tr); len(got) != 0 {
		t.Errorf("数据目录里的变化被算进来了：%v", got)
	}
}

// TestScanSurvivesMissingRoot 钉着目录不在了也不炸。
//
// 服务目录被挪走、被删掉是常事（用户正在整理仓库），那时该做的是
// 什么都不报，而不是让整条巡检协程带着一次 fatal 走掉。
func TestScanSurvivesMissingRoot(t *testing.T) {
	tr := New(filepath.Join(t.TempDir(), "不在"), nil, nil)
	if _, err := tr.Scan(); err != nil {
		t.Errorf("根目录不在时返回了错误：%v", err)
	}
}
