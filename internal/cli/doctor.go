package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
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

// DoctorOut 是 `pier doctor --json` 的形状。
//
// 刻意没有 ok：doctor 没有「失败」这一说（缺一套工具只影响依赖它的服务，
// 别的照样能起，退出码也一直是 0）。要判就判 missing——
// 给它一个恒为真的 ok，读的人迟早会以为它能表达什么。
type DoctorOut struct {
	// Config 是这次读到的清单，没读成时为空，原因在 ConfigError 里。
	Config      string `json:"config"`
	ConfigError string `json:"configError"`
	// Kinds 是每一类 SDK 的解析结果，没解析出来的那一条只有 error 有值。
	Kinds []proc.ToolInfo `json:"kinds"`
	// Missing 是没解析出来的类别名，按 toolchain.AllKinds 的顺序。
	Missing []string `json:"missing"`
	// Services 是按服务解析的结果，走的是和真正启动同一个解析器。
	Services []DoctorServiceOut `json:"services"`
}

// DoctorServiceOut 是一个服务这次会用到哪几套 SDK。
type DoctorServiceOut struct {
	Name  string          `json:"name"`
	Tools []proc.ToolInfo `json:"tools"`
	// Error 是连服务那一档都没解析出来时的原因（多半是目录或项目文件不对）。
	Error string `json:"error"`
}

// kindResult 是一次分类解析的结果：一份给 JSON，一份给终端。
//
// 只解析一次、两处呈现，是为了让「缺了哪几类」在屏幕上和 JSON 里不可能对不上。
// Env 不在 proc.ToolInfo 里（那是给界面看的形状，界面不展示注入的变量），
// 所以它单拎出来放在这儿，而不是为了这一处往对外合同上加一个字段。
type kindResult struct {
	info proc.ToolInfo
	env  []string
}

// cmdDoctor 逐个解析工具链并说明来源。这是排查「为什么命令行起不来、IDEA 却可以」的入口：
// 只要这里能解析出来，Pier 就能像 IDEA 一样带着正确的 JDK / PATH 启动服务。
func cmdDoctor(args []string) int {
	cfgPath, rest := extractConfig(args)
	jsonOut, rest := extractJSON(rest)
	if err := noExtra("doctor", rest); err != nil {
		return fail("%v", err)
	}

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
	}

	kinds := make([]kindResult, 0, len(toolchain.AllKinds))
	missing := make([]string, 0, len(toolchain.AllKinds))
	for _, k := range toolchain.AllKinds {
		t, err := r.Resolve(k)
		if err != nil {
			missing = append(missing, string(k))
			kinds = append(kinds, kindResult{info: proc.ToolInfo{Kind: string(k), Error: err.Error()}})
			continue
		}
		kinds = append(kinds, kindResult{
			info: proc.ToolInfo{
				Kind: string(k), Label: t.Label(), Version: t.Version, Home: t.Home,
				Bin: t.Bin, Source: t.Source, Reason: t.Reason, Warn: t.Warn,
			},
			env: t.Env,
		})
	}

	// 同一个 java、node 在不同服务下会解析到不同版本（服务上指定、项目声明各不相同）。
	// 版本选错不会当场报错，而是编译到一半才失败，所以按服务把实际要用的列出来，
	// 走的是和真正启动时同一个解析器，诊断结果才和实际一致。
	services := make([]DoctorServiceOut, 0)
	if cfgErr == nil && len(cfg.Services) > 0 {
		sup := proc.New(cfg)
		for _, svc := range cfg.Services {
			tools, _, err := sup.Tools(svc)
			item := DoctorServiceOut{Name: svc.Name, Tools: proc.ToolInfos(tools, nil)}
			if err != nil {
				item.Error = err.Error()
			}
			services = append(services, item)
		}
	}

	if jsonOut {
		return printJSON(DoctorOut{
			Config:      configPathOf(cfg),
			ConfigError: errText(cfgErr),
			Kinds:       kindInfos(kinds),
			Missing:     missing,
			Services:    services,
		})
	}

	if cfgErr == nil {
		fmt.Printf("配置：%s\n\n", cfg.Path)
	}

	fmt.Println("工具链解析（不读取交互式 shell profile）：")
	fmt.Println()
	for _, k := range kinds {
		if k.info.Error != "" {
			fmt.Printf("  %-7s ✗  %s\n", k.info.Kind, k.info.Error)
			continue
		}
		fmt.Printf("  %-7s ✓  %s\n", k.info.Kind, k.info.Bin)
		detail := k.info.Label + "，来源：" + k.info.Source + "，依据：" + k.info.Reason
		if len(k.env) > 0 {
			detail += "　注入：" + compactEnv(k.env)
		}
		fmt.Printf("  %-7s    %s\n", "", detail)
	}

	if len(services) > 0 {
		fmt.Println()
		fmt.Println("按服务解析的工具链：")
		for _, s := range services {
			for _, t := range s.Tools {
				fmt.Printf("  %-14s %-6s %s（%s，依据：%s）\n", s.Name, t.Kind, t.Label, t.Bin, t.Reason)
				if t.Warn != "" {
					fmt.Printf("  %-14s %-6s ⚠ %s\n", "", "", t.Warn)
				}
			}
			if s.Error != "" {
				fmt.Printf("  %-14s ✗ %s\n", s.Name, s.Error)
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

// kindInfos 把内部分类结果剥成对外的那一份。
func kindInfos(kinds []kindResult) []proc.ToolInfo {
	out := make([]proc.ToolInfo, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k.info)
	}
	return out
}

// configPathOf 取清单路径；没读成时为空串。
func configPathOf(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Path
}

// errText 把 error 变成一句能进 JSON 的话。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
