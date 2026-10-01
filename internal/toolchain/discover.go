package toolchain

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件回答「这台机器上装了哪些 SDK」。
//
// 以前只认 sdkman（Java）和 nvm（Node），不用这两个工具的人一个 SDK 都找不到；
// 版本号还靠目录名去猜。现在按语言把 macOS 上常见的安装方式都扫一遍，版本号读 SDK
// 自己的元数据（JDK 的 release 文件、Go 的 VERSION 文件、Maven 的 maven-core jar 名），
// 读不到才退回目录名，最后才去执行一次 --version。结果按真实路径去重、按版本从高到低排。

// SDK 是本机扫描到的一个可用运行时。
type SDK struct {
	Kind Kind `json:"kind"`
	// Home 是安装根目录：Java 是 JAVA_HOME，Go 是 GOROOT，Maven / Node / Python 是 bin 的上一级。
	Home string `json:"home"`
	// Bin 是可执行文件本身（java / mvn / node / python3 / go）。
	Bin     string `json:"bin"`
	Version string `json:"version"`
	// Vendor 是发行商，只有 JDK 有意义（Oracle、Temurin、Zulu…）。
	Vendor string `json:"vendor"`
	// Source 说明它是从哪里被发现的（sdkman、Homebrew、系统 JDK 目录、手动添加…）。
	Source string `json:"source"`
	Manual bool   `json:"manual"`
}

// Major 返回主版本号：21.0.9 → 21，1.8.0_472 → 8，3.12.10 → 3.12（Python 要到次版本才有意义）。
func (s SDK) Major() string {
	parts := versionParts(s.Version)
	if len(parts) == 0 {
		return ""
	}
	if s.Kind == Java && parts[0] == 1 && len(parts) > 1 {
		return fmt.Sprint(parts[1])
	}
	if s.Kind == Python && len(parts) > 1 {
		return fmt.Sprintf("%d.%d", parts[0], parts[1])
	}
	return fmt.Sprint(parts[0])
}

// Label 是给人看的一行：「21.0.9 · Oracle」「22.20.0」。
func (s SDK) Label() string {
	v := s.Version
	if v == "" {
		v = "未知版本"
	}
	if s.Vendor != "" {
		return v + " · " + s.Vendor
	}
	return v
}

// ── 扫描结果缓存 ─────────────────────────────────────────────────────────

// 扫描只是一批 stat 和读小文件，但界面每 5 秒刷新一次状态、每次都要知道每个服务用哪个 SDK，
// 没必要每次都重扫。30 秒足够让「刚装了一个新 JDK」很快被看到；SDK 管理页的「重新扫描」会立刻清掉。
const discoverTTL = 30 * time.Second

type discoverKey struct {
	kind Kind
	home string
}

var (
	discoverMu    sync.Mutex
	discoverCache = map[discoverKey]discoverEntry{}
	versionCache  = map[string]string{} // 可执行文件真实路径 → 执行 --version 得到的版本
)

type discoverEntry struct {
	at   time.Time
	sdks []SDK
}

// InvalidateDiscovery 清掉扫描缓存，下一次查询会重新扫描磁盘。
func InvalidateDiscovery() {
	discoverMu.Lock()
	discoverCache = map[discoverKey]discoverEntry{}
	discoverMu.Unlock()
}

// Discover 返回本机扫描到的某类 SDK，加上 manual 里手动添加的，按版本从高到低。
func Discover(k Kind, manual []string) []SDK {
	home, _ := os.UserHomeDir()
	return discoverIn(home, k, manual)
}

func discoverIn(home string, k Kind, manual []string) []SDK {
	key := discoverKey{k, home}
	discoverMu.Lock()
	e, ok := discoverCache[key]
	discoverMu.Unlock()
	if !ok || time.Since(e.at) > discoverTTL {
		e = discoverEntry{at: time.Now(), sdks: scan(home, k)}
		discoverMu.Lock()
		discoverCache[key] = e
		discoverMu.Unlock()
	}

	out := append([]SDK(nil), e.sdks...)
	for _, p := range manual {
		if s, err := inspectIn(home, k, p); err == nil {
			s.Source, s.Manual = "手动添加", true
			out = append(out, s)
		}
	}
	return dedupe(out)
}

// dedupe 按可执行文件的真实路径去重（同一个 JDK 常被 sdkman、jenv、JAVA_HOME 以不同路径各指一次；
// Homebrew 的 python3 还会从 opt 与 Cellar 两条链指到同一个文件），保留先出现的那个，
// 再按版本从高到低排。手动添加的排在自动扫到的同路径之后也会被合并，但 Manual 标记保留下来，
// SDK 管理页才能给出「移除」。
//
// 比的是 Bin 而不是 Home：Homebrew 的 /opt/homebrew/bin/python3 一路软链到框架里，
// 它的 Home 和 opt 那条的 Home 是两个不同的目录，只有可执行文件是同一个。
func dedupe(in []SDK) []SDK {
	seen := map[string]int{}
	var out []SDK
	for _, s := range in {
		key := realPath(s.Bin)
		if i, ok := seen[key]; ok {
			if s.Manual {
				out[i].Manual = true
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return versionLess(out[j].Version, out[i].Version) })
	return out
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// Inspect 校验一个目录（或可执行文件）是不是某类 SDK，是的话读出版本等信息。
// 「浏览…」手动指定、SDK 管理页手动添加都走它。
func Inspect(k Kind, path string) (SDK, error) {
	home, _ := os.UserHomeDir()
	return inspectIn(home, k, path)
}

func inspectIn(_ string, k Kind, path string) (SDK, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return SDK{}, fmt.Errorf("请给出 %s 的绝对路径", kindName(k))
	}
	var s SDK
	var ok bool
	switch k {
	case Java:
		// macOS 的 .jdk 包要进到 Contents/Home 才是 JAVA_HOME，选到包本身也认；
		// Homebrew 的 openjdk 还多一层 libexec/openjdk.jdk。
		for _, root := range rootsOf(path, "libexec/openjdk.jdk") {
			for _, cand := range []string{root, filepath.Join(root, "Contents", "Home")} {
				if s, ok = javaSDK(cand, ""); ok {
					break
				}
			}
			if ok {
				break
			}
		}
	case Maven:
		for _, cand := range rootsOf(path, "libexec") {
			if s, ok = mavenSDK(cand, ""); ok {
				break
			}
		}
	case Go:
		for _, cand := range rootsOf(path, "libexec") {
			if s, ok = goSDK(cand, ""); ok {
				break
			}
		}
	case Node:
		s, ok = binSDK(Node, path, []string{"node"}, "")
	case Python:
		s, ok = binSDK(Python, path, []string{"python3", "python"}, "")
	default:
		return SDK{}, fmt.Errorf("不支持的类别：%s", k)
	}
	if !ok {
		return SDK{}, fmt.Errorf("%s 不是可用的 %s（找不到可执行文件）", path, kindName(k))
	}
	return s, nil
}

// Label 是这一类别的名字（JDK / Maven / Node.js / Python / Go），给界面上的分组标题用。
func (k Kind) Label() string { return kindName(k) }

// rootsOf 把「浏览…」选中的那一级翻译成几种可能的安装根目录，按可能性从高到低：
// 根目录本身、其下的子目录（Homebrew 的 libexec）、选到 bin 目录时的上一级、
// 以及选到可执行文件时的上两级。
//
// 选到可执行文件时要先解开软链：/opt/homebrew/bin/go 指向 Cellar 里的真身，
// 不解开的话上两级会落到 /opt/homebrew——那里既没有 VERSION 也不是一个 GOROOT，
// 手动添加上去只会得到一个没有版本号的「Go」。
func rootsOf(path string, subdirs ...string) []string {
	out := make([]string, 0, len(subdirs)+3)
	out = append(out, path)
	for _, sub := range subdirs {
		out = append(out, filepath.Join(path, sub))
	}
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		path = realPath(path)
	}
	return append(out, filepath.Dir(path), filepath.Dir(filepath.Dir(path)))
}

func kindName(k Kind) string {
	switch k {
	case Java:
		return "JDK"
	case Maven:
		return "Maven"
	case Node:
		return "Node.js"
	case Python:
		return "Python"
	case Go:
		return "Go"
	}
	return string(k)
}

// ── 各语言的扫描位置 ─────────────────────────────────────────────────────

// systemScan 为假时只扫用户主目录下的位置（测试用：不让本机真实装着的 JDK 混进断言）。
var systemScan = true

func scan(home string, k Kind) []SDK {
	var out []SDK
	add := func(s SDK, ok bool) {
		if ok {
			out = append(out, s)
		}
	}
	// sysAdd 用于主目录之外的位置（/Applications、JAVA_HOME、PATH…）。
	sysAdd := func(s SDK, ok bool) {
		if systemScan {
			add(s, ok)
		}
	}
	each := func(pattern, source string, fn func(dir, source string) (SDK, bool)) {
		if !systemScan && !strings.HasPrefix(pattern, home) {
			return
		}
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if filepath.Base(m) == "current" {
				continue // sdkman 的 current 是指向某个版本的链接，版本本身会被扫到
			}
			add(fn(m, source))
		}
	}
	h := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }

	switch k {
	case Java:
		j := func(dir, src string) (SDK, bool) { return javaSDK(dir, src) }
		jdk := func(dir, src string) (SDK, bool) { return javaSDK(filepath.Join(dir, "Contents", "Home"), src) }
		each("/Library/Java/JavaVirtualMachines/*", "系统 JDK 目录", jdk)
		each(h("Library", "Java", "JavaVirtualMachines", "*"), "IntelliJ 下载", jdk)
		each(h(".sdkman", "candidates", "java", "*"), "sdkman", j)
		each("/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk", "Homebrew", jdk)
		each("/usr/local/opt/openjdk*/libexec/openjdk.jdk", "Homebrew", jdk)
		each(h(".asdf", "installs", "java", "*"), "asdf", j)
		each(h(".local", "share", "mise", "installs", "java", "*"), "mise", j)
		each(h(".jenv", "versions", "*"), "jenv", j)
		for _, app := range []string{"IntelliJ IDEA", "IntelliJ IDEA CE", "IntelliJ IDEA Ultimate", "Android Studio"} {
			sysAdd(javaSDK(filepath.Join("/Applications", app+".app", "Contents", "jbr", "Contents", "Home"), "JetBrains 内置"))
		}
		if jh := os.Getenv("JAVA_HOME"); jh != "" {
			sysAdd(javaSDK(jh, "JAVA_HOME"))
		}

	case Maven:
		m := func(dir, src string) (SDK, bool) { return mavenSDK(dir, src) }
		each(h(".sdkman", "candidates", "maven", "*"), "sdkman", m)
		each("/opt/homebrew/opt/maven/libexec", "Homebrew", m)
		each("/usr/local/opt/maven/libexec", "Homebrew", m)
		each(h(".asdf", "installs", "maven", "*"), "asdf", m)
		each(h(".local", "share", "mise", "installs", "maven", "*"), "mise", m)
		if p := lookPath("mvn"); p != "" {
			sysAdd(mavenSDK(filepath.Dir(filepath.Dir(realPath(p))), "PATH"))
		}

	case Node:
		n := func(bin string) func(dir, src string) (SDK, bool) {
			return func(dir, src string) (SDK, bool) { return binSDK(Node, filepath.Join(dir, bin), []string{"node"}, src) }
		}
		each(h(".nvm", "versions", "node", "*"), "nvm", n("bin/node"))
		each(h(".local", "share", "fnm", "node-versions", "*"), "fnm", n("installation/bin/node"))
		each(h("Library", "Application Support", "fnm", "node-versions", "*"), "fnm", n("installation/bin/node"))
		each(h(".volta", "tools", "image", "node", "*"), "volta", n("bin/node"))
		each(h(".asdf", "installs", "nodejs", "*"), "asdf", n("bin/node"))
		each(h(".local", "share", "mise", "installs", "node", "*"), "mise", n("bin/node"))
		each("/opt/homebrew/opt/node*", "Homebrew", n("bin/node"))
		each("/usr/local/opt/node*", "Homebrew", n("bin/node"))
		sysAdd(binSDK(Node, "/usr/local/bin/node", []string{"node"}, "官方安装包"))
		if p := lookPath("node"); p != "" {
			// 解开软链再交给 binSDK：/opt/homebrew/bin/node 的上一级是 /opt/homebrew，
			// 那不是一个「安装根目录」，而且会和 Homebrew 那条同一个可执行文件各占一行。
			sysAdd(binSDK(Node, realPath(p), []string{"node"}, "PATH"))
		}

	case Python:
		py := func(rel ...string) func(dir, src string) (SDK, bool) {
			return func(dir, src string) (SDK, bool) {
				for _, r := range rel {
					if s, ok := binSDK(Python, filepath.Join(dir, r), nil, src); ok {
						return s, true
					}
				}
				return SDK{}, false
			}
		}
		each(h(".pyenv", "versions", "*"), "pyenv", py("bin/python3", "bin/python"))
		each(h(".asdf", "installs", "python", "*"), "asdf", py("bin/python3"))
		each(h(".local", "share", "mise", "installs", "python", "*"), "mise", py("bin/python3"))
		// Homebrew 的 Python 老 formula 是 libexec/bin/python3，新的是 bin/python3，
		// 两种都要试：只看 libexec 的话，装了 python@3.14 的人在界面上看不到它——
		// 界面从访达启动，PATH 上没有 /opt/homebrew/bin，那条兜底也接不上。
		each("/opt/homebrew/opt/python@3*", "Homebrew", py("libexec/bin/python3", "bin/python3", "bin/python"))
		each("/usr/local/opt/python@3*", "Homebrew", py("libexec/bin/python3", "bin/python3", "bin/python"))
		each("/Library/Frameworks/Python.framework/Versions/3*", "python.org", py("bin/python3"))
		// /usr/bin/python3 在没装命令行工具的机器上是个会弹安装框的占位程序，
		// 所以只在命令行工具真的在时，直接用它背后的那个解释器。
		sysAdd(binSDK(Python, "/Library/Developer/CommandLineTools/usr/bin/python3", nil, "Xcode 命令行工具"))
		if p := lookPath("python3"); p != "" && p != "/usr/bin/python3" {
			sysAdd(binSDK(Python, realPath(p), nil, "PATH"))
		}

	case Go:
		g := func(dir, src string) (SDK, bool) { return goSDK(dir, src) }
		// 带版本号的 formula（go@1.24）和 go 一样常见，要一并收进来：只认 /opt/homebrew/opt/go
		// 的话，装 go@1.24 的人在这里会看到一个空的 Go 分组。
		each("/opt/homebrew/opt/go*/libexec", "Homebrew", g)
		each("/usr/local/opt/go*/libexec", "Homebrew", g)
		each("/usr/local/go", "官方安装包", g)
		each(h("sdk", "go*"), "golang.org/dl", g)
		// go.mod 要求更新的 Go 时，go 命令会把整套 SDK 下载进模块缓存，那是真能跑的一个 Go。
		// IDEA 的 GOROOT 下拉也是从这里列出来的。
		each(h("go", "pkg", "mod", "golang.org", "toolchain@*"), "Go 工具链缓存", g)
		each(h(".goenv", "versions", "*"), "goenv", g)
		each(h(".gvm", "gos", "*"), "gvm", g)
		each(h(".asdf", "installs", "golang", "*", "go"), "asdf", g)
		each(h(".local", "share", "mise", "installs", "go", "*"), "mise", g)
		if p := lookPath("go"); p != "" {
			sysAdd(goSDK(filepath.Dir(filepath.Dir(realPath(p))), "PATH"))
		}
	}
	return dedupe(out)
}

// ── 单个 SDK 的校验与版本读取 ────────────────────────────────────────────

var jdkVendors = map[string]string{
	"Oracle Corporation": "Oracle", "Eclipse Adoptium": "Temurin", "Azul Systems, Inc.": "Zulu",
	"Amazon.com Inc.": "Corretto", "JetBrains s.r.o.": "JetBrains", "BellSoft": "Liberica",
	"Microsoft": "Microsoft", "GraalVM Community": "GraalVM", "Homebrew": "Homebrew",
	"Eclipse Foundation": "Temurin", "Alibaba": "Dragonwell", "Tencent": "Kona", "SAP SE": "SapMachine",
}

// javaSDK 读 JDK 根目录下的 release 文件拿版本与发行商，这比从目录名里猜可靠得多
// （jenv、asdf、手动解压的目录名五花八门）。
func javaSDK(dir, source string) (SDK, bool) {
	bin := filepath.Join(dir, "bin", "java")
	if !isExec(bin) {
		return SDK{}, false
	}
	s := SDK{Kind: Java, Home: dir, Bin: bin, Source: source}
	kv := readKV(filepath.Join(dir, "release"))
	s.Version = kv["JAVA_VERSION"]
	if impl := kv["IMPLEMENTOR"]; impl != "" {
		if v, ok := jdkVendors[impl]; ok {
			s.Vendor = v
		} else {
			s.Vendor = impl
		}
	}
	if s.Version == "" {
		s.Version = leadingVersion(filepath.Base(realPath(dir)))
	}
	return s, true
}

var mavenCoreRe = regexp.MustCompile(`^maven-core-(\d[\w.-]*)\.jar$`)

func mavenSDK(dir, source string) (SDK, bool) {
	bin := filepath.Join(dir, "bin", "mvn")
	if !isExec(bin) {
		return SDK{}, false
	}
	s := SDK{Kind: Maven, Home: dir, Bin: bin, Source: source}
	if entries, err := os.ReadDir(filepath.Join(dir, "lib")); err == nil {
		for _, e := range entries {
			if m := mavenCoreRe.FindStringSubmatch(e.Name()); m != nil {
				s.Version = m[1]
				break
			}
		}
	}
	if s.Version == "" {
		s.Version = leadingVersion(filepath.Base(realPath(dir)))
	}
	return s, true
}

func goSDK(dir, source string) (SDK, bool) {
	bin := filepath.Join(dir, "bin", "go")
	if !isExec(bin) {
		return SDK{}, false
	}
	s := SDK{Kind: Go, Home: dir, Bin: bin, Source: source}
	if f, err := os.Open(filepath.Join(dir, "VERSION")); err == nil {
		sc := bufio.NewScanner(f)
		if sc.Scan() {
			s.Version = strings.TrimPrefix(strings.TrimSpace(sc.Text()), "go")
		}
		f.Close()
	}
	if s.Version == "" {
		s.Version = leadingVersion(strings.TrimPrefix(filepath.Base(realPath(dir)), "go"))
	}
	return s, true
}

var cellarRe = regexp.MustCompile(`/Cellar/[^/]+/(\d[\w.]*)/`)

// binSDK 处理「给一个可执行文件」的那几类（Node、Python）。path 可以是可执行文件本身，
// 也可以是安装根目录（手动添加时常选到这一级），names 是根目录下 bin/ 里要找的文件名。
//
// 版本依次从：版本管理器的目录名（nvm 的 v22.20.0、pyenv 的 3.12.10）、Homebrew Cellar 路径、
// 执行一次 --version 里取。执行的结果按真实路径缓存，不会每次刷新都去跑。
func binSDK(k Kind, path string, names []string, source string) (SDK, bool) {
	bin := path
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		bin = ""
		if names == nil {
			names = []string{"python3", "python"}
		}
		for _, n := range names {
			if c := filepath.Join(path, "bin", n); isExec(c) {
				bin = c
				break
			}
		}
	}
	if bin == "" || !isExec(bin) {
		return SDK{}, false
	}
	s := SDK{Kind: k, Home: filepath.Dir(filepath.Dir(bin)), Bin: bin, Source: source}
	switch {
	case source == "nvm" || source == "fnm" || source == "volta" || source == "asdf" || source == "mise" || source == "pyenv":
		dir := filepath.Base(s.Home)
		if source == "fnm" {
			dir = filepath.Base(filepath.Dir(s.Home))
		}
		s.Version = leadingVersion(strings.TrimPrefix(dir, "v"))
	}
	if s.Version == "" {
		if m := cellarRe.FindStringSubmatch(realPath(bin)); m != nil {
			s.Version = m[1]
		}
	}
	if s.Version == "" {
		s.Version = execVersion(bin)
	}
	// pyenv 里偶尔有装坏的目录（比如把 node 版本号当 python 装了一份），版本号对不上
	// Python 的形状就不要，免得下拉里出现一个 22.20.0 的「Python」。
	if k == Python && !strings.HasPrefix(s.Version, "2.") && !strings.HasPrefix(s.Version, "3.") {
		if v := execVersion(bin); strings.HasPrefix(v, "3.") || strings.HasPrefix(v, "2.") {
			s.Version = v
		} else {
			return SDK{}, false
		}
	}
	return s, true
}

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)+(?:[._+-]\w+)?`)

func execVersion(bin string) string {
	key := realPath(bin)
	discoverMu.Lock()
	v, ok := versionCache[key]
	discoverMu.Unlock()
	if ok {
		return v
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err == nil {
		v = versionRe.FindString(string(out))
	}
	discoverMu.Lock()
	versionCache[key] = v
	discoverMu.Unlock()
	return v
}

// leadingVersion 取字符串开头的版本号：21.0.9-oracle → 21.0.9，3.12.10 → 3.12.10。
func leadingVersion(s string) string {
	if m := versionRe.FindString(s); m != "" && strings.HasPrefix(s, m[:1]) {
		return m
	}
	return ""
}

// readKV 读 KEY="VALUE" 形式的文件（JDK 的 release、Python 的 pyvenv.cfg 都是这种）。
func readKV(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		out[strings.TrimSpace(line[:i])] = strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
	}
	return out
}
