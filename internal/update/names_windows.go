//go:build windows

package update

// 产物里那两份可执行文件的名字。三个平台的产物形状是同一个（见 package.sh），
// 只有 Windows 给它们加 .exe。
const (
	guiExeName = "pier-gui.exe"
	cliExeName = "pier.exe"
)
