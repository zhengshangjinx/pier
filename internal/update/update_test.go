package update

import (
	"os"
	"testing"
)

// newTestClient 建一个 Client，数据目录与下载目录都指到临时目录里。
// 测试绝不碰用户真实的 ~/.pier，仓库里的一条硬规矩。
//
// PIER_HOME 已经被这个测试设过就不再动它：一个用例里要建两个 Client 时
// （「重启之后还认得上一次查到的」那种），两次必须看着同一份数据。
func newTestClient(t *testing.T, opts Options) *Client {
	t.Helper()
	if os.Getenv("PIER_HOME") == "" {
		t.Setenv("PIER_HOME", t.TempDir())
	}
	if opts.Current == "" {
		opts.Current = "0.2.0"
	}
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	return New(opts)
}

// 基址能被环境变量顶掉。这条是给手工验收留的唯一一条路：真界面连一个本地的
// 假 release 走完整条路——不发一版就试不了这个功能。
func TestBaseURLFromEnv(t *testing.T) {
	t.Setenv(baseURLEnv, "http://127.0.0.1:9/")
	if got := New(Options{}).base; got != "http://127.0.0.1:9" {
		t.Errorf("基址是 %q", got)
	}
	// 显式给的那个更硬：测试要能把它顶掉。
	if got := New(Options{BaseURL: "http://x/"}).base; got != "http://x" {
		t.Errorf("显式基址被环境变量盖掉了：%q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "3"); got != "3" {
		t.Errorf("firstNonEmpty = %q，想要 3", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty = %q，想要空", got)
	}
}

// 状态文件写坏了就当没有：它只是「上次查到哪儿」，不是用户写的配置，
// 不该让下一次检查直接失败。
func TestLoadStateTolerant(t *testing.T) {
	path := t.TempDir() + "/update.json"
	if got := LoadState(path); !got.CheckedAt.IsZero() {
		t.Errorf("文件不存在时该给空状态，得到 %+v", got)
	}
	if err := os.WriteFile(path, []byte("{ 这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(path); !got.CheckedAt.IsZero() {
		t.Errorf("文件坏了时该给空状态，得到 %+v", got)
	}
}

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	path, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	want := State{ETag: `W/"abc"`, Release: Release{Version: "0.3.0", Tag: "v0.3.0", Notes: "说明"}}
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	got := LoadState(path)
	if got.ETag != want.ETag || got.Release.Version != want.Release.Version || got.Release.Notes != want.Release.Notes {
		t.Errorf("读回来的是 %+v，想要 %+v", got, want)
	}
}
