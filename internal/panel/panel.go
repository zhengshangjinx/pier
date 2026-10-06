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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/diag"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// LogLines 是一次返回给界面的最大日志行数。
const LogLines = 2000

// LogBytes 是一次从日志文件读取的最大字节数。界面在日志抽屉打开时每秒拉取，
// 整读一个有几十兆 Maven 输出的文件会把磁盘和内存都拖垮。
const LogBytes = 256 * 1024

// 自动重启的额度：restartWindow 之内最多 restartLimit 次。
//
// 加窗口是为了让额度自己过期。不加的话，一个每隔几天崩一次的服务攒够三次之后
// 就再也救不回来了——那三次可能横跨一个月。反过来，起来就崩、起来就崩
// （依赖没装、端口配错）的，十秒内就会用光额度，不至于无限重试下去。
const (
	restartWindow = 10 * time.Minute
	restartLimit  = 3
	// restartPoll 是查一遍「有没有谁崩了」的间隔。
	//
	// 比界面的刷新间隔（两秒）再慢一点：这不是状态展示，是补救动作，
	// 崩溃之后多等一两秒完全值得，换来的是更少的空转。
	restartPoll = 3 * time.Second

	// restartLockName 是自动重启独占权的锁文件名，落在数据目录下。
	restartLockName = "restart.lock"
)

// job 是一次待执行的启停动作。
type job struct {
	kind string // start / stop / restart
	svc  *config.Service
	op   *operation
	// auto 为真表示这次是巡检自己排的（见 enqueueRestart），不是用户点的。
	//
	// 只影响「起不来的时候要不要发系统通知」：用户点的那一下，他正看着屏幕，
	// 界面上已经写着为什么没起来；巡检排的那一次没人在看，不说就没人知道。
	auto bool
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
	// id 是这把操作自己的编号，只用来给通知去重（见 opKey）。
	// 不用指针当编号：同一个地址会被后来的分配复用，两次不同的失败就可能被认成同一次。
	id uint64
}

// opSeq 发号。进程内单调递增，不复用。
var opSeq atomic.Uint64

func newOp(kind, phase string) *operation {
	ctx, cancel := context.WithCancel(context.Background())
	return &operation{
		kind: kind, phase: phase, ctx: ctx, cancel: cancel,
		started: make(chan struct{}), id: opSeq.Add(1),
	}
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

// opInfo 把动作翻成快照里那一组字段。失败态不显示动作、只留原因，
// 这条判断留在这里就够，别让快照的组装知道 phase 有哪几个值。
func (o *operation) opInfo() OpInfo {
	info := OpInfo{Label: o.label(), Kind: o.kind}
	if o.phase == "error" {
		info.Err = o.err
	}
	return info
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

	// restarts 记着每个服务最近几次自动重启的时刻，用来算额度（见 restartWindow）。
	// 用户自己动过手（启动、停止、重启）就清掉：那是「我知道了，再来」，
	// 不该还背着一笔自动重启的账。
	//
	// 拿到独占权时从 restarts.json 读回来，记一次就写回去：关掉界面再打开，
	// 一个还在崩溃循环里的服务不该重新拥有全部额度（见 restarts.go）。
	restarts map[string][]time.Time

	// restartClaim 是「自动重启这把活归我干」的独占权，拿不到时为 nil。
	//
	// 同一台机器上可以同时开着不止一个 Pier：图形界面、命令行面板、pier api。
	// 每个都有自己的巡检，而它们看见的是同一个状态文件、同一批服务。不给这件事
	// 指定唯一的主人，一个崩了的服务会被两边同时拉起来——两套编译、两份日志、
	// 两个进程抢同一个端口，而两边都以为自己处理得很干净。
	//
	// 锁落在数据目录而不是状态文件旁边：独占权说的是「这台机器上的 Pier 谁来巡检」，
	// 与这一份清单是哪一个无关，也就不必在每次加载清单时换手。
	restartClaim *proc.Claim

	// restartLock 是上面那把锁的位置，New 里算一次：每一轮巡检都要去抢它
	// （持有它的那个窗口退出之后得能接过去），不想到时候再算一遍路径。
	restartLock string

	// done 在 Close 时关上，巡检据此收工。
	//
	// 少了这一条，面板关掉之后巡检还在跑：宿主调 Close 说的是「我不干了」，
	// 而一个还在替人拉服务的协程跟这句话正相反——界面窗口已经关了，
	// 后台每隔几秒又去看一眼、又去拉一次。
	done      chan struct{}
	closeOnce sync.Once

	// notify 在「状态可能变了」时被调用，宿主据此推送事件。
	//
	// 它由 worker 协程调用，所以实现必须立刻返回：在这里同步跑一次 State()
	// 会做健康探测（最坏两秒），整个启停队列都会被堵住。正确做法是往 channel
	// 里非阻塞地丢一个信号，让宿主自己的循环去取状态。
	notify func()

	// userNotify 是给用户看的系统通知（标题 + 正文），只在真出事时响一次：
	// 服务自己没了、启动失败、自动重启到上限。
	//
	// 与上面那个 notify 分开：那个是宿主的刷新信号，每两秒都在响，长什么样
	// 由宿主自己决定；这个直接是给用户的一句话。命令行、pier api 这些宿主不装它
	// （传 nil 就是不发），它们背后没有「用户此刻正在哪儿」可言，
	// 弹一条系统通知只会莫名其妙。
	//
	// 调用规则与 notify 一样：由 worker 或巡检协程调用，实现必须立刻返回。
	// 起一个子进程去弹通知（osascript、PowerShell）要半秒，那半秒不该堵住
	// 启停队列，也不该拖慢巡检——所以真正的动作留给宿主自己去开协程。
	userNotify func(title, body string)

	// told 记着每一件事最近通知到哪一次了，键是「服务名 + 用途」，值是这件事的标识
	// （见 crashKey / opKey）。
	//
	// 没有它就会一直响：一个服务崩了、额度也用光了，而「它不在了」这件事每三秒
	// 被看见一次，每次都走到「到上限了」那一步，于是同一个服务每三秒弹一条。
	//
	// 按「服务名 + 用途」分栏，而不是一个服务一栏：「它崩了」和「它拉不起来」
	// 是同一次崩溃上的两件事、两条通知，挤在一栏里后发的会把前一条的记号顶掉，
	// 下一次巡检看见记号对不上，就把前一条重发一遍。
	told map[string]string

	// onLoad 在每次成功加载清单后调用，宿主用它记住「上次用的是哪份」。
	//
	// 挂在 Panel 上而不是让宿主包一层 Load，是因为编辑入口（保存服务、改分组）
	// 写完之后也会触发一次重载，那条路径不经过宿主的显式调用——漏掉它，
	// 用户改完清单、重启界面，就会回到上一份旧清单上。
	onLoad func(path string)
}

// New 建一个空面板并启动执行协程。清单要另外用 Load 装进来。
func New() *Panel {
	p := &Panel{
		ops:      map[string]*operation{},
		jobs:     make(chan job, 256),
		restarts: map[string][]time.Time{},
		told:     map[string]string{},
		done:     make(chan struct{}),
	}
	p.mgr = manage.New(p.Load)
	// 删除服务前要看一眼这个服务身上有没有没结束的动作：状态文件只记已经拉起来的
	// 进程，编译中的服务那里什么都没有，光看文件会以为它没在跑。
	p.mgr.SetBusyProbe(p.HasOp)
	go p.worker()
	// 巡检只由拿到独占权的那个进程跑，见 restartClaim。先抢一次，抢不到也不打紧：
	// watchRestarts 每轮还会再试——持有它的那个窗口退出之后，这份活得有人接过去。
	//
	// 清单目录建不出来时也照样跑：拿不到锁顶多是别的进程在巡检，
	// 而这里是「连目录都没有」，多半是第一次运行，不会有人跟它抢。
	p.restartLock = restartLockPath()
	p.ensureRestartLock()
	go p.watchRestarts()
	return p
}

// restartLockPath 是自动重启独占权那把锁的位置。
func restartLockPath() string {
	d, err := config.Dirs()
	if err != nil {
		return filepath.Join(os.TempDir(), "pier-restart.lock")
	}
	return filepath.Join(d.Data, restartLockName)
}

// Close 停掉巡检并交回独占权。进程退出时内核也会释放，这里给的是一个干净的说法，
// 让宿主在「面板关掉了」和「进程要退出了」之间不必自己区分。
//
// 重复调用无妨：宿主可能既在收尾处调一次，又在信号处理里调一次。
func (p *Panel) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		// 摘下来再放：放着的那把锁不该再由这个面板去动，而 ensureRestartLock
		// 见到 done 已经关上就不会再抢（两件事在同一把锁里，见那里的说明）。
		p.mu.Lock()
		claim := p.restartClaim
		p.restartClaim = nil
		p.mu.Unlock()
		claim.Release()
	})
}

// ensureRestartLock 保证这把独占权在自己手上：没有就再抢一次，抢到了才算数。
//
// 每轮巡检都要问一次，而不是在 New 里抢一次就完：拿到它的那个窗口可能已经退出，
// 这时这台机器上就没有人巡检了，而界面上「崩了会自动重启」那句话还写着——
// 用户等着它自己好，它却不会好。
func (p *Panel) ensureRestartLock() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.restartClaim != nil {
		return true
	}
	select {
	case <-p.done:
		// 面板已经收工，别再抢了：抢到手之后没有人会去放它。
		return false
	default:
	}
	claim, ok, err := proc.TryClaim(p.restartLock)
	if err != nil || !ok {
		return false
	}
	p.restartClaim = claim
	// 接过来的是「这台机器上已经救过它几次」这笔账，不只是那把锁。
	// 手里这份是空的（没巡检就没记过账），所以直接换上读回来的那份。
	p.restarts = loadRestarts(time.Now())
	return true
}

// OwnsRestarts 报告这台机器上的自动重启是不是归这个面板管。
// 界面据此说明「崩了会自动重启」这句话此刻算不算数。
func (p *Panel) OwnsRestarts() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restartClaim != nil
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

// SetUserNotify 装上给用户看的系统通知。传 nil 表示不发（命令行、pier api 就是这样）。
func (p *Panel) SetUserNotify(fn func(title, body string)) {
	p.mu.Lock()
	p.userNotify = fn
	p.mu.Unlock()
}

// crashKey 认出一个「这一次运行」：同一个 PID 换了新的启动时刻，就是另一次崩溃。
//
// 只看服务名不行——那样一个崩了又起、起了又崩的服务只会在第一次开口，
// 第二次开始就永远安静了。只看 PID 也不行：PID 会被系统复用。
// 两个凑一起，才既分得开先后、又不会把两件事混成一件。
func crashKey(e *proc.Entry) string {
	return fmt.Sprintf("%d@%d", e.PID, e.StartedAt.UnixNano())
}

// opKey 认出「哪一次动作」。起不来这件事没有进程可以认（进程压根没起来），
// 编的是动作的号（见 operation.id）。
func opKey(op *operation) string {
	return fmt.Sprintf("op%d", op.id)
}

// 通知的用途。同一个服务上的两件事各占一个槽位：挤在一个槽里的话，
// 后发的记号会把前一条顶掉，巡检下一次看见记号对不上，就把前一条重发一遍。
const (
	useCrash = "异常退出"
	useStart = "启动失败"
)

// say 发一条系统通知，同一件事只发一次。
//
// name 是服务名、use 是这件事叫什么（「异常退出」「启动失败」各算一件）、
// key 是这件事的标识（见 crashKey / opKey），三者一起构成去重的凭据。
//
// 去重放在这里而不是各个调用点上：发通知的地方有好几处（崩了、起不来、额度用光），
// 而它们各自都知道该拿什么当凭据，谁先发出去算谁的。
func (p *Panel) say(name, use, key, title, body string) {
	slot := name + "\x00" + use
	p.mu.Lock()
	if p.told[slot] == key {
		p.mu.Unlock()
		return
	}
	p.told[slot] = key
	fn := p.userNotify
	p.mu.Unlock()
	if fn != nil {
		fn(title, body)
	}
}

// sayStartFail 通知「这一次没拉起来」。
//
// 用户自己点的那一下不发（auto 为假）：他正看着屏幕，界面上那一行已经写着为什么，
// 再弹一条系统通知是同一句话在第二个地方又说一遍。巡检排的那一次没人在看，
// 不说就没人知道——而「自动重启了几次还是起不来」正是最该被告知的那件事。
func (p *Panel) sayStartFail(svc *config.Service, op *operation, auto bool, hit diag.Hit, hasHit bool, tail string) {
	if !auto {
		return
	}
	p.say(svc.Name, useStart, opKey(op), svc.Name+" 启动失败", notifyBody(hit, hasHit, trimDetailPath(tail)))
}

// notifyBody 拼一条通知的正文：认得出原因就把原因、下一步和原文一并说了，再说后面这句。
//
// 原文要带上：只说「依赖没装」，用户还得自己去日志里翻是哪一个依赖，
// 而「Cannot find module 'express'」里那个名字正是他下一步要动手的地方。
//
// 认不出来时只留后面那句。硬凑一句「原因不详」没有意义——通知要的是「你现在该做什么」，
// 说不出就不说（见 internal/diag）。
func notifyBody(hit diag.Hit, hasHit bool, tail string) string {
	if !hasHit {
		return tail
	}
	return fmt.Sprintf("%s：%s\n%s\n%s", hit.Reason, hit.Next, hit.Line, tail)
}

// trimDetailPath 去掉错误尾巴上那句「，详见 <日志路径>」。
//
// 系统通知点不开一条路径，而它常常比前半句还长，正文会被它占满。
// 日志在哪，界面上那行的「查看日志」和命令行的提示都写着。
//
// 摘的是 proc 拼的那句「服务 X 编译失败，详见 <路径>」（见 supervisor.go），
// 只有认识它才摘得准：认不出就原样留着，宁可长一点。
func trimDetailPath(s string) string {
	if i := strings.LastIndex(s, "，详见 "); i >= 0 {
		return s[:i]
	}
	return s
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

// ── 崩溃自愈 ─────────────────────────────────────────────────────────────
//
// 只做一件事：配了 restart: on-failure 的服务，如果状态文件里还记着它、
// 而进程已经不在了，就再拉一次。
//
// 为什么是自己轮询而不是让服务退出时通知 Pier：服务是 setsid 出去的独立进程，
// 日志 fd 由它继承，Pier 这边连一个能 Wait 的句柄都没有——进程什么时候没的，
// 除了回头去看没有别的途径。轮询因此不是偷懒，是唯一可行的做法。

func (p *Panel) watchRestarts() {
	t := time.NewTicker(restartPoll)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			// 独占权每轮重问一次：持有它的那个窗口退出之后要能接过去。
			// 抢不到的这一轮什么都不做——另一个窗口正在巡检同一批服务。
			if !p.ensureRestartLock() {
				continue
			}
			p.recoverCrashed()
		}
	}
}

func (p *Panel) recoverCrashed() {
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	if cfg == nil {
		return
	}
	// 只看状态文件与进程本身，不调 sup.Status()：那个会顺带列出全部监听端口、
	// 还会给每个配了探针的服务发一次 HTTP 请求，每几秒跑一遍太贵。
	// 这里要回答的只有「崩了没有」，读一份 JSON 就够。
	state, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		return
	}
	now := time.Now()
	for _, svc := range cfg.StartOrder() {
		if svc.Restart != config.RestartOnFailure {
			// 只报「说好要一直跑」的那几个。restart 留空的服务崩了也照崩，
			// 再起一次会盖掉编辑器里那份现场；而更要紧的是，一次性跑完就退出的
			// 服务（跑一遍构建、做一次迁移）在 Pier 眼里和崩溃长得一模一样——
			// 退出码拿不到（进程是 Release 出去的，见 supervisor.go），
			// 分不出「跑完了」和「崩了」，那就不能替用户下结论说它出事了。
			continue
		}
		e, ok := state.Services[svc.Name]
		if !ok || proc.EntryAlive(e) {
			// 没有记录 = 从来没起来过，或者被用户清理过。两种情况都不该自动重来：
			// 前者多半是启动就失败（端口冲突、依赖没装），再起一次还是同样的结果。
			continue
		}
		key := crashKey(e)
		p.mu.Lock()
		told := p.told[svc.Name+"\x00"+useCrash] == key
		p.mu.Unlock()
		if told {
			continue
		}
		// 诊断要在排重启之前读。重启一旦跑起来就会往日志里写一行新的启动标记，
		// 那之后读到的「最后一次运行」就是这一次空白的新运行了（见 proc.TrimToLastRun），
		// 而要说的是它为什么没的那一次。
		hit, hasHit := diag.FromLog(cfg, svc.Name)
		note := p.enqueueRestart(svc, now)
		if note == "" {
			// 身上已经有动作在跑了——用户正动它，或者上一次自动重启还没完。
			// 这一次不说什么，等它自己有个结果。
			continue
		}
		p.say(svc.Name, useCrash, key, svc.Name+" 异常退出", notifyBody(hit, hasHit, note))
	}
}

// enqueueRestart 记一次自动重启并把启动排进队列，返回一句「接下来会怎样」。
//
// 返回空串表示这次什么都没排（身上已经有动作了），调用方据此决定要不要说话。
// 「重启到上限」也算排上了——那句话正是不该被吞掉的那一条。
//
// 记账与占位在同一把锁里完成，不能拆成「先看看有没有额度、再调 Start」：
// 两次加锁之间隔着一个 tick 的话，一个每秒都崩的服务会被同一秒里的两次轮询各排一次。
func (p *Panel) enqueueRestart(svc *config.Service, now time.Time) string {
	p.mu.Lock()
	if op := p.ops[svc.Name]; op != nil && op.phase != "error" {
		// 身上已经有动作了——上一次自动重启还在跑，或者用户正动它。
		p.mu.Unlock()
		return ""
	}
	kept := keepRecent(p.restarts[svc.Name], now)
	if len(kept) >= restartLimit {
		// 额度用光。把记录留着，界面据此说明「它已经自己救过几次、现在不救了」；
		// 等窗口过去这些时刻会自然过期，额度重新有。
		p.restarts[svc.Name] = kept
		p.mu.Unlock()
		return fmt.Sprintf("已经自动重启 %d 次，到上限了（%d 分钟内最多 %d 次），先不再拉起",
			len(kept), int(restartWindow.Minutes()), restartLimit)
	}
	op := newOp("start", "queued")
	p.ops[svc.Name] = op
	p.restarts[svc.Name] = append(kept, now)
	p.persistRestartsLocked()
	p.mu.Unlock()

	p.jobs <- job{kind: "start", svc: svc, op: op, auto: true}
	p.fireNotify()
	return fmt.Sprintf("已自动重启第 %d 次", len(kept)+1)
}

// clearRestartsLocked 清掉一个服务的记账：用户自己动过手，额度重新算。
// 调用方必须持有 p.mu。
func (p *Panel) clearRestartsLocked(name string) {
	delete(p.restarts, name)
	p.persistRestartsLocked()
}

// persistRestartsLocked 把记账写回文件。调用方必须持有 p.mu。
//
// 不是自己在巡检的窗口写不得：没拿到独占权就没有账可记，手里这份多半是空的，
// 照写一遍等于把另一个窗口的账本抹掉（它的额度会凭空回来）。代价只是「我在这个
// 窗口里手动起过一次」传不过去，而这件事只有另一个窗口在管，本来就轮不到这里说。
//
// 写在锁里而不是放锁之后：两次改动之间放开锁，后写的那次会盖掉前一次——
// 记账是个整体，没有「只写这一条」的做法。
func (p *Panel) persistRestartsLocked() {
	if p.restartClaim == nil {
		return
	}
	saveRestarts(p.restarts)
}

// restartNote 是这个服务此刻该显示的自动重启说明，没有则返回空串。
func (p *Panel) restartNote(name string) string {
	cut := time.Now().Add(-restartWindow)
	p.mu.Lock()
	at := p.restarts[name]
	n := 0
	for _, t := range at {
		if t.After(cut) {
			n++
		}
	}
	p.mu.Unlock()
	switch {
	case n == 0:
		return ""
	case n >= restartLimit:
		return fmt.Sprintf("进程退出后已自动重启 %d 次，已达上限（%d 分钟 %d 次），暂停自动重启",
			n, int(restartWindow.Minutes()), restartLimit)
	default:
		return fmt.Sprintf("进程退出后已自动重启 %d 次", n)
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
	sup, cfg := p.sup, p.cfg
	p.mu.Unlock()
	if sup == nil {
		p.fail(svc.Name, op, errors.New("尚未加载服务清单"))
		return
	}

	// 端口已被监听说明可能已经在 IDEA 或别的终端跑着，此时再起一个必然冲突。
	if svc.Port > 0 && proc.PortOpen(svc.Port) {
		err := fmt.Errorf("端口 %d 已被占用（可能已在 IDEA 或其它终端运行）", svc.Port)
		p.fail(svc.Name, op, err)
		// 不带诊断：这一次运行一个字都没往日志里写过（还没轮到监督进程），
		// 读出来的只会是上一件事的原文，张冠李戴比不说更坏。
		p.sayStartFail(svc, op, j.auto, diag.Hit{}, false, err.Error())
		return
	}
	if err := sup.StartContext(op.ctx, svc); err != nil {
		if op.ctx.Err() == nil {
			p.fail(svc.Name, op, err)
			// 诊断要在这次失败之后读：监督进程这一趟写进日志的东西正是它起不来的原因，
			// 而它是刚写下去的（不像 recoverCrashed，那里要赶在重启覆盖之前读）。
			hit, hasHit := diag.FromLog(cfg, svc.Name)
			p.sayStartFail(svc, op, j.auto, hit, hasHit, err.Error())
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
	// 用户自己动过手，自动重启的额度重新算（见 restarts）。
	p.clearRestartsLocked(name)
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

// enqueueAll 把清单里的全部服务排队，顺序由依赖决定（见 startOrder / stopOrder）。
func (p *Panel) enqueueAll(kind string) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	return p.enqueueSet(kind, p.order(cfg, kind))
}

// order 按动作给出该走的顺序：启动顺着依赖，停止反着来（见 config.StopOrder）。
func (p *Panel) order(cfg *config.Config, kind string) []*config.Service {
	if kind == "stop" {
		return cfg.StopOrder()
	}
	return cfg.StartOrder()
}

// enqueueSet 把给定的这批服务排队，顺序就用传进来的顺序。
func (p *Panel) enqueueSet(kind string, svcs []*config.Service) (string, error) {
	p.mu.Lock()
	// 先把能排的都标记上再统一入队：标记与入队在同一把锁里完成，
	// 中间不会插进另一个请求把同一个服务排两遍。
	targets := make([]job, 0, len(svcs))
	for _, svc := range svcs {
		if op := p.ops[svc.Name]; op != nil && op.phase != "error" {
			continue
		}
		op := newOp(kind, "queued")
		p.ops[svc.Name] = op
		p.clearRestartsLocked(svc.Name)
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
	if len(targets) == 1 {
		return "", nil
	}
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
//
// 配了 depends_on 的话，前置的那些会先排进去。单点启动也要带上前置，
// 否则依赖就只有「全部启动」时才成立——而用户最常做的是点某一个，
// 那时前置没起来，服务照样起不来，只是错在别处（连不上数据库那类），
// 报错跟「你少点了一个服务」看不出关系。
//
// 前置已经在跑的会被跳过：Start 的入队逻辑对「身上有动作」的服务直接略过，
// 而一个跑着的服务身上没有动作。前置正卡在别的动作里时也略过，
// 不会因为它没就绪就把这次启动整个拒掉——顺序是约定，不是准入条件。
func (p *Panel) Start(name string) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return "", err
	}
	if len(svc.DependsOn) == 0 {
		return p.enqueue("start", name)
	}
	want := dependencyClosure(cfg, name)
	var targets []*config.Service
	var deps []string
	for _, s := range cfg.StartOrder() {
		if !want[s.Name] {
			continue
		}
		targets = append(targets, s)
		if s.Name != name {
			deps = append(deps, s.Name)
		}
	}
	msg, err := p.enqueueSet("start", targets)
	if err != nil {
		return "", err
	}
	if msg == "" {
		// 前置都在操作中，只剩自己要起。说清楚「前置这次没排」比装作没事好。
		return fmt.Sprintf("已把 %s 排入队列（前置 %s 正在操作中，本次未重排）", name, joinNames(deps)), nil
	}
	return fmt.Sprintf("已把 %s 及其前置 %s 排入队列，按顺序执行", name, joinNames(deps)), nil
}

// dependencyClosure 返回 name 以及顺着 depends_on 一路能走到的全部名字。
func dependencyClosure(cfg *config.Config, name string) map[string]bool {
	in := map[string]bool{name: true}
	queue := []string{name}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		s, err := cfg.Find(cur)
		if err != nil {
			continue
		}
		for _, d := range s.DependsOn {
			if !in[d] {
				in[d] = true
				queue = append(queue, d)
			}
		}
	}
	return in
}

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
