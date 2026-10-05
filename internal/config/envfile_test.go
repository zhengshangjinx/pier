package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bom 用字节写而不是转义：\u 那种写法在传递过程中很容易被存成一个真的 BOM，
// 而这里要的恰恰是「文件开头有这么三个字节」这件事本身。
var bom = string([]byte{0xEF, 0xBB, 0xBF})

func TestParseEnvFile(t *testing.T) {
	cases := []struct {
		what string
		in   string
		want []EnvKV
	}{
		{"空文件", "", nil},
		{"只有空行与注释", "\n   \n# 说明\n", nil},
		{"基本的几条", "A=1\nB=2\n", []EnvKV{{"A", "1"}, {"B", "2"}}},
		{"export 前缀", "export A=1\n", []EnvKV{{"A", "1"}}},
		{"等号两边留白", "  A  =  1  \n", []EnvKV{{"A", "1"}}},
		{"值里的等号", "URL=postgres://h/db?sslmode=disable\n", []EnvKV{{"URL", "postgres://h/db?sslmode=disable"}}},
		{"双引号", `A="x y"` + "\n", []EnvKV{{"A", "x y"}}},
		{"引号里的井号", "A='x #y'\n", []EnvKV{{"A", "x #y"}}},
		{"值里的井号不是注释", "PASS=ab#cd\n", []EnvKV{{"PASS", "ab#cd"}}},
		{"空值", "A=\n", []EnvKV{{"A", ""}}},
		{"CRLF", "A=1\r\nB=2\r\n", []EnvKV{{"A", "1"}, {"B", "2"}}},
		{"开头的 BOM", bom + "A=1\n", []EnvKV{{"A", "1"}}},
		{"下划线开头的名字", "_A=1\n", []EnvKV{{"_A", "1"}}},
		{"同名以最后一条为准", "A=1\nB=2\nA=3\n", []EnvKV{{"A", "3"}, {"B", "2"}}},
		// 值原样保留，不做转义：.env 不是脚本，里面的 \n 就该是两个字符，
		// 而且各家实现怎么转义本来也不一致。
		{"值里的反斜杠原样留着", `A=a\nb` + "\n", []EnvKV{{"A", `a\nb`}}},
	}
	for _, c := range cases {
		got, err := ParseEnvFile(c.in)
		if err != nil {
			t.Errorf("%s：不该报错，得到 %v", c.what, err)
			continue
		}
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，想要 %v", c.what, got, c.want)
		}
	}
}

func TestParseEnvFileRejects(t *testing.T) {
	cases := []struct {
		what string
		in   string
		says string // 报错里必须出现的话，让人能照着改
	}{
		{"没有等号", "JUST_A_WORD\n", "第 1 行"},
		{"引号没配对", `A="x` + "\n", "引号没有配对"},
		{"名字不合法", "A-B=1\n", "变量名不合法"},
		{"名字是数字开头", "1A=1\n", "变量名不合法"},
		{"名字是空的", "=1\n", "变量名不合法"},
	}
	for _, c := range cases {
		_, err := ParseEnvFile(c.in)
		if err == nil {
			t.Errorf("%s：应当报错，却通过了", c.what)
			continue
		}
		// 报错要指到具体那一行：一个几百行的 .env 里说「有个引号没配对」等于没说。
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s：报错是 %q，里面没提 %q", c.what, err, c.says)
		}
	}
}

func TestLoadEnvFileMissing(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadEnvFile(filepath.Join(dir, EnvFileName))
	if err != nil {
		t.Fatalf("目录里没有 .env 是常态，不该报错：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("没有文件时该是什么都没有，得到 %v", got)
	}

	path := filepath.Join(dir, EnvFileName)
	if err := os.WriteFile(path, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = LoadEnvFile(path)
	if err != nil {
		t.Fatalf("读得动却报错：%v", err)
	}
	if len(got) != 1 || got[0].Key != "A" || got[0].Value != "1" {
		t.Errorf("得到 %v，想要 A=1", got)
	}
}

func TestValidEnvName(t *testing.T) {
	ok := []string{"A", "_x", "a1", "PATH", "HTTPS_PROXY", "__"}
	bad := []string{"", "1A", "A-B", "A.B", "A B", "变量", "A$"}
	for _, s := range ok {
		if !ValidEnvName(s) {
			t.Errorf("%q 应当算合法", s)
		}
	}
	for _, s := range bad {
		if ValidEnvName(s) {
			t.Errorf("%q 不该算合法", s)
		}
	}
}
