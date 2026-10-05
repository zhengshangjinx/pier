package cli

import (
	"fmt"

	"github.com/zhengshangjinx/pier/internal/version"
)

// cmdVersion 打印版本号。不问「你是哪一版」以外的任何事，所以永远返回 0：
// 版本号认不出来也不该当成命令出错。
//
// 多出来的词是错的（多半是把 `pier version` 和别的东西连在一起敲了），
// 报出来而不是静默忽略——`pier --version` 这一个写法也是在这里认下的。
func cmdVersion(args []string) int {
	if err := noExtra("version", args); err != nil {
		return fail("%v", err)
	}
	fmt.Println(version.Summary())
	return 0
}
