package version

import (
	"runtime/debug"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v0.2.0", "0.2.0"},
		{"V0.2.0", "0.2.0"},
		{" 0.2.0\n", "0.2.0"},
		{"0.2.0", "0.2.0"},
		// v 后面不是数字就不动它：免得把 "version" 的头一个字母也吃掉。
		{"version", "version"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// 字符串比大小会在这里出错：0.9.0 > 0.10.0，所以必须按数字段比。
		{"0.9.0", "0.10.0", -1},
		{"0.10.0", "0.9.0", 1},
		{"0.2.0", "0.2.0", 0},
		{"v0.2.0", "0.2.0", 0},
		{"0.2", "0.2.0", 0},
		{"1.2.3", "1.2", 1},
		// 预发布比同号正式版旧。
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		// 构建元数据不参与比较。
		{"1.2.3+build.5", "1.2.3", 0},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if !ok {
			t.Errorf("Compare(%q, %q) 说不可比较，期望可比较", c.a, c.b)
			continue
		}
		if got != c.want {
			t.Errorf("Compare(%q, %q) = %d，想要 %d", c.a, c.b, got, c.want)
		}
	}
}

// 不可比较的输入一律返回 ok=false：调用方据此不做「有新版」的判断。
func TestCompareUnparseable(t *testing.T) {
	bad := []string{
		"", "dev", "dev-abc", "(devel)",
		"1.2.3.4", "0.2.x", "1..2", "x1.2.3",
	}
	for _, s := range bad {
		if _, ok := Compare(s, "0.2.0"); ok {
			t.Errorf("Compare(%q, %q) 说可比较，应该不可比较", s, "0.2.0")
		}
		if _, ok := Compare("0.2.0", s); ok {
			t.Errorf("Compare(%q, %q) 说可比较，应该不可比较", "0.2.0", s)
		}
	}
}

// 带后缀的一律不算「我们的版本号」。伪版本那一例是重点：从某个提交
// go install 出来就是它，认了等于「永远比任何 tag 都旧」，于是每次检查都提示升级。
//
// 顺带钉住返回的是重建出来的规范形式，不是原串——注入的值带着 +meta 时，
// 界面上该显示 0.2.0 而不是 0.2.0+meta。
func TestCanonRejectsPrerelease(t *testing.T) {
	for _, s := range []string{
		"v0.0.0-20261005120000-abcdef123456",
		"1.0.0-0.20261005120000-abcdef123456",
		"0.3.0-rc1",
	} {
		if got, ok := Canon(s); ok {
			t.Errorf("Canon(%q) = %q, true，应该不认", s, got)
		}
	}
	ok := []struct{ in, want string }{
		{"v0.2.0", "0.2.0"},
		{"0.2", "0.2.0"},
		{"1.2.3+build.5", "1.2.3"},
	}
	for _, c := range ok {
		got, isOK := Canon(c.in)
		if !isOK || got != c.want {
			t.Errorf("Canon(%q) = %q, %v，想要 %q, true", c.in, got, isOK, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	if !Newer("0.3.0", "0.2.0") {
		t.Error("0.3.0 应该比 0.2.0 新")
	}
	if Newer("0.2.0", "0.2.0") {
		t.Error("同版本不该算新")
	}
	// 关键是这两条：当前版本不可比较时，永远不说「有新版」。
	if Newer("0.3.0", "dev") {
		t.Error("当前是 dev 时不该报有新版")
	}
	if Newer("dev", "0.2.0") {
		t.Error("候选版本是 dev 时不该报有新版")
	}
}

func TestCurrentUsesInjectedVersion(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "0.3.0"
	if got := Current(); got != "0.3.0" {
		t.Errorf("Current() = %q，想要 0.3.0", got)
	}
	// tag 那种带 v 的写法也认。
	Version = "v0.3.0"
	if got := Current(); got != "0.3.0" {
		t.Errorf("Current() = %q，想要 0.3.0（v 该被去掉）", got)
	}
	if Dev() {
		t.Error("注入过版本号时不该算 dev")
	}
}

// 没注入时回落到"dev"：测试二进制里 Main.Version 不是版本号，源码构建也是这条路。
func TestCurrentFallsBackToDev(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	// 伪版本也在这条路上：注入成伪版本（相当于从某个提交装的）不该被当成一个版本号。
	for _, injected := range []string{"", "dev", "0.2.x", "v0.0.0-20261005120000-abcdef123456"} {
		Version = injected
		if got := Current(); got != "dev" {
			t.Errorf("Version=%q 时 Current() = %q，想要 dev", injected, got)
		}
	}
	if !Dev() {
		t.Error("没注入版本号时应该是 dev 构建")
	}
}

// 测试二进制相当于「在源码树里编出来的那一份」：既没注入版本号，也不算 go install 装的。
// 这两个标志是自更新的开关（只有 Packaged 那份才允许原地替换自己），所以钉在这里。
func TestReadInTestBinary(t *testing.T) {
	b := Read()
	if b.Packaged {
		t.Error("测试二进制不该被当成发布产物")
	}
	if b.GoInstall {
		t.Error("测试二进制不该被当成 go install 装出来的")
	}
	if !b.Source {
		t.Error("测试二进制应该被当成源码构建")
	}
	if b.Version != Current() {
		t.Errorf("Read().Version = %q，与 Current() = %q 不一致", b.Version, Current())
	}
}

// 来历的判定。入参是构建信息里的两样东西，用手写的假数据覆盖全部分支——
// 真家伙造不出来：go install 装出来的那种构建信息（没有 vcs 组）在本机没有模块缓存可供
// 操作，而源码树里编的永远带着 vcs 组。
func TestClassify(t *testing.T) {
	tree := []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "e7f25a4a98dfb0edb4f492c66e91744fda7d1dea"},
		{Key: "vcs.modified", Value: "true"},
	}
	cases := []struct {
		name     string
		version  string // 打包注入的版本号
		released string // 打包注入的记号
		mainVer  string // 构建信息里的模块版本
		settings []debug.BuildSetting
		want     Build
	}{
		{
			name:     "打包产物",
			version:  "0.2.0",
			released: "1",
			mainVer:  "v0.2.0",
			settings: tree,
			want:     Build{Version: "0.2.0", Packaged: true},
		},
		{
			// 源码树里编的：从 go 1.24 起在打了 tag 的提交上也会被盖上版本号，
			// 但有 vcs 组在，仍该算源码构建。
			name:     "源码树里编的（正好在 tag 上）",
			mainVer:  "v0.2.0",
			settings: tree,
			want:     Build{Version: "0.2.0", Source: true},
		},
		{
			name:     "源码树里编的（不在 tag 上）",
			mainVer:  "(devel)",
			settings: tree,
			want:     Build{Version: "dev", Source: true},
		},
		{
			// go install <模块>@<版本>：带着真版本号，但没有 vcs 组（模块缓存里没有 .git）。
			name:    "go install 装的",
			mainVer: "v0.2.0",
			want:    Build{Version: "0.2.0", GoInstall: true},
		},
		{
			// 版本号认不出来（Current 会退回 dev），但它确实是装出来的：
			// 提示语该说的是「用 go install 升级」，不是「你这份是源码编的」。
			name:    "从某个提交 go install 的（伪版本）",
			mainVer: "v0.0.0-20261005120000-abcdef123456",
			want:    Build{Version: "dev", GoInstall: true},
		},
		{
			// 在源码树里编、但没盖 vcs 信息（-buildvcs=false，或者容器里没装 git）：
			// 这时也没有模块版本，仍旧算源码构建。
			name:    "源码树里编的（没盖 vcs 信息）",
			mainVer: "(devel)",
			want:    Build{Version: "dev", Source: true},
		},
		{
			// 连模块信息都没有（GOPATH 那种构建）。
			name: "没有模块信息",
			want: Build{Version: "dev", Source: true},
		},
		{
			// 记号就是记号，不管版本号认不认得出：这种组合只可能来自一个改坏了的打包脚本，
			// 真出现时按发布产物对待，症状（界面说「版本号认不出来」）摆在明面上更好查。
			name:     "有记号但版本号认不出",
			released: "1",
			mainVer:  "(devel)",
			settings: tree,
			want:     Build{Version: "dev", Packaged: true},
		},
		{
			// 什么都读不到：按最保守的那一类算。
			name: "读不到构建信息",
			want: Build{Version: "dev", Source: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldRel, oldVer := released, Version
			defer func() { released, Version = oldRel, oldVer }()
			released, Version = c.released, c.version
			if got := classify(c.mainVer, c.settings); got != c.want {
				t.Errorf("classify(%q) = %+v，想要 %+v", c.mainVer, got, c.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	Version = "0.3.0"
	if got := Summary(); got != "Pier 0.3.0" {
		t.Errorf("Summary() = %q", got)
	}
	Version = ""
	if got := Summary(); got != "Pier dev（本机构建，无版本号）" {
		t.Errorf("Summary() = %q", got)
	}
}
