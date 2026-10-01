package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	systemScan = false
	os.Exit(m.Run())
}

// fakeSDKMan 在临时目录里铺一套假的 sdkman JDK，current 指向 current 参数给的那个版本。
func fakeSDKMan(t *testing.T, current string, versions ...string) string {
	t.Helper()
	home := t.TempDir()
	base := filepath.Join(home, ".sdkman", "candidates", "java")
	for _, v := range versions {
		bin := filepath.Join(base, v, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "java"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(current, filepath.Join(base, "current")); err != nil {
		t.Fatal(err)
	}
	return home
}

func newIn(home string) *Resolver {
	InvalidateDiscovery()
	r := New()
	r.home = home
	return r
}

// 工程要求 21、而 current 是 17：必须挑 21，否则编译报「无效的目标发行版: 21」。
func TestJavaFollowsProjectRequirement(t *testing.T) {
	home := fakeSDKMan(t, "17.0.12-oracle", "17.0.12-oracle", "21.0.1-tem", "21.0.9-oracle", "8.0.472-zulu")
	r := newIn(home)
	r.JavaMajor = "21"

	tool, err := r.Resolve(Java)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tool.Home, "21.0.9-oracle") {
		t.Errorf("挑到了 %s，想要同主版本里最高的 21.0.9-oracle", tool.Home)
	}
	if !strings.Contains(tool.Reason, "pom") || !strings.Contains(tool.Reason, "21") {
		t.Errorf("选择依据里应当写明是按 pom 要求挑的：%q", tool.Reason)
	}
	if tool.Warn != "" {
		t.Errorf("要求满足了不该有警告：%q", tool.Warn)
	}
}

// current 恰好就是要求的主版本时用 current；本机没有要求的版本时退回 current 并给出警告；
// 服务上指定的永远优先。
func TestJavaRequirementFallbacks(t *testing.T) {
	home := fakeSDKMan(t, "21.0.1-tem", "17.0.12-oracle", "21.0.1-tem", "21.0.9-oracle")

	r := newIn(home)
	r.JavaMajor = "21"
	if tool, _ := r.Resolve(Java); tool == nil || !strings.Contains(tool.Home, "21.0.1-tem") {
		t.Errorf("current 已经是 21，应当直接用它，实际 %+v", tool)
	}

	r = newIn(home)
	r.JavaMajor = "11"
	tool, _ := r.Resolve(Java)
	if tool == nil || !strings.Contains(tool.Home, "21.0.1-tem") || tool.Reason != "sdkman 当前版本" {
		t.Errorf("本机没有 11，应当退回 sdkman current，实际 %+v", tool)
	}
	if tool != nil && !strings.Contains(tool.Warn, "11") {
		t.Errorf("没满足项目要求时应当给出警告：%q", tool.Warn)
	}

	r = newIn(home)
	r.JavaMajor = "21"
	r.Overrides[Java] = "17.0.12-oracle"
	if tool, _ := r.Resolve(Java); tool == nil || !strings.Contains(tool.Home, "17.0.12-oracle") || tool.Reason != "服务上指定" {
		t.Errorf("服务上指定的版本应当优先，实际 %+v", tool)
	}
}
