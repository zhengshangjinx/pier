package cli

import (
	"fmt"

	"github.com/zhengshangjinx/pier/internal/version"
)

// cmdVersion 打印版本号。永远返回 0：问一句「你是哪一版」没有失败的情形，
// 脚本里也不该因为版本号认不出来就当成命令出错。
func cmdVersion(args []string) int {
	fmt.Println(version.Summary())
	return 0
}
