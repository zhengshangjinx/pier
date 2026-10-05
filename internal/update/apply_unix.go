//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceFile 用 src 顶掉 dst。
//
// 先拷到 dst 旁边再改名过去，而不是直接改名：src 在缓存目录里，和安装位常常
// 不在同一个卷上，跨卷改名会直接失败。改名本身是原子的，中途失败留下的是一个
// .new，dst 还是原来那一份完整的——跑着的进程手里那个 inode 也不受影响，
// 所以 unix 上换一个正在运行的可执行文件是安全的。
func replaceFile(src, dst string, logf LogFunc) error {
	tmp := dst + ".new"
	if err := copyFile(src, tmp); err != nil {
		return fmt.Errorf("复制 %s 到 %s 失败：%w", filepath.Base(src), filepath.Base(tmp), err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换 %s 失败：%w", dst, err)
	}
	logf("换掉了 %s", dst)
	return nil
}
