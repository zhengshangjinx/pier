//go:build linux

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 装出来的形状是两份可执行文件并排放在一个目录里（install.sh 装到 ~/.local/bin），
// 认的依据就是这个，不写死那个路径。
func TestDetectInstallLinux(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, cliExeName)
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := detectInstall(exe); ok {
		t.Error("旁边没有 pier-gui 也认了")
	}
	gui := filepath.Join(dir, guiExeName)
	if err := os.WriteFile(gui, []byte("x"), 0o755); err != nil {
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
}

// 换文件那一步跑的是解压出来的 install.sh：升级就是重跑一遍它。
// 这里用一个只把两份可执行文件挪到别处的假脚本，验的是「有没有把这一整套
// 交给脚本、脚本失败的话说不说得清楚」。
func TestApplyLinuxRunsInstallScript(t *testing.T) {
	dir := t.TempDir()
	installDir := filepath.Join(dir, "装到这儿")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stage := filepath.Join(dir, "stage", "pier-0.3.0-linux-amd64")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("新的 "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\ncp \"$(dirname \"$0\")/" + cliExeName + "\" " + installDir + "/\n" +
		"cp \"$(dirname \"$0\")/" + guiExeName + "\" " + installDir + "/\n"
	if err := os.WriteFile(filepath.Join(stage, "install.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{
		Version: "0.3.0",
		Dir:     filepath.Join(dir, "cache"),
		Stage:   filepath.Dir(stage),
		Target:  installDir,
		Kind:    KindCLI,
	}, nil)
	if !res.OK {
		t.Fatalf("没换成：%s", res.Message)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		raw, err := os.ReadFile(filepath.Join(installDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "新的 "+name {
			t.Errorf("%s 换过去的是 %q", name, raw)
		}
	}
}

// 脚本失败时不能只说一句「失败了」：它是一串就地覆盖，跑到一半停下留下的是
// 半新半旧的安装，用户得知道现在是什么状态、下一步做什么。
func TestApplyLinuxReportsScriptFailure(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\necho '这一步没成' >&2\nexit 3\n"
	if err := os.WriteFile(filepath.Join(stage, "install.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res := Apply(Plan{Version: "0.3.0", Dir: filepath.Join(dir, "cache"), Stage: stage, Target: t.TempDir(), Kind: KindCLI}, nil)
	if res.OK {
		t.Fatal("脚本失败了也当成了成功")
	}
	for _, want := range []string{"install.sh", "手工运行"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("失败原因里没有 %q：%s", want, res.Message)
		}
	}
}

// 产物里少一份就不能动手：换过去之后才发现，留下的是一份打不开的安装。
func TestApplyLinuxRefusesIncompleteStage(t *testing.T) {
	dir := t.TempDir()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "install.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := Apply(Plan{Version: "0.3.0", Dir: dir, Stage: stage, Target: t.TempDir(), Kind: KindCLI}, nil)
	if res.OK {
		t.Fatal("缺东西的产物也做了")
	}
	if !strings.Contains(res.Message, cliExeName) {
		t.Errorf("没说清缺的是什么：%s", res.Message)
	}
}
