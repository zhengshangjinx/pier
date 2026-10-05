//go:build linux

package main

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

// 各平台一份的系统通知，这一份是 Linux 的。
//
// 走 notify-send：桌面环境里那个「通知」的样子由它自己决定（GNOME 的横幅、
// KDE 的气泡），与用户平时收到的别的通知是同一套。不用 cgo 调 libnotify——
// 那又要引一份 GTK 之外的头文件，而 notify-send 是 libnotify 自带的命令行。
//
// 没有就安静地什么都不做：headless 的机器、没装桌面组件的容器里本来就没有
// 通知可弹，装一个包才能收到通知这种事，不值得为它打扰用户。
func notifySend(title, body string) error {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return errors.New("没找到 notify-send，这台机器上没有可用的系统通知")
	}
	cmd := exec.Command(bin, "--app-name=Pier", title, body)
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
