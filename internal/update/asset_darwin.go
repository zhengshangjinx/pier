//go:build darwin

package update

// assetName 返回这个平台上该下哪一份产物。
//
// macOS 有两条装法，要的不是同一份：拖 .app 的那种是整包（zip 里就是 Pier.app，
// 换的时候整个换掉），只装了命令行的那种（tar.gz 装出来的）只要那一个可执行文件。
func assetName(ver string, kind Kind) string {
	if kind == KindBundle {
		return "Pier-" + ver + "-macos-universal.zip"
	}
	return "Pier-" + ver + "-macos-universal.tar.gz"
}
