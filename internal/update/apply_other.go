//go:build !darwin && !linux && !windows

package update

import "errors"

// 这个平台没有自己的换文件做法，我们也不为它发产物（见 asset_other.go），
// 所以这里一律「不做」。五个钩子是平台分家的最小一套：认安装位、问写不写得了、
// 换、能不能当场换、拉回来。
//
// 不做就是不做：界面与命令行都会如实说明这个平台没有自动更新，
// 而不是去走一条没人验过的路。

func canReplaceInPlace() bool { return false }

func detectInstall(exe string) (Install, bool) { return Install{}, false }

func writableDir(in Install) string { return "" }

func applyPlatform(p Plan, logf LogFunc) (bool, error) {
	return false, errors.New("这个平台没有自动更新，请到发布页下载：" + ReleasesPage())
}

func relaunch(p Plan, logf LogFunc) error {
	return errors.New("这个平台没有自动更新，请到发布页下载：" + ReleasesPage())
}
