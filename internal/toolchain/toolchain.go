// Package toolchain 在「不依赖交互式 shell profile」的前提下定位各语言工具链。
//
// 背景：本机 shell profile 在 GVM_ROOT 处报错中断，之后的 sdkman / nvm 初始化从未执行，
// 于是已装好的 java、mvn 都不在 PATH 上——这正是这些项目只能在 IDEA 里启动的原因
// （IDEA 不读用户 profile，直接用自带的 JBR）。所以这里按「配置覆盖 → PATH → 已知安装位置」
// 的顺序自行解析，并产出运行该工具所需的环境变量，让命令行具备与 IDEA 同等的能力。
//
// 本机有哪些 SDK 见 discover.go，项目自己声明要哪个版本见 project.go，怎么在其中选一个见 Resolver。
package toolchain

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zhengshangjinx/pier/internal/execpath"
)

// Kind 是需要解析的工具类别。
type Kind string

const (
	Go     Kind = "go"
	Java   Kind = "java"
	Maven  Kind = "maven"
	Node   Kind = "node"
	Pnpm   Kind = "pnpm"
	Python Kind = "python"
)

// AllKinds 是 doctor 的检查清单，顺序即展示顺序。
var AllKinds = []Kind{Go, Java, Maven, Node, Pnpm, Python}

// Tool 是一次解析结果。
type Tool struct {
	Kind Kind
	// Bin 是可执行文件绝对路径；Home 对 java/maven/go 是安装根目录，对 node/pnpm/python 是 bin 目录。
	Bin  string
	Home string
	// Version / Vendor 取自扫描到的 SDK，给界面和日志显示。
	Version string
	Vendor  string
	// Source 是这个 SDK 从哪被发现的（sdkman、Homebrew、系统 JDK 目录…）。
	Source string
	// Reason 是为什么选了它（服务上指定、按 pom 要求的 Java 21、全局默认…）。
	Reason string
	// Warn 非空表示项目的要求没能满足、退而求其次，界面要把它标出来。
	Warn string
	// Env 是运行该工具需注入的环境变量（KEY=VALUE）；Path 是要前置到 PATH 的目录。
	// PATH 单独放：几套工具链都要改 PATH，混在 Env 里按「同名覆盖」合并，后一个会把前一个冲掉。
	Env  []string
	Path []string
}

// Label 是给人看的一行：「Java 21.0.9 · Oracle」。
func (t *Tool) Label() string {
	s := SDK{Kind: t.Kind, Version: t.Version, Vendor: t.Vendor}
	return kindName(t.Kind) + " " + s.Label()
}

// Resolver 为一个服务解析工具链，结果按 Kind 缓存。
//
// 选择顺序（每一步都会写进 Tool.Reason，界面和启动日志里都看得到）：
//  1. 服务上指定的（Overrides，界面「专属设置」里选的）；
//  2. 项目自己声明的：pom 的 Java 版本、mvnw、.nvmrc / engines、.venv / .python-version、go.mod；
//  3. 全局默认（Defaults，「SDK 管理」页里设的）；
//  4. 本机兜底：sdkman / nvm 的当前版本、PATH 上的、版本最高的。
//
// 项目有要求但本机没有满足的版本时，照样往下选一个能用的，同时在 Warn 里说清楚。
type Resolver struct {
	Overrides map[Kind]string
	// JavaMajor 是项目要求的 Java 主版本（如 "21"，由 config 包从 pom 里读出来）。
	JavaMajor string
	// ProjectDir 是服务目录，用来读 .nvmrc、.venv、mvnw、go.mod 这些项目声明。
	ProjectDir string
	// Manual 是「SDK 管理」里手动添加的 SDK 路径；Defaults 是各类的全局默认（SDK 路径）。
	Manual   map[Kind][]string
	Defaults map[Kind]string
	home     string
	cache    map[Kind]*Tool
	errs     map[Kind]error
}

func New() *Resolver {
	h, _ := os.UserHomeDir()
	return &Resolver{Overrides: map[Kind]string{}, home: h, cache: map[Kind]*Tool{}, errs: map[Kind]error{}}
}

// Resolve 返回指定类别的工具；结果按 Kind 缓存，失败也缓存以免重复探测。
func (r *Resolver) Resolve(k Kind) (*Tool, error) {
	if t, seen := r.cache[k]; seen {
		if t == nil {
			return nil, r.errs[k]
		}
		return t, nil
	}
	t, err := r.resolve(k)
	r.cache[k] = t
	if err != nil {
		r.cache[k], r.errs[k] = nil, err
		return nil, err
	}
	return t, nil
}

// ResolveAll 返回所有类别的解析结果，缺失的类别以 nil 占位而不中断。
func (r *Resolver) ResolveAll() map[Kind]*Tool {
	out := make(map[Kind]*Tool, len(AllKinds))
	for _, k := range AllKinds {
		t, err := r.Resolve(k)
		if err != nil {
			continue
		}
		out[k] = t
	}
	return out
}

func (r *Resolver) resolve(k Kind) (*Tool, error) {
	if k == Pnpm {
		return r.resolvePnpm()
	}
	sdks := discoverIn(r.home, k, r.Manual[k])

	if v := strings.TrimSpace(r.Overrides[k]); v != "" {
		s, err := r.pick(k, v, sdks)
		if err != nil {
			return nil, fmt.Errorf("服务上指定的 %s 不可用：%w", kindName(k), err)
		}
		return r.tool(s, "服务上指定"), nil
	}

	// 项目自带的、不需要去匹配版本的：Maven Wrapper 与虚拟环境。
	if r.ProjectDir != "" {
		switch k {
		case Maven:
			if w := MavenWrapper(r.ProjectDir); w != "" {
				return r.tool(SDK{Kind: Maven, Home: r.ProjectDir, Bin: w, Version: "Wrapper", Source: "项目自带"}, "项目自带的 mvnw"), nil
			}
		case Python:
			if venv, ok := PythonVenv(r.ProjectDir); ok {
				t := r.tool(venv, "项目虚拟环境")
				t.Env = append(t.Env, "VIRTUAL_ENV="+venv.Home)
				return t, nil
			}
		}
	}

	fb, fbWhy, fbOK := r.fallback(k, sdks)
	var warn string
	if want := r.projectWant(k); !want.empty() {
		// 满足要求的里面，先看全局默认和本机兜底那个是不是正好满足（尊重开发者自己的选择），
		// 再按版本从高到低挑。
		prefer := []SDK{}
		if d := strings.TrimSpace(r.Defaults[k]); d != "" {
			if s, err := r.pick(k, d, sdks); err == nil {
				prefer = append(prefer, s)
			}
		}
		if fbOK {
			prefer = append(prefer, fb)
		}
		for _, s := range append(prefer, sdks...) {
			if satisfies(s, want) {
				return r.tool(s, "按 "+want.From+" 要求的 "+kindName(k)+" "+want.String()), nil
			}
		}
		warn = want.From + " 要求 " + kindName(k) + " " + want.String() + "，本机没有"
		if k == Go {
			warn = "" // GOTOOLCHAIN=auto：本机版本不够时 go 命令会自己下载所需版本，算不上问题
		}
	}

	if d := strings.TrimSpace(r.Defaults[k]); d != "" {
		if s, err := r.pick(k, d, sdks); err == nil {
			t := r.tool(s, "全局默认")
			t.Warn = warn
			return t, nil
		}
	}
	if fbOK {
		t := r.tool(fb, fbWhy)
		t.Warn = warn
		return t, nil
	}
	return nil, fmt.Errorf("未找到 %s：请安装，或在「SDK 管理」里手动添加", kindName(k))
}

// satisfies 判断 SDK 是否满足项目要求。Java 的要求只有主版本（pom 里写 21 或 1.8），
// 按主版本比；其余按版本号前缀或下限比。
func satisfies(s SDK, w Want) bool {
	if s.Kind == Java && !w.Min {
		return s.Major() == w.Prefix
	}
	return w.matches(s.Version)
}

func (r *Resolver) projectWant(k Kind) Want {
	switch k {
	case Java:
		if m := strings.TrimSpace(r.JavaMajor); m != "" {
			return Want{Prefix: m, From: "pom"}
		}
	case Node:
		if r.ProjectDir != "" {
			return NodeWant(r.ProjectDir)
		}
	case Python:
		if r.ProjectDir != "" {
			return PythonWant(r.ProjectDir)
		}
	case Go:
		if r.ProjectDir != "" {
			return GoWant(r.ProjectDir)
		}
	}
	return Want{}
}

// fallback 是既没有指定、也没有项目要求时的选择：先尊重版本管理器里「当前 / 默认」的那个，
// 再看 PATH，最后取本机版本最高的。
func (r *Resolver) fallback(k Kind, sdks []SDK) (SDK, string, bool) {
	if len(sdks) == 0 {
		return SDK{}, "", false
	}
	byReal := func(p string) (SDK, bool) {
		rp := realPath(p)
		for _, s := range sdks {
			if realPath(s.Home) == rp || realPath(s.Bin) == rp {
				return s, true
			}
		}
		return SDK{}, false
	}
	switch k {
	case Java:
		if s, ok := byReal(filepath.Join(r.home, ".sdkman", "candidates", "java", "current")); ok {
			return s, "sdkman 当前版本", true
		}
		if jh := os.Getenv("JAVA_HOME"); jh != "" {
			if s, ok := byReal(jh); ok {
				return s, "JAVA_HOME", true
			}
		}
	case Maven:
		if s, ok := byReal(filepath.Join(r.home, ".sdkman", "candidates", "maven", "current")); ok {
			return s, "sdkman 当前版本", true
		}
	case Node:
		if raw, err := os.ReadFile(filepath.Join(r.home, ".nvm", "alias", "default")); err == nil {
			w := Want{Prefix: strings.TrimPrefix(strings.TrimSpace(string(raw)), "v")}
			for _, s := range sdks {
				if s.Source == "nvm" && len(versionParts(w.Prefix)) > 0 && w.matches(s.Version) {
					return s, "nvm 默认版本", true
				}
			}
		}
	}
	for _, s := range sdks {
		if s.Source == "PATH" {
			return s, "PATH 上的 " + filepath.Base(s.Bin), true
		}
	}
	return sdks[0], "本机版本最高的", true
}

// pick 按指定值找 SDK：绝对路径（SDK 根目录或可执行文件）直接校验；
// 否则按目录名、版本号或显示名在扫描结果里找（兼容 sdkman 候选名，如 21.0.9-oracle）。
func (r *Resolver) pick(k Kind, v string, sdks []SDK) (SDK, error) {
	if filepath.IsAbs(v) {
		rp := realPath(v)
		for _, s := range sdks {
			if realPath(s.Home) == rp || realPath(s.Bin) == rp {
				return s, nil
			}
		}
		return inspectIn(r.home, k, v)
	}
	for _, s := range sdks {
		if filepath.Base(s.Home) == v || s.Version == v || s.Label() == v {
			return s, nil
		}
	}
	return SDK{}, fmt.Errorf("本机没有 %s", v)
}

// tool 把选中的 SDK 变成可执行的工具：可执行文件、要注入的环境变量、要前置的 PATH。
func (r *Resolver) tool(s SDK, reason string) *Tool {
	t := &Tool{Kind: s.Kind, Bin: s.Bin, Home: s.Home, Version: s.Version, Vendor: s.Vendor,
		Source: s.Source, Reason: reason}
	switch s.Kind {
	case Java:
		t.Env = []string{"JAVA_HOME=" + s.Home}
		t.Path = []string{filepath.Join(s.Home, "bin")}
	case Maven:
		// mvn / mvnw 都是脚本，自身不带 JDK：必须同时给出 JAVA_HOME，
		// 否则报 "Unable to locate a Java Runtime"，而且会和上面选的 JDK 对不上。
		if j, err := r.Resolve(Java); err == nil {
			t.Env = []string{"JAVA_HOME=" + j.Home}
		}
		t.Path = []string{filepath.Dir(s.Bin)}
	case Node, Python:
		t.Home = filepath.Dir(s.Bin)
		t.Path = []string{t.Home}
	case Go:
		t.Env = []string{"GOTOOLCHAIN=auto"}
		t.Path = []string{filepath.Join(s.Home, "bin")}
	}
	return t
}

// resolvePnpm 让 pnpm 跟着选中的 node 走：nvm 下每个 node 版本有自己的 pnpm，
// 混用会出现「node 版本与 pnpm 期望不一致」的诡异报错。
func (r *Resolver) resolvePnpm() (*Tool, error) {
	n, nerr := r.Resolve(Node)
	var nodePath []string
	if nerr == nil {
		nodePath = []string{n.Home}
		for _, cand := range []string{filepath.Join(n.Home, "pnpm"), filepath.Join(r.home, "Library", "pnpm", "pnpm")} {
			if execpath.Is(cand) {
				return &Tool{Kind: Pnpm, Bin: cand, Home: n.Home, Source: "跟随 node", Reason: "跟随所选的 node",
					Path: append([]string{filepath.Dir(cand)}, nodePath...)}, nil
			}
		}
	}
	if bin := firstExec("/opt/homebrew/bin/pnpm", "/usr/local/bin/pnpm"); bin != "" {
		return &Tool{Kind: Pnpm, Bin: bin, Home: filepath.Dir(bin), Source: "Homebrew", Reason: "已知安装位置", Path: nodePath}, nil
	}
	if bin := lookPath("pnpm"); bin != "" {
		return &Tool{Kind: Pnpm, Bin: bin, Home: filepath.Dir(bin), Source: "PATH", Reason: "PATH 上的 pnpm", Path: nodePath}, nil
	}
	return nil, errors.New("未找到 pnpm：请安装（npm i -g pnpm 或 corepack enable）")
}

// javaHome 校验一个候选 JAVA_HOME 目录，返回其 bin/java 与规范化后的 Home。
func javaHome(dir string) (bin, home string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	b := execpath.FirstIn(filepath.Join(dir, "bin"), "java")
	if b == "" {
		return "", "", false
	}
	return b, dir, true
}

func firstExec(cands ...string) string {
	for _, c := range cands {
		if execpath.Is(c) {
			return c
		}
	}
	return ""
}

func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// versionLess 比较 v22.20.0 / 17.0.12-oracle / 1.8.0_472 这类版本号，按数值段逐段比。
func versionLess(a, b string) bool {
	as, bs := versionParts(a), versionParts(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			return av < bv
		}
	}
	return a < b
}

func versionParts(s string) []int {
	s = strings.TrimPrefix(s, "v")
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '-' || r == '_' || r == '+'
	})
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
