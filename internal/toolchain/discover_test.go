package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// 不用 sdkman 的人也要找得到 JDK：IntelliJ 下载的、asdf 装的都算；
// 版本与发行商读 release 文件，不靠目录名去猜。
func TestDiscoverJavaBeyondSDKMan(t *testing.T) {
	home := t.TempDir()
	idea := filepath.Join(home, "Library", "Java", "JavaVirtualMachines", "temurin-17", "Contents", "Home")
	writeExec(t, filepath.Join(idea, "bin", "java"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(idea, "release"), []byte("IMPLEMENTOR=\"Eclipse Adoptium\"\nJAVA_VERSION=\"17.0.11\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(home, ".asdf", "installs", "java", "zulu-8", "bin", "java"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(home, ".asdf", "installs", "java", "zulu-8", "release"), []byte("IMPLEMENTOR=\"Azul Systems, Inc.\"\nJAVA_VERSION=\"1.8.0_402\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	InvalidateDiscovery()
	sdks := discoverIn(home, Java, nil)
	if len(sdks) != 2 {
		t.Fatalf("应当扫到 2 个 JDK，实际 %+v", sdks)
	}
	if sdks[0].Version != "17.0.11" || sdks[0].Vendor != "Temurin" || sdks[0].Source != "IntelliJ 下载" {
		t.Errorf("第一个应当是 IntelliJ 下载的 Temurin 17.0.11，实际 %+v", sdks[0])
	}
	if sdks[1].Major() != "8" || sdks[1].Vendor != "Zulu" {
		t.Errorf("1.8.0_402 的主版本应当是 8、发行商 Zulu，实际 %+v", sdks[1])
	}

	// 手动添加：选到 .jdk 包或 Contents/Home 都认，同一路径不重复。
	manual := filepath.Dir(filepath.Dir(idea))
	InvalidateDiscovery()
	if got := discoverIn(home, Java, []string{manual}); len(got) != 2 || !got[0].Manual {
		t.Errorf("手动添加已扫到的 JDK 应当合并成一条并标记为手动：%+v", got)
	}
}

// Python：项目自己的 .venv 优先于一切（依赖装在里面）；没有 venv 时按 .python-version 选。
func TestPythonPrefersProjectVenv(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	writeExec(t, filepath.Join(home, ".pyenv", "versions", "3.11.9", "bin", "python3"), "#!/bin/sh\n")
	writeExec(t, filepath.Join(home, ".pyenv", "versions", "3.12.4", "bin", "python3"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(proj, ".python-version"), []byte("3.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newIn(home)
	r.ProjectDir = proj
	if tool, _ := r.Resolve(Python); tool == nil || !strings.Contains(tool.Bin, "3.11.9") || !strings.Contains(tool.Reason, ".python-version") {
		t.Errorf("应当按 .python-version 选 3.11.9，实际 %+v", tool)
	}

	venv := filepath.Join(proj, ".venv")
	writeExec(t, filepath.Join(venv, "bin", "python3"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = /x\nversion = 3.11.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = newIn(home)
	r.ProjectDir = proj
	tool, _ := r.Resolve(Python)
	if tool == nil || !strings.HasPrefix(tool.Bin, venv) || tool.Reason != "项目虚拟环境" {
		t.Fatalf("有 .venv 时应当用它，实际 %+v", tool)
	}
	found := false
	for _, kv := range tool.Env {
		found = found || kv == "VIRTUAL_ENV="+venv
	}
	if !found {
		t.Errorf("用虚拟环境时应当设置 VIRTUAL_ENV：%v", tool.Env)
	}
}

// Node：按 .nvmrc 选；package.json 的 engines 下限也认；都没有时用 nvm 的默认版本。
func TestNodeFollowsProjectDeclaration(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	for _, v := range []string{"v18.20.4", "v20.15.1", "v22.20.0"} {
		writeExec(t, filepath.Join(home, ".nvm", "versions", "node", v, "bin", "node"), "#!/bin/sh\n")
	}
	writeExec(t, filepath.Join(home, ".nvm", "alias", "default"), "20\n")

	r := newIn(home)
	r.ProjectDir = proj
	if tool, _ := r.Resolve(Node); tool == nil || tool.Version != "20.15.1" || tool.Reason != "nvm 默认版本" {
		t.Errorf("没有项目声明时应当用 nvm 默认版本 20，实际 %+v", tool)
	}

	if err := os.WriteFile(filepath.Join(proj, ".nvmrc"), []byte("v18\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = newIn(home)
	r.ProjectDir = proj
	if tool, _ := r.Resolve(Node); tool == nil || tool.Version != "18.20.4" || !strings.Contains(tool.Reason, ".nvmrc") {
		t.Errorf("应当按 .nvmrc 选 18，实际 %+v", tool)
	}

	os.Remove(filepath.Join(proj, ".nvmrc"))
	if err := os.WriteFile(filepath.Join(proj, "package.json"), []byte(`{"engines":{"node":">=21"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r = newIn(home)
	r.ProjectDir = proj
	if tool, _ := r.Resolve(Node); tool == nil || tool.Version != "22.20.0" {
		t.Errorf("engines >=21 时默认的 20 不满足，应当选 22，实际 %+v", tool)
	}
}

// Maven：项目带 mvnw 就用它；Maven 注入的 JAVA_HOME 必须和选中的 JDK 是同一个。
func TestMavenPrefersWrapperAndSharesJDK(t *testing.T) {
	home := fakeSDKMan(t, "21.0.9-oracle", "21.0.9-oracle")
	proj := t.TempDir()
	writeExec(t, filepath.Join(proj, "mvnw"), "#!/bin/sh\n")

	r := newIn(home)
	r.ProjectDir = proj
	mvn, err := r.Resolve(Maven)
	if err != nil {
		t.Fatal(err)
	}
	if mvn.Bin != filepath.Join(proj, "mvnw") {
		t.Errorf("项目有 mvnw 时应当用它，实际 %s", mvn.Bin)
	}
	jdk, _ := r.Resolve(Java)
	if jdk == nil || len(mvn.Env) == 0 || mvn.Env[0] != "JAVA_HOME="+jdk.Home {
		t.Errorf("Maven 的 JAVA_HOME 应当是选中的 JDK：mvn=%v jdk=%+v", mvn.Env, jdk)
	}
}

// 全局默认：没有项目要求时用它；项目要求它满足不了时让位给满足要求的。
func TestGlobalDefault(t *testing.T) {
	home := fakeSDKMan(t, "21.0.9-oracle", "17.0.12-oracle", "21.0.9-oracle")
	def := filepath.Join(home, ".sdkman", "candidates", "java", "17.0.12-oracle")

	r := newIn(home)
	r.Defaults = map[Kind]string{Java: def}
	if tool, _ := r.Resolve(Java); tool == nil || tool.Home != def || tool.Reason != "全局默认" {
		t.Errorf("应当用全局默认 17，实际 %+v", tool)
	}

	r = newIn(home)
	r.Defaults = map[Kind]string{Java: def}
	r.JavaMajor = "21"
	if tool, _ := r.Resolve(Java); tool == nil || !strings.Contains(tool.Home, "21.0.9") {
		t.Errorf("pom 要 21 时全局默认的 17 应当让位，实际 %+v", tool)
	}
}

// go.mod 要求更新的 Go 时，go 命令会把整套 SDK 下载进模块缓存。那是真能跑的一个 Go，
// 但以前根本不扫这里——于是项目依赖 go 1.25 的人，在「SDK 管理」里看到的是空的 Go 分组。
func TestDiscoverGoToolchainCache(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "go", "pkg", "mod", "golang.org", "toolchain@v0.0.1-go1.25.4.darwin-arm64")
	writeExec(t, filepath.Join(dir, "bin", "go"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("go1.25.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	InvalidateDiscovery()
	sdks := discoverIn(home, Go, nil)
	if len(sdks) != 1 {
		t.Fatalf("模块缓存里的工具链应当被扫到，实际 %+v", sdks)
	}
	if sdks[0].Version != "1.25.4" || sdks[0].Source != "Go 工具链缓存" {
		t.Errorf("版本与来源读错了：%+v", sdks[0])
	}
}

// 手动指定 Go 时，选到哪一级都要认：GOROOT、bin 目录、bin 里那个可执行文件。
//
// 选到可执行文件时还要先解开软链——Homebrew 的 /opt/homebrew/bin/go 指向 Cellar 里的真身，
// 不解开的话上两级会落到 /opt/homebrew，解出来一个没有版本号的假 GOROOT。
func TestInspectGoAcceptsAnyLevel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go1.25.4")
	writeExec(t, filepath.Join(root, "bin", "go"), "#!/bin/sh\n")
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("go1.25.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 造一个 Homebrew 那样的 bin 目录：里面是可执行文件的软链，根目录在别处。
	brewBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(brewBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "bin", "go"), filepath.Join(brewBin, "go")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{root, filepath.Join(root, "bin"), filepath.Join(root, "bin", "go"), filepath.Join(brewBin, "go")} {
		s, err := Inspect(Go, p)
		if err != nil {
			t.Errorf("选了 %s 应当认出来：%v", p, err)
			continue
		}
		// 比真实路径：macOS 的临时目录本身是个软链（/var → /private/var）。
		if realPath(s.Home) != realPath(root) {
			t.Errorf("选了 %s 解出来的根目录是 %s，应当是 %s", p, s.Home, root)
		}
		if s.Version != "1.25.4" {
			t.Errorf("选了 %s 读出来的版本是 %q", p, s.Version)
		}
	}
}
