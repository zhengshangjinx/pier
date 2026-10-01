package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifest 在临时目录里铺一份清单与可选的覆盖文件，返回清单路径。
func writeManifest(t *testing.T, base string, overlay string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigName)
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	if overlay != "" {
		if err := os.WriteFile(filepath.Join(dir, OverlayName), []byte(overlay), 0o644); err != nil {
			t.Fatalf("写覆盖文件失败：%v", err)
		}
	}
	return path
}

const baseYAML = `
services:
  - name: alpha
    dir: a
    kind: go
    group: 组一
    port: 1001
  - name: beta
    dir: b
    kind: node
    group: 组一
    port: 1002
  - name: gamma
    dir: c
    kind: node
`

func names(c *Config) []string { return c.Names() }

func TestLoadWithoutOverlay(t *testing.T) {
	c, err := Load(writeManifest(t, baseYAML, ""))
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := names(c); strings.Join(got, ",") != "alpha,beta,gamma" {
		t.Errorf("服务顺序 = %v", got)
	}
	for _, s := range c.Services {
		if s.Origin != OriginBase {
			t.Errorf("%s 的来源应为 base，实际 %s", s.Name, s.Origin)
		}
	}
}

func TestOverlayAppendsAndOverridesInPlace(t *testing.T) {
	overlay := `
services:
  - name: beta
    dir: b2
    kind: node
    group: 组二
    port: 2002
  - name: delta
    dir: d
    kind: go
    group: 组二
`
	c, err := Load(writeManifest(t, baseYAML, overlay))
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}

	// beta 被覆盖但留在原位，delta 追加到末尾 —— 覆盖不该打乱手写清单的顺序。
	if got := names(c); strings.Join(got, ",") != "alpha,beta,gamma,delta" {
		t.Fatalf("服务顺序 = %v", got)
	}

	beta, err := c.Find("beta")
	if err != nil {
		t.Fatal(err)
	}
	if beta.Dir != "b2" || beta.Port != 2002 {
		t.Errorf("beta 没被覆盖：dir=%s port=%d", beta.Dir, beta.Port)
	}
	if beta.Origin != OriginOverlay {
		t.Errorf("被覆盖的 beta 来源应为 overlay，实际 %s", beta.Origin)
	}
	if !beta.IsOverlay() {
		t.Error("被覆盖的服务应当可以真正删除")
	}

	alpha, _ := c.Find("alpha")
	if alpha.Origin != OriginBase || alpha.IsOverlay() {
		t.Error("没被覆盖的 alpha 应当仍是只读的 base 来源")
	}

	// 覆盖里的 dir 也相对清单目录解析，和被覆盖的服务保持同一套规则。
	delta, _ := c.Find("delta")
	if want := filepath.Join(filepath.Dir(c.Path), "d"); delta.AbsDir() != want {
		t.Errorf("delta.AbsDir = %s，期望 %s", delta.AbsDir(), want)
	}
}

func TestOverlayHides(t *testing.T) {
	overlay := "hidden:\n  - gamma\n"
	c, err := Load(writeManifest(t, baseYAML, overlay))
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if got := names(c); strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("隐藏后仍列出 %v", got)
	}
	if _, err := c.Find("gamma"); err == nil {
		t.Error("被隐藏的服务不该还能按名字找到")
	}
	if len(c.Hidden) != 1 || c.Hidden[0] != "gamma" {
		t.Errorf("Hidden = %v，界面要靠它列出可恢复的项", c.Hidden)
	}
}

// 覆盖文件里同一个名字写两遍，合并时只有最后一条生效，另一条会无声消失。
// 那意味着界面上怎么改都改不动某个服务且毫无提示，必须在读进来时就报错。
func TestOverlayDuplicateNameRejected(t *testing.T) {
	overlay := `
services:
  - name: dup
    dir: x
    kind: go
  - name: dup
    dir: y
    kind: go
`
	_, err := Load(writeManifest(t, baseYAML, overlay))
	if err == nil {
		t.Fatal("重名应当报错")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Errorf("错误信息没说明是重名：%v", err)
	}
}

// 拼错的键名必须报错。覆盖文件是给人手写的，静默忽略一个写错的键
// 会让人以为配置生效了。
func TestOverlayUnknownKeyRejected(t *testing.T) {
	overlay := "services:\n  - name: x\n    dir: x\n    kindX: go\n"
	if _, err := Load(writeManifest(t, baseYAML, overlay)); err == nil {
		t.Fatal("未知键名应当报错")
	}
}

// 覆盖文件损坏时，错误必须指向覆盖文件，而不是让人以为手写的清单坏了。
func TestOverlayErrorNamesTheOverlayFile(t *testing.T) {
	_, err := Load(writeManifest(t, baseYAML, "services: [ this is not valid"))
	if err == nil {
		t.Fatal("坏文件应当报错")
	}
	if !strings.Contains(err.Error(), OverlayName) {
		t.Errorf("错误信息没指明是覆盖文件：%v", err)
	}
}

func TestGroupNameAndGroups(t *testing.T) {
	c, err := Load(writeManifest(t, baseYAML, ""))
	if err != nil {
		t.Fatal(err)
	}
	// gamma 没写 group，归入未分组，且分组顺序按服务出现顺序。
	if got := strings.Join(c.Groups(), ","); got != "组一,未分组" {
		t.Errorf("Groups = %v", got)
	}
	if n := c.CountInGroup("组一"); n != 2 {
		t.Errorf("组一 = %d 个，期望 2", n)
	}
	if n := c.CountInGroup(UngroupedName); n != 1 {
		t.Errorf("未分组 = %d 个，期望 1", n)
	}
}

// 界面里的写入全部走 Overlay.Save，落盘后必须能被原样读回来。
func TestOverlaySaveLoadRoundTrip(t *testing.T) {
	c, err := Load(writeManifest(t, baseYAML, ""))
	if err != nil {
		t.Fatal(err)
	}
	o, err := LoadOverlay(c.OverlayPath)
	if err != nil {
		t.Fatal(err)
	}
	o.Upsert(&Service{Name: "added", Dir: "z", Kind: KindGo, Group: "组三", Note: "备注"})
	o.Hide("beta")
	if err := o.Save(c.OverlayPath); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	// 重新加载，确认覆盖真的生效。
	c2, err := Load(c.Path)
	if err != nil {
		t.Fatalf("重新加载失败：%v", err)
	}
	if got := names(c2); strings.Join(got, ",") != "alpha,gamma,added" {
		t.Errorf("服务 = %v", got)
	}
	added, err := c2.Find("added")
	if err != nil {
		t.Fatal(err)
	}
	if added.Note != "备注" || added.Group != "组三" {
		t.Errorf("备注或分组没保住：%+v", added)
	}

	// 再删掉它，确认删除是彻底的。
	o2, _ := LoadOverlay(c2.OverlayPath)
	if !o2.Remove("added") {
		t.Fatal("Remove 应当返回 true")
	}
	o2.Unhide("beta")
	if err := o2.Save(c2.OverlayPath); err != nil {
		t.Fatal(err)
	}
	c3, err := Load(c2.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(c3); strings.Join(got, ",") != "alpha,beta,gamma" {
		t.Errorf("删除与取消隐藏后 = %v", got)
	}
}
