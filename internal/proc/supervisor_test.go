package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

func TestShebangInterp(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		what string
		body string
		want string
		ok   bool
	}{
		{"env 形式", "#!/usr/bin/env node\nconsole.log(1)\n", "node", true},
		{"绝对路径形式", "#!/opt/homebrew/bin/python3.14\n", "python3.14", true},
		{"sh 脚本", "#!/bin/sh\n", "sh", true},
		{"env 带选项", "#!/usr/bin/env -S node --experimental\n", "node", true},
		{"只认第一行", "#!/bin/zsh\n#!/usr/bin/env node\n", "zsh", true},
		{"Mach-O 不是脚本", "\xcf\xfa\xed\xfe\x0c\x00\x00\x01rest", "", false},
		{"空文件", "", "", false},
		{"光有 #!", "#!", "", false},
	}
	for _, c := range cases {
		got, ok := shebangInterp(write("f-"+c.what, c.body))
		if ok != c.ok || got != c.want {
			t.Errorf("%s：得到 (%q, %v)，想要 (%q, %v)", c.what, got, ok, c.want, c.ok)
		}
	}
	if _, ok := shebangInterp(filepath.Join(dir, "不存在")); ok {
		t.Error("打不开的文件不该当成脚本")
	}
}

func TestNamedArgv(t *testing.T) {
	cases := []struct {
		what string
		in   []string
		want []string
	}{
		// sh -c 要补回 $0：不补的话脚本里引用的 $0 会从 /bin/sh 变成服务名。
		{"自定义命令", []string{"/bin/sh", "-c", "sleep 60"}, []string{"web", "-c", "sleep 60", "/bin/sh"}},
		{"普通命令", []string{"/x/node", "run", "dev"}, []string{"web", "run", "dev"}},
		{"已经点名的 sh -c", []string{"/bin/sh", "-c", "echo $0", "自定义"}, []string{"web", "-c", "echo $0", "自定义"}},
	}
	for _, c := range cases {
		got := namedArgv(c.in, "web")
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s：得到 %q，想要 %q", c.what, got, c.want)
		}
	}
	if got := namedArgv(nil, "web"); got != nil {
		t.Errorf("空 argv 应当原样返回，得到 %q", got)
	}
}

// 硬链这一步的两个边界：不能吃掉别人的正经文件，能跟上运行时换版本。
func TestLinkAsService(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	dir := t.TempDir()
	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n  - name: app\n    dir: " + dir + "\n    kind: shell\n    run: \"true\"\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sup := New(cfg)
	if err := os.MkdirAll(cfg.BinDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	// Go 服务的编译产物正好叫 <BinDir>/<服务名>，nlink 是 1。
	// 这里无脑先删再链的话，删掉的就是刚编出来的那个二进制。
	built := filepath.Join(cfg.BinDir(), "app")
	if err := os.WriteFile(built, []byte("编译产物"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := sup.linkAsService(filepath.Join(dir, "某种运行时"), "app"); got != "" {
		t.Errorf("不该顶掉不是硬链的文件，却返回了 %q", got)
	}
	if _, err := os.Stat(built); err != nil {
		t.Fatalf("编译产物被删掉了：%v", err)
	}
	if err := os.Remove(built); err != nil {
		t.Fatal(err)
	}

	// 自己卷上的运行时：链得上，且换版本之后跟着换。
	node1 := filepath.Join(dir, "node-1")
	node2 := filepath.Join(dir, "node-2")
	for p, body := range map[string]string{node1: "node 1", node2: "node 2"} {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := sup.linkAsService(node1, "app")
	if link != built {
		t.Fatalf("链接应当落在 %s，得到 %q", built, link)
	}
	if same, _ := sameContent(link, node1); !same {
		t.Error("链接没有指向那个运行时")
	}
	if again := sup.linkAsService(node1, "app"); again != built {
		t.Errorf("同一个文件再链一次应当原样返回，得到 %q", again)
	}
	if sup.linkAsService(node2, "app") == "" {
		t.Fatal("换版本之后应当重链")
	}
	if same, _ := sameContent(link, node2); !same {
		t.Error("运行时换版本之后链接没跟上")
	}

	// 已经叫这个名字的文件（Go 的产物）不必经手。
	if got := sup.linkAsService(built, "app"); got != built {
		t.Errorf("已经叫服务名的文件应当原样返回 %q，得到 %q", built, got)
	}
}

func sameContent(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

// 执行的命令怎么被换成「叫服务名的那份」：只有 node 会被动，其余原样。
func TestResolveRunExec(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	dir := t.TempDir()
	// 假 node：只为了让「解释器在哪」这一步走得通，形状对不对由断言看。
	fakeNode := filepath.Join(dir, "node")
	if err := os.WriteFile(fakeNode, []byte("\xcf\xfa\xed\xfe假 node"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // 解释器查找会退回 PATH，别受跑测试的 shell 影响

	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n  - name: app\n    dir: " + dir + "\n    kind: shell\n    run: \"true\"\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := cfg.Find("app")
	sup := New(cfg)
	plan := &config.Plan{}
	want := filepath.Join(cfg.BinDir(), "app")
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// node 脚本，且写的是相对路径：文件头要按服务目录去读，不然读的是 Pier 的 cwd。
	write("slow.mjs", "#!/usr/bin/env node\nsetTimeout(() => {}, 1e9)\n")
	got := sup.resolveRunExec([]string{"./slow.mjs", "--flag"}, svc, plan)
	if len(got) != 3 || got[0] != want || got[1] != "./slow.mjs" || got[2] != "--flag" {
		t.Errorf("node 脚本应当展开成 [硬链 原路径 参数]，得到 %q", got)
	}

	// 直接 exec node（自定义 run 写成 `node xxx`）：硬链那一把同样生效。
	got = sup.resolveRunExec([]string{fakeNode, "server.mjs"}, svc, plan)
	if len(got) != 2 || got[0] != want || got[1] != "server.mjs" {
		t.Errorf("node 本身应当换成硬链 %s，得到 %q", want, got)
	}

	// 非 node 的脚本与二进制都不动：换个位置它们就起不来，见 resolveRunExec 的注释。
	sh := write("slow.sh", "#!/bin/sh\nsleep 30\n")
	if got := sup.resolveRunExec([]string{sh, "--flag"}, svc, plan); got[0] != sh || len(got) != 2 {
		t.Errorf("非 node 的脚本应当原样返回，得到 %q", got)
	}
	bin := write("runtime", "\xcf\xfa\xed\xfe二进制")
	if got := sup.resolveRunExec([]string{bin, "run", "dev"}, svc, plan); got[0] != bin || len(got) != 3 {
		t.Errorf("非 node 的二进制应当原样返回，得到 %q", got)
	}

	// 解释器找不到：原样退回，一个字都不改。
	weird := write("weird", "#!/usr/bin/env 没这个解释器\n")
	if got := sup.resolveRunExec([]string{weird}, svc, plan); got[0] != weird || len(got) != 1 {
		t.Errorf("解释器解不动时应当原样返回，得到 %q", got)
	}
}

// 起一个真服务，看它在系统里叫什么。这是这次改动的核心断言：
// ps 的 COMMAND 列（pgrep / pkill -x / killall 认的那一列）必须是服务名。
func TestStartNamesProcessAfterService(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir()) // 别碰真实数据
	dir := t.TempDir()
	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n  - name: probe-name\n    dir: " + dir + "\n    kind: shell\n    run: \"sleep 30\"\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := cfg.Find("probe-name")
	sup := New(cfg)
	if err := sup.Start(svc); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer func() { _ = sup.Stop("probe-name") }()

	st, err := LoadState(cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}
	e := st.Services["probe-name"]
	if e == nil {
		t.Fatal("状态文件里没有这条记录")
	}
	out, err := sysOutput("ps", "-p", strconv.Itoa(e.PID), "-o", "comm=")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "probe-name" {
		t.Errorf("进程在 ps 里叫 %q，应当是服务名 probe-name", got)
	}
}

// 启动写下的日志落在「服务名/日期」那一格里，而且同一天重启是追加而不是清空。
//
// 这条是分天存储的要害：以前每次启动都把文件清空，「日志里只有这次的输出」
// 是免费得到的；改成追加之后，这个保证改由启动标记 + TrimToLastRun 提供，
// 写的一端和读的一端必须对得上。
func TestStartAppendsToTodaysLog(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir()) // 别碰真实数据
	dir := t.TempDir()
	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n  - name: log-probe\n    dir: " + dir + "\n    kind: shell\n    run: \"echo 第一次\"\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := cfg.Find("log-probe")

	run := func() {
		sup := New(cfg)
		if err := sup.Start(svc); err != nil {
			t.Fatalf("启动失败：%v", err)
		}
		// 命令是 echo，起来就结束了。等它写完再读，不然读到的是半截。
		waitFor(t, 3*time.Second, func() bool {
			st, err := LoadState(cfg.StatePath())
			if err != nil {
				return false
			}
			e := st.Services["log-probe"]
			return e == nil || !ProcessAlive(e.PID)
		})
	}

	run()
	run()

	// 日志在服务自己的目录里，文件名就是今天。
	path := cfg.LogPath("log-probe")
	if want := filepath.Join(cfg.LogDir(), "log-probe", time.Now().Format(config.LogDateLayout)+".log"); path != want {
		t.Fatalf("日志路径 = %q，想要 %q", path, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读日志失败：%v", err)
	}
	text := string(raw)

	// 两次启动留两个标记：追加而不是覆盖。
	if n := strings.Count(text, LogStartMarker("log-probe")); n != 2 {
		t.Errorf("日志里有 %d 个启动标记，想要 2 个（同一天重启应当是追加）：\n%s", n, text)
	}
	// 每次运行的抬头（「--- 运行：」那行）也在，说明前一次的内容没有被清掉。
	if n := strings.Count(text, "--- 运行：echo 第一次"); n != 2 {
		t.Errorf("两次运行的抬头都该在，实际出现 %d 次：\n%s", n, text)
	}

	// 读的时候截到最后一次：上面的旧输出不能混进来，否则读的人会把它
	// 当成这次失败的原因。
	tail := TrimToLastRun(text, "log-probe")
	if strings.Count(tail, LogStartMarker("log-probe")) != 1 {
		t.Errorf("截取之后应当只剩最后一次运行的标记：\n%s", tail)
	}
}

// waitFor 轮询直到条件成立或超时。
func waitFor(t *testing.T, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s 内条件一直没有成立", d)
}

// 端口握在子进程手里时也要认得出来——记录在案的 PID 只是那个壳。
func TestManagedNameMatchesWholeProcessGroup(t *testing.T) {
	self, pgid := os.Getpid(), syscall.Getpgrp()
	st := &State{Services: map[string]*Entry{
		"mine": {PID: self, PGID: pgid},
	}}
	if got := ManagedName(st, self); got != "mine" {
		t.Errorf("自己的 PID 应当认成 mine，得到 %q", got)
	}
	if got := ManagedName(st, 1); got != "" {
		t.Errorf("别的进程组的进程不该被认领，得到 %q", got)
	}
	if got := ManagedName(&State{Services: map[string]*Entry{"x": nil}}, self); got != "" {
		t.Errorf("空记录不该被认领，得到 %q", got)
	}
	if got := ManagedName(nil, self); got != "" {
		t.Errorf("没有状态文件时不该认领，得到 %q", got)
	}
}
