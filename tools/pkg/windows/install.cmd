@echo off
rem Pier 在 Windows 上的安装入口：双击这个文件。
rem
rem 干活的是同目录的 install.ps1，这里只负责把它叫起来。之所以不直接双击 .ps1：
rem PowerShell 默认的 ExecutionPolicy 会拦下脚本，连「以管理员身份运行」也绕不过去
rem （管理员改的是机器策略那一档，用户策略照样拦）。-ExecutionPolicy Bypass 只对这一次
rem 调用生效，不改系统上的任何设置。
rem
rem 这个文件有两件事不能做：一是不能存成带 BOM 的 UTF-8，cmd.exe 不认 BOM，会把那三个
rem 字节当成一条命令去执行；二是不要 echo 中文，cmd 输出走的是控制台代码页，在英文系统
rem 上会变成一串问号。上面这些 rem 里的中文倒是没关系——cmd 会把整行吞掉，不会去解析里
rem 面的字符，而 UTF-8 的多字节序列本身也撞不出重定向、管道那些记号。给人看的提示都在
rem install.ps1 里，那个文件带 BOM，中文正常。
rem
rem 也不要在这个文件里加 chcp 65001：它会改变 cmd 接着往下读这个文件的解码方式，
rem 文件里已经有中文时容易读串行。
rem
rem 注释里还不能出现「百分号紧跟波浪号」那种写法（批处理里取脚本所在目录用的就是它）：
rem cmd 对 rem 行照做变量展开，展开不了就当成命令去执行，报的是致命错误。
rem 单个百分号跟着名字倒是没事，普通的重定向符和管道符也没事，rem 会把整行吞掉。
rem
rem PowerShell 的路径写死到系统目录（SystemRoot）而不走 PATH：与界面里那三个选择框
rem 同一个道理，本机装过 pwsh 7 之类的东西把 PATH 改坏时，这里仍然要能找到系统自带的
rem 那一份。5.1 一定在，pwsh 7 不一定认 -ExecutionPolicy，所以只认它。

setlocal

set "PS=%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe"
if not exist "%PS%" set "PS=powershell"

"%PS%" -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"

echo.
pause
