//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// 各平台一份的「让系统弹个框选东西」，这一份是 Linux 的。
//
// 走 zenity / kdialog / qarma：各桌面环境自带的对话框工具，弹出来的是用户
// 在文件管理器里见惯了的那套框。不用 cgo 调 GTK 的 GtkFileChooserDialog：
// 那会把界面绑死在 GTK 的头文件上，而这几个小工具在各自的会话里几乎总是装着的。
//
// 三个函数都返回已经 cleanPath 过的绝对路径，取消时 canceled 为真、err 为 nil。

// dialogKind 是弹框工具的两套参数形状：zenity 与它的分支 qarma 一套，
// KDE 的 kdialog 另一套。
type dialogKind int

const (
	dialogNone dialogKind = iota
	dialogZenity
	dialogKdialog
)

// dialogBin 找当前会话里该用哪个工具。
//
// 顺序要紧：KDE 上装了 zenity 也不该弹一个 GTK 的框，所以先看当前桌面环境是不是 KDE，
// 是就优先 kdialog（XDG_CURRENT_DESKTOP 是各家都认的那一个变量）。
func dialogBin() (string, dialogKind) {
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		if p, err := exec.LookPath("kdialog"); err == nil {
			return p, dialogKdialog
		}
	}
	for _, c := range []struct {
		name string
		kind dialogKind
	}{
		{"zenity", dialogZenity},
		{"qarma", dialogZenity}, // zenity 的轻量分支，参数一样
		{"kdialog", dialogKdialog},
	} {
		if p, err := exec.LookPath(c.name); err == nil {
			return p, c.kind
		}
	}
	return "", dialogNone
}

// pickDir 弹出「选择文件夹」。
func pickDir(start string) (string, bool, error) {
	bin, kind := dialogBin()
	switch kind {
	case dialogZenity:
		args := []string{"--file-selection", "--directory", "--title=选择项目目录"}
		if start != "" {
			// 结尾补一个斜杠，框里才会进到这个目录、而不是选中它的上一层。
			args = append(args, "--filename="+strings.TrimRight(start, "/")+"/")
		}
		return runDialog(bin, args)
	case dialogKdialog:
		args := []string{"--title", "选择项目目录", "--getexistingdirectory"}
		if start != "" {
			args = append(args, start)
		}
		return runDialog(bin, args)
	}
	return "", false, errNoDialog()
}

// pickSavePath 弹出「存储为」，返回要写入的完整路径。
func pickSavePath(defaultName string) (string, bool, error) {
	bin, kind := dialogBin()
	switch kind {
	case dialogZenity:
		return runDialog(bin, []string{
			"--file-selection", "--save", "--confirm-overwrite",
			"--title=导出清单", "--filename=" + defaultName,
		})
	case dialogKdialog:
		return runDialog(bin, []string{
			"--title", "导出清单", "--getsavefilename", defaultName, "*.yaml *.yml *.json",
		})
	}
	return "", false, errNoDialog()
}

// pickOpenPath 弹出「打开文件」。
func pickOpenPath(prompt string) (string, bool, error) {
	bin, kind := dialogBin()
	switch kind {
	case dialogZenity:
		return runDialog(bin, []string{"--file-selection", "--title=" + prompt})
	case dialogKdialog:
		return runDialog(bin, []string{
			"--title", prompt, "--getopenfilename", ".", "*.yaml *.yml *.json",
		})
	}
	return "", false, errNoDialog()
}

// runDialog 跑一个选择框，取它写出的那行路径。
func runDialog(bin string, args []string) (string, bool, error) {
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// 退出码 1 是这两个工具的「用户取消」，别的码才是真出错。
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", true, nil
		}
		msg := strings.TrimSpace(stderr.String())
		return "", false, errors.New(firstNonEmpty(msg, bin+"："+err.Error()))
	}
	return cleanPath(stdout.String()), false, nil
}

// errNoDialog 一个工具都没有时说清楚缺什么，别只留一个空框不弹。
func errNoDialog() error {
	return errors.New("没找到 zenity / kdialog / qarma，装一个才能弹出选择框")
}
