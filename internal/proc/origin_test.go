package proc

import (
	"strings"
	"testing"
)

// originTable 把「谁起了谁」写成一串 pid:ppid:命令行，拼出一份假的进程表。
//
// 溯源的全部判断都落在「链上认得出谁」这一件事上，而这跟机器上真正跑着什么无关。
// 拿一份写死的表来验，比在本机真起一个编辑器再去查它可靠得多——那既要装那个
// 编辑器，还得让它在测试里把服务拉起来。
func originTable(rows ...string) map[int]ancestor {
	table := map[int]ancestor{}
	for _, r := range rows {
		pid, rest, ok := cutField(r)
		if !ok {
			panic("用例写错了：" + r)
		}
		ppid, cmd, ok := cutField(rest)
		if !ok {
			panic("用例写错了：" + r)
		}
		table[pid] = ancestor{PID: pid, PPID: ppid, Args: cmd}
	}
	return table
}

// TestOriginFromChain 逐个走一遍真实形状的进程链。
//
// 每一行的期望值都是「用户问『这是谁起的』时想要的那个答案」，而不只是
// 「表里写没写这一条」：集成终端里的 node 要答「终端」而不是「VS Code」，
// 但链上还得留着 VS Code——排查时他记的是当时在哪个窗口里敲的命令。
func TestOriginFromChain(t *testing.T) {
	cases := []struct {
		name  string
		rows  []string
		want  string // 期望的 Label
		chain []string
	}{
		{
			name: "终端里直接跑起来",
			rows: []string{
				"900 1 /sbin/launchd",
				"500 900 /System/Library/CoreServices/.../Terminal.app/Contents/MacOS/Terminal",
				"480 500 -zsh",
				"300 480 node server.js",
			},
			want:  "终端",
			chain: []string{"终端"},
		},
		{
			name: "编辑器里的集成终端",
			rows: []string{
				"900 1 /sbin/launchd",
				"500 900 /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
				"480 500 -zsh",
				"300 480 node server.js",
			},
			want:  "VS Code",
			chain: []string{"VS Code"},
		},
		{
			name: "终端跑在编辑器里（两层都认得出）",
			rows: []string{
				"900 1 /sbin/launchd",
				"600 900 /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
				"500 600 /System/Library/CoreServices/.../Terminal.app/Contents/MacOS/Terminal",
				"480 500 -zsh",
				"300 480 node server.js",
			},
			want:  "终端",
			chain: []string{"终端", "VS Code"},
		},
		{
			name: "编码助理排在编辑器之前",
			rows: []string{
				"900 1 /sbin/launchd",
				"600 900 /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
				"480 600 /usr/local/bin/claude",
				"300 480 node server.js",
			},
			want:  "Claude Code",
			chain: []string{"Claude Code", "VS Code"},
		},
		{
			name: "tmux 套在终端里",
			rows: []string{
				"900 1 /sbin/launchd",
				"500 900 /Applications/iTerm.app/Contents/MacOS/iTerm2",
				"480 500 tmux",
				"300 480 python -m http.server",
			},
			want:  "tmux",
			chain: []string{"tmux", "iTerm"},
		},
		{
			name: "Pier 面板起的服务",
			rows: []string{
				"900 1 /sbin/launchd",
				"700 900 /Users/me/Applications/Pier.app/Contents/MacOS/pier-gui",
				"300 700 node server.js",
			},
			want:  "Pier 面板",
			chain: []string{"Pier 面板"},
		},
		{
			name: "访达双击起来的",
			rows: []string{
				"900 1 /sbin/launchd",
				"500 900 /System/Library/CoreServices/Finder.app/Contents/MacOS/Finder",
				"300 500 ./demo",
			},
			want:  "访达",
			chain: []string{"访达"},
		},
		{
			name: "交给 launchd 托管的",
			rows: []string{
				"900 1 /sbin/launchd",
				"300 900 /usr/local/bin/nginx",
			},
			want:  "系统启动",
			chain: []string{"系统启动"},
		},
		{
			name:  "认不出来就空着",
			rows:  []string{"300 1 node server.js"},
			want:  "",
			chain: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := originFrom(originTable(c.rows...), 300)
			if o.Label != c.want {
				t.Fatalf("Label = %q，想要 %q", o.Label, c.want)
			}
			if got := strings.Join(o.Chain, "|"); got != strings.Join(c.chain, "|") {
				t.Errorf("Chain = %v，想要 %v", o.Chain, c.chain)
			}
			if (c.want != "") != o.OK() {
				t.Errorf("OK() = %v，Label = %q", o.OK(), o.Label)
			}
		})
	}
}

// TestOriginChainHasNoDuplicates 钉着同一个宿主在链上连着出现两次时只写一遍。
// 编辑器常常先起一层自己的 helper 再起终端，链上认出来是同一个人。
func TestOriginChainHasNoDuplicates(t *testing.T) {
	o := originFrom(originTable(
		"900 1 /sbin/launchd",
		"600 900 /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
		"560 600 /Applications/Visual Studio Code.app/Contents/MacOS/Electron",
		"480 560 -zsh",
		"300 480 node server.js",
	), 300)
	if len(o.Chain) != 1 {
		t.Errorf("Chain = %v，想要只出现一次", o.Chain)
	}
}

// TestOriginStopsAtCycle 钉着进程表互相指认时不会转不出来。
//
// 进程表在极端情况下（pid 复用、容器里看到的表不全）会读出环，
// 而这段代码跑在用户点开「端口」那一屏的路上。
func TestOriginStopsAtCycle(t *testing.T) {
	table := originTable(
		"300 400 node server.js",
		"400 300 sh",
	)
	o := originFrom(table, 300)
	if o.OK() {
		t.Errorf("环上不该认出什么，却得到了 %+v", o)
	}
}

// TestAncestorsStartAtTheProcessItself 钉着链首是 pid 自己。
//
// 端口握在谁手里，谁就有可能是那个「被起起来的」东西：直接在终端里跑一个
// node，链上没有别的东西，答案只能是终端，而它正是 node 的父亲。
func TestAncestorsStartAtTheProcessItself(t *testing.T) {
	table := originTable(
		"500 900 terminal",
		"300 500 node server.js",
	)
	got := ancestorsIn(table, 300)
	if len(got) != 2 {
		t.Fatalf("链长 = %d，想要 2：%+v", len(got), got)
	}
	if got[0].PID != 300 || got[1].PID != 500 {
		t.Errorf("链 = %+v，想要从 300 往上", got)
	}
}

// TestAncestorsStopAtPidOne 钉着不会一路走到 pid 1 之外。
func TestAncestorsStopAtPidOne(t *testing.T) {
	got := ancestorsIn(originTable(
		"1 0 /sbin/launchd",
		"500 1 terminal",
		"300 500 node server.js",
	), 300)
	for _, a := range got {
		if a.PID == 1 {
			t.Errorf("链里不该含 pid 1：%+v", got)
		}
	}
}

// TestOriginsSilentAboutUnknown 钉着「认不出来就是没有这一项」，
// 而不是给一个空壳让界面去判断。
func TestOriginsSilentAboutUnknown(t *testing.T) {
	// 没有真进程表时一律返回 nil，一个空壳都不给。
	if got := Origins(nil); got != nil {
		t.Errorf("Origins(nil) = %v，想要 nil", got)
	}
}

// TestOriginMarkHit 验一下标记表本身：名字与路径两条路都要能命中。
func TestOriginMarkHit(t *testing.T) {
	cases := []struct {
		name string
		mark originMark
		args string
		want bool
	}{
		{
			"按可执行文件名",
			originMark{kind: "agent", label: "Codex", execs: []string{"codex"}},
			"/opt/homebrew/bin/codex --foo",
			true,
		},
		{
			"大小写不敏感",
			originMark{kind: "terminal", label: "Kitty", execs: []string{"kitty"}},
			"/Applications/Kitty.app/Contents/MacOS/KITTY",
			true,
		},
		{
			"按路径",
			originMark{kind: "editor", label: "GoLand", paths: []string{"GoLand.app/Contents/MacOS"}},
			"/Applications/GoLand.app/Contents/MacOS/goland",
			true,
		},
		{
			"都不是",
			originMark{kind: "editor", label: "GoLand", paths: []string{"GoLand.app/Contents/MacOS"}},
			"/usr/bin/go run ./cmd/server",
			false,
		},
		{
			"名字要整段相等，不做子串",
			originMark{kind: "agent", label: "Codex", execs: []string{"codex"}},
			"/usr/bin/codex-helper",
			false,
		},
	}
	for _, c := range cases {
		if got := c.mark.hit(ancestor{PID: 1, Args: c.args}); got != c.want {
			t.Errorf("%s：hit(%q) = %v，想要 %v", c.name, c.args, got, c.want)
		}
	}
}

func TestBaseNameAndArgv0(t *testing.T) {
	if got := argv0("  /usr/bin/node   server.js  "); got != "/usr/bin/node" {
		t.Errorf("argv0 = %q", got)
	}
	if got := argv0("node"); got != "node" {
		t.Errorf("argv0 = %q", got)
	}
	if got := argv0(""); got != "" {
		t.Errorf("argv0 = %q", got)
	}
	// 两种分隔符都要认：一张表要盖住三个平台。
	if got := baseName(`C:\Program Files\nodejs\node.exe`); got != "node.exe" {
		t.Errorf("baseName = %q", got)
	}
	if got := baseName("/usr/local/bin/pnpm"); got != "pnpm" {
		t.Errorf("baseName = %q", got)
	}
}
