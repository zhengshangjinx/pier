//go:build windows

package toolchain

import (
	"os"
	"path/filepath"
)

// platformRoots 是 Windows 上、主目录与 PATH 之外还要去扫的位置。
//
// Windows 上没有「装到哪」的共识，同一个 JDK 可能来自官方安装包、Adoptium、Zulu、
// Corretto、IDE 自带、chocolatey、scoop、nvm-windows……所以这里的清单比 unix 长得多，
// 每一条都对应一个装得到东西的渠道。位置取自各家的默认安装路径，
// 一律经环境变量拼（用户改过安装盘符时也能找对）。
func platformRoots(home string, k Kind) []sdkRoot {
	var out []sdkRoot
	add := func(kind Kind, pattern, source string, form rootForm) {
		if pattern != "" {
			out = append(out, sdkRoot{kind, pattern, source, form})
		}
	}
	// env 拼一个「%VAR%\子路径」；变量没设（理论上不该发生）时返回空串，
	// 调用方会跳过——否则 Glob 会把它当成相对路径去扫当前目录。
	env := func(name string, parts ...string) string {
		base := os.Getenv(name)
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, parts...)...)
	}

	switch k {
	case Java:
		// 各家 JDK 安装包都把根目录（里面直接是 bin\java.exe）放在这些位置下。
		for _, pf := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			add(Java, env(pf, "Java", "*"), "系统 JDK 目录", formJavaHome)
			add(Java, env(pf, "Eclipse Adoptium", "*"), "Temurin", formJavaHome)
			add(Java, env(pf, "Microsoft", "jdk-*"), "Microsoft", formJavaHome)
			add(Java, env(pf, "Zulu", "*"), "Zulu", formJavaHome)
			add(Java, env(pf, "BellSoft", "LibericaJDK*"), "Liberica", formJavaHome)
			add(Java, env(pf, "Amazon Corretto", "*"), "Corretto", formJavaHome)
			add(Java, env(pf, "Semeru", "*"), "Semeru", formJavaHome)
			add(Java, env(pf, "JetBrains", "*", "jbr"), "JetBrains 内置", formJavaHome)
		}
		add(Java, env("LocalAppData", "Programs", "Eclipse Adoptium", "*"), "Temurin", formJavaHome)
		// IDEA 的「下载 JDK」把各个版本都放在这里。
		add(Java, filepath.Join(home, ".jdks", "*"), "IntelliJ 下载", formJavaHome)
		add(Java, filepath.Join(home, "scoop", "apps", "openjdk", "current"), "scoop", formJavaHome)

	case Maven:
		add(Maven, env("ProgramData", "chocolatey", "lib", "maven", "apache-maven-*"), "chocolatey", formMavenHome)
		add(Maven, env("ProgramFiles", "JetBrains", "*", "plugins", "maven", "lib", "maven3"), "JetBrains 内置", formMavenHome)
		add(Maven, filepath.Join(home, "scoop", "apps", "maven", "current"), "scoop", formMavenHome)

	case Node:
		add(Node, env("ProgramFiles", "nodejs"), "官方安装包", formNodeRoot)
		// nvm-windows 把每个版本放在 %APPDATA%\nvm\v22.20.0 下，node.exe 就在版本目录里。
		add(Node, env("AppData", "nvm", "v*"), "nvm", formNodeRoot)
		add(Node, env("AppData", "fnm", "node-versions", "*", "installation"), "fnm", formNodeRoot)
		add(Node, env("LocalAppData", "Volta", "tools", "image", "node", "*"), "volta", formNodeRoot)
		add(Node, filepath.Join(home, "scoop", "apps", "nodejs", "current"), "scoop", formNodeRoot)

	case Python:
		add(Python, env("LocalAppData", "Programs", "Python", "Python3*"), "官方安装包", formPythonHome)
		add(Python, env("ProgramFiles", "Python3*"), "官方安装包", formPythonHome)
		add(Python, filepath.Join(home, "anaconda3"), "Anaconda", formPythonHome)
		add(Python, filepath.Join(home, "miniconda3"), "Miniconda", formPythonHome)

	case Go:
		add(Go, env("ProgramFiles", "Go"), "官方安装包", formGoHome)
		add(Go, env("LocalAppData", "Programs", "Go"), "官方安装包", formGoHome)
		add(Go, env("ProgramData", "chocolatey", "lib", "golang", "tools"), "chocolatey", formGoHome)
		add(Go, filepath.Join(home, "scoop", "apps", "go", "current"), "scoop", formGoHome)
	}
	return out
}
