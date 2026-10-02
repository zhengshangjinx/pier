//go:build windows

package config

// shellPrefix 是 Windows 上跑一整条命令的方式：cmd /c 是那边的 sh -c。
//
// 一个已知的边界：Go 按 MSVCRT 的规矩给参数加引号，而 cmd 自己认的是另一套
// （内部的双引号要写成两个、不认反斜杠转义），所以命令里带引号的复杂情况
// 会被 cmd 解析得和写的不是一个意思。Pier 自己生成的那些命令（pnpm run dev、
// mvn -pl api -am install、python main.py）都没有引号，不受影响。
var shellPrefix = []string{"cmd", "/c"}
