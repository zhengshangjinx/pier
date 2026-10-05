package cli

import (
	"fmt"
	"strconv"

	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/view"
)

// cmdPorts 列出本机此刻正在监听的端口，以及每个端口背后是谁、站在哪个目录、
// 是谁把它拉起来的。
//
// 与界面那一屏用的是同一次扫描（manage.ScanPorts），不是各扫各的：
// 同一件事在两个入口给出不同的答案最难查，因为两边看着都对。
//
// 只读、不落盘。纳管要经过表单确认，那是界面的事——照目录推出来的启动方式
// 只对「一个目录一个服务」的项目成立，直接写进清单会留下一条要人去删的错记录。
func cmdPorts(args []string) int {
	cfgPath, rest := extractConfig(args)
	jsonOut, rest := extractJSON(rest)
	if err := noExtra("ports", rest); err != nil {
		return fail("%v", err)
	}
	cfg, _, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}

	m := manage.New(nil)
	m.SetConfig(cfg)
	out, err := m.ScanPorts()
	if err != nil {
		return fail("%v", err)
	}
	if jsonOut {
		// 扫不动也照样输出：PortScanOut 上本来就有 ok 与 msg 两格，
		// 脚本读那两格比读一屏中文再猜要准。退出码仍然非 0——没扫成就是没扫成。
		printJSON(out)
		if !out.OK {
			return 1
		}
		return 0
	}
	// 扫不动和「扫出来是空的」必须分开说：lsof 不在的时候空表看着就像
	// 「本机什么也没在跑」，那是个和事实相反、又看不出哪里不对的结论。
	if !out.OK {
		return fail("%s", out.Msg)
	}

	fmt.Printf("%s\n\n", cfg.Path)
	rows := make([][]string, 0, len(out.Ports))
	for _, p := range out.Ports {
		rows = append(rows, []string{
			strconv.Itoa(p.Port),
			p.Command,
			pidText(p.PID),
			dashIfEmpty(p.DirShort),
			view.OriginText(p.Origin),
			portNote(p),
		})
	}
	renderTable([]string{"端口", "进程", "PID", "项目目录", "启动来源", "说明"}, rows)
	return 0
}

// portNote 说清这一行和 Pier 的关系，没有关系时留空。
//
// 两者不是一回事，措辞上必须分开：Managed 说的是「此刻这个进程就是 Pier 起的」，
// Known 说的是「清单里有一条服务的目录正好是这个」。前者该去点「停止」，
// 后者多半是「清单里有它，但它现在跑在别处」，两句话给的动作完全不同。
func portNote(p manage.ScannedPort) string {
	switch {
	case p.Managed:
		return "Pier 服务：" + p.Service
	case p.Known != "":
		return "目录属于清单里的 " + p.Known
	default:
		return ""
	}
}

// pidText 展示进程号。系统进程监听在 lsof 里可能没有归属进程，那不是 PID 0。
func pidText(pid int) string {
	if pid <= 0 {
		return view.Dash
	}
	return strconv.Itoa(pid)
}

// dashIfEmpty 空格子填 Dash：端口那一屏里「目录查不到」是个有意义的结论
// （多半是别人的进程），空着看起来像这一列没做。
func dashIfEmpty(s string) string {
	if s == "" {
		return view.Dash
	}
	return s
}
