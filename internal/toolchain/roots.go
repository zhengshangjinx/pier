package toolchain

import "path/filepath"

// 本文件是「系统级安装位置」的公共部分：同一个目录在 Java 是 JDK 根、在 Node 是安装根，
// 校验方式不一样，所以位置清单里必须连形态一起写。各平台的位置清单在
// roots_darwin.go / roots_linux.go / roots_windows.go。
//
// 只放主目录与 PATH 之外的：sdkman、nvm、asdf、mise 这些版本管理器都在主目录下，
// 三平台同一套路径，留在 discover.go 里。
//
// 清单里可以写 glob（`/usr/lib/jvm/*`），也可以写一个具体的可执行文件
// （`/usr/local/bin/node`）——Glob 对没有通配符的路径同样命中，存在就返回它自己。

// rootForm 说明一个位置是什么形态。
type rootForm int

const (
	formJavaHome   rootForm = iota // 目录本身就是 JAVA_HOME
	formJDKApp                     // 目录的 Contents/Home 才是 JAVA_HOME（macOS 的 .app 与 .jdk 包）
	formMavenHome                  // 目录下有 bin/mvn
	formGoHome                     // 目录下有 bin/go
	formNodeHome                   // 目录下有 bin/node（unix 布局）
	formNodeRoot                   // 目录下直接是 node，没有 bin/（Windows 的安装包与 nvm-windows）
	formPythonHome                 // 目录下有 bin/python3、bin/python，或直接有 python（Windows）
	formPythonBrew                 // 先看 libexec/bin/python3（Homebrew 的老 formula），再看上面那些
	formExecFile                   // 路径本身就是可执行文件
)

// sdkRoot 是「这个系统上、除主目录与 PATH 之外」的一个安装位置。
type sdkRoot struct {
	kind    Kind
	pattern string
	source  string
	form    rootForm
}

// sdk 按形态校验一个路径，认出来就返回。
func (r sdkRoot) sdk(path string) (SDK, bool) {
	switch r.form {
	case formJavaHome:
		return javaSDK(path, r.source)
	case formJDKApp:
		return javaSDK(filepath.Join(path, "Contents", "Home"), r.source)
	case formMavenHome:
		return mavenSDK(path, r.source)
	case formGoHome:
		return goSDK(path, r.source)
	case formNodeHome:
		return binSDK(Node, filepath.Join(path, "bin"), []string{"node"}, r.source)
	case formNodeRoot:
		// Windows 的安装布局：node.exe 直接躺在安装目录（或 nvm 的版本目录）下。
		// binSDK 认出来后会把 Home 算成上一级，这里按布局改回目录自己——
		// 版本目录才是「这个 node 住在哪」，前置到 PATH 的也是它。
		if s, ok := binSDK(Node, filepath.Join(path, "node"), []string{"node"}, r.source); ok {
			s.Home = path
			return s, true
		}
	case formPythonHome:
		return pythonIn(path, r.source, "bin/python3", "bin/python", "python3", "python")
	case formPythonBrew:
		// Homebrew 的 Python 老 formula 在 libexec/bin，新 formula 和 python.org 一样在 bin。
		return pythonIn(path, r.source, "libexec/bin/python3", "bin/python3", "bin/python", "python3", "python")
	case formExecFile:
		return binSDK(r.kind, path, nil, r.source)
	}
	return SDK{}, false
}

func pythonIn(dir, source string, rels ...string) (SDK, bool) {
	for _, rel := range rels {
		if s, ok := binSDK(Python, filepath.Join(dir, rel), nil, source); ok {
			return s, true
		}
	}
	return SDK{}, false
}
