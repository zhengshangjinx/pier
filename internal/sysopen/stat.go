package sysopen

import "os"

// isExecutable 判断路径是不是一个可执行文件。
func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
