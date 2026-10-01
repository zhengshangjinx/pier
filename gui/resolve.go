package main

import (
	"path/filepath"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
)

// resolveConfig 决定本次加载哪份清单，并说明它的来源。
//
// 命令行给了 --config 就用它（YAML 清单只读，界面上的编辑入口会收起来）；
// 否则用 Pier 自己的数据文件，不存在就建一个空的，界面会引导添加第一个服务。
func resolveConfig(flagPath string) (path, source string, err error) {
	if p := strings.TrimSpace(flagPath); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p, "命令行指定", nil
	}
	store, err := config.DefaultStorePath()
	if err != nil {
		return "", "", err
	}
	if _, err := config.EnsureStore(store, nil); err != nil {
		return "", "", err
	}
	return store, "本机数据", nil
}
