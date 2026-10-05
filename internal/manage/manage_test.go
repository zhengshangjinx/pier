package manage

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// baseYAML 是导入前的旧清单。刻意给 alpha 带上 note 和 run：分组改名只该动
// group 字段，其余字段要是被带丢了，这两个值就会静默消失。
const baseYAML = `
services:
  - name: alpha
    dir: a
    kind: go
    group: 组一
    port: 1001
    note: 手写的备注
    run: go run ./cmd/alpha
  - name: beta
    dir: b
    kind: go
    group: 组一
    port: 1002
  - name: gamma
    dir: c
    kind: go
    port: 1003
  - name: delta
    dir: d
    kind: go
`

type harness struct {
	t       *testing.T
	base    string // 导入来源的旧清单路径
	baseRaw []byte // 它的原始字节，用来核对没被动过
	store   string // 导入后的数据文件
	reloads int
	m       *Manager
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t, baseYAML) }

func newHarnessWith(t *testing.T, base string) *harness {
	t.Helper()
	dir := t.TempDir()
	basePath := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(basePath, []byte(base), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("读回清单失败：%v", err)
	}

	// 和真实启动走同一条路：先把旧清单导入成数据文件，之后只读写数据文件。
	store := filepath.Join(t.TempDir(), config.StoreName)
	if _, err := config.EnsureStore(store, []string{basePath}); err != nil {
		t.Fatalf("导入清单失败：%v", err)
	}
	h := &harness{t: t, base: basePath, baseRaw: raw, store: store}

	// reload 模拟宿主的重新加载：换掉自己手上那份清单。
	h.m = New(func(path string) error {
		h.reloads++
		cfg, err := config.Open(path)
		if err != nil {
			return err
		}
		h.m.SetConfig(cfg)
		return nil
	})

	cfg, err := config.LoadStore(store)
	if err != nil {
		t.Fatalf("加载数据文件失败：%v", err)
	}
	h.m.SetConfig(cfg)
	return h
}

// onDisk 从磁盘重新读一遍数据文件：界面重启后看到的就是它。
func (h *harness) onDisk() *config.Config {
	h.t.Helper()
	cfg, err := config.LoadStore(h.store)
	if err != nil {
		h.t.Fatalf("数据文件读不回来：%v", err)
	}
	return cfg
}

// assertBaseUntouched 核对导入来源的旧清单一个字节都没变。
//
// 数据搬进 Pier 之后，旧清单就是用户的备份：导入失败、想回退，靠的都是它原样还在。
// 所以每个写用例末尾都要过一遍这道检查。
func (h *harness) assertBaseUntouched() {
	h.t.Helper()
	now, err := os.ReadFile(h.base)
	if err != nil {
		h.t.Fatalf("清单读不回来了：%v", err)
	}
	if string(now) != string(h.baseRaw) {
		h.t.Errorf("pier.yaml 被改动了，这份文件必须是只读的\n改后内容：\n%s", now)
	}
}

func (h *harness) svc(name string) *config.Service {
	h.t.Helper()
	svc, err := h.m.Config().Find(name)
	if err != nil {
		h.t.Fatalf("找不到服务 %s：%v", name, err)
	}
	return svc
}

// ── 只读承诺 ───────────────────────────────────────────────────────────────

func TestWriteOpsNeverTouchBaseManifest(t *testing.T) {
	h := newHarness(t)

	if _, err := h.m.SaveService(ServiceIn{
		Name: "新增的", Dir: "e", Kind: "go", Port: 1005,
	}); err != nil {
		t.Fatalf("新增服务失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.m.CreateGroup("新组"); err != nil {
		t.Fatalf("新建分组失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.m.RenameGroup("组一", "改过的组"); err != nil {
		t.Fatalf("分组改名失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.m.DeleteGroup("改过的组"); err != nil {
		t.Fatalf("删除分组失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.m.SaveSharedEnv(map[string]string{"DB_HOST": "127.0.0.1"}); err != nil {
		t.Fatalf("保存共享环境变量失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.m.DeleteService("alpha"); err != nil {
		t.Fatalf("删除服务失败：%v", err)
	}
	h.assertBaseUntouched()

	if _, err := h.onDisk().Find("新增的"); err != nil {
		t.Errorf("新增的服务没有写进数据文件：%v", err)
	}
}

// 写操作改完之后必须请宿主重新加载，否则界面显示的还是改动前那份清单。
func TestWriteOpsTriggerReload(t *testing.T) {
	h := newHarness(t)
	before := h.reloads

	if _, err := h.m.CreateGroup("新组"); err != nil {
		t.Fatalf("新建分组失败：%v", err)
	}
	if h.reloads <= before {
		t.Error("写完数据文件后没有触发重新加载，界面会继续显示旧清单")
	}
}

// ── 分组 ───────────────────────────────────────────────────────────────────

// 分组改名只动成员的 group 字段，其余字段一个都不能丢——
// 漏一个就等于把用户写过的内容悄悄清掉。
func TestRenameGroupPreservesOtherFields(t *testing.T) {
	h := newHarness(t)

	if _, err := h.m.RenameGroup("组一", "组二"); err != nil {
		t.Fatalf("分组改名失败：%v", err)
	}

	alpha := h.svc("alpha")
	if alpha.GroupName() != "组二" {
		t.Errorf("alpha 的分组 = %q，应当跟着改成 组二", alpha.GroupName())
	}
	if alpha.Note != "手写的备注" {
		t.Errorf("alpha 的备注 = %q，改名时被清掉了", alpha.Note)
	}
	if alpha.Run != "go run ./cmd/alpha" {
		t.Errorf("alpha 的启动命令 = %q，改名时被清掉了", alpha.Run)
	}
	if alpha.Port != 1001 {
		t.Errorf("alpha 的端口 = %d，改名时被改掉了", alpha.Port)
	}
	if alpha.Kind != config.KindGo {
		t.Errorf("alpha 的类型 = %q，改名时被改掉了", alpha.Kind)
	}

	if beta := h.svc("beta"); beta.GroupName() != "组二" {
		t.Errorf("beta 的分组 = %q，应当跟着改成 组二", beta.GroupName())
	}
	// 本来就没分组的服务不该被卷进来。
	if gamma := h.svc("gamma"); gamma.GroupName() != config.UngroupedName {
		t.Errorf("gamma 的分组 = %q，它本来就不在 组一 里", gamma.GroupName())
	}
}

// 删分组和删应用是两回事，成员必须留下来。
func TestDeleteGroupKeepsMembers(t *testing.T) {
	h := newHarness(t)

	msg, err := h.m.DeleteGroup("组一")
	if err != nil {
		t.Fatalf("删除分组失败：%v", err)
	}
	if msg == "" {
		t.Error("删除分组应当回报一条消息")
	}

	for _, name := range []string{"alpha", "beta"} {
		svc := h.svc(name)
		if svc.GroupName() != config.UngroupedName {
			t.Errorf("%s 的分组 = %q，删分组后应当退回「%s」",
				name, svc.GroupName(), config.UngroupedName)
		}
	}
}

func TestGroupGuards(t *testing.T) {
	cases := []struct {
		what string
		run  func(m *Manager) error
	}{
		{"分组名不能为空", func(m *Manager) error {
			_, err := m.CreateGroup("   ")
			return err
		}},
		{"重名的分组建不出来", func(m *Manager) error {
			_, err := m.CreateGroup("组一")
			return err
		}},
		{"分组名不能带制表符", func(m *Manager) error {
			_, err := m.CreateGroup("带\t制表符")
			return err
		}},
		{"分组名不能超过 24 个字", func(m *Manager) error {
			_, err := m.CreateGroup("一二三四五六七八九十一二三四五六七八九十一二三四五")
			return err
		}},
		{"内置分组改不了名", func(m *Manager) error {
			_, err := m.RenameGroup(config.UngroupedName, "别的")
			return err
		}},
		{"不能改成已存在的分组名", func(m *Manager) error {
			_, err := m.CreateGroup("组二")
			if err != nil {
				return err
			}
			_, err = m.RenameGroup("组一", "组二")
			return err
		}},
		{"内置分组删不掉", func(m *Manager) error {
			_, err := m.DeleteGroup(config.UngroupedName)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			h := newHarness(t)
			if err := tc.run(h.m); err == nil {
				t.Errorf("%s：应当报错，却通过了", tc.what)
			}
			h.assertBaseUntouched()
		})
	}
}

// 改成同一个名字不算错，但也不该白写一次文件。
func TestRenameGroupToSameNameIsNoop(t *testing.T) {
	h := newHarness(t)
	before := h.reloads

	msg, err := h.m.RenameGroup("组一", "组一")
	if err != nil {
		t.Fatalf("改成同名不该报错：%v", err)
	}
	if msg == "" {
		t.Error("应当回报一条说明")
	}
	if h.reloads != before {
		t.Error("改成同名不该重新加载清单")
	}
	h.assertBaseUntouched()
}

// ── 服务 ───────────────────────────────────────────────────────────────────

func TestSaveServiceValidation(t *testing.T) {
	cases := []struct {
		what string
		in   ServiceIn
	}{
		{"服务名不能为空", ServiceIn{Dir: "x", Kind: "go"}},
		{"服务名不能带空格", ServiceIn{Name: "有 空格", Dir: "x", Kind: "go"}},
		{"服务名不能带斜杠", ServiceIn{Name: "有/斜杠", Dir: "x", Kind: "go"}},
		{"目录不能为空", ServiceIn{Name: "ok", Kind: "go"}},
		{"目录不支持波浪号", ServiceIn{Name: "ok", Dir: "~/x", Kind: "go"}},
		{"端口不能超过 65535", ServiceIn{Name: "ok", Dir: "x", Kind: "go", Port: 70000}},
		{"端口不能是负数", ServiceIn{Name: "ok", Dir: "x", Kind: "go", Port: -1}},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.m.SaveService(tc.in); err == nil {
				t.Errorf("%s：应当报错，却保存成功了", tc.what)
			}
			h.assertBaseUntouched()
		})
	}
}

// 删掉一个服务后再用同名添加回来，要能正常出现。
func TestDeleteThenAddAgain(t *testing.T) {
	h := newHarness(t)

	if _, err := h.m.DeleteService("alpha"); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if _, err := h.m.Config().Find("alpha"); err == nil {
		t.Fatal("alpha 应当已经被删掉了")
	}
	if _, err := h.m.SaveService(ServiceIn{Name: "alpha", Dir: "a", Kind: "go", Port: 1001}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if _, err := h.m.Config().Find("alpha"); err != nil {
		t.Errorf("重新添加后 alpha 应当出现：%v", err)
	}
	h.assertBaseUntouched()
}

// 新加的服务删得掉。
func TestDeleteServiceRemovesNewService(t *testing.T) {
	h := newHarness(t)
	const name = "pier-测试-临时"

	if _, err := h.m.SaveService(ServiceIn{Name: name, Dir: "x", Kind: "go", Port: 1009}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if _, err := h.m.DeleteService(name); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if _, err := h.m.Config().Find(name); err == nil {
		t.Errorf("%s 删除后应当真的消失", name)
	}
	h.assertBaseUntouched()
}

// 导入的服务和新加的服务一视同仁：删就是删，重启后也不会从旧清单里「长回来」。
func TestDeleteServiceIsReal(t *testing.T) {
	h := newHarness(t)

	msg, err := h.m.DeleteService("alpha")
	if err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if msg == "" {
		t.Error("删除应当回报一条消息")
	}
	if _, err := h.m.Config().Find("alpha"); err == nil {
		t.Error("alpha 删除后还在内存里的清单中")
	}
	if _, err := h.onDisk().Find("alpha"); err == nil {
		t.Error("alpha 删除后还在数据文件里，重启就会回来")
	}
	if _, err := h.m.DeleteService("alpha"); err == nil {
		t.Error("删除一个不存在的服务应当报错")
	}
	h.assertBaseUntouched()
}

// deadPID 找一个当前没被任何进程占用的进程号。
//
// 用来写「CrashLoop 之后留下的死记录」：那种记录指向的进程早没了，
// 它不该让一个服务变成删不掉的。
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 99999; pid > 1; pid-- {
		if !proc.ProcessAlive(pid) {
			return pid
		}
	}
	t.Fatal("扫不到空闲的进程号")
	return 0
}

// 正在跑的服务不能删。
//
// 删掉定义之后进程还在，而界面上已经没有那一行能点「停止」了：只能去活动监视器
// 手工杀，状态文件里还留着一条指向它的记录。要删就先停。
func TestDeleteServiceRefusesRunningService(t *testing.T) {
	h := newHarness(t)

	// 拿自己的进程充当那个还活着的服务：ProcessAlive 认的就是进程在不在。
	st := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: os.Getpid(), PGID: os.Getpid()},
	}}
	if err := st.Save(h.m.Config().StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	_, err := h.m.DeleteService("alpha")
	if err == nil {
		t.Fatal("服务在跑，删除却成功了")
	}
	if !strings.Contains(err.Error(), "先停止") {
		t.Errorf("错误里没说清该怎么做：%v", err)
	}
	if _, err := h.onDisk().Find("alpha"); err != nil {
		t.Error("被拒的删除还是把定义删掉了")
	}
	h.assertBaseUntouched()
}

// 状态文件里的死记录不该挡住删除。
//
// 判的是「进程还在不在」而不是「状态文件里有没有这一条」：CrashLoop 之后
// 留下的记录指向的进程早没了，按记录判会让这个服务永远删不掉。
func TestDeleteServiceAllowsStaleRecord(t *testing.T) {
	h := newHarness(t)

	st := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: deadPID(t)},
	}}
	if err := st.Save(h.m.Config().StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	if _, err := h.m.DeleteService("alpha"); err != nil {
		t.Fatalf("指向已死进程的记录不该挡住删除：%v", err)
	}
	if _, err := h.onDisk().Find("alpha"); err == nil {
		t.Error("alpha 删除后还在数据文件里")
	}
	h.assertBaseUntouched()
}

// 编译中的服务也删不得。
//
// 这个窗口只在宿主的动作簿记里看得见：进程还没拉起来，状态文件里什么都没有，
// 光看它就是「没在跑」。删掉定义之后那次启动照常完成，留下的进程谁也认领不了。
func TestDeleteServiceRefusesServiceMidAction(t *testing.T) {
	h := newHarness(t)
	h.m.SetBusyProbe(func(name string) bool { return name == "alpha" })

	_, err := h.m.DeleteService("alpha")
	if err == nil {
		t.Fatal("服务身上还有没结束的动作，删除却成功了")
	}
	if _, err := h.onDisk().Find("alpha"); err != nil {
		t.Error("被拒的删除还是把定义删掉了")
	}
	// 不在操作中的服务不受影响。
	if _, err := h.m.DeleteService("beta"); err != nil {
		t.Errorf("beta 没在操作中，应当能删：%v", err)
	}
	h.assertBaseUntouched()
}

// 高级设置里的环境变量与 JDK：没传就沿用、传空就清掉、键名不合法就拒绝；
// toolchain 里表单不管的键（比如 maven）始终保留。
func TestSaveServiceEnvAndJava(t *testing.T) {
	h := newHarnessWith(t, `
services:
  - name: alpha
    dir: a
    kind: go
    env:
      APP_ENV: dev
    toolchain:
      java: "21.0.9-oracle"
      maven: "3.9.9"
`)
	base := ServiceIn{Name: "alpha", Dir: "a", Kind: "go"}

	in := base
	in.Env = map[string]string{"A": "1", " B ": "x=y"}
	in.Toolchain = map[string]string{"java": "17.0.12-oracle"}
	if _, err := h.m.SaveService(in); err != nil {
		t.Fatal(err)
	}
	svc := h.svc("alpha")
	if svc.Env["A"] != "1" || svc.Env["B"] != "x=y" || svc.Env["APP_ENV"] != "" {
		t.Errorf("环境变量应当以提交的为准：%v", svc.Env)
	}
	if svc.Toolchain["java"] != "17.0.12-oracle" || svc.Toolchain["maven"] != "3.9.9" {
		t.Errorf("JDK 应当换成提交的、maven 保留：%v", svc.Toolchain)
	}

	if _, err := h.m.SaveService(base); err != nil {
		t.Fatal(err)
	}
	if svc := h.svc("alpha"); svc.Env["A"] != "1" || svc.Toolchain["java"] != "17.0.12-oracle" {
		t.Errorf("没传的项应当原样沿用：env=%v toolchain=%v", svc.Env, svc.Toolchain)
	}

	in = base
	in.Env, in.Toolchain = map[string]string{}, map[string]string{"java": ""}
	if _, err := h.m.SaveService(in); err != nil {
		t.Fatal(err)
	}
	if svc := h.svc("alpha"); len(svc.Env) != 0 || svc.Toolchain["java"] != "" || svc.Toolchain["maven"] != "3.9.9" {
		t.Errorf("传空应当清掉 env 与 JDK、保留 maven：env=%v toolchain=%v", svc.Env, svc.Toolchain)
	}

	in = base
	in.Env = map[string]string{"1BAD": "x"}
	if _, err := h.m.SaveService(in); err == nil {
		t.Error("以数字开头的环境变量名应当被拒绝")
	}
}

// 命令行指定的 YAML 清单只读：一切写操作都要拒绝，并且文件不变。
// 「不再检查健康」是探针过不去时唯一的出路：地址当初是猜的、或者这个服务
// 压根没有健康接口，两种都不该去改服务本身。
func TestClearHealth(t *testing.T) {
	h := newHarnessWith(t, `
services:
  - name: alpha
    dir: a
    kind: go
    port: 1001
    health: http://localhost:1001/health
  - name: beta
    dir: b
    kind: go
    port: 1002
    note: 手写的备注
`)

	msg, err := h.m.ClearHealth("alpha")
	if err != nil {
		t.Fatalf("去掉健康检查失败：%v", err)
	}
	if strings.TrimSpace(msg) == "" {
		t.Error("回执不能是空的：界面上要拿它提示")
	}
	if got := h.svc("alpha").Health; got != "" {
		t.Errorf("健康地址还在：%q", got)
	}
	// 只动这一项：端口、目录这些都要留在原处。
	a := h.svc("alpha")
	if a.Port != 1001 || a.Dir == "" {
		t.Errorf("改健康检查时动了别的字段：%+v", a)
	}
	// 落盘了才算数——界面上点完是重新读一遍状态，只在内存里改等于没改。
	if got := h.onDisk().Services[0].Health; got != "" {
		t.Errorf("磁盘上还留着健康地址：%q", got)
	}
	if h.reloads == 0 {
		t.Error("写完应当触发一次重新加载")
	}
	h.assertBaseUntouched()

	// 本来就没有的服务：说清楚，不报错也不写盘。
	before := h.reloads
	msg, err = h.m.ClearHealth("beta")
	if err != nil {
		t.Fatalf("本来就没有健康检查不该报错：%v", err)
	}
	if !strings.Contains(msg, "beta") {
		t.Errorf("回执里应当点名是哪个服务，实际 %q", msg)
	}
	if h.reloads != before {
		t.Error("没什么可改的时候不该写盘")
	}

	// 服务名不存在要报错，而不是当成「本来就空」静默通过。
	if _, err := h.m.ClearHealth("根本没有这个服务"); err == nil {
		t.Error("服务名不存在时应当报错")
	}
}

func TestYAMLConfigRefusesWrites(t *testing.T) {
	h := newHarness(t)
	cfg, err := config.Load(h.base)
	if err != nil {
		t.Fatal(err)
	}
	h.m.SetConfig(cfg)

	if _, err := h.m.SaveService(ServiceIn{Name: "x", Dir: "x", Kind: "go"}); err == nil {
		t.Error("YAML 清单上保存服务应当被拒绝")
	}
	if _, err := h.m.CreateGroup("新组"); err == nil {
		t.Error("YAML 清单上新建分组应当被拒绝")
	}
	h.assertBaseUntouched()
}

// 编辑保存不许弄丢表单管不到的字段。
//
// env 和 toolchain 界面上没有对应的输入框，ServiceIn 里也没有这两个字段，
// 它们只可能来自导入的旧清单。保存是整条替换，照 ServiceIn 新建一条写进去，
// 等于把它们抹掉了——改一次备注就会发生。
//
// 这不是假想：旧清单里 demo-admin 钉着 APP_ENV: dev（防止外部导出的
// APP_ENV 让服务误用 prod 配置），shop-admin 钉着 toolchain.java 21
// （sdkman 里另有 8 和 17，挑错了编译不过）。
func TestSaveServiceKeepsFieldsTheFormCannotEdit(t *testing.T) {
	h := newHarnessWith(t, `
services:
  - name: alpha
    dir: a
    kind: go
    port: 1001
    note: 手写的备注
    env:
      APP_ENV: dev
    toolchain:
      java: "21.0.9-oracle"
`)

	// 界面提交的那 11 个字段，一个 env / toolchain 都没有。
	if _, err := h.m.SaveService(ServiceIn{
		Name: "alpha", Dir: "a", Kind: "go", Port: 1001, Note: "改过的备注",
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}

	svc := h.svc("alpha")
	if got := svc.Env["APP_ENV"]; got != "dev" {
		t.Errorf("保存后 APP_ENV = %q，应当是 dev；表单管不到的字段被抹掉了", got)
	}
	if got := svc.Toolchain["java"]; got != "21.0.9-oracle" {
		t.Errorf("保存后 toolchain.java = %q，应当是 21.0.9-oracle；表单管不到的字段被抹掉了", got)
	}
	h.assertBaseUntouched()
}

// ── 排序、改名、复制 ───────────────────────────────────────────────────────

// 在分组页上拖动，别的分组一个都不许动。
func TestMoveServicesPartiallyReorders(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.SaveService(ServiceIn{Name: "单组的", Dir: "x", Kind: "go", Group: "组二"}); err != nil {
		t.Fatal(err)
	}
	before := h.onDisk().Names()

	// 组一那一页看到的是 alpha、beta，把它们对调。
	if _, err := h.m.MoveServices([]string{"beta", "alpha"}); err != nil {
		t.Fatalf("排序失败：%v", err)
	}
	want := "beta,alpha,gamma,delta,单组的"
	if got := strings.Join(h.onDisk().Names(), ","); got != want {
		t.Errorf("顺序 = %s，想要 %s", got, want)
	}
	// 组二那条的位置要原封不动（它排在末尾，两个组一的服务对调影响不到它）。
	if got := before[len(before)-1]; got != "单组的" {
		t.Errorf("末尾那条 = %s，本来应当是 单组的", got)
	}
	if h.reloads == 0 {
		t.Error("排序之后没有请宿主重新加载")
	}
	h.assertBaseUntouched()
}

// 不存在的名字要整条拒绝，而不是照着填一半。
func TestMoveServicesRejectsUnknownName(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.MoveServices([]string{"alpha", "查无此服务"}); err == nil {
		t.Error("不存在的服务名应当被拒绝")
	}
	if got := strings.Join(h.onDisk().Names(), ","); got != "alpha,beta,gamma,delta" {
		t.Errorf("被拒绝之后顺序不该变，实际 %s", got)
	}
}

// 分组排序：没声明过的分组也要能挪，顺序要存得住。
func TestMoveGroupsDeclaresAndKeeps(t *testing.T) {
	h := newHarness(t)
	// 起始：声明过「组一」，gamma/delta 没写分组。
	if got := strings.Join(h.onDisk().AllGroups(), ","); got != "组一,未分组" {
		t.Fatalf("起始分组 = %s", got)
	}
	if _, err := h.m.MoveGroups([]string{"组一"}); err != nil {
		t.Fatalf("只挪一个分组也该成功：%v", err)
	}
	if _, err := h.m.MoveGroups([]string{"未分组"}); err == nil {
		t.Error("「未分组」是内置的桶，不该能拖")
	}
	h.assertBaseUntouched()
}

// 在编辑表单里改名字：提交的还是一次「保存」，落盘却必须是「改名 + 覆盖」。
//
// 直接按新名字 Upsert 的话会多出一条：旧的挂在旧名字下原样留着，而用户以为自己
// 只是改了个名字。改名还必须发生在端口查重之前——查重是按名字把自己排除掉的，
// 还挂着旧名字的那一条会被当成别人。
func TestSaveServiceRenamesInPlace(t *testing.T) {
	h := newHarness(t)
	// 按名字存放的两处东西也摆好：改名之后它们得跟着走。
	cfg := h.m.Config()
	if err := os.MkdirAll(cfg.LogDirFor("alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.LogDirFor("alpha"), "2026-09-01.log"), []byte("上一次运行的输出\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.BinDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.BinDir(), "alpha"), []byte("硬链"), 0o755); err != nil {
		t.Fatal(err)
	}

	msg, err := h.m.SaveService(ServiceIn{
		Name: "阿尔法", OrigName: "alpha", Dir: "a", Kind: "go", Port: 1001,
		Env: map[string]string{"APP_ENV": "dev"},
	})
	if err != nil {
		t.Fatalf("带改名的保存失败：%v", err)
	}
	if !strings.Contains(msg, "阿尔法") {
		t.Errorf("回执里没提新名字：%s", msg)
	}
	if got := strings.Join(h.onDisk().Names(), ","); got != "阿尔法,beta,gamma,delta" {
		t.Errorf("顺序 = %s，改名不该换位置，也不该多出一条", got)
	}
	svc, err := h.onDisk().Find("阿尔法")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Port != 1001 || svc.Env["APP_ENV"] != "dev" {
		t.Errorf("改名后的字段不对：%+v", svc)
	}
	// 历史日志要跟着走，否则那条记录就再无入口了。
	if _, err := os.Stat(filepath.Join(cfg.LogDirFor("阿尔法"), "2026-09-01.log")); err != nil {
		t.Errorf("旧日志没有跟着搬过去：%v", err)
	}
	// 编译产物的名字也是服务名，改了名它就再也用不上（下次启动会照新名字重做一个）。
	if _, err := os.Stat(filepath.Join(cfg.BinDir(), "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Error("旧的编译产物没清掉")
	}
	h.assertBaseUntouched()

	// 名字没变时 OrigName 不该有任何作用。
	if _, err := h.m.SaveService(ServiceIn{
		Name: "beta", OrigName: "beta", Dir: "b", Kind: "go", Port: 1002,
	}); err != nil {
		t.Errorf("OrigName 与 Name 相同时应当照常保存：%v", err)
	}
	// 改成别人占着的名字要当场拒绝，并且什么都不写。
	if _, err := h.m.SaveService(ServiceIn{
		Name: "gamma", OrigName: "beta", Dir: "b", Kind: "go", Port: 1002,
	}); err == nil {
		t.Error("改成已有的名字应当被拒绝")
	}
	if _, err := h.onDisk().Find("beta"); err != nil {
		t.Errorf("被拒绝的改名不该动到原来的那一条：%v", err)
	}
	// 空名字、带空格的名字也不许——名字同时是日志目录名与可执行文件名。
	if _, err := h.m.SaveService(ServiceIn{
		Name: "   ", OrigName: "beta", Dir: "b", Kind: "go",
	}); err == nil {
		t.Error("空名字应当被拒绝")
	}
	if _, err := h.m.SaveService(ServiceIn{
		Name: "带 空格", OrigName: "beta", Dir: "b", Kind: "go",
	}); err == nil {
		t.Error("带空格的名字应当被拒绝")
	}
}

// 复制：新名字顺延、端口另挑一个没被占的、健康检查清掉，其余照抄。
func TestDuplicateServicePicksPortAndClearsHealth(t *testing.T) {
	h := newHarnessWith(t, `
services:
  - name: alpha
    dir: a
    kind: go
    port: 1001
    health: http://localhost:1001/health
    note: 备注
    env:
      APP_ENV: dev
`)
	out, err := h.m.DuplicateService("alpha")
	if err != nil {
		t.Fatalf("复制失败：%v", err)
	}
	if out.Name != "alpha-copy" {
		t.Errorf("新名字 = %s，想要 alpha-copy", out.Name)
	}
	cp, err := h.onDisk().Find("alpha-copy")
	if err != nil {
		t.Fatalf("复制出来的服务不在数据文件里：%v", err)
	}
	if cp.Port == 1001 || cp.Port <= 0 {
		t.Errorf("端口 = %d，必须另挑一个（不能被原服务的 %d 占着）", cp.Port, 1001)
	}
	if cp.Health != "" {
		t.Errorf("健康检查 = %q，它指着旧端口，必须清掉", cp.Health)
	}
	// 端口不能被别的服务占着（自己那一条不算）。
	for _, other := range h.onDisk().Services {
		if other.Name != cp.Name && other.Port == cp.Port {
			t.Errorf("挑中的端口 %d 已经被 %s 写了", cp.Port, other.Name)
		}
	}
	if cp.Note != "备注" || cp.Env["APP_ENV"] != "dev" || cp.Dir != h.svc("alpha").Dir {
		t.Errorf("其余字段要照抄：%+v", cp)
	}
	// 复制出来的 map 不能和原服务共用一份：改其中一个不该动到另一个。
	if cp.Env != nil {
		cp.Env["APP_ENV"] = "prod"
		if h.svc("alpha").Env["APP_ENV"] != "dev" {
			t.Error("两个服务共用了一份 env")
		}
	}

	// 再复制一次：名字顺延，而不是覆盖上一份。
	out2, err := h.m.DuplicateService("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if out2.Name != "alpha-copy2" {
		t.Errorf("第二次复制的名字 = %s，想要 alpha-copy2", out2.Name)
	}
	if _, err := h.m.DuplicateService("查无此服务"); err == nil {
		t.Error("复制一个不存在的服务应当报错")
	}
	h.assertBaseUntouched()
}

// ── 端口 ───────────────────────────────────────────────────────────────────

func TestKillPortOwnerRejectsBadInput(t *testing.T) {
	h := newHarness(t)

	if _, err := h.m.KillPortOwner("alpha", "不是数字", ""); err == nil {
		t.Error("无效进程号应当报错")
	}
	if _, err := h.m.KillPortOwner("alpha", "0", ""); err == nil {
		t.Error("进程号 0 应当报错")
	}
	if _, err := h.m.KillPortOwner("不存在", "123", ""); err == nil {
		t.Error("服务不存在应当报错")
	}
	// delta 没配端口，无从判断占用。
	if _, err := h.m.KillPortOwner("delta", "123", ""); err == nil {
		t.Error("没配端口的服务应当报错")
	}
}

func TestPortCandidatesSeparatesFreeFromUsed(t *testing.T) {
	h := newHarness(t)

	out, err := h.m.PortCandidates("")
	if err != nil {
		t.Fatalf("取候选端口失败：%v", err)
	}
	if len(out.Free) != PortCandCount {
		t.Errorf("应当给出 %d 个可用候选，实际 %d 个", PortCandCount, len(out.Free))
	}
	// 清单里写掉的端口必须落在 used 而不是 free：两者冲突的解决办法完全不同。
	used := map[int]bool{}
	for _, p := range h.m.Config().UsedPorts() {
		used[p] = true
	}
	for _, p := range out.Free {
		if used[p] {
			t.Errorf("端口 %d 已经写在清单里了，不该出现在 available 里", p)
		}
	}
	if out.Hints == nil {
		t.Error("常见端口提示不该是 nil，界面要拿它做说明")
	}
}

// ── 目录识别 ───────────────────────────────────────────────────────────────

// 目录不存在是界面要告诉用户的信息，不是错误。
func TestInspectDirMissingDirIsNotAnError(t *testing.T) {
	h := newHarness(t)

	out, err := h.m.InspectDir("根本没有这个目录", InspectHint{})
	if err != nil {
		t.Fatalf("目录不存在不该报错：%v", err)
	}
	if out.Msg == "" {
		t.Error("应当给一句说明，界面要显示它")
	}
	if len(out.Groups) == 0 || len(out.Names) == 0 {
		t.Error("即使目录不存在，也该把已有分组与服务名带回去供界面做提示")
	}
}

func TestInspectDirRequiresDir(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.InspectDir("   ", InspectHint{}); err == nil {
		t.Error("目录为空应当报错")
	}
}

func TestInspectDirDetectsGo(t *testing.T) {
	h := newHarness(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n"), 0o644); err != nil {
		t.Fatalf("铺 go.mod 失败：%v", err)
	}

	out, err := h.m.InspectDir(dir, InspectHint{})
	if err != nil {
		t.Fatalf("识别目录失败：%v", err)
	}
	if out.Kind != config.KindGo {
		t.Errorf("识别出的类型 = %q，应当是 go", out.Kind)
	}
	if out.Plan == "" {
		t.Error("应当推导出启动方案，存之前要让用户看一眼将要执行什么")
	}
	if out.SuggestPort <= 0 {
		t.Error("应当给出建议端口")
	}
	found := false
	for _, f := range out.Found {
		if f == "go.mod" {
			found = true
		}
	}
	if !found {
		t.Errorf("Found = %v，应当列出实际找到的 go.mod", out.Found)
	}
}

// 端口建议优先照抄项目自己声明的那个，而不是挑一个空闲端口塞给它。
// FreePort 挑出来的端口是「没被占用」，但不一定是这个服务真正listen的端口，
// 换掉它健康探针就永远探不通。
func TestInspectDirPrefersPortDeclaredByTheProject(t *testing.T) {
	h := newHarness(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n"), 0o644); err != nil {
		t.Fatalf("铺 go.mod 失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("server:\n  path: /admin\n  port: 20351\n"), 0o644); err != nil {
		t.Fatalf("铺 config.yaml 失败：%v", err)
	}

	out, err := h.m.InspectDir(dir, InspectHint{})
	if err != nil {
		t.Fatalf("识别目录失败：%v", err)
	}
	if out.SuggestPort != 20351 {
		t.Errorf("SuggestPort = %d，应当照抄 config.yaml 里的 20351", out.SuggestPort)
	}
	if out.PortFrom == "" {
		t.Error("PortFrom 应当说明这个端口是从哪读来的")
	}
	if want := "http://localhost:20351/admin/health"; out.Health != want {
		t.Errorf("Health = %q，应当是 %q", out.Health, want)
	}
	// 目录名是随机的临时目录名，转出来的服务名只要求跟目录名对得上。
	if out.SuggestName == "" {
		t.Error("应当由目录名给出一个建议服务名")
	}
}

// 编辑一个早就配好 module 的 Java 服务，不能再报「必须给出 module」。
//
// 这是个真实的误报：推导只看目录、不看表单里已填的子模块，于是每个 Maven 多模块
// 工程一编辑就提示「推导启动命令失败」，而且错误里还露出了内部占位名「服务 probe」。
func TestInspectDirUsesModuleFromForm(t *testing.T) {
	h := newHarness(t)

	dir := t.TempDir()
	pom := "<project><modules><module>shop-admin</module><module>shop-app</module></modules></project>"
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := h.m.InspectDir(dir, InspectHint{Name: "shop-admin", Module: "shop-admin"})
	if err != nil {
		t.Fatalf("识别目录失败：%v", err)
	}
	if out.Plan == "" || out.Msg != "" {
		t.Errorf("填了子模块应当推得出启动命令：plan=%q msg=%q", out.Plan, out.Msg)
	}

	out, err = h.m.InspectDir(dir, InspectHint{})
	if err != nil {
		t.Fatalf("识别目录失败：%v", err)
	}
	if !strings.Contains(out.Msg, "shop-admin") || !strings.Contains(out.Msg, "shop-app") {
		t.Errorf("没填子模块时应当列出 pom.xml 里的模块，实际：%q", out.Msg)
	}
	if strings.Contains(out.Msg, "probe") {
		t.Errorf("提示里不该露出内部占位名：%q", out.Msg)
	}
}

// 读不到端口时才退回空闲端口。这时 PortFrom 必须是空的——
// 界面靠它区分「项目自己声明的」和「随便挑的一个」。
func TestInspectDirFallsBackToFreePortWithoutSource(t *testing.T) {
	h := newHarness(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n"), 0o644); err != nil {
		t.Fatalf("铺 go.mod 失败：%v", err)
	}

	out, err := h.m.InspectDir(dir, InspectHint{})
	if err != nil {
		t.Fatalf("识别目录失败：%v", err)
	}
	if out.SuggestPort <= 0 {
		t.Errorf("读不到端口时仍应给出一个空闲端口，得到 %d", out.SuggestPort)
	}
	if out.PortFrom != "" {
		t.Errorf("端口是猜的，PortFrom 应当为空，得到 %q", out.PortFrom)
	}
	if out.Health != "" {
		t.Errorf("端口是猜的，不该顺手编一个健康检查地址，得到 %q", out.Health)
	}
}

// ── 分享与迁移 ─────────────────────────────────────────────────────────────

// 复制成 YAML 与导出清单，取出的是清单里的定义而不是界面上那份投影。
func TestExportYAMLComesFromManifest(t *testing.T) {
	h := newHarness(t)

	one, err := h.m.ServiceYAML("alpha")
	if err != nil {
		t.Fatal(err)
	}
	// 手写清单里的每一栏都要带出来：漏掉哪一栏，贴回去的人就少一栏，
	// 而且要等到服务起不来才会发现。
	for _, want := range []string{"name: alpha", "port: 1001", "note: 手写的备注", "run: go run ./cmd/alpha"} {
		if !strings.Contains(one, want) {
			t.Errorf("片段里没有 %q：\n%s", want, one)
		}
	}
	// 运行状态是界面投影才有的东西（端口在那边是字符串、还挂着占用与进程号），
	// 混进片段里贴到别人机器上就是一堆读不懂的字段。
	for _, bad := range []string{"running", "statusKey", "occupant", "pid"} {
		if strings.Contains(one, bad) {
			t.Errorf("片段里混进了运行状态 %q：\n%s", bad, one)
		}
	}
	if _, err := h.m.ServiceYAML("nope"); err == nil {
		t.Error("复制一个不存在的服务应当报错")
	}

	all, err := h.m.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(all, "name: "+n) {
			t.Errorf("导出的清单里没有 %s：\n%s", n, all)
		}
	}
	// 导出是只读动作：一份都不该写回数据文件，更不该碰来时的清单。
	h.assertBaseUntouched()
}

// ── 共享环境变量 ───────────────────────────────────────────────────────────

// 共享变量整份替换、存进数据文件、换来一次重新加载，清空之后整段消失。
func TestSaveSharedEnv(t *testing.T) {
	h := newHarness(t)
	before := h.reloads

	msg, err := h.m.SaveSharedEnv(map[string]string{"DB_HOST": "127.0.0.1", "DB_PORT": "5432"})
	if err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("回话该说清存了几条，得到 %q", msg)
	}
	if h.reloads <= before {
		t.Error("保存之后没有重新加载，界面与命令行还是旧的那份")
	}
	if got := h.onDisk().Env; len(got) != 2 || got["DB_HOST"] != "127.0.0.1" {
		t.Errorf("数据文件里的共享变量不对：%v", got)
	}
	// 服务自己的 env 与共享段是两处，改一处不该动另一处。
	if _, err := h.m.SaveService(ServiceIn{Name: "alpha", Dir: "a", Kind: "go",
		Env: map[string]string{"OWN": "x"}}); err != nil {
		t.Fatal(err)
	}
	if got := h.onDisk().Env; len(got) != 2 {
		t.Errorf("保存服务把共享段弄丢了：%v", got)
	}

	// 整份替换：第二次只写一条，前一条就该没了——界面上那个框里就是全部。
	if _, err := h.m.SaveSharedEnv(map[string]string{"DB_HOST": "10.0.0.9"}); err != nil {
		t.Fatal(err)
	}
	if got := h.onDisk().Env; len(got) != 1 || got["DB_HOST"] != "10.0.0.9" {
		t.Errorf("整份替换之后应当是仅剩一条：%v", got)
	}

	// 一条不剩就整个去掉，不留一个空对象。
	if _, err := h.m.SaveSharedEnv(nil); err != nil {
		t.Fatal(err)
	}
	if got := h.onDisk().Env; len(got) != 0 {
		t.Errorf("清空之后不该还有：%v", got)
	}
	h.assertBaseUntouched()
}

// ${} 只认得出合法变量名，收下一个做不到的名字，等于答应了一件谁也没法兑现的事。
func TestSaveSharedEnvRejectsBadName(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"A-B", "1A", "", "A B"} {
		if _, err := h.m.SaveSharedEnv(map[string]string{name: "1"}); err == nil {
			t.Errorf("%q 不该被收下", name)
		}
	}
	if len(h.onDisk().Env) != 0 {
		t.Error("被拒的那几次不该写进数据文件")
	}
}

// ── 没清单时一律拒绝 ───────────────────────────────────────────────────────

// 清单没加载出来时，每个需要清单的动作都必须拒绝，而不是拿着 nil 往下走。
func TestEveryMethodRefusesWithoutConfig(t *testing.T) {
	m := New(nil) // 故意不 SetConfig

	cases := []struct {
		what string
		run  func() error
	}{
		{"查端口占用", func() error { _, err := m.PortOwner("x"); return err }},
		{"结束占用进程", func() error { _, err := m.KillPortOwner("x", "1", ""); return err }},
		// 校验在取清单之前，所以这里要给一份能过校验的内容，
		// 否则量到的是校验错误，测不到「没有清单」这条路径。
		{"保存服务", func() error {
			_, err := m.SaveService(ServiceIn{Name: "x", Dir: "y", Kind: "go"})
			return err
		}},
		{"删除服务", func() error { _, err := m.DeleteService("x"); return err }},
		{"新建分组", func() error { _, err := m.CreateGroup("x"); return err }},
		{"分组改名", func() error { _, err := m.RenameGroup("a", "b"); return err }},
		{"删除分组", func() error { _, err := m.DeleteGroup("x"); return err }},
		{"检查目录", func() error { _, err := m.InspectDir("x", InspectHint{}); return err }},
		{"候选端口", func() error { _, err := m.PortCandidates(""); return err }},
		{"复制服务 YAML", func() error { _, err := m.ServiceYAML("x"); return err }},
		{"导出清单", func() error { _, err := m.ExportYAML(); return err }},
		{"保存共享环境变量", func() error {
			_, err := m.SaveSharedEnv(map[string]string{"A": "1"})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("没有清单时应当报错")
			}
			if !errors.Is(err, ErrNoConfig) {
				t.Errorf("错误 = %v，应当是 ErrNoConfig", err)
			}
		})
	}
}

// ── 端口扫描 ───────────────────────────────────────────────────────────────

// TestScanPortsFindsOurOwnListener 起一个真的监听，看它出不出来。
//
// 这一屏的全部价值在于「本机此刻真开着什么」，所以这里不塞假数据：真的 bind 一个
// 端口，再走一遍真正的 lsof。外部命令不在时跳过而不是失败——那是机器的事，
// 不是代码的事，而这条用例在装了 lsof 的机器上仍然是有意义的。
func TestScanPortsFindsOurOwnListener(t *testing.T) {
	if err := proc.PortToolsAvailable(); err != nil {
		t.Skipf("这台机器上看不了端口：%v", err)
	}
	h := newHarness(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	out, err := h.m.ScanPorts()
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	// 扫不动和「扫出来是空的」必须分得开：后者看着就像本机什么也没在跑。
	if !out.OK {
		t.Fatalf("扫描没成功：%s", out.Msg)
	}

	var hit *ScannedPort
	for i := range out.Ports {
		if out.Ports[i].Port == port {
			hit = &out.Ports[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("没扫到自己起的 %d（一共 %d 行）", port, len(out.Ports))
	}
	if hit.PID <= 0 {
		t.Errorf("没认出监听进程：%+v", hit)
	}
	// 命令行与界面必须显示同一个字符串：各缩各的迟早一个「~/w/a」一个「~/w/b」。
	if got := view.ShortPath(hit.Dir); got != hit.DirShort {
		t.Errorf("DirShort = %q，ShortPath(Dir) = %q", hit.DirShort, got)
	}
}

// TestScanPortsSortedByPort 钉着按端口升序。
//
// 不按「能不能纳管」分堆：那个判断在这一屏上看得到（就是有没有那几列），
// 而按它排序会让同一屏的顺序随进程起落跳来跳去。
func TestScanPortsSortedByPort(t *testing.T) {
	if err := proc.PortToolsAvailable(); err != nil {
		t.Skipf("这台机器上看不了端口：%v", err)
	}
	h := newHarness(t)
	out, err := h.m.ScanPorts()
	if err != nil || !out.OK {
		t.Skipf("扫描不可用：%v %s", err, out.Msg)
	}
	for i := 1; i < len(out.Ports); i++ {
		if out.Ports[i-1].Port > out.Ports[i].Port {
			t.Fatalf("第 %d 行 %d 排在了 %d 后面", i, out.Ports[i].Port, out.Ports[i-1].Port)
		}
	}
}

// TestScanPortsNeedsConfig 钉着没有清单时直接拒绝，而不是给一张空表。
func TestScanPortsNeedsConfig(t *testing.T) {
	m := New(nil)
	if _, err := m.ScanPorts(); !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v，想要 ErrNoConfig", err)
	}
}

// TestAdoptPortReadsTheLiveProcess 钉着纳管的语义：照它此刻的样子记下来。
//
// 端口一律用进程正在监听的那个，而不是项目文件里声明的那个。同一个项目在
// 另一个 profile、另一组环境变量下完全可能监听另一个端口，项目里写的那个
// 跟眼前这个进程没有关系。
func TestAdoptPortReadsTheLiveProcess(t *testing.T) {
	if err := proc.PortToolsAvailable(); err != nil {
		t.Skipf("这台机器上看不了端口：%v", err)
	}
	h := newHarness(t)

	// 在清单里的某个服务目录下起一个监听，让纳管能认出这是个什么项目。
	svc := h.svc("alpha")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	out, err := h.m.AdoptPort(strconv.Itoa(port), "被纳管的")
	if err != nil {
		t.Fatalf("纳管失败：%v", err)
	}
	if out.SuggestPort != port {
		t.Errorf("建议端口 = %d，想要 %d", out.SuggestPort, port)
	}
	if out.PortFrom == "" {
		t.Error("没说这个端口是怎么来的")
	}
	// 说清楚纳的是谁：点这一下的人要能核对「纳的确实是我看到的那个进程」。
	if !strings.Contains(out.Adopted, strconv.Itoa(os.Getpid())) {
		t.Errorf("Adopted = %q，没写出 PID", out.Adopted)
	}
	if out.SuggestName == "" {
		t.Error("没给出建议的服务名，界面预填不了")
	}
	_ = svc
}

// TestAdoptPortRefusesGonePort 钉着「端口上已经没有监听进程了」。
//
// 纳管是异步的：界面上那一屏可能已经过去几十秒，进程完全可能退出、端口被
// 另一个人接走。动手之前重新查一遍，而不是信带过来的那一行。
func TestAdoptPortRefusesGonePort(t *testing.T) {
	if err := proc.PortToolsAvailable(); err != nil {
		t.Skipf("这台机器上看不了端口：%v", err)
	}
	h := newHarness(t)

	// 先占一个端口再放掉，拿到的号此刻没人监听。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_, err = h.m.AdoptPort(strconv.Itoa(port), "x")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("err = %v，想要指出这个端口上已经没人了", err)
	}
}

// TestAdoptPortRejectsBadInput 钉着端口先过一遍格式，别到 lsof 那一步才发现。
func TestAdoptPortRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"", "abc", "0", "-1", "70000", "80a"} {
		if _, err := h.m.AdoptPort(bad, "x"); err == nil {
			t.Errorf("端口 %q 被接受了", bad)
		}
	}
}

// TestAdoptPortNeedsConfig 钉着没有清单时直接拒绝。
func TestAdoptPortNeedsConfig(t *testing.T) {
	m := New(nil)
	if _, err := m.AdoptPort("8080", "x"); !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v，想要 ErrNoConfig", err)
	}
}
