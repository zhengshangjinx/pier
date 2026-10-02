# Pier 的 Windows 安装脚本。双击同目录的 install.cmd 会跑到这里——直接双击 .ps1 多半
# 会被 ExecutionPolicy 拦下，那正是 install.cmd 存在的原因。
#
# 做三件事：
#   1. 把 pier-gui.exe 与 bin\pier.exe 复制到 %LOCALAPPDATA%\Programs\Pier
#   2. 把那个目录加进用户级 PATH（`pier` 命令就是从这儿来的）
#   3. 在开始菜单里放一个 Pier
#
# 不装进 Program Files、不动系统 PATH、不写注册表里的其它东西：不需要管理员，
# 也不弹 UAC。装完把解压出来的文件夹整个删掉不影响使用。
#
# 这个文件存成带 BOM 的 UTF-8。Windows PowerShell 5.1 对没有 BOM 的脚本按系统代码页
# （简体中文上是 GBK）解码，下面的中文会变成乱码——改这个文件时别把 BOM 弄丢。

$ErrorActionPreference = 'Stop'

$src = $PSScriptRoot
$dst = Join-Path $env:LOCALAPPDATA 'Programs\Pier'

# 少一份就先说清楚，别等 Copy-Item 报一句看不懂的路径错误。
foreach ($f in @('pier-gui.exe', 'bin\pier.exe')) {
    if (-not (Test-Path (Join-Path $src $f))) {
        throw "这个文件夹里没有 $f。请把整个压缩包解压之后再运行 install.cmd。"
    }
}

# 正在跑的 pier-gui.exe 是被锁住的，复制一定失败。与其让人对着「另一个程序正在使用此
# 文件，进程无法访问」去猜是哪个程序，不如直接点名。
$running = @(Get-Process -Name 'pier-gui', 'pier' -ErrorAction SilentlyContinue)
if ($running.Count -gt 0) {
    $names = ($running | ForEach-Object { "$($_.ProcessName)（PID $($_.Id)）" }) -join '、'
    throw "Pier 正开着：$names。退出之后再运行一次 install.cmd。"
}

Write-Host "==> 安装到 $dst"
New-Item -ItemType Directory -Force -Path $dst | Out-Null
Copy-Item (Join-Path $src 'pier-gui.exe') $dst -Force
Copy-Item (Join-Path $src 'bin\pier.exe') $dst -Force

Write-Host '==> 加入 PATH'
# 只读写用户级那份 PATH（注册表里的 HKCU\Environment），从不碰系统级那份：不需要管理员，
# 也不会影响这台机器上的其他账户。
#
# 这里绕开 [Environment]::SetEnvironmentVariable 直接读写注册表，有两个原因：它写回去的
# 一律是 REG_SZ，而 Windows 自带的用户 PATH 里本来就有 REG_EXPAND_SZ 的项
# （`%USERPROFILE%\AppData\Local\Microsoft\WindowsApps` 就是一条），一改就变成不展开的
# 字面量，Store 装的 python、winget 那些别名会跟着从 PATH 上掉下去；读的时候也要显式说
# DoNotExpandEnvironmentNames，否则 %USERPROFILE% 会被换成写死的路径。
#
# 更不能图省事用 setx：超过 1024 字符的值会被它整个截断，PATH 长一点就中招，而且是静默的。
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if ($null -eq $key) { throw '打不开注册表里的 HKCU\Environment，PATH 改不了。' }
try {
    $raw = $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
    if ($key.GetValueNames() -contains 'Path') { $kind = $key.GetValueKind('Path') }

    # 空项直接丢掉：PATH 里多一个空的 ; 不出错，但也没必要留着。
    #
    # 比较用的是去掉末尾反斜杠之后的形式：`C:\...\Pier\` 与 `C:\...\Pier` 是同一个目录，
    # 谁手动加过一条，就不该再由这里添第二条。原样的 $parts 留着写回去，不改用户写的东西。
    # -contains 比字符串时不区分大小写，所以重复运行也不会塞进第二份。
    $parts = @($raw -split ';' | Where-Object { $_ })
    if (@($parts | ForEach-Object { $_.TrimEnd('\') }) -contains $dst) {
        Write-Host "    $dst 本来就已在 PATH 里"
    } else {
        $key.SetValue('Path', (@($parts) + $dst) -join ';', $kind)
        Write-Host "    已加入 $dst"
    }
} finally {
    $key.Close()
}

Write-Host '==> 创建开始菜单项'
# 快捷方式要 WScript.Shell 这个 COM 组件，被策略锁死的机器上可能拿不到。真拿不到也只是
# 少一个菜单项——PATH 里的 pier 和装好的 pier-gui.exe 都还在，所以这里失败只警告、不中断。
try {
    $exe = Join-Path $dst 'pier-gui.exe'
    $lnk = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Pier.lnk'
    $shell = New-Object -ComObject WScript.Shell
    $sc = $shell.CreateShortcut($lnk)
    $sc.TargetPath = $exe
    $sc.WorkingDirectory = $dst
    $sc.IconLocation = "$exe,0"
    $sc.Description = 'Pier'
    $sc.Save()
    Write-Host '    开始菜单里搜 Pier 就能找到'
} catch {
    Write-Host "    这一步没成（$($_.Exception.Message)）。不影响使用，直接运行 $exe 即可。"
}

# 广播一次 WM_SETTINGCHANGE，让资源管理器（以及从它启动的终端）重新读环境变量。
# 不广播这一下，用户新开的窗口里仍然找不到 pier，得注销一次——而「装完就能用」
# 正是这个脚本存在的全部意义。拿不到这个 API 也不要紧，提示里已经写了退路。
try {
    Add-Type -Namespace PierInstall -Name Native -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Auto, SetLastError = true)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam,
    string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
    $result = [UIntPtr]::Zero
    [PierInstall.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero,
        'Environment', 2, 5000, [ref]$result) | Out-Null
} catch {
    Write-Host '    环境变量通知没发出去。新开的终端里若还找不到 pier，注销一次就好。'
}

Write-Host ''
Write-Host '装好了：'
Write-Host '  · 开始菜单里搜 Pier —— 打开图形界面'
Write-Host '  · 新开一个终端，敲 pier —— 用命令行'
Write-Host "  · 想卸载：删掉 $dst，再到「系统属性 → 环境变量」里去掉用户 PATH 中的这一项"
