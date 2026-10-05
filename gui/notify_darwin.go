//go:build darwin

package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// 各平台一份的系统通知，这一份是 macOS 的。
//
// 走 osascript 的 display notification：与「浏览…」那几个选择框同一条路
// （见 picker_darwin.go），弹的是通知中心里那个原生横幅，不必为它引一层
// Objective-C 桥接去调 UNUserNotificationCenter。
//
// 代价是它认的是**当前这个程序**的通知权限——从终端里跑起来时，通知归
// 终端那个应用所有；双击 .app 启动时归 Pier 自己。两者都能弹，只是用户在
// 「通知」设置里看到的类别不同（终端里的那份想关也关得掉）。
func notifySend(title, body string) error {
	script := "display notification " + quoteAppleScript(body) +
		" with title " + quoteAppleScript(title)
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
