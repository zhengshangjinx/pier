package watch

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// skipNames 是扫树时整个跳过的目录名，外加两个不该算数的文件名。
//
// 这一张表是为了**防自激**，不是为了省时间：编译产物就落在服务目录里，
// 而重启要重新编译一遍——不排掉的话，一次改动会以「编译→产物落地→再重启」
// 一直转下去，用户看到的是服务在不停地重启，而原因藏在时序里。
//
// 名单挑的是各类项目里名叫产物的那几个目录。宁可多排一个（大不了某次改动
// 没被看见，用户手动重启一次），也不能少排——少排的那个会让服务停不下来。
// `.DS_Store` 单列进来是因为它太容易变了：在访达里打开一次那个目录就会写一个，
// 而它与代码毫无关系。
var skipNames = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".DS_Store": true,
	"node_modules": true, "bower_components": true,
	"target": true, "dist": true, "out": true, "build": true,
	".idea": true, ".vscode": true, ".gradle": true, ".mvn": true,
	"__pycache__": true, ".venv": true, "venv": true,
	"vendor": true, ".bundle": true,
	".next": true, ".nuxt": true, ".svelte-kit": true, ".astro": true,
	".turbo": true, ".parcel-cache": true, ".cache": true,
	"coverage": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
}

// stamp 是一个文件的「此刻长什么样」。
//
// 比 mtime 与大小而不是内容摘要：算摘要要把每个文件读一遍，一棵有几百兆资源的
// 树每秒钟读一次是不能接受的，而 mtime 是文件系统替我们维护的那份摘要。
// 代价是「改回同样的字节数、又恰好把 mtime 改回原样」看不到——手工不会发生。
type stamp struct {
	mod  time.Time
	size int64
}

func (s stamp) same(o stamp) bool { return s.size == o.size && s.mod.Equal(o.mod) }

// Tree 是盯着一棵目录的那份状态。
//
// 不是并发安全的：它由持有自动重启独占权的那个宿主的一轮巡检驱动，一次只有一个
// 调用方（见 panel 的 watchLoop）。
type Tree struct {
	root    string
	include []string
	// outside 是永远不看的几棵树（绝对路径）：Pier 自己的数据目录在里面——
	// 服务的日志写在 ~/.pier/logs 下、go 的产物写在 ~/.pier/cache/bin 下，
	// 而这两处每次启动都在写，算进来的话每个配了监视的服务都会自己重启自己。
	outside []string
	stamps  map[string]stamp
	base    bool
}

// New 起一棵树的监视。include 为空表示整个目录（排除的那些仍然排除，见 skipNames）。
func New(root string, include, outside []string) *Tree {
	return &Tree{root: root, include: include, outside: outside}
}

// Scan 扫一遍，返回相对路径上变化的文件（新增、改动、删除），按名字排序。
//
// 第一趟只建立基线（返回空）：刚起的服务不该因为「现在开始看了」就重启一次，
// 而那一下重启会把用户手里正在跑的现场清掉。
func (t *Tree) Scan() ([]string, error) {
	next := make(map[string]stamp)
	err := filepath.WalkDir(t.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 读不动的目录（权限、半路被删）跳过：一棵树里有一个这样的目录，
			// 不该让整个监视停摆——那等于把剩下的几百个文件也一起放弃了。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != t.root && t.skip(p, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if skipNames[d.Name()] || t.under(p) {
			return nil
		}
		rel, err := filepath.Rel(t.root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !Match(t.include, rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		next[rel] = stamp{mod: info.ModTime(), size: info.Size()}
		return nil
	})
	if err != nil {
		return nil, err
	}

	prev := t.stamps
	t.stamps = next
	if !t.base {
		t.base = true
		return nil, nil
	}

	var changed []string
	for rel, st := range next {
		if old, ok := prev[rel]; !ok || !old.same(st) {
			changed = append(changed, rel)
		}
	}
	for rel := range prev {
		if _, ok := next[rel]; !ok {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// Baseline 表示这棵树还没扫过第一趟。
func (t *Tree) Baseline() bool { return !t.base }

func (t *Tree) skip(abs, name string) bool {
	return skipNames[name] || t.under(abs)
}

// under 判断一个绝对路径落不落在不看的那几棵树里。
func (t *Tree) under(abs string) bool {
	for _, root := range t.outside {
		if root == "" {
			continue
		}
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
