// Package sysopen 把一个路径交给系统默认程序打开。
//
// 单独成一个包，是因为它同时被两个宿主用到：网页界面（gui）和原生界面的内核
// （kernel）。系统集成的代码各写一份，迟早会有一边漏掉某个平台差异，
// 而症状是「点了没反应」——最难排查的那一类。
package sysopen

import (
	"fmt"
	"os/exec"
	"runtime"
)

// 按绝对路径找 open，不依赖 PATH：从访达启动时继承的是 launchd 给的最小环境，
// PATH 里有什么全看系统版本和用户的 shell 配置。理由与 internal/proc/sysbin.go 同。
var openBins = map[string][]string{
	"darwin": {"/usr/bin/open"},
	"linux":  {"/usr/bin/xdg-open", "/bin/xdg-open"},
}

// Run 用系统默认程序打开 target。reveal 为真时改为「在文件管理器里显示它」。
//
// 失败只回报，不重试：打不开通常是因为路径已经不在，重试还是同一个结果。
func Run(target string, reveal bool) error {
	cands, ok := openBins[runtime.GOOS]
	if !ok {
		return fmt.Errorf("当前系统（%s）不支持用默认程序打开路径", runtime.GOOS)
	}

	var bin string
	for _, c := range cands {
		if isExecutable(c) {
			bin = c
			break
		}
	}
	if bin == "" {
		// 回落是留给非常规安装的；正常路径上走不到这里。
		found, err := exec.LookPath(cands[0])
		if err != nil {
			return fmt.Errorf("找不到系统命令 %s", cands[0])
		}
		bin = found
	}

	// -R 是 macOS 的「在访达中显示」，只有 open 认它；其它平台忽略这个参数。
	args := []string{}
	if reveal && runtime.GOOS == "darwin" {
		args = append(args, "-R")
	}
	args = append(args, target)
	if err := exec.Command(bin, args...).Run(); err != nil {
		return fmt.Errorf("打开 %s 失败：%w", target, err)
	}
	return nil
}
