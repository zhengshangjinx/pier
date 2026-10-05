//go:build linux

package update

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// canReplaceInPlace 见 apply_windows.go 的说明。
func canReplaceInPlace() bool { return true }

// detectInstall 认一下这份二进制是怎么装上的。
//
// Linux 那份是 install.sh 装出来的：两份可执行文件并排放在 ~/.local/bin 里
// （或者用户自己改过的别处）。认的依据就是「我这个可执行文件旁边还站着 pier-gui」，
// 不写死 ~/.local/bin——写死了，把它装到 /usr/local 的人就永远更新不了。
func detectInstall(exe string) (Install, bool) {
	dir := filepath.Dir(exe)
	if !exists(filepath.Join(dir, guiExeName)) {
		return Install{}, false
	}
	return Install{Kind: KindCLI, Target: dir, Relaunch: filepath.Join(dir, guiExeName)}, true
}

// writableDir 是检查写不动时该去看的那个目录：装两份可执行文件的目录本身。
func writableDir(in Install) string { return in.Target }

// applyPlatform 跑解压出来的 install.sh。
//
// 不去挨个覆盖那两个文件：install.sh 干的就是这件事，而且它顺带把 .desktop 与
// 图标一起刷了。重跑一遍安装脚本是幂等的，本来就是升级该走的那条路。
//
// 它写到哪里由那份脚本自己定（当前是 ~/.local/bin），不是由这里的 Target 定——
// 换一版改了安装位置的脚本，跟着走的是脚本，我们只负责把它跑起来。
func applyPlatform(p Plan, logf LogFunc) (bool, error) {
	root, err := stageRoot(p.Stage, "install.sh")
	if err != nil {
		return false, err
	}
	for _, name := range []string{cliExeName, guiExeName} {
		if err := checkNotEmpty(filepath.Join(root, name), true); err != nil {
			return false, err
		}
	}
	script := filepath.Join(root, "install.sh")
	if err := checkNotEmpty(script, false); err != nil {
		return false, err
	}

	logf("运行 %s", script)
	cmd := exec.Command("/bin/sh", script)
	// 工作目录定在解压出来的那棵树里：脚本自己会按 $0 找同目录的文件，
	// 但把 cwd 也摆正，它里面任何一处相对路径都不会跑到别处去。
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			logf("安装脚本：%s", line)
		}
	}
	if err != nil {
		// 这一步做不到「要么全成、要么全不动」：脚本是一串就地覆盖，跑到一半失败
		// 留下的是半新半旧的安装。所以说明里要把现在是什么状态、下一步怎么办讲清楚，
		// 而不是只说一句「失败了」。
		return false, fmt.Errorf("安装脚本失败：%v\n安装目录可能处于半新半旧的状态，请手工运行 %s 再试一次", err, script)
	}
	return false, nil
}

// relaunch 把换好的界面拉回来。
func relaunch(p Plan, logf LogFunc) error { return startDetached(p.Relaunch) }
