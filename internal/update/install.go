package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhengshangjinx/pier/internal/version"
)

// Install 描述「这份 Pier 装在哪」，也就是这次更新该动什么。
type Install struct {
	// Kind 是安装形态。只有 macOS 上两者要的不是同一份产物，见 Kind 的说明。
	Kind Kind
	// Target 是要换掉的东西：整包是那个 .app，其余是安装它的那个位置
	// （Windows 的安装根目录、Linux 装着两份可执行文件的目录、macOS 裸命令行
	// 就是那个可执行文件本身）。
	Target string
	// Relaunch 是换完之后顺手能拉回来的东西。到底拉不拉由发起方决定
	// （见 Plan.Relaunch）：命令行发起的替换不该凭空弹出一个界面。
	Relaunch string
}

// WhyNoSelfUpdate 说明这份二进制为什么不能自己换自己，以及该走哪条路升级。
// 只有打包产物返回空串。
//
// 界面与命令行摆的是同一句话：两处各拼一句，迟早一句说「重新编译」、
// 另一句说「重新安装」，读的人得自己判断这两句是不是一回事。
func WhyNoSelfUpdate(b version.Build) string {
	switch {
	case b.Packaged:
		return ""
	case b.GoInstall:
		return "这一份是 go install 装的，升级请运行：go install " + version.ModulePath() + "@latest"
	default:
		return "这一份是从源码编译的，不会自己替换自己；要升级就重新编译，或到发布页下载：\n  " + ReleasesPage()
	}
}

// Detect 认一下这份 Pier 是怎么装上的。
//
// 返回的 error 是一句可以直接摆给用户看的话，说明为什么这次替换做不了：
// 来历不对（不是打包产物）、或者装的位置认不出来。**认不出来就不动手**——
// 猜错一次就是把用户的安装目录搞坏，而没有第二次机会去解释。
func Detect() (Install, error) {
	if s := WhyNoSelfUpdate(version.Read()); s != "" {
		return Install{}, errors.New(s)
	}
	exe, err := os.Executable()
	if err != nil {
		return Install{}, fmt.Errorf("认不出这份 Pier 装在哪：%w", err)
	}
	// 先解开软链：/usr/local/bin/pier 指着整包里的那份，直接看 os.Executable()
	// 会认成「裸命令行安装」，于是去换那个软链本身，而 .app 还是旧的。
	// 与 toolchain 那边「选到可执行文件要先解开软链」是同一个坑。
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	inst, ok := detectInstall(exe)
	if !ok {
		return Install{}, errors.New("认不出这份 Pier 是怎么装的，不会自己替换自己；请到发布页下载：\n  " + ReleasesPage())
	}
	return inst, nil
}

// CheckWritable 检查这次替换要动的地方此刻写不写得动。
//
// 这一步要发生在用户点「重启并安装」之前：等他点了、Pier 退了、才发现写不进去，
// 那就只剩一句事后解释了。查得到的结论一定如实说出来——装不上就说装不上，
// 而不是让他看着窗口消失。
func (in Install) CheckWritable() error {
	dir := writableDir(in)
	if dir == "" {
		return errors.New("认不出这份 Pier 装在哪，不会自己替换自己；请到发布页下载：\n  " + ReleasesPage())
	}
	if err := probeWritable(dir); err != nil {
		return fmt.Errorf("装 Pier 的那个目录（%s）写不动，这次更新做不了：%v\n可以到发布页下载后手动覆盖安装：\n  %s",
			dir, err, ReleasesPage())
	}
	return nil
}
