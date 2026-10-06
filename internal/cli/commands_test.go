package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// --port 是「换一个端口起」在命令行上的入口。摘参数这一步做错的表现是
// 「找不到服务 --port」——与真正的问题隔着一层，所以每个写法都要钉一条。
func TestParsePortFlag(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		port  int
		given bool
		rest  []string
		bad   bool
	}{
		{name: "没给", args: []string{"api", "web"}, rest: []string{"api", "web"}},
		{name: "分开写", args: []string{"--port", "8081", "api"}, port: 8081, given: true, rest: []string{"api"}},
		{name: "等号写", args: []string{"api", "--port=8081"}, port: 8081, given: true, rest: []string{"api"}},
		{name: "排在服务名后面", args: []string{"api", "--port", "8081"}, port: 8081, given: true, rest: []string{"api"}},
		// 0 是「自己挑一个空闲的」，与「没给」是两件事：没给就是照清单里的端口起。
		{name: "自己挑一个", args: []string{"api", "--port", "0"}, port: 0, given: true, rest: []string{"api"}},
		{name: "只有它自己", args: []string{"--port=9000"}, port: 9000, given: true, rest: []string{}},
		{name: "后面没跟数字", args: []string{"api", "--port"}, bad: true},
		{name: "不是数字", args: []string{"--port", "八千"}, bad: true},
		{name: "负数", args: []string{"--port", "-1"}, bad: true},
		{name: "超出了端口范围", args: []string{"--port", "65536"}, bad: true},
		{name: "空等号", args: []string{"--port="}, bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, rest, err := parsePortFlag(c.args)
			if c.bad {
				if err == nil {
					t.Fatalf("parsePortFlag(%v) = %+v，本该报错", c.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePortFlag(%v) 报错：%v", c.args, err)
			}
			if got.port != c.port || got.given != c.given {
				t.Errorf("parsePortFlag(%v) = %+v，想要 %d / given=%v", c.args, got, c.port, c.given)
			}
			if strings.Join(rest, " ") != strings.Join(c.rest, " ") {
				t.Errorf("剩下的参数 = %v，想要 %v", rest, c.rest)
			}
		})
	}
}

// swapPort 给的是「这一次改用 port 起」的那一份，清单一个字都不动。
func TestSwapPort(t *testing.T) {
	cfg := &config.Config{Services: []*config.Service{
		{Name: "api", Port: 8080, Health: "http://localhost:8080/health"},
		{Name: "web", Port: 8081},
	}}

	svc := cfg.Services[0]
	got, err := swapPort(svc, 9000, cfg)
	if err != nil {
		t.Fatalf("换端口失败：%v", err)
	}
	if got == svc {
		t.Fatal("给的必须是副本，直接把清单里那一份改掉就写回去了")
	}
	if got.Port != 9000 {
		t.Errorf("Port = %d，想要 9000", got.Port)
	}
	if want := "http://localhost:9000/health"; got.Health != want {
		t.Errorf("Health = %q，想要 %q", got.Health, want)
	}
	if svc.Port != 8080 {
		t.Errorf("清单里那一份被改了：Port = %d", svc.Port)
	}

	// 0 表示自己挑一个：挑出来的既不能是清单里那个，也不能是清单里别的服务占着的
	// （web 就写在紧邻的 8081 上，正是「往后就近找」要绕开的那一个）。
	picked, err := swapPort(svc, 0, cfg)
	if err != nil {
		t.Fatalf("自己挑端口失败：%v", err)
	}
	if picked.Port <= 0 {
		t.Fatalf("挑出来的端口 = %d", picked.Port)
	}
	for _, used := range cfg.UsedPorts() {
		if picked.Port == used {
			t.Errorf("挑中的 %d 是清单里已经写掉的", picked.Port)
		}
	}

	cases := []struct {
		name string
		svc  *config.Service
		port int
		want string
	}{
		{"没有可换的端口", &config.Service{Name: "api"}, 0, "没有配端口"},
		{"换的还是清单里那一个", svc, 8080, "不必换"},
		// 65535 往后没有端口可挑了：这时要如实说「没找到」，
		// 而不是给一个 0 让它一路走到「端口 0 已被占用」那种没人看得懂的地方。
		{"从 65535 起挑不出下一个", &config.Service{Name: "api", Port: 65535}, 0, "没找到空闲端口"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := swapPort(c.svc, c.port, cfg)
			if err == nil {
				t.Fatal("本该报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误 = %q，应当说到 %q", err, c.want)
			}
		})
	}
}

// pier restart 是先停后起，而一停记录就销了——「上一次用的是哪个端口」只有状态文件里
// 那一条记着，所以这件事必须赶在停之前问出来。这里钉的就是读的那一半：读的是状态文件、
// 读出来的是「这一次该用哪个端口」。
func TestResumePortsReadsTheRunRecord(t *testing.T) {
	hold := func() (int, func()) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("占端口失败：%v", err)
		}
		release := func() { _ = ln.Close() }
		t.Cleanup(release)
		return ln.Addr().(*net.TCPAddr).Port, release
	}
	manifest, release := hold()
	swapped, releaseSwapped := hold()
	releaseSwapped()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "pier.yaml")
	manifestYAML := fmt.Sprintf(`
services:
  - name: alpha
    dir: .
    kind: shell
    run: sleep 300
    port: %d
  - name: beta
    dir: .
    kind: shell
    run: sleep 300
`, manifest)
	if err := os.WriteFile(cfgPath, []byte(manifestYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	if err := proc.UpdateState(cfg.StatePath(), func(s *proc.State) error {
		// alpha 上一次是从清单里那个端口让路出来的；beta 照旧。
		s.Services["alpha"] = &proc.Entry{PID: os.Getpid(), Port: swapped}
		s.Services["beta"] = &proc.Entry{PID: os.Getpid()}
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	got := resumePorts(cfgPath, nil)
	if got["alpha"] != swapped {
		t.Errorf("alpha 该接着 %d 起，拿到 %d", swapped, got["alpha"])
	}
	if p, ok := got["beta"]; ok {
		t.Errorf("beta 没换过端口，不该出现在里面（拿到 %d）", p)
	}

	// 点名的只算点名的那几个：让它去起别的服务就不是它该做的事了。
	if got := resumePorts(cfgPath, []string{"beta"}); got != nil {
		t.Errorf("只点了 beta，拿到 %v", got)
	}
	if got := resumePorts(cfgPath, []string{"alpha"}); got["alpha"] != swapped {
		t.Errorf("点了 alpha，拿到 %v", got)
	}
	// 名字不认识、清单读不出来：一句都不说。接着要报这件事的是 stop 与 start，
	// 它们手上有完整的原因，这里再抢着报一句就是第二条没头没尾的错误。
	if got := resumePorts(cfgPath, []string{"gamma"}); got != nil {
		t.Errorf("不认识的服务的名字，拿到 %v", got)
	}
	if got := resumePorts(filepath.Join(dir, "没有这份.yaml"), nil); got != nil {
		t.Errorf("清单读不出来，拿到 %v", got)
	}

	// 占着清单里那个端口的进程走了：下一次启动就该回到清单里写的那个端口上。
	release()
	if got := resumePorts(cfgPath, nil); got != nil {
		t.Errorf("那个端口空出来之后拿到 %v，该回清单了", got)
	}
}
