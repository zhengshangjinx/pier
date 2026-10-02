//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// 各平台一份的「让系统弹个框选东西」，这一份是 Windows 的。
//
// 走系统自带的 Windows PowerShell 5.1 与 .NET 的选择框控件：弹出来的是资源管理器
// 那一套框。不用 cgo 调 IFileDialog / SHBrowseForFolder——那要为三个对话框引一套
// COM 桥接，而这里只要一个能用的框。
//
// powershell.exe 的路径写死到 %SystemRoot%\System32\WindowsPowerShell\v1.0\，
// 不走 PATH：一是 PowerShell 7（pwsh）没有 -STA，而这几个对话框控件要求单线程套间；
// 二是从图形界面启动时 PATH 本来就不可靠。
//
// 三个函数都返回已经 cleanPath 过的绝对路径，取消时 canceled 为真、err 为 nil。

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW。
//
// powershell.exe 是控制台程序：不挡这一下，从图形界面里弹框时它会先闪一个黑窗口。
// 只挡控制台——WinForms 的对话框是另建的顶层窗口，照常显示。
const createNoWindow = 0x08000000

// pickDir 弹出「选择文件夹」。
func pickDir(start string) (string, bool, error) {
	script := `
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = '选择项目目录'
$d.ShowNewFolderButton = $true
if (` + psQuote(start) + ` -ne '') { $d.SelectedPath = ` + psQuote(start) + ` }
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) } else { exit 3 }
`
	return runPowerShell(script)
}

// pickSavePath 弹出「存储为」，返回要写入的完整路径。选到已有文件时系统会问要不要替换。
func pickSavePath(defaultName string) (string, bool, error) {
	script := `
$f = New-Object System.Windows.Forms.SaveFileDialog
$f.Title = '导出清单'
$f.FileName = ` + psQuote(defaultName) + `
$f.Filter = '清单 (*.yaml;*.yml;*.json)|*.yaml;*.yml;*.json|全部文件 (*.*)|*.*'
$f.OverwritePrompt = $true
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($f.FileName) } else { exit 3 }
`
	return runPowerShell(script)
}

// pickOpenPath 弹出「打开文件」。
func pickOpenPath(prompt string) (string, bool, error) {
	script := `
$f = New-Object System.Windows.Forms.OpenFileDialog
$f.Title = ` + psQuote(prompt) + `
$f.Filter = '清单 (*.yaml;*.yml;*.json)|*.yaml;*.yml;*.json|全部文件 (*.*)|*.*'
$f.CheckFileExists = $true
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($f.FileName) } else { exit 3 }
`
	return runPowerShell(script)
}

// runPowerShell 跑一段脚本，取它写出的那行路径。
func runPowerShell(body string) (string, bool, error) {
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(ps); err != nil {
		return "", false, errors.New("找不到 PowerShell：" + ps)
	}

	// -NoProfile：别人的 profile 可能改编码、改输出，弹框不该受它影响。
	// 脚本头两行是必需的：不先把输出编码定成 UTF-8，中文路径经重定向出来会按
	// 系统 OEM 代码页编码，Go 这边读到的就是乱码；Add-Type 之后才能 New-Object
	// System.Windows.Forms 里那些控件。
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n" +
		"Add-Type -AssemblyName System.Windows.Forms\n" + body

	cmd := exec.Command(ps, "-NoProfile", "-STA", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		// 脚本里取消的分支统一 exit 3，与真正的失败（找不到控件、脚本报错）分开。
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return "", true, nil
		}
		msg := strings.TrimSpace(stderr.String())
		return "", false, errors.New(firstNonEmpty(msg, err.Error()))
	}
	return cleanPath(stdout.String()), false, nil
}

// psQuote 把字符串包成 PowerShell 的单引号字面量。
//
// 单引号字符串里唯一的转义就是把自己写两遍。路径里的反斜杠不算转义字符
// （双引号字符串里才算），所以这里不需要动它。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
