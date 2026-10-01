package manage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sdkEnv 把数据目录指到临时目录。
//
// 「SDK 管理」这一摊读写的是 ~/.pier/settings.json，而它属于用户：不指走的话，
// 跑一次测试就会往真实的偏好文件里塞几条假 SDK，或者更糟——把用户自己加的删掉。
func sdkEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())
}

// fakeSDK 造一个 toolchain.Inspect 认得的 SDK：一个目录，里面有可执行的 bin/<名字>。
//
// 手动添加是真的去 stat 那个可执行文件、真的跑一次 --version，所以测试必须把文件
// 造出来，光给一个路径字符串是不行的——那样测的就不是同一条路了。
func fakeSDK(t *testing.T, name, version string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"" + version + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func itemOf(t *testing.T, out *SDKListOut, kind, path string) *SDKItemOut {
	t.Helper()
	for i := range out.Kinds {
		if out.Kinds[i].Kind != kind {
			continue
		}
		for j := range out.Kinds[i].Items {
			if out.Kinds[i].Items[j].Path == path {
				return &out.Kinds[i].Items[j]
			}
		}
	}
	return nil
}

// 手动添加要先真的校验一遍。不校验的话，记进去的只是一个选不动的选项，
// 而错误要到启动服务时才浮出来——那时已经离「刚加了什么」很远了。
func TestSDKAddRejectsSomethingThatIsNotAnSDK(t *testing.T) {
	sdkEnv(t)
	m := New(nil)

	empty := t.TempDir()
	if _, err := m.SDKAdd("python", empty); err == nil {
		t.Fatal("一个空目录被当成 Python 收下了")
	}
	if _, err := m.SDKAdd("python", ""); err == nil {
		t.Fatal("空路径被收下了")
	}
	if _, err := m.SDKAdd("java", empty); err == nil {
		t.Fatal("一个空目录被当成 JDK 收下了")
	}
	// 认不出的类别要在落盘之前就拒绝，否则设置里会多出一个谁也读不懂的键。
	if _, err := m.SDKAdd("ruby", empty); err == nil {
		t.Fatal("不认识的类别被收下了")
	}

	out, err := m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	if itemOf(t, out, "python", empty) != nil {
		t.Error("校验失败的目录还是被记了下来")
	}
}

func TestSDKAddThenRemove(t *testing.T) {
	sdkEnv(t)
	m := New(nil)
	py := fakeSDK(t, "python3", "Python 3.12.10")

	add, err := m.SDKAdd("python", py)
	if err != nil {
		t.Fatal(err)
	}
	if add.Item == nil || add.Item.Path != py {
		t.Fatalf("添加后要回传那一条（界面靠它把新加的直接选中）：%+v", add.Item)
	}
	if !add.Item.Manual {
		t.Error("手动添加的那一条必须标成 Manual，否则界面上删不掉")
	}
	if !strings.Contains(add.Msg, "3.12.10") {
		t.Errorf("提示里应当带上读出来的版本：%q", add.Msg)
	}

	out, err := m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	it := itemOf(t, out, "python", py)
	if it == nil {
		t.Fatal("刚添加的 SDK 没有出现在清单里")
	}
	if !it.Manual {
		t.Error("列出来的这一条也必须是 Manual")
	}
	if it.Version != "3.12.10" {
		t.Errorf("版本读错了：%q", it.Version)
	}

	// 同一条再加一次不该出现两遍。
	if _, err := m.SDKAdd("python", py); err != nil {
		t.Fatal(err)
	}
	out, err = m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range out.Kinds {
		if k.Kind != "python" {
			continue
		}
		for _, s := range k.Items {
			if s.Path == py {
				n++
			}
		}
	}
	if n != 1 {
		t.Errorf("同一条加了两次，清单里出现了 %d 条", n)
	}

	if _, err := m.SDKRemove("python", py); err != nil {
		t.Fatal(err)
	}
	out, err = m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	if itemOf(t, out, "python", py) != nil {
		t.Error("删掉之后它还在清单里")
	}
}

// 全局默认也要先校验：一个打错的路径会让这一类每次解析都失败，
// 而失败信息要到启动服务时才看得到。
func TestSDKDefaultValidatesAndClears(t *testing.T) {
	sdkEnv(t)
	m := New(nil)
	py := fakeSDK(t, "python3", "Python 3.12.10")

	if _, err := m.SDKDefault("python", "/nope/not/here"); err == nil {
		t.Fatal("指向一个不存在目录的默认值被收下了")
	}
	if _, err := m.SDKDefault("bogus", py); err == nil {
		t.Fatal("不认识的类别被收下了")
	}

	if _, err := m.SDKDefault("python", py); err != nil {
		t.Fatal(err)
	}
	out, err := m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range out.Kinds {
		if k.Kind == "python" && k.Default != py {
			t.Errorf("默认值没设上：%q", k.Default)
		}
	}

	if _, err := m.SDKDefault("python", ""); err != nil {
		t.Fatal(err)
	}
	out, err = m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range out.Kinds {
		if k.Kind == "python" && k.Default != "" {
			t.Errorf("清空之后默认值还在：%q", k.Default)
		}
	}
}

// 删掉一条手动添加的 SDK 时，指着它的那个全局默认必须一起清掉：
// 留一个指向已删路径的默认值，只会让这一类每次解析都失败。
func TestSDKRemoveClearsDefaultPointingAtIt(t *testing.T) {
	sdkEnv(t)
	m := New(nil)
	py := fakeSDK(t, "python3", "Python 3.12.10")
	if _, err := m.SDKAdd("python", py); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKDefault("python", py); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SDKRemove("python", py); err != nil {
		t.Fatal(err)
	}

	out, err := m.SDKList()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range out.Kinds {
		if k.Kind == "python" && k.Default != "" {
			t.Errorf("默认值还指着已经删掉的 %q", k.Default)
		}
	}
}

// 「将使用 X，依据 Y」必须和真正启动时走同一个解析器。
//
// 这条测试就是冲这一点来的：如果哪天后端给表单另写一份简化版的挑选逻辑，
// 服务上钉住的那一套就会在这条上对不上——而那正是表单存在的意义。
func TestToolchainResolvesTheSameWayAStartWould(t *testing.T) {
	sdkEnv(t)
	h := newHarness(t)
	py := fakeSDK(t, "python3", "Python 3.12.10")
	proj := t.TempDir()

	out, err := h.m.Toolchain(ServiceIn{
		Name: "sim", Dir: proj, Kind: "python", Run: "true",
		Toolchain: map[string]string{"python": py},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("解析失败：%s", out.Msg)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("python 服务应当只解析出一套 SDK，实际 %d 套：%+v", len(out.Tools), out.Tools)
	}
	tool := out.Tools[0]
	// 比的是 Bin 而不是 Home：node 与 python 的 Home 是「要前置到 PATH 的那个
	// 目录」（toolchain.Tool 的语义，见 tool()），而 Bin 才是这次真正会跑的那个
	// 可执行文件——钉住的那一套到底有没有被用上，看它最直接。
	if tool.Kind != "python" || tool.Bin != filepath.Join(py, "bin", "python3") {
		t.Errorf("钉住的 SDK 没被用上：%+v", tool)
	}
	if !strings.Contains(tool.Reason, "服务上指定") {
		t.Errorf("依据应当写明是服务上指定的：%q", tool.Reason)
	}

	// 表单上还没填目录时要有话说，而不是回一个空结果——那会让「将使用」那一行
	// 在最长的一段时间里（填目录之前）什么都不显示。
	if _, err := h.m.Toolchain(ServiceIn{Name: "sim", Kind: "python", Run: "true"}); err == nil {
		t.Error("目录为空时应当报错")
	}
}
