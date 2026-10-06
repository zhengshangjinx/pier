package proc

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 三种探针各探一次。这一层只回答「此刻通不通」，判定的是同一件事的三条路：
// 一个地址能不能 GET 通、一个端口后面有没有人在听、一条命令的退出码是不是 0。
// 各自写错都会以「服务一直启动中」的样子出现，而那是看不出来哪儿错了的。

func TestProbeHTTP(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	if !ProbeHealth("", ok.URL) {
		t.Errorf("204 应当算就绪：%s", ok.URL)
	}
	if ProbeHealth("", bad.URL) {
		t.Error("500 不该算就绪")
	}
	// 关掉的端口连不上，这是最常见的「还没起来」。
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()
	if ProbeHealth("", dead.URL) {
		t.Error("服务已经关了还探通了")
	}
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	if !ProbeHealth("", "tcp://"+ln.Addr().String()) {
		t.Errorf("有人在听的端口应当算就绪：%s", ln.Addr())
	}
	// 端口空着：连不上就是没就绪。挑一个刚关掉的端口，别的进程正好也用上的概率极低。
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	_ = closed.Close()
	if ProbeHealth("", "tcp://"+addr) {
		t.Errorf("没人听的端口不该算就绪：%s", addr)
	}
}

func TestProbeCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("下面的命令写的是 sh 那一套")
	}
	// 退出码就是全部：输出一律丢掉，探针只回答一个是非题。
	if !ProbeHealth("", "cmd: sh -c 'echo hi; exit 0'") {
		t.Error("退出码 0 应当算就绪")
	}
	if ProbeHealth("", "cmd: sh -c 'echo 出事了 >&2; exit 3'") {
		t.Error("退出码非 0 不该算就绪")
	}
	// 命令本身跑不起来（可执行文件不存在）也是「没就绪」，不能反过来算通过：
	// 一个写错名字的探针命令会让每个服务都显示成已就绪。
	if ProbeHealth("", "cmd: 这个命令不存在-9f3a") {
		t.Error("跑不起来的命令不该算就绪")
	}
}

// TestProbeCmdIsBounded 钉着探针命令有超时。
//
// 命令卡住时探针必须自己收场：它是每 500ms 一次轮询里的那一次，
// 一次卡死会把整条等待链拖住，而界面上只会看到「启动中」停在那儿。
func TestProbeCmdIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("下面的命令写的是 sh 那一套")
	}
	start := time.Now()
	if ProbeHealth("", "cmd: sleep 30") {
		t.Error("没跑完的命令不该算就绪")
	}
	if d := time.Since(start); d > 3*healthTimeout {
		t.Errorf("探一次用了 %s，超时没生效", d)
	}
}

// TestProbeGarbageIsNotReady 钉着解析不了的探针当作「没就绪」而不是报错。
//
// 清单在加载时已经拦过一遍；走到这儿的多半是状态文件里留下的旧值。
// 为它中断启动不值得，但绝不能算通过。
func TestProbeGarbageIsNotReady(t *testing.T) {
	for _, raw := range []string{"", "   ", "localhost:8080/health", "ftp://example.com/x", "cmd:"} {
		if ProbeHealth("", raw) {
			t.Errorf("%q 不该算就绪", raw)
		}
	}
}

// TestDepProbePrefersTheRunningPort 钉着等前置时按「此刻在跑的那一份」探。
//
// 前置上一次是从别的端口让路起的话，清单里那个端口上站着的可能是别人——
// 探它会得到一个与这次启动无关的答案，而那个答案多半是「通了」。
func TestDepProbePrefersTheRunningPort(t *testing.T) {
	dir := t.TempDir()
	// 走 YAML 而不是数据文件：清单与状态文件都落在临时目录里（状态是清单旁边的
	// .pier/state.json），一个字节都不碰真实数据，也不必设 PIER_HOME。
	manifest := filepath.Join(dir, config.DefaultConfigName)
	body := `
services:
  - name: db
    dir: db
    kind: shell
    port: 3306
    health: tcp://localhost:3306
`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatalf("清单没加载起来：%v", err)
	}

	if dep, ok := DepProbe(cfg, "db"); !ok || dep.Probe != "tcp://localhost:3306" {
		t.Errorf("没跑过时按清单探，得到 %q（%v）", dep.Probe, ok)
	}

	// 这次运行实际听在 3307 上。
	if err := UpdateState(cfg.StatePath(), func(st *State) error {
		st.Services["db"] = &Entry{PID: os.Getpid(), Port: 3307, StartedAt: time.Now()}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dep, ok := DepProbe(cfg, "db")
	if !ok {
		t.Fatal("db 在清单里，怎么认不出来")
	}
	if !strings.Contains(dep.Probe, "3307") {
		t.Errorf("探的该是这次在跑的那个端口，拿到 %q", dep.Probe)
	}
	// 目录跟着探针一起回来：cmd 探针要在那儿跑（见 probeCmd）。
	if want := filepath.Join(dir, "db"); dep.Dir != want {
		t.Errorf("探针的目录 = %q，想要 %q", dep.Dir, want)
	}

	// 清单里没有的名字：调用方据此立刻记一句「没等到」，而不是去探一个空的地址。
	if _, ok := DepProbe(cfg, "不存在"); ok {
		t.Error("清单里没有的名字不该说它有")
	}
}

// TestProbeCmdRunsInTheServiceDir 钉着 cmd 探针在服务自己的目录里跑。
//
// 相对路径解的是哪儿，取决于探针是从谁那儿发出去的——命令行下是用户敲命令的那一层，
// 界面从访达启动时是 `/`。同一份清单在两条路上探出不同的答案，是最说不清的一类现象；
// 而这个目录是清单里写死的，本就该由它说了算。
func TestProbeCmdRunsInTheServiceDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("下面的命令写的是 sh 那一套")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if !ProbeHealth(dir, "cmd: test -f ready") {
		t.Error("服务目录里有这个文件，探针应当通过")
	}
	// 同一个命令在别的目录里跑就不该通过：不然上面那句什么也没说明，
	// 可能命令压根没在 dir 里跑、而进程的当前目录里正好也有一个 ready。
	if ProbeHealth(t.TempDir(), "cmd: test -f ready") {
		t.Error("换一个目录还探通了，说明命令没在 dir 里跑")
	}
}
