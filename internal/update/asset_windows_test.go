//go:build windows

package update

import "testing"

// 产物名由 package.sh 定，两边必须一个字都对得上：对不上就是「这一版里没有
// 那个文件」，看着像发布那边漏发了，其实是这里拼错了。
//
// Windows 上两种装法用的是同一份（界面与命令行都在里面），kind 不该影响结果。
func TestAssetName(t *testing.T) {
	for _, kind := range []Kind{KindBundle, KindCLI} {
		if got := assetName("0.3.0", kind); got != "Pier-0.3.0-windows-amd64.zip" {
			t.Errorf("kind %d 的产物是 %q", kind, got)
		}
	}
}
