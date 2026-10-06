//go:build !windows

package panel

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// bulkHarness 起一个面板，装上一份有 manual 服务的清单。
//
// 名字带 @ 与中文：这一版新加的那条路（按分组挑）走的就是它们，
// 用纯 ASCII 铺数据的话，编码上出的问题在这里照不出来。
func bulkHarness(t *testing.T, yaml string) *Panel {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return p
}

// queued 报告此刻排着动作的那些服务名，排好序方便比对。
func queued(p *Panel) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.ops))
	for name := range p.ops {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

const bulkYAML = `
services:
  - name: mock
    dir: m
    kind: go
    manual: true
  - name: web
    dir: w
    kind: go
    depends_on: [api]
  - name: api
    dir: a
    kind: go
`

// 「全部启动」按下去不该带上标了 manual 的那几个——这正是那颗开关存在的意义。
//
// 同时钉住另一半：不带 manual 的一个都不许少。少了一个的话，用户看到的是
// 「按了全部启动，服务少了一个」，而屏幕上没有任何地方说得清为什么。
func TestStartAllSkipsManual(t *testing.T) {
	p := bulkHarness(t, bulkYAML)

	if _, err := p.StartAll(); err != nil {
		t.Fatalf("全部启动失败：%v", err)
	}
	got := queued(p)
	if len(got) != 2 || got[0] != "api" || got[1] != "web" {
		t.Errorf("排进队列的是 %v，想要 [api web]（mock 不参与全部）", got)
	}
}

// 全部停止与全部启动是同一份名单。只排除启动那一半的话，pier restart
// 会把 manual 的服务停掉却不再拉起来——静默地少一个服务，界面上还全都正常。
func TestStopAllSkipsManual(t *testing.T) {
	p := bulkHarness(t, bulkYAML)

	// 排上就够：Stop 在入队之前不挑状态（停一个本来就没跑的也会走完一条
	// 「已经退出」的路），所以看队列就是看名单。
	if _, err := p.StopAll(); err != nil {
		t.Fatalf("全部停止失败：%v", err)
	}
	got := queued(p)
	if len(got) != 2 || got[0] != "api" || got[1] != "web" {
		t.Errorf("排进队列的是 %v，想要 [api web]（mock 不参与全部）", got)
	}
}

// 点名的照做：manual 说的是「别在全部里带上我」，不是「谁都不许动我」。
func TestNamedStartStillReachesManual(t *testing.T) {
	p := bulkHarness(t, bulkYAML)

	if _, err := p.Start("mock"); err != nil {
		t.Fatalf("点名启动失败：%v", err)
	}
	if got := queued(p); len(got) != 1 || got[0] != "mock" {
		t.Errorf("排进队列的是 %v，想要 [mock]", got)
	}
}
