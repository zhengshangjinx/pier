package proc

import (
	"strconv"
	"strings"
)

// 这一份回答的是「这个进程是被谁拉起来的」。
//
// 光知道「端口被 PID 12345 占着」只够把它杀掉，不够判断该不该杀——同一个端口上
// 蹲着的可能是自己刚在编辑器里起的服务，也可能是上一轮调试忘了关的终端，
// 还可能是 Pier 面板自己拉起来的那份。这三者的下一步动作完全不同，
// 而命令行与进程名往往长得一模一样（都是 node、都是 java）。
//
// 做法是从这个进程沿父进程一路往上走，看链上最先认出来的是谁。往上走而不是看
// 进程自己，是因为服务本身的命令行没有任何身份信息；而链上那几个「宿主」——
// 编辑器、终端、面板——名字是确定的。
//
// 表与「怎么读进程表」都是平台各一份（origin_unix.go / origin_windows.go）：
// 两边的宿主程序名毫无重叠，硬凑到一张表里只会让每一条都带上「哪个平台」的注解。
// 往上走的那段是共用的，两个平台的链是一回事。

// Origin 是一次溯源的结果。
type Origin struct {
	// Kind 是来源的类别，取值见各平台标记表：editor / terminal / agent / panel / system 等。
	Kind string `json:"kind"`
	// Label 是可以直接摆在界面上的名字，如「VS Code」「终端」。
	Label string `json:"label"`
	// Chain 是链上认出来的那几个，从近到远。第一项就是 Label。
	//
	// 多给一层是因为「VS Code 里那个终端」和「VS Code 本身」在做决定时是一回事，
	// 但在排查时不是——只给最近的那一个，用户会以为自己记错了当时怎么起的。
	Chain []string `json:"chain"`
}

// OK 表示这次溯源认出来了。
func (o Origin) OK() bool { return o.Kind != "" }

// originMark 是标记表里的一行。
type originMark struct {
	kind  string
	label string
	// execs 按可执行文件名精确匹配。这是最可靠的一路：
	// 名字对上了就是它，不受路径、参数、包装脚本影响。
	execs []string
	// paths 按命令行里的片段匹配，用于认那些可执行文件名说明不了问题的程序。
	//
	// 典型是 macOS 上的应用包：编辑器、终端启动起来的进程，argv[0] 是包里的
	// 某个通用名字（Electron、MacOS），只有整条路径里才带着「Visual Studio Code」。
	// Windows 上拿不到别的进程的命令行，那边的表里这一栏是空的。
	paths []string
	// fallback 表示这一条只在链上什么都没认出来时才用。
	//
	// 给的是「系统启动」这种：图形会话里的一切追到根上都是它起的，所以它几乎
	// 总在链尾。一视同仁地收进链里，每一行都会变成「终端 ← 系统启动」——
	// 后半截既不是排查时的上下文，也没有哪个窗口叫这个名字，纯是噪声。
	// 单独摆出来时它才是有用的答案：「这是 launchd 拉起来的，不是谁手敲的」。
	fallback bool
}

// hit 判断进程链上的这一环是不是这个标记。
//
// 名字比对交给各平台的 matchesExec：表里写的是「Code」「idea64」这样的程序名，
// 而各平台进程表里给的形态不一样（Windows 带 .exe，unix 不带）。
func (m originMark) hit(a ancestor) bool {
	base := baseName(argv0(a.Args))
	for _, e := range m.execs {
		if matchesExec(base, e) {
			return true
		}
	}
	for _, p := range m.paths {
		if strings.Contains(a.Args, p) {
			return true
		}
	}
	return false
}

// originDepth 是最多往上走几层。
//
// 给足余量：编辑器里的集成终端再起一层 shell 再起服务，链长也不过五六层。
// 封顶是为了防环——进程表在极端情况下会读到互相指认的父子关系。
const originDepth = 24

// ProcessOrigin 沿父子链往上找出 pid 是谁拉起来的；认不出来时返回零值。
//
// 只在用户问「这个端口是怎么回事」时才调用：一次要读一遍进程表，
// 不是状态刷新那条每两秒走一遍的路。要问一批就调 Origins，别在循环里调它。
func ProcessOrigin(pid int) Origin { return originFrom(processTable(), pid) }

// Origins 一次给一批进程做溯源，只读一遍进程表。
//
// 端口那一屏有几十行，逐个调 ProcessOrigin 就是几十次进程表读取。
// 返回的 map 里没有那些认不出来的：认不出来就是没有来源可报。
func Origins(pids []int) map[int]Origin {
	if len(pids) == 0 {
		return nil
	}
	table := processTable()
	if len(table) == 0 {
		return nil
	}
	out := make(map[int]Origin, len(pids))
	for _, pid := range pids {
		if o := originFrom(table, pid); o.OK() {
			out[pid] = o
		}
	}
	return out
}

// originFrom 在一份已经读好的进程表上做一次溯源。
func originFrom(table map[int]ancestor, pid int) Origin {
	if pid <= 0 || len(table) == 0 {
		return Origin{}
	}
	var out Origin
	seen := map[string]bool{}
	for _, a := range ancestorsIn(table, pid) {
		for _, m := range originMarks {
			if !m.hit(a) {
				continue
			}
			// 兜底的那些让位给链上更近的人，理由见 originMark.fallback。
			if m.fallback && out.Kind != "" {
				break
			}
			// 最近的那一个定身份：链上更远的只是「当时在哪儿」，不是「谁起的」。
			if out.Kind == "" {
				out.Kind, out.Label = m.kind, m.label
			}
			if !seen[m.label] {
				seen[m.label] = true
				out.Chain = append(out.Chain, m.label)
			}
			break
		}
	}
	return out
}

// ancestorsIn 返回从 pid 自己开始、沿父进程往上的一段进程链。
//
// 链首是 pid 自己：端口握在谁手里，谁就有可能是那个「被起起来的」东西，
// 直接从它开始认，用户看到的才是最近的那个答案。
func ancestorsIn(table map[int]ancestor, pid int) []ancestor {
	var out []ancestor
	seen := map[int]bool{}
	for p := pid; p > 1 && len(out) < originDepth; {
		if seen[p] {
			break
		}
		seen[p] = true
		a, ok := table[p]
		if !ok {
			break
		}
		out = append(out, a)
		p = a.PPID
	}
	return out
}

// ancestor 是进程链上的一环。
type ancestor struct {
	PID  int
	PPID int
	// Args 是这一环的命令行（Windows 上只有可执行文件名，见那边的说明）。
	Args string
}

// argv0 取命令行里的第一个词。
func argv0(args string) string {
	args = strings.TrimLeft(args, " \t")
	if i := strings.IndexAny(args, " \t"); i >= 0 {
		return args[:i]
	}
	return args
}

// baseName 取路径的最后一段。
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// cutField 从行首切下一个数字，返回它与剩下的部分。
//
// 给 unix 那边读 ps 的输出用，也用来在测试里拼一份假的进程表——两侧共用这一个，
// 拼出来的表才和真读进来的那份形状一样。
//
// 不用 strings.Fields 整体切开再拼回去：命令行里的空格是它自己的，
// 切开再拼会把连续空格压成一个，而这一串是要原样给人看的。
func cutField(s string) (int, string, bool) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return 0, "", false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimLeft(s[i:], " \t"), true
}
