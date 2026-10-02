//go:build linux

package toolchain

// platformRoots 是 Linux 上、主目录与 PATH 之外还要去扫的位置。
//
// 发行版把 JDK 装在 /usr/lib/jvm（Debian/Ubuntu、Fedora）、/usr/lib64/jvm（openSUSE）、
// /usr/java（Oracle 的 rpm）三处，Go 装在 /usr/lib/go* 与 /usr/local/go；
// Homebrew on Linux 在 /home/linuxbrew 下，布局与 macOS 的 /opt/homebrew 一致。
func platformRoots(home string, k Kind) []sdkRoot {
	switch k {
	case Java:
		return []sdkRoot{
			{Java, "/usr/lib/jvm/*", "系统 JDK 目录", formJavaHome},
			{Java, "/usr/lib64/jvm/*", "系统 JDK 目录", formJavaHome},
			{Java, "/usr/java/*", "Oracle 安装包", formJavaHome},
			{Java, "/opt/java/*", "系统 JDK 目录", formJavaHome},
			{Java, "/home/linuxbrew/.linuxbrew/opt/openjdk*/libexec/openjdk.jdk", "Homebrew", formJDKApp},
			{Java, "/home/linuxbrew/.linuxbrew/opt/openjdk*", "Homebrew", formJavaHome},
		}
	case Maven:
		return []sdkRoot{
			{Maven, "/usr/share/maven", "系统包管理器", formMavenHome},
			{Maven, "/usr/local/maven", "手动解压", formMavenHome},
			{Maven, "/opt/maven", "手动解压", formMavenHome},
			{Maven, "/home/linuxbrew/.linuxbrew/opt/maven/libexec", "Homebrew", formMavenHome},
		}
	case Node:
		return []sdkRoot{
			{Node, "/usr/local/bin/node", "官方安装包", formExecFile},
			{Node, "/usr/local/lib/nodejs/*/bin/node", "官方安装包", formExecFile},
		}
	case Python:
		return []sdkRoot{
			{Python, "/usr/local/bin/python3", "官方安装包", formExecFile},
			{Python, "/opt/python/*/bin/python3", "手动解压", formExecFile},
		}
	case Go:
		return []sdkRoot{
			{Go, "/usr/local/go", "官方安装包", formGoHome},
			{Go, "/usr/lib/go", "系统包管理器", formGoHome},
			{Go, "/usr/lib/go-*", "系统包管理器", formGoHome},
			{Go, "/snap/go/current", "snap", formGoHome},
			{Go, "/home/linuxbrew/.linuxbrew/opt/go*/libexec", "Homebrew", formGoHome},
		}
	}
	return nil
}
