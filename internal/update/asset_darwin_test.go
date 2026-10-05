//go:build darwin

package update

import "testing"

// 产物名由 package.sh 定，两边必须一个字都对得上：对不上就是「这一版里没有
// 那个文件」，看着像发布那边漏发了，其实是这里拼错了。
//
// macOS 是唯一一个两种装法要两份不同产物的平台：拖进「应用程序」的那种是整包
// （zip 里就是 Pier.app），只装了命令行的那种只要那一个可执行文件。
func TestAssetName(t *testing.T) {
	if got := assetName("0.3.0", KindBundle); got != "Pier-0.3.0-macos-universal.zip" {
		t.Errorf("整包产物是 %q", got)
	}
	if got := assetName("0.3.0", KindCLI); got != "Pier-0.3.0-macos-universal.tar.gz" {
		t.Errorf("命令行产物是 %q", got)
	}
}
