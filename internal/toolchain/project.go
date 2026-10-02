package toolchain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zhengshangjinx/pier/internal/execpath"
)

// 本文件回答「这个项目自己声明要哪个版本」。
//
// IDEA 能「打开就跑」，很大程度上是因为它尊重项目里的声明：pom 的 Java 版本、Maven Wrapper、
// .nvmrc、虚拟环境。Pier 不读这些，就只能拿全局默认去碰运气——拿 17 编译要求 21 的工程、
// 拿系统 Python 跑装在 .venv 里的依赖，都是这么来的。
//
// Java 的要求来自 pom，由 config 包读出来传进 Resolver.JavaMajor（pom 的解析在 config 里）；
// 其余几种只看项目目录里的文件，放在这里。

// Want 是项目对某个 SDK 版本的要求。
type Want struct {
	// Prefix 是版本号前缀（按段比较）：20 匹配 20.x.y，3.12 匹配 3.12.x。
	Prefix string
	// Min 为真表示「至少这个版本」（engines 的 >=、go.mod 的 go 指令）。
	Min bool
	// From 说明这条要求写在哪（给人看的依据）。
	From string
}

func (w Want) empty() bool { return w.Prefix == "" }

// matches 判断某个版本是否满足要求。
func (w Want) matches(version string) bool {
	if w.empty() {
		return true
	}
	have, want := versionParts(version), versionParts(w.Prefix)
	if len(have) == 0 || len(want) == 0 {
		return false
	}
	if w.Min {
		return !versionLess(version, w.Prefix)
	}
	if len(have) < len(want) {
		return false
	}
	for i := range want {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

// String 是给人看的要求，如「20」「≥ 18」。
func (w Want) String() string {
	if w.Min {
		return "≥ " + w.Prefix
	}
	return w.Prefix
}

var firstVersionRe = regexp.MustCompile(`(>=|\^|~)?\s*v?(\d+(?:\.\d+)*)`)

// NodeWant 读项目要求的 Node 版本：.nvmrc → .node-version → package.json 的 volta.node → engines.node。
// lts/*、node、stable 这类别名不是具体版本，不当成要求。
func NodeWant(dir string) Want {
	for _, name := range []string{".nvmrc", ".node-version"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]), "v")
		if m := firstVersionRe.FindStringSubmatch(v); m != nil && strings.HasPrefix(v, m[2][:1]) {
			return Want{Prefix: m[2], From: name}
		}
	}
	var pkg struct {
		Volta   struct{ Node string } `json:"volta"`
		Engines struct{ Node string } `json:"engines"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil && json.Unmarshal(raw, &pkg) == nil {
		if m := firstVersionRe.FindStringSubmatch(pkg.Volta.Node); m != nil {
			return Want{Prefix: m[2], From: "package.json volta.node"}
		}
		if m := firstVersionRe.FindStringSubmatch(pkg.Engines.Node); m != nil {
			w := Want{Prefix: m[2], From: "package.json engines.node"}
			switch m[1] {
			case ">=":
				w.Min = true
			case "^":
				w.Prefix = strings.SplitN(m[2], ".", 2)[0]
			}
			return w
		}
	}
	return Want{}
}

// PythonVenv 找项目自己的虚拟环境（.venv / venv / env，以 pyvenv.cfg 为准）。
// 找到的话它就是这个项目的解释器，别的一概不用看——依赖都装在里面。
func PythonVenv(dir string) (SDK, bool) {
	for _, name := range []string{".venv", "venv", "env"} {
		root := filepath.Join(dir, name)
		cfg := readKV(filepath.Join(root, "pyvenv.cfg"))
		if len(cfg) == 0 {
			continue
		}
		for _, bin := range []string{"python3", "python"} {
			p := filepath.Join(root, "bin", bin)
			if execpath.Is(p) {
				v := cfg["version"]
				if v == "" {
					v = cfg["version_info"]
				}
				return SDK{Kind: Python, Home: root, Bin: p, Version: leadingVersion(v), Source: "项目虚拟环境 " + name}, true
			}
		}
	}
	return SDK{}, false
}

// PythonWant 读 pyenv 的 .python-version。
func PythonWant(dir string) Want {
	raw, err := os.ReadFile(filepath.Join(dir, ".python-version"))
	if err != nil {
		return Want{}
	}
	v := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	if m := firstVersionRe.FindStringSubmatch(v); m != nil && strings.HasPrefix(v, m[2][:1]) {
		return Want{Prefix: m[2], From: ".python-version"}
	}
	return Want{}
}

var goDirectiveRe = regexp.MustCompile(`(?m)^\s*(toolchain\s+go|go\s+)(\d+(?:\.\d+)*)`)

// GoWant 读 go.mod 的 toolchain / go 指令。go 指令是「至少这个版本」；
// GOTOOLCHAIN=auto 下本机版本不够时 go 命令会自己去下，所以它只影响「优先挑哪个」。
func GoWant(dir string) Want {
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return Want{}
	}
	var w Want
	for _, m := range goDirectiveRe.FindAllStringSubmatch(string(raw), -1) {
		if strings.HasPrefix(m[1], "toolchain") {
			return Want{Prefix: m[2], Min: true, From: "go.mod toolchain"}
		}
		w = Want{Prefix: m[2], Min: true, From: "go.mod"}
	}
	return w
}

// MavenWrapper 返回项目自带的 mvnw。IDEA 默认优先用它：它钉住了项目要的 Maven 版本，
// 第一次运行时自己去下，比本机随便哪个 mvn 都可靠。
func MavenWrapper(dir string) string {
	p := filepath.Join(dir, "mvnw")
	if execpath.Is(p) {
		return p
	}
	return ""
}
