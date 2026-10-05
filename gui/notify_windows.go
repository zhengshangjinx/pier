//go:build windows

package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

// 各平台一份的系统通知，这一份是 Windows 的。
//
// 走系统自带的 Windows PowerShell 5.1 调 WinRT 的 toast：弹出来的是右下角那一条，
// 与别的应用发的通知同一个样子。不用 cgo 调 WinRT——那要为一行字引一套 COM 桥接，
// 还得先给进程注册一个 AUMID。
//
// **不生成 .ps1、整段脚本走 -EncodedCommand 传**：脚本文件的那条编码规矩
// （PS 5.1 对没有 BOM 的文件按系统代码页解码，见 install.ps1）在这里绕不开——
// 运行时生成的脚本没法保证带 BOM。而 -EncodedCommand 收的是 base64 的 UTF-16LE，
// 从命令行直接进解析器，中间不经过任何代码页，中文标题也就原样到了。
// 顺带的好处：脚本不出现在进程列表的命令行里。
func notifySend(title, body string) error {
	root := os.Getenv("SystemRoot")
	if root == "" {
		// 变量意外为空时退回那个永远存在的默认值：不兜这一下只会拼出一个
		// 不存在的路径，报出来的错与真正的原因毫无关系。
		root = `C:\Windows`
	}
	ps := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := "$ErrorActionPreference = 'Stop'\n" +
		"[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null\n" +
		"[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null\n" +
		"$tpl = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)\n" +
		"$nodes = $tpl.GetElementsByTagName('text')\n" +
		"$nodes.Item(0).AppendChild($tpl.CreateTextNode(" + psQuote(title) + ")) | Out-Null\n" +
		"$nodes.Item(1).AppendChild($tpl.CreateTextNode(" + psQuote(body) + ")) | Out-Null\n" +
		"$toast = New-Object Windows.UI.Notifications.ToastNotification $tpl\n" +
		"[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(" + psQuote(toastAppID) + ").Show($toast)\n"

	cmd := exec.Command(ps, "-NoProfile", "-EncodedCommand", encodePowerShell(script))
	// 与「浏览…」那几个框同一个理由：powershell.exe 是控制台程序，
	// 不挡这一下，弹通知时任务栏会闪一个黑窗口。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
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

// toastAppID 是发这条通知的「应用」。
//
// 没有注册过 AUMID 的进程调 CreateToastNotifier，Windows 会把通知直接丢掉——
// 不报错、不显示，只是什么都没有。而一个 AUMID 要么装进注册表、要么随一个带
// AppUserModelID 的开始菜单快捷方式走，Pier 这两样都没有（install.ps1 建的那个
// 快捷方式不带这个字段）。
//
// 这一串是 PowerShell 自己的，每一台 Windows 上都注册着，装完就有。
// 用它的代价只有一个：通知的落款写着「Windows PowerShell」而不是 Pier——
// 而通知的标题里本来就带着是哪个服务出的事。换上自己的 AUMID 要动安装脚本
// 的快捷方式与注册表，那是另一件事。
const toastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// encodePowerShell 把脚本编成 -EncodedCommand 要的那串 base64（UTF-16LE）。
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
