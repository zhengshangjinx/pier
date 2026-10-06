package watch

import (
	"strings"
	"testing"
)

// TestMatch 钉着模式表的三条规则：不含 `/` 的按名字认（任意深度），
// 含 `/` 的从根算起，`**` 跨任意多段。
//
// 这三条是「写的人一眼能猜到」的全部内容，改了它们，清单里已经写下的
// `watch: [...]` 会在用户不知情的时候换一份含义。
func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		include []string
		rel     string
		want    bool
	}{
		{"不写模式全算数", nil, "anything/at/all.txt", true},
		{"空表全算数", []string{}, "a/b/c.txt", true},

		{"根上的 go 文件", []string{"*.go"}, "main.go", true},
		{"任意深度的 go 文件", []string{"*.go"}, "internal/watch/match.go", true},
		{"扩展名不对", []string{"*.go"}, "internal/watch/match_test.go.bak", false},
		{"点名的文件", []string{"go.mod"}, "go.mod", true},
		{"点名的文件在子目录里也算", []string{"go.mod"}, "sub/go.mod", true},
		{"点名的文件按名字认而不是按后缀", []string{"go.mod"}, "go.mod.bak", false},

		{"有斜杠就从根算起", []string{"src/main/**"}, "src/main/java/App.java", true},
		{"有斜杠时不吃别处", []string{"src/main/**"}, "src/test/java/AppTest.java", false},
		{"** 吃零段", []string{"src/**"}, "src/App.java", true},
		{"** 吃多段", []string{"src/**"}, "src/a/b/c/App.java", true},
		{"单星不跨斜杠", []string{"src/*.go"}, "src/a/main.go", false},
		{"单星在本段里", []string{"src/*.go"}, "src/main.go", true},

		{"表里任意一条命中就算", []string{"*.py", "src/**"}, "src/index.ts", true},
		{"表里都不命中", []string{"*.py", "src/**"}, "docs/readme.md", false},
		{"末尾带斜杠说的是这个目录底下", []string{"src/"}, "src/main.go", true},
		{"末尾带斜杠不吃同名的文件", []string{"src/"}, "vendor/src.go", false},
		{"只写 ** 是全都要", []string{"**"}, "a/b/c.txt", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Match(c.include, c.rel); got != c.want {
				t.Errorf("Match(%v, %q) = %v，想要 %v", c.include, c.rel, got, c.want)
			}
		})
	}
}

// TestCheckPattern 钉着当场拦下的那几种写法。
//
// 拦在加载清单这一步：模式写错了扫起来只是「什么都没盯到」，界面上一切正常，
// 而用户改了半天代码没有任何反应——那时再回头查一个反斜杠要花很久。
func TestCheckPattern(t *testing.T) {
	good := []string{"*.go", "src/main/**", "go.mod", "a/b/c.ts"}
	for _, p := range good {
		if err := CheckPattern(p); err != nil {
			t.Errorf("CheckPattern(%q) = %v，想要 nil", p, err)
		}
	}

	bad := []string{
		"",
		"   ",
		"/etc/passwd",
		`\windows\system32`,
		`C:\Users\me\src`,
		"C:/Users/me/src",
		"../sibling/**",
		"src/../../etc/**",
	}
	for _, p := range bad {
		if err := CheckPattern(p); err == nil {
			t.Errorf("CheckPattern(%q) 没报错，但它写不成样子", p)
		}
	}
}

// TestCheckPatternNamesThePattern 钉着报错里带着那个模式本身。
//
// 一个服务可以写十几条模式，只说「有个模式不对」的话，用户得一条条试。
func TestCheckPatternNamesThePattern(t *testing.T) {
	err := CheckPattern("../outside/**")
	if err == nil {
		t.Fatal("没报错")
	}
	if !strings.Contains(err.Error(), "../outside/**") {
		t.Errorf("报错里没有那个模式本身：%v", err)
	}
}
