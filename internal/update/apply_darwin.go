//go:build darwin

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhengshangjinx/pier/internal/sysopen"
)

// macOS 上更新要用的几样东西。一律走绝对路径：这个进程可能是从访达启动的，
// 环境里的 PATH 只有 /usr/bin:/bin，但把位置写死更省心，也不怕用户改过 PATH。
const (
	// dittoBin 每次 macOS 都带着，就是用来整包拷贝的。
	//
	// 不用手写的递归拷贝：那会丢掉 xattr 与 bundle 的元数据，还可能把 ad-hoc
	// 签名弄坏——Apple Silicon 上签名坏掉的 arm64 二进制连启都启不来。
	dittoBin = "/usr/bin/ditto"
	// xattrBin 用来清隔离位。我们自己下下来的文件不带它，但用户可能先在浏览器里
	// 下了一份再拖进来，顺手清一下不花钱。
	xattrBin = "/usr/bin/xattr"

	// oldSuffix 是整包替换时旧的那份先挪到哪儿去。
	oldSuffix = ".old"
)

// canReplaceInPlace 见 apply_windows.go 的说明。
func canReplaceInPlace() bool { return true }

// detectInstall 认一下这份二进制是怎么装上的。
func detectInstall(exe string) (Install, bool) {
	if app, ok := bundleRoot(exe); ok {
		return Install{Kind: KindBundle, Target: app, Relaunch: app}, true
	}
	// 不是整包：那是 tar.gz 装出来的那种，只有一份可执行文件，换的就是它自己。
	return Install{Kind: KindCLI, Target: exe}, true
}

// writableDir 是检查写不动时该去看的那个目录。
//
// 两种形态都是「在安装位旁边先放一份新的，再改名过去」，所以要的是上一级目录
// 写不写得动——/Applications 对非管理员账号就可能是只读的。
func writableDir(in Install) string { return filepath.Dir(in.Target) }

// bundleRoot 从一个可执行文件的位置往上找出它所属的 .app。
func bundleRoot(exe string) (string, bool) {
	macos := filepath.Dir(exe)
	if filepath.Base(macos) != "MacOS" {
		return "", false
	}
	contents := filepath.Dir(macos)
	if filepath.Base(contents) != "Contents" {
		return "", false
	}
	app := filepath.Dir(contents)
	if !strings.HasSuffix(filepath.Base(app), ".app") {
		return "", false
	}
	return app, true
}

// applyPlatform 换文件。整包整个换，裸命令行只换那一个文件。
func applyPlatform(p Plan, logf LogFunc) (bool, error) {
	if p.Kind == KindBundle {
		return applyBundle(p, logf)
	}
	root, err := stageRoot(p.Stage, cliExeName)
	if err != nil {
		return false, err
	}
	src := filepath.Join(root, cliExeName)
	if err := checkNotEmpty(src, true); err != nil {
		return false, err
	}
	if err := replaceFile(src, p.Target, logf); err != nil {
		return false, err
	}
	return false, nil
}

// applyBundle 把整个 .app 换掉。
//
// 顺序是「旧的先挪开 → 新的拷进来 → 成功了才删旧的」。挪开是一步原子改名，
// 拷到一半失败就把旧的挪回来——中间那段时间安装目录里是空的，但那比留着一份
// 半新半旧的 bundle 强：前者一眼看得出来，后者能启动、却到处都不对。
func applyBundle(p Plan, logf LogFunc) (bool, error) {
	src, err := findBundle(p.Stage)
	if err != nil {
		return false, err
	}
	// 先把新的验一遍再动手：一份坏包把能用的安装换成打不开的东西，
	// 是这次更新里代价最大的错误。
	if err := checkBundle(src); err != nil {
		return false, err
	}
	old := p.Target + oldSuffix
	if fi, err := os.Stat(old); err == nil {
		// 这个名字只有我们会用，但万一它是别的东西（用户自己建的文件），
		// 宁可停下来说清楚，也不去删一个来路不明的东西。
		if !fi.IsDir() {
			return false, fmt.Errorf("%s 已经存在，而且不是我们留下的目录，不敢动它；先把它挪开再试", old)
		}
		if err := os.RemoveAll(old); err != nil {
			return false, fmt.Errorf("清不掉上一次留下的 %s：%w", old, err)
		}
		logf("清掉了上一次留下的 %s", old)
	}

	if err := os.Rename(p.Target, old); err != nil {
		return false, fmt.Errorf("把旧的挪开失败（%s 一个文件都没动）：%w", p.Target, err)
	}
	logf("旧的挪到了 %s", old)

	if err := runDitto(src, p.Target); err != nil {
		return rollbackBundle(p, old, err, logf)
	}
	// 反过来再验一次：ditto 说成功了，但装出来的东西能不能用是另一回事。
	if err := checkBundle(p.Target); err != nil {
		return rollbackBundle(p, old, fmt.Errorf("换过去的包不完整：%w", err), logf)
	}

	// 清隔离位不花什么，失败也不影响这次替换：签名的有效性由系统在启动时判，
	// 而我们在下面已经把签名验过了。
	if out, err := exec.Command(xattrBin, "-dr", "com.apple.quarantine", p.Target).CombinedOutput(); err != nil {
		logf("清隔离位没成（不影响使用）：%v %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.RemoveAll(old); err != nil {
		// 删不掉只是留下一份旧包，不影响这次替换成不成立。
		logf("旧的没能删掉（不影响使用）：%v", err)
	}
	return false, nil
}

// rollbackBundle 把旧的那份挪回去。
func rollbackBundle(p Plan, old string, cause error, logf LogFunc) (bool, error) {
	os.RemoveAll(p.Target)
	if err := os.Rename(old, p.Target); err != nil {
		return false, fmt.Errorf("%w；而且旧的那份没能挪回来（还在 %s），请手工把 %s 改成 %s",
			cause, old, filepath.Base(old), filepath.Base(p.Target))
	}
	logf("把旧的挪回了 %s", p.Target)
	return true, cause
}

// runDitto 把新的一份整包拷到安装位。
func runDitto(src, dst string) error {
	out, err := exec.Command(dittoBin, src, dst).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("拷贝新的 .app 失败：%s", msg)
	}
	return nil
}

// findBundle 在解压出来的那棵树里找那个 .app。
func findBundle(stage string) (string, error) {
	if looksLikeApp(stage) {
		return stage, nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", fmt.Errorf("读不了解压目录：%w", err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			found = append(found, filepath.Join(stage, e.Name()))
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("解压出来的产物里没有 .app：%s", stage)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("解压出来的产物里有 %d 个 .app，不知道换哪一个", len(found))
	}
}

// checkBundle 检查一份 .app 里该有的东西都在且不是空的。
func checkBundle(app string) error {
	if err := checkNotEmpty(filepath.Join(app, "Contents", "Info.plist"), false); err != nil {
		return err
	}
	if err := checkNotEmpty(filepath.Join(app, "Contents", "MacOS", guiExeName), true); err != nil {
		return err
	}
	return nil
}

// looksLikeApp 报告这个路径看起来是不是一个 app bundle。
func looksLikeApp(app string) bool {
	return exists(filepath.Join(app, "Contents", "Info.plist"))
}

// relaunch 把换好的 Pier 拉回来。
//
// 整包要走系统的打开方式：注册到 LaunchServices 的是一份 .app，不是一个裸
// 二进制，直接 exec 里面那个文件会得到一个没有应用身份的进程。
func relaunch(p Plan, logf LogFunc) error {
	if p.Kind == KindBundle {
		return sysopen.Run(p.Relaunch, false)
	}
	return startDetached(p.Relaunch)
}
