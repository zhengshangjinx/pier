package panel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// 「动哪一批服务」这一件事。
//
// 界面上是按分组看服务的，命令行上是一个个念名字，本地接口与 MCP 两种都要有。
// 于是同一件事——「启动 / 停止这一批」——需要一份共同的说法，就是下面这个
// Selection：三个入口把它翻译成同一句话，再交给同一段代码去办。

// ErrBadSelection 说明这个选择本身写得不对（两个字段都给了、名字里有个空的）。
//
// 与 ErrUnknownSelection 分开，是因为接口那一层按它们回不同的状态码：
// 这个对应 400（请求本身有问题），那个对应 404（指的东西不存在）。
// 混成一句「选择不对」，脚本就只能读 msg 猜。
var ErrBadSelection = errors.New("服务的选法不对")

// ErrUnknownSelection 说明选择指向的东西在清单里没有（服务名写错、分组不存在）。
var ErrUnknownSelection = errors.New("清单里没有这个服务或分组")

// Selection 说这一次动的是哪一批服务。
//
// 两个字段只给一个：都给了（既点名一批、又说一个分组）说不清该听谁的；
// 两个都不给就是「全部」——那正是界面上「全部启动」与命令行 up / down 的那个
// 「全部」，判定只有一份（见 pick）。
//
// 分组写成一个名字而不是先展开成服务名再传进来：传的是「前端这一组」这句话，
// 展开发生在动手的那一刻——中间隔着的这段时间里清单改了，动的也还是当时那一组。
type Selection struct {
	Names []string `json:"names,omitempty"`
	Group string   `json:"group,omitempty"`
}

// Validate 只说这个选择的写法对不对，不查清单里有没有——那要有清单才判得了。
func (sel Selection) Validate() error {
	if strings.TrimSpace(sel.Group) != "" && len(sel.Names) > 0 {
		return fmt.Errorf("%w：names 与 group 只能给一个", ErrBadSelection)
	}
	for _, n := range sel.Names {
		if strings.TrimSpace(n) == "" {
			return fmt.Errorf("%w：names 里有一个空名字", ErrBadSelection)
		}
	}
	return nil
}

// all 表示这个选择就是「全部」。
//
// 单独判一次而不是看 pick 返回了几个：一个只写着 manual 服务的清单里，
// 「全部」挑出来的是空的，而那与「什么都没选」是两回事。
func (sel Selection) all() bool {
	return strings.TrimSpace(sel.Group) == "" && len(sel.Names) == 0
}

// pick 把选择解析成一批服务，按这次动作该走的顺序排好。
//
// 顺序取自清单的 StartOrder / StopOrder，而不是传进来的名字顺序：启动要先起被依赖的、
// 停止要反着来，脚本里先写了谁不该决定这件事（与「全部」走的也是同一份顺序）。
//
// 点名的那几个照做，标了 manual 也在内——manual 说的是「别在全部里带上我」，
// 不是「谁都不许动我」（见 config.Bulk）。分组也一样：一个分组里标了 manual 的
// 服务，按着这一组启动时就该起来，只按「全部」那一颗时跳过它。
func (p *Panel) pick(cfg *config.Config, sel Selection, kind string) ([]*config.Service, error) {
	if err := sel.Validate(); err != nil {
		return nil, err
	}
	order := p.order(cfg, kind)

	if sel.all() {
		return config.Bulk(order), nil
	}

	want := make(map[string]bool, len(sel.Names))
	if g := strings.TrimSpace(sel.Group); g != "" {
		svcs := cfg.InGroup(g)
		if len(svcs) == 0 {
			// 空分组与不存在的分组要分开说：前者是建过但还没往里放东西（界面上
			// 那一页是空的，看着像坏了），后者是名字写错了，而两句都只说「没有」，
			// 用户就得回去数一遍侧栏。
			if !slices.Contains(cfg.AllGroups(), g) {
				return nil, fmt.Errorf("%w：没有「%s」这个分组（现有：%s）",
					ErrUnknownSelection, g, joinNames(cfg.AllGroups()))
			}
			return nil, fmt.Errorf("%w：「%s」这个分组里还没有服务", ErrUnknownSelection, g)
		}
		for _, s := range svcs {
			want[s.Name] = true
		}
	} else {
		for _, n := range sel.Names {
			svc, err := cfg.Find(strings.TrimSpace(n))
			if err != nil {
				return nil, fmt.Errorf("%w：%w", ErrUnknownSelection, err)
			}
			want[svc.Name] = true
		}
	}

	out := make([]*config.Service, 0, len(want))
	for _, s := range order {
		if want[s.Name] {
			out = append(out, s)
		}
	}
	return out, nil
}

// StartSet 排队启动选择出来的一批服务。
//
// 点名或按分组挑出来的这一批，连同它们的前置一起起（与 Start 一个规矩）：
// 只起点名的那些，被依赖的没在跑，起来也是残的——而残在哪儿，用户从
// 「连不上数据库」这类报错里看不出它与「你少起了一个服务」有关系。
//
// 「全部」不走补前置这一步：那时候整份清单本来就都在名单里，再补一次会把
// 标了 manual 的前置也拉起来，而那正是那颗开关要挡住的事。
func (p *Panel) StartSet(sel Selection) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	svcs, err := p.pick(cfg, sel, "start")
	if err != nil {
		return "", err
	}
	if sel.all() {
		return p.enqueueSet("start", svcs)
	}

	named := make(map[string]bool, len(svcs))
	for _, s := range svcs {
		named[s.Name] = true
	}
	want := make(map[string]bool, len(svcs))
	for _, s := range svcs {
		for name := range dependencyClosure(cfg, s.Name) {
			want[name] = true
		}
	}
	all := make([]*config.Service, 0, len(want))
	var extra []string
	for _, s := range cfg.StartOrder() {
		if !want[s.Name] {
			continue
		}
		all = append(all, s)
		if !named[s.Name] {
			extra = append(extra, s.Name)
		}
	}

	msg, err := p.enqueueSet("start", all)
	if err != nil {
		return "", err
	}
	if msg == "" {
		return "", nil
	}
	// 名单比点名的多，要说一句多出来的那一截是从哪儿来的：屏幕上写着「已把 5 个
	// 服务排入队列」，而用户只点了两个名字，不说清楚就成了一件自己发生的事。
	if len(extra) > 0 {
		return msg + "（含前置 " + joinNames(extra) + "）", nil
	}
	return msg, nil
}

// StopSet 停止选择出来的一批服务，正在启动的也一并打断（见 Stop）。
//
// 走的是逐个 Stop，不是把一批服务整队排进去：排队停与「打断正在跑的启动」
// 是两回事，后者的全部意义就在不再等它——一次 Maven 编译几分钟，起错了还要
// 干等它跑完，正是「启动中停不下来」最让人抓狂的地方。
func (p *Panel) StopSet(sel Selection) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	svcs, err := p.pick(cfg, sel, "stop")
	if err != nil {
		return "", err
	}
	n := 0
	for _, svc := range svcs {
		if _, err := p.Stop(svc.Name); err == nil {
			n++
		}
	}
	return fmt.Sprintf("已对 %d 个服务发出停止，正在启动的已打断", n), nil
}

// RestartSet 重启选择出来的一批服务。
//
// 一个服务一次「先停后起」是原子的（见 Restart 那条 job），所以这里不能像
// 命令行那样整批停完再整批起——那要两个动作排两遍队，中间还得防着自己刚排进去的
// 停止动作把随后的启动挡在门外。逐个排，每个自己停自己起。
//
// 顺序取的是停止顺序：先把依赖别人的停掉。这样轮到它起来时，被它依赖的那些
// 要么没动过、要么已经起回来了——反着来的话，每停一个都在把还在跑的那个
// 服务指向一个空端口。
func (p *Panel) RestartSet(sel Selection) (string, error) {
	cfg := p.Config()
	if cfg == nil {
		return "", manage.ErrNoConfig
	}
	svcs, err := p.pick(cfg, sel, "stop")
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(svcs))
	for _, svc := range svcs {
		// 单个服务自己正卡在别的动作里（排队中、编译中）：Restart 会拒掉它。
		// 整批不该因为这个整个失败，跳过它、把名字报出来——用户要的是「其余几个
		// 重启好了」，而不是一句「有一个不行」于是谁都没动。
		if _, err := p.Restart(svc.Name); err != nil {
			continue
		}
		names = append(names, svc.Name)
	}
	if len(names) == 0 {
		return "", errors.New("没有可重启的服务（都已在操作中）")
	}
	if len(names) == 1 {
		return "", nil
	}
	return fmt.Sprintf("已把 %d 个服务排入队列，依次重启：%s", len(names), joinNames(names)), nil
}

// waitPoll 是等「身上那个动作走完」的轮询间隔。
//
// 比健康探针那 500ms 密一些：这一步等的是编译、拉起这类已经开始的事，
// 它一结束就该接着去探，中间多睡的每一毫秒都是白等的。
const waitPoll = 250 * time.Millisecond

// WaitSet 等选择出来的一批服务通过健康探针。
//
// 就绪的判定本身在 proc.WaitFor 那一份，与命令行的 pier wait 共用：同一份清单
// 在两个入口探出两种答案，是最难解释的一类。这里只多补一件事——
//
// 接口与 MCP 上问「就绪了吗」几乎总是紧跟在一句启动之后（POST …/start 立刻返回
// 202），而那一刻服务还在排队或编译，状态里一条记录都没有。直接把名字交给
// proc.WaitFor，它会当场回一句「没有在运行，先起它」，可它明明正起着。
// 所以先等还在动作里的那些走完，再交给那一份判定。
//
// timeout 是整段共用的窗口（先等起来 + 再等探针），<=0 用 proc.HealthWait。
func (p *Panel) WaitSet(sel Selection, timeout time.Duration) ([]proc.WaitResult, error) {
	return p.WaitSetCtx(context.Background(), sel, timeout)
}

// WaitSetCtx 与 WaitSet 相同，另外认一个 ctx：等的人不想要这个答案了（请求断开、
// 界面里按了取消）就该当场散掉，而不是把整段窗口走完。
//
// 一次等满三分钟是常事，而这三分钟里那条协程一直占着——MCP 那一侧尤其要紧：
// 客户端发一条取消通知，等的这头必须立刻撒手。
func (p *Panel) WaitSetCtx(ctx context.Context, sel Selection, timeout time.Duration) ([]proc.WaitResult, error) {
	cfg := p.Config()
	if cfg == nil {
		return nil, manage.ErrNoConfig
	}
	svcs, err := p.pick(cfg, sel, "start")
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = proc.HealthWait
	}
	deadline := time.Now().Add(timeout)

	names := make([]string, 0, len(svcs))
	for _, svc := range svcs {
		p.awaitLeave(ctx, svc.Name, deadline)
		names = append(names, svc.Name)
	}
	left := time.Until(deadline)
	if left < 0 {
		left = 0
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	res, err := proc.WaitForContext(ctx, cfg, names, left, nil)
	if err != nil {
		return nil, err
	}
	// 中途被叫停的这一次不给答案：探出来的那些「没就绪」是因为不看了，
	// 不是因为它们不好——交出去就是一个没人要的错话。
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return res, nil
}

// awaitLeave 等这个服务身上那个还在进行的动作走完——成功、失败都算走完。
//
// 到窗口尽头就撒手：那时它多半还在编译，接着由 proc.WaitFor 去探，探到的是一句
// 「没有在运行」。这句话此刻是对的（它就是还没起来），而原因在状态里写着
// （见 ServiceOut.OpErr），不必在这里猜。
func (p *Panel) awaitLeave(ctx context.Context, name string, deadline time.Time) {
	for p.acting(name) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitPoll):
		}
	}
}

// acting 报告这个服务此刻是不是正被一个动作动着（排队、编译、拉起、等待就绪）。
//
// 与 HasOp 的差别在失败态：那一条留在 ops 里是为了把原因摆在界面上（见 fail），
// 不是一个还在进行的动作。等就绪时把失败态当成「还在进行」，会一路等到窗口尽头。
func (p *Panel) acting(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	op := p.ops[name]
	return op != nil && op.phase != "error"
}
