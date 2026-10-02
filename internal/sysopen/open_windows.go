//go:build windows

package sysopen

import (
	"os"
	"path/filepath"
)

// command 给出在 Windows 上打开一个路径要跑的命令。
//
// 两种口径：
//   - reveal：`explorer.exe /select,<路径>`，弹出资源管理器并选中它（对应 macOS 的 open -R）。
//     `/select,` 与路径之间**没有空格**，写惯了 unix 的参数很容易在这儿加一个。
//   - 其它：`cmd /c start "" <路径>`。那个空串是 start 的窗口标题占位——
//     不给它，start 会把第一个带引号的参数（也就是路径本身）当成标题，路径就丢了。
func command(target string, reveal bool) (string, []string, error) {
	sys, err := findBin(system32("explorer.exe"), "explorer")
	if err != nil {
		return "", nil, err
	}
	if reveal {
		return sys, []string{"/select," + target}, nil
	}
	// start 是 cmd 的内置命令，不是可执行文件，所以要连着 cmd 一起找。
	shell, err := findBin(system32("cmd.exe"), "cmd")
	if err != nil {
		return "", nil, err
	}
	return shell, []string{"/c", "start", "", target}, nil
}

// system32 拼 %SystemRoot%\System32 下的绝对路径；变量没设时返回空（交给 PATH 回落）。
func system32(name string) []string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return nil
	}
	return []string{filepath.Join(root, "System32", name)}
}
