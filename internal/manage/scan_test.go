package manage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// writeFile 在临时目录里铺一个文件，父目录不存在就建出来。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}

// fixture 铺一个混着四种项目的目录树，返回它的根。
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// Go：有 main 包才算服务，端口在 config.yaml 里
	writeFile(t, filepath.Join(root, "shop-api", "go.mod"), "module shop-api\n")
	writeFile(t, filepath.Join(root, "shop-api", "main.go"), "package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(root, "shop-api", "config.yaml"), "server:\n  port: 8081\n  path: /shop\n")

	// Node：启动脚本认 dev，端口在 .env 里
	writeFile(t, filepath.Join(root, "web", "package.json"),
		`{"name":"web","scripts":{"build":"vite build","dev":"vite"}}`)
	writeFile(t, filepath.Join(root, "web", ".env"), "VITE_PORT=5173\n")

	// Python：有 main.py 才算
	writeFile(t, filepath.Join(root, "jobs", "requirements.txt"), "requests\n")
	writeFile(t, filepath.Join(root, "jobs", "main.py"), "print(1)\n")

	// 有 package.json 但没有约定俗成的启动脚本：不该被列出来
	writeFile(t, filepath.Join(root, "docs-site", "package.json"),
		`{"name":"docs","scripts":{"build":"gatsby build"}}`)

	// 依赖目录里的东西一律不看
	writeFile(t, filepath.Join(root, "web", "node_modules", "left-pad", "package.json"),
		`{"name":"left-pad","scripts":{"dev":"node ."}}`)

	return root
}

// find 按名字取一条扫到的记录。
func find(t *testing.T, out *ScanOut, name string) ScanItemOut {
	t.Helper()
	for _, it := range out.Items {
		if it.Name == name {
			return it
		}
	}
	var names []string
	for _, it := range out.Items {
		names = append(names, it.Name)
	}
	t.Fatalf("没扫到 %s，扫到的有：%s", name, strings.Join(names, "、"))
	return ScanItemOut{}
}

func TestScanDirReadsPortsAndEvidence(t *testing.T) {
	m := New(nil)
	out, err := m.ScanDir(fixture(t))
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(out.Items) != 3 {
		t.Fatalf("应该扫到 3 个项目（go / node / python），实际 %d 个", len(out.Items))
	}

	goItem := find(t, out, "shop-api")
	if goItem.Kind != config.KindGo {
		t.Errorf("shop-api 的类型应该是 go，实际 %q", goItem.Kind)
	}
	if goItem.Port != 8081 || goItem.PortFrom != "config.yaml 的 server.port" {
		t.Errorf("shop-api 的端口应该是 8081 且写明出处，实际 %d / %q", goItem.Port, goItem.PortFrom)
	}
	if !strings.HasSuffix(goItem.Health, "/shop/health") {
		t.Errorf("shop-api 的健康检查应该带上 server.path，实际 %q", goItem.Health)
	}
	if len(goItem.Evidence) == 0 {
		t.Error("扫到的每一条都要说清楚凭什么算一个项目，shop-api 一条依据都没有")
	}

	node := find(t, out, "web")
	if node.Kind != config.KindNode || node.Script != "dev" || node.Port != 5173 {
		t.Errorf("web 应该是 node/dev/5173，实际 %s/%s/%d", node.Kind, node.Script, node.Port)
	}

	// Python 读不出端口，那就空着——不替它挑一个（见 scan.go 文件头）。
	py := find(t, out, "jobs")
	if py.Port != 0 || py.PortFrom != "" {
		t.Errorf("jobs 没写端口就该空着，实际 %d / %q", py.Port, py.PortFrom)
	}
	if py.Skip != "" {
		t.Errorf("jobs 不该被挡下：%s", py.Skip)
	}
}

// 端口与名字这类「凭什么」在清单没加载出来时也得给出来：pier detect 正是会在
// 一份读不出来的清单旁边跑。
func TestScanDirWithoutConfig(t *testing.T) {
	m := New(nil) // 没有 SetConfig
	out, err := m.ScanDir(fixture(t))
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	it := find(t, out, "shop-api")
	if it.Port != 8081 {
		t.Errorf("没有清单也要读得出端口，实际 %d", it.Port)
	}
	if it.Plan != "" {
		t.Errorf("没有清单就推不出启动方案，实际 %q", it.Plan)
	}
}

func TestScanDirMarksDuplicatesAndExisting(t *testing.T) {
	root := fixture(t)
	// 两份都叫 web 的项目：后一条会被 st.Upsert 悄悄顶掉，所以扫描阶段就要标出来。
	writeFile(t, filepath.Join(root, "site", "web", "package.json"), `{"scripts":{"start":"node ."}}`)

	h := newHarness(t)
	// 清单里已经有一条指着 shop-api 那个目录
	if _, err := h.m.SaveService(ServiceIn{Name: "alpha", Dir: filepath.Join(root, "shop-api"), Kind: config.KindGo, Port: 9101}); err != nil {
		t.Fatalf("铺清单失败：%v", err)
	}

	out, err := h.m.ScanDir(root)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}

	// alpha 那条：名字对不上（扫出来叫 shop-api），但目录已经被占
	ours := find(t, out, "shop-api")
	if ours.Skip == "" {
		t.Error("目录已经在清单里，这一条必须标出来，否则「全部添加」会把它当成新服务")
	}
	if !strings.Contains(ours.Skip, "alpha") {
		t.Errorf("那句话里要说清楚是谁占着，实际 %q", ours.Skip)
	}

	// 两条 web：一条能加，另一条标住
	var webs []ScanItemOut
	for _, it := range out.Items {
		if it.Name == "web" {
			webs = append(webs, it)
		}
	}
	if len(webs) != 2 {
		t.Fatalf("应该扫到两条 web，实际 %d 条", len(webs))
	}
	blocked := 0
	for _, w := range webs {
		if w.Skip != "" {
			blocked++
		}
	}
	if blocked != 1 {
		t.Errorf("同名两条里应该正好有一条被标住，实际 %d 条", blocked)
	}
}

func TestScanDirEmpty(t *testing.T) {
	m := New(nil)
	out, err := m.ScanDir(t.TempDir())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("空目录不该扫出东西，实际 %d 条", len(out.Items))
	}
	if out.Msg == "" {
		t.Error("扫不到东西时要说明白扫了哪儿、往下看了几层，否则用户只会以为是坏了")
	}
}

func TestScanDirRejectsBadInput(t *testing.T) {
	m := New(nil)
	if _, err := m.ScanDir(""); err == nil {
		t.Error("空目录名要拦住")
	}
	if _, err := m.ScanDir(filepath.Join(t.TempDir(), "不存在")); err == nil {
		t.Error("不存在的目录要拦住")
	}
}

// ── 勾选之后一次全加 ───────────────────────────────────────────────────────

func TestAddScannedWritesOnce(t *testing.T) {
	root := fixture(t)
	h := newHarness(t)
	before := h.reloads

	out, err := h.m.AddScanned([]ServiceIn{
		{Name: "shop-api", Dir: filepath.Join(root, "shop-api"), Kind: config.KindGo, Port: 8081},
		{Name: "web", Dir: filepath.Join(root, "web"), Kind: config.KindNode, Script: "dev", Port: 5173},
	})
	if err != nil {
		t.Fatalf("批量添加失败：%v", err)
	}
	if !out.OK || len(out.Added) != 2 || len(out.Failed) != 0 {
		t.Fatalf("两条都该加成功，实际 %+v", out)
	}
	if h.reloads != before+1 {
		t.Errorf("整批只该写一次盘、重载一次，实际重载了 %d 次", h.reloads-before)
	}
	if h.svc("web").Script != "dev" {
		t.Error("脚本要跟着一起存进去")
	}
	h.assertBaseUntouched()
}

// 一批里两条都写 8080：一条加上、一条报端口被谁用了。整批退回的话，
// 用户得把这一批拆开来一条条重试。
func TestAddScannedReportsPerItemFailures(t *testing.T) {
	root := fixture(t)
	h := newHarness(t)

	out, err := h.m.AddScanned([]ServiceIn{
		{Name: "one", Dir: filepath.Join(root, "shop-api"), Kind: config.KindGo, Port: 8080},
		{Name: "two", Dir: filepath.Join(root, "web"), Kind: config.KindNode, Script: "dev", Port: 8080},
	})
	if err != nil {
		t.Fatalf("批量添加失败：%v", err)
	}
	if len(out.Added) != 1 || len(out.Failed) != 1 {
		t.Fatalf("应该一成一败，实际 %+v", out)
	}
	if !strings.Contains(out.Failed[0].Reason, "8080") {
		t.Errorf("失败那句要写明是哪个端口撞了，实际 %q", out.Failed[0].Reason)
	}
	if out.Failed[0].Name != "two" {
		t.Errorf("失败的那条要报出自己的名字，实际 %q", out.Failed[0].Name)
	}
}

// 一条都加不成时不落盘：否则每次点一下「全部添加」都会白写一遍数据文件。
func TestAddScannedWritesNothingOnTotalFailure(t *testing.T) {
	h := newHarness(t)
	before := h.reloads

	out, err := h.m.AddScanned([]ServiceIn{{Name: "有 空格", Dir: "/tmp"}})
	if err != nil {
		t.Fatalf("批量添加失败：%v", err)
	}
	if out.OK || len(out.Failed) != 1 {
		t.Fatalf("这一条该被名字里的空格拦下，实际 %+v", out)
	}
	if h.reloads != before {
		t.Error("一条都没加成时不该写盘")
	}
	h.assertBaseUntouched()
}

// 批量添加和单条保存走的是同一份校验：认不出类型的空目录，
// 两个入口都得拦住，不然界面上会多出一个点不动的按钮。
//
// 类型不写是有意的——写了 go 反而推得出「go run .」：那是一条真能跑的命令，
// 目录空不空不归 Pier 判断。
func TestAddScannedSharesSaveValidation(t *testing.T) {
	empty := t.TempDir()
	h := newHarness(t)

	batch, err := h.m.AddScanned([]ServiceIn{{Name: "nothing", Dir: empty}})
	if err != nil {
		t.Fatalf("批量添加失败：%v", err)
	}
	if len(batch.Failed) != 1 {
		t.Fatalf("推不出启动命令的目录该被拦下，实际 %+v", batch)
	}

	// 同一份判据也要挡住单条保存：两个入口各写一遍的话，迟早一个拦住一个放过。
	if _, err := h.m.SaveService(ServiceIn{Name: "nothing", Dir: empty}); err == nil {
		t.Fatal("单条保存也该拦下同一个目录")
	} else if !strings.Contains(err.Error(), "没法推导出启动命令") {
		t.Errorf("报错要说清楚是推不出启动命令，实际 %v", err)
	}
}
