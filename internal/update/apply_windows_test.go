//go:build windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 装出来的形状是两份 exe 并排放在一个目录里（见 install.ps1），认的依据就是这个。
// 从解压出来的文件夹里直接跑的那种认不出：那是临时目录，替换它没有意义。
func TestDetectInstallWindows(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, cliExeName)
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectInstall(exe); ok {
		t.Error("旁边没有 pier-gui.exe 也认了")
	}
	gui := filepath.Join(dir, guiExeName)
	if err := os.WriteFile(gui, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst, ok := detectInstall(exe)
	if !ok {
		t.Fatal("装好的那一份没认出来")
	}
	if inst.Kind != KindCLI || inst.Target != dir || inst.Relaunch != gui {
		t.Errorf("认成了 %+v", inst)
	}
	if got := writableDir(inst); got != dir {
		t.Errorf("该检查 %q 写不写得动，问的是 %q", dir, got)
	}

	// 压缩包里的形状：pier.exe 在 bin\ 里，旁边没有 pier-gui.exe。
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, cliExeName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectInstall(filepath.Join(bin, cliExeName)); ok {
		t.Error("解压出来还没装的那一份也认了")
	}
}

// 两份 exe 换掉，别的什么都不动：PATH、开始菜单在第一安装时就写好了，
// 再跑一遍安装器等于把那几件事重做一次，每一步都是新的失败可能。
func TestApplyWindowsReplacesBothExes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Programs", "Pier")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if err := os.WriteFile(filepath.Join(target, name), []byte("旧的 "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 装完还剩着上一次更新留下的旧文件。
	stale := filepath.Join(target, cliExeName+oldPrefix+"20260101-000000")
	if err := os.WriteFile(stale, []byte("上一次留下的"), 0o644); err != nil {
		t.Fatal(err)
	}

	stage := filepath.Join(dir, "stage")
	inner := filepath.Join(stage, "Pier-0.3.0-windows-amd64")
	if err := os.MkdirAll(filepath.Join(inner, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, guiExeName), []byte("新的 "+guiExeName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "bin", cliExeName), []byte("新的 "+cliExeName), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{Version: "0.3.0", Dir: filepath.Join(dir, "cache"), Stage: stage, Target: target, Kind: KindCLI}, nil)
	if !res.OK {
		t.Fatalf("没换成：%s", res.Message)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		raw, err := os.ReadFile(filepath.Join(target, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "新的 "+name {
			t.Errorf("%s 换过去的是 %q", name, raw)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("上一次留下的旧文件没清掉")
	}
	leftovers, err := filepath.Glob(filepath.Join(target, "*"+oldPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("换完之后还留着：%v", leftovers)
	}
}

// 产物不完整时一个文件都不许动。Windows 上换文件是「旧的先改名挪开」，
// 挪开了才发现新的不能用，安装目录里就空了。
func TestApplyWindowsRefusesIncompleteStage(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Pier")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if err := os.WriteFile(filepath.Join(target, name), []byte("旧的 "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, guiExeName), []byte("新的"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{Version: "0.3.0", Dir: filepath.Join(dir, "cache"), Stage: stage, Target: target, Kind: KindCLI}, nil)
	if res.OK {
		t.Fatal("缺了 pier.exe 也做了")
	}
	if !strings.Contains(res.Message, cliExeName) {
		t.Errorf("没说清缺的是什么：%s", res.Message)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if raw, err := os.ReadFile(filepath.Join(target, name)); err != nil || string(raw) != "旧的 "+name {
			t.Errorf("安装目录被动了：%s 现在是 %q（%v）", name, raw, err)
		}
	}
}

// Windows 上换不了正在运行的自己，所以命令行那条路要交给助手。
func TestWindowsCannotReplaceInPlace(t *testing.T) {
	if CanReplaceInPlace() {
		t.Error("说能当场换掉自己，可 Windows 上正在运行的 exe 是锁着的")
	}
}
