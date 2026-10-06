// Package cli 负责命令分发与终端输出。
package cli

import (
	"fmt"
	"os"

	"github.com/zhengshangjinx/pier/internal/update"
)

// handlers 是动词到实现的表。它比 help.go 的 commands 只多一条：
// update.ApplyVerb（更新助手的入口，不是给人敲的，见下面那条注释）。
//
// help 不在表里，也不在 commands 里——它解释的是命令，不是一件能做的事，
// 由 Run 直接接住（printCommands / printCommandHelp）。它的用法写在全局帮助里。
//
// 两张表对不对得上由 help_test.go 守着：漏一条，帮助里就会写着一条敲了没反应的命令。
var handlers = map[string]func([]string) int{
	"doctor":         cmdDoctor,
	"detect":         cmdDetect,
	"import":         cmdImport,
	"up":             cmdUp,
	"down":           cmdDown,
	"restart":        cmdRestart,
	"wait":           cmdWait,
	"status":         cmdStatus,
	"ports":          cmdPorts,
	"logs":           cmdLogs,
	"ui":             cmdUI,
	"api":            cmdApi,
	"mcp":            cmdMcp,
	"version":        cmdVersion,
	"update":         cmdUpdate,
	update.ApplyVerb: update.RunHelper,
}

// extraVerbs 是 handlers 里有、commands 里没有的那几个，供一致性测试比对。
// 单列一张表而不是在测试里写死：多一个隐藏动词时，忘的是改这里，而不是改测试。
var extraVerbs = []string{update.ApplyVerb}

// Run 分发子命令并返回进程退出码。
func Run(args []string) int {
	if len(args) == 0 {
		printCommands(os.Stdout)
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		// `pier help <命令>` 与 `pier <命令> -h` 说的是同一件事，
		// 摆两条路是因为两种写法都太常见了，缺一条就会被当成打错。
		if len(rest) > 0 {
			return helpFor(rest[0])
		}
		printCommands(os.Stdout)
		return 0
	case "--version", "-v":
		// `--version` 不是命令，是通用的那一类开关，所以在这里拦、不进 handlers。
		return cmdVersion(nil)
	}

	h, ok := handlers[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n", cmd)
		printCommands(os.Stderr)
		return 2
	}
	if wantsHelp(rest) {
		// 在动词自己的解析器之前拦：`pier up --help` 会被 up 读成一个服务名，
		// 报「没有名为 --help 的服务」——一句看起来像用户打错了的话。
		if c, ok := findCommand(cmd); ok {
			printCommandHelp(os.Stdout, c)
			return 0
		}
	}
	if wantsJSON(rest) && !acceptsJSON(cmd) {
		// 不认 --json 的动词当场说清楚。放它过去的话，up 会把它当服务名，
		// version 会当没看见——两种都只是让脚本拿到一份不是 JSON 的东西。
		return rejectJSON(cmd)
	}
	return h(rest)
}

// helpFor 打印一个动词的说明。认不出来就是打错了，按未知命令处理。
func helpFor(name string) int {
	c, ok := findCommand(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n", name)
		printCommands(os.Stderr)
		return 2
	}
	printCommandHelp(os.Stdout, c)
	return 0
}

// fail 打印错误并返回退出码 1。
func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "错误："+format+"\n", a...)
	return 1
}
