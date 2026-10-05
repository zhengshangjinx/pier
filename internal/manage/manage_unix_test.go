//go:build !windows

// 这两条都要造一份「有进程正在跑」的记录，办法是借当前测试进程自己所在的进程组。
// 那是 unix 的认领口径（PID + PGID）；Windows 上没有进程组，改看进程创建时间，
// 等价的两条测试要另写。

package manage

import (
	"os"
	"syscall"
	"testing"

	"github.com/zhengshangjinx/pier/internal/proc"
)

// 正在跑的服务拒绝改名：进程、日志、状态文件都按名字记账，改了就成孤儿。
//
// 改名只有一条路——在编辑表单里改名字，落盘的还是一次 SaveService。这道闸挂在
// SaveService 上，所以这里从那条路走。
func TestSaveServiceRefusesRenameWhileRunning(t *testing.T) {
	h := newHarness(t)
	self, pgid := os.Getpid(), syscall.Getpgrp()
	st := &proc.State{Services: map[string]*proc.Entry{"alpha": {PID: self, PGID: pgid}}}
	if err := st.Save(h.m.Config().StatePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.SaveService(ServiceIn{Name: "阿尔法", OrigName: "alpha", Dir: "a", Kind: "go"}); err == nil {
		t.Fatal("运行中的服务不该能改名")
	}
	if _, err := h.onDisk().Find("alpha"); err != nil {
		t.Error("被拒绝之后数据文件不该变")
	}

	// 编译中（只在动作簿记里露面）同样要拦。
	st.Services = map[string]*proc.Entry{}
	if err := st.Save(h.m.Config().StatePath()); err != nil {
		t.Fatal(err)
	}
	h.m.SetBusyProbe(func(name string) bool { return name == "alpha" })
	if _, err := h.m.SaveService(ServiceIn{Name: "阿尔法", OrigName: "alpha", Dir: "a", Kind: "go"}); err == nil {
		t.Error("正在启动中的服务不该能改名")
	}
	h.m.SetBusyProbe(func(string) bool { return false })
}

// 界面上的「被谁占」要认出自家服务：端口多半握在子进程手里，记录在案的 PID
// 只是那个壳，所以得按进程组认。这里借当前测试进程自己所在的组当样本。
func TestManagedNameClaimsByProcessGroup(t *testing.T) {
	h := newHarness(t)

	self, pgid := os.Getpid(), syscall.Getpgrp()
	st := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: self, PGID: pgid},
	}}
	if err := st.Save(h.m.Config().StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}

	if got, err := h.m.managedName(self); err != nil || got != "alpha" {
		t.Errorf("组里的进程应当认成 alpha，得到 %q（%v）", got, err)
	}
	if got, err := h.m.managedName(1); err != nil || got != "" {
		t.Errorf("别的进程组的进程不该被认领，得到 %q（%v）", got, err)
	}
	// 记录还在、进程早没了：PGID 可能已经被系统分给别人，不能照着组号认。
	stale := &proc.State{Services: map[string]*proc.Entry{
		"alpha": {PID: 1 << 22, PGID: pgid},
	}}
	if err := stale.Save(h.m.Config().StatePath()); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
	if got, err := h.m.managedName(self); err != nil || got != "" {
		t.Errorf("首领已经不在的记录不该被认领，得到 %q（%v）", got, err)
	}

	// 读不了和「不是自家服务」是两回事：前者必须报错，否则上面那道护栏
	// （是自家服务就不许结束进程）会静默失效。
	if err := os.WriteFile(h.m.Config().StatePath(), []byte("{ 半份"), 0o644); err != nil {
		t.Fatalf("写坏状态文件失败：%v", err)
	}
	if got, err := h.m.managedName(self); err == nil {
		t.Errorf("状态文件坏了应当报错，却得到 %q", got)
	}
}
