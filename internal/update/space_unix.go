//go:build !windows

package update

import "syscall"

// freeSpace 返回 path 所在文件系统上，当前用户还能用多少字节。
func freeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail 是「非特权用户能用的块数」（总块数里已经扣掉了留给 root 的那 5%），
	// Bsize 是一块多大。
	return int64(st.Bavail) * int64(st.Bsize), nil
}
