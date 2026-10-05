package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
)

// --json 的规矩，只有两条，都写在这里：
//
//   - 输出的形状一律来自后端已有的那一份（StateOut、PortScanOut、ToolInfo…），
//     命令行不另立一套。换一套的话，同一个事实就有了两种说法，而用脚本的人
//     看不到界面，他只会以为自己手上的这份就是全部。
//   - stdout 上只有 JSON，一个字的别的都不许有。提示、进度、说明一律走 stderr，
//     这样 `pier status --json | jq` 永远能用，不必先剥掉几行前言。

// jsonVerbs 是认 --json 的那几个动词，也是「哪儿能拿到机器读的输出」唯一的一处出处：
// 全局帮助里那句、拒绝别的动词时那句、测试，都从这里读。多一处手抄的名单，
// 就多一次「帮助说认、实际不认」的机会。
var jsonVerbs = []usage{
	{"status", ""},
	{"ports", ""},
	{"doctor", ""},
	{"logs", "--size"},
}

// jsonVerbList 把上面那张表写成一句话：status / ports / doctor / logs --size。
func jsonVerbList() string {
	parts := make([]string, 0, len(jsonVerbs))
	for _, v := range jsonVerbs {
		if v.desc != "" {
			parts = append(parts, v.form+" "+v.desc)
			continue
		}
		parts = append(parts, v.form)
	}
	return strings.Join(parts, " / ")
}

// extractJSON 摘出 --json，返回它有没有出现与其余参数。
//
// 不认它的动词由 Run 里的 acceptsJSON 拦下，而不是装作没看见：
// 「参数被悄悄忽略」正是这一版要收拾的那类问题——用户以为拿到的是机器读的输出，
// 实际上拿到的是给人看的表格，脚本里就是一句莫名其妙的解析失败。
func extractJSON(args []string) (bool, []string) {
	on := false
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			on = true
			continue
		}
		out = append(out, a)
	}
	return on, out
}

// wantsJSON 报告这串参数里有没有 --json。
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// acceptsJSON 报告这个动词认不认 --json。
//
// logs 算认：它的 --json 只在 --size 下有效，那一条由它自己报（措辞比这里准）。
func acceptsJSON(cmd string) bool {
	for _, v := range jsonVerbs {
		if v.form == cmd {
			return true
		}
	}
	return false
}

// rejectJSON 把「这个动词不认 --json」说清楚，并指出哪儿认。
func rejectJSON(cmd string) int {
	return fail("%s 不支持 --json。输出 JSON 的是：%s", cmd, jsonVerbList())
}

// printJSON 把一个结构整份写到 stdout。缩进两格：它是给人看也要给脚本看的，
// 一行不换的整块在终端里读不出结构，还得先拿 jq 过一遍才知道自己拿到了什么。
func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fail("输出 JSON 失败：%v", err)
	}
	return 0
}

// loadConfigSource 与 loadConfig 是同一次加载，只是还带回清单的来源说法
// （「本机数据」/「命令行指定」）。`pier status --json` 里那一格要的是界面上的
// 同一种说法，所以走 config.Resolve——不另起一句措辞。
//
// 出错时 path 与 src 尽量带回来：清单打不开也是要如实报出去的一件事
// （status --json 会把原因写在 error 里），不能因为失败就什么都不说。
func loadConfigSource(cfgPath string) (cfg *config.Config, path, src string, err error) {
	path, src, err = config.Resolve(cfgPath)
	if err != nil {
		return nil, path, src, err
	}
	cfg, err = config.Open(path)
	if err != nil {
		return nil, path, src, err
	}
	return cfg, path, src, nil
}

// noExtra 检查位置参数是不是空的。多出来的词多半是打错了命令或者少打了参数，
// 静默丢掉的话，用户看到的是一份「看起来没问题」的回答。
func noExtra(cmd string, rest []string) error {
	if len(rest) == 0 {
		return nil
	}
	return fmt.Errorf("%s 不认识参数 %s", cmd, strings.Join(rest, "、"))
}
