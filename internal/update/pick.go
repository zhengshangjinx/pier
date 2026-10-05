package update

import "errors"

// Kind 是这次要替换的安装形态。
//
// 只有 macOS 上这两者要的不是同一份产物：拖进「应用程序」的那种是整包 .app（zip），
// 只装了命令行的那种是一个可执行文件（tar.gz）。另外两个平台上两种装法都在同一份里，
// kind 不影响结果。哪一份叫什么名字由平台文件拼，见 asset_*.go。
type Kind int

const (
	// KindBundle 是整包替换的那种安装（macOS 的 .app）。
	KindBundle Kind = iota
	// KindCLI 是只有可执行文件的那种安装。
	KindCLI
)

// Pick 挑出这次该下载的产物。
//
// 名字交给平台文件拼，这里只负责把「没有对应产物」说清楚：认不出要下哪一份就不下，
// 绝不猜一个名字去撞 404——那会让人以为是网络问题。
func (r Release) Pick(kind Kind) (Asset, error) {
	name := assetName(r.Version, kind)
	if name == "" {
		return Asset{}, errors.New("这个平台没有对应的发布产物")
	}
	a, ok := r.Find(name)
	if !ok {
		return Asset{}, errors.New(r.displayVersion() + "里没有 " + name)
	}
	return a, nil
}
