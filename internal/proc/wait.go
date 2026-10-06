package proc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 等一组服务通过健康探针。
//
// 这是「就绪」这件事的唯一一份判定：命令行的 pier wait、接口的
// POST /api/services/wait、MCP 的 wait_ready 都从这里过一道。三处各写一遍的话，
// 同一份清单在三个入口会探出三种答案，而它们看的是同一批进程、同一个探针。
//
// 这一层只回答「此刻通了没有」，不负责把服务拉起来（那是 panel 的事），
// 也不负责解释为什么没通（那是日志与诊断的事）。

// 没等到的三种原因。写成短字符串而不是中文句子：接口要把它们交给脚本与模型，
// 认一个稳定的键比认一句话可靠（要显示给人看的那句在 view.WaitWhy）。
const (
	// WaitNoProbe：清单里没写 health，根本没有「就绪」这个信号可等。
	WaitNoProbe = "no_probe"
	// WaitNotRunning：此刻没有在跑，也没人去起它。等下去不会有结果。
	WaitNotRunning = "not_running"
	// WaitTimeout：等满了窗口，探针还没通。
	WaitTimeout = "timeout"
)

// WaitResult 是等一个服务就绪的结果。
type WaitResult struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// Why 说明没就绪是哪一种，见上面那三个常量；就绪时为空。
	//
	// 三种分开报是因为下一步动作各不相同：没配探针要往清单里加 health、
	// 没在跑要先去起它、探针没通要看它卡在哪一行日志。合成一句「没就绪」，
	// 拿到它的人只知道继续等。
	Why string `json:"why,omitempty"`
	// Probe 是这次实际探的地址。换过端口起的那次与清单里写的不是一个，
	// 说清楚探的是什么，能省掉一轮「我明明写了 8080」。
	Probe string `json:"probe,omitempty"`
}

// ParseWaitTimeout 解析一个等待窗口，认 30s / 2m 这类时长，也认光写数字的秒数
// ——脚本里最容易写出来的就是 --timeout 30，为它回一句「请写成 30s」不值得。
//
// 与 WaitFor 同一份：命令行的 --timeout、接口的 ?timeout=、MCP 的参数都从这儿过。
// 三处各解析一遍，迟早一处认 30 是 30 秒、另一处认 30 是 30 纳秒。
func ParseWaitTimeout(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("要一个正数，收到的是「%s」", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("要一个时长，例如 30s、2m；收到的是「%s」", s)
}

// WaitFor 等一组服务通过健康探针，按传进来的名字顺序返回。
//
// 名字在清单里找不到就整个报错：调用方给的是一份要等的名单，里面混进一个
// 不存在的名字，多半是把名字写错了，而只等其余几个会让这个错更难发现。
//
// report 在每一个服务有结果时被调用一次，可以为 nil。调用来自等着的那些协程，
// 但彼此串行（同一把锁底下），所以实现里不必自己加锁。命令行靠它边等边打：
// 等满三分钟而一句话都不说，用户会以为它卡住了。
func WaitFor(cfg *config.Config, names []string, timeout time.Duration, report func(WaitResult)) ([]WaitResult, error) {
	return WaitForContext(context.Background(), cfg, names, timeout, report)
}

// WaitForContext 与 WaitFor 相同，另外认一个 ctx：接口那边请求断开时，
// 等的人也该散（http.Server 不会替我们取消一个正在跑的 handler）。
func WaitForContext(ctx context.Context, cfg *config.Config, names []string,
	timeout time.Duration, report func(WaitResult)) ([]WaitResult, error) {
	if cfg == nil {
		return nil, errors.New("尚未加载服务清单")
	}

	// 「在不在跑」用的是状态里那一条判定（见 EntryAlive），不是端口：
	// 端口可能是被别人的进程占着，那跟这个服务就绪与否没有关系。
	//
	// 读整份状态（而不是 RunningNames 那一份名字表），是因为还要拿它把服务换成
	// 「此刻真正在跑的那一份」：换过端口起的那次，探针地址得跟着落到实际那个端口上，
	// 否则探的是旧端口上那个陌生进程——它可能正好回 200，于是这里宣布「已就绪」，
	// 而就绪的是别人。
	state, err := LoadState(cfg.StatePath())
	if err != nil {
		state = &State{Services: map[string]*Entry{}}
	}

	svcs := make([]*config.Service, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		svc, err := cfg.Find(name)
		if err != nil {
			return nil, err
		}
		// 同一个名字报两遍就探两遍：两份结果一行一样，读的人只会以为自己看花了。
		if seen[svc.Name] {
			continue
		}
		seen[svc.Name] = true
		svcs = append(svcs, svc)
	}

	out := make([]WaitResult, len(svcs))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	reportOne := func(i int, r WaitResult) {
		mu.Lock()
		out[i] = r
		if report != nil {
			report(r)
		}
		mu.Unlock()
	}

	// 两种等不到的不必等满窗口：等下去也不会有结果，而盯着一个不会再变的东西
	// 看满三分钟，正是 wait 该替人省掉的事。
	var waiting []int
	for i, svc := range svcs {
		switch {
		case svc.Health == "":
			reportOne(i, WaitResult{Name: svc.Name, Why: WaitNoProbe})
		case !EntryAlive(state.Services[svc.Name]):
			reportOne(i, WaitResult{Name: svc.Name, Probe: svc.Health, Why: WaitNotRunning})
		default:
			waiting = append(waiting, i)
		}
	}

	// 一起等，不排队：一个 Java 服务等满三分钟再加一个三十秒的，串起来就是三分半，
	// 而它们本来并行（up 里那一轮、等前置那些，走的都是同一个道理）。
	for _, i := range waiting {
		svc := RunningService(svcs[i], state)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := WaitResult{Name: svc.Name, Probe: svc.Health}
			r.Ready = WaitHealthyContext(ctx, svc.AbsDir(), svc.Health, timeout)
			if !r.Ready {
				r.Why = WaitTimeout
			}
			reportOne(i, r)
		}()
	}
	wg.Wait()
	return out, nil
}
