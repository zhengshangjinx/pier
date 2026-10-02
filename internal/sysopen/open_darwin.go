//go:build darwin

package sysopen

// command 给出在 macOS 上打开一个路径要跑的命令。
//
// `open -R` 是「在访达中显示」：不打开文件本身，而是弹出它所在的目录并选中它。
// 只有 open 认这个参数，所以另外两个平台各写各的。
func command(target string, reveal bool) (string, []string, error) {
	bin, err := findBin([]string{"/usr/bin/open"}, "open")
	if err != nil {
		return "", nil, err
	}
	if reveal {
		return bin, []string{"-R", target}, nil
	}
	return bin, []string{target}, nil
}
