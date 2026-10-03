package proc

import (
	"fmt"
	"os"
	"path/filepath"
)

// Claim 是一项「这件事只该由我来做」的独占权。
//
// 与状态文件那把锁不是一回事：那把锁跨过的是「读—改—写」这一小段，谁都会拿、
// 拿了就放；这个不一样，它跨的是整个进程的生命周期——拿到就一直握着，
// 直到进程退出（由内核释放）或者自己交回。要解决的是「同一件事被两个进程
// 各做了一遍」，而不是「两次写撞在一起」。
type Claim struct {
	f    *os.File
	path string
}

// TryClaim 尝试独占 path 这个锁文件。拿不到时返回 (nil, false, nil)，不是错误：
// 那说明这件事已经有人在做了，正常结果。
//
// 锁跟着打开的文件描述符走，所以 Claim 必须一直留着，不能拿了就把文件关掉。
func TryClaim(path string) (*Claim, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("创建锁目录失败：%w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("打开锁文件失败：%w", err)
	}
	ok, err := tryLockFile(f)
	if err != nil {
		f.Close()
		return nil, false, fmt.Errorf("锁定 %s 失败：%w", path, err)
	}
	if !ok {
		f.Close()
		return nil, false, nil
	}
	return &Claim{f: f, path: path}, true, nil
}

// Path 返回这把锁落在哪个文件上。
func (c *Claim) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Release 交回独占权。重复调用无妨。
//
// 文件本身不删：删掉的话，正在等的那个进程手里握着的会是一个已经不在目录里的
// 文件描述符，两边各锁各的，锁就白加了。那份文件里不装任何数据，留着不占什么。
func (c *Claim) Release() {
	if c == nil || c.f == nil {
		return
	}
	unlockFile(c.f)
	_ = c.f.Close()
	c.f = nil
}
