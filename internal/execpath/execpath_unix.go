//go:build !windows

package execpath

import "os"

// platformExts 在 unix 上是空的：命令名就是文件名，没有后缀要补。
// Windows 那份里它是一串扩展名，见 execpath_windows.go。
var platformExts []string

// runnable 看执行位。三个权限位里任何一位为真即认：
// 一个「用户可执行、组不可执行」的文件照样跑得起来，卡 0o111 的某一位会漏掉它。
func runnable(_ string, fi os.FileInfo) bool {
	return fi.Mode()&0o111 != 0
}
