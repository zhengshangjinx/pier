// Package panel 是 Pier 服务面板的内核。
//
// 它管的是「面板」这件事本身：当前加载了哪份清单、哪些服务在跑、谁在排队、
// 上一个动作失败了没有。界面怎么画、用什么技术画，它一概不知道——图形界面和
// 将来的原生界面都是它的宿主，各自把同样的值渲染成各自的形状。
//
// 两条设计约束值得写下来：
//
//   - 所有启停都丢进一个单消费者队列排队执行，入口函数立刻返回。Maven 编译一次
//     几十秒到几分钟，若在请求处理里同步跑完，界面在这段时间里除了一个转圈什么也
//     说明不了；排队还顺带保证同时只有一个编译在跑，否则并发的 Java 构建会互相抢
//     CPU、日志也会糊成一团。
//   - 排队状态、进行中的动作、失败原因都放在服务自己身上，由 State() 一并返回，
//     界面不需要自己维护「我刚才点了什么」——那份状态迟早会和真实情况对不上。
package panel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// LogLines 是一次返回给界面的最大日志行数。
const LogLines = 2000

// LogBytes 是一次从日志文件读取的最大字节数。界面在日志抽屉打开时每秒拉取，
// 整读一个有几十兆 Maven 输出的文件会把磁盘和内存都拖垮。
const LogBytes = 256 * 1024

// job 是一次待执行的启停动作。
type job struct {
	kind string // start / stop / restart
	svc  *config.Service
	op   *operation
}

// operation 是某个服务身上正在进行的动作。
//
// ctx / cancel 让「停止」能打断一次还没完成的启动（排队中、编译中、等待就绪都算）；
// started 在启动流程的同步部分（编译 + 拉起进程）结束或被跳过时关闭，
// 打断的一方等它关了再去停进程，免得停在前面、进程却在后面刚被拉起来。
type operation struct {
	kind    string
	phase   string // queued / running / waiting / error
	err     string
	ctx     context.Context
	cancel  context.CancelFunc
	started chan struct{}
}

func newOp(kind, phase string) *operation {
	ctx, cancel := context.WithCancel(context.Background())
	return &operation{kind: kind, phase: phase, ctx: ctx, cancel: cancel, started: make(chan struct{})}
}

// opLabels 把「动作 + 阶段」翻成界面文案。
var opLabels = map[string]map[string]string{
	"start":   {"queued": "排队启动", "running": "启动中", "waiting": "等待就绪"},
	"stop":    {"queued": "排队停止", "running": "停止中"},
	"restart": {"queued": "排队重启", "running": "重启中", "waiting": "等待就绪"},
}

// label 返回该动作当前该显示成什么。失败态不再显示动作，改由 OpErr 呈现原因。
func (o *operation) label() string {
	if o == nil || o.phase == "error" {
		return ""
	}
	if l, ok := opLabels[o.kind][o.phase]; ok {
		return l
	}
	return o.kind
}

// Panel 是面板内核。
type Panel struct {
	mu      sync.Mutex
	cfg     *config.Config
	sup     *proc.Supervisor
	cfgPath string
	cfgSrc  string // 清单来源，供界面显示
	cfgErr  string // 加载失败的原因；cfg 为 nil 时有效
	ops     map[string]*operation
	jobs    chan job
	mgr     *manage.Manager

	// notify 在「状态可能变了」时被调用，宿主据此推送事件。
	//
	// 它由 worker 协程调用，所以实现必须立刻返回：在这里同步跑一次 State()
	// 会做健康探测（最坏两秒），整个启停队列都会被堵住。正确做法是往 channel
	// 里非阻塞地丢一个信号，让宿主自己的循环去取状态。
	notify func()

	// onLoad 在每次成功加载清单后调用，宿主用它记住「上次用的是哪份」。
	//
	// 挂在 Panel 上而不是让宿主包一层 Load，是因为编辑入口（保存服务、改分组）
	// 写完之后也会触发一次重载，那条路径不经过宿主的显式调用——漏掉它，
	// 用户改完清单、重启界面，就会回到上一份旧清单上。
	onLoad func(path string)
}

// New 建一个空面板并启动执行协程。清单要另外用 Load 装进来。
func New() *Panel {
	p := &Panel{ops: map[string]*operation{}, jobs: make(chan job, 256)}
	p.mgr = manage.New(p.Load)
	// 删除服务前要看一眼这个服务身上有没有没结束的动作：状态文件只记已经拉起来的
	// 进程，编译中的服务那里什么都没有，光看文件会以为它没在跑。
	p.mgr.SetBusyProbe(p.HasOp)
	go p.worker()
	return p
}

// Manager 返回共用的清单编辑业务层，宿主把它接到自己的编辑入口上。
func (p *Panel) Manager() *manage.Manager { return p.mgr }

// HasOp 返回这个服务身上有没有排着或正在进行的动作（启动、停止、重启）。
func (p *Panel) HasOp(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.ops[name]
	return ok
}

// SetNotifier 装上状态变化通知。传 nil 表示不通知（宿主自己轮询）。
func (p *Panel) SetNotifier(fn func()) {
	p.mu.Lock()
	p.notify = fn
	p.mu.Unlock()
}

func (p *Panel) fireNotify() {
	p.mu.Lock()
	fn := p.notify
	p.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Load 加载配置并替换掉当前这份。失败时不破坏原有状态，调用方决定怎么提示。
func (p *Panel) Load(path string) error {
	cfg, err := config.Open(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.cfg, p.sup, p.cfgPath, p.cfgErr = cfg, proc.New(cfg), cfg.Path, ""
	// 换了清单，旧清单上的排队与错误都不再有意义，但正在进行的动作要分情况——
	// 编辑任意一个服务都会触发一次重载，顺手把别处正在跑的编译杀掉是不能接受的。
	//
	//   - 排队中还没开始的：撤掉。轮到它时队列里那个任务拿的还是旧定义（旧目录、
	//     旧端口、旧命令），用新的监管器去起它，起的是一份已经不存在的东西。
	//   - 已经开始的（编译中、拉起中、等待就绪）：服务还在新清单里就让它跑完；
	//     已经被删掉或改名的必须打断——编译完拉起来的进程会是一个界面看不见、
	//     命令行也找不着的孤儿，只能去活动监视器手工杀。
	//   - 停止：按名字清场与它在不在新清单里无关，服务刚被删掉时正是最需要它的时候。
	next := map[string]*operation{}
	for name, op := range p.ops {
		switch {
		case op.phase == "error", op.phase == "queued" && op.kind != "stop":
			op.cancel()
		case op.kind == "stop":
			next[name] = op
		default:
			if _, err := cfg.Find(name); err != nil {
				op.cancel()
				continue
			}
			next[name] = op
		}
	}
	p.ops = next
	p.mu.Unlock()

	// 业务层跟着换到新清单上。放在解锁之后：SetConfig 拿的是它自己的锁，
	// 两把锁不重叠，也就没有交叉持锁的机会。
	p.mgr.SetConfig(cfg)

	p.mu.Lock()
	hook := p.onLoad
	p.mu.Unlock()
	if hook != nil {
		hook(cfg.Path)
	}

	p.fireNotify()
	return nil
}

// SetOnLoad 装上「加载成功后」的钩子，宿主用它记录当前清单路径。
func (p *Panel) SetOnLoad(fn func(path string)) {
	p.mu.Lock()
	p.onLoad = fn
	p.mu.Unlock()
}

// SetLoadError 记下「清单没能加载」的原因，供界面显示。
func (p *Panel) SetLoadError(msg string) {
	p.mu.Lock()
	p.cfgErr = msg
	p.mu.Unlock()
}

// SetSource 记录这份清单是怎么来的（命令行指定 / 上次使用 / 手动指定…）。
func (p *Panel) SetSource(src string) {
	p.mu.Lock()
	p.cfgSrc = src
	p.mu.Unlock()
}

// Source 返回清单来源。
func (p *Panel) Source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfgSrc
}

// Config 返回当前清单，可能为 nil。
func (p *Panel) Config() *config.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg
}

// ConfigPath 返回当前清单路径，未加载时为空。
func (p *Panel) ConfigPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfgPath
}

// ConfigErr 返回清单加载失败的原因。
func (p *Panel) ConfigErr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfgErr
}

// ── 排队与执行 ───────────────────────────────────────────────────────────

func (p *Panel) worker() {
	for j := range p.jobs {
		p.exec(j)
	}
}

func (p *Panel) exec(j job) {
	defer close(j.op.started)
	// 排队期间被「停止」撤销了：什么都不做，状态已经交给停止那一方。
	if j.op.ctx.Err() != nil {
		return
	}
	// 排队期间换了清单：这个任务拿的还是旧清单上的服务（旧目录、旧命令），
	// 队列那边已经把它从簿记里撤掉了（见 Load）。用新监管器把它拉起来，
	// 只会造出一个新清单里根本没有的进程。
	if !p.isCurrent(j.svc.Name, j.op) {
		return
	}
	p.setPhase(j.svc.Name, j.op, "running")
	p.fireNotify()
	switch j.kind {
	case "stop":
		if err := p.stopOne(j.svc); err != nil {
			p.fail(j.svc.Name, j.op, err)
			return
		}
		p.finish(j.svc.Name, j.op)
	case "restart":
		// 停止阶段失败（通常是端口被 Pier 之外的进程占着）时不再继续启动，
		// 否则只会在一个注定冲突的端口上再撞一次。
		if err := p.stopOne(j.svc); err != nil {
			p.fail(j.svc.Name, j.op, err)
			return
		}
		p.startOne(j)
	default:
		p.startOne(j)
	}
}

// isCurrent 判断这个服务身上排着的还是不是同一个动作。
//
// 任务从入队到被取出之间，清单可能被换过（Load 会把不再作数的动作从簿记里撤掉），
// 停止也可能插进来把它顶掉（换成 stop 动作并取消它的 ctx）。两种情况下队列里
// 那个任务都已经是「上一轮的事」，不该再去动进程。
func (p *Panel) isCurrent(name string, op *operation) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ops[name] == op
}

// stopOne 停止服务。返回错误才算失败——「本来就停着」是正常情况，不是错误。
func (p *Panel) stopOne(svc *config.Service) error {
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		return errors.New("尚未加载服务清单")
	}

	err := sup.Stop(svc.Name)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, proc.ErrAlreadyGone):
		return nil
	case errors.Is(err, proc.ErrNotManaged):
		// 没有记录有两种情况，必须分开：本来就停着（正常），
		// 或端口被 IDEA / 别的终端占着（要明确告知，因为 Pier 动不了它）。
		if svc.Port > 0 && proc.PortOpen(svc.Port) {
			return fmt.Errorf("端口 %d 被 Pier 之外的进程占用，Pier 不会去动它", svc.Port)
		}
		return nil
	default:
		return err
	}
}

// startOne 启动服务。Start 返回时进程已经拉起，但编译型服务还要初始化，
// 所以配了健康探针的服务继续停在「等待就绪」，探针通过才算完成。
//
// 整个过程都可能被「停止」打断（ctx 被取消）：那时不写任何状态——
// 这个服务的状态已经归停止那一方管，这里再写只会把「停止中」覆盖掉。
func (p *Panel) startOne(j job) {
	svc, op := j.svc, j.op
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup == nil {
		p.fail(svc.Name, op, errors.New("尚未加载服务清单"))
		return
	}

	// 端口已被监听说明可能已经在 IDEA 或别的终端跑着，此时再起一个必然冲突。
	if svc.Port > 0 && proc.PortOpen(svc.Port) {
		p.fail(svc.Name, op, fmt.Errorf("端口 %d 已被占用（可能已在 IDEA 或其它终端运行）", svc.Port))
		return
	}
	if err := sup.StartContext(op.ctx, svc); err != nil {
		if op.ctx.Err() == nil {
			p.fail(svc.Name, op, err)
		}
		return
	}
	if svc.Health == "" {
		p.finish(svc.Name, op)
		return
	}
	p.setPhase(svc.Name, op, "waiting")
	p.fireNotify()
	// 健康等待不占着队列：否则一个起不来的 Java 服务会把后面所有服务堵上三分钟。
	//
	// 等满窗口还没通过不算启动失败——进程是 Pier 自己拉起来的，它就在那儿跑着，
	// 失败的是探针那一路（地址填错、服务没有健康接口、接口不返回 2xx）。
	// 这里照常收尾，状态交给 Status()：它会把这个服务报成「运行中 + 探针未通过」
	// （见 proc.Status.ProbeExpired）。
	//
	// 原先这里报「启动失败」，代价是正确的东西被标成错的：服务明明在跑、端口
	// 明明在听，界面却挂着一个红错，而错误信息里那句「详见日志」指过去的日志里
	// 一行错都没有。真正需要人注意的情况——服务起不来——另有更准的信号：
	// 进程不在了（stale），或者端口没起来。
	go func() {
		proc.WaitHealthyContext(op.ctx, svc.Health, proc.HealthWait)
		if op.ctx.Err() == nil {
			p.finish(svc.Name, op)
		}
	}()
}

// 下面三个写状态的方法都先核对「这个服务身上的动作还是不是 op」：被停止打断的
// 启动流程稍后可能还会回来写一次，那时它已经不是当前动作了，写了就会把新状态冲掉。

func (p *Panel) setPhase(name string, op *operation, phase string) {
	p.mu.Lock()
	if p.ops[name] == op {
		op.phase = phase
	}
	p.mu.Unlock()
}

func (p *Panel) finish(name string, op *operation) {
	p.mu.Lock()
	if p.ops[name] == op {
		delete(p.ops, name)
	}
	p.mu.Unlock()
	p.fireNotify()
}

// fail 记下失败原因。条目留在 ops 里而不是删掉，是为了让界面能把原因显示出来；
// 该服务下一次操作时会自然被覆盖。
func (p *Panel) fail(name string, op *operation, err error) {
	p.mu.Lock()
	if p.ops[name] == op {
		op.phase, op.err = "error", err.Error()
	}
	p.mu.Unlock()
	p.fireNotify()
}

// begin 校验并占住一个服务，返回待执行的任务。它不碰队列。
//
// 与入队分开是为了能单独验证「谁能开始、谁被挡下」：那部分全是判断，跑起来不碰
// 任何进程；混在入队里测，就只能靠真起一个服务去观察，代价大得多。
//
// 校验必须在占位之前完成，这样界面能立刻拿到「不行」的反馈，
// 而不是等排到队头才发现服务名是错的。
func (p *Panel) begin(kind, name string) (*config.Service, *operation, error) {
	cfg := p.Config()
	if cfg == nil {
		return nil, nil, manage.ErrNoConfig
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return nil, nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if op := p.ops[name]; op != nil && op.phase != "error" {
		return nil, nil, fmt.Errorf("%s 正在%s，请等它结束", name, op.label())
	}
	// 直接覆盖：上一个动作若是失败态，它的原因到这里就该让位——
	// 否则界面会一边显示「排队重启」一边显示上次的报错。
	op := newOp(kind, "queued")
	p.ops[name] = op
	return svc, op, nil
}

// enqueue 把一个动作排进队列。
func (p *Panel) enqueue(kind, name string) (string, error) {
	svc, op, err := p.begin(kind, name)
	if err != nil {
		return "", err
	}
	p.jobs <- job{kind: kind, svc: svc, op: op}
	p.fireNotify()
	return "", nil
}

// enqueueAll 把清单里的全部服务排队。顺序即配置顺序，与命令行的 up/down 一致。
func (p *Panel) enqueueAll(kind string) (string, error) {
	p.mu.Lock()
	cfg := p.cfg
	if cfg == nil {
		p.mu.Unlock()
		return "", manage.ErrNoConfig
	}
	// 先把能排的都标记上再统一入队：标记与入队在同一把锁里完成，
	// 中间不会插进另一个请求把同一个服务排两遍。
	targets := make([]job, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		if op := p.ops[svc.Name]; op != nil && op.phase != "error" {
			continue
		}
		op := newOp(kind, "queued")
		p.ops[svc.Name] = op
		targets = append(targets, job{kind: kind, svc: svc, op: op})
	}
	p.mu.Unlock()

	if len(targets) == 0 {
		return "", errors.New("没有可执行的服务（都已在操作中）")
	}
	for _, j := range targets {
		p.jobs <- j
	}
	p.fireNotify()
	return fmt.Sprintf("已把 %d 个服务排入队列，按顺序执行", len(targets)), nil
}

// ResetToolchains 让下一次启动、下一次状态刷新按最新的 SDK 设置重新选工具链。
func (p *Panel) ResetToolchains() {
	p.mu.Lock()
	sup := p.sup
	p.mu.Unlock()
	if sup != nil {
		sup.ResetToolchains()
	}
}

// ── 对外的动作 ─────────────────────────────────────────────────────────────

// Start 排队启动一个服务。
func (p *Panel) Start(name string) (string, error) { return p.enqueue("start", name) }

// Stop 停止一个服务，不管它此刻处在什么状态。
//
// 空闲或失败态：照常排队停止。正在启动（排队中、编译中、拉起中、等待就绪）：
// 直接打断那次启动——编译连同整组子进程一起结束，已经拉起的进程接着停掉——
// 而不是排到它后面。一次 Maven 编译几十秒到几分钟，起错了、起重了还要干等它跑完，
// 这正是「启动中停不下来」最让人抓狂的地方。已经在停止中的，重复点不算错。
func (p *Panel) Stop(name string) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	cur := p.ops[name]
	if cur != nil && cur.phase != "error" && cur.kind == "stop" {
		p.mu.Unlock()
		return "", nil
	}
	if cur == nil || cur.phase == "error" {
		p.mu.Unlock()
		return p.enqueue("stop", name)
	}
	// 打断进行中的启动 / 重启。
	wasQueued := cur.phase == "queued"
	stopOp := newOp("stop", "running")
	close(stopOp.started) // 停止不走队列，没有「同步部分」可等
	p.ops[name] = stopOp
	p.mu.Unlock()
	cur.cancel()
	p.fireNotify()

	go func() {
		// 还在排队的那次启动根本没开始，轮到它时会自己跳过，不必等；
		// 已经开始的要等它的同步部分退出（编译被杀、或进程刚拉起），再去停进程。
		if !wasQueued {
			<-cur.started
		}
		if err := p.stopOne(svc); err != nil {
			p.fail(name, stopOp, err)
			return
		}
		p.finish(name, stopOp)
	}()
	return "", nil
}

// Restart 排队重启一个服务。
func (p *Panel) Restart(name string) (string, error) { return p.enqueue("restart", name) }

// StartAll 排队启动清单里的全部服务。
func (p *Panel) StartAll() (string, error) { return p.enqueueAll("start") }

// StopAll 停止清单里的全部服务，正在启动的也一并打断（见 Stop）。
func (p *Panel) StopAll() (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	n := 0
	for _, svc := range cfg.Services {
		if _, err := p.Stop(svc.Name); err == nil {
			n++
		}
	}
	return fmt.Sprintf("已对 %d 个服务发出停止，正在启动的已打断", n), nil
}

// SetConfig 切换到用户指定的清单。先确认能加载成功再换，避免把界面切到一个坏路径上。
func (p *Panel) SetConfig(path string) (string, error) {
	if err := p.Load(path); err != nil {
		return "", err
	}
	p.SetSource("手动指定")
	return "已加载 " + path, nil
}

// Prune 清理「有记录但进程已不在」的残留。命令行的 down 会顺手清，
// 但服务崩溃或被手工 kill 之后，得有人来收尾。
func (p *Panel) Prune() (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	var dead []string
	if err := proc.UpdateState(cfg.StatePath(), func(st *proc.State) error {
		dead = st.Prune()
		return nil
	}); err != nil {
		return "", err
	}
	if len(dead) == 0 {
		return "没有需要清理的记录", nil
	}
	sort.Strings(dead)
	return fmt.Sprintf("已清理 %d 条失效记录：%s", len(dead), joinNames(dead)), nil
}

// ── 交给系统处理的几个动作 ───────────────────────────────────────────────
//
// 只返回路径，实际「打开」由宿主完成：那是系统集成，不是面板内核的事。
// 路径一律由清单推导，界面传什么都无法让宿主去打开一个任意路径。

// Lookup 按名字取服务。
func (p *Panel) Lookup(name string) (*config.Service, *config.Config, error) {
	cfg := p.Config()
	if cfg == nil {
		return nil, nil, manage.ErrNoConfig
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return nil, nil, err
	}
	return svc, cfg, nil
}

// ServiceDir 返回服务的工作目录，供宿主在访达里显示。
func (p *Panel) ServiceDir(name string) (string, error) {
	svc, _, err := p.Lookup(name)
	if err != nil {
		return "", err
	}
	return svc.AbsDir(), nil
}

// ServiceLogPath 返回服务此刻在写（或最近写过）的那份日志文件的路径。
//
// 拿的是最新的一份而不是今天那份：跨了零点还在跑的服务写的一直是启动那天的文件，
// 「在访达中显示」应该跳到那个真实存在的文件上。
func (p *Panel) ServiceLogPath(name string) (string, error) {
	_, cfg, err := p.Lookup(name)
	if err != nil {
		return "", err
	}
	return proc.LogFile(cfg, name), nil
}

// HealthURL 返回服务的健康检查地址，没配则报错。
func (p *Panel) HealthURL(name string) (string, error) {
	svc, _, err := p.Lookup(name)
	if err != nil {
		return "", err
	}
	if svc.Health == "" {
		return "", fmt.Errorf("%s 没有配置健康检查地址", name)
	}
	return svc.Health, nil
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "、"
		}
		out += n
	}
	return out
}
