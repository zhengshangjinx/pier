//go:build !windows

package proc

import "strings"

// 溯源在 unix 这一侧的实现：读一遍进程表，再顺着父进程往上走。
//
// 一次 ps 问全表，而不是每层问一次 ps -p <pid>：后者在链长五六层时就是
// 五六次 exec，而这里只为了回答一个「端口后面是谁」，不该比查端口本身还贵。

// originMarks 是「认得出名字的宿主」清单。顺序即优先级，链上同一环只取第一条命中的。
//
// 为什么认的是宿主而不是服务自己：服务那一条命令行里没有任何身份信息，
// 而宿主是确定的几个。这也正是「光看进程名分不清是谁起的」的原因——
// node、java、python 谁都会用。
var originMarks = []originMark{
	// 编码助理排在编辑器与终端之前。它们本来就跑在某个终端或编辑器里，
	// 而用户问「这是谁起的」时，答案几乎总是最近的那个助理，不是更外面的终端。
	{kind: "agent", label: "Claude Code", execs: []string{"claude"}, paths: []string{"claude-code"}},
	{kind: "agent", label: "Codex", execs: []string{"codex"}},

	// 编辑器。macOS 上一律靠应用包路径认：那些进程的 argv[0] 是 Electron、MacOS
	// 这类通用名字，光看可执行文件名全都一样。
	{kind: "editor", label: "VS Code", execs: []string{"code-insiders"},
		paths: []string{"Visual Studio Code.app/Contents/MacOS", "Visual Studio Code - Insiders.app/Contents/MacOS", "/share/code/code"}},
	{kind: "editor", label: "Cursor", execs: []string{"cursor"}, paths: []string{"Cursor.app/Contents/MacOS"}},
	{kind: "editor", label: "Windsurf", paths: []string{"Windsurf.app/Contents/MacOS"}},
	{kind: "editor", label: "IntelliJ IDEA", paths: []string{"IntelliJ IDEA.app/Contents/MacOS"}},
	{kind: "editor", label: "GoLand", paths: []string{"GoLand.app/Contents/MacOS"}},
	{kind: "editor", label: "WebStorm", paths: []string{"WebStorm.app/Contents/MacOS"}},
	{kind: "editor", label: "PyCharm", paths: []string{"PyCharm.app/Contents/MacOS"}},
	{kind: "editor", label: "PhpStorm", paths: []string{"PhpStorm.app/Contents/MacOS"}},
	{kind: "editor", label: "CLion", paths: []string{"CLion.app/Contents/MacOS"}},
	{kind: "editor", label: "DataGrip", paths: []string{"DataGrip.app/Contents/MacOS"}},
	{kind: "editor", label: "RubyMine", paths: []string{"RubyMine.app/Contents/MacOS"}},
	{kind: "editor", label: "Android Studio", paths: []string{"Android Studio.app/Contents/MacOS"}},
	{kind: "editor", label: "Zed", execs: []string{"zed"}, paths: []string{"Zed.app/Contents/MacOS"}},

	// 终端。macOS 上同样靠应用包路径，Linux 上靠可执行文件名。
	{kind: "terminal", label: "终端", paths: []string{"Terminal.app/Contents/MacOS"},
		execs: []string{"gnome-terminal", "konsole", "xfce4-terminal", "tilix", "xterm", "foot"}},
	{kind: "terminal", label: "iTerm", paths: []string{"iTerm.app/Contents/MacOS", "iTerm2.app/Contents/MacOS"}},
	{kind: "terminal", label: "Warp", paths: []string{"Warp.app/Contents/MacOS"}},
	{kind: "terminal", label: "Tabby", paths: []string{"Tabby.app/Contents/MacOS"}},
	{kind: "terminal", label: "Hyper", paths: []string{"Hyper.app/Contents/MacOS"}},
	{kind: "terminal", label: "Kitty", execs: []string{"kitty"}},
	{kind: "terminal", label: "Alacritty", execs: []string{"alacritty"}},
	{kind: "terminal", label: "WezTerm", execs: []string{"wezterm", "wezterm-gui"}},
	{kind: "terminal", label: "Ghostty", execs: []string{"ghostty"}},

	// 会话复用器：套在终端里，链上比真正的终端更近。认出来更有用：
	// 「在 tmux 里」和「在某个终端窗口里」在找回那个窗口时是两件事。
	{kind: "multiplexer", label: "tmux", execs: []string{"tmux"}},
	{kind: "multiplexer", label: "screen", execs: []string{"screen"}},

	// Pier 自己。面板起的服务，父进程就是界面本体（服务是 setsid 出去的，
	// 中间没有别的壳），所以这一条常常是第一眼就命中的那个。
	{kind: "panel", label: "Pier 面板", execs: []string{"pier-gui", "pier"}},

	{kind: "finder", label: "访达", paths: []string{"Finder.app/Contents/MacOS"}},
	{kind: "remote", label: "SSH 会话", execs: []string{"sshd"}},
	{kind: "system", label: "系统启动", execs: []string{"launchd", "systemd", "init"}, fallback: true},
}

// matchesExec 比对程序名。unix 上 ps 给的就是不带后缀的名字，直接折大小写比。
func matchesExec(base, want string) bool { return strings.EqualFold(base, want) }

// processTable 读一遍全表，形状是 pid → 这一行的父进程与命令行。
func processTable() map[int]ancestor {
	out, err := sysOutput("ps", "-axo", "pid=,ppid=,command=")
	if err != nil {
		return nil
	}
	table := make(map[int]ancestor, 256)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimLeft(line, " \t")
		pid, rest, ok := cutField(line)
		if !ok {
			continue
		}
		ppid, cmd, ok := cutField(rest)
		if !ok {
			continue
		}
		table[pid] = ancestor{PID: pid, PPID: ppid, Args: cmd}
	}
	return table
}
