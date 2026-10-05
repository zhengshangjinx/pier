package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// sentNotifications 换掉真的那条投递，把发出去的通知收下来。
//
// 真发的那一下会弹出系统横幅，用例不该去打扰坐在机器前的人。
type sentNotifications struct {
	mu  sync.Mutex
	got [][2]string
}

func (s *sentNotifications) take() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

// swapNotifier 把 app 上的投递换成收集器。
func swapNotifier(a *app) *sentNotifications {
	s := &sentNotifications{}
	a.notify = func(title, body string) error {
		s.mu.Lock()
		s.got = append(s.got, [2]string{title, body})
		s.mu.Unlock()
		return nil
	}
	return s
}

// waitNotified 等通知到达。投递在另一条协程上（见 notifyService），
// 调用方不能假设它已经发完了。
func waitNotified(t *testing.T, s *sentNotifications) [][2]string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.take(); len(got) > 0 {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("通知一直没发出来")
	return nil
}

// TestNotifyHonorsTheSwitch 钉着「关掉之后一条都不发」。
//
// 这是通知那个开关的全部意义：用户关掉它，图的是**一条都不再响**。
// 关掉之后还在响，比一开始就没有这个开关更让人恼火——他已经明确表过态了。
func TestNotifyHonorsTheSwitch(t *testing.T) {
	a := testApp(t)
	s := swapNotifier(a)

	// 默认是开的：一个刚装上的 Pier 就该在服务崩了的时候说话。
	a.notifyService("alpha 异常退出", "已自动重启第 1 次")
	if got := waitNotified(t, s); len(got) != 1 {
		t.Fatalf("默认该发，实际发了 %d 条：%v", len(got), got)
	}

	// 走用户真正走的那条路改设置，而不是直接改文件：开关确实写进了 settings.json。
	p, err := config.SettingsPath()
	if err != nil {
		t.Fatalf("取偏好路径失败：%v", err)
	}
	if err := config.UpdateSettings(p, func(st *config.Settings) { st.Notify = false }); err != nil {
		t.Fatalf("写偏好失败：%v", err)
	}
	a.notifyService("alpha 异常退出", "已自动重启第 1 次")
	// 等一小会儿再确认：这一条是「不发」，不能等出超时才判定。
	time.Sleep(100 * time.Millisecond)
	if got := s.take(); len(got) != 0 {
		t.Errorf("关掉之后还在发：%v", got)
	}
}

// TestNotifyServiceReturnsImmediately 钉着「发通知不挡着调用方」。
//
// 调用它的是巡检那条协程（每三秒跑一遍）。起一个 osascript / powershell 慢起来
// 是几百毫秒，压在巡检上就等于让整个人「服务崩了没人管」的那条路跟着一起等。
func TestNotifyServiceReturnsImmediately(t *testing.T) {
	a := testApp(t)
	release := make(chan struct{})
	a.notify = func(string, string) error {
		<-release
		return nil
	}

	done := make(chan struct{})
	go func() {
		a.notifyService("alpha 异常退出", "已自动重启第 1 次")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("notifyService 一直没返回，它在等那条通知发完")
	}
	close(release)
}

// TestNotifySurvivesAWriteFailure 钉着「通知发不出去不算故障」。
//
// macOS 上用户关了通知权限、Linux 上没装 notify-send——都是用户自己的选择，
// 不是 Pier 坏了。投递返回的错误一律丢掉，为它弹一个错误框只会让人以为工具出了问题。
func TestNotifySurvivesAWriteFailure(t *testing.T) {
	a := testApp(t)
	failed := make(chan struct{}, 1)
	a.notify = func(string, string) error {
		failed <- struct{}{}
		return filepath.ErrBadPattern
	}
	a.notifyService("alpha 异常退出", "已自动重启第 1 次")
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("投递根本没被调用")
	}
	// 没有 panic、没有把宿主带崩，就够了。
}
