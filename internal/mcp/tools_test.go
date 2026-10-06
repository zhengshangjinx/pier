package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/panel"
)

// 这一份测那七个工具本身：它们看到了什么、说出来的话对不对。
// 协议那一层在 mcp_test.go 里。

// newTestPanel 起一个面板，清单放在临时目录里。
//
// 三条服务都用 shell 而**不给 run**：这个姿势最安全——`Plan()` 当场就报
// 「必须显式给出 run」，一个进程都不会真的起来，用例跑完不会在机器上留下一串
// 睡着的服务。失败态正好是这几条要用到的那一种：起不来之后状态里会留下原因，
// 而「起不来时说什么」比「起来了说什么」更要紧。
func newTestPanel(t *testing.T) *panel.Panel {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())

	base := filepath.Join(t.TempDir(), "pier.yaml")
	const yaml = `
services:
  - name: api
    dir: a
    kind: shell
    group: 后端
    port: 18080
    health: "cmd: true"
  - name: web
    dir: w
    kind: shell
    group: 前端
    depends_on: [api]
  - name: cron
    dir: c
    kind: shell
    group: 后端
    manual: true
`
	if err := os.WriteFile(base, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := panel.New()
	t.Cleanup(func() {
		settle(t, p)
		p.Close()
	})
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return p
}

// settle 等队列跑空，理由与 internal/api 那一份相同：动作是异步的，队列里还
// 留着东西时结束用例，tempdir 的清理会撞见一个刚被创建出来的目录。
func settle(t *testing.T, p *panel.Panel) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if p.State().BusyCount == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("队列一直没跑空，可能有动作卡住了")
}

// toolText 调一个工具，返回它给的那段字与 isError。
func toolText(t *testing.T, s *session, id float64, name, args string) (string, bool) {
	t.Helper()
	m := s.call(id, name, args)
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s 没有 result：%s", name, dump(t, m))
	}
	isErr, _ := res["isError"].(bool)
	return textOf(t, m), isErr
}

// ── 工具表 ─────────────────────────────────────────────────────────────────

// 工具就是路线图上写的那七个，名字与顺序都一样。
//
// 顺序也钉着：tools/list 是按这个顺序递到模型手上的，而摆在前面的那几个
// （先看有什么、再看某一个怎么了）本来就是它该先走的。
func TestToolsAreTheDocumentedSeven(t *testing.T) {
	p := newTestPanel(t)
	var names []string
	for _, tool := range Tools(p) {
		names = append(names, tool.Name)
	}
	want := []string{
		"list_services", "service_status", "start_service", "stop_service",
		"restart_service", "read_logs", "wait_ready",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("工具表 = %v，想要 %v", names, want)
	}
}

// 真的那七个工具的 schema 与它们的入参结构体也对得上（假的那些在 mcp_test 里测过）。
func TestRealToolSchemasMatchParams(t *testing.T) {
	p := newTestPanel(t)
	for _, tool := range Tools(p) {
		checkSchema(t, tool)
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatalf("%s 的 schema 解不开：%v", tool.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s 的 schema 没有关掉 additionalProperties", tool.Name)
		}
	}
}

// 每个工具都得有说明：模型靠那一句决定用不用它。
func TestEveryToolExplainsItself(t *testing.T) {
	p := newTestPanel(t)
	for _, tool := range Tools(p) {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s 没有说明", tool.Name)
		}
		if strings.TrimSpace(tool.Title) == "" {
			t.Errorf("%s 没有标题", tool.Name)
		}
	}
}

// ── list_services ──────────────────────────────────────────────────────────

func TestListServices(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "list_services", `{}`)
	if isErr {
		t.Fatalf("list_services 失败了：%s", text)
	}
	for _, want := range []string{"共 3 个服务，0 个在跑", "api", "web", "cron", "后端", "前端", "未启动"} {
		if !strings.Contains(text, want) {
			t.Errorf("回执里没有 %q：\n%s", want, text)
		}
	}
	// 标了「不参与全部启停」的要在这一行里就说清楚：它是「按了全部启动也不会起来」
	// 的那一个，不说的话看着就像漏了。
	if !strings.Contains(text, "不参与全部启停") {
		t.Errorf("没有标出 manual 的服务：\n%s", text)
	}
}

// ── service_status ─────────────────────────────────────────────────────────

func TestServiceStatus(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "service_status", `{"name":"api"}`)
	if isErr {
		t.Fatalf("service_status 失败了：%s", text)
	}
	for _, want := range []string{"服务：api", "分组：后端", "状态：未启动", "端口：18080", "健康探针"} {
		if !strings.Contains(text, want) {
			t.Errorf("回执里没有 %q：\n%s", want, text)
		}
	}
	// 清单里配了 health、但探针还没跑过：这一档要看得见（它在界面上是「未检查」，
	// 与「没配」只差一个字，含义差得远）。
	if !strings.Contains(text, "未检查") {
		t.Errorf("配了 health 却没标出探针还没跑过：\n%s", text)
	}
}

// 名字写错时要把现有的名字都列出来：模型看不到侧栏，没有这一句它只能瞎猜。
func TestServiceStatusUnknownName(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "service_status", `{"name":"api2"}`)
	if !isErr {
		t.Fatal("名字不存在时没有报错")
	}
	for _, want := range []string{"api2", "api", "web", "cron"} {
		if !strings.Contains(text, want) {
			t.Errorf("回执里没有 %q：%s", want, text)
		}
	}
}

// ── 批量动作 ───────────────────────────────────────────────────────────────

// 按分组点名要连带把跨组的前置拉进来，并说明白多出来的那一截是从哪儿来的。
func TestStartServiceByGroupPullsDeps(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "start_service", `{"group":"前端"}`)
	if isErr {
		t.Fatalf("按分组启动失败了：%s", text)
	}
	if !strings.Contains(text, "「前端」这一组") {
		t.Errorf("回执里没说动的是哪一批：%s", text)
	}
	if !strings.Contains(text, "前置") || !strings.Contains(text, "api") {
		t.Errorf("回执里没说明带上了前置 api：%s", text)
	}
	// 「起完就完了」与「起完了」是两件事，这一句是模型接着调 wait_ready 的依据。
	if !strings.Contains(text, "wait_ready") {
		t.Errorf("回执里没有说下一步该等就绪：%s", text)
	}
}

// 明明白白给一个空数组是「说错了」，不是「全部」。
//
// 当成全部执行的话，一次手滑动的就是整份清单——这个代价与当场报错差得太远。
func TestEmptyNamesIsRejected(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	for _, tool := range []string{"start_service", "stop_service", "restart_service"} {
		text, isErr := toolText(t, s, 1, tool, `{"names":[]}`)
		if !isErr {
			t.Errorf("%s 收下了一个空的 names：%s", tool, text)
		}
		if !strings.Contains(text, "names") {
			t.Errorf("%s 的报错里没说清是哪个参数：%s", tool, text)
		}
	}
}

// names 与 group 一起给是说不清该听谁的，当场拒掉。
func TestNamesAndGroupTogetherIsRejected(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "start_service", `{"names":["api"],"group":"前端"}`)
	if !isErr {
		t.Fatalf("两个都给也照做了：%s", text)
	}
	if !strings.Contains(text, "只能给一个") {
		t.Errorf("报错没说清为什么：%s", text)
	}
}

// 不存在的分组要把现有的分组列出来。
func TestUnknownGroupListsGroups(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "start_service", `{"group":"前端1"}`)
	if !isErr {
		t.Fatal("不存在的分组没有报错")
	}
	for _, want := range []string{"前端1", "前端", "后端"} {
		if !strings.Contains(text, want) {
			t.Errorf("回执里没有 %q：%s", want, text)
		}
	}
}

// 起完之后列表里要看得见「上次起不来」，而且带着原因。
//
// 这一条串起了三个工具：动作排进去（start_service）、服务起来失败（清单里没有 run）、
// 下一次 list_services 必须让模型看见它，否则它只知道「排了队」。
func TestStartFailureShowsUpInList(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	if text, isErr := toolText(t, s, 1, "start_service", `{"names":["api"]}`); isErr {
		t.Fatalf("启动失败了：%s", text)
	}
	settle(t, p)

	text, isErr := toolText(t, s, 2, "list_services", `{}`)
	if isErr {
		t.Fatalf("list_services 失败了：%s", text)
	}
	if !strings.Contains(text, "上次起不来") || !strings.Contains(text, "run") {
		t.Errorf("列表里没有那条失败与它的原因：\n%s", text)
	}

	// 详情里那句原因也在。
	detail, isErr := toolText(t, s, 3, "service_status", `{"name":"api"}`)
	if isErr {
		t.Fatalf("service_status 失败了：%s", detail)
	}
	if !strings.Contains(detail, "上次失败") {
		t.Errorf("详情里没有失败原因：\n%s", detail)
	}
}

// ── read_logs ──────────────────────────────────────────────────────────────

// writeLog 往这个服务今天的日志文件里写几行。
func writeLog(t *testing.T, p *panel.Panel, name string, lines []string) string {
	t.Helper()
	for _, svc := range p.State().Services {
		if svc.Name != name {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(svc.LogPath), 0o755); err != nil {
			t.Fatalf("建日志目录失败：%v", err)
		}
		body := strings.Join(lines, "\n") + "\n"
		if err := os.WriteFile(svc.LogPath, []byte(body), 0o644); err != nil {
			t.Fatalf("写日志失败：%v", err)
		}
		return svc.LogPath
	}
	t.Fatalf("清单里没有 %s", name)
	return ""
}

func TestReadLogsTails(t *testing.T) {
	p := newTestPanel(t)
	path := writeLog(t, p, "web", []string{"第一行", "第二行", "第三行", "第四行", "第五行"})
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "read_logs", `{"name":"web","lines":2}`)
	if isErr {
		t.Fatalf("read_logs 失败了：%s", text)
	}
	if !strings.Contains(text, "第四行\n第五行") {
		t.Errorf("回执里不是最后两行：\n%s", text)
	}
	if strings.Contains(text, "第三行") {
		t.Errorf("回执里多给了更早的行：\n%s", text)
	}
	// 末尾那个换行不算一行：上面那五行就该是五行。
	if !strings.Contains(text, "最后 2 行") {
		t.Errorf("回执里没写清给了几行：\n%s", text)
	}
	if !strings.Contains(text, filepath.Base(path)) {
		t.Errorf("回执里没有日志文件在哪：\n%s", text)
	}
}

// 还没写过日志不是错误：服务可能还没起来过。
func TestReadLogsWithoutFile(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "read_logs", `{"name":"web"}`)
	if isErr {
		t.Fatalf("没有日志被当成了失败：%s", text)
	}
	if !strings.Contains(text, "还没有日志") {
		t.Errorf("回执不对：%s", text)
	}
}

// 日期形状不对当场拒掉，不能悄悄读成今天的。
//
// 面板认不出日期时会当成「没给」，于是「10 月 5 号怎么了」这个问题的答案是
// 今天的日志——一句错话，而且看上去完全正常。
func TestReadLogsBadDate(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "read_logs", `{"name":"web","date":"2026/01/01"}`)
	if !isErr {
		t.Fatalf("坏日期被收下了：%s", text)
	}
	if !strings.Contains(text, config.LogDateLayout) {
		t.Errorf("报错里没写该写成什么样：%s", text)
	}
}

// 那天没有日志时，把有日志的那些天列出来。
func TestReadLogsMissingDate(t *testing.T) {
	p := newTestPanel(t)
	writeLog(t, p, "web", []string{"今天写了点什么"})
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "read_logs", `{"name":"web","date":"2001-01-01"}`)
	if isErr {
		t.Fatalf("那天没有日志被当成了失败：%s", text)
	}
	today := time.Now().Format(config.LogDateLayout)
	if !strings.Contains(text, today) {
		t.Errorf("回执里没列出有日志的天（%s）：%s", today, text)
	}
}

func TestReadLogsUnknownService(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	if text, isErr := toolText(t, s, 1, "read_logs", `{"name":"没有这个"}`); !isErr {
		t.Errorf("不存在的服务没有报错：%s", text)
	}
}

// ── wait_ready ─────────────────────────────────────────────────────────────

// 「没等到」是一个答案，不是这次调用出了错——回执里要写清楚卡在哪一步。
//
// 三类原因分开说，因为下一步各不相同：没配探针的要往清单里加 health、
// 没在跑的要先去起它。合成一句「没就绪」，拿到它的人只知道继续等。
func TestWaitReadyExplainsWhyNot(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	// api 配了探针但没在跑；web 压根没配探针。两者都不必等窗口走完。
	text, isErr := toolText(t, s, 1, "wait_ready", `{"names":["api","web"]}`)
	if isErr {
		t.Fatalf("没等到被当成了调用失败：%s", text)
	}
	if !strings.Contains(text, "0 / 2 就绪") {
		t.Errorf("回执里没有那行合计：\n%s", text)
	}
	if !strings.Contains(text, "没有在运行") {
		t.Errorf("没在跑的那个没说清楚：\n%s", text)
	}
	if !strings.Contains(text, "没配健康探针") {
		t.Errorf("没配探针的那个没说清楚：\n%s", text)
	}
	if !strings.Contains(text, "没等到：") {
		t.Errorf("回执里没有那句总结：\n%s", text)
	}
}

// 坏的超时写法当场拒掉，不默认成三分钟。
func TestWaitReadyBadTimeout(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	if text, isErr := toolText(t, s, 1, "wait_ready", `{"names":["api"],"timeout":"一会儿"}`); !isErr {
		t.Errorf("坏的超时被收下了：%s", text)
	}
}

// ── 停止 ───────────────────────────────────────────────────────────────────

// 停一个本来就没在跑的服务不算失败，回执里说清楚做了什么。
func TestStopService(t *testing.T) {
	p := newTestPanel(t)
	s := newSession(t, Tools(p))
	defer s.close()

	text, isErr := toolText(t, s, 1, "stop_service", `{"names":["api"]}`)
	if isErr {
		t.Fatalf("停止失败了：%s", text)
	}
	if !strings.Contains(text, "停止") {
		t.Errorf("回执没说做了什么：%s", text)
	}
	settle(t, p)
}
