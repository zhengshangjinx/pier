package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/toolchain"
)

// compactEnv 精简环境变量的展示。PATH 只显示新增的那一段——
// 完整 PATH 有几十项，铺满屏幕反而看不出重点。
func compactEnv(env []string) string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, "PATH="); ok {
			first, _, _ := strings.Cut(rest, string(os.PathListSeparator))
			out = append(out, "PATH 前置 "+first)
			continue
		}
		out = append(out, kv)
	}
	return strings.Join(out, "　")
}

// cmdDoctor 逐个解析工具链并说明来源。这是排查「为什么命令行起不来、IDEA 却可以」的入口：
// 只要这里能解析出来，Pier 就能像 IDEA 一样带着正确的 JDK / PATH 启动服务。
func cmdDoctor(args []string) int {
	cfgPath, _ := extractConfig(args)

	// 配置存在就套用其中的工具链覆盖，让 doctor 反映实际启动时用的那一套；
	// 配置文件缺失不算错误，doctor 本身要能在任何目录下用来排查问题。
	r := toolchain.New()
	cfg, cfgErr := loadConfig(cfgPath)
	if cfgErr == nil {
		for k, v := range cfg.Toolchain {
			if strings.TrimSpace(v) != "" {
				r.Overrides[toolchain.Kind(k)] = v
			}
		}
		fmt.Printf("配置：%s\n\n", cfg.Path)
	}

	fmt.Println("工具链解析（不读取交互式 shell profile）：")
	fmt.Println()

	missing := make([]string, 0, len(toolchain.AllKinds))
	for _, k := range toolchain.AllKinds {
		t, err := r.Resolve(k)
		if err != nil {
			missing = append(missing, string(k))
			fmt.Printf("  %-7s ✗  %v\n", k, err)
			continue
		}
		fmt.Printf("  %-7s ✓  %s\n", k, t.Bin)
		detail := t.Label() + "，来源：" + t.Source + "，依据：" + t.Reason
		if len(t.Env) > 0 {
			detail += "　注入：" + compactEnv(t.Env)
		}
		fmt.Printf("  %-7s    %s\n", "", detail)
	}

	// 同一个 java、node 在不同服务下会解析到不同版本（服务上指定、项目声明各不相同）。
	// 版本选错不会当场报错，而是编译到一半才失败，所以按服务把实际要用的列出来，
	// 走的是和真正启动时同一个解析器，诊断结果才和实际一致。
	if cfgErr == nil && len(cfg.Services) > 0 {
		fmt.Println()
		fmt.Println("按服务解析的工具链：")
		sup := proc.New(cfg)
		for _, svc := range cfg.Services {
			tools, _, err := sup.Tools(svc)
			for _, t := range tools {
				if t.Kind == toolchain.Pnpm {
					continue
				}
				fmt.Printf("  %-14s %-6s %s（%s，依据：%s）\n", svc.Name, t.Kind, t.Label(), t.Bin, t.Reason)
				if t.Warn != "" {
					fmt.Printf("  %-14s %-6s ⚠ %s\n", "", "", t.Warn)
				}
			}
			if err != nil {
				fmt.Printf("  %-14s ✗ %v\n", svc.Name, err)
			}
		}
	}

	fmt.Println()
	if len(missing) == 0 {
		fmt.Println("全部就绪。")
		return 0
	}
	// 缺工具不算致命错误：只用到 Go + Node 的服务照样能起来，所以只提示不失败。
	fmt.Printf("未解析到：%s（仅影响依赖它们的服务）\n", strings.Join(missing, "、"))
	return 0
}
