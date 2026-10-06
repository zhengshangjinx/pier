package cli

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// cmdWait 等一组服务通过健康探针。退出码就是答案：0 全都就绪，1 有没等到的。
//
// 与 up 的区别只在「谁去拉起」：up 自己起、自己等，wait 是对已经跑着的服务等
// ——界面里起的、别的终端里起的、或者 up 跑完之后过了一阵才要确认的那一段。
// 探针与等待方式都取自 proc 那一份（up 内部走的也是它），所以两个入口对
// 「就绪」的定义不会各说各话。
//
// 三种「等不到」分开报，因为下一步动作各不相同：没配探针（要往清单里加 health）、
// 没在跑（要先去起它）、探针没通（要看它卡在哪一行日志）。
func cmdWait(args []string) int {
	cfgPath, rest := extractConfig(args)
	timeout := proc.HealthWait
	names := make([]string, 0, len(rest))

	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--timeout" || strings.HasPrefix(a, "--timeout="):
			arg := strings.TrimPrefix(a, "--timeout=")
			if a == "--timeout" {
				if i+1 >= len(rest) {
					return fail("--timeout 后面要跟时长，例如：pier wait api --timeout 30s")
				}
				i++
				arg = rest[i]
			}
			d, err := parseTimeout(arg)
			if err != nil {
				return fail("%v", err)
			}
			timeout = d
		default:
			if strings.HasPrefix(a, "-") {
				return fail("wait 不认识参数 %s（看帮助：pier wait -h）", a)
			}
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		return fail("wait 需要一个服务名，例如：pier wait api")
	}

	// 只要清单，不要 supervisor：wait 不动进程，它只是看着。
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	svcs := make([]*config.Service, 0, len(names))
	for _, n := range names {
		svc, err := cfg.Find(n)
		if err != nil {
			return fail("%v", err)
		}
		svcs = append(svcs, svc)
	}

	// 「在不在跑」用的是状态里那一条判定（见 proc.EntryAlive），不是端口：
	// 端口可能是被别人的进程占着，那跟这个服务就绪与否没有关系。
	//
	// 读整份状态（而不是 RunningNames 那一份名字表），是因为还要拿它把服务换成
	// 「此刻真正在跑的那一份」：换过端口起的那次，探针地址得跟着落到实际那个端口上，
	// 否则 wait 探的是旧端口上那个陌生进程——它可能正好回 200，于是这里宣布「已就绪」，
	// 而就绪的是别人。
	state, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		state = &proc.State{Services: map[string]*proc.Entry{}}
	}

	// 两种等不到的不必等满窗口：等下去也不会有结果，而盯着一个不会再变的东西
	// 看三分钟，正是 wait 该替人省掉的事。
	code := 0
	missed := make([]string, 0, len(svcs))
	waited := make([]*config.Service, 0, len(svcs))
	for _, svc := range svcs {
		switch {
		case svc.Health == "":
			fmt.Printf("  %-14s 没配健康探针，等不到「就绪」这个信号（在清单里给它加 health）\n", svc.Name)
			missed = append(missed, svc.Name+"（没配探针）")
			code = 1
		case !proc.EntryAlive(state.Services[svc.Name]):
			fmt.Printf("  %-14s 没有在运行，先起它：pier up %s\n", svc.Name, svc.Name)
			missed = append(missed, svc.Name+"（没在跑）")
			code = 1
		default:
			waited = append(waited, proc.RunningService(svc, state))
		}
	}

	// 一起等，不排队：一个 Java 服务等满三分钟再加一个三十秒的，串起来就是三分半，
	// 而它们本来并行（up 里那一轮也是这个道理）。
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, svc := range waited {
		wg.Add(1)
		go func(svc *config.Service) {
			defer wg.Done()
			ok := proc.WaitHealthy(svc.AbsDir(), svc.Health, timeout)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				fmt.Printf("  %-14s 已就绪  %s\n", svc.Name, svc.Health)
				return
			}
			code = 1
			missed = append(missed, fmt.Sprintf("%s（%s 内没通过探针）", svc.Name, timeout))
			fmt.Printf("  %-14s 等满了 %s，探针还没通  %s\n", svc.Name, timeout, svc.Health)
			fmt.Printf("  %-14s   看它卡在哪一行：pier logs %s\n", "", svc.Name)
		}(svc)
	}
	wg.Wait()

	if code != 0 {
		fmt.Printf("\n没等到：%s\n", strings.Join(missed, "、"))
		fmt.Println("  谁在跑、探针通没通：pier status")
		return 1
	}
	fmt.Printf("\n%d 个服务已就绪。\n", len(waited))
	return 0
}

// parseTimeout 认 `30s` / `2m` 这类时长，也认光写数字的秒数——脚本里最容易
// 写出来的就是 `--timeout 30`，为它回一句「请写成 30s」不值得。
func parseTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("--timeout 要一个正数，收到的是「%s」", s)
		}
		return time.Duration(n) * time.Second, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("--timeout 要一个时长，例如 30s、2m；收到的是「%s」", s)
}
