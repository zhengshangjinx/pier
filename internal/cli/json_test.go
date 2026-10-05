package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/panel"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// capture 把 os.Stdout 换掉，跑完 fn 再把内容读回来。
//
// 命令行的输出直接写在 os.Stdout 上（不是可注入的 writer），所以只能这样接。
// 读的那一头放在协程里：输出超过管道缓冲时，先写完再读会当场死锁。
func capture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(r)
		done <- string(raw)
	}()
	fn()
	w.Close()
	return <-done
}

// tempHome 把数据目录挪到临时目录里。测试绝不碰真实的 ~/.pier。
func tempHome(t *testing.T) {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())
}

// writeManifest 写一份临时清单。dir 用清单自己所在的目录：这两个服务不会被真的
// 起来，只是让清单通过校验。
func writeManifest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	yaml := "services:\n" +
		"  - name: api\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"    health: http://127.0.0.1:1/health\n" +
		"  - name: plain\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n"
	path := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 分发这一层的退出码：认错的命令、多写的词、打在不认它的动词上的 --json。
// 每一条都是「脚本只看退出码」时会踩到的。
func TestRunExitCodes(t *testing.T) {
	tempHome(t)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"没有参数", nil, 2},
		{"未知命令", []string{"nope"}, 2},
		{"--help", []string{"--help"}, 0},
		{"-h", []string{"-h"}, 0},
		{"help", []string{"help"}, 0},
		{"help 某个动词", []string{"help", "logs"}, 0},
		{"help 一个不存在的动词", []string{"help", "nope"}, 2},
		{"--version", []string{"--version"}, 0},
		{"version", []string{"version"}, 0},
		{"version 带多余参数", []string{"version", "extra"}, 1},
		{"status 带多余参数", []string{"status", "extra"}, 1},
		{"status 带多余参数与 --json", []string{"status", "extra", "--json"}, 1},

		// --json 只有那几个动词认。别的动词拿到它必须报错，
		// 否则 up 会把它当服务名、version 会当没看见。
		{"up --json", []string{"up", "--json"}, 1},
		{"version --json", []string{"version", "--json"}, 1},
		{"detect --json", []string{"detect", "--json"}, 1},

		// logs 的开关组合：宁可当场报错，也不给「给了没反应」的余地。
		{"logs 没给服务名", []string{"logs"}, 1},
		{"logs --json 没配 --size", []string{"logs", "--json"}, 1},
		{"logs --all 没配 --clean", []string{"logs", "--all"}, 1},
		{"logs --tail 没有值", []string{"logs", "api", "--tail"}, 1},
		{"logs --tail 0", []string{"logs", "api", "--tail", "0"}, 1},
		{"logs --tail 不是数", []string{"logs", "api", "--tail=x"}, 1},
		{"logs --tail 撞 --size", []string{"logs", "--size", "--tail", "5"}, 1},
		{"logs --size 点名", []string{"logs", "--size", "api"}, 1},
		{"logs --follow 撞 --clean", []string{"logs", "api", "-f", "--clean"}, 1},
		{"logs --follow 跟两个", []string{"logs", "api", "plain", "-f"}, 1},
		{"logs 认不出的开关", []string{"logs", "api", "--tial", "5"}, 1},

		{"wait 没给服务名", []string{"wait"}, 1},
		{"wait --timeout 没有值", []string{"wait", "api", "--timeout"}, 1},
		{"wait --timeout 0", []string{"wait", "api", "--timeout", "0"}, 1},
		{"wait --timeout 认不出的时长", []string{"wait", "api", "--timeout", "一会儿"}, 1},
		{"wait 认不出的开关", []string{"wait", "api", "--json"}, 1},

		{"-h 在动词后面", []string{"logs", "-h"}, 0},
		{"-h 在参数中间", []string{"wait", "api", "--timeout", "1s", "-h"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := 0
			capture(t, func() { got = Run(c.args) })
			if got != c.want {
				t.Errorf("Run(%q) = %d，想要 %d", c.args, got, c.want)
			}
		})
	}
}

// --json 交出来的必须是后端那一份：字段名一个不改地能解回 StateOut。
// 另立一套形状的话，同一个事实就有两种说法，而用脚本的人看不到界面，
// 他只会以为自己手上这份就是全部。
func TestStatusJSONIsStateOut(t *testing.T) {
	tempHome(t)
	manifest := writeManifest(t)

	var code int
	raw := capture(t, func() { code = Run([]string{"status", "--json", "--config", manifest}) })
	if code != 0 {
		t.Fatalf("status --json 的退出码 = %d，想要 0；输出：%s", code, raw)
	}

	var st panel.StateOut
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("输出解不回 StateOut：%v\n%s", err, raw)
	}
	if !st.OK {
		t.Errorf("清单能打开，ok 却是 false：%s", st.Error)
	}
	if st.ConfigPath != manifest {
		t.Errorf("configPath = %q，想要 %q", st.ConfigPath, manifest)
	}
	if !st.ReadOnly || st.ConfigSrc != "命令行指定" {
		t.Errorf("--config 指定的清单应当是只读的：readOnly=%v source=%q", st.ReadOnly, st.ConfigSrc)
	}
	names := make([]string, 0, len(st.Services))
	for _, s := range st.Services {
		names = append(names, s.Name)
	}
	if strings.Join(names, "、") != "api、plain" {
		t.Errorf("服务列表 = %v，想要 [api plain]", names)
	}
	// 嵌套那一层也要能解出来：只对顶层键名，等于只验了一半。
	if len(st.Services) > 0 && st.Services[0].Health == "" {
		t.Errorf("服务上的 health 没跟着出来：%+v", st.Services[0])
	}
}

// 清单打不开时照样是一份能读的 JSON（ok=false + error），退出码非 0。
// 让脚本自己去读 error，比让它去解析一屏中文报错准得多。
func TestStatusJSONBrokenManifest(t *testing.T) {
	tempHome(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	var code int
	raw := capture(t, func() { code = Run([]string{"status", "--json", "--config", missing}) })
	if code == 0 {
		t.Errorf("清单读不到，退出码却是 0；输出：%s", raw)
	}
	var st panel.StateOut
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("输出解不回 StateOut：%v\n%s", err, raw)
	}
	if st.OK {
		t.Error("清单没读成，ok 却是 true")
	}
	if !strings.Contains(st.Error, "nope.yaml") {
		t.Errorf("error 里要说清是哪一份清单读不到，实际是：%q", st.Error)
	}
}

// logs --size 给的是「日志管理」页那一份统计，不是另算的一份。
func TestLogSizeJSONIsLogUsageOut(t *testing.T) {
	tempHome(t)
	manifest := writeManifest(t)

	var code int
	raw := capture(t, func() { code = Run([]string{"logs", "--size", "--json", "--config", manifest}) })
	if code != 0 {
		t.Fatalf("logs --size --json 的退出码 = %d，想要 0；输出：%s", code, raw)
	}
	var out panel.LogUsageOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("输出解不回 LogUsageOut：%v\n%s", err, raw)
	}
	if !out.OK {
		t.Errorf("ok 是 false：%s", out.Dir)
	}
	if out.KeepDays != proc.LogKeepDays {
		t.Errorf("keepDays = %d，想要 %d（保留天数只有一个出处）", out.KeepDays, proc.LogKeepDays)
	}
	if out.Dir == "" {
		t.Error("dir 空着：脚本要靠它找到日志目录")
	}
}
