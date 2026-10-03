//go:build windows

package proc

import "strings"

// 溯源在 Windows 这一侧的实现。
//
// 父进程链直接来自进程快照（Toolhelp32），不必像 unix 那样 exec 一个 ps：
// 那边没有全表的系统调用，这边有。

// originMarks 是「认得出名字的宿主」清单。顺序即优先级，链上同一环只取第一条命中的。
//
// 只按可执行文件名认：Windows 上拿不到别的进程的命令行（要读 PEB，见 processTimes），
// 而没有命令行就无从匹配路径片段。好在 Windows 上这个前提成立——宿主进程的名字
// 本来就各不相同（Code.exe、WindowsTerminal.exe），不像 macOS 上那样清一色 Electron。
var originMarks = []originMark{
	// 编码助理排在编辑器与终端之前。它们本来就跑在某个终端或编辑器里，
	// 而用户问「这是谁起的」时，答案几乎总是最近的那个助理，不是更外面的终端。
	{kind: "agent", label: "Claude Code", execs: []string{"claude"}},
	{kind: "agent", label: "Codex", execs: []string{"codex"}},

	{kind: "editor", label: "VS Code", execs: []string{"Code", "Code - Insiders"}},
	{kind: "editor", label: "Cursor", execs: []string{"Cursor"}},
	{kind: "editor", label: "Windsurf", execs: []string{"Windsurf"}},
	{kind: "editor", label: "IntelliJ IDEA", execs: []string{"idea64", "idea"}},
	{kind: "editor", label: "GoLand", execs: []string{"goland64", "goland"}},
	{kind: "editor", label: "WebStorm", execs: []string{"webstorm64", "webstorm"}},
	{kind: "editor", label: "PyCharm", execs: []string{"pycharm64", "pycharm"}},
	{kind: "editor", label: "PhpStorm", execs: []string{"phpstorm64", "phpstorm"}},
	{kind: "editor", label: "CLion", execs: []string{"clion64", "clion"}},
	{kind: "editor", label: "DataGrip", execs: []string{"datagrip64", "datagrip"}},
	{kind: "editor", label: "RubyMine", execs: []string{"rubymine64", "rubymine"}},
	{kind: "editor", label: "Android Studio", execs: []string{"studio64"}},
	{kind: "editor", label: "Zed", execs: []string{"zed"}},
	{kind: "editor", label: "Notepad++", execs: []string{"notepad++"}},

	{kind: "terminal", label: "Windows 终端", execs: []string{"WindowsTerminal", "wt"}},
	{kind: "terminal", label: "PowerShell", execs: []string{"powershell", "pwsh"}},
	{kind: "terminal", label: "命令提示符", execs: []string{"cmd"}},
	{kind: "terminal", label: "Windows PowerShell ISE", execs: []string{"powershell_ise"}},
	{kind: "terminal", label: "ConEmu", execs: []string{"ConEmu", "ConEmu64"}},
	{kind: "terminal", label: "Cmder", execs: []string{"Cmder"}},
	{kind: "terminal", label: "Git Bash", execs: []string{"bash", "mintty"}},
	{kind: "terminal", label: "Alacritty", execs: []string{"alacritty"}},
	{kind: "terminal", label: "WezTerm", execs: []string{"wezterm", "wezterm-gui"}},

	{kind: "multiplexer", label: "tmux", execs: []string{"tmux"}},

	// Pier 自己。面板起的服务，父进程就是界面本体（服务不跟着 Pier 退出，
	// 但那说的是 Pier 走的时候不带它，起的时候父进程仍然是 Pier）。
	{kind: "panel", label: "Pier 面板", execs: []string{"pier-gui", "pier"}},

	{kind: "finder", label: "资源管理器", execs: []string{"explorer"}},
	{kind: "remote", label: "SSH 会话", execs: []string{"sshd"}},
	{kind: "system", label: "系统启动", execs: []string{"services", "wininit", "svchost"}},
}

// processTable 把进程快照转成溯源要的形状。
//
// Args 一律给可执行文件名，拿不到全路径：读别的进程的镜像路径要多一次
// OpenProcess + QueryFullProcessImageName，而这一份只用来认表里那几个名字，
// 名字够用。取不到的（受保护进程）留空，不会命中任何一条标记。
func processTable() map[int]ancestor {
	rows, _ := processSnapshot()
	if rows == nil {
		return nil
	}
	table := make(map[int]ancestor, len(rows))
	for pid, r := range rows {
		table[pid] = ancestor{PID: r.pid, PPID: r.ppid, Args: r.exe}
	}
	return table
}

// matchesExec 比对程序名。快照给的是 Code.exe，而表里写的是 Code，
// 后缀在这里统一去掉，省得表里每一条都要记着自己该不该带 .exe。
func matchesExec(base, want string) bool {
	return strings.EqualFold(strings.TrimSuffix(base, ".exe"), want)
}
