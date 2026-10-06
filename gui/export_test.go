package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/panel"
)

// TestExportLogCopiesWholeFile 钉住「导出」导的是整份，不是抽屉里显示的那一段。
//
// 抽屉一次只读末尾 panel.LogLines 行 / panel.LogBytes 字节，那个复制按钮复制的
// 就是这一段；导出要是也走读数那条路，导出来的文件会在最要紧的地方少一截——
// 开头那几行常常正是启动命令和配置，而文件本身看着完全正常。
func TestExportLogCopiesWholeFile(t *testing.T) {
	a := testApp(t)
	path, err := a.panel.LogPath("fixture-api", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	// 造一份比读取上限大得多的日志，头一行放一个只可能出现在整份里的标记。
	var b strings.Builder
	b.WriteString("第一行：启动命令 build && run\n")
	for b.Len() < panel.LogBytes*2 {
		b.WriteString("随后的输出行\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "导出.log")
	n, err := exportLogTo(path, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(b.Len()) {
		t.Errorf("导出 %d 字节，原文件 %d 字节", n, b.Len())
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != b.String() {
		t.Error("导出来的内容与原文件不一致")
	}

	// 抽屉那一份拿来对照：它本来就该是被截过的，两者不是一回事。
	out, err := a.panel.Logs("fixture-api", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || strings.Contains(out.Text, "第一行") {
		t.Fatalf("前提不成立：抽屉这一段本来就该是截过的（truncated=%v）", out.Truncated)
	}
}
