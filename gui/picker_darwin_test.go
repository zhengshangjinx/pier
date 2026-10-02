//go:build darwin

package main

import "testing"

// TestQuoteAppleScript 盯住目录选择框的字符串转义。
//
// 这个函数没有返回值可看，它的成败要看 AppleScript 的语法约定：字面量以 " 起止，
// 内部的 " 必须写成 \"、\ 必须写成 \\。转义漏了不会报错，而是让字面量提前闭合——
// 路径被截断成另一个目录，用户点「浏览…」选了一个目录，填进表单的是另一个。
// 这种错很难在事后看出来，所以在这里按约定逐条核。
func TestQuoteAppleScript(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`/Users/me/proj`, `"/Users/me/proj"`},
		{`/Users/me/a b`, `"/Users/me/a b"`}, // 空格无需转义
		// 反斜杠必须先转，否则 " 转出来的 \" 会被后一步再转成 \\"，
		// 结果是「一个反斜杠 + 一个字面量结束符」——字面量还是断了。
		{`/Users/me/a\b`, `"/Users/me/a\\b"`},
		{`/Users/me/say"hi"`, `"/Users/me/say\"hi\""`},
		{`/Users/me/back\slash"and"quote`, `"/Users/me/back\\slash\"and\"quote"`},
	}
	for _, c := range cases {
		if got := quoteAppleScript(c.in); got != c.want {
			t.Errorf("quoteAppleScript(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}

	// 再按语法规则验一遍，不依赖上面手写的期望值：
	// 去掉首尾的定界引号后，所有 " 都应处在奇数个连续反斜杠之后（即被转义）。
	for _, c := range cases {
		got := quoteAppleScript(c.in)
		if len(got) < 2 || got[0] != '"' || got[len(got)-1] != '"' {
			t.Errorf("quoteAppleScript(%q) = %s，首尾不是定界引号", c.in, got)
			continue
		}
		body := got[1 : len(got)-1]
		run := 0
		for i := 0; i < len(body); i++ {
			switch body[i] {
			case '\\':
				run++
			case '"':
				if run%2 == 0 {
					t.Errorf("quoteAppleScript(%q) = %s，第 %d 个字符处的引号没被转义，字面量会提前闭合", c.in, got, i)
				}
				run = 0
			default:
				run = 0
			}
		}
		// 结尾处不能留下悬空的奇数个反斜杠：那会把收尾的定界引号转义掉，
		// 于是整个字符串吞掉后面的脚本内容。
		if run%2 == 1 {
			t.Errorf("quoteAppleScript(%q) = %s，结尾有悬空的反斜杠", c.in, got)
		}
	}
}
