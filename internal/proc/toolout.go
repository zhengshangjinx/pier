package proc

import "github.com/zhengshangjinx/pier/internal/toolchain"

// ToolInfo 是一套解析出来的 SDK 的对外形状。
//
// 服务行上的「Java 21 · Temurin」、表单下方的「将使用 X，依据 Y」、启动日志开头那几行，
// 讲的都是同一件事——某个服务这次会用哪个运行时、为什么用它——所以共用一个形状，
// 不再各写一份。字段与 toolchain.Tool 一一对应。
type ToolInfo struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"` // 「Java 21.0.9 · Oracle」
	Version string `json:"version"`
	Home    string `json:"home"`
	Bin     string `json:"bin"`
	Source  string `json:"source"` // 从哪发现的
	Reason  string `json:"reason"` // 为什么选它
	Warn    string `json:"warn"`   // 项目要求没满足时的说明
	Error   string `json:"error"`  // 根本找不到时的原因
}

// ToolInfos 把一次解析结果转成给界面看的形状。
//
// pnpm 不单列：它只是跟着选中的 node 走的一个包管理器，摆进「这套服务用哪些 SDK」里
// 只会让人以为它是独立的一项。
//
// 解析失败时补一条只带 Error 的记录，而不是返回空表：界面要能说清「为什么没有」，
// 空表只会显示成「这项没配」——那和「找不到」是两回事。
func ToolInfos(tools []*toolchain.Tool, err error) []ToolInfo {
	out := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		if t.Kind == toolchain.Pnpm {
			continue
		}
		out = append(out, ToolInfo{
			Kind: string(t.Kind), Label: t.Label(), Version: t.Version, Home: t.Home,
			Bin: t.Bin, Source: t.Source, Reason: t.Reason, Warn: t.Warn,
		})
	}
	if err != nil {
		out = append(out, ToolInfo{Error: err.Error()})
	}
	return out
}
