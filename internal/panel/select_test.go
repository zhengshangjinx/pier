//go:build !windows

// 这一份要造一条「有进程正在跑」的记录，借的是当前测试进程自己所在的进程组
// （unix 的认领口径，见 internal/manage/manage_unix_test.go 同一件事）。

package panel

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// selectYAML 铺一份带分组、带依赖、还带一个 manual 的清单。
//
// 服务的排列顺序刻意与依赖反着来（依赖写在被依赖的前面）：这样排列出来的执行
// 顺序一定与清单顺序不同，下面那几条断言才真的在测「顺序」这件事，
// 而不是在核对清单自己是按什么顺序写的。
const selectYAML = `
services:
  - name: web
    dir: w
    kind: shell
    group: 前端
    depends_on: [api]
  - name: api
    dir: a
    kind: shell
    group: 后端
    depends_on: [db]
  - name: db
    dir: d
    kind: shell
    group: 后端
  - name: mock
    dir: m
    kind: shell
    group: 前端
    manual: true
`

// pickedNames 把一次挑选摊成名字，顺序就是它将来的执行顺序。
func pickedNames(t *testing.T, p *Panel, sel Selection, kind string) []string {
	t.Helper()
	svcs, err := p.pick(p.Config(), sel, kind)
	if err != nil {
		t.Fatalf("挑选失败：%v", err)
	}
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return out
}

// 挑出来的这一批要按依赖排好，不是按名字给进来的顺序。
func TestPickOrdersByDependency(t *testing.T) {
	p := bulkHarness(t, selectYAML)

	if got := pickedNames(t, p, Selection{Names: []string{"web"}}, "start"); strings.Join(got, ",") != "web" {
		t.Errorf("启动顺序是 %v，想要 web 自己一个", got)
	}
	// 停止反着来：先起的那一批往往是被依赖的，先停它们等于让还在跑的服务
	// 对着一个已经关掉的端口刷连接错误。
	if got := pickedNames(t, p, Selection{}, "stop"); strings.Join(got, ",") != "web,api,db" {
		t.Errorf("全部停止的顺序是 %v，想要 web,api,db", got)
	}
	if got := pickedNames(t, p, Selection{}, "start"); strings.Join(got, ",") != "db,api,web" {
		t.Errorf("全部启动的顺序是 %v，想要 db,api,web", got)
	}
}

// 按分组启动要连带把前置拉起，哪怕前置在别的分组里；分组里标了 manual 的那个
// 也要起——manual 说的是「别在全部里带上我」，不是「谁都不许动我」。
func TestStartSetByGroupPullsPrerequisites(t *testing.T) {
	p := bulkHarness(t, selectYAML)

	msg, err := p.StartSet(Selection{Group: "前端"})
	if err != nil {
		t.Fatalf("按分组启动失败：%v", err)
	}
	if got := queued(p); strings.Join(got, ",") != "api,db,mock,web" {
		t.Errorf("排进队列的是 %v，想要 api,db,mock,web（含跨分组的前置与组内 manual）", got)
	}
	// 名单比用户点的多，要说清楚多出来的那一截是从哪儿来的。
	if !strings.Contains(msg, "4 个服务") || !strings.Contains(msg, "db、api") {
		t.Errorf("回执是 %q，想要带上个数与那两条前置", msg)
	}
}

// 「全部」不走补前置那一步：那时候整份清单本来就在名单里，再补一次会把标了
// manual 的前置也拉起来，而那正是那颗开关要挡住的事。
func TestStartSetAllDoesNotPullManual(t *testing.T) {
	p := bulkHarness(t, selectYAML)

	msg, err := p.StartSet(Selection{})
	if err != nil {
		t.Fatalf("全部启动失败：%v", err)
	}
	if got := queued(p); strings.Join(got, ",") != "api,db,web" {
		t.Errorf("排进队列的是 %v，想要 api,db,web（mock 不参与全部）", got)
	}
	if strings.Contains(msg, "前置") {
		t.Errorf("回执是 %q，全部启动不该提前置", msg)
	}
}

// 停止也是同一份选择：按分组停，组里标了 manual 的照样停——只排除启动那一半的话，
// 一个分组会停不干净，而界面上看不出少了谁。
func TestStopSetByGroupKeepsManual(t *testing.T) {
	p := bulkHarness(t, selectYAML)

	msg, err := p.StopSet(Selection{Group: "前端"})
	if err != nil {
		t.Fatalf("按分组停止失败：%v", err)
	}
	if got := queued(p); strings.Join(got, ",") != "mock,web" {
		t.Errorf("排进队列的是 %v，想要 mock,web", got)
	}
	if !strings.Contains(msg, "2 个服务") {
		t.Errorf("回执是 %q，想要说清楚发了几个", msg)
	}
}

// 写错的选择要能被认出来，而且是两种不同的错：写法不对（400）与清单里没有（404）
// 在接口上是两个状态码，脚本按它分流。
func TestSelectionErrorsAreDistinguishable(t *testing.T) {
	p := bulkHarness(t, selectYAML)

	_, err := p.pick(p.Config(), Selection{Names: []string{"web"}, Group: "前端"}, "start")
	if !errors.Is(err, ErrBadSelection) {
		t.Errorf("两个字段都给：%v，想要 ErrBadSelection", err)
	}

	_, err = p.pick(p.Config(), Selection{Names: []string{"没有这个服务"}}, "start")
	if !errors.Is(err, ErrUnknownSelection) {
		t.Errorf("名字写错：%v，想要 ErrUnknownSelection", err)
	}

	// 分组写错时要把现有的分组名带上：MCP 那一侧的模型手上没有侧栏可以数，
	// 而「没有这个分组」四个字不会告诉它该写哪个。
	_, err = p.pick(p.Config(), Selection{Group: "前端组"}, "start")
	if !errors.Is(err, ErrUnknownSelection) {
		t.Errorf("分组写错：%v，想要 ErrUnknownSelection", err)
	}
	for _, want := range []string{"前端组", "前端", "后端"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错里没有 %q：%v", want, err)
		}
	}
	// 服务全都有分组时，「未分组」不在现有名单里——它是没写 group 的那些的去处，
	// 而此刻没有这种服务。这不是漏了一条，是它本来就没什么可指的。
	if strings.Contains(err.Error(), config.UngroupedName) {
		t.Errorf("清单里没有未分组的服务，不该把它列进现有分组：%v", err)
	}
}

// 建过、还空着的分组与压根不存在的分组要分两句说。
//
// 前者是用户自己在侧栏建了一个「压测」分组、还没往里放服务（界面上那一页是空的，
// 看着像坏了），后者是名字写错了。两句都只说「没有」，用户就只能回去数侧栏。
func TestPickTellsEmptyGroupFromMissingOne(t *testing.T) {
	p := bulkHarness(t, selectYAML)
	// 在内存里建一个空分组就够：这一句判定读的就是 AllGroups。
	p.Config().AddGroup("压测")

	_, err := p.pick(p.Config(), Selection{Group: "压测"}, "start")
	if !errors.Is(err, ErrUnknownSelection) {
		t.Errorf("空分组：%v，想要 ErrUnknownSelection", err)
	}
	if !strings.Contains(err.Error(), "还没有服务") {
		t.Errorf("空分组的说法是 %q，想要说的是「组里还没有服务」", err)
	}

	_, err = p.pick(p.Config(), Selection{Group: "压测组"}, "start")
	if !strings.Contains(err.Error(), "现有：") {
		t.Errorf("不存在的分组该把现有的列出来：%v", err)
	}
}

// 「等服务就绪」要认得出「它正起着」：接口与 MCP 上这句几乎总是紧跟在一句启动
// 后面，那时它还在排队或编译，状态里一条记录都没有。直接把名字交给探针那一份
// 判定，会当场回一句「没有在运行，先起它」——可它明明正起着。
func TestWaitSetWaitsForAStartStillInFlight(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer ready.Close()

	p := bulkHarness(t, fmt.Sprintf(`
services:
  - name: api
    dir: a
    kind: shell
    health: %s/health
`, ready.URL))

	// 假装它正启动着：簿记里挂一条排队中的动作，300ms 后撤掉并补上一条活记录。
	// 顺序就是这个顺序——先落状态再撤动作，两者中间不该有「既没在跑、又没在动」的空档。
	p.mu.Lock()
	p.ops["api"] = newOp("start", "queued")
	p.mu.Unlock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		self, pgid := os.Getpid(), syscall.Getpgrp()
		_ = proc.UpdateState(p.Config().StatePath(), func(st *proc.State) error {
			st.Services["api"] = &proc.Entry{PID: self, PGID: pgid, StartedAt: time.Now()}
			return nil
		})
		p.mu.Lock()
		delete(p.ops, "api")
		p.mu.Unlock()
	}()

	start := time.Now()
	out, err := p.WaitSet(Selection{Names: []string{"api"}}, 5*time.Second)
	if err != nil {
		t.Fatalf("等就绪失败：%v", err)
	}
	if len(out) != 1 || !out[0].Ready {
		t.Fatalf("结果 %+v，想要一条已就绪", out)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Errorf("等了 %v 就回来了：它还在起的时候不该先去探", el)
	}
}
