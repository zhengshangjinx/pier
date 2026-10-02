//go:build darwin

package toolchain

import "path/filepath"

// platformRoots 是 macOS 上、主目录与 PATH 之外还要去扫的位置。
func platformRoots(home string, k Kind) []sdkRoot {
	switch k {
	case Java:
		return []sdkRoot{
			{Java, "/Library/Java/JavaVirtualMachines/*", "系统 JDK 目录", formJDKApp},
			{Java, "/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk", "Homebrew", formJDKApp},
			{Java, "/usr/local/opt/openjdk*/libexec/openjdk.jdk", "Homebrew", formJDKApp},
			// IDEA 自带的 JBR：不装 JDK 的机器上，这是唯一一个能用的 Java。
			{Java, filepath.Join("/Applications", "IntelliJ IDEA.app", "Contents", "jbr", "Contents", "Home"), "JetBrains 内置", formJavaHome},
			{Java, filepath.Join("/Applications", "IntelliJ IDEA CE.app", "Contents", "jbr", "Contents", "Home"), "JetBrains 内置", formJavaHome},
			{Java, filepath.Join("/Applications", "IntelliJ IDEA Ultimate.app", "Contents", "jbr", "Contents", "Home"), "JetBrains 内置", formJavaHome},
			{Java, filepath.Join("/Applications", "Android Studio.app", "Contents", "jbr", "Contents", "Home"), "JetBrains 内置", formJavaHome},
		}
	case Maven:
		return []sdkRoot{
			{Maven, "/opt/homebrew/opt/maven/libexec", "Homebrew", formMavenHome},
			{Maven, "/usr/local/opt/maven/libexec", "Homebrew", formMavenHome},
		}
	case Node:
		return []sdkRoot{
			// 带版本号的 formula（node@22）和 node 一样常见，要一并收进来：
			// 只认 /opt/homebrew/opt/node 的话，装 node@22 的人会看到一个空的 Node 分组。
			{Node, "/opt/homebrew/opt/node*", "Homebrew", formNodeHome},
			{Node, "/usr/local/opt/node*", "Homebrew", formNodeHome},
			// 官方 .pkg 装在这里；从访达启动时 PATH 上没有 /usr/local/bin。
			{Node, "/usr/local/bin/node", "官方安装包", formExecFile},
		}
	case Python:
		return []sdkRoot{
			// Homebrew 的 Python 老 formula 是 libexec/bin/python3，新的是 bin/python3，
			// 两种都要试：只看 libexec 的话，装了 python@3.14 的人在界面上看不到它——
			// 界面从访达启动，PATH 上没有 /opt/homebrew/bin，那条兜底也接不上。
			{Python, "/opt/homebrew/opt/python@3*", "Homebrew", formPythonBrew},
			{Python, "/usr/local/opt/python@3*", "Homebrew", formPythonBrew},
			{Python, "/Library/Frameworks/Python.framework/Versions/3*", "python.org", formPythonHome},
			// /usr/bin/python3 在没装命令行工具的机器上是个会弹安装框的占位程序，
			// 所以只在命令行工具真的在时，直接用它背后的那个解释器。
			{Python, "/Library/Developer/CommandLineTools/usr/bin/python3", "Xcode 命令行工具", formExecFile},
		}
	case Go:
		return []sdkRoot{
			// 带版本号的 formula（go@1.24）和 go 一样常见，要一并收进来：只认 /opt/homebrew/opt/go
			// 的话，装 go@1.24 的人在这里会看到一个空的 Go 分组。
			{Go, "/opt/homebrew/opt/go*/libexec", "Homebrew", formGoHome},
			{Go, "/usr/local/opt/go*/libexec", "Homebrew", formGoHome},
			{Go, "/usr/local/go", "官方安装包", formGoHome},
		}
	}
	return nil
}
