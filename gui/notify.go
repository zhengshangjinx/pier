package main

// 系统通知：服务出事时弹一条，让不在屏幕前的人也能知道。
//
// 三平台各一份 sendNotify（见同目录的 notify_<平台>.go），这一份是三家共用的
// 门面。分成两份的理由与窗口、选择框那几处一样：平台差异只出现在带 build tag
// 的文件里，共用文件里不出现 osascript、toast 这类字眼。
//
// 这一层只做一件事：把一段文字交给系统。它不认识「崩溃」「重启到上限」这些概念——
// 该不该说、说什么，全在 internal/panel 里定（见那里的 say 与 notifyBody）。
// 换成原生界面时，这段可以直接接到 UNUserNotificationCenter 上，内核一行不用改。

// sendNotify 让系统弹一条通知。
//
// 返回的 error 只给日志与测试看：**通知发不出去不是故障**。macOS 上用户关掉了
// 通知权限、Linux 上没装 notify-send、Windows 上组策略禁了 toast——这些都是
// 用户自己的选择，不是 Pier 坏了，为它弹一个错误框只会让人以为工具出了问题。
// 所以调用方一律把错误丢掉（见 app.notifyService）。
func sendNotify(title, body string) error {
	return notifySend(title, body)
}
