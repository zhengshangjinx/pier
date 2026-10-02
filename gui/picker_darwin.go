//go:build darwin

package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// 各平台一份的「让系统弹个框选东西」，这一份是 macOS 的。
//
// 走 osascript：弹的是系统原生的选择框，支持拖拽、侧栏、最近使用、回车确认，
// 与直接调 NSOpenPanel 是同一个东西，却不必为它引一层 Objective-C 桥接
// （那要让 build-app.sh 多依赖一套 clang 的 Objective-C 编译）。
//
// 三个函数都返回已经 cleanPath 过的绝对路径，取消时 canceled 为真、err 为 nil。

// pickDir 弹出「选择文件夹」。
func pickDir(start string) (string, bool, error) {
	script := `set p to choose folder with prompt "选择项目目录"`
	if start != "" {
		script += ` default location POSIX file ` + quoteAppleScript(start)
	}
	script += `
	return POSIX path of p`
	return runAppleScript(script)
}

// pickSavePath 弹出「存储为」，返回要写入的完整路径。
// 选到一个已存在的文件时系统自己会问「要替换吗」。
func pickSavePath(defaultName string) (string, bool, error) {
	script := `set f to choose file name with prompt "导出清单" default name ` + quoteAppleScript(defaultName) + `
	return POSIX path of f`
	return runAppleScript(script)
}

// pickOpenPath 弹出「打开文件」。
func pickOpenPath(prompt string) (string, bool, error) {
	script := `set f to choose file with prompt ` + quoteAppleScript(prompt) + `
	return POSIX path of f`
	return runAppleScript(script)
}

// runAppleScript 跑一段 AppleScript，返回它写出的那串路径。
func runAppleScript(script string) (string, bool, error) {
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		// -128 是 AppleScript 的「用户取消」，中文系统上 stderr 还可能写成别的字样，
		// 两个都认。取消不是故障：调用方据此安静地什么都不做，
		// 报一条红错误只会让人以为工具坏了。
		if strings.Contains(msg, "-128") || strings.Contains(msg, "User canceled") {
			return "", true, nil
		}
		return "", false, errors.New(firstNonEmpty(msg, err.Error()))
	}
	return cleanPath(stdout.String()), false, nil
}

// quoteAppleScript 把字符串包成 AppleScript 的字符串字面量。
//
// AppleScript 里反斜杠和双引号都要转义。路径本身很少含这两个字符，
// 但真含了而没转义，轻则弹框报语法错，重则把路径截断成另一个目录。
func quoteAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
