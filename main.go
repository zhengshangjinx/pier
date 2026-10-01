// Pier 用一个命令行统一启停本地多个项目（Go / Java / Python / Node），
// 以代替在 IDEA 里逐个点运行。工具本身零运行时依赖：编译出的单一二进制即可使用。
package main

import (
	"os"

	"github.com/zhengshangjinx/pier/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
