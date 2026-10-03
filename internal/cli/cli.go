// Package cli 负责命令分发与终端输出。
package cli

import (
	"fmt"
	"os"
)

const usageText = `Pier —— 统一启停本地多个项目的命令行工具

用法：
  pier <命令> [参数]

命令：
  doctor              检查各语言工具链能否解析（排查「只能在 IDEA 里跑」的问题）
  detect [目录]       扫描目录，识别项目类型并给出建议的启动项
  import [目录]       读取 .idea 运行配置，转换为 Pier 的服务定义
  up [服务...]        启动服务（不带名字则启动全部）
  down [服务...]      停止服务
  restart [服务...]   重启服务
  status              列出所有服务的运行状态
  ports               列出本机正在监听的端口，以及各自是谁起的、在哪个目录
  logs <服务>         查看某个服务的日志（-f 跟随）
  logs --size         查看日志占了多少磁盘
  logs --clean [服务] 清理超过 14 天的日志（--all 清空，不看天数）
  ui                  打开交互式面板
  api                 起一个只服务本机的 HTTP 接口（--show-token / --rotate / --port N）

通用：
  -h, --help          显示本帮助
`

func usage() {
	fmt.Fprint(os.Stdout, usageText)
}

// Run 分发子命令并返回进程退出码。
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "doctor":
		return cmdDoctor(rest)
	case "detect":
		return cmdDetect(rest)
	case "import":
		return cmdImport(rest)
	case "ui":
		return cmdUI(rest)
	case "api":
		return cmdApi(rest)
	case "up":
		return cmdUp(rest)
	case "down":
		return cmdDown(rest)
	case "restart":
		return cmdRestart(rest)
	case "status":
		return cmdStatus(rest)
	case "ports":
		return cmdPorts(rest)
	case "logs":
		return cmdLogs(rest)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n", cmd)
		usage()
		return 2
	}
}

// fail 打印错误并返回退出码 1。
func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "错误："+format+"\n", a...)
	return 1
}
