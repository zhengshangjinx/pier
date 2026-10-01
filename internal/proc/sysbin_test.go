package proc

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSysBinIgnoresPath 是这次 PATH 依赖问题的回归护栏。
//
// 造的场面就是出问题的那一种：PATH 清空，lsof 依然必须找得到。
// 如果哪天有人把 sysOutput 改回 exec.Command("lsof", ...)，这条会立刻红，
// 而不是等到某台从访达启动的机器上悄悄渲染出一排「—」才被发现。
func TestSysBinIgnoresPath(t *testing.T) {
	t.Setenv("PATH", "")
	for _, name := range []string{"lsof", "ps"} {
		got, err := sysBin(name)
		if err != nil {
			t.Errorf("PATH 为空时找不到 %s：%v", name, err)
			continue
		}
		if !filepath.IsAbs(got) {
			t.Errorf("%s 解析结果不是绝对路径：%q", name, got)
		}
		if !isExecutable(got) {
			t.Errorf("%s 解析到 %q，但它不可执行", name, got)
		}
	}
}

// TestSysBinAbsolutePathIsRun 验证解析出来的路径真能跑起来。
// 光看路径存在不够：路径对但文件不对（比如解析到了同名目录）时，
// 上面的存在性检查会过，而这里会露馅。
func TestSysBinAbsolutePathIsRun(t *testing.T) {
	bin, err := sysBin("ps")
	if err != nil {
		t.Skipf("本机没有 ps：%v", err)
	}
	out, err := sysOutput("ps", "-p", "1", "-o", "pid=")
	if err != nil {
		t.Fatalf("%s 跑不起来：%v", bin, err)
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Errorf("ps -p 1 输出 = %q，期望 1", strings.TrimSpace(string(out)))
	}
}

// TestSysBinUnknownCommand 确认找不到时给的是错误而不是空路径。
// 返回空串会让调用方 exec.Command("") 失败得更晚、更难懂。
func TestSysBinUnknownCommand(t *testing.T) {
	if p, err := sysBin("pier-绝对不存在的命令"); err == nil {
		t.Errorf("期望报错，却拿到 %q", p)
	}
}

// TestSysBinCandidatesAreAbsolute 防止候选表里混进相对路径。
// 一旦混进去，解析结果就又会跟着工作目录走，等于把 PATH 问题换个形式搬回来。
func TestSysBinCandidatesAreAbsolute(t *testing.T) {
	for name, cands := range sysBinCand {
		if len(cands) == 0 {
			t.Errorf("%s 的候选表是空的，等于永远只能回落 PATH", name)
		}
		for _, c := range cands {
			if !filepath.IsAbs(c) {
				t.Errorf("%s 的候选 %q 不是绝对路径", name, c)
			}
		}
	}
	// lsof 在 macOS 上就在 /usr/sbin，这个顺序不能被人无意改掉：
	// 放在后面的代价是每次查询多几次注定失败的 stat。
	if got := sysBinCand["lsof"][0]; got != "/usr/sbin/lsof" {
		t.Errorf("lsof 首选 = %q，期望 /usr/sbin/lsof", got)
	}
}
