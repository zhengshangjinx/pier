package config

// 用户写的 run / build 是一整条命令（`pnpm run dev`、`mvn -pl api -am spring-boot:run`、
// 也可能带管道和重定向），要交给一个 shell 去跑。各平台用哪个 shell 见
// shell_unix.go / shell_windows.go；包与展示的那两个函数放在这里，
// 让「怎么包」和「怎么认回来」只写一份——proc 展示命令、给进程改名时用的也是它。

// ShellArgv 把用户写的整条命令包成真正要 exec 的 argv。
func ShellArgv(s string) []string {
	return append(append(make([]string, 0, len(shellPrefix)+1), shellPrefix...), s)
}

// ShellScript 判断 argv 是不是「交给 shell 跑一整条命令」的形状，是的话返回命令原文。
func ShellScript(argv []string) (string, bool) {
	if len(argv) != len(shellPrefix)+1 {
		return "", false
	}
	for i, p := range shellPrefix {
		if argv[i] != p {
			return "", false
		}
	}
	return argv[len(shellPrefix)], true
}

// DisplayArgv 把 argv 还原成给人看的命令行：交给 shell 的那种只展示命令体，
// 外壳（sh -c / cmd /c）是 Pier 自己加的，不属于用户写的命令。
func DisplayArgv(argv []string) string {
	if s, ok := ShellScript(argv); ok {
		return s
	}
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
