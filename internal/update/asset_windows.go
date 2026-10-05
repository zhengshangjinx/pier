//go:build windows

package update

// assetName 返回这个平台上该下哪一份产物。
//
// Windows 那份里界面与命令行都在（pier-gui.exe 与 bin\pier.exe），两种装法下的是
// 同一份，kind 不影响结果。
func assetName(ver string, kind Kind) string {
	return "Pier-" + ver + "-windows-amd64.zip"
}
