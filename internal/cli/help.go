package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// 本文件是命令的说明书，也是帮助唯一的出处：全局的 `pier --help` 与每个动词的
// `pier <命令> -h` 都从这里渲染。分两处写的话，改了参数只改一处，另一处就开始
// 骗人——而它骗的正是刚上手、最需要文档的那个人。
//
// 顺序就是帮助里的顺序，按用途分块：先认项目，再启停，再看，最后是工具。
// 分发用的 handlers（cli.go）是另一张表：那边还有隐藏动词（更新助手），
// 它不是给人敲的，不能出现在这里。两张表对不对得上由测试守着（help_test.go）。

// usage 是帮助里的一行：怎么写 + 做什么。
type usage struct {
	form string
	desc string
}

// command 是一个动词的说明书。
type command struct {
	name  string
	usage []usage  // 全局帮助里的行；一个动词可以有好几种用法（logs 有三种）
	desc  string   // 一句话说明；全局帮助的第二列，也是 `pier <命令> -h` 的开头
	flags []usage  // 这个动词自己的参数
	notes []string // 收尾几句：退出码、注意事项
	// noConfig 为真表示这个动词不认 --config。只有 import 是这样：它不读清单，
	// 它是把 IDEA 的运行配置产出成清单。与其让它悄悄漏掉，不如在这里写明。
	noConfig bool
}

// commands 是全部动词，顺序即帮助里的顺序。
var commands = []command{
	{
		name: "doctor",
		usage: []usage{
			{"doctor", "检查各语言工具链能否解析（排查「只能在 IDEA 里跑」的问题）"},
		},
		desc: "检查各语言工具链能否解析，并说明选了哪一个、依据是什么。",
		flags: []usage{
			{"--json", "机器读的输出：哪几类没解析出来也写在里面（missing）"},
		},
		notes: []string{
			"退出码恒为 0：缺一套工具只影响依赖它的服务，别的照样能起。",
			"要脚本自己判「缺了哪些」，看 --json 里的 missing。",
		},
	},
	{
		name: "detect",
		usage: []usage{
			{"detect [目录]", "扫描目录，识别项目类型并给出建议的启动项"},
		},
		desc: "扫描一个目录，认出里面有哪些项目、各自该用什么类型和端口。",
		notes: []string{
			"输出的是 YAML 片段，确认后粘到 pier.yaml 的 services 下面。",
			"port 与 health 拿不准时留空并注明，需要照项目实际配置补全。",
		},
	},
	{
		name:     "import",
		usage:    []usage{{"import [目录]", "读取 .idea 运行配置，转换为 Pier 的服务定义"}},
		desc:     "读取 IDEA 的 .idea/workspace.xml，把运行配置转成服务定义。",
		noConfig: true,
		flags: []usage{
			{"--prefix, -p <前缀>", "给导入的服务名加一个前缀，只对单个目录有效"},
			{"-o, --out <文件>", "写到文件而不是打印；相对路径以该文件所在目录为准"},
			{"--force", "输出文件已存在时覆盖它"},
		},
	},
	{
		name:  "up",
		usage: []usage{{"up [服务...]", "启动服务（不带名字则启动全部）"}},
		desc:  "启动服务，等它们就绪后返回。",
		flags: []usage{
			{"--port <端口>", "换一个端口起这一个服务（0 表示自己挑一个空闲的），只这一次，不写回清单"},
		},
		notes: []string{
			"退出码 0 表示全都起来了；1 表示有服务没起来、端口被占跳过、或者起来了但探针没通。",
			"端口已被占用时跳过该服务——它多半已经在 IDEA 或别的终端里跑着了，看是谁占的：pier ports。要的就是这一次换开，用 --port。",
		},
	},
	{
		name:  "down",
		usage: []usage{{"down [服务...]", "停止服务"}},
		desc:  "停止服务（不带名字则停止全部）。",
		notes: []string{
			"「不是 Pier 起的」与「已经退出」只提示，退出码仍是 0——重复执行 down 本来就该是安全的。",
		},
	},
	{
		name:  "restart",
		usage: []usage{{"restart [服务...]", "重启服务"}},
		desc:  "先停后起。",
		notes: []string{
			"停止失败就中止，不再启动：端口还占着的时候再拉一次只会更乱。",
		},
	},
	{
		name:  "wait",
		usage: []usage{{"wait <服务...>", "等这些服务就绪（或超时），退出码说话"}},
		desc:  "等已经跑着的服务通过健康探针。",
		flags: []usage{
			{"--timeout <时长>", "等待上限，默认 180s；写 30s、2m 或者光写秒数都行"},
		},
		notes: []string{
			"退出码 0 是全都探通；1 是有没等到的（超时、没在跑、没配健康探针）——原因逐条写在输出里。",
			"等的是服务进程，不是编译：它得已经在跑（pier up 过、界面起过）。",
		},
	},
	{
		name:  "status",
		usage: []usage{{"status", "列出所有服务的运行状态"}},
		desc:  "列出所有服务的运行状态。",
		flags: []usage{
			{"--json", "与界面同一份状态快照（字段名是对外承诺，只增不改）"},
		},
		notes: []string{
			"--json 时清单没加载成功也照实写在 error 里，退出码为 1。",
		},
	},
	{
		name:  "ports",
		usage: []usage{{"ports", "列出本机正在监听的端口，以及各自是谁起的、在哪个目录"}},
		desc:  "列出本机此刻正在监听的端口，以及每个端口背后是谁。",
		flags: []usage{
			{"--json", "机器读的输出：端口、进程、目录、启动来源、认领情况"},
		},
	},
	{
		name: "logs",
		usage: []usage{
			{"logs <服务>...", "看这些服务最近的日志（-f 跟随，一次只能跟一个）"},
			{"logs --size", "看日志占了多少磁盘"},
			{"logs --clean [服务]", "清理超过 14 天的日志（--all 清空，不看天数）"},
		},
		desc: "看、跟随、清理服务日志。",
		flags: []usage{
			{"-f, --follow", "跟着新增的内容一直打印"},
			{"--tail <行数>", "只看最后几行，默认 " + strconv.Itoa(logTailLines*5)},
			{"--size", "列出每个服务的日志占用，按大小排"},
			{"--clean", "清理；不带服务名就是全部"},
			{"--all", "配合 --clean：不看天数，清空"},
			{"--json", "配合 --size：输出与「日志管理」页同一份统计"},
		},
		notes: []string{
			"清理会跳过正在运行的服务：日志句柄在那个独立进程手里，删了只是从目录里摘掉。",
			"跳过时退出码为 1——想清的那一份没清成。",
			"一次点几个服务就是依次打印，每个前面标出服务名；其中一个读不成（比如它还没跑过）" +
				"不影响其余几个，但退出码为 1。",
		},
	},
	{
		name:  "ui",
		usage: []usage{{"ui", "打开终端里的交互式面板"}},
		desc:  "打开终端里的交互式面板。",
	},
	{
		name:  "api",
		usage: []usage{{"api", "起一个只服务本机的 HTTP 接口"}},
		desc:  "起一个只服务本机的 HTTP 接口。",
		flags: []usage{
			{"--show-token", "打印当前令牌后退出"},
			{"--rotate", "换一份新令牌后退出"},
			{"--port, -p <端口>", "换一个监听端口（默认 7717）"},
			{"--addr <回环主机>", "换一个回环地址（::1、127.0.0.2 这类）"},
		},
		notes: []string{
			"只监听回环地址，别的值当场报错：这个接口能启停进程。",
			"每个请求都要令牌（见 --show-token），OpenAPI 在 GET /openapi.json。",
		},
	},
	{
		name:  "version",
		usage: []usage{{"version", "显示版本号"}},
		desc:  "显示版本号。",
		notes: []string{"pier --version 是同一个意思。"},
	},
	{
		name: "update",
		usage: []usage{
			{"update --check", "查一下有没有新版本，只查不装"},
			{"update", "下载、校验并换上最新版本"},
		},
		desc: "查更新，或把这一份换成最新版。",
		flags: []usage{
			{"--check", "只查不装；退出码 0 已是最新，10 有新版本，1 没查成"},
		},
		notes: []string{
			"界面开着时命令行拒绝动手：先在界面里退出，或用界面里的「重启并安装」。",
		},
	},
}

// commonFlags 是几乎所有动词都认的那一条。import 是例外，理由见 command.noConfig。
var commonFlags = []usage{
	{"--config <清单>", "改用一份 YAML 清单（只读）；写在命令名后面"},
}

// helpFlag 每条命令都认：它在分发之前统一拦下（见 Run），写在哪个位置都算。
var helpFlag = usage{"-h, --help", "显示这份说明"}

// jsonFlag 只在 jsonVerbs 那几个地方认，那句话从那张表渲染出来（见 json.go）。
// 别的动词见到 --json 会明确报错而不是装作没看见：
// 「悄悄忽略一个参数」正是这一版要收拾的那类问题。
var jsonFlag = usage{"--json", "机器读的输出；只有 " + jsonVerbList() + " 认"}

// findCommand 找一条命令的说明书。
func findCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// wantsHelp 判断这一串参数里有没有 -h / --help。
//
// 在命令自己的解析器之前拦：`pier up --help` 会被 up 读成一个服务名，
// 报「没有名为 --help 的服务」——一句看起来像用户打错了的话。
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// printCommands 打印全局帮助。
func printCommands(w io.Writer) {
	fmt.Fprint(w, "Pier —— 统一启停本地多个项目的命令行工具\n\n")
	fmt.Fprintln(w, "用法：")
	fmt.Fprintln(w, "  pier <命令> [参数]")
	fmt.Fprintln(w)

	rows := make([]usage, 0, len(commands)+4)
	for _, c := range commands {
		rows = append(rows, c.usage...)
	}
	// help 不是一件「能做的事」，所以不进 commands，但摆在这一列里最好找。
	rows = append(rows, usage{"help [命令]", "显示帮助（同 pier --help、pier <命令> -h）"})
	fmt.Fprintln(w, "命令：")
	printUsageTable(w, rows)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "通用：")
	printUsageTable(w, append(append([]usage{}, commonFlags...),
		jsonFlag,
		helpFlag,
		usage{"--version", "显示版本号（同 pier version）"},
	))
}

// printCommandHelp 打印一个动词自己的说明。
func printCommandHelp(w io.Writer, c command) {
	fmt.Fprintf(w, "pier %s —— %s\n\n", c.name, c.desc)

	forms := make([]usage, 0, len(c.usage))
	for _, u := range c.usage {
		forms = append(forms, usage{form: "pier " + u.form, desc: u.desc})
	}
	fmt.Fprintln(w, "用法：")
	printUsageTable(w, forms)

	flags := make([]usage, 0, len(c.flags)+2)
	if !c.noConfig {
		flags = append(flags, commonFlags...)
	}
	flags = append(flags, c.flags...)
	flags = append(flags, helpFlag)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "参数：")
	printUsageTable(w, flags)

	if len(c.notes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "说明：")
		for _, n := range c.notes {
			fmt.Fprintln(w, "  "+n)
		}
	}
}

// printUsageTable 打印一张「怎么写 + 做什么」的表。多块之间共用一个列宽，
// 分开算的话两块的文字会各停在一处。
//
// 中文是双宽字符，补空格要按显示宽度而不是字节数，否则含中文的那些行参差不齐。
func printUsageTable(w io.Writer, blocks ...[]usage) {
	width := 0
	for _, b := range blocks {
		for _, r := range b {
			if n := runewidth.StringWidth(r.form); n > width {
				width = n
			}
		}
	}
	for _, b := range blocks {
		for _, r := range b {
			line := "  " + runewidth.FillRight(r.form, width)
			if r.desc != "" {
				line += " " + r.desc
			}
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
	}
}
