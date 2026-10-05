//go:build windows

package update

import "golang.org/x/sys/windows"

// freeSpace 返回 path 所在盘上，当前用户还能用多少字节。
func freeSpace(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// 三个返回值依次是「调用者还能用的」「磁盘总共多大」「磁盘一共还剩多少」。
	// 配额也算在第一个里面，那才是我们想要的那个数。
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(avail), nil
}
