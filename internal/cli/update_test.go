package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/update"
	"github.com/zhengshangjinx/pier/internal/version"
)

func rel(ver, tag string) update.Release {
	return update.Release{
		Version:   ver,
		Tag:       tag,
		URL:       "https://example.test/releases/tag/" + tag,
		Published: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
	}
}

// 三个退出码就是这个命令的全部契约：脚本只看它。
func TestReportCheckExitCodes(t *testing.T) {
	cases := []struct {
		name string
		res  update.Result
		want int
		says string
	}{
		{
			name: "有新版本",
			res:  update.Result{Release: rel("0.3.0", "v0.3.0"), Current: "0.2.0", HasUpdate: true},
			want: checkHasUpdate,
			says: "有新版本 0.3.0",
		},
		{
			name: "已是最新",
			res:  update.Result{Release: rel("0.2.0", "v0.2.0"), Current: "0.2.0"},
			want: checkUpToDate,
			says: "已经是最新版本",
		},
		{
			// 本机构建没有版本号，根本比不了。这时候说「已经是最新」是个和事实
			// 相反的结论——看着像认真比过了。
			name: "本机构建",
			res:  update.Result{Release: rel("0.3.0", "v0.3.0"), Current: "dev"},
			want: 1,
			says: "不比较版本",
		},
		{
			// tag 认不出来时不比大小，但那一版的信息照旧摆出来。
			name: "标签认不出来",
			res:  update.Result{Release: rel("", "nightly"), Current: "0.2.0"},
			want: checkUpToDate,
			says: "nightly",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := reportCheck(&buf, c.res)
			if got != c.want {
				t.Errorf("退出码是 %d，想要 %d", got, c.want)
			}
			if !strings.Contains(buf.String(), c.says) {
				t.Errorf("输出里没有 %q：\n%s", c.says, buf.String())
			}
		})
	}
}

// 本机构建那一支不能说「已经是最新版本」。
func TestReportCheckDevDoesNotClaimLatest(t *testing.T) {
	var buf bytes.Buffer
	reportCheck(&buf, update.Result{Release: rel("0.3.0", "v0.3.0"), Current: "dev"})
	if strings.Contains(buf.String(), "已经是最新版本") {
		t.Errorf("没比过就说已经是最新：\n%s", buf.String())
	}
}

// 发布时间与发布页地址是接口给的，没给就不摆那一行。
func TestReportCheckOmitsMissingFields(t *testing.T) {
	var buf bytes.Buffer
	reportCheck(&buf, update.Result{Release: update.Release{Version: "0.2.0"}, Current: "0.2.0"})
	out := buf.String()
	if strings.Contains(out, "发布时间") || strings.Contains(out, "发布说明") {
		t.Errorf("没给的东西不该摆出来：\n%s", out)
	}
}

// 标签认不出来时「最新版本」那一行要留空，不能整行消失：整行没了看着像
// 这次检查没做成，而其实是做成了、只是那个 tag 不是版本号。
func TestReportCheckKeepsLatestRow(t *testing.T) {
	var buf bytes.Buffer
	reportCheck(&buf, update.Result{Release: update.Release{Tag: "nightly"}, Current: "0.2.0"})
	if !strings.Contains(buf.String(), "最新版本  nightly") {
		t.Errorf("最新版本那一行不见了：\n%s", buf.String())
	}
}

// 中文标签按显示宽度对齐：按字节数补空格会歪（一个汉字三个字节、只占两格）。
func TestWriteKVAlignsCJK(t *testing.T) {
	var buf bytes.Buffer
	writeKV(&buf, [][2]string{{"当前版本", "0.2.0"}, {"发布说明", "https://x"}})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("打出来 %d 行", len(lines))
	}
	tagLen := strings.Index(lines[0], "0.2.0")
	urlLen := strings.Index(lines[1], "https://x")
	if tagLen != urlLen {
		t.Errorf("两行的值没对齐：%d 对 %d\n%s", tagLen, urlLen, buf.String())
	}
}

// 该由谁来升级，三种来历三句话。这两类替换文件没有意义：下一次 go build /
// go install 又盖回去，而用户会以为升级成功了。
func TestWriteInstallHint(t *testing.T) {
	cases := []struct {
		name   string
		build  version.Build
		says   string
		silent bool
	}{
		{name: "打包产物不多说", build: version.Build{Packaged: true}, silent: true},
		{name: "go install 装的", build: version.Build{GoInstall: true}, says: "go install"},
		{name: "源码编的", build: version.Build{Source: true}, says: "从源码编译的"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeInstallHint(&buf, c.build)
			out := buf.String()
			if c.silent {
				if out != "" {
					t.Errorf("不该说什么，却说了：%q", out)
				}
				return
			}
			if !strings.Contains(out, c.says) {
				t.Errorf("该说 %q，说的是 %q", c.says, out)
			}
		})
	}
}

// 不认识的参数要挡住，不能默默当成 --check：一个打错的参数换来一次真下载，
// 是最难解释的那种意外。
func TestCmdUpdateRejectsUnknownArgs(t *testing.T) {
	if got := cmdUpdate([]string{"--nope"}); got != 1 {
		t.Errorf("退出码是 %d，想要 1", got)
	}
}

// 这一份该由谁来升级，要在出网之前就判掉。
//
// 测试二进制就是这么一份「从源码编出来的」：认不出安装位，于是不必连网、
// 也不该连网——去问一次「有没有新版本」，对一个根本换不了自己的东西毫无意义。
func TestInstallUpdateRefusesSourceBuild(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	if got := cmdUpdate(nil); got != 1 {
		t.Errorf("退出码是 %d，想要 1", got)
	}
}

// 进度是刷在终端上那一行的，不是写给文件的：重定向到文件时那些回车会把它
// 写成一堆重复的碎片。
func TestNewProgressOnlyOnTerminal(t *testing.T) {
	var buf bytes.Buffer
	if p := newProgress(&buf); p != nil {
		t.Error("写入口不是终端，却还是给了一个刷进度的")
	}
}
