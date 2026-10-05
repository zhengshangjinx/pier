package update

import (
	"path/filepath"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// 这条路线上有两把锁，管的是两件不同的事，别合成一把：
//
//   - gui.lock 由界面进程从启动握到退出，谁握着就说明「有一个界面开着」。
//     命令行据此拒绝替换——换得动是一回事，把一个跑着旧代码、指着一份新安装的
//     界面留在那儿是另一回事，症状很怪。
//   - apply.lock 由发起替换的那个进程握着，直到它退出。它有两个用处：
//     挡住「两次替换同时进行」，以及给助手一个「发起方已经走了」的判据
//     （锁由内核在进程退出的那一刻放开，比查 PID 可靠，见 waitForCallerExit）。
const (
	guiLockName   = "gui.lock"
	applyLockName = "apply.lock"
)

// guiLockPath 是「有界面在跑」那把锁的位置。
func guiLockPath() (string, error) {
	d, err := config.Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data, guiLockName), nil
}

// applyLockPath 是「正在换文件」那把锁的位置。
func applyLockPath() (string, error) {
	d, err := config.Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data, applyLockName), nil
}

// TryHoldGUI 让界面进程独占「有界面在跑」这把锁，一直握到自己退出。
//
// ok 为假表示已经有一个界面开着（那把锁在它手里）。这不是错误：同时开两个
// 界面本来就允许，只是后开的那个没有「重启并安装」可用——它的替换会
// 让先开的那个变成一份跑着旧代码的界面。调用方拿到 false 时如实说一句就行。
func TryHoldGUI() (*proc.Claim, bool, error) {
	path, err := guiLockPath()
	if err != nil {
		return nil, false, err
	}
	return proc.TryClaim(path)
}

// GUIRunning 报告此刻有没有 Pier 界面在跑。
//
// 抢得到就说明没有（顺手放开，界面随后自己会来拿）。这里有一个极短的窗口：
// 判定与真正的替换之间，用户可能刚好把界面打开——那也没关系，那种情况下
// 换完的文件要等下次启动才生效，不会坏在半路上。
func GUIRunning() bool {
	path, err := guiLockPath()
	if err != nil {
		return false
	}
	claim, ok, err := proc.TryClaim(path)
	if err != nil {
		// 问不出来就别拦着：这只是一道预防，拦错了的方向是「永远更新不了」。
		return false
	}
	if !ok {
		return true
	}
	claim.Release()
	return false
}
