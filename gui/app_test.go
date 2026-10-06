package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/diag"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/panel"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/toolchain"
	"github.com/zhengshangjinx/pier/internal/update"
)

// 这些绑定是图形界面的全部入口。窗口里的按钮点不动时，问题多半出在这一层，
// 所以它们必须能在不开窗口的情况下被验证。
//
// 测试刻意只碰只读与校验路径：真正启停服务会改工作空间、占端口、写构建产物，
// 不是一个单元测试该干的事——那部分由命令行侧的端到端验证覆盖。

// findManifest 返回测试用的 YAML 清单。放在仓库里而不是去读工作空间里的真实清单：
// 真实清单改名、搬走或者根本不存在时，依赖它的测试会整批跳过——跳过看起来和通过一模一样。
func findManifest(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "pier.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testApp 把测试清单**导入**到临时目录里的数据文件，再用它建一个 app：
// 数据文件、进程状态、日志都落在临时目录，测试不会碰到用户的任何真实数据。
func testApp(t *testing.T) *app {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)

	store := filepath.Join(home, config.StoreName)
	if _, err := config.EnsureStore(store, []string{findManifest(t)}); err != nil {
		t.Fatalf("导入清单失败：%v", err)
	}
	return newApp(store)
}

func decode(t *testing.T, raw string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		t.Fatalf("返回的不是合法 JSON：%v\n原文：%s", err, raw)
	}
}

// TestPortOwnerReportsEveryField 走完整条链路：真开一个监听 → portOwner 绑定 → JSON。
//
// 这是针对「端口占用详情里进程名、启动于、命令行整排是 —」那个故障的护栏。
// 那几栏不是装饰：用户就是靠它们判断占着端口的是自己刚才在别的终端起的服务，
// 还是一个毫不相干的程序。全是「—」等于这个功能没做。
//
// 用自造的清单而不是真实清单，是为了把端口攥在自己手里：
// 真实清单的端口上跑着什么完全取决于用户当时开了哪些服务，测试会时灵时不灵。
func TestPortOwnerReportsEveryField(t *testing.T) {
	if err := proc.PortToolsAvailable(); err != nil {
		t.Skipf("本机查不了端口占用，这条功能整体不可用：%v", err)
	}

	// 先要一个空闲端口，再把它写进清单——反过来（先定死端口再祈祷它空着）会在
	// 端口被占时表现为「测试失败」，而真正的原因只是撞车。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("开监听失败：%v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	manifest := filepath.Join(dir, "pier.yaml")
	body := fmt.Sprintf(`services:
  - name: probe
    dir: %s
    kind: go
    group: 测试
    port: %d
`, dir, port)
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时清单失败：%v", err)
	}

	var out manage.PortOwnerOut
	decode(t, newApp(manifest).portOwner("probe"), &out)

	if !out.OK {
		t.Fatalf("查端口占用失败：%s", out.Msg)
	}
	if out.Port != port {
		t.Errorf("Port = %d，期望 %d", out.Port, port)
	}
	if out.Owner == nil {
		t.Fatal("Owner 为空，界面上就是一排「—」")
	}
	// 监听是本测试进程开的，所以这几项都有确定答案，可以逐个较真。
	if out.Owner.PID != os.Getpid() {
		t.Errorf("PID = %d，期望 %d（监听就是本进程开的）", out.Owner.PID, os.Getpid())
	}
	for _, f := range []struct {
		name string
		got  string
	}{
		{"Command（进程名）", out.Owner.Command},
		{"User（属主）", out.Owner.User},
		{"Started（启动于）", out.Owner.Started},
		{"Args（命令行）", out.Owner.Args},
	} {
		if strings.TrimSpace(f.got) == "" {
			t.Errorf("%s 为空，界面上会显示成「—」", f.name)
		}
	}
	// 自己开的监听不该被认成 Pier 的服务，否则界面会把「结束进程」换成「停止服务」，
	// 而那个服务其实不存在。
	if out.Owner.Managed {
		t.Errorf("Managed 为真，但 %d 不是 Pier 启动的服务", out.Owner.PID)
	}
}

func TestStateListsEveryService(t *testing.T) {
	a := testApp(t)

	var st panel.StateOut
	decode(t, a.state(), &st)

	if !st.OK {
		t.Fatalf("state() 应当成功，却报错：%s", st.Error)
	}
	if st.ConfigPath == "" {
		t.Error("configPath 为空，界面顶栏会显示不出当前清单")
	}
	if len(st.Services) == 0 {
		t.Fatal("一个服务都没有，界面会是空的")
	}

	valid := map[string]bool{
		"running": true, "starting": true, "external": true, "stale": true, "stopped": true,
	}
	for _, s := range st.Services {
		if s.Name == "" || s.StatusText == "" || s.StatusKey == "" {
			t.Errorf("服务字段不完整：%+v", s)
		}
		if !valid[s.StatusKey] {
			t.Errorf("%s 的状态键 %q 不在约定集合里，界面会认不出它", s.Name, s.StatusKey)
		}
		if s.LogPath == "" {
			t.Errorf("%s 没有 logPath，日志按钮会失效", s.Name)
		}
		if s.Kind == "" {
			t.Errorf("%s 没有 kind，界面上的类型标签会是空的", s.Name)
		}
	}
}

// 状态键与文案必须一一对应。二者分岔就会出现「写着运行中、涂着红色」这类界面。
func TestStatusKeyMatchesText(t *testing.T) {
	a := testApp(t)

	var st panel.StateOut
	decode(t, a.state(), &st)
	for _, s := range st.Services {
		switch s.StatusKey {
		case "stopped":
			if s.StatusText != "未启动" {
				t.Errorf("%s：键为 stopped 却显示 %q", s.Name, s.StatusText)
			}
		case "running":
			if s.StatusText != "运行中" {
				t.Errorf("%s：键为 running 却显示 %q", s.Name, s.StatusText)
			}
		case "external":
			if s.StatusText != "外部运行" {
				t.Errorf("%s：键为 external 却显示 %q", s.Name, s.StatusText)
			}
		case "stale":
			if s.StatusText != "已退出" {
				t.Errorf("%s：键为 stale 却显示 %q", s.Name, s.StatusText)
			}
		}
	}
}

// 界面把服务名直接交给后端，后端必须挡住不存在的名字，而不是让队列炸掉。
func TestUnknownServiceIsRejected(t *testing.T) {
	a := testApp(t)

	cases := []struct {
		name string
		call func() string
	}{
		{"start", func() string { return a.start("根本没有这个服务") }},
		{"stop", func() string { return a.stop("根本没有这个服务") }},
		{"restart", func() string { return a.restart("根本没有这个服务") }},
		{"logs", func() string { return a.logs("根本没有这个服务", "", 0) }},
		{"reveal", func() string { return a.reveal("根本没有这个服务") }},
		{"openHealth", func() string { return a.openHealth("根本没有这个服务") }},
	}
	for _, c := range cases {
		var out msgOut
		decode(t, c.call(), &out)
		if out.OK {
			t.Errorf("%s 对不存在的服务应当失败", c.name)
		}
		if out.Msg == "" {
			t.Errorf("%s 失败了却没给出原因，界面只能显示一个空提示", c.name)
		}
	}
}

// reveal/openHealth 只接受服务名，路径一律由配置推导。
// 这条测试同时确认「不存在的名字不会触发任何系统调用」——即不会弹出访达。
func TestNoSystemCallForUnknownService(t *testing.T) {
	a := testApp(t)

	for _, name := range []string{"", "..", "/etc/passwd", "../../etc"} {
		var out msgOut
		decode(t, a.reveal(name), &out)
		if out.OK {
			t.Errorf("reveal(%q) 竟然成功了——路径不该由界面提供", name)
		}
	}
}

// 不带参数启动就用数据目录里的数据文件，不存在就建一个空的；空的也要能正常显示。
func TestFreshStartCreatesEmptyStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)

	var st panel.StateOut
	decode(t, newApp("").state(), &st)
	if !st.OK {
		t.Fatalf("全新启动没能加载：%s", st.Error)
	}
	if len(st.Services) != 0 || st.ReadOnly {
		t.Errorf("想要一份空的、可写的数据，实际 %d 个服务、只读=%v", len(st.Services), st.ReadOnly)
	}
	if filepath.Dir(st.ConfigPath) != home || st.ConfigSrc != "本机数据" {
		t.Errorf("数据文件应当在 %s 下，实际 %s（来源 %q）", home, st.ConfigPath, st.ConfigSrc)
	}
}

// 命令行指定的 YAML 清单是只读的：能看能启停，但改不了，界面据此收起编辑入口。
func TestYAMLConfigIsReadOnly(t *testing.T) {
	src := findManifest(t)
	raw, _ := os.ReadFile(src)
	manifest := filepath.Join(t.TempDir(), config.DefaultConfigName)
	if err := os.WriteFile(manifest, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	a := newApp(manifest)

	var st panel.StateOut
	decode(t, a.state(), &st)
	if !st.ReadOnly {
		t.Error("YAML 清单应当标成只读")
	}
	var out msgOut
	decode(t, a.createGroup("随便"), &out)
	if out.OK {
		t.Error("YAML 清单不该能被界面修改")
	}
	if now, _ := os.ReadFile(manifest); string(now) != string(raw) {
		t.Error("YAML 清单被改动了")
	}
}

// ── 端口候选 ────────────────────────────────────────────────────────────────

// 候选端口必须真的能用：不能撞上清单里已写的端口，也不能撞上此刻在监听的端口。
// 这条是被用户报回来的问题逼出来的——「找空闲端口」如果给出一堆照样冲突的号，
// 这个功能就等于没做。
func TestPortCandidatesAreActuallyFree(t *testing.T) {
	a := testApp(t)

	var out manage.PortCandOut
	decode(t, a.portCandidates("8080"), &out)
	if !out.OK {
		t.Fatal("查询候选端口失败（失败时返回的是 msgOut，不是 portCandOut，所以这里没有错误文案可打）")
	}
	if out.From != 8080 {
		t.Errorf("起点应当是 8080，实际 %d", out.From)
	}
	if len(out.Free) != manage.PortCandCount {
		t.Errorf("应当给出 %d 个候选，实际 %d 个", manage.PortCandCount, len(out.Free))
	}

	taken := map[int]bool{}
	for _, p := range out.Taken {
		taken[p] = true
	}
	used := map[int]bool{}
	for _, p := range out.Used {
		used[p] = true
	}
	prev := 0
	for _, p := range out.Free {
		if p <= prev {
			t.Errorf("候选端口没有按从小到大排列：%d 出现在 %d 之后", p, prev)
		}
		prev = p
		if taken[p] {
			t.Errorf("端口 %d 此刻正被监听，却被当成空闲的", p)
		}
		if used[p] {
			t.Errorf("端口 %d 清单里已经写掉了，却被当成空闲的", p)
		}
	}
	for _, p := range append(append([]int{}, out.Used...), out.Taken...) {
		if p > out.ScanTo {
			t.Errorf("端口 %d 超出了声明的扫描上界 %d", p, out.ScanTo)
		}
	}
	// 扫描上界是「这一趟真正看过的最后一个端口」，不是 from+2000 那个预算。
	// 找满一批候选就收工了，报一个没看过的上界等于让界面说假话——用户会以为
	// 更靠后的端口也查过了，而那里到底有没有被占，这次根本没看。
	if out.ScanTo != out.Free[len(out.Free)-1] {
		t.Errorf("扫描上界应当是最后一个看过的端口 %d，实际 %d",
			out.Free[len(out.Free)-1], out.ScanTo)
	}
	// 两种冲突要分开报：清单里写了但没在跑的，和真被别的进程占着的，
	// 处理办法完全不同，混成一个列表界面就没法给出正确的建议。
	for p := range taken {
		if used[p] {
			t.Errorf("端口 %d 同时出现在「清单已用」和「被进程占用」里", p)
		}
	}
}

// 起点写得不像话时要退回默认值，而不是报错或者从 0 开始扫。
func TestPortCandidatesBadStartFallsBack(t *testing.T) {
	a := testApp(t)
	for _, in := range []string{"", "abc", "0", "-1", "70000", "  "} {
		var out manage.PortCandOut
		decode(t, a.portCandidates(in), &out)
		if !out.OK {
			t.Errorf("起点 %q 应当退回默认值，却被当成错误", in)
			continue
		}
		if out.From != 8080 {
			t.Errorf("起点 %q 应当退回 8080，实际 %d", in, out.From)
		}
	}
}

// ── 分组的新增 / 改名 / 删除 ────────────────────────────────────────────────

func (a *app) groupNames(t *testing.T) []string {
	t.Helper()
	var st panel.StateOut
	decode(t, a.state(), &st)
	out := make([]string, 0, len(st.Groups))
	for _, g := range st.Groups {
		out = append(out, g.Name)
	}
	return out
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// 走一遍完整的生命周期：建 → 重名要拦住 → 改名 → 删。每一步之后都重新读一次
// 状态，因为界面看到的是重载后的结果，只信返回值不算数。
func TestGroupLifecycle(t *testing.T) {
	a := testApp(t)
	const name, renamed = "临时分组", "改过名的分组"

	var out msgOut
	decode(t, a.createGroup(name), &out)
	if !out.OK {
		t.Fatalf("新建分组失败：%s", out.Msg)
	}
	if !hasString(a.groupNames(t), name) {
		t.Fatalf("新建之后状态里没有「%s」", name)
	}

	// 空分组也要留在列表里，否则用户建完就找不到它了。
	var st panel.StateOut
	decode(t, a.state(), &st)
	for _, g := range st.Groups {
		if g.Name == name && g.Count != 0 {
			t.Errorf("刚建的空分组计数应当是 0，实际 %d", g.Count)
		}
	}

	decode(t, a.createGroup(name), &out)
	if out.OK {
		t.Errorf("重名的分组应当被拦住，却报成功：%s", out.Msg)
	}

	decode(t, a.renameGroup(name, renamed), &out)
	if !out.OK {
		t.Fatalf("重命名失败：%s", out.Msg)
	}
	names := a.groupNames(t)
	if hasString(names, name) || !hasString(names, renamed) {
		t.Errorf("改名后分组列表不对：%v", names)
	}

	decode(t, a.deleteGroup(renamed), &out)
	if !out.OK {
		t.Fatalf("删除分组失败：%s", out.Msg)
	}
	if hasString(a.groupNames(t), renamed) {
		t.Errorf("删除之后「%s」还在：%v", renamed, a.groupNames(t))
	}
}

func TestGroupRejectsBadNames(t *testing.T) {
	a := testApp(t)

	var out msgOut
	decode(t, a.createGroup("   "), &out)
	if out.OK {
		t.Error("空名字应当被拒绝")
	}
	decode(t, a.createGroup(strings.Repeat("长", 25)), &out)
	if out.OK {
		t.Error("超过 24 个字的名字应当被拒绝")
	}
	decode(t, a.createGroup("带\n换行"), &out)
	if out.OK {
		t.Error("带换行的名字应当被拒绝")
	}

	// 内置的「未分组」既不能改名也不能删。
	decode(t, a.renameGroup(config.UngroupedName, "别的名字"), &out)
	if out.OK {
		t.Errorf("「%s」是内置分组，不该能改名", config.UngroupedName)
	}
	decode(t, a.deleteGroup(config.UngroupedName), &out)
	if out.OK {
		t.Errorf("「%s」是内置分组，不该能删除", config.UngroupedName)
	}
}

// 删分组不能顺手把里面的服务也删掉。这是最容易写错、也最不可接受的一种错。
func TestDeleteGroupKeepsItsServices(t *testing.T) {
	a := testApp(t)
	const gname, sname = "待删分组", "pier-测试服务"

	var out msgOut
	decode(t, a.createGroup(gname), &out)
	if !out.OK {
		t.Fatalf("新建分组失败：%s", out.Msg)
	}

	payload, err := json.Marshal(manage.ServiceIn{
		Name: sname, Dir: ".", Group: gname, Kind: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	decode(t, a.saveService(string(payload)), &out)
	if !out.OK {
		t.Fatalf("保存服务失败：%s", out.Msg)
	}

	decode(t, a.deleteGroup(gname), &out)
	if !out.OK {
		t.Fatalf("删除分组失败：%s", out.Msg)
	}

	var st panel.StateOut
	decode(t, a.state(), &st)
	var found *panel.ServiceOut
	for i := range st.Services {
		if st.Services[i].Name == sname {
			found = &st.Services[i]
		}
	}
	if found == nil {
		t.Fatalf("删分组把服务「%s」一起删掉了，这是不能接受的", sname)
	}
	// 这里刻意只断言「还在」而不写死它落到哪个分组名：
	// 分组名是后端给的常量，测试里再抄一遍等于把同一个事实记两处。
	if found.Group == gname {
		t.Errorf("分组已删除，服务「%s」却还挂在这个分组下", sname)
	}
}

// 新加的服务删掉就是删掉。
func TestDeleteServiceReallyDeletes(t *testing.T) {
	a := testApp(t)
	const sname = "pier-测试服务-临时"

	payload, _ := json.Marshal(manage.ServiceIn{Name: sname, Dir: ".", Kind: "go"})
	var out msgOut
	decode(t, a.saveService(string(payload)), &out)
	if !out.OK {
		t.Fatalf("保存服务失败：%s", out.Msg)
	}

	decode(t, a.deleteService(sname), &out)
	if !out.OK {
		t.Fatalf("删除服务失败：%s", out.Msg)
	}

	var st panel.StateOut
	decode(t, a.state(), &st)
	if hasString(serviceNames(st.Services), sname) {
		t.Errorf("删除之后「%s」还在服务列表里", sname)
	}
}

// 从 YAML 导入的服务也能真删，而且删完原来的 YAML 文件纹丝不动。
func TestDeleteImportedServiceLeavesYAMLAlone(t *testing.T) {
	a := testApp(t)
	src := findManifest(t)
	before, _ := os.ReadFile(src)

	var st panel.StateOut
	decode(t, a.state(), &st)
	if len(st.Services) == 0 {
		t.Skip("清单里没有服务，跳过")
	}
	target := st.Services[0].Name
	if !st.Services[0].Editable {
		t.Fatalf("数据文件里的服务应当可编辑")
	}

	var out msgOut
	decode(t, a.deleteService(target), &out)
	if !out.OK {
		t.Fatalf("删除失败：%s", out.Msg)
	}
	decode(t, a.state(), &st)
	if hasString(serviceNames(st.Services), target) {
		t.Errorf("「%s」删除之后还在列表里", target)
	}
	if after, _ := os.ReadFile(src); string(after) != string(before) {
		t.Error("删除服务改动了旧的 YAML 清单，它必须保持原样")
	}
}

func serviceNames(list []panel.ServiceOut) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

func TestPruneOnEmptyState(t *testing.T) {
	a := testApp(t)

	var out msgOut
	decode(t, a.prune(), &out)
	if !out.OK {
		t.Errorf("空状态上清理应当成功（无事可做），却报错：%s", out.Msg)
	}
}

// 日志尾读是界面每秒都在走的路径，截断处不能留下半行。
func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")

	var sb strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&sb, "第 %d 行内容填充填充填充\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("按行数截断", func(t *testing.T) {
		text, truncated, err := panel.TailFile(path, 10, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Error("只取 10 行却没标记为截断")
		}
		// 文件以换行结尾，正文也以换行结尾——增量跟随靠这个逐字节对齐。
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		if len(lines) != 10 {
			t.Fatalf("应当有 10 行，实际 %d 行", len(lines))
		}
		if !strings.Contains(lines[9], "第 5000 行") {
			t.Errorf("末尾不是最后一行，而是 %q", lines[9])
		}
	})

	t.Run("按字节截断且不留半行", func(t *testing.T) {
		text, truncated, err := panel.TailFile(path, 100000, 512)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Error("只读了 512 字节却没标记为截断")
		}
		for i, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			// 每一行都必须是完整的「第 N 行内容填充填充填充」，
			// 半行会以残缺的形式出现在界面上。
			if !strings.HasPrefix(l, "第 ") || !strings.HasSuffix(l, "填充填充填充") {
				t.Fatalf("第 %d 行是半行：%q", i, l)
			}
		}
	})

	t.Run("文件不存在", func(t *testing.T) {
		if _, _, err := panel.TailFile(filepath.Join(dir, "没有这个.log"), 10, 512); !os.IsNotExist(err) {
			t.Errorf("应当返回文件不存在，实际：%v", err)
		}
	})
}

// uiSources 返回界面侧的全部源码。
//
// 界面拆成了两层：ui.html 只是空壳，真正的调用都在 app.js 里。凡是「界面与后端
// 的约定」类检查都必须把两份都读进来——只读 ui.html 的话，一条也匹配不到，
// 检查会以「找不到」而不是「不匹配」告终。
func uiSources(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range []string{"ui.html", "app.js"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", name, err)
		}
		out = append(out, string(raw))
	}
	return out
}

// antd 上被引用的每个顶层名字都必须在包里真的存在。
//
// 这条测试来自一个真实的空白弹窗：备注那一栏写成了 <A.TextArea>，而 antd 里
// 只有 Input.TextArea，没有顶层的 TextArea。名字不对不会提示「没有这个组件」，
// 而是整棵子树抛 React #130（Element type is invalid），弹窗打开就是一片白，
// 控制台里也看不到是哪个名字写错了。用到的名字直接在源码里逐个核对，成本极低。
func TestAntdNamesExist(t *testing.T) {
	exported := antdExports(t)

	re := regexp.MustCompile(`\bA\.([A-Za-z_]\w*)`)
	used := map[string]bool{}
	for _, src := range uiSources(t) {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			used[m[1]] = true
		}
	}
	if len(used) == 0 {
		t.Fatal("没能从界面源码里解析出任何 antd 名字——正则或界面结构变了，这条测试已经失去意义")
	}

	for name := range used {
		if !exported[name] {
			t.Errorf("antd 上没有 %s 这个导出，界面上引用它会整棵子树渲染失败", name)
		}
	}
}

// antdExports 从内置的 UMD 包里解析出导出名。
//
// 导出表是一段字面量：t.d(e,{Affix:function(){return ce},Alert:function(){return Le},...})
// 所以不需要跑 JS 就能读出来。包换了版本、这段形状变了就应当让它失败，
// 而不是悄悄返回一个空集合——空集合会让上面那条测试变成永不报错的空转。
func antdExports(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("assets/antd.min.js")
	if err != nil {
		t.Fatalf("读 antd 包失败：%v", err)
	}
	block := regexp.MustCompile(`t\.d\(e,\{((?:\w+:function\(\)\{return [\w.$]+\},?)+)\}`).
		FindSubmatch(raw)
	if block == nil {
		t.Fatal("在 antd 包里找不到导出表——包的构建形式变了，这条检查需要跟着改")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(\w+):function\(\)\{return`).FindAllSubmatch(block[1], -1) {
		out[string(m[1])] = true
	}
	if len(out) < 50 {
		t.Fatalf("只解析出 %d 个 antd 导出，明显不对——导出表的结构可能变了", len(out))
	}
	// locales 不在上面那张表里：它是 with-locales 这层包装在运行时挂上去的
	// （包里能看到 r.locales={} 那一行）。这不是漏解析，是它确实不走导出表。
	// 真要多出第二个这样的名字，这里会直接报出来，补一行即可。
	for _, extra := range []string{"locales"} {
		out[extra] = true
	}
	return out
}

// 绑定名必须与界面里调用的名字逐字一致。不一致的表现是「点了没反应」：
// 不报错、不提示，只是那个按钮永远不工作——所以这里必须由测试守着。
func TestBindingsMatchUI(t *testing.T) {
	a := testApp(t)

	// 界面侧的名字都以 window.pier 开头，取出来做双向比对。
	re := regexp.MustCompile(`window\.(pier[A-Z][A-Za-z]*)`)
	used := map[string]bool{}
	for _, src := range uiSources(t) {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			used[m[1]] = true
		}
	}
	if len(used) == 0 {
		t.Fatal("没能从界面源码里解析出任何绑定名——正则或界面结构变了，这条测试已经失去意义")
	}

	declared := map[string]bool{}
	for _, b := range a.bindings() {
		if declared[b.name] {
			t.Errorf("绑定 %s 声明了两次", b.name)
		}
		declared[b.name] = true
		if b.fn == nil {
			t.Errorf("绑定 %s 没有对应的实现", b.name)
		}
	}

	for name := range used {
		if !declared[name] {
			t.Errorf("ui.html 调用了 %s，但后端没有绑定它——界面上对应的按钮会永远没反应", name)
		}
	}
	for name := range declared {
		if !used[name] {
			t.Logf("提示：后端绑定了 %s，但界面没有用到（可能是残留）", name)
		}
	}
}

// 界面读的字段名必须真的存在于后端返回的结构里。
//
// 这条测试是被一个真实的故障逼出来的：PortOwner 的 json 标签是小写的
// （pid/command/started），而界面按 Go 的字段名读（o.PID/o.Command），
// 于是端口占用详情里整排显示成「—」，按钮上写着「结束进程 undefined」。
// 后端查得好好的，看起来却像整个功能没做——而当时唯一的一致性测试
// 只比对绑定**函数名**，字段名怎么错都拦不住。
//
// 做法：把界面源码里所有 `某个已知响应变量.字段` 的读法抓出来，
// 逐个确认它是后端某个输出结构里真实存在的 json 键。变量名是白名单枚举的，
// 不做泛化猜测——宁可漏报，也不要因为误报被后来的人整条关掉。
func TestUIFieldNamesExistInBackend(t *testing.T) {
	// 每个变量名对应它承载的后端类型。
	targets := []struct {
		vars  []string
		props func() []string
		what  string
	}{
		{[]string{"data"}, keysOf(panel.StateOut{}), "状态"},
		{[]string{"s", "svc", "editing"}, keysOf(panel.ServiceOut{}), "服务"},
		{[]string{"g"}, keysOf(panel.GroupOut{}), "分组"},
		{[]string{"poData"}, keysOf(manage.PortOwnerOut{}), "端口占用"},
		{[]string{"owner"}, keysOf(proc.PortOwner{}), "占用进程"},
		{[]string{"occupant"}, keysOf(proc.Listener{}), "监听进程"},
		{[]string{"info"}, keysOf(manage.InspectOut{}), "目录检查"},
		{[]string{"cand"}, keysOf(manage.PortCandOut{}), "端口候选"},
		// 端口扫描与纳管。「scan」不能改叫 data：那个名字上挂的是面板状态结构。
		{[]string{"scan"}, keysOf(manage.PortScanOut{}), "端口扫描"},
		{[]string{"sp"}, keysOf(manage.ScannedPort{}), "扫描到的端口"},
		// 空清单那一屏的目录扫描。这两个名字不能省成 data / item：前者是面板状态，
		// 后者已经被表单里那条运行配置的预填值占了。
		{[]string{"sco"}, keysOf(manage.ScanOut{}), "目录扫描"},
		{[]string{"sci"}, keysOf(manage.ScanItemOut{}), "扫描到的项目"},
		{[]string{"logData"}, keysOf(panel.LogOut{}), "日志"},
		// SDK 管理页与表单里那次工具链预演。
		//
		// 注意上面那条：变量名本身就是被检查的对象，所以 SDK 清单不能也叫 data
		// ——那会让它读的字段拿面板状态结构去比对，kinds / items 一个都过不了。
		// 名字撞车这事只有这条测试看得见，界面上照常渲染（只是渲染成空的）。
		{[]string{"sdks"}, keysOf(manage.SDKListOut{}), "SDK 清单"},
		{[]string{"ki"}, keysOf(manage.SDKKindOut{}), "SDK 类别"},
		{[]string{"si"}, keysOf(manage.SDKItemOut{}), "SDK 条目"},
		{[]string{"preview"}, keysOf(manage.ToolchainOut{}), "工具链预演"},
		// 日志页。占用是 panel 那一层的结构（在 proc.LogUsageOut 上多带了
		// 换算好的 size），所以对的是 panel.LogUsageOut / panel.LogServiceOut。
		{[]string{"lu"}, keysOf(panel.LogUsageOut{}), "日志占用"},
		{[]string{"luSvc"}, keysOf(panel.LogServiceOut{}), "日志占用明细"},
		// 偏好设置页那两块：更新状态与上一次替换的结果。
		//
		// 结果那一条必须是另一个变量名：result 是嵌在状态里的一个子结构，
		// 而 keysOf 只看顶层。写成 up.result.ok 的话，比对的是 update.StatusOut
		// 上没有的那个 result 键——所以界面里先把它取出来叫 res（见 SettingsPage）。
		{[]string{"up"}, keysOf(update.StatusOut{}), "更新状态"},
		{[]string{"res"}, keysOf(update.ApplyResult{}), "更新结果"},
		// 服务行下面那句诊断。它同样嵌在服务里，得单列一个变量名：
		// 写成 s.diag.reason 的话，比对的是 ServiceOut 上没有的 reason 键。
		{[]string{"dg"}, keysOf(diag.Hit{}), "诊断"},
	}

	for _, tg := range targets {
		allowed := map[string]bool{}
		for _, k := range tg.props() {
			allowed[k] = true
		}
		// 变量名本身不参与匹配，只匹配 `名字.字段`。
		pat := regexp.MustCompile(`\b(?:` + strings.Join(tg.vars, "|") + `)\.([A-Za-z_][A-Za-z0-9_]*)`)

		for _, src := range uiSources(t) {
			for _, m := range pat.FindAllStringSubmatch(src, -1) {
				field := m[1]
				if allowed[field] {
					continue
				}
				// 形如 o.pid 这种读法若是 JS 自己的属性（比如数组方法），会落进这里，
				// 但白名单变量名都是纯数据对象，不该有方法调用，所以直接报出来更安全。
				t.Errorf("%s：界面读了 .%s，但后端 %s 结构里没有这个 json 键（可用：%s）",
					tg.what, field, tg.what, strings.Join(sortedKeys(allowed), "、"))
			}
		}
	}
}

// keysOf 反射出一个结构的全部 json 键。
func keysOf(v any) func() []string {
	return func() []string {
		rt := reflect.TypeOf(v)
		out := make([]string, 0, rt.NumField())
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name == "" || name == "-" {
				name = f.Name
			}
			out = append(out, name)
		}
		return out
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 表单里「专属设置」摆哪几个 SDK 下拉，取决于这个界面自己那张表
// （app.js 的 KIND_TOOLS，服务类型 → 工具链类别）。
//
// 那是 internal/config 里各 planXxx 的第二份，而且是纯展示用的第二份：
// 摆错了不会报错、不会崩，只会让人在一个与服务无关的空下拉前发呆，或者更糟——
// 以为某个版本已经钉住了，而那次启动压根不看这个类别。所以逐条跟真实的
// Plan 对一遍。
//
// pnpm 单独说：node 的 Plan 里带着它（按锁文件决定用 pnpm 还是 npm），
// 但界面上不列——它不是独立安装的一套运行时，而是跟着选中的 node 走的，
// 面板与 doctor 也都不展示它（internal/proc.ToolInfos）。
func TestFormToolKindsMatchPlan(t *testing.T) {
	src := strings.Join(uiSources(t), "\n")
	block := regexp.MustCompile(`(?s)var KIND_TOOLS = \{(.*?)\n  \};`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("app.js 里找不到 KIND_TOOLS——写法变了，这条测试已经失去意义")
	}
	got := map[string][]string{}
	for _, m := range regexp.MustCompile(`(\w+):\s*\[([^\]]*)\]`).FindAllStringSubmatch(block[1], -1) {
		kinds := []string{}
		for _, v := range strings.Split(m[2], ",") {
			if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
				kinds = append(kinds, v)
			}
		}
		got[m[1]] = kinds
	}
	if len(got) == 0 {
		t.Fatal("KIND_TOOLS 里一条都没解析出来——正则或写法变了")
	}

	cfg := &config.Config{}
	// run 一律显式给出：让每个类型都走到自己的 planXxx，而不必先铺一个能
	// 认得出类型的项目目录（那样测的就不是这张表了）。
	for _, kind := range []string{"go", "java", "node", "python", "shell"} {
		svc := &config.Service{Name: "probe", Kind: kind, Run: "true"}
		plan, err := svc.Plan(cfg)
		if err != nil {
			t.Fatalf("%s 的启动方案解析失败：%v", kind, err)
		}
		want := make([]string, 0, len(plan.Tools))
		for _, k := range plan.Tools {
			if k == toolchain.Pnpm {
				continue
			}
			want = append(want, string(k))
		}
		list, ok := got[kind]
		if !ok {
			t.Errorf("KIND_TOOLS 里没有 %s：这个类型的服务在界面上一个 SDK 下拉都不会出现", kind)
			continue
		}
		if strings.Join(list, ",") != strings.Join(want, ",") {
			t.Errorf("KIND_TOOLS[%s] 是 [%s]，但 %s 的服务实际要用 [%s]"+
				"——界面会摆错下拉，而摆错不会报任何错",
				kind, strings.Join(list, " "), kind, strings.Join(want, " "))
		}
		delete(got, kind)
	}
	for kind := range got {
		t.Errorf("KIND_TOOLS 里多了一个 %s——它不是有效的服务类型", kind)
	}
}

// 输出结构的字段必须都带显式 json 标签。
//
// 不写标签时 Go 会把键导出成 Port、PID 这样的大写形式，与界面约定的小写命名
// 天生不一致；而这种错不会让程序崩，只会让界面某处默默显示成空。
func TestOutputStructsTagEveryField(t *testing.T) {
	structs := map[string]any{
		"msgOut": msgOut{}, "panel.LogOut": panel.LogOut{}, "panel.StateOut": panel.StateOut{},
		"panel.ServiceOut": panel.ServiceOut{}, "panel.GroupOut": panel.GroupOut{},
		"panel.UsageOut":      panel.UsageOut{},
		"manage.PortOwnerOut": manage.PortOwnerOut{}, "manage.InspectOut": manage.InspectOut{},
		"manage.PortCandOut": manage.PortCandOut{}, "manage.ServiceIn": manage.ServiceIn{},
		"manage.SDKListOut": manage.SDKListOut{}, "manage.SDKKindOut": manage.SDKKindOut{},
		"manage.SDKItemOut": manage.SDKItemOut{}, "manage.SDKAddOut": manage.SDKAddOut{},
		"manage.ToolchainOut": manage.ToolchainOut{},
		"proc.PortOwner":      proc.PortOwner{}, "proc.Listener": proc.Listener{},
		"proc.ToolInfo":    proc.ToolInfo{},
		"update.StatusOut": update.StatusOut{}, "update.ApplyResult": update.ApplyResult{},
	}
	names := make([]string, 0, len(structs))
	for n := range structs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		rt := reflect.TypeOf(structs[n])
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Tag.Get("json") == "" {
				t.Errorf("%s.%s 没有 json 标签：Go 会把它导出成 %q，与界面约定的小写命名对不上",
					n, f.Name, f.Name)
			}
		}
	}
}

// 桥断开时界面必须报错，而不是退回演示数据。
// 演示模式只在显式加 ?demo=1 时启用：一旦自动回退，桥真的断了也会显示一份假状态，
// 看上去一切正常，比直接报错危险得多。
func TestDemoModeRequiresExplicitOptIn(t *testing.T) {
	src := strings.Join(uiSources(t), "\n")
	if !strings.Contains(src, `new URLSearchParams(location.search).has("demo")`) {
		t.Error("界面不再要求显式 ?demo=1 才进入演示模式——桥断开时可能静默显示假数据")
	}
	if strings.Contains(src, "const DEMO = bridge === null") {
		t.Error("界面又变回了「没有后端就用演示数据」——这正是会让故障静默的写法")
	}
	if !strings.Contains(src, "missingBindings.length") {
		t.Error("界面没有检查绑定是否齐全，桥断开时不会报错")
	}
}

// 「运行 -」这个 bug 的回归护栏。
//
// 后端拿 view.Dash（"-"）表示「这一项没有」，而 "-" 在 JavaScript 里是真值，
// 所以 `if (s.uptime)` 判断不出「没有」——未启动的服务会多渲染一段
// 「运行 -」，看上去像是运行了但时长读不出来。凡是这类字段都必须先过 hasVal。
func TestDashSentinelsAreTreatedAsEmpty(t *testing.T) {
	src := strings.Join(uiSources(t), "\n")
	code := stripLineComments(src)

	if !strings.Contains(code, "function hasVal(") {
		t.Fatal("界面里没有 hasVal()——「没有值」的判断散在各处，迟早有一处漏掉")
	}
	// hasVal 必须真的把 "-" 当空，而不是只挡 undefined/null。
	if !regexp.MustCompile(`function hasVal\([\s\S]{0,300}?"-"`).MatchString(code) {
		t.Error(`hasVal() 没有把 "-" 当成空值，但它正是后端表示「这一项没有」的写法`)
	}

	// 后端那边这个约定由 view.Dash 定义，两边要一致。
	viewSrc, err := os.ReadFile("../internal/view/view.go")
	if err != nil {
		t.Fatalf("读 internal/view/view.go 失败：%v", err)
	}
	if !strings.Contains(string(viewSrc), `const Dash = "-"`) {
		t.Error(`internal/view 里的占位符不再是 "-" 了，前端 hasVal() 要跟着改`)
	}

	// 时长、PID 这类字段不能再退回真值判断。
	for _, bad := range []string{"if (s.uptime)", "s.uptime ?", "if (s.note)", "s.note &&", "s.userNote ?"} {
		if strings.Contains(code, bad) {
			t.Errorf("界面里又出现了 %q：这些字段可能是 \"-\"，真值判断会把占位符当成有值", bad)
		}
	}
	if !strings.Contains(code, "hasVal(s.uptime)") {
		t.Error("时长的显示没有走 hasVal()，「运行 -」会再次出现")
	}
}

// stripLineComments 去掉整行注释与块注释。
//
// 上面那条检查扫的是「代码里有没有写错的判断」，而注释里正需要引用这个写错的
// 写法来说明它错在哪——不剥掉注释，测试会被自己文档里的一句话绊倒。
// 只处理整行注释，够用且不会误伤字符串里的 //（比如 http:// 的地址）。
func stripLineComments(src string) string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if i := strings.Index(trimmed, "*/"); i >= 0 {
				inBlock = false
				trimmed = trimmed[i+2:]
				if trimmed != "" {
					out = append(out, trimmed)
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if i := strings.Index(trimmed, "*/"); i >= 0 && i > 1 {
				out = append(out, trimmed[i+2:])
				continue
			}
			inBlock = true
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// 界面源码里的色值只准出现在一处：app.js 的 PALETTE 调色板。
//
// 这条来自一次返工：上一版界面在 CSS 里自造了一套主色（#5A6ACF）、橙色（#fa8c16）、
// 红色（#cf1322）和一批圆角字号，叠在 antd 之上等于另立了一套规范，结果主色、
// 圆角、控件高度、字号全和 antd 官方对不上，深色下的表现也不再由算法决定。
// 颜色只能从 antd 出：组件用语义属性，自绘的地方用 theme.useToken()，
// 连崩溃红屏也不例外（它拿不到 context，就用静态的 theme.getDesignToken()）。
//
// 后来界面按 bigmodel.cn 控制台那套观感定了自己的配色，于是规则改成两条：
//   - 色值集中写在 app.js 的 PALETTE 里，通过 ConfigProvider 交给 antd，
//     而不是散在 CSS 和内联样式里各写各的；
//   - 调色板以外的地方，除白名单外再出现任何色值都算失败。
//
// 白名单里只剩两类「不跟主题走」的固定图形：
//   - 日志区刻意做成「终端」样子的深底白字，antd 的 token 里没有这样一组；
//   - 品牌标记是固定的图形，底色与图形不能随主题漂移。
func TestSourcesHaveNoHardcodedColors(t *testing.T) {
	allowed := map[string]map[string]string{
		"app.css": {
			"#14161a": "日志区的深色底",
			"#d7dae0": "日志区的浅色字",
		},
		"app.js": {
			"#3F6BFF": "品牌标记底板渐变的上端",
			"#1433D6": "品牌标记底板渐变的下端",
			"#FFFFFF": "品牌标记的桥面、桩与水面",
			"#3DDC97": "品牌标记顶上那盏灯",
		},
	}
	colorLit := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`)

	// 调色板整块摘掉再扫。摘掉之后剩下的部分才是「调色板以外」。
	// 用非贪婪匹配到顶层那个 `  };`——内层对象都以四个空格的 `    },` 收尾，
	// 不会提前截断。匹配不上就直接失败：那时候这条测试会永远绿灯。
	paletteBlock := regexp.MustCompile(`(?s)var PALETTE = \{.*?\n  \};`)

	for file, ok := range allowed {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", file, err)
		}
		// 注释里会引用这些色值来说明它们曾经错在哪，扫之前先剥掉。
		body := stripLineComments(string(raw))
		if file == "app.js" {
			if !paletteBlock.MatchString(body) {
				t.Fatalf("app.js 里找不到 PALETTE 调色板区块——写法变了，这条测试已经失去意义")
			}
			body = paletteBlock.ReplaceAllString(body, "")
		}
		found := map[string]bool{}
		for _, m := range colorLit.FindAllString(body, -1) {
			found[m] = true
		}
		if len(found) == 0 {
			t.Fatalf("%s 里一个色值都没扫到——正则或文件的写法变了，这条测试已经失去意义", file)
		}
		for lit := range found {
			if _, permit := ok[lit]; permit {
				continue
			}
			t.Errorf("%s 里出现了自造的颜色 %s。"+
				"颜色应当由 antd 出：组件用语义属性，自绘的地方用 theme.useToken()", file, lit)
		}
		// 白名单也要跟着收拾：已经删掉的例外不该继续挂着，
		// 否则它会一直替一个不存在的色值打掩护（把那几行改回去也不会被发现）。
		for lit, why := range ok {
			if !found[lit] {
				t.Errorf("%s 的白名单里列着 %s（%s），但文件里已经没有这个色值了，把这条删掉",
					file, lit, why)
			}
		}
	}
}

// 控件尺寸只由 antd 出，界面里不许再自己压一档或抬一档。
//
// 这条来自一次实测：添加应用的表单写着 size="small"，里面的输入框全是 24px 高，
// 而弹窗底部的「取消 / 添加」和整个主界面都是 antd 默认的 32px——同一屏里两种
// 控件高度，看着就是「尺寸没统一」。字号同理，写死 12px、11.5px 等于在 antd 的
// 字号阶梯之外又开了几个档。要更小的字就用 token.fontSizeSM，别写字面量。
//
// 只管「控件尺寸」和「字号」两类。布局性的 padding / height 不在此列：
// 端口候选那种两行的方块本来就没有对应的 antd 组件，得自己撑高度。
func TestNoAdHocControlSizes(t *testing.T) {
	// 例外逐个列出，新增任何一个都会让这条测试失败。目前是空的——
	// 出现第一条时，请连同「为什么 antd 的默认值不能用」一起写在这里。
	allowed := map[string]string{}

	sizeOverride := regexp.MustCompile(`size\s*[:=]\s*"(small|middle|large)"`)
	fontSizeLit := regexp.MustCompile(`fontSize\s*:\s*[0-9.]+`)

	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	body := stripLineComments(string(raw))

	// 扫不到任何东西说明正则失效了，那时候这条测试会永远绿灯。
	if !strings.Contains(body, "A.Button") {
		t.Fatal("app.js 里连 A.Button 都找不到了——文件或正则变了，这条测试已经失去意义")
	}

	found := map[string]bool{}
	for _, m := range sizeOverride.FindAllString(body, -1) {
		found[m] = true
	}
	for _, m := range fontSizeLit.FindAllString(body, -1) {
		found[m] = true
	}
	for lit := range found {
		if _, permit := allowed[lit]; permit {
			continue
		}
		t.Errorf("app.js 里出现了自定的控件尺寸 %q。"+
			"控件尺寸一律用 antd 默认值（去掉 size=），字号用 token.fontSizeSM 这类 token", lit)
	}
	for lit, why := range allowed {
		if !found[lit] {
			t.Errorf("白名单里列着 %q（%s），但文件里已经没有它了，把这条删掉", lit, why)
		}
	}

	// 顺带守住弹窗高度：每个 Modal 都要挂 BODY_SCROLL。
	//
	// antd 的 Modal 不限制自身高度。添加应用那个表单实测 784px 高，而窗口只有
	// 780px——底部的「取消 / 添加」被顶到窗口外面，弹窗本身却看不出任何异常，
	// 表现就是「保存按钮点不着」。BODY_SCROLL 给 body 一个上限让它自己滚。
	modals := strings.Count(body, "return html`<${A.Modal}")
	scrolled := strings.Count(body, "body: BODY_SCROLL")
	if modals == 0 {
		t.Fatal("一个 A.Modal 都没数到，弹窗的写法变了，这条测试已经失去意义")
	}
	if modals != scrolled {
		t.Errorf("有 %d 个 Modal，但只有 %d 个挂了 BODY_SCROLL。"+
			"内容一长，底部的确定/取消按钮就会被顶出窗口外", modals, scrolled)
	}
}

// htm 模板里没有「注释」这回事：写在里面的 // 不是注释，是一个文本节点。
//
// 这条来自一次真实的返工：日期下拉上面写了两行 `// 宽度是按内容量的……`。
// go vet、go test（全是源码扫描）、node --check 三样全过，而页面顶上原样印着
// 那两行字——只有截图能看见。换成 /* */ 也一样，模板里它同样是文本。
// 要写说明就写到模板外面，或者在 ${} 表达式里写。
//
// 判断「某一行是不是落在模板的字面文本里」需要一个小状态机：模板里能嵌 ${}
// 表达式，表达式里又能再嵌模板和字符串，光看缩进或者看前一行是分不出来的。
func TestNoCommentLinesInsideHtmTemplates(t *testing.T) {
	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	src := string(raw)

	// 一处模板都没扫到，说明状态机已经跟文件的写法对不上了，
	// 那时候这条测试会永远绿灯。
	if n := strings.Count(src, "html`"); n < 20 {
		t.Fatalf("app.js 里只数到 %d 处 html`，模板的写法变了，这条测试已经失去意义", n)
	}

	lines := strings.Split(src, "\n")
	for _, n := range htmCommentLines(src) {
		t.Errorf("app.js:%d 是模板里的一行 //，它会被原样印到页面上：%s",
			n, strings.TrimSpace(lines[n-1]))
	}
}

// jsFrame 是扫描 app.js 时的一层上下文。
type jsFrame struct {
	kind  byte // 'c' 代码、't' 模板字面量、's' 字符串
	quote byte // kind=='s' 时，是 ' 还是 "
	depth int  // kind=='c' 时，还没配平的 { 有几个
	html  bool // kind=='t' 时，这个模板是 html`...`
}

// htmCommentLines 返回 src 里「落在 html 模板的字面文本中、且不在 ${} 里」的
// // 注释所在的行号（从 1 数起）。
func htmCommentLines(src string) []int {
	var bad []int
	stack := []jsFrame{{kind: 'c'}}
	top := func() *jsFrame { return &stack[len(stack)-1] }
	line := 1
	// 是否还停在行首的空白里：只有行首的 // 才是「注释的样子」，
	// 出现在别处的 // 在模板里也是文本，但那种多半是刻意写进去的路径。
	start := true
	push := func(f jsFrame) { stack = append(stack, f) }

	for i := 0; i < len(src); {
		c := src[i]
		if c == '\n' {
			line++
			start = true
			i++
			continue
		}
		switch top().kind {
		case 's':
			if c == '\\' {
				i += 2
			} else {
				if c == top().quote {
					stack = stack[:len(stack)-1]
				}
				i++
			}
			start = false
		case 't':
			switch {
			case c == '\\':
				i += 2
			case c == '`':
				stack = stack[:len(stack)-1]
				i++
			case c == '$' && i+1 < len(src) && src[i+1] == '{':
				push(jsFrame{kind: 'c'})
				i += 2
			default:
				if start && top().html && c == '/' && i+1 < len(src) && src[i+1] == '/' {
					bad = append(bad, line)
				}
				i++
			}
			if c != ' ' && c != '\t' {
				start = false
			}
		default: // 'c' 代码
			if c == ' ' || c == '\t' || c == '\r' {
				i++
				continue
			}
			start = false
			switch {
			case c == '"' || c == '\'':
				push(jsFrame{kind: 's', quote: c})
				i++
			case c == '`':
				push(jsFrame{kind: 't', html: strings.HasSuffix(src[:i], "html")})
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					if src[i] == '\n' {
						line++
					}
					i++
				}
				i += 2
			case c == '{':
				top().depth++
				i++
			case c == '}':
				// depth 已经是 0 还遇到 }，说明它配的是当初那个 ${。
				if top().depth == 0 && len(stack) > 1 {
					stack = stack[:len(stack)-1]
				} else {
					top().depth--
				}
				i++
			default:
				i++
			}
		}
	}
	return bad
}

// 主题状态只能有一份。
//
// useTheme() 是带 useState 的 hook，调两次就是两份互不相干的状态：侧栏的主题切换
// 改到的是 App 那份，而决定 antd 算法的 ConfigProvider 在 Root 里读的是另一份，
// 于是「点了暗色没反应」，只有 localStorage 被悄悄改掉，下次启动才生效。
// 这类「状态分裂」不会报任何错，只能靠测试数调用点。
func TestThemeStateHasSingleOwner(t *testing.T) {
	code := stripLineComments(strings.Join(uiSources(t), "\n"))

	// 减去函数签名那一处 `function useTheme()`，剩下的才是调用点。
	defs := strings.Count(code, "function useTheme()")
	if defs != 1 {
		t.Fatalf("useTheme 的定义有 %d 处，预期 1 处", defs)
	}
	if n := strings.Count(code, "useTheme()") - defs; n != 1 {
		t.Errorf("useTheme() 被调用了 %d 次，主题状态必须只有一份（只有 Root 可以调，"+
			"其余地方从 props 拿）", n)
	}
	if !strings.Contains(code, "<${App} th=${th}/>") {
		t.Error("Root 没有把主题状态传给 App——App 里如果再自己调一次 useTheme()，" +
			"侧栏的主题切换就不会作用到 ConfigProvider 上")
	}
}

// 内联进 <script> 的第三方库里不能出现会截断 HTML 的序列。
// 整个界面是被拼成一份 HTML 字符串交给 webview 的，库里只要出现一个字面量
// "</script"，页面就会从这里被截断，剩下的脚本全都不执行——表现是一个白窗口，
// 而且换版本时才会发生，本地怎么改界面代码都修不好。
func TestAssetsInlineSafely(t *testing.T) {
	entries, err := os.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("assets 目录是空的，界面会缺少依赖")
	}
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("assets", en.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"</script", "<!--"} {
			if strings.Contains(strings.ToLower(string(raw)), bad) {
				t.Errorf("assets/%s 里含有 %q，内联进 HTML 会把页面截断", en.Name(), bad)
			}
		}
	}
}

// TestDumpHTML 把拼好的整份 HTML 写到文件，供无头浏览器预览用。
//
// 默认跳过：它不验证任何东西，只是一个把界面导出成静态文件的工具。
// 有了它才能在不开窗口的情况下检查排版——窗口里的东西只有人能点，
// 而排版问题不该等到那时候才发现。用法：
//
//	PIER_DUMP_HTML=/tmp/preview.html go test ./gui/ -run TestDumpHTML
//
// 导出的页面直接拿浏览器打开就行；要出图（含六个弹窗、深浅两套）用
// tools/shoot/shoot.py，它会在导出结果上再注入一套只给预览用的补丁，
// 处理无头 Chrome 的虚拟时间不驱动 rAF、以及过渡中间态被截进去这两个坑。
func TestDumpHTML(t *testing.T) {
	out := os.Getenv("PIER_DUMP_HTML")
	if out == "" {
		t.Skip("未设置 PIER_DUMP_HTML，跳过")
	}
	if err := os.WriteFile(out, []byte(buildHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("已写出 %s", out)
}

// ui.html 必须留着两个资源占位标记，否则第三方库和应用代码根本没被拼进去，
// 窗口会打开但一片空白。
func TestShellKeepsAssetMarker(t *testing.T) {
	raw, err := os.ReadFile("ui.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{styleMarker, scriptMarker} {
		if !strings.Contains(string(raw), m) {
			t.Errorf("ui.html 里没有 %s，buildHTML 拼进去的资源会被丢掉", m)
		}
	}
	if got := buildHTML(); !strings.Contains(got, "antd") {
		t.Error("拼出来的 HTML 里没有 antd，界面组件会全部找不到")
	}
}

// 脚本必须排在 <div id="root"> 之后。
//
// 这条测试来自一个真实的空白窗口：五个包全内联在 <head> 里时，app.js 在
// 容器元素还没被解析出来时就跑了，React 抛 #299（Target container is not a
// DOM element），页面上什么都没有，控制台之外看不到任何线索。
func TestScriptsComeAfterRoot(t *testing.T) {
	html := buildHTML()
	// 锚点都取带闭合标签/完整调用的形式：ui.html 的注释里也提到了 <div id="root">，
	// 只用前半截会匹配到注释，测试就永远通过了。
	root := strings.Index(html, `id="root"></div>`)
	app := strings.Index(html, `createRoot(document.getElementById("root"))`)
	if root < 0 {
		t.Fatal("拼出来的 HTML 里没有 #root 容器")
	}
	if app < 0 {
		t.Fatal("拼出来的 HTML 里找不到 app.js 的挂载调用")
	}
	if app < root {
		t.Errorf("app.js（偏移 %d）排在 #root（偏移 %d）之前，React 挂载时会找不到容器", app, root)
	}
	// 样式反过来要在 head 里，否则首屏会闪一下没有样式的裸 HTML。
	style := strings.Index(html, "<style>")
	if style < 0 || style > root {
		t.Errorf("样式表应当排在 #root 之前，实际偏移 %d（#root 在 %d）", style, root)
	}
}

// ── 只有图标的按钮要有名字 ──────────────────────────────────────────────────

// 一个按钮里只有一颗图标时，它对读屏就是「按钮」两个字——一屏十来个按钮，
// 每个都这么念，键盘和读屏用户根本分不出哪个是哪个。鼠标用户也少一截：
// 没有 title 就没有悬停提示，一颗「⋯」和一颗「文件夹」得点一下才知道。
//
// 只认**自闭和**的 `<${A.Button} … />`：那就是「这个按钮里什么都没有」，
// 正是要检查的那一类。带子节点的写法里有没有文字，得看模板内容才知道，
// 靠正则判断会误伤「图标 + 文字」那一大批（那些本来就不需要额外的名字）。
//
// 名字给在元素的 title 或 aria-label 上都算数：前者鼠标看得见，后者读屏读得到。
// 两个都给最好；只给一个也比没有强，所以不强制要求两个都有。
func TestIconOnlyButtonsHaveLabels(t *testing.T) {
	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	src := string(raw)

	// 惰性匹配到第一个 `/>` 或 `<//>`，是前者才说明这个按钮没有子节点。
	re := regexp.MustCompile(`(?s)<\$\{A\.Button\}(.*?)(/>|<//>)`)
	checked := 0
	for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
		props := src[m[2]:m[3]]
		if src[m[4]:m[5]] != "/>" {
			continue
		}
		// 属性里出现 `<//>` 是不可能的，但 `/>` 可能藏在字符串里；真出现时
		// 下面这条「至少要有个名字」会把它的 props 判成残缺而报出来，不会静默。
		checked++
		if strings.Contains(props, "title=") || strings.Contains(props, "aria-label=") {
			continue
		}
		line := 1 + strings.Count(src[:m[0]], "\n")
		t.Errorf("app.js 第 %d 行的按钮里只有一颗图标，却没有 title 也没有 aria-label："+
			"读屏会把它念成一个没有名字的「按钮」，鼠标用户也看不到悬停提示", line)
	}
	if checked < 5 {
		t.Fatalf("只认出 %d 个自闭和的按钮，明显不对——写法变了，这条检查需要跟着改", checked)
	}
}

// ── 对象字面量的键不能重名 ──────────────────────────────────────────────────

// 键写重了，后写的那个静静地赢，前一个连同读它的那段代码一起失效：不报错、
// 不警告，node --check 与 go vet 都管不着（ES6 起重名键连严格模式下都合法）。
//
// 这个坑真踩过：拖动排序那个描述对象里，drop 既是「落点在哪一行」又是
// 「放手时做什么」，函数赢了，于是 dnd.drop.name === s.name 永远不成立，
// 落点那条线一次也没画出来。拖动本身照常工作，只是没有提示线——截图上看不出
// 少了什么，翻代码也一眼扫不过去，是拿真浏览器拖了一次才揪出来的。
//
// 只认「一个 var 直接赋一个多行对象字面量」这一种写法，也只比同一层缩进的键；
// 嵌套在里面的对象另有自己的扫描（它自己就是一个块）。这是刻意写窄的：
// 宁可漏掉几处，也不要因为误判让这条检查被人顺手关掉。
func TestObjectKeysAreUnique(t *testing.T) {
	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("读 app.js 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")

	// 块的起点：`var x = {` 或 `var x = (…) ? {`，两者都以 `{` 收尾。
	// 刻意排除 `var x = function () {`——那是函数体，里面的 case、标签都不是键。
	head := regexp.MustCompile(`^(\s*)var ([\w$]+) = (.*\? \{|\{)$`)
	key := regexp.MustCompile(`^(\s+)([A-Za-z_$][\w$]*)\s*:`)
	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

	blocks := 0
	for i := 0; i < len(lines); i++ {
		m := head.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		indent, name := m[1], m[2]

		// 块的终点：与 var **同缩进**的那一行。这里必须比缩进本身，不能只比
		// 「以 var 的缩进开头」——对象体里每一个更深缩进的行都满足那个条件，
		// 而 `      },`（收一个嵌在里面的函数体）去掉空白就是个 `},`，
		// 会把块截在半道上，后面的键全都扫不到。
		end := -1
		for j := i + 1; j < len(lines); j++ {
			ln := lines[j]
			if strings.TrimSpace(ln) == "" || indentOf(ln) != len(indent) {
				continue
			}
			if t := strings.TrimSpace(ln); t == "};" || t == "} : null;" || t == "}," || t == "}" {
				end = j
			}
			break
		}
		if end < 0 {
			// 认不出边界就跳过，不报错：这一行 var 可能压根不是
			// 「直接赋一个多行对象」（比如后面还跟着别的声明）。
			// 收尾的写法真变了的话，下面的块数下限会兜住。
			continue
		}
		blocks++

		// 顶层键在最浅的那一层缩进上；嵌进去的对象缩得更深，归它自己的块管。
		body := lines[i+1 : end]
		least := -1
		for _, ln := range body {
			if k := key.FindStringSubmatch(ln); k != nil && (least < 0 || len(k[1]) < least) {
				least = len(k[1])
			}
		}
		if least < 0 {
			continue
		}
		seen := map[string]int{}
		for n, ln := range body {
			k := key.FindStringSubmatch(ln)
			if k == nil || len(k[1]) != least {
				continue
			}
			if first, dup := seen[k[2]]; dup {
				t.Errorf("app.js 第 %d 行的 var %s 里，键 %s 写了两遍（另一处在第 %d 行）："+
					"后写的会盖掉前写的，读前一个的那段代码会静静地失效",
					i+1, name, k[2], i+1+first)
				continue
			}
			seen[k[2]] = n + 1
		}
	}

	// 一个块都没扫到，说明上面那两条正则已经对不上 app.js 的写法了——
	// 那时这条检查会变成永不报错的空转，比没有还糟。
	if blocks < 10 {
		t.Fatalf("只认出 %d 个对象字面量，明显不对——写法变了，这条检查需要跟着改", blocks)
	}
}
