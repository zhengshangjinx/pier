package cli

import (
	"fmt"
	"strings"

	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// cmdWait 等一组服务通过健康探针。退出码就是答案：0 全都就绪，1 有没等到的。
//
// 与 up 的区别只在「谁去拉起」：up 自己起、自己等，wait 是对已经跑着的服务等
// ——界面里起的、别的终端里起的、或者 up 跑完之后过了一阵才要确认的那一段。
//
// 判定与等待方式都取自 proc.WaitFor：本地接口与 MCP 的 wait_ready 走的是同一个
// 函数，所以三个入口对「就绪」的定义不会各说各话。
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
			d, err := proc.ParseWaitTimeout(arg)
			if err != nil {
				return fail("--timeout %v", err)
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

	// 边等边打：当场就能回答的那两条（没配探针、没在跑）不必等，而用户盯着一个
	// 什么都不动的终端等三分钟，正是 wait 该替他省掉的事。
	okCount := 0
	results, err := proc.WaitFor(cfg, names, timeout, func(r proc.WaitResult) {
		fmt.Printf("  %-14s %s\n", r.Name, view.WaitText(r, timeout))
	})
	if err != nil {
		return fail("%v", err)
	}
	for _, r := range results {
		if r.Ready {
			okCount++
		}
	}

	if missed := view.WaitMissed(results, timeout); missed != "" {
		fmt.Printf("\n没等到：%s\n", missed)
		fmt.Println("  谁在跑、探针通没通：pier status")
		return 1
	}
	fmt.Printf("\n%d 个服务已就绪。\n", okCount)
	return 0
}
