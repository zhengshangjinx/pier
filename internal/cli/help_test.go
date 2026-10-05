package cli

import (
	"bytes"
	"strings"
	"testing"
)

// handlers 与 commands 必须对得上：漏一条，帮助里就写着一条敲了没反应的命令；
// 反过来，多一条没进帮助的动词，用户永远不知道它存在。
func TestHandlersMatchCommands(t *testing.T) {
	documented := map[string]bool{}
	for _, c := range commands {
		documented[c.name] = true
		if _, ok := handlers[c.name]; !ok {
			t.Errorf("帮助里有 %s，handlers 里没有", c.name)
		}
	}
	extra := map[string]bool{}
	for _, v := range extraVerbs {
		extra[v] = true
		if _, ok := handlers[v]; !ok {
			t.Errorf("extraVerbs 里的 %s 不在 handlers 里", v)
		}
	}
	for name := range handlers {
		if !documented[name] && !extra[name] {
			t.Errorf("handlers 里的 %s 既不在帮助里，也没写进 extraVerbs", name)
		}
	}
}

// 「哪儿能拿机器读的输出」只有 jsonVerbs 一处出处：它和每个动词自己那份参数表
// 必须一致，否则帮助里的两句话会有一句是假的。
func TestJSONVerbsMatchFlags(t *testing.T) {
	declared := map[string]bool{}
	for _, c := range commands {
		for _, f := range c.flags {
			if strings.Contains(f.form, "--json") {
				declared[c.name] = true
			}
		}
	}
	for _, v := range jsonVerbs {
		if !declared[v.form] {
			t.Errorf("%s 认 --json，但它自己的说明里没列出来", v.form)
		}
	}
	for name := range declared {
		if !acceptsJSON(name) {
			t.Errorf("%s 的说明里写着 --json，但 acceptsJSON 不认它", name)
		}
	}
}

// 全局帮助里的那两列必须真的渲染出来：一行空着、或者表头少了，
// 看帮助的人只会以为自己少看了一段。
func TestPrintCommands(t *testing.T) {
	var buf bytes.Buffer
	printCommands(&buf)
	out := buf.String()

	for _, want := range []string{"命令：", "通用：", "--config <清单>", "-h, --help", "--version"} {
		if !strings.Contains(out, want) {
			t.Errorf("全局帮助里少了 %q", want)
		}
	}
	// 每个动词都得在「命令」那一列里露脸。
	for _, c := range commands {
		if !strings.Contains(out, c.usage[0].form) {
			t.Errorf("全局帮助里没有 %s 的用法行", c.name)
		}
	}
	// 认 --json 的那几个，那句话是从 jsonVerbs 渲染的，得真的出现。
	if !strings.Contains(out, jsonVerbList()) {
		t.Errorf("全局帮助里没写明 --json 认哪些动词（%s）", jsonVerbList())
	}
}

func TestPrintCommandHelp(t *testing.T) {
	for _, c := range commands {
		var buf bytes.Buffer
		printCommandHelp(&buf, c)
		out := buf.String()
		for _, want := range []string{"pier " + c.name, c.desc, "用法：", "参数：", "-h, --help"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s 的说明里少了 %q", c.name, want)
			}
		}
		if !c.noConfig && !strings.Contains(out, "--config <清单>") {
			t.Errorf("%s 的说明里少了 --config", c.name)
		}
	}
}

// `-h` 打在哪个位置都算，且要在动词自己的解析器之前生效——
// 否则 `pier up --help` 会被 up 读成一个服务名，报「没有名为 --help 的服务」。
func TestWantsHelp(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"demo"}, false},
		{[]string{"-h"}, true},
		{[]string{"--help"}, true},
		{[]string{"demo-admin", "-h"}, true},
		{[]string{"--tail", "5", "--help"}, true},
		{[]string{"--tail", "-h"}, true}, // 当值也照样算：它是帮助，不是行数
	}
	for _, c := range cases {
		if got := wantsHelp(c.args); got != c.want {
			t.Errorf("wantsHelp(%q) = %v，想要 %v", c.args, got, c.want)
		}
	}
}
