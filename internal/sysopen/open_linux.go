//go:build linux

package sysopen

// command 给出在 Linux 上打开一个路径要跑的命令。
//
// reveal 不做区分：xdg-open 只能「打开」，没有「选中这个文件」的口径——
// 传目录进去是弹开目录，行为虽不完全一样，但至少点得动。
func command(target string, reveal bool) (string, []string, error) {
	bin, err := findBin([]string{"/usr/bin/xdg-open", "/bin/xdg-open"}, "xdg-open")
	if err != nil {
		return "", nil, err
	}
	_ = reveal
	return bin, []string{target}, nil
}
