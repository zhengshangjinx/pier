package view

import (
	"os"
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

// 换过端口起的那一次：显示的是实际在听的那个数，另有一句话解释它为什么和清单对不上。
//
// 这一组是「界面上写着 A、跑起来是 B」最容易发生的地方——端口那一列和清单里的值
// 不一样，而用户手上只有这一屏能核对。
func TestPortTextUsesTheRunningPort(t *testing.T) {
	// 清单里写 8080，这次起在 8081 上（清单里那个被别人的进程占着）。
	swapped := running(func(s *proc.Status) {
		s.Service.Port = 8080
		s.Service.Health = "http://localhost:8080/health"
		s.Port = 8081
		s.PortOpen = true
	})
	if got, want := PortText(swapped), "8081 ✓"; got != want {
		t.Errorf("PortText = %q，想要 %q——显示的是这次实际在听的那个端口", got, want)
	}
	if got, want := PortNote(swapped), "清单里写的是 8080，这次用的是 8081"; got != want {
		t.Errorf("PortNote = %q，想要 %q", got, want)
	}
	// 说明位要给这句话，而不是健康地址：端口那一列的数字与清单对不上，
	// 这一句是唯一解释它的地方。
	if got := NoteText(swapped); got != PortNote(swapped) {
		t.Errorf("NoteText = %q，想要端口那句 %q", got, PortNote(swapped))
	}
	// 探针地址里的端口也跟着换了：不换就会去探旧端口上那个陌生进程。
	if got, want := swapped.RunHealth(), "http://localhost:8081/health"; got != want {
		t.Errorf("RunHealth = %q，想要 %q", got, want)
	}

	// 没换过就一个字都不多说。
	same := running(func(s *proc.Status) { s.Service.Port = 8080; s.Port = 8080; s.PortOpen = true })
	if got := PortNote(same); got != "" {
		t.Errorf("端口没换过时不该有那句话，实际 %q", got)
	}
	// 记录里没写端口（命令行面板在真实状态回来之前先按配置铺的行）时，
	// 退回清单里那个值，也不该冒出那句解释。
	noEntry := running(func(s *proc.Status) { s.Service.Port = 8080; s.PortOpen = true })
	if got := PortNote(noEntry); got != "" {
		t.Errorf("记录里没有端口时不该有那句话，实际 %q", got)
	}
	if got, want := PortText(noEntry), "8080 ✓"; got != want {
		t.Errorf("PortText = %q，想要 %q", got, want)
	}

	// 停掉之后（记录还在、进程已不在）：端口那一列留着上次那个数，
	// 说明位说的是记录待清理——两句话不能抢同一个位置。
	stale := proc.Status{Service: &config.Service{Name: "alpha", Port: 8080}, Port: 8081, Stale: true}
	if got, want := PortText(stale), "8081"; got != want {
		t.Errorf("PortText = %q，想要 %q", got, want)
	}
	if got, want := NoteText(stale), "进程已不在，记录待清理"; got != want {
		t.Errorf("NoteText = %q，想要 %q", got, want)
	}
}

// RunPort 是「有记录就用记录里的，没有就用清单里的」那一条判断，界面与命令行
// 都靠它兜底——手工搭出来的 Status 里只有清单那个端口。
func TestRunPortFallsBackToManifest(t *testing.T) {
	cases := []struct {
		name string
		st   proc.Status
		want int
	}{
		{"记录里有端口", proc.Status{Service: &config.Service{Port: 8080}, Port: 8081}, 8081},
		{"记录里没端口", proc.Status{Service: &config.Service{Port: 8080}}, 8080},
		{"没有服务", proc.Status{Port: 8081}, 8081},
		{"什么都没有", proc.Status{}, 0},
	}
	for _, c := range cases {
		if got := c.st.RunPort(); got != c.want {
			t.Errorf("%s：RunPort = %d，想要 %d", c.name, got, c.want)
		}
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

// ── 端口那一屏的展示 ───────────────────────────────────────────────────────

// TestShortPath 钉着两件事：缩的必须是整整一层目录，以及外来路径原样返回。
//
// 缩多一格（~/workspace2 → ~2）是个看起来对、实际指到别处去的错，
// 而这一列是要被人当路径读的。
func TestShortPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("拿不到主目录")
	}

	sep := string(os.PathSeparator)
	cases := []struct{ in, want string }{
		{"", ""},
		{home, "~"},
		{home + sep + "workspace", "~" + sep + "workspace"},
		{home + sep + "workspace" + sep + "pier", "~" + sep + "workspace" + sep + "pier"},
		// 前缀相同但不是同一层：不能缩。
		{home + "2", home + "2"},
		{home + "2" + sep + "x", home + "2" + sep + "x"},
		{sep + "opt" + sep + "home", sep + "opt" + sep + "home"},
		{"相对路径", "相对路径"},
	}
	for _, c := range cases {
		if got := ShortPath(c.in); got != c.want {
			t.Errorf("ShortPath(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}

	// 另一套分隔符也认：同一个 ~ 在 Windows 上一样要用。
	if got := ShortPath(home + `\workspace`); got != `~\workspace` {
		t.Errorf("反斜杠那一侧 ShortPath = %q，想要 %q", got, `~\workspace`)
	}
}

func TestOriginText(t *testing.T) {
	cases := []struct {
		name string
		in   *proc.Origin
		want string
	}{
		{"认不出来", nil, Dash},
		{"空结果", &proc.Origin{}, Dash},
		{"只有类别没有链", &proc.Origin{Kind: "editor", Label: "VS Code"}, Dash},
		{"一层", &proc.Origin{Kind: "terminal", Label: "终端", Chain: []string{"终端"}}, "终端"},
		{
			"编辑器里的集成终端",
			&proc.Origin{Kind: "terminal", Label: "终端", Chain: []string{"终端", "VS Code"}},
			"终端 ← VS Code",
		},
	}
	for _, c := range cases {
		if got := OriginText(c.in); got != c.want {
			t.Errorf("%s：OriginText = %q，想要 %q", c.name, got, c.want)
		}
	}
}
