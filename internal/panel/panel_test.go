//go:build !windows

// 这一份用 /bin/sh 起替身进程，用量按进程组核对，整套是照着 POSIX 写的。

package panel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// fixtureYAML 刻意不写 port：状态里一旦牵涉端口，结果就取决于本机此刻谁在监听，
// 同一份用例在这台机器上过、在另一台上挂。端口相关的判定属于 internal/proc 的用例。
//
// alpha 带 note 和 run，用来核对 State() 把编辑表单要预填的字段原样带出来了——
// 漏掉的话，界面上编辑一个已有服务会把它手写的启动命令悄悄清掉。
const fixtureYAML = `
services:
  - name: alpha
    dir: a
    kind: go
    group: 组一
    note: 手写的备注
    run: go run ./cmd/alpha
  - name: beta
    dir: b
    kind: go
    group: 组一
  - name: gamma
    dir: c
    kind: go
`

type harness struct {
	t    *testing.T
	dir  string
	base string
	p    *Panel
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(fixtureYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := New()
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return &harness{t: t, dir: dir, base: base, p: p}
}

// busy 手工把一个动作塞进内核，绕过队列。
//
// 直接 Start() 会真的拉起进程——单元测试不该去动工作空间里的东西，也不该依赖
// 某个命令能不能跑起来。而这些展示字段的组装逻辑（谁在忙、忙什么、上次为什么失败）
// 正是这里要验的，跟进程有没有真起来无关。
func (h *harness) busy(name, kind, phase, errMsg string) {
	h.t.Helper()
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	op := newOp(kind, phase)
	op.err = errMsg
	h.p.ops[name] = op
}

// waitStopSettled 等后台那次停止收尾——停成了、或者失败了，两种都算。
//
// Stop 是另起一个协程去停进程的，调用方不等它也能返回。测试要是在那之前结束，
// t.TempDir 的清理会撞上还在动目录的那个协程，报一句「directory not empty」，
// 和用例本身要验的东西毫无关系。
func (h *harness) waitStopSettled(name string) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.p.mu.Lock()
		op := h.p.ops[name]
		h.p.mu.Unlock()
		if op == nil || op.kind != "stop" || op.phase == "error" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Errorf("%s 上那次停止一直没收尾", name)
}

func (h *harness) find(t *testing.T, name string) ServiceOut {
	t.Helper()
	for _, s := range h.p.State().Services {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("状态里没有服务 %s", name)
	return ServiceOut{}
}

func TestStateReportsGroupsInManifestOrder(t *testing.T) {
	h := newHarness(t)
	st := h.p.State()

	if !st.OK {
		t.Fatalf("清单已加载，OK 应为真，实际 Error=%q", st.Error)
	}
	if st.ConfigPath != h.base {
		t.Errorf("ConfigPath = %q，想要 %q", st.ConfigPath, h.base)
	}
	if st.ConfigDir != h.dir {
		t.Errorf("ConfigDir = %q，想要 %q", st.ConfigDir, h.dir)
	}
	if st.UngroupedName != config.UngroupedName {
		t.Errorf("UngroupedName = %q，想要 %q", st.UngroupedName, config.UngroupedName)
	}

	// 分组顺序按清单里第一次出现的顺序，未分组的排在它之后。
	want := []GroupOut{
		{Name: "组一", Count: 2},
		{Name: config.UngroupedName, Count: 1, Builtin: true},
	}
	if len(st.Groups) != len(want) {
		t.Fatalf("分组数 = %d，想要 %d：%+v", len(st.Groups), len(want), st.Groups)
	}
	for i, g := range st.Groups {
		if g != want[i] {
			t.Errorf("第 %d 个分组 = %+v，想要 %+v", i+1, g, want[i])
		}
	}

	if len(st.Services) != 3 {
		t.Fatalf("服务数 = %d，想要 3", len(st.Services))
	}
	if got := h.find(t, "gamma").Group; got != config.UngroupedName {
		t.Errorf("gamma 的分组 = %q，想要 %q", got, config.UngroupedName)
	}
	// 直接加载 YAML 清单是命令行 --config 的用法，只读，界面据此收起编辑入口。
	if !st.ReadOnly {
		t.Error("YAML 清单应当标成只读")
	}
}

// 数据文件加载出来的清单可写。
func TestStateFromStoreIsEditable(t *testing.T) {
	h := newHarness(t)
	store := filepath.Join(t.TempDir(), config.StoreName)
	if _, err := config.EnsureStore(store, []string{h.base}); err != nil {
		t.Fatal(err)
	}
	if err := h.p.Load(store); err != nil {
		t.Fatal(err)
	}
	st := h.p.State()
	if st.ReadOnly {
		t.Error("数据文件不该标成只读")
	}
	if st.ConfigDir != filepath.Dir(store) {
		t.Errorf("ConfigDir 应当是数据文件所在的数据目录，实际 %q", st.ConfigDir)
	}
	if !h.find(t, "alpha").Editable {
		t.Error("数据文件里的服务应当可编辑")
	}
}

func TestStateCarriesFieldsTheEditFormNeeds(t *testing.T) {
	h := newHarness(t)
	a := h.find(t, "alpha")

	if a.UserNote != "手写的备注" {
		t.Errorf("UserNote = %q，备注没有原样带出来", a.UserNote)
	}
	if a.Run != "go run ./cmd/alpha" {
		t.Errorf("Run = %q，编辑表单预填不到就会把它清掉", a.Run)
	}
	if a.Dir != filepath.Join(h.dir, "a") {
		t.Errorf("Dir = %q，相对路径没有解析成绝对路径", a.Dir)
	}
	// 定义来自只读的主清单，界面只能「隐藏」，不能真删。
	if a.Editable {
		t.Error("主清单里的定义 Editable 应为假")
	}
	if a.StatusKey != view.StateStopped {
		t.Errorf("StatusKey = %q，想要 %q", a.StatusKey, view.StateStopped)
	}
	if a.LogPath == "" {
		t.Error("LogPath 为空，界面上的「看日志」会没有目标")
	}
}

// 在跑的服务的用量要按进程组取到，并且加进它所属的分组。
//
// 这条守的是整条接线：进程组号有没有从状态文件透到 Status、采样结果有没有被
// 取到服务和分组上。任何一环断了，界面上都是一片 0——而 0 看上去和
// 「这个服务确实不占资源」一模一样，不报错、不提示，正是最难发现的那种坏法。
func TestStateCarriesUsageOfRunningService(t *testing.T) {
	h := newHarness(t)

	// 起一个自带会话的进程树当替身：Setsid 之后进程组号等于首进程 PID，
	// 与 Supervisor.Start 起服务时的情形一致；而 sh 派生两个 sleep，
	// 对应「记录在案的 PID 是壳、真正干活的是它的子进程」。
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起替身进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()

	// 状态文件里把 alpha 记成正在跑。这是面板判断「在跑」的唯一依据。
	st := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now()},
	}}
	if err := st.Save(h.p.Config().StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	// 等子进程派生出来。不到 2 个就说明只按 PID 取了数——那正是这条要防的。
	deadline := time.Now().Add(5 * time.Second)
	var a ServiceOut
	for {
		a = h.find(t, "alpha")
		if !a.Running {
			t.Fatal("状态文件里记着在跑，面板却没报运行中")
		}
		if a.Usage.Procs >= 2 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("进程组里只采到 %d 个进程——子进程被漏掉了", a.Usage.Procs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if a.Usage.MemBytes <= 0 {
		t.Error("内存为 0，rss 那一列没被读进来")
	}

	// 分组把组内服务加起来。fixture 里「组一」有 alpha 和 beta 两个服务，
	// beta 没在跑，所以整组应当正好等于 alpha 这一棵树的用量。
	got := map[string]UsageOut{}
	for _, g := range h.p.State().Groups {
		got[g.Name] = g.Usage
	}
	if got["组一"].Procs != a.Usage.Procs || got["组一"].MemBytes != a.Usage.MemBytes {
		t.Errorf("分组「组一」的用量 = %+v，想要 alpha 那一份 %+v", got["组一"], a.Usage)
	}
	if u := got[config.UngroupedName]; u.Procs != 0 || u.MemBytes != 0 {
		t.Errorf("「%s」里只有没在跑的 gamma，用量应当全 0，实际 %+v", config.UngroupedName, u)
	}

	// 全部服务的合计就是各分组相加（每个服务恰好属于一个分组），
	// 面板自身也要有数，否则界面上那一格没法给出参照。
	full := h.p.State()
	var sum UsageOut
	for _, g := range full.Groups {
		sum.add(g.Usage)
	}
	if full.Usage.Procs != sum.Procs || full.Usage.MemBytes != sum.MemBytes {
		t.Errorf("全部服务的用量 = %+v，各分组加起来是 %+v", full.Usage, sum)
	}
	if full.Self.Procs < 1 {
		t.Errorf("面板自身 = %+v，至少得有它自己这一个进程", full.Self)
	}
	if full.MetricsErr != "" {
		t.Errorf("采样失败：%s", full.MetricsErr)
	}
}

func TestStateCountsOnlyRunningOperationsAsBusy(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "start", "queued", "")
	// 失败态不算「忙」：它已经结束了，只是结果留在界面上等用户看见。
	h.busy("beta", "start", "error", "端口 20351 已被占用")

	st := h.p.State()
	if st.BusyCount != 1 {
		t.Errorf("BusyCount = %d，想要 1（只有 alpha 在忙）", st.BusyCount)
	}

	a := h.find(t, "alpha")
	if a.Op != "排队启动" {
		t.Errorf("alpha 的 Op = %q，想要 %q", a.Op, "排队启动")
	}
	if a.OpErr != "" {
		t.Errorf("alpha 不该有失败原因，实际 %q", a.OpErr)
	}

	b := h.find(t, "beta")
	if b.Op != "" {
		t.Errorf("失败态不该再显示动作，实际 Op = %q", b.Op)
	}
	if b.OpErr != "端口 20351 已被占用" {
		t.Errorf("beta 的 OpErr = %q，失败原因没有传到界面", b.OpErr)
	}
}

func TestOperationLabels(t *testing.T) {
	cases := []struct {
		op   *operation
		want string
	}{
		{nil, ""},
		{&operation{kind: "start", phase: "queued"}, "排队启动"},
		{&operation{kind: "start", phase: "running"}, "启动中"},
		{&operation{kind: "start", phase: "waiting"}, "等待就绪"},
		{&operation{kind: "stop", phase: "queued"}, "排队停止"},
		{&operation{kind: "stop", phase: "running"}, "停止中"},
		{&operation{kind: "restart", phase: "waiting"}, "等待就绪"},
		// 失败态一律不显示动作：动作已经结束了，再显示「启动中」是骗人的。
		{&operation{kind: "start", phase: "error", err: "炸了"}, ""},
		// 阶段认不出来时退回动作本身，总比显示空白强。
		{&operation{kind: "start", phase: "某个新阶段"}, "start"},
	}
	for _, c := range cases {
		if got := c.op.label(); got != c.want {
			t.Errorf("label(%+v) = %q，想要 %q", c.op, got, c.want)
		}
	}
}

func TestStateWithoutConfigKeepsTheReason(t *testing.T) {
	p := New()
	p.SetLoadError("读取配置失败：no such file")
	p.SetSource("上次使用")

	st := p.State()
	if st.OK {
		t.Error("没有清单时 OK 应为假")
	}
	if st.Error != "读取配置失败：no such file" {
		t.Errorf("Error = %q，加载失败的原因没有带出来", st.Error)
	}
	// 来源仍然要带出来：界面靠它显示「上次使用的那份没加载成功」，
	// 否则用户只看到一句失败，不知道工具在找哪份文件。
	if st.ConfigSrc != "上次使用" {
		t.Errorf("ConfigSrc = %q，想要「上次使用」", st.ConfigSrc)
	}
	if st.Services != nil {
		t.Errorf("没有清单时不该有服务列表，实际 %d 条", len(st.Services))
	}
}

func TestRefusesEverythingWithoutConfig(t *testing.T) {
	p := New()

	actions := []struct {
		name string
		run  func() error
	}{
		{"启动", func() error { _, err := p.Start("alpha"); return err }},
		{"停止", func() error { _, err := p.Stop("alpha"); return err }},
		{"重启", func() error { _, err := p.Restart("alpha"); return err }},
		{"全部启动", func() error { _, err := p.StartAll(); return err }},
		{"全部停止", func() error { _, err := p.StopAll(); return err }},
		{"清理残留", func() error { _, err := p.Prune(); return err }},
		{"查看日志", func() error { _, err := p.Logs("alpha", "", 0); return err }},
		{"打开目录", func() error { _, err := p.ServiceDir("alpha"); return err }},
		{"定位日志", func() error { _, err := p.ServiceLogPath("alpha"); return err }},
		{"打开健康地址", func() error { _, err := p.HealthURL("alpha"); return err }},
	}
	for _, a := range actions {
		err := a.run()
		if !errors.Is(err, manage.ErrNoConfig) {
			t.Errorf("%s：错误 = %v，想要 ErrNoConfig", a.name, err)
		}
	}
}

func TestEnqueueRejectsUnknownServiceBeforeQueueing(t *testing.T) {
	h := newHarness(t)

	_, err := h.p.Start("根本没有这个服务")
	if err == nil {
		t.Fatal("服务名不存在时应当报错")
	}
	if !strings.Contains(err.Error(), "没有名为") {
		t.Errorf("错误 = %v，应当说明找不到这个服务", err)
	}
	// 校验必须在入队前完成：排到队头才发现名字是错的，用户已经白等了一轮。
	if st := h.p.State(); st.BusyCount != 0 {
		t.Errorf("校验失败不该留下排队记录，BusyCount = %d", st.BusyCount)
	}
}

func TestEnqueueRefusesServiceThatIsAlreadyBusy(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "start", "waiting", "")

	// 启动 / 重启不能叠在一次进行中的启动上（停止除外，见下面几条）。
	_, err := h.p.Restart("alpha")
	if err == nil {
		t.Fatal("正在等待就绪时再点重启，应当被拒绝")
	}
	// 文案要说清它在忙什么：只说「请稍后」的话，用户不知道要等多久、在等什么。
	if !strings.Contains(err.Error(), "正在等待就绪") {
		t.Errorf("错误 = %v，应当带上正在进行的动作", err)
	}
}

// 不管服务处在哪个阶段，「停止」都必须能点：它会打断进行中的启动，
// 而不是被「请等它结束」挡回去——一次 Maven 编译要几十秒到几分钟。
func TestStopInterruptsStartInAnyPhase(t *testing.T) {
	for _, phase := range []string{"queued", "running", "waiting"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			h.busy("alpha", "start", phase, "")
			h.p.mu.Lock()
			startOp := h.p.ops["alpha"]
			h.p.mu.Unlock()
			// 模拟启动流程的同步部分已经退出（排队中的不需要）。
			if phase != "queued" {
				close(startOp.started)
			}

			if _, err := h.p.Stop("alpha"); err != nil {
				t.Fatalf("%s 阶段点停止被拒：%v", phase, err)
			}
			if startOp.ctx.Err() == nil {
				t.Error("进行中的启动没有被取消")
			}

			// 停止在后台完成；alpha 本来就没跑，停完状态应当被清掉。
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				h.p.mu.Lock()
				_, busy := h.p.ops["alpha"]
				h.p.mu.Unlock()
				if !busy {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Errorf("%s 阶段停止之后状态一直没清掉", phase)
		})
	}
}

// 被打断的启动流程事后再回来写状态，不能把「停止」的结果冲掉。
func TestInterruptedStartCannotOverwriteState(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "start", "waiting", "")
	h.p.mu.Lock()
	startOp := h.p.ops["alpha"]
	h.p.mu.Unlock()
	close(startOp.started)
	if _, err := h.p.Stop("alpha"); err != nil {
		t.Fatal(err)
	}

	h.p.fail("alpha", startOp, errors.New("已启动，但未通过健康探针"))
	h.p.setPhase("alpha", startOp, "waiting")

	// 字段要在锁里读出来：后台那次停止随时会把结果写进同一个 operation
	// （fail 是持锁写的），在锁外读就是一次真数据竞争——-race 下能复现。
	h.p.mu.Lock()
	cur := h.p.ops["alpha"]
	kind, errText := "", ""
	if cur != nil {
		kind, errText = cur.kind, cur.err
	}
	h.p.mu.Unlock()
	if cur != nil && (kind != "stop" || errText != "") {
		t.Errorf("旧的启动流程覆盖了停止的状态：kind=%s err=%s", kind, errText)
	}
	h.waitStopSettled("alpha")
}

// 重复点停止不算错。
func TestStopWhileStoppingIsNoop(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "stop", "running", "")
	if _, err := h.p.Stop("alpha"); err != nil {
		t.Errorf("停止中再点停止不该报错：%v", err)
	}
}

func TestFailedServiceCanBeRetriedImmediately(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "start", "error", "上一次失败的原因")

	// 走 begin 而不是 Restart：这里要验的是「放不放行」，真排进队列会去起进程，
	// 那既动了工作空间，结果也取决于本机的工具链。
	if _, _, err := h.p.begin("restart", "alpha"); err != nil {
		t.Fatalf("失败后重试被拒：%v", err)
	}
	h.p.mu.Lock()
	op := h.p.ops["alpha"]
	h.p.mu.Unlock()
	if op == nil || op.phase != "queued" || op.kind != "restart" {
		t.Fatalf("重试没有占住服务：%+v", op)
	}
	// 旧的失败原因必须被清掉，否则界面会一边显示「排队重启」一边显示上次的报错。
	if op.err != "" {
		t.Errorf("重试后仍带着旧的失败原因：%q", op.err)
	}
}

func TestBeginDoesNotTouchTheQueue(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.p.begin("start", "alpha"); err != nil {
		t.Fatalf("begin 失败：%v", err)
	}
	select {
	case j := <-h.p.jobs:
		t.Fatalf("begin 往队列里放了任务：%+v", j)
	default:
	}
}

func TestSetConfigFailureKeepsCurrentManifest(t *testing.T) {
	h := newHarness(t)
	h.p.SetSource("命令行指定")

	if _, err := h.p.SetConfig(filepath.Join(h.dir, "并没有这份.yaml")); err == nil {
		t.Fatal("路径不存在时应当报错")
	}

	// 切到一个坏路径上必须原地不动：把界面切成空白，用户就再也回不来了。
	if got := h.p.ConfigPath(); got != h.base {
		t.Errorf("ConfigPath = %q，坏路径把当前清单顶掉了", got)
	}
	if got := h.p.Source(); got != "命令行指定" {
		t.Errorf("Source = %q，加载失败不该改来源", got)
	}
	if st := h.p.State(); !st.OK {
		t.Errorf("加载失败后原有清单应当照常可用，实际 Error=%q", st.Error)
	}
}

func TestSetConfigSuccessSwitchesManifest(t *testing.T) {
	h := newHarness(t)
	other := filepath.Join(t.TempDir(), config.DefaultConfigName)
	if err := os.WriteFile(other, []byte("services:\n  - name: 另一个\n    dir: x\n    kind: go\n"), 0o644); err != nil {
		t.Fatalf("写第二份清单失败：%v", err)
	}

	msg, err := h.p.SetConfig(other)
	if err != nil {
		t.Fatalf("切换清单失败：%v", err)
	}
	if !strings.Contains(msg, other) {
		t.Errorf("提示 = %q，应当说明加载了哪份", msg)
	}
	if got := h.p.Source(); got != "手动指定" {
		t.Errorf("Source = %q，想要「手动指定」", got)
	}
	st := h.p.State()
	if len(st.Services) != 1 || st.Services[0].Name != "另一个" {
		t.Fatalf("服务列表没有跟着换：%+v", st.Services)
	}
	// 换了清单，旧清单上的排队与错误都不再有意义。
	if st.BusyCount != 0 {
		t.Errorf("换清单后 BusyCount = %d，旧清单的动作应当被清掉", st.BusyCount)
	}
}

// 换清单是在编辑任意一个服务之后都会发生的事（见 Manager.commit），所以处理在途动作
// 必须分清对象：拿旧定义排队等着的不该再执行，而别处正在跑的编译不该被顺手杀掉。
func TestReloadSortsOutInFlightOperations(t *testing.T) {
	h := newHarness(t)

	h.busy("alpha", "start", "running", "") // 正在编译
	h.busy("beta", "start", "queued", "")   // 还在排队
	h.busy("gamma", "stop", "queued", "")   // 排队的停止

	h.p.mu.Lock()
	compiling, queued, stopping := h.p.ops["alpha"], h.p.ops["beta"], h.p.ops["gamma"]
	h.p.mu.Unlock()

	if err := h.p.Load(h.base); err != nil {
		t.Fatalf("重载清单失败：%v", err)
	}

	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	if h.p.ops["alpha"] != compiling || compiling.ctx.Err() != nil {
		t.Error("还在编译的服务被换清单打断了——编辑另一个服务不该杀掉它的编译")
	}
	if h.p.ops["beta"] != nil {
		t.Error("排队等着启动的动作没有被撤掉")
	}
	if queued.ctx.Err() == nil {
		t.Error("撤掉的动作没有被取消，轮到它时还会去起进程")
	}
	if h.p.ops["gamma"] != stopping || stopping.ctx.Err() != nil {
		t.Error("排队的停止被撤掉了：服务刚被删掉时正是最需要它的时候")
	}
}

// 正在启动的服务被删掉（或改名）时，那次启动必须打断。
//
// 编译阶段的进程是跟旧定义走的，等它编译完把进程拉起来，就成了一个界面按新清单
// 渲染时看不见、pier down 也找不着的孤儿，只能去活动监视器手工杀。
func TestReloadCancelsStartOfServiceThatIsGone(t *testing.T) {
	h := newHarness(t)
	h.busy("alpha", "start", "running", "")
	h.p.mu.Lock()
	op := h.p.ops["alpha"]
	h.p.mu.Unlock()

	other := filepath.Join(t.TempDir(), config.DefaultConfigName)
	if err := os.WriteFile(other, []byte("services:\n  - name: 另一个\n    dir: x\n    kind: go\n"), 0o644); err != nil {
		t.Fatalf("写第二份清单失败：%v", err)
	}
	if _, err := h.p.SetConfig(other); err != nil {
		t.Fatalf("切换清单失败：%v", err)
	}

	if op.ctx.Err() == nil {
		t.Error("服务已经不在新清单里了，那次启动却没有被打断")
	}
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	if h.p.ops["alpha"] != nil {
		t.Error("换了清单之后还留着旧清单上服务的动作")
	}
}

// 换了清单之后，队列里那些拿旧定义的任务不能再被执行：它们说的服务（旧目录、
// 旧命令）在新清单里可能已经不存在，用新监管器把它拉起来只会造出一个孤儿进程。
func TestQueuedJobFromOldManifestIsNotRun(t *testing.T) {
	h := newHarness(t)
	svc, op, err := h.p.begin("start", "alpha")
	if err != nil {
		t.Fatalf("占住服务失败：%v", err)
	}

	other := filepath.Join(t.TempDir(), config.DefaultConfigName)
	if err := os.WriteFile(other, []byte("services:\n  - name: 另一个\n    dir: x\n    kind: go\n"), 0o644); err != nil {
		t.Fatalf("写第二份清单失败：%v", err)
	}
	if _, err := h.p.SetConfig(other); err != nil {
		t.Fatalf("切换清单失败：%v", err)
	}

	h.p.exec(job{kind: "start", svc: svc, op: op})

	// 真跑起来的话，StartContext 一进门就会把日志目录建出来——
	// 而这里要的正是「一步都没迈出去」。
	if _, err := os.Stat(h.p.Config().LogDirFor("alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("旧清单上的任务被执行了，日志目录已经建出来（%v）", err)
	}
}

func TestOnLoadHookFiresOnEverySuccessfulLoad(t *testing.T) {
	p := New()
	var seen []string
	p.SetOnLoad(func(path string) { seen = append(seen, path) })

	dir := t.TempDir()
	legacy := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(legacy, []byte(fixtureYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	// 编辑只能落在数据文件上，所以这里加载的是导入后的数据文件。
	base := filepath.Join(t.TempDir(), config.StoreName)
	if _, err := config.EnsureStore(base, []string{legacy}); err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	if err := p.Load(base); err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	// 加载失败不能触发钩子：记住一个坏路径，下次启动会卡在加载不出来的状态。
	if err := p.Load(filepath.Join(dir, "不存在.yaml")); err == nil {
		t.Fatal("路径不存在时应当报错")
	}

	// 业务层写完之后的那次重载也要走钩子。漏掉它，用户改完清单重启界面
	// 就会回到上一份旧清单上——这条路径不经过宿主的显式调用，只能靠钩子覆盖。
	if _, err := p.Manager().CreateGroup("新分组"); err != nil {
		t.Fatalf("建分组失败：%v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("钩子触发了 %d 次，想要 2 次：%v", len(seen), seen)
	}
	for i, path := range seen {
		if path != base {
			t.Errorf("第 %d 次钩子收到 %q，想要 %q", i+1, path, base)
		}
	}
	// 编辑落盘后清单要真的重新加载过，界面上的分组才会立刻多出来。
	found := false
	for _, g := range p.State().Groups {
		if g.Name == "新分组" {
			found = true
		}
	}
	if !found {
		t.Error("新建的分组没有出现在状态里，说明写完之后没有重新加载")
	}
}

func TestLogsOfNeverStartedServiceIsNotAnError(t *testing.T) {
	h := newHarness(t)

	out, err := h.p.Logs("alpha", "", 0)
	if err != nil {
		t.Fatalf("还没启动过不该报错：%v", err)
	}
	if !out.OK {
		t.Error("OK 应为真")
	}
	if out.Text != "" {
		t.Errorf("没有日志文件时 Text 应为空，实际 %q", out.Text)
	}
	// 路径仍要给出来：界面靠它显示「日志将写到哪」，也是「在访达中显示」的目标。
	// 没启动过时给的是今天那份（logs/服务名/年-月-日.log）——文件还不存在，
	// 但「它会在哪」是个有意义的答案。
	want := filepath.Join(h.dir, ".pier", "logs", "alpha", time.Now().Format(config.LogDateLayout)+".log")
	if out.Path != want {
		t.Errorf("Path = %q，想要 %q", out.Path, want)
	}
}

func TestLogCleanupSkipsRunningAndStartingServices(t *testing.T) {
	h := newHarness(t)

	// 起一个替身进程当「正在跑的服务」：Setsid 之后进程组号等于首进程 PID，
	// 和 Supervisor.Start 起服务时一样（判定见 proc.RunningNames）。
	cmd := exec.Command("/bin/sh", "-c", "sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("起替身进程失败：%v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()

	cfg := h.p.Config()
	st := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now()},
	}}
	if err := st.Save(cfg.StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	// 一个服务一天一份日志；摆两天，好让「清理超期」有东西可清。
	write := func(svc, date, text string) string {
		t.Helper()
		dir := cfg.LogDirFor(svc)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, date+".log")
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := time.Now().AddDate(0, 0, -30).Format(config.LogDateLayout)
	mine := write("alpha", old, "alpha 在跑，这份是它写的")
	other := write("beta", old, "beta 没在跑")

	msg, err := h.p.PruneLogs("")
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("正在运行的服务的日志不该被删：%v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Error("没在跑的服务的超期日志应当照清")
	}
	// 回执里要点名：不然「已删除 1 个文件」会让人以为跑着的那个也清了。
	if !strings.Contains(msg, "alpha") {
		t.Errorf("回执 %q 里没说清楚跳过了谁", msg)
	}

	// 点名单个服务时直接说清楚，别让人以为清完了。
	msg, err = h.p.ClearLogs("alpha")
	if err != nil {
		t.Fatalf("清空失败：%v", err)
	}
	if !strings.Contains(msg, "运行") {
		t.Errorf("回执 %q 应当说明它正在跑", msg)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("点名清空也不该动正在运行的服务：%v", err)
	}

	// 还没起来、正在编译的服务同样不能动：它没进状态文件，但已经在往那份日志里写了。
	h.busy("gamma", "start", "compile", "")
	msg, err = h.p.ClearLogs("gamma")
	if err != nil {
		t.Fatalf("清空失败：%v", err)
	}
	if !strings.Contains(msg, "启动") {
		t.Errorf("回执 %q 应当说明它正在启动", msg)
	}
}

func TestLogsReadsHistoryAndFollowsIncrementally(t *testing.T) {
	// 抽屉的三个动作都压在这一条契约上：整段读（Reset）、接着往后读（since/Offset）、
	// 按日期读历史。三者错一个的表现都是「日志显示得不对」，而那是最后才会被怀疑的地方。
	h := newHarness(t)
	cfg := h.p.Config()
	dir := cfg.LogDirFor("alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	today := time.Now().Format(config.LogDateLayout)
	yesterday := time.Now().AddDate(0, 0, -1).Format(config.LogDateLayout)
	write := func(date, text string) {
		t.Helper()
		if err := os.WriteFile(cfg.LogPathDate("alpha", date), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(yesterday, "昨天的输出\n")
	write(today, "第一行\n第二行\n")

	// 没给日期 = 它此刻在写的那一份，也就是最新那份。
	out, err := h.p.Logs("alpha", "", 0)
	if err != nil {
		t.Fatalf("读日志失败：%v", err)
	}
	if !out.Reset || out.Date != today || out.Text != "第一行\n第二行\n" {
		t.Fatalf("整段读的结果不对：%+v", out)
	}
	// 日期列表从新到旧，抽屉里的选择框直接拿它当选项。
	if len(out.Dates) != 2 || out.Dates[0] != today || out.Dates[1] != yesterday {
		t.Errorf("Dates = %v，想要 [%s %s]", out.Dates, today, yesterday)
	}

	// 带 since 来 = 只要新增的那一段。Offset 用读之前的长度，
	// 读的过程中又写进来的字节留给下一轮——重了会在界面上看见两遍。
	write(today, "第一行\n第二行\n第三行\n")
	more, err := h.p.Logs("alpha", today, out.Offset)
	if err != nil {
		t.Fatalf("增量读失败：%v", err)
	}
	if more.Reset || more.Text != "第三行\n" {
		t.Errorf("增量读 = %+v，想要只回「第三行」那一段", more)
	}
	if more.Offset <= out.Offset {
		t.Errorf("Offset 没有跟着前进：%d → %d", out.Offset, more.Offset)
	}

	// 同一天里又启动过一次（往同一份文件后面追加，这正是分天之后的常态）：
	// 手上那些文字整个作废，改整段重读——否则界面会把上一次运行的输出
	// 当成这次的失败原因。
	f, err := os.OpenFile(cfg.LogPathDate("alpha", today), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(proc.LogStartMarker("alpha") + " 09:31\n刚起来的这一次\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	again, err := h.p.Logs("alpha", today, more.Offset)
	if err != nil {
		t.Fatalf("增量读失败：%v", err)
	}
	if !again.Reset {
		t.Error("这一小段里出现了启动标记，应当整段重读")
	}
	if strings.Contains(again.Text, "旧的输出") {
		t.Errorf("重读之后仍带着上一次运行的输出：%q", again.Text)
	}

	// 指定日期 = 读历史那一天。
	hist, err := h.p.Logs("alpha", yesterday, 0)
	if err != nil {
		t.Fatalf("读历史日志失败：%v", err)
	}
	if hist.Text != "昨天的输出\n" || hist.Date != yesterday {
		t.Errorf("历史日志读错了：%+v", hist)
	}

	// 文件没了（清空、手工删）时也要 Reset：界面得把手上那段换掉。
	if err := os.Remove(cfg.LogPathDate("alpha", yesterday)); err != nil {
		t.Fatal(err)
	}
	gone, err := h.p.Logs("alpha", yesterday, 0)
	if err != nil {
		t.Fatalf("读不存在的日志不该报错：%v", err)
	}
	if !gone.Reset || gone.Text != "" {
		t.Errorf("文件已经没了，应当让界面把正文清掉：%+v", gone)
	}
}

func TestLogsRejectsUnknownService(t *testing.T) {
	h := newHarness(t)
	if _, err := h.p.Logs("根本没有这个服务", "", 0); err == nil {
		t.Fatal("服务名不存在时应当报错")
	}
}

func TestTailFileKeepsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()

	t.Run("按行数截断", func(t *testing.T) {
		path := filepath.Join(dir, "many.log")
		var b strings.Builder
		for i := 1; i <= 500; i++ {
			fmt.Fprintf(&b, "第 %d 行\n", i)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		text, truncated, err := TailFile(path, 10, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Error("行数被截断时应当报告截断，界面要据此提示「只显示末尾」")
		}
		// 正文是字节对齐的：文件以换行结尾，返回的也以换行结尾，
		// 这样界面把下一段接上来才不会和上一行粘住（见 tailFrom）。
		if !strings.HasSuffix(text, "\n") {
			t.Errorf("正文应当保留文件的行尾换行，实际 %q", text)
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) != 10 {
			t.Fatalf("行数 = %d，想要 10", len(lines))
		}
		if !strings.Contains(lines[9], "500") {
			t.Errorf("末行 = %q，截断后留下的应当是末尾", lines[9])
		}
	})

	t.Run("按字节截断时丢掉半行", func(t *testing.T) {
		path := filepath.Join(dir, "big.log")
		// 字节上限落在某一行中间时，开头必然是半行。切在哪个字节上取决于上限，
		// 这里挑一个刚好落进第一行中间的位置。
		content := strings.Repeat("a", 20) + "\nKEEP1\nKEEP2\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		text, truncated, err := TailFile(path, 100, 8)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Error("应当报告截断")
		}
		// 半行残片必须丢掉，否则界面上会出现一行乱码。
		if text != "KEEP2\n" {
			t.Errorf("内容 = %q，想要「KEEP2\\n」（半行残片应当被丢掉）", text)
		}
	})

	t.Run("文件不存在时报 ErrNotExist", func(t *testing.T) {
		_, _, err := TailFile(filepath.Join(dir, "没有这个.log"), 10, 512)
		if !os.IsNotExist(err) {
			t.Errorf("错误 = %v，界面靠 IsNotExist 区分「还没启动过」和真故障", err)
		}
	})
}

func TestManagerIsSharedWithThePanel(t *testing.T) {
	h := newHarness(t)

	// 界面上的编辑入口和内核必须共用同一个业务层：各拿一个的话，保存之后
	// 只有其中一个会看到新清单，界面显示的和实际能起的两份清单会对不上。
	if h.p.Manager() != h.p.Manager() {
		t.Fatal("Manager() 每次返回的应当是同一个实例")
	}
	if h.p.Manager().Config() != h.p.Config() {
		t.Error("业务层手上的清单和内核的不是同一份")
	}
}
