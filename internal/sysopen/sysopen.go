// Package sysopen 把一个路径交给系统默认程序打开。
//
// 单独成一个包，是因为它同时被两个宿主用到：网页界面（gui）和原生界面的内核
// （kernel）。系统集成的代码各写一份，迟早会有一边漏掉某个平台差异，
// 而症状是「点了没反应」——最难排查的那一类。
//
// 各平台用哪个命令、参数怎么写，见 open_<平台>.go。
package sysopen

import (
	"fmt"
	"os/exec"

	"github.com/zhengshangjinx/pier/internal/execpath"
)

// Run 用系统默认程序打开 target。reveal 为真时改为「在文件管理器里显示它」。
//
// 失败只回报，不重试：打不开通常是因为路径已经不在，重试还是同一个结果。
func Run(target string, reveal bool) error {
	bin, args, err := command(target, reveal)
	if err != nil {
		return err
	}
	if err := exec.Command(bin, args...).Run(); err != nil {
		return fmt.Errorf("打开 %s 失败：%w", target, err)
	}
	return nil
}

// findBin 在一串绝对路径里挑第一个存在的；都不在时回到 PATH。
//
// 按绝对路径找，不依赖 PATH：从访达、资源管理器启动时继承的是系统给的最小环境，
// PATH 里有什么全看系统版本和用户的 shell 配置。
func findBin(cands []string, name string) (string, error) {
	for _, c := range cands {
		if execpath.Is(c) {
			return c, nil
		}
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("找不到系统命令 %s", name)
	}
	return found, nil
}
