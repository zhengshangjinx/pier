// Package watch 盯着一个目录，看有没有文件变过。
//
// 用轮询 mtime 而不是系统的文件通知（macOS 的 FSEvents、Linux 的 inotify、
// Windows 的 ReadDirectoryChangesW）：三套 API 各有一堆边界（重命名、软链、
// 递归深度、监视数上限），而这里要回答的问题只有「刚那一下改动落定了没有」——
// 一秒扫一遍一棵源码树在开发机上不算什么，换来的是三平台同一份实现。
//
// 这一包不认识服务，也不重启任何东西：它只回答「哪些文件与上一趟不一样了」。
// 什么时候重启、要不要合并、由谁来重启，是 panel 那边的事。
package watch

import (
	"fmt"
	"path"
	"strings"
)

// Match 判断一个相对路径（用 / 分隔）算不算命中了模式表。
//
// 规则三条，都是照「写的人一眼能猜到」挑的：
//
//   - 模式里没有 `/` 时匹配**任意深度**的文件名：`*.go` 就是这棵树里所有的 go 文件。
//     写成只匹配根目录的话，默认那套就得写成 `**/*.go`，而绝大多数人不会这么写，
//     于是「盯了，但没盯到 src/ 里的东西」——一个不报错、也不生效的设置。
//   - 有 `/` 时从根算起：`src/main/**` 只认 src/main 底下。
//   - `*` 与 `?` 不跨 `/`（filepath.Match 那套），`**` 跨任意多段（含零段）。
//   - 以 `/` 收尾表示「这个目录底下的东西」：`src/` 就是 `src/**`。不这么认的话
//     它会解成「一个叫 src 的文件」，什么也盯不到，而写的人以为已经把 src 交出去了。
//
// include 为空表示全都算数，这是给 shell 服务那一档留的（见 config.WatchPatterns）。
func Match(include []string, rel string) bool {
	if len(include) == 0 {
		return true
	}
	name := strings.Split(rel, "/")
	for _, pattern := range include {
		if matchOne(pattern, name) {
			return true
		}
	}
	return false
}

func matchOne(pattern string, name []string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	segments := strings.Split(pattern, "/")
	if !strings.Contains(pattern, "/") {
		// 不含 `/` 的模式：补一个 `**/` 在前头，见 Match 的第一条规则。
		segments = append([]string{"**"}, segments...)
	}
	return matchSegments(segments, name)
}

func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// `**` 吃掉零到多段。剩下的模式能在哪一段接上就算命中——
			// 递归一层层试，模式与路径都很短，不必做动态规划。
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pattern[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pattern[0], name[0])
		if err != nil || !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// CheckPattern 在加载清单时拦下写不成样子的模式。
//
// 拦在这里而不是扫的时候：模式写错了（绝对路径、`..` 钻出目录）扫起来只是「什么都没盯到」，
// 界面上看着一切正常，而用户改了半天代码没反应——一个必须当场说出来的错误。
func CheckPattern(pattern string) error {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return fmt.Errorf("模式不能为空")
	}
	// 绝对路径当场拦下：换一台机器就指到别处去了，而清单是跟着仓库走的。
	// 第二三个条件认的是 Windows 的盘符（`C:\…` 与 `C:/…`）——这里不能借
	// filepath.IsAbs，它在 macOS 上对 `C:\…` 说不是，同一份清单在三个平台上
	// 就有两种判法。
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || (len(p) >= 2 && p[1] == ':') {
		return fmt.Errorf("模式要相对服务目录写，不能是绝对路径：%s", pattern)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("模式不能钻出服务目录：%s", pattern)
		}
	}
	return nil
}
