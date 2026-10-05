//go:build !darwin && !windows && !linux

package main

import "errors"

// 别的系统（各 BSD、illumos…）上没有一条通用的通知通道，这一份就什么都不做。
//
// 与 Linux 那份「没装 notify-send 就安静地不做」是同一件事，只是这里连
// 候选命令都没有。让主线编得起来、跑得动，是这一份存在的全部理由。
func notifySend(title, body string) error {
	return errors.New("这个系统上还没有接系统通知")
}
