//go:build !windows

package panel

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// restartYAML 是一份带重启策略的清单。alpha 开着自动重启，beta 没开。
const restartYAML = `
services:
  - name: alpha
    dir: a
    kind: go
    restart: on-failure
  - name: beta
    dir: b
    kind: go
`

// deadPID 找一个确定已经不在的进程号。
//
// 不复用刚退出的那个也是刻意的：PID 被复用是这套判断最怕的事，
// 拿一个没人用过的号更干净。
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 999999; pid > 100000; pid-- {
		if !proc.ProcessAlive(pid) {
			return pid
		}
	}
	t.Skip("找不到一个空闲的进程号")
	return 0
}

// newOwnedPanel 起一个面板，并在用例结束时先等队列跑空再收尾。
//
// 排队重启是真的会跑起来的：worker 拿到任务就去建日志目录、去拉进程。不等它
// 跑完就结束用例，tempdir 的清理会在半路上撞见一个刚建出来的目录，
// 报一句和被测行为毫无关系的「目录非空」。
func newOwnedPanel(t *testing.T) *Panel {
	t.Helper()
	p := New()
	t.Cleanup(func() {
		drain(t, p)
		p.Close()
	})
	return p
}

// drain 等没有动作还在跑了。
//
// 等的就是 BusyCount——界面上「N 个动作在进行」用的也是它，所以在测试里等的
// 和用户看到的是同一件事。不能数 ops 里有几条：失败的动作是刻意留在那儿的，
// 界面靠它显示「上次为什么没起来」，它会一直留在簿记里直到用户再动一次手。
//
// 等到零为止是有依据的：动作在入队之前就登记在案（见 enqueueRestart 里那段
// 加锁的说明），所以按下按钮的那一刻它已经算数了，不会漏掉还没开始的那些。
func drain(t *testing.T, p *Panel) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p.State().BusyCount == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("队列一直没跑空，可能有动作卡住了")
}

// restartHarness 起一个带重启策略的面板，并把状态文件铺好。
func restartHarness(t *testing.T, entries map[string]*proc.Entry) *harness {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(restartYAML), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	h := &harness{t: t, dir: dir, base: base, p: p}

	cfg := p.Config()
	if err := proc.UpdateState(cfg.StatePath(), func(s *proc.State) error {
		s.Services = entries
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
	return h
}

// markRestarts 手工记上几次重启，绕开真正拉起进程那一步。
func markRestarts(h *harness, name string, times int) {
	h.t.Helper()
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	now := time.Now()
	for i := 0; i < times; i++ {
		h.p.restarts[name] = append(h.p.restarts[name], now)
	}
}

// pendingRestart 报告这个服务身上是不是排着一次自动重启。
func pendingRestart(h *harness, name string) bool {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	op := h.p.ops[name]
	return op != nil && op.kind == "start"
}

// currentOp 取出这个服务身上那个动作，用来核对「还是同一个」。
func currentOp(h *harness, name string) *operation {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	return h.p.ops[name]
}

// TestRecoverCrashedOnlyForOnFailure 钉着「谁开了自动重启才救谁」。
//
// restart 留空是默认，也是大多数服务该有的样子：编辑器里正调着的服务崩了就崩了，
// 自动把它拉起来会盖掉那份现场。
func TestRecoverCrashedOnlyForOnFailure(t *testing.T) {
	pid := deadPID(t)
	h := restartHarness(t, map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
		"beta":  {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
	})

	h.p.recoverCrashed()

	if !pendingRestart(h, "alpha") {
		t.Error("开了 on-failure 的 alpha 没有被排上重启")
	}
	if pendingRestart(h, "beta") {
		t.Error("没开重启策略的 beta 也被排上了")
	}
}

// TestRecoverCrashedSkipsServicesWithNoRecord 钉着「从来没起来过的不重来」。
//
// 没有记录多半是启动就失败了——端口冲突、依赖没装、编译不过。再起一次还是
// 同样的结果，而每几秒重试一次会把日志刷满，把真正的原因埋掉。
func TestRecoverCrashedSkipsServicesWithNoRecord(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{})
	h.p.recoverCrashed()
	if pendingRestart(h, "alpha") {
		t.Error("状态里没有记录的服务被重来了")
	}
}

// TestRecoverCrashedLeavesRunningAlone 钉着还活着的服务不动。
func TestRecoverCrashedLeavesRunningAlone(t *testing.T) {
	// 拿当前进程自己当那个「还活着的服务」。PGID 要给真的那个：这一条判定
	// 比的是「还在不在原来那个进程组里」，而测试进程的组号并不等于它自己的 PID。
	me := os.Getpid()
	pgid, err := syscall.Getpgid(me)
	if err != nil {
		t.Fatalf("取不到自己的进程组号：%v", err)
	}
	h := restartHarness(t, map[string]*proc.Entry{
		"alpha": {PID: me, PGID: pgid, StartedAt: time.Now()},
	})
	h.p.recoverCrashed()
	if pendingRestart(h, "alpha") {
		t.Error("还在跑的 alpha 被重来了")
	}
}

// TestRestartBudgetRunsOut 钉着额度：窗口里救够次数就停手。
//
// 一个每秒都崩的服务会一直崩下去；没有上限的话，日志、CPU 和磁盘都会被这一次
// 无穷无尽的「重试」占满，而问题本身还在原地。
func TestRestartBudgetRunsOut(t *testing.T) {
	pid := deadPID(t)
	h := restartHarness(t, map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
	})
	markRestarts(h, "alpha", restartLimit)

	h.p.recoverCrashed()
	if pendingRestart(h, "alpha") {
		t.Errorf("已经救过 %d 次了还在排", restartLimit)
	}

	// 说明位要把这件事说出来：界面上「已自动重启 N 次」和「不救了」是两回事，
	// 用户据此决定是自己去看一眼，还是继续等它自己好。
	note := h.p.restartNote("alpha")
	if !strings.Contains(note, "上限") {
		t.Errorf("到顶之后的说明 = %q，没提上限", note)
	}
	if n := h.find(t, "alpha").RestartNote; n != note {
		t.Errorf("界面上的说明 = %q，restartNote = %q", n, note)
	}
}

// TestRestartBudgetRefillsAfterWindow 钉着额度会随时间回来。
//
// 用完就永久放弃的话，一个中午崩过的服务到了晚上还是「不再自动重启」，
// 而这两件事之间早就没有关系了。
func TestRestartBudgetRefillsAfterWindow(t *testing.T) {
	pid := deadPID(t)
	h := restartHarness(t, map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
	})
	h.p.mu.Lock()
	for i := 0; i < restartLimit; i++ {
		h.p.restarts["alpha"] = append(h.p.restarts["alpha"], time.Now().Add(-restartWindow-time.Minute))
	}
	h.p.mu.Unlock()

	if got := h.p.restartNote("alpha"); got != "" {
		t.Errorf("窗口外的记录还留着说明：%q", got)
	}
	h.p.recoverCrashed()
	if !pendingRestart(h, "alpha") {
		t.Error("窗口过去之后额度没回来")
	}
}

// TestRestartNoteCounts 钉着说明里那个数字是窗口内的次数。
func TestRestartNoteCounts(t *testing.T) {
	h := restartHarness(t, nil)
	if got := h.p.restartNote("alpha"); got != "" {
		t.Errorf("没重启过时说明 = %q，想要空", got)
	}
	markRestarts(h, "alpha", 2)
	if got := h.p.restartNote("alpha"); !strings.Contains(got, "2 次") {
		t.Errorf("说明 = %q，想要带上「2 次」", got)
	}
}

// TestRecoverCrashedDoesNotPileUp 钉着同一次崩溃不会被排两次。
//
// 巡检每几秒跑一遍，而一次自动重启要编译、要拉起、要等就绪，可能跨过好几个
// 巡检周期。不记账的话，同一秒里的两次轮询会各排一次，两套编译抢同一个端口。
func TestRecoverCrashedDoesNotPileUp(t *testing.T) {
	pid := deadPID(t)
	h := restartHarness(t, map[string]*proc.Entry{
		"alpha": {PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)},
	})

	h.p.recoverCrashed()
	if !pendingRestart(h, "alpha") {
		t.Fatal("第一次没排上")
	}
	before := currentOp(h, "alpha")
	h.p.recoverCrashed()

	if after := currentOp(h, "alpha"); after != before {
		t.Error("第二次巡检把已经在排队的那次顶掉了")
	}
	if n := len(h.p.restarts["alpha"]); n != 1 {
		t.Errorf("记了 %d 次账，想要 1 次", n)
	}
}

// waitOwns 等这个面板把独占权接过去，最多等两个巡检周期再多一点。
func waitOwns(t *testing.T, p *Panel) bool {
	t.Helper()
	deadline := time.Now().Add(2*restartPoll + time.Second)
	for time.Now().Before(deadline) {
		if p.OwnsRestarts() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return p.OwnsRestarts()
}

// TestSecondPanelDoesNotOwnRestarts 钉着「自动重启只由拿到独占权的那个进程跑」。
//
// 同一台机器上可以同时开着图形界面和 pier api。两个巡检看着同一个状态文件、
// 同一批服务，没有独占权的话，一个崩了的服务会被两边同时拉起来——
// 两套编译、两份日志、两个进程抢同一个端口，而两边都以为自己处理得很干净。
func TestSecondPanelDoesNotOwnRestarts(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())

	first := New()
	defer first.Close()
	if !first.OwnsRestarts() {
		t.Fatal("第一个面板没拿到独占权")
	}

	second := New()
	defer second.Close()
	if second.OwnsRestarts() {
		t.Error("第二个面板也拿到了独占权，两边会各巡检一遍")
	}

	// 第一个退出之后由第二个接过去。这一步必须真等巡检自己再抢一次，
	// 不能只看「再建一个面板拿不拿得到」：拿它的那个窗口可能是异常退出
	// （崩溃、被强杀、被活动监视器结束），那时候没有任何人来交回这把锁，
	// 巡检只挂在 New 那一次抢锁上的话，这台机器到下次开机为止都不会再自动重启，
	// 而界面上「崩了会自动重启」那句话还写着。
	first.Close()
	if !waitOwns(t, second) {
		t.Error("前一个退出之后，还在跑的那个面板一直没接过去")
	}
}

// TestSecondPanelDoesNotClobberLedger 钉着没拿到独占权的窗口不写记账文件。
//
// 它手里那份多半是空的（没巡检就没记过账），照写一遍等于把别人的账本抹掉——
// 另一个窗口的额度会凭空回来，而这正是「救过几次」这个数唯一要防的事。
func TestSecondPanelDoesNotClobberLedger(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())

	first := New()
	defer first.Close()
	if !first.OwnsRestarts() {
		t.Fatal("第一个面板没拿到独占权")
	}
	first.mu.Lock()
	first.restarts["alpha"] = []time.Time{time.Now()}
	first.persistRestartsLocked()
	first.mu.Unlock()

	second := New()
	defer second.Close()
	if second.OwnsRestarts() {
		t.Fatal("第二个面板不该拿到独占权")
	}
	// 用户在这个窗口里自己动了一次手——它照样要清自己手里那份记账，
	// 但落盘那一步必须跳过。
	second.mu.Lock()
	second.clearRestartsLocked("alpha")
	second.mu.Unlock()

	if got := loadRestarts(time.Now()); len(got["alpha"]) != 1 {
		t.Errorf("没拿到独占权的窗口把账本写坏了：%v", got)
	}
}

// TestRestartLedgerSurvivesReopen 钉着「重开一次界面」不会把已经救过几次这笔账抹掉。
//
// 这笔账要回答的是「还要不要继续救它」，而重开界面恰好是人碰上崩溃循环时
// 最自然的动作。只记在内存里的话，每重开一次额度就回满，一个永远起不来的服务
// 会被无限救下去，日志也跟着被刷满。
func TestRestartLedgerSurvivesReopen(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())

	first := New()
	if !first.OwnsRestarts() {
		t.Fatal("第一个面板没拿到独占权")
	}
	first.mu.Lock()
	for i := 0; i < restartLimit; i++ {
		first.restarts["alpha"] = append(first.restarts["alpha"], time.Now())
	}
	first.persistRestartsLocked()
	first.mu.Unlock()
	first.Close()

	second := New()
	defer second.Close()
	if !second.OwnsRestarts() {
		t.Fatal("第二个面板没接过去")
	}
	// 走 restartNote：界面上显示的就是它，而「到上限了」这件事正是用户
	// 重开界面想确认的那一句。
	if got := second.restartNote("alpha"); !strings.Contains(got, "上限") {
		t.Errorf("重开之后记账 = %q，想要还看得见「已达上限」", got)
	}
}

// TestCloseIsIdempotent 钉着重复收尾不炸：宿主可能既在收尾处调一次，
// 又在信号处理里调一次。
func TestCloseIsIdempotent(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	p := New()
	p.Close()
	p.Close()
}

// TestStartPullsInDependencies 钉着「启动一个服务会先把它的前置排进去」。
//
// 只排它自己是不够的：前置没起来，起来的那个会立刻报连接失败，
// 而报错的地方在日志里、看着像是这个服务自己的毛病。
func TestStartPullsInDependencies(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	yaml := `
services:
  - name: web
    dir: w
    kind: go
    depends_on: [api]
  - name: api
    dir: a
    kind: go
    depends_on: [db]
  - name: db
    dir: d
    kind: go
  - name: other
    dir: o
    kind: go
`
	if err := os.WriteFile(base, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}

	msg, err := p.Start("web")
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	// 说了哪几个被一并排上：少了哪一步在这句话里能看出来。
	for _, want := range []string{"web", "api", "db"} {
		if !strings.Contains(msg, want) {
			t.Errorf("说明里没提 %s：%q", want, msg)
		}
	}
	if strings.Contains(msg, "other") {
		t.Errorf("没依赖关系的 other 也被排上了：%q", msg)
	}

	// 队列真的按顺序排上了：三个都在，且 db 在 api 前面、api 在 web 前面。
	p.mu.Lock()
	got := map[string]*operation{}
	for name, op := range p.ops {
		got[name] = op
	}
	p.mu.Unlock()
	for _, name := range []string{"web", "api", "db"} {
		if got[name] == nil {
			t.Fatalf("%s 没排进队列：%v", name, got)
		}
	}

	// 队列跑空由 newOwnedPanel 注册的收尾负责，见那里的说明。
}

// TestStopOrderIsReversed 钉着成批停止时先停后起的那些。
//
// 反过来停的话，还在跑的服务会对着一个已经关掉的端口刷连接错误——
// 一串没有意义的报错，把真正的原因盖在下面。
func TestStopOrderIsReversed(t *testing.T) {
	c := &config.Config{Services: []*config.Service{
		{Name: "db"},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "web", DependsOn: []string{"api"}},
	}}
	var start, stop []string
	for _, s := range c.StartOrder() {
		start = append(start, s.Name)
	}
	for _, s := range c.StopOrder() {
		stop = append(stop, s.Name)
	}
	if strings.Join(start, " ") != "db api web" {
		t.Errorf("启动顺序 = %v", start)
	}
	if strings.Join(stop, " ") != "web api db" {
		t.Errorf("停止顺序 = %v，想要启动顺序倒过来", stop)
	}
}
