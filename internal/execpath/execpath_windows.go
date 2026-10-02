//go:build windows

package execpath

import (
	"os"
	"path/filepath"
	"strings"
)

// platformExts 是 Windows 上「这算一个能直接跑的程序」的扩展名。
//
// 取自 PATHEXT 的常见取值，但写死在这里、不去读那个环境变量：
// 用户可以把 PATHEXT 改掉（去掉 .CMD 之类），而 Pier 自己解析工具链是为了
// 不依赖环境——引用了 PATHEXT，从资源管理器启动与从终端启动就会得到两份结果。
//
// 顺序由长到短没有意义（.exe 与 .cmd 不会同名共存），按常见程度排即可。
var platformExts = []string{".exe", ".cmd", ".bat", ".com"}

// runnable 看扩展名。
//
// 这里**不能**看执行位：os.Stat 在 Windows 上对所有普通文件都返回 0666，
// 检查 0o111 的话每个 .exe 都会被判成不可执行——而那正是「装了却找不到」的成因。
func runnable(path string, _ os.FileInfo) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range platformExts {
		if ext == e {
			return true
		}
	}
	return false
}
