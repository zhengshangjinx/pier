package view

import (
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// 状态归纳。这一条是三个入口（命令行、终端面板、图形界面）共用的那份判断，
// 措辞错一处三处一起错，所以每种情形都钉一条。

func running(o func(*proc.Status)) proc.Status {
	st := proc.Status{Service: &config.Service{Name: "alpha"}, Running: true, PID: 1234}
	if o != nil {
		o(&st)
	}
	return st
}

func TestStateKey(t *testing.T) {
	cases := []struct {
		name string
		st   proc.Status
		want string
	}{
		{"没起来", proc.Status{Service: &config.Service{Name: "alpha"}}, StateStopped},
		{"记录还在但进程没了", proc.Status{Service: &config.Service{Name: "alpha"}, Stale: true}, StateStale},
		{"端口被别人占着", proc.Status{Service: &config.Service{Name: "alpha"}, PortOpen: true}, StateExternal},
		{"在跑，没配探针", running(nil), StateRunning},
		{"在跑，探针过了", running(func(s *proc.Status) { s.HasHealth, s.Healthy = true, true }), StateRunning},
		{"在跑，探针还没过，还在等",
			running(func(s *proc.Status) { s.HasHealth = true }), StateStarting},
		// 这一条是「不是所有服务都有健康检查地址」那个问题的核心：
		// 等满一个窗口还没探通，就不再算「启动中」了。服务在好好跑着，
		// 一直挂在启动中只会让人以为再等等就好。
		{"在跑，探针等满窗口还没过",
			running(func(s *proc.Status) { s.HasHealth, s.ProbeExpired = true, true }), StateRunning},
	}
	for _, c := range cases {
		if got := StateKey(c.st); got != c.want {
			t.Errorf("%s：StateKey = %q，想要 %q", c.name, got, c.want)
		}
	}
}

func TestStateTextsAreStable(t *testing.T) {
	// 图形界面按状态键取色、取文案，键一改界面就静默错位。
	want := map[string]string{
		StateRunning:  "运行中",
		StateStarting: "启动中",
		StateExternal: "外部运行",
		StateStale:    "已退出",
		StateStopped:  "未启动",
	}
	for k, v := range want {
		if stateLabels[k] != v {
			t.Errorf("%s 的文案 = %q，想要 %q", k, stateLabels[k], v)
		}
	}
}

func TestNoteText(t *testing.T) {
	health := running(func(s *proc.Status) { s.Service.Health = "http://localhost:8080/health" })

	cases := []struct {
		name string
		st   proc.Status
		want string
	}{
		{"没起来就不说", proc.Status{Service: &config.Service{Name: "alpha"}}, ""},
		{"记录待清理", proc.Status{Service: &config.Service{Name: "alpha"}, Stale: true}, "进程已不在，记录待清理"},
		{"端口被别人占着",
			proc.Status{Service: &config.Service{Name: "alpha"}, PortOpen: true}, "端口被 Pier 之外的进程占用"},
		{"探针还在等",
			running(func(s *proc.Status) { s.HasHealth = true }), "尚未通过健康探针"},
		{"探针等满窗口没过",
			running(func(s *proc.Status) { s.HasHealth, s.ProbeExpired = true, true }),
			"健康探针未通过：地址可能不对，或这个服务没有健康接口"},
		{"一切正常时报健康地址", health, "http://localhost:8080/health"},
	}
	for _, c := range cases {
		if got := NoteText(c.st); got != c.want {
			t.Errorf("%s：NoteText = %q，想要 %q", c.name, got, c.want)
		}
	}
}

func TestNoteTextForExpiredProbeBlamesTheProbe(t *testing.T) {
	// 这句话要一路说到「怎么办」：探针过不去时服务是好好的，最容易被读成
	// 「服务起失败了」，于是有人去翻日志——而日志里什么错都没有。
	got := NoteText(running(func(s *proc.Status) { s.HasHealth, s.ProbeExpired = true, true }))
	if got == "" {
		t.Fatal("探针没过必须给一句话")
	}
	for _, word := range []string{"健康探针", "服务"} {
		if !strings.Contains(got, word) {
			t.Errorf("这句话里应当有 %q，实际 %q", word, got)
		}
	}
}

func TestPortPIDUptimeText(t *testing.T) {
	up := running(func(s *proc.Status) {
		s.Service.Port = 8080
		s.PortOpen = true
		s.Uptime = 12*time.Minute + 30*time.Second
	})
	if got, want := PortText(up), "8080 ✓"; got != want {
		t.Errorf("PortText = %q，想要 %q", got, want)
	}
	if got, want := PIDText(up), "1234"; got != want {
		t.Errorf("PIDText = %q，想要 %q", got, want)
	}
	if got, want := UptimeText(up), "12m30s"; got != want {
		t.Errorf("UptimeText = %q，想要 %q", got, want)
	}

	// 没起来时 PID 与运行时长是那个跨端约定的占位符，不是空串——
	// 空串在界面上会渲染成「运行 」，比「-」更难看出是「没有」。
	down := proc.Status{Service: &config.Service{Name: "alpha", Port: 8080}}
	for _, got := range []string{PIDText(down), UptimeText(down)} {
		if got != Dash {
			t.Errorf("未运行时应当给 %q，实际 %q", Dash, got)
		}
	}
	// 端口不一样：它是配置里写着的，没跑也照样报出来（还没有那个勾）。
	if got, want := PortText(down), "8080"; got != want {
		t.Errorf("PortText = %q，想要 %q", got, want)
	}
	// 没配端口的服务才给占位符。
	noPort := proc.Status{Service: &config.Service{Name: "alpha"}}
	if got := PortText(noPort); got != Dash {
		t.Errorf("没配端口时应当给 %q，实际 %q", Dash, got)
	}

	// 端口开着但不是 Pier 起的：要说清是被别人占着，不能显示成一个勾。
	foreign := proc.Status{Service: &config.Service{Name: "alpha", Port: 8080}, PortOpen: true}
	if got, want := PortText(foreign), "8080 (被占)"; got != want {
		t.Errorf("PortText = %q，想要 %q", got, want)
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{655360, "640 KB"},
		{16777216, "16 MB"},
		{24771197, "23.6 MB"},
		{1099511627776, "1 TB"},
	}
	for _, c := range cases {
		if got := Bytes(c.in); got != c.want {
			t.Errorf("Bytes(%d) = %q，想要 %q", c.in, got, c.want)
		}
	}
}
