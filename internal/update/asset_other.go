//go:build !darwin && !windows && !(linux && amd64) && !(linux && arm64)

package update

// assetName 返回这个平台上该下哪一份产物。
//
// 我们只发 macOS、Windows 与 Linux（amd64 / arm64）这几种，其余平台一律空串：
// 调用方据此如实说「这里没有你这一份」，不猜一个名字去撞 404。
func assetName(ver string, kind Kind) string { return "" }
