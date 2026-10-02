// Package execpath 回答「这个文件算不算一个能直接跑的可执行文件」。
//
// 单独成一个包，和 sysopen 是同一个理由：这条规则各平台不一样，而问它的地方有三个
// ——proc 把命令名解析成绝对路径、toolchain 扫本机装了哪些 SDK、sysopen 找系统自带的
// 打开工具。各写一份的话，漏掉任何一份的症状都是「明明装了却找不到」，
// 那是最难排查的一类；而三份里的差异，靠读代码是看不出来的。
//
// 两个平台的分法：
//
//   - unix：看文件的执行位。0o111 里有任何一位就认。
//   - Windows：没有执行位这回事——os.Stat 对所有普通文件都返回 0666，
//     带上 .exe 也一样。认的是扩展名（同 PATHEXT 的常见取值）。
//     不认的话，Windows 上一个可执行文件都找不出来。
package execpath

import (
	"os"
	"path/filepath"
)

// Is 判断路径是不是一个可执行文件。
func Is(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return runnable(path, fi)
}

// Names 列出「在某个目录里找一个叫 name 的命令」时要依次试的文件名。
//
// unix 上就是它自己：可执行位说了算，不靠后缀。Windows 上要先补扩展名——
// 那边的 mvn 叫 mvn.cmd、node 叫 node.exe，不补就一个也找不到。
// 不带后缀的原名排在最后：真有那么一个文件时也得认。
func Names(name string) []string {
	out := make([]string, 0, len(platformExts)+1)
	for _, ext := range platformExts {
		out = append(out, name+ext)
	}
	// 原名排在最后：真有那么一个不带后缀的文件时也得认。
	return append(out, name)
}

// First 在一串目录里找第一个可用的命令，返回完整路径；找不到返回空串。
//
// 目录按给的顺序找，靠前的优先——调用方据此把「解析出来的工具链目录」排在 PATH 前面。
func First(dirs []string, name string) string {
	names := Names(name)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		for _, n := range names {
			if cand := filepath.Join(d, n); Is(cand) {
				return cand
			}
		}
	}
	return ""
}

// FirstIn 在同一个目录里按顺序试几个命令名，返回第一个可用的。
//
// 与 First 相对：那是「一个命令名、多个目录」，这是「一个目录、多个命令名」，
// python3 / python 这种同一件东西的两个名字就属于这种。
func FirstIn(dir string, names ...string) string {
	for _, n := range names {
		for _, cand := range Names(n) {
			p := filepath.Join(dir, cand)
			if Is(p) {
				return p
			}
		}
	}
	return ""
}
