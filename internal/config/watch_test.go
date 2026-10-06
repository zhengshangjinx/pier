package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// watchYAML 把三种写法与两种不该认的写法各写一遍。
//
// 服务目录都指向真实存在的空目录（下面的 touchDirs 铺）：beta 那条要验
// 「没写模式时按类型给默认」，而类型是认出来的——认不出来的话它会掉进
// 「整个目录都算数」那一档，正好是这条用例要发现的区别。
const watchYAML = `
services:
  - name: plain
    dir: a
  - name: bool
    dir: b
    kind: node
    watch: true
  - name: list
    dir: c
    kind: go
    watch: ["src/**", "go.mod"]
  - name: empty
    dir: d
    watch: false
  - name: nothing
    dir: e
    watch:
`

func loadWatchManifest(t *testing.T, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, DefaultConfigName)
	if err := os.WriteFile(base, []byte(body), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	if err := touchDirs(dir, "a", "b", "c", "d", "e", "f"); err != nil {
		t.Fatalf("建服务目录失败：%v", err)
	}
	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return cfg
}

func touchDirs(root string, names ...string) error {
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// TestWatchParsesTwoShapes 钉着清单里只认那两种写法。
func TestWatchParsesTwoShapes(t *testing.T) {
	cfg := loadWatchManifest(t, watchYAML)

	byName := map[string]*Service{}
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}

	if got := byName["plain"].Watch; got.On {
		t.Error("没写 watch 的服务被当成了要监视")
	}
	if got := byName["bool"].Watch; !got.On || got.Include != nil {
		t.Errorf("watch: true 解成了 %+v，想要「开着、没点名」", got)
	}
	want := []string{"src/**", "go.mod"}
	if got := byName["list"].Watch; !got.On || !reflect.DeepEqual(got.Include, want) {
		t.Errorf("watch: [...] 解成了 %+v，想要 %v", got, want)
	}
	if got := byName["empty"].Watch; got.On {
		t.Error("watch: false 被当成了要监视")
	}
	// `watch:` 后面空着：写了一半的配置不该让服务起不来，当没配。
	if got := byName["nothing"].Watch; got.On {
		t.Error("空的 watch 被当成了要监视")
	}
}

// TestWatchPatternsDefault 钉着 `watch: true` 展开成什么。
//
// 返回的是**实际生效的**那一份（界面与状态都读它），所以这里要的是展开后的模式，
// 不是清单里那个 true。
func TestWatchPatternsDefault(t *testing.T) {
	cfg := loadWatchManifest(t, `
services:
  - name: go
    dir: a
    kind: go
    watch: true
  - name: java
    dir: b
    kind: java
    module: api
    watch: true
  - name: shell
    dir: c
    kind: shell
    watch: true
  - name: off
    dir: d
    kind: go
  - name: mine
    dir: e
    kind: go
    watch: ["cmd/**"]
`)

	byName := map[string]*Service{}
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}

	goPatterns := byName["go"].WatchPatterns()
	if !reflect.DeepEqual(goPatterns, DefaultWatchPatterns(KindGo)) {
		t.Errorf("go 的默认 = %v，想要 %v", goPatterns, DefaultWatchPatterns(KindGo))
	}
	// go.mod 必须在里头：只盯 *.go 的话，加一个依赖（改了 go.mod）不会重启，
	// 而下次编译才报错——那时人已经不知道自己刚才改了什么。
	if !hasPattern(goPatterns, "go.mod") {
		t.Errorf("go 的默认里没有 go.mod：%v", goPatterns)
	}
	if got := byName["java"].WatchPatterns(); !hasPattern(got, "pom.xml") {
		t.Errorf("java 的默认里没有 pom.xml：%v", got)
	}
	// 认不出类型的目录（shell）不猜：整个目录都算数。
	if got := byName["shell"].WatchPatterns(); len(got) != 0 {
		t.Errorf("shell 的默认 = %v，想要空（整个目录）", got)
	}
	// 没配监视的服务不报模式：状态里那一格空着，界面才知道不用显示它。
	if got := byName["off"].WatchPatterns(); len(got) != 0 {
		t.Errorf("没开监视的服务报出了模式：%v", got)
	}
	// 自己点了名的照自己的来，不叠默认。
	if got := byName["mine"].WatchPatterns(); !reflect.DeepEqual(got, []string{"cmd/**"}) {
		t.Errorf("点名的那几个 = %v，想要 [cmd/**]", got)
	}
}

// TestWatchPatternsDetectsKind 钉着「没写 kind 的服务按认出来的类型给默认」。
func TestWatchPatternsDetectsKind(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, DefaultConfigName)
	body := `
services:
  - name: inferred
    dir: app
    watch: true
`
	if err := os.WriteFile(base, []byte(body), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	if err := touchDirs(dir, "app"); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatalf("写标志文件失败：%v", err)
	}
	cfg, err := Load(base)
	if err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	got := cfg.Services[0].WatchPatterns()
	if !reflect.DeepEqual(got, DefaultWatchPatterns(KindGo)) {
		t.Errorf("认出来是 go，模式却是 %v", got)
	}
}

// TestWatchPatternRejected 钉着写坏的模式在加载清单时就报错，并指到那个服务。
func TestWatchPatternRejected(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, DefaultConfigName)
	body := `
services:
  - name: bad
    dir: a
    watch: ["../outside/**"]
`
	if err := os.WriteFile(base, []byte(body), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	if err := touchDirs(dir, "a"); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	_, err := Load(base)
	if err == nil {
		t.Fatal("模式钻出服务目录，加载却没报错")
	}
	if !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "watch") {
		t.Errorf("报错没有指到是哪个服务的 watch：%v", err)
	}
}

// TestWatchRoundTrip 钉着读进来的那一份写回去还是原样。
//
// 界面改一个服务是「读出整份、改一处、写回去」：读成 true 却写成 [true]，
// 下一次打开清单就解不动了，而屏幕上看着一切正常。
func TestWatchRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want any
	}{
		{"开着没点名", `watch: true`, true},
		{"点名了几个", `watch: ["src/**"]`, []any{"src/**"}},
		{"没开", `watch: false`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 过一道带 watch 字段的结构体，和解清单时走的是同一条路。
			var holder struct {
				Watch Watch `yaml:"watch" json:"watch"`
			}
			if err := yaml.Unmarshal([]byte(c.in), &holder); err != nil {
				t.Fatalf("解不动：%v", err)
			}
			out, err := yaml.Marshal(holder)
			if err != nil {
				t.Fatalf("写不回去：%v", err)
			}
			var back struct {
				Watch any `yaml:"watch"`
			}
			if err := yaml.Unmarshal(out, &back); err != nil {
				t.Fatalf("写出来的解不动：%v", err)
			}
			if !reflect.DeepEqual(back.Watch, c.want) {
				t.Errorf("写回去成了 %v，想要 %v", back.Watch, c.want)
			}

			// JSON 那一侧同一条规矩：两种清单格式的形状只能有一处实现。
			jw, err := json.Marshal(holder)
			if err != nil {
				t.Fatalf("JSON 写不回去：%v", err)
			}
			var jback struct {
				Watch any `json:"watch"`
			}
			if err := json.Unmarshal(jw, &jback); err != nil {
				t.Fatalf("JSON 写出来的解不动：%v", err)
			}
			if !reflect.DeepEqual(jback.Watch, c.want) {
				t.Errorf("JSON 写回去成了 %v，想要 %v", jback.Watch, c.want)
			}
		})
	}
}

// TestWatchRejectsObjectShape 钉着对象形状当场报错。
//
// 不是不支持，而是不能悄悄接受：写成 {include: [...]} 的人以为自己配上了，
// 而里面那些键一个都没算数。
func TestWatchRejectsObjectShape(t *testing.T) {
	var w Watch
	err := yaml.Unmarshal([]byte("watch:\n  include: [\"*.go\"]\n"), &w)
	if err == nil {
		t.Fatalf("对象形状被接受了：%+v", w)
	}
}

func hasPattern(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
