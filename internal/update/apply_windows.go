//go:build windows

package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// canReplaceInPlace 报告这份二进制能不能当场把运行中的自己换掉。
//
// Windows 上不行：正在运行的 exe 是锁着的，改名都改不动。所以命令行那份更新
// 不自己动手，一律交给助手——等本进程退出之后，由助手在空档里换（见 apply.go 的
// Schedule / RunHelper）。
func canReplaceInPlace() bool { return false }

// detectInstall 认一下这份二进制是怎么装上的。
//
// 安装脚本把两份可执行文件并排放在 %LOCALAPPDATA%\Programs\Pier 里
// （见 install.ps1）。认的依据就是「我这个可执行文件旁边还站着另一个」，
// 不写死那个路径——写死了，把它装到别处的人就永远更新不了。
//
// 从解压出来的文件夹里直接跑（pier-gui.exe 在根上、pier.exe 在 bin\ 里）认不出：
// 那是一个临时目录，替换它没有意义，而且它压根没装在 PATH 上。
func detectInstall(exe string) (Install, bool) {
	dir := filepath.Dir(exe)
	gui := filepath.Join(dir, guiExeName)
	cli := filepath.Join(dir, cliExeName)
	if !exists(gui) || !exists(cli) {
		return Install{}, false
	}
	return Install{Kind: KindCLI, Target: dir, Relaunch: gui}, true
}

// writableDir 是检查写不动时该去看的那个目录：装着两份 exe 的目录本身。
func writableDir(in Install) string { return in.Target }

// oldPrefix 是挪开旧文件时给它们加的前缀，后面接一个时刻。
const oldPrefix = ".old-"

// applyPlatform 把两份 exe 换掉。
//
// 有意不跑 install.ps1：升级要做的只是把两个文件换掉，PATH 与开始菜单在第一
// 次安装时就写好了，再跑一遍安装器等于把那几件事重做一次，每一步都是新的失败
// 可能——而失败发生在 Pier 已经退出、没有窗口能报错的时候。
//
// 换法分三段，每一段失败都能退回去：
//
//	旧的挪开（改名）→ 新的放进去 → 成了才删旧的
//
// 改名是一步原子操作，所以「挪开」这一段要么全成要么没动；放新文件那一段用复制
// 加改名（先落 .tmp 再改名过去），中途失败留下的是一个 .tmp，不会是一份看起来
// 像模像样的半截二进制。
func applyPlatform(p Plan, logf LogFunc) (bool, error) {
	root, err := stageRoot(p.Stage, guiExeName)
	if err != nil {
		return false, err
	}
	// bin\ 里那份是压缩包里的位置（见 package.sh）；摊在根上的那种也认。
	srcCLI := filepath.Join(root, "bin", cliExeName)
	if !exists(srcCLI) {
		srcCLI = filepath.Join(root, cliExeName)
	}
	pairs := []filePair{
		{src: filepath.Join(root, guiExeName), dst: filepath.Join(p.Target, guiExeName)},
		{src: srcCLI, dst: filepath.Join(p.Target, cliExeName)},
	}
	for _, pr := range pairs {
		// 不要执行位：Windows 上普通文件一律 0666，看模式位看不出什么。
		if err := checkNotEmpty(pr.src, false); err != nil {
			return false, err
		}
	}

	cleanOldFiles(p.Target, logf)

	// 第一段：旧的挪开。
	stamp := time.Now().Format("20060102-150405")
	moved := make([]filePair, 0, len(pairs))
	for _, pr := range pairs {
		if !exists(pr.dst) {
			// 上一次替换只做了一半，这个位置本来就是空的；直接放新的进去。
			logf("%s 不在，直接放新的。", filepath.Base(pr.dst))
			continue
		}
		old := pr.dst + oldPrefix + stamp
		if err := os.Rename(pr.dst, old); err != nil {
			return false, restoreOld(moved, fmt.Errorf("把 %s 挪开失败（安装目录一个文件都没动）：%w", filepath.Base(pr.dst), err), logf)
		}
		logf("%s 挪到了 %s", filepath.Base(pr.dst), filepath.Base(old))
		moved = append(moved, filePair{src: old, dst: pr.dst})
	}

	// 第二段：新的放进去。
	placed := make([]string, 0, len(pairs))
	for _, pr := range pairs {
		if err := copyFile(pr.src, pr.dst); err != nil {
			for _, name := range placed {
				os.Remove(name)
			}
			return false, restoreOld(moved, fmt.Errorf("放入 %s 失败：%w", filepath.Base(pr.dst), err), logf)
		}
		placed = append(placed, pr.dst)
		logf("换上了 %s", filepath.Base(pr.dst))
	}

	// 第三段：旧的删掉。删不掉只是留下一份旧文件，不影响这次替换成不成立。
	for _, pr := range moved {
		if err := os.Remove(pr.src); err != nil {
			logf("旧的 %s 没能删掉（不影响使用）：%v", filepath.Base(pr.src), err)
		}
	}
	return false, nil
}

// filePair 记一对「从哪儿到哪儿」。挪旧文件那一段里，src 是挪开之后的新名字。
type filePair struct{ src, dst string }

// restoreOld 把已经挪开的旧文件挪回原位。
//
// 挪回去也失败就把话说清楚：哪个文件现在叫什么名字、该手工改成什么——
// 这是助手留下的最后一句，用户只能照着它把机器收回来。
func restoreOld(moved []filePair, cause error, logf LogFunc) error {
	var stuck []string
	for _, pr := range moved {
		if err := os.Rename(pr.src, pr.dst); err != nil {
			stuck = append(stuck, fmt.Sprintf("%s 还在 %s", filepath.Base(pr.dst), pr.src))
			continue
		}
		logf("把 %s 挪回去了", filepath.Base(pr.dst))
	}
	if len(stuck) > 0 {
		return fmt.Errorf("%w；而且旧的文件没能挪回去（%s），请手工把它们改回原来的名字", cause, strings.Join(stuck, "；"))
	}
	return fmt.Errorf("%w；原来的文件已经放回去了，安装目录没变", cause)
}

// cleanOldFiles 清掉上一次留下的 .old-* 。
//
// 只认这两份 exe 开头的名字：那个目录里别的东西是用户的，一个都不碰。
func cleanOldFiles(dir string, logf LogFunc) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		for _, exe := range []string{guiExeName, cliExeName} {
			if strings.HasPrefix(name, exe+oldPrefix) {
				if err := os.Remove(filepath.Join(dir, name)); err == nil {
					logf("清掉了上一次留下的 %s", name)
				}
				break
			}
		}
	}
}

// relaunch 把换好的界面拉回来。
func relaunch(p Plan, logf LogFunc) error { return startDetached(p.Relaunch) }
