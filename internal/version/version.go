// Package version 回答两个问题：这份二进制是哪个版本，以及两个版本号哪个新。
//
// 打包时由 -ldflags 注入两个字符串（package.sh / build-app.sh / tools/pkg/linux/build.sh）：
// 版本号本身，以及一个「这是打包产物」的记号。本地直接 go build 出来的两个都没有，
// 版本号一律叫 dev——自更新那边据此不做任何「有新版」的判断：宁可漏报，
// 也不能拿一个编出来的号去催人升级。
package version

import (
	"runtime/debug"
	"strconv"
	"strings"
)

var (
	// Version 是发布版本号，形如 0.2.0（不带 v）。空表示没注入过（本地 go build）。
	Version = ""
	// released 是「这一份是打包产物」的记号，由打包脚本连同 Version 一起注入。
	//
	// 为什么不能只看 Version：从 go 1.24 起，在打了 tag 的提交上直接 go build，
	// go 也会把模块版本盖进 Main.Version（工作区脏的时候还带个 +dirty），
	// 于是「自己编的」与「打包出来的」从这个值上分辨不出来。
	// 也不能从构建设置里反查 -ldflags：debug.ReadBuildInfo().Settings 里根本没有它
	// （只有 -tags、-trimpath 这一类），所以只能让脚本再钉一个记号。
	released = ""
)

// Current 返回当前二进制的版本号。
//
// 三个来源按顺序试：打包时注入的 → 模块版本 → "dev"。
//
// 模块版本这一路既覆盖 `go install <模块>@<版本>` 装出来的那份，也覆盖「在打了 tag
// 的提交上直接 go build」——从 go 1.24 起后者也会被盖上 v0.2.0（工作区是脏的还带个
// +dirty），那确实是这个版本号，认它没问题（该不该由我们替换自己另说，见 Read）。
// 不能认的是另外两类：在源码树里编出来的 "(devel)"，以及从某个提交装出来的伪版本
// v0.0.0-20261005120000-abcdef123456。它们都不是「第几版」，认了就等于承认
// 「永远比任何 tag 都旧」，于是每次检查都提示有新版。所以在 Canon 那里整类挡掉。
func Current() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return currentFrom(bi.Main.Version)
	}
	return currentFrom("")
}

// currentFrom 是 Current 的内核：给定构建信息里的模块版本，挑出该用的那个版本号。
func currentFrom(mainVersion string) string {
	if v, ok := Canon(Version); ok {
		return v
	}
	if v, ok := Canon(mainVersion); ok {
		return v
	}
	return "dev"
}

// Dev 报告这份二进制是不是没有版本号（本机构建）。
func Dev() bool { return Current() == "dev" }

// Build 描述这份二进制的来历。自更新要分清「打包装的那份」与「自己编 / 装的那份」：
// 只有前者能原地替换，后两者替换了也没有意义（下次 go build / go install 又覆盖回去）。
type Build struct {
	// Version 与 Current() 同一个值。
	Version string
	// Packaged 表示这一份是打包脚本出来的产物，也就是从 Releases 下载来的那一种。
	// 只有它允许原地替换自己。
	Packaged bool
	// GoInstall 表示这是 go install <模块>@<版本> 装的。它带着真版本号，
	// 所以能比较、能查到有新版，但升级该走 go install，不该由我们替换文件。
	GoInstall bool
	// Source 表示在源码树里编的：自己 go build 出来的，或者在一份干净检出里
	// 直接跑 go build（从 go 1.24 起，在打了 tag 的提交上它也带着版本号，
	// 但同样不该由我们替换——下次编译又覆盖回去）。
	Source bool
}

// Read 从构建信息里读出来历。
func Read() Build {
	// 读不到构建信息（不是模块构建）时按最保守的那一类算：入参全空，
	// classify 会给出「源码构建」，于是不给「原地替换自己」这条路。
	if bi, ok := debug.ReadBuildInfo(); ok {
		return classify(bi.Main.Version, bi.Settings)
	}
	return classify("", nil)
}

// classify 判定这份二进制的来历。入参就是构建信息里的那两样，之所以拆成纯函数是为了
// 能离线测：debug.ReadBuildInfo 读的是正在跑的这一份二进制，测试里换不成别的，
// 而 go install 装出来的那种构建信息在本机根本造不出来（模块缓存里没有 .git）。
func classify(mainVersion string, settings []debug.BuildSetting) Build {
	b := Build{Version: currentFrom(mainVersion)}
	// 打包脚本钉的记号最硬：这一份就是从 Releases 下载来的产物。
	if released != "" {
		b.Packaged = true
		return b
	}
	// 在源码树里编的会被盖上 vcs.revision 这类设置，从模块缓存里编的不会（那里没有 .git）。
	// 光看 Main.Version 分不出这两者：从 go 1.24 起，在打了 tag 的提交上直接 go build，
	// Main.Version 也会被盖成 v0.2.0。
	fromTree := false
	for _, s := range settings {
		if s.Key == "vcs" || strings.HasPrefix(s.Key, "vcs.") {
			fromTree = true
			break
		}
	}
	// 这里判的不再是「版本号认不认得出来」，而是「有没有模块版本」：
	// go install <模块>@<提交> 装出来的那份带的是伪版本，版本号认不出来（Current 会退回 dev），
	// 但它确实是装出来的、不是一个能改的源码目录，提示语该说的是「用 go install 升级」。
	if !fromTree && hasModuleVersion(mainVersion) {
		b.GoInstall = true
		return b
	}
	b.Source = true
	return b
}

// hasModuleVersion 报告构建信息里带着一个模块版本：既不是空，也不是源码树里那个 "(devel)"。
func hasModuleVersion(s string) bool { return s != "" && s != "(devel)" }

// ModulePath 是这份二进制的模块路径，给「go install <它>@latest」那句提示用。
//
// 从构建信息里读而不是写死：fork 出去自己编的人，看到的该是他自己那条命令。
func ModulePath() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path != "" {
		return bi.Main.Path
	}
	return "github.com/zhengshangjinx/pier"
}

// Summary 是 `pier version` 那一行。
func Summary() string {
	if Dev() {
		return "Pier dev（本机构建，无版本号）"
	}
	return "Pier " + Current()
}

// Normalize 去掉首尾空白与开头的 v。发布产物里两种写法都在：tag 是 v0.2.0，
// 二进制里注入的是 0.2.0。
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	// 只认「v 后面跟数字」这一种，免得把 "version" 这种词的头一个字母也吃掉。
	if len(s) > 1 && (s[0] == 'v' || s[0] == 'V') && s[1] >= '0' && s[1] <= '9' {
		s = s[1:]
	}
	return s
}

// Compare 比较两个版本号：a 比 b 旧返回 -1，相同返回 0，a 比 b 新返回 1。
//
// ok 为 false 表示有一方不是能比较的版本号（dev、空、伪版本、胡写的），
// 这时调用方**不能**得出「有更新」的结论。
func Compare(a, b string) (int, bool) {
	an, apre, ok := split(Normalize(a))
	if !ok {
		return 0, false
	}
	bn, bpre, ok := split(Normalize(b))
	if !ok {
		return 0, false
	}
	// 必须按数字比：字符串比的话 0.9.0 会大于 0.10.0。
	for i := range an {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1, true
			}
			return 1, true
		}
	}
	// 数字段一样时，没有 - 后缀的那个新：1.0.0 比 1.0.0-rc1 新（SemVer 第 11 条）。
	// 两边都有后缀就按字面比，够用——发布用的 tag 一直是三段纯数字，这条只是兜底。
	switch {
	case apre == bpre:
		return 0, true
	case apre == "":
		return 1, true
	case bpre == "":
		return -1, true
	case apre < bpre:
		return -1, true
	default:
		return 1, true
	}
}

// Newer 报告 cand 是不是比 cur 新。任何一方不可比较时返回 false。
func Newer(cand, cur string) bool {
	n, ok := Compare(cand, cur)
	return ok && n > 0
}

// Canon 把 s 规范化成 0.2.0 这样的版本号；不是版本号时返回 false。
//
// 除了 Current 自己用，发布那边也要问同一个问题：GitHub 给的 tag 得先过这一关，
// 「认不出来」的 tag 不能进版本比较（两份判定必须一致，各写一份迟早一处松一处紧）。
//
// 只有三段纯数字的正式版才算数：我们发的 tag 一直是这个形状。带后缀的一律不算——
// -rc1 这类预发布 releases/latest 本来就不会给出来；更要紧的是 go 的伪版本
// v0.0.0-20261005120000-abcdef123456（从某个提交 go install 出来的那份带着它），
// 认了它就等于承认「永远比任何 tag 都旧」，于是每次检查都提示有新版。
// 去认伪版本的格式得跟着 go 的规则一起改，不如整类挡在外面。
func Canon(s string) (string, bool) {
	nums, pre, ok := split(Normalize(s))
	if !ok || pre != "" {
		return "", false
	}
	// 重建而不是回原串：顺手把 0.2 补成 0.2.0，把 +meta 那段构建元数据丢掉。
	return strconv.Itoa(nums[0]) + "." + strconv.Itoa(nums[1]) + "." + strconv.Itoa(nums[2]), true
}

// split 把版本号拆成三段数字加一个预发布后缀，缺的段补 0。
func split(s string) ([3]int, string, bool) {
	var nums [3]int
	if s == "" {
		return nums, "", false
	}
	// + 之后是构建元数据，不参与比较（SemVer 第 10 条），直接丢掉；- 之后先留着。
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre, s = s[i+1:], s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nums, "", false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return nums, "", false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return nums, "", false
			}
		}
		// 上面已经保证是 9 位以内的纯数字，这里不会出错。
		n, _ := strconv.Atoi(p)
		nums[i] = n
	}
	return nums, pre, true
}
