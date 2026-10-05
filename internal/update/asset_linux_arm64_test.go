//go:build linux && arm64

package update

import "testing"

// 产物名由 package.sh 定，两边必须一个字都对得上：对不上就是「这一版里没有
// 那个文件」，看着像发布那边漏发了，其实是这里拼错了。
//
// 架构要认准：arm64 的那份下成 amd64 的话，装上去是一份跑不起来的二进制，
// 而那一步发生在 Pier 已经退出之后，没人能报错。
func TestAssetName(t *testing.T) {
	for _, kind := range []Kind{KindBundle, KindCLI} {
		if got := assetName("0.3.0", kind); got != "Pier-0.3.0-linux-arm64.tar.gz" {
			t.Errorf("kind %d 的产物是 %q", kind, got)
		}
	}
}
