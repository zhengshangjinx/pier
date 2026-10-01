package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 从 YAML 导入：覆盖文件里的改动生效、隐藏的不导入、相对目录照样指向原处，
// 原文件一个字节不动；数据文件已经在时不再导入。
func TestEnsureStoreImportsYAML(t *testing.T) {
	ws := t.TempDir()
	base := filepath.Join(ws, DefaultConfigName)
	if err := os.WriteFile(base, []byte(baseYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := "groups: [空组]\nhidden: [gamma]\nservices:\n  - name: beta\n    dir: b\n    kind: node\n    group: 组一\n    port: 2002\n"
	if err := os.WriteFile(filepath.Join(ws, OverlayName), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(base)

	store := filepath.Join(t.TempDir(), StoreName)
	from, err := EnsureStore(store, []string{"", filepath.Join(ws, "nope.yaml"), base})
	if err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	if from != base {
		t.Errorf("导入来源 = %q，想要 %q", from, base)
	}

	c, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Names(), ","); got != "alpha,beta" {
		t.Errorf("导入的服务 = %s，想要 alpha,beta（gamma 是隐藏的，不导入）", got)
	}
	if b, _ := c.Find("beta"); b == nil || b.Port != 2002 {
		t.Error("覆盖文件里的改动没有带进来")
	}
	if a, _ := c.Find("alpha"); a == nil || a.AbsDir() != filepath.Join(ws, "a") {
		t.Error("相对目录没有按原清单所在目录解析")
	}
	if !contains(c.AllGroups(), "空组") {
		t.Error("空分组丢了")
	}
	if after, _ := os.ReadFile(base); string(after) != string(before) {
		t.Error("YAML 被改动了，它必须保持原样")
	}

	if from, err := EnsureStore(store, []string{base}); err != nil || from != "" {
		t.Errorf("数据文件已存在时不该再导入：from=%q err=%v", from, err)
	}
}

// 没给 YAML 就建一个空的；空的也能正常加载。
func TestEnsureStoreCreatesEmptyStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, nil); err != nil {
		t.Fatal(err)
	}
	c, err := LoadStore(store)
	if err != nil {
		t.Fatalf("空数据文件应当能加载：%v", err)
	}
	if len(c.Services) != 0 || !c.IsStore() {
		t.Errorf("想要一个空的、可写的清单，实际 %d 个服务", len(c.Services))
	}
}

// YAML 坏了必须报出来，而不是悄悄建一个空的——那样用户会以为服务全丢了。
func TestEnsureStoreRefusesBrokenYAML(t *testing.T) {
	ws := t.TempDir()
	base := filepath.Join(ws, DefaultConfigName)
	if err := os.WriteFile(base, []byte("services: [oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, []string{base}); err == nil {
		t.Fatal("YAML 解析失败时应当报错")
	}
	if _, err := os.Stat(store); err == nil {
		t.Error("导入失败时不该留下数据文件，否则下次启动就不会再尝试导入")
	}
}

// 编辑后写回、再读出来要一致；分组改名与删除只动服务的 group 字段。
func TestStoreEditRoundTrip(t *testing.T) {
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, nil); err != nil {
		t.Fatal(err)
	}
	c, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	c.AddGroup("前端")
	c.Upsert(&Service{Name: "web", Dir: "/tmp/web", Kind: KindNode, Group: "前端", Port: 3000,
		Env: map[string]string{"A": "1"}})
	c.Upsert(&Service{Name: "api", Dir: "/tmp/api", Kind: KindGo, Group: "前端"})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c, _ = LoadStore(store)
	if n := c.RenameGroup("前端", "Web"); n != 2 {
		t.Errorf("改名应当带走 2 个服务，实际 %d", n)
	}
	if n := c.RemoveGroup("Web"); n != 2 {
		t.Errorf("删分组应当移出 2 个服务，实际 %d", n)
	}
	if !c.Remove("api") {
		t.Error("删除服务失败")
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	c, _ = LoadStore(store)
	web, err := c.Find("web")
	if err != nil {
		t.Fatal(err)
	}
	if web.Group != "" || web.Port != 3000 || web.Env["A"] != "1" {
		t.Errorf("写回后字段不对：%+v", web)
	}
	if contains(c.AllGroups(), "Web") || contains(c.AllGroups(), "前端") {
		t.Errorf("删掉的分组还在：%v", c.AllGroups())
	}
	// 临时文件不能残留在数据目录里。
	entries, _ := os.ReadDir(filepath.Dir(store))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("数据目录里残留了临时文件 %s", e.Name())
		}
	}
}

// 拖动排序只动给出的那几个名字占着的位置，别处一个都不许动。
//
// 这是「在分组页 / 筛选之后拖动」那个场景的地基：界面手里只有看得见的那几行，
// 如果后端按整表替换来理解，看不见的服务会被挤成一堆——用户只挪了一行，
// 回来的却是另一个列表，而且两个页面的顺序从此对不上。
func TestReorderServicesOnlyTouchesGivenSlots(t *testing.T) {
	c := newStore(filepath.Join(t.TempDir(), StoreName), "/tmp")
	for _, n := range []string{"a1", "b1", "a2", "b2", "a3"} {
		c.Upsert(&Service{Name: n, Dir: "/tmp/" + n})
	}
	// 只看 a 组那一页：可见的是 a1、a2、a3，把它们倒过来。
	if err := c.ReorderServices([]string{"a3", "a2", "a1"}); err != nil {
		t.Fatalf("排序失败：%v", err)
	}
	want := "a3,b1,a2,b2,a1"
	if got := strings.Join(c.Names(), ","); got != want {
		t.Errorf("顺序 = %s，想要 %s（b1、b2 必须待在原处）", got, want)
	}
}

// 名字对不上时整条拒绝，而不是照着一份过期的名单乱填。
func TestReorderServicesRejectsBadNames(t *testing.T) {
	c := newStore(filepath.Join(t.TempDir(), StoreName), "/tmp")
	for _, n := range []string{"one", "two"} {
		c.Upsert(&Service{Name: n, Dir: "/tmp/" + n})
	}
	for _, in := range [][]string{
		{},              // 什么都没给
		{"one", "nope"}, // 有个不存在的
		{"one", "one"},  // 重名
	} {
		if err := c.ReorderServices(in); err == nil {
			t.Errorf("%v 应当被拒绝", in)
		}
	}
	if got := strings.Join(c.Names(), ","); got != "one,two" {
		t.Errorf("拒绝之后顺序不该变，实际 %s", got)
	}
}

// 拖动分组：没声明过的分组要先补写进声明里，否则拖了不生效
// （AllGroups 每次都把它们接在末尾，位置根本存不下来）。
func TestReorderGroupsDeclaresThenMoves(t *testing.T) {
	c := newStore(filepath.Join(t.TempDir(), StoreName), "/tmp")
	c.AddGroup("前端")
	c.Upsert(&Service{Name: "web", Dir: "/tmp/web", Group: "前端"})
	c.Upsert(&Service{Name: "api", Dir: "/tmp/api", Group: "后端"})
	c.Upsert(&Service{Name: "job", Dir: "/tmp/job", Group: "定时"})
	if got := strings.Join(c.AllGroups(), ","); got != "前端,后端,定时" {
		t.Fatalf("起始顺序 = %s", got)
	}

	// 把「定时」拖到最前面。
	if err := c.ReorderGroups([]string{"定时", "前端", "后端"}); err != nil {
		t.Fatalf("排序失败：%v", err)
	}
	if got := strings.Join(c.AllGroups(), ","); got != "定时,前端,后端" {
		t.Errorf("顺序 = %s，想要 定时,前端,后端", got)
	}
	// 后端、定时原先只是「服务里有」的分组，现在应当已经写进声明里。
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := LoadStore(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(back.AllGroups(), ","); got != "定时,前端,后端" {
		t.Errorf("写回再读出来 = %s，顺序必须存得住", got)
	}
}

// 「未分组」是内置的桶，位置固定，拖不动也不能被写进声明。
func TestReorderGroupsRefusesUngrouped(t *testing.T) {
	c := newStore(filepath.Join(t.TempDir(), StoreName), "/tmp")
	c.Upsert(&Service{Name: "web", Dir: "/tmp/web"})
	if err := c.ReorderGroups([]string{UngroupedName}); err == nil {
		t.Error("「未分组」应当被拒绝")
	}
	if len(c.DeclaredGroups) != 0 {
		t.Errorf("被拒绝的分组不该被写进声明：%v", c.DeclaredGroups)
	}
}

// 改名是原地换个名字，位置、分组、端口都留着。
func TestRenameServiceKeepsItsPlace(t *testing.T) {
	c := newStore(filepath.Join(t.TempDir(), StoreName), "/tmp")
	for _, n := range []string{"one", "two", "three"} {
		c.Upsert(&Service{Name: n, Dir: "/tmp/" + n, Port: 3000, Group: "组"})
	}
	if err := c.RenameService("two", "second"); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	if got := strings.Join(c.Names(), ","); got != "one,second,three" {
		t.Errorf("顺序 = %s，改名不该换位置", got)
	}
	if s, _ := c.Find("second"); s == nil || s.Port != 3000 || s.Group != "组" {
		t.Errorf("改名后字段丢了：%+v", s)
	}
	if err := c.RenameService("one", "second"); err == nil {
		t.Error("改成已有的名字应当报错")
	}
	if err := c.RenameService("nope", "x"); err == nil {
		t.Error("改一个不存在的服务应当报错")
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// 数据文件只存绝对路径、不再有 root 这个隐含前提；相对路径进来会被就地转成绝对路径。
func TestStoreKeepsOnlyAbsoluteDirs(t *testing.T) {
	ws := t.TempDir()
	base := filepath.Join(ws, DefaultConfigName)
	if err := os.WriteFile(base, []byte(baseYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, []string{base}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(store)
	if strings.Contains(string(raw), `"root"`) {
		t.Errorf("数据文件里不该再有 root：\n%s", raw)
	}
	c, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.Services {
		if !filepath.IsAbs(s.Dir) {
			t.Errorf("%s 的目录不是绝对路径：%q", s.Name, s.Dir)
		}
	}
	if fi, _ := os.Stat(store); fi.Mode().Perm() != 0o644 {
		t.Errorf("数据文件权限 = %v，想要 0644（与目录里其余文件一致）", fi.Mode().Perm())
	}
}

// 「复制成 YAML」的那一段要能原样贴回清单里——这就是它唯一的存在理由。
//
// 所以这里不比对字符串，而是真把它当清单的一条读回来：少写一个字段、
// 或者把不该外出的字段（Origin 是「从哪份文件来的」，只在本次加载里有意义）
// 写出去，都会在这条上露出来。
func TestServiceYAMLPastesBackIntoManifest(t *testing.T) {
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, []string{writeManifest(t, baseYAML, "")}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := c.Find("alpha")
	if err != nil {
		t.Fatal(err)
	}
	text, err := ServiceYAML(alpha)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "origin") {
		t.Errorf("Origin 不该跟着走：\n%s", text)
	}

	// 片段是「一条服务」，贴进清单要变成 services 下面的一项：
	// 第一行接在「- 」后面，其余各行按缩进对齐。
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var doc strings.Builder
	doc.WriteString("services:\n")
	for i, ln := range lines {
		if i == 0 {
			doc.WriteString("  - " + ln + "\n")
		} else {
			doc.WriteString("    " + ln + "\n")
		}
	}
	back, err := Load(writeManifest(t, doc.String(), ""))
	if err != nil {
		t.Fatalf("贴回去的片段读不了：%v\n%s", err, doc.String())
	}
	got, err := back.Find("alpha")
	if err != nil {
		t.Fatalf("贴回去之后找不到 alpha：%v", err)
	}
	if got.Port != alpha.Port || got.Kind != alpha.Kind || got.Group != alpha.Group || got.AbsDir() != alpha.AbsDir() {
		t.Errorf("贴回来的这条和原来不一样：\n原来 %+v\n回来 %+v", alpha, got)
	}
}

// 导出整份清单也走同一条路：导出的文件必须能直接被 Load 读回来。
//
// 这里最容易踩的是 groups：基础清单里根本没有这个键（它是覆盖文件才有的），
// 而 Load 用 KnownFields(true) 读，导出里多写一行 groups 就是一句
// 「field groups not found」——拿到这份文件的人在那边看到的是清单加载失败。
func TestExportYAMLRoundTrips(t *testing.T) {
	store := filepath.Join(t.TempDir(), StoreName)
	if _, err := EnsureStore(store, []string{writeManifest(t, baseYAML, "")}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadStore(store)
	if err != nil {
		t.Fatal(err)
	}
	text, err := c.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "groups:") {
		t.Errorf("导出里不该有 groups：\n%s", text)
	}
	out := writeManifest(t, text, "")
	back, err := Load(out)
	if err != nil {
		t.Fatalf("导出的清单读不回来：%v\n%s", err, text)
	}
	if got := strings.Join(back.Names(), ","); got != "alpha,beta,gamma" {
		t.Errorf("读回来的服务 = %s，想要 alpha,beta,gamma", got)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		want, _ := c.Find(name)
		got, err := back.Find(name)
		if err != nil {
			t.Errorf("%s 没带过去：%v", name, err)
			continue
		}
		if got.Port != want.Port || got.AbsDir() != want.AbsDir() {
			t.Errorf("%s 变了：端口 %d→%d，目录 %s→%s", name, want.Port, got.Port, want.AbsDir(), got.AbsDir())
		}
	}
}

// 数据目录默认在 ~/.pier，日志与编译产物都在它下面。
func TestDirsLiveUnderOneRoot(t *testing.T) {
	t.Setenv("PIER_HOME", "")
	d, err := Dirs()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if d.Data != filepath.Join(home, ".pier") {
		t.Errorf("数据目录 = %s，想要 ~/.pier", d.Data)
	}
	for _, sub := range []string{d.Logs, d.Cache} {
		if !strings.HasPrefix(sub, d.Data+string(filepath.Separator)) {
			t.Errorf("%s 不在数据目录 %s 之下", sub, d.Data)
		}
	}
}
