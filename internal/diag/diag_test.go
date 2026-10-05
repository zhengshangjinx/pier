package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// 规则表的用例：给一段日志，期望认得出哪一句。
//
// 写成表而不是「一条规则一个测试」，是因为这张表最要紧的性质是**彼此的先后**
// （编译类的两句话谁都可能先出现），单独测每一条测不出这个。
func TestScan(t *testing.T) {
	cases := []struct {
		name   string
		log    string
		want   string // 期望的 Reason，空表示认不出来
		line   string // 期望抄回来的原文（want 非空时）
		reason string // 这一条为什么这么期望
	}{
		{
			name: "Go 的端口冲突",
			log: `go: downloading github.com/spf13/cobra
2026/10/05 10:00:00 listen tcp :8080: bind: address already in use
exit status 1`,
			want: "端口被占着",
			line: "2026/10/05 10:00:00 listen tcp :8080: bind: address already in use",
		},
		{
			name: "Node 的端口冲突",
			log:  "Error: listen EADDRINUSE: address already in use :::3000\n    at Server.setupListenHandle",
			want: "端口被占着",
			line: "Error: listen EADDRINUSE: address already in use :::3000",
		},
		{
			name: "Windows 的端口冲突（中文系统那句）",
			log:  "listen tcp :8080: bind: 通常每个套接字地址(协议/网络地址/端口)只允许使用一次。",
			want: "端口被占着",
			line: "listen tcp :8080: bind: 通常每个套接字地址(协议/网络地址/端口)只允许使用一次。",
		},
		{
			name: "找不到命令（unix）",
			log:  "sh: line 1: pnpm: command not found",
			want: "找不到命令",
			line: "sh: line 1: pnpm: command not found",
		},
		{
			name: "找不到命令（Windows 中文控制台）",
			log:  "'mvn' 不是内部或外部命令，也不是可运行的程序或批处理文件。",
			want: "找不到命令",
			line: "'mvn' 不是内部或外部命令，也不是可运行的程序或批处理文件。",
		},
		{
			name: "找不到命令（Go 自己那句）",
			log:  `exec: "pnpm": executable file not found in $PATH`,
			want: "找不到命令",
			line: `exec: "pnpm": executable file not found in $PATH`,
		},
		{
			name: "依赖没装",
			log:  "Error: Cannot find module 'express'\nRequire stack:\n- /app/server.js",
			want: "依赖没装",
			line: "Error: Cannot find module 'express'",
		},
		{
			name: "JDK 版本对不上",
			log:  "java.lang.UnsupportedClassVersionError: com/example/App has been compiled by a more recent version",
			want: "JDK 版本对不上",
			line: "java.lang.UnsupportedClassVersionError: com/example/App has been compiled by a more recent version",
		},
		{
			name: "Maven 里的 JDK 版本",
			log:  "[ERROR] invalid target release: 21",
			want: "JDK 版本对不上",
			line: "[ERROR] invalid target release: 21",
		},
		{
			// 这一条是规则顺序的用例：一次拉不到依赖的构建里，「拉了谁失败」和
			// 「BUILD FAILURE」都会出现，而后者只是结论。要报的是前者。
			name: "拉不到依赖排在构建失败前面",
			log: `[INFO] Building demo 1.0
[ERROR] Failed to execute goal on project demo: Could not resolve dependencies for project com.example:demo:jar:1.0: Failure to find com.example:common:jar:1.0
[INFO] BUILD FAILURE
[INFO] Total time:  2.145 s`,
			want: "拉不到依赖（私服或代理不通）",
			line: "[ERROR] Failed to execute goal on project demo: Could not resolve dependencies for project com.example:demo:jar:1.0: Failure to find com.example:common:jar:1.0",
		},
		{
			name: "编译没过（符号找不到）",
			log:  "src/main/java/App.java:12: error: cannot find symbol\n  symbol:   variable port\n[INFO] BUILD FAILURE",
			want: "编译没过",
			line: "src/main/java/App.java:12: error: cannot find symbol",
		},
		{
			name: "编译没过（语法错）",
			log:  "  File \"app.py\", line 3\n    def f(\n         ^\nSyntaxError: invalid syntax",
			want: "编译没过",
			line: "SyntaxError: invalid syntax",
		},
		{
			name: "权限不够",
			log:  "sh: ./gradlew: Permission denied",
			want: "权限不够",
			line: "sh: ./gradlew: Permission denied",
		},
		{
			name: "被系统杀了",
			log:  "Killed",
			want: "被系统杀了",
			line: "Killed",
		},
		{
			name: "内存不够",
			log:  "npm ERR! Cannot allocate memory",
			want: "被系统杀了",
			line: "npm ERR! Cannot allocate memory",
		},
		{
			// 同一个错误被打了好几遍（每个 worker 各报一次），要报最后那次：
			// 日志是顺着写的，越靠后越接近停下那一刻。
			name: "同一条规则命中多处时报最后一行",
			log:  "bind: address already in use\n重试中…\nbind: address already in use",
			want: "端口被占着",
			line: "bind: address already in use",
		},
		{
			name: "换行是 CRLF、前面有缩进",
			log:  "TypeError: Cannot find module 'x'\r\n    at Object.<anonymous>\r\n",
			want: "依赖没装",
			line: "TypeError: Cannot find module 'x'",
		},
		{
			name: "什么都没发生",
			log:  "编译完成\n服务已就绪 http://127.0.0.1:8080/health\n",
			want: "",
		},
		{
			name: "空日志",
			log:  "",
			want: "",
		},
		{
			name: "只有启动标记",
			log:  "=== api 启动于 2026-10-05 10:00:00\n",
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hit, ok := Scan(c.log)
			if c.want == "" {
				if ok {
					t.Fatalf("不该认出什么，却给了「%s」（原文：%s）", hit.Reason, hit.Line)
				}
				return
			}
			if !ok {
				t.Fatalf("没认出来，期望「%s」", c.want)
			}
			if hit.Reason != c.want {
				t.Errorf("原因 = %q，期望 %q", hit.Reason, c.want)
			}
			if c.line != "" && hit.Line != c.line {
				t.Errorf("原文 = %q，期望 %q", hit.Line, c.line)
			}
			// 两句必须都给：只有原因没有下一步，等于把问题原样丢回给用户。
			if hit.Next == "" {
				t.Error("下一步是空的")
			}
		})
	}
}

// 原文要抄得下，也不能没有上限：一行的长度由被启动的程序决定，
// Java 的栈和 Maven 的依赖树都能写出一屏宽的行。
func TestClip(t *testing.T) {
	long := "Error: Cannot find module '" + strings.Repeat("x", maxLine*2) + "'"
	hit, ok := Scan(long)
	if !ok {
		t.Fatal("这么长的一行也该认得出来")
	}
	if got := len([]rune(hit.Line)); got != maxLine+1 {
		t.Errorf("截断后 %d 个字符，期望 %d（含省略号）", got, maxLine+1)
	}
	if !strings.HasSuffix(hit.Line, "…") {
		t.Error("截断了却没有省略号，看着像原文本来就在那儿断的")
	}

	// 中文不能在中间劈开：按字节截会把一个汉字切成两半，显示出来是乱码。
	cn := "Error: Cannot find module '" + strings.Repeat("中", maxLine*2) + "'"
	hit, _ = Scan(cn)
	if strings.ContainsRune(hit.Line, '�') {
		t.Error("截断处出现了替换字符，说明是按字节切的")
	}
}

// 读日志这一段：路径、截取、认哪一次运行，都靠这一条钉住。
func TestFromLog(t *testing.T) {
	cfg := testConfig(t)
	name := "api"
	dir := cfg.LogDirFor(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := cfg.LogPath(name)

	write := func(parts ...string) {
		if err := os.WriteFile(path, []byte(strings.Join(parts, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("没有日志文件", func(t *testing.T) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if _, ok := FromLog(cfg, name); ok {
			t.Error("还没跑过的服务不该报出诊断")
		}
	})

	t.Run("上一次运行的错误不算这一次的", func(t *testing.T) {
		// 同一天的多次启动追加在同一份文件里。上面那次是端口冲突，
		// 而这一次只是编译慢了点——把上面那次的话报出来，用户会去查一个
		// 早就不存在的问题。
		write(
			proc.LogStartMarker(name)+" 2026-10-05 09:00:00",
			"listen tcp :8080: bind: address already in use",
			proc.LogStartMarker(name)+" 2026-10-05 10:00:00",
			"编译完成，正在等待健康检查…",
		)
		if hit, ok := FromLog(cfg, name); ok {
			t.Errorf("报出了上一次运行的原因：%s（原文：%s）", hit.Reason, hit.Line)
		}
	})

	t.Run("这一次的能认出来", func(t *testing.T) {
		write(
			proc.LogStartMarker(name)+" 2026-10-05 09:00:00",
			"listen tcp :8080: bind: address already in use",
			proc.LogStartMarker(name)+" 2026-10-05 10:00:00",
			"listen tcp :8080: bind: address already in use",
		)
		hit, ok := FromLog(cfg, name)
		if !ok {
			t.Fatal("这一次的端口冲突没认出来")
		}
		if hit.Reason != "端口被占着" {
			t.Errorf("原因 = %q，期望「端口被占着」", hit.Reason)
		}
	})
}

// testConfig 造一份临时清单，拿它的日志目录。PIER_HOME 一并换到临时目录：
// 测试绝不碰真实数据（仓库里所有测试都是这条规矩）。
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())
	dir := t.TempDir()
	yaml := "services:\n  - name: api\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n"
	path := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
