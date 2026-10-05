//go:build linux && amd64

package update

// assetName 返回这个平台上该下哪一份产物。界面与命令行都在同一份里，
// kind 不影响结果。
func assetName(ver string, kind Kind) string {
	return "Pier-" + ver + "-linux-amd64.tar.gz"
}
