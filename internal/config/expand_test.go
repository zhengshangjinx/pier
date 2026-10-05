package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	lookup := func(name string) (string, bool) {
		v, ok := map[string]string{"HOST": "db", "PORT": "5432", "EMPTY": ""}[name]
		return v, ok
	}
	cases := []struct {
		what string
		in   string
		want string
		bad  bool
	}{
		{"没有引用", "plain", "plain", false},
		{"孤立的美元号", "价格 $5", "价格 $5", false},
		{"$NAME 不算引用", "$HOST", "$HOST", false},
		{"一个变量", "${HOST}", "db", false},
		{"夹在中间", "http://${HOST}:${PORT}/x", "http://db:5432/x", false},
		{"空值也是值", "a${EMPTY}b", "ab", false},
		{"转义", "$${HOST}", "${HOST}", false},
		{"转义夹在中间", "a$${HOST}b", "a${HOST}b", false},
		{"$$ 不是转义时原样留着", "a$$b", "a$$b", false},
		{"没有定义的变量", "${NOPE}", "", true},
		{"${ 没有收尾", "${HOST", "", true},
		{"空名字", "${}", "", true},
		{"数字开头", "${1A}", "", true},
		{"名字里有短横线", "${A-B}", "", true},
		{"名字里有空格", "${A B}", "", true},
		{"多出来的花括号原样留着", "${HOST}}", "db}", false},
	}
	for _, c := range cases {
		got, err := Expand(c.in, lookup)
		if c.bad {
			if err == nil {
				t.Errorf("%s：应当报错，得到 %q", c.what, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s：不该报错：%v", c.what, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s：得到 %q，想要 %q", c.what, got, c.want)
		}
	}
}

// 认不出来的名字要能给调用方一个「这名字眼下还没有」的信号，
// 而不是与「写法不对」混成一种错——前者等一等还有救，后者等多久都是错的。
func TestExpandUnknownIsTyped(t *testing.T) {
	_, err := Expand("${NOPE}", func(string) (string, bool) { return "", false })
	var unknown *UnknownVarError
	if !errors.As(err, &unknown) {
		t.Fatalf("想要的是一句「没有定义」，得到 %v", err)
	}
	if unknown.Name != "NOPE" {
		t.Errorf("名字该是 NOPE，得到 %q", unknown.Name)
	}
}

func TestResolveEnvLayer(t *testing.T) {
	base := func(name string) (string, bool) {
		v, ok := map[string]string{"HOST": "db", "PORT": "5432"}[name]
		return v, ok
	}
	cases := []struct {
		what  string
		layer []EnvKV
		want  []EnvKV
		bad   string // 报错里必须出现的话；为空表示不该报错
	}{
		{
			what:  "取外面的变量",
			layer: []EnvKV{{"URL", "jdbc://${HOST}:${PORT}/a"}},
			want:  []EnvKV{{"URL", "jdbc://db:5432/a"}},
		},
		{
			// 链式引用：map 的遍历顺序是随机的，只有解到不动点才能都对。
			what:  "同一层里互相引用",
			layer: []EnvKV{{"A", "${B}-a"}, {"B", "${C}-b"}, {"C", "c"}},
			want:  []EnvKV{{"A", "c-b-a"}, {"B", "c-b"}, {"C", "c"}},
		},
		{
			// 层里的名字压过外面的同名变量：服务自己的 env 覆盖共享段就靠这一条。
			what:  "层里覆盖外面的",
			layer: []EnvKV{{"HOST", "local"}, {"URL", "http://${HOST}"}},
			want:  []EnvKV{{"HOST", "local"}, {"URL", "http://local"}},
		},
		{
			what:  "没有引用就原样返回",
			layer: []EnvKV{{"A", "100%"}, {"B", "a=b"}},
			want:  []EnvKV{{"A", "100%"}, {"B", "a=b"}},
		},
		{
			what:  "输出顺序固定",
			layer: []EnvKV{{"Z", "1"}, {"A", "2"}, {"M", "3"}},
			want:  []EnvKV{{"A", "2"}, {"M", "3"}, {"Z", "1"}},
		},
		{
			what:  "没有定义的变量",
			layer: []EnvKV{{"A", "${NOPE}"}},
			bad:   "${NOPE} 没有定义",
		},
		{
			what:  "自己引用自己",
			layer: []EnvKV{{"A", "${A}"}},
			bad:   "绕回了这一层",
		},
		{
			what:  "几个变量绕成环",
			layer: []EnvKV{{"A", "${B}"}, {"B", "${A}"}},
			bad:   "绕回了这一层",
		},
		{
			what:  "写法不对",
			layer: []EnvKV{{"A", "${}"}},
			bad:   "不是一个变量名",
		},
	}
	for _, c := range cases {
		got, err := ResolveEnvLayer(c.layer, base)
		if c.bad != "" {
			if err == nil {
				t.Errorf("%s：应当报错，得到 %v", c.what, got)
				continue
			}
			if !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%s：报错是 %q，里面没提 %q", c.what, err, c.bad)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s：不该报错：%v", c.what, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，想要 %v", c.what, got, c.want)
		}
	}
}

// 一层里只要有一个解不出来，整层都不返回：部分展开的结果看着像成功了，
// 而某个值里静静地留着一个 ${X}，等子进程去撞。
func TestResolveEnvLayerAllOrNothing(t *testing.T) {
	got, err := ResolveEnvLayer([]EnvKV{{"A", "ok"}, {"B", "${NOPE}"}}, func(string) (string, bool) {
		return "", false
	})
	if err == nil {
		t.Fatal("应当报错")
	}
	if got != nil {
		t.Errorf("报错时不该给出半份结果，得到 %v", got)
	}
}

func TestEnvList(t *testing.T) {
	if got := EnvList(nil); got != nil {
		t.Errorf("空 map 该给出空表，得到 %v", got)
	}
	got := EnvList(map[string]string{"B": "2", "A": "1", "C": "3"})
	want := []EnvKV{{"A", "1"}, {"B", "2"}, {"C", "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("得到 %v，想要 %v", got, want)
	}
}
