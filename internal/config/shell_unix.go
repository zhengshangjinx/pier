//go:build !windows

package config

// shellPrefix 是 unix 上跑一整条命令的方式。用绝对路径 /bin/sh，
// 不去 PATH 上找（从访达启动时 PATH 只有 /usr/bin:/bin）。
var shellPrefix = []string{"/bin/sh", "-c"}
