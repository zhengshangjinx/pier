// Package diag 把日志尾部翻译成两句能照着做的话。
//
// 服务起不来时，用户手上是一屏工具链自己的行话：EADDRINUSE、cannot find symbol、
// UnsupportedClassVersionError。每一条都有明确的对策，但对策不在日志里。
// 这里认最常见的几种，给出「一句原因 + 一句下一步」，并在旁边抄上命中的那行原文。
//
// 认不出来就什么都不给。宁可不说，也不能猜：猜错一次，用户就照着一条不相干的建议
// 去改配置，而日志里明明写着真正的原因。这与 proc/origin.go「认不出来就留空」
// 是同一条原则。
//
// 这一层是纯文本的，不碰文件也不碰进程：规则怎么排、哪一行命中都能单测，不必起服务。
// 读日志在 log.go 里。
package diag

import "strings"

// Hit 是一次命中。
//
// Line 是命中那一行的原文，必须一起给出去：端口号、模块名、缺的符号这些字只在
// 原文里有，而它们恰恰是「该去改哪一处」的答案；只回一句概括，用户还得回去翻日志。
type Hit struct {
	Reason string `json:"reason"`
	Next   string `json:"next"`
	Line   string `json:"line"`
}

// maxLine 是回显原文时的字符上限。Java 的异常栈、Maven 的依赖树都能把一行写得很长，
// 整行抄出来会把界面里那一条撑得没法看；截一下，剩下的用户自己会去日志里找
// （完整日志的路径就在旁边）。
const maxLine = 300

// rule 是一条规则：行里出现任一 needle 就给出这两句。
//
// needle 一律小写：比对是忽略大小写的。同一个错误在日志里有 EADDRINUSE、
// eaddrinuse、Errno 48 各种写法，大小写不该决定认不认得出来。
type rule struct {
	reason  string
	next    string
	needles []string
}

// rules 按「越具体越靠前」排。
//
// 顺序是这张表唯一需要小心的地方。编译类（BUILD FAILURE）放在最后，因为一次构建
// 失败常常同时打出真正的病因和这几个字：拉不到依赖会以 BUILD FAILURE 收场，
// 语法错误也会。先命中哪个就报哪个，所以「BUILD FAILURE」这种只是结论的行必须排后面，
// 它自己是说不出该去做什么的。
var rules = []rule{
	{
		reason: "端口被占着",
		// 这里不提「换成 ${PORT}」：清单里的 ${} 展开还没有做，现在写上去，
		// 用户照着改只会得到一个原样的 ${PORT}，看着像 Pier 认了、其实是子进程认了。
		next: "看是谁占的：pier ports；或者把这条服务的端口换一个",
		needles: []string{
			"eaddrinuse",
			// Go 报「bind: address already in use」，Node 报「listen EADDRINUSE」，
			// 这一条把前者的后半截也盖住了。
			"address already in use",
			// 同一件事在 Windows 上的说法（Go 与 Node 都原样带出系统那句），
			// 中文与英文各一份。不带的话，Windows 上这条规则等于是空的。
			"通常每个套接字地址",
			"only one usage of each socket address",
		},
	},
	{
		reason: "找不到命令",
		next:   "去「SDK 管理」里确认这一套，或者跑一次 pier doctor",
		needles: []string{
			"command not found",
			// Windows 的两种说法（中文控制台与英文控制台），以及 Go 自己那句。
			"不是内部或外部命令",
			"is not recognized as an internal or external command",
			"executable file not found in $path",
		},
	},
	{
		reason: "依赖没装",
		next:   "在这个服务的目录里跑一次装依赖",
		needles: []string{
			"cannot find module",
			"module_not_found",
		},
	},
	{
		reason: "JDK 版本对不上",
		next:   "看这条服务选的 Java 是哪一套（清单里的 toolchain 或全局默认）",
		needles: []string{
			"unsupportedclassversionerror",
			// javac / maven 的两种说法：JDK 比代码要求的低时是前一句，
			// 编译插件里的配置互相打架时是后一句。
			"invalid target release",
			"invalid source release",
		},
	},
	{
		reason: "拉不到依赖（私服或代理不通）",
		next:   "查网络与 settings.xml：私服地址、镜像、代理",
		needles: []string{
			"could not resolve dependencies",
		},
	},
	{
		reason: "权限不够",
		next:   "看涉及的目录与文件的可执行位",
		needles: []string{
			"permission denied",
		},
	},
	{
		reason: "被系统杀了",
		next:   "多半是内存不够：看当时机器还剩多少可用内存",
		needles: []string{
			// 进程被 OOM killer 干掉时，日志里就只剩这一行。
			"killed",
			"cannot allocate memory",
		},
	},
	{
		reason: "编译没过",
		next:   "看原文里第一个 error，那里写着是哪一处",
		needles: []string{
			"syntaxerror",
			"cannot find symbol",
		},
	},
	{
		// 兜底的一条：上面全没命中时，构建工具只留下了这一句结论。
		// 它必须排在最末——「BUILD FAILURE」是任何一次构建失败的收场，
		// 拉不到依赖、语法错、测试没过都会打出它，而它自己说不出是哪儿错了。
		// 排在前面的话，认出来的永远是最没用的那一行。
		reason: "编译没过",
		next:   "看原文里第一个 error，那里写着是哪一处",
		needles: []string{
			"build failure",
		},
	},
}

// Scan 在日志文本里认一条出来。
//
// 外层按 rules 的顺序走，命中之后再看那一行是最后出现的哪一个：同一个错误往往被
// 打了好几遍（每个模块、每个 worker 各报一次），而日志是顺着写的，
// 越靠后越接近停下那一刻，也就越接近用户当时看见的现场。
func Scan(text string) (Hit, bool) {
	if strings.TrimSpace(text) == "" {
		return Hit{}, false
	}
	lines := strings.Split(text, "\n")
	for _, r := range rules {
		if h, ok := r.match(lines); ok {
			return h, true
		}
	}
	return Hit{}, false
}

// match 从后往前找这一条规则命中的那一行。
func (r rule) match(lines []string) (Hit, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		for _, n := range r.needles {
			if strings.Contains(low, n) {
				return Hit{Reason: r.reason, Next: r.next, Line: clip(line)}, true
			}
		}
	}
	return Hit{}, false
}

// clip 把过长的一行截到 maxLine 个字符。按字符而不是字节切：
// 从字节中间切开会把一个汉字劈成半个，显示出来是一片乱码。
func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxLine {
		return s
	}
	return string(r[:maxLine]) + "…"
}
