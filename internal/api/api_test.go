package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/panel"
)

// ── 文档与代码的一致性 ─────────────────────────────────────────────────────
//
// 这两条是这份文档能信的全部理由。手写的文档一旦和代码走散，照着它写脚本的人
// 会在一个不存在的字段上卡住，而报错信息看起来像是他自己写错了。

// specServiceProps 解出文档里 Service 那一节写了哪些字段。
func specServiceProps(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Components struct {
			Schemas struct {
				Service struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"Service"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(openAPISpec), &doc); err != nil {
		t.Fatalf("文档不是合法 JSON：%v", err)
	}
	props := doc.Components.Schemas.Service.Properties
	if len(props) == 0 {
		t.Fatal("文档里没有 Service.properties")
	}
	return props
}

// TestSpecServiceMatchesStruct 钉着文档里的 Service 字段与真正返回的结构一致。
//
// 字段名对不上的代价是实打实的：文档里写着 state，返回的却是 statusKey，
// 照着文档写的脚本在读第一个字段时就会崩，而它崩在自己的代码上。
func TestSpecServiceMatchesStruct(t *testing.T) {
	props := specServiceProps(t)

	inStruct := map[string]bool{}
	rt := reflect.TypeOf(panel.ServiceOut{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			t.Errorf("ServiceOut 的 %s 没有可用的 json 标签", rt.Field(i).Name)
			continue
		}
		inStruct[name] = true
		if _, ok := props[name]; !ok {
			t.Errorf("文档里少了 %s（ServiceOut.%s）", name, rt.Field(i).Name)
		}
	}

	var extra []string
	for name := range props {
		if !inStruct[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("文档里多写了结构里没有的字段：%v", extra)
	}
}

// TestSpecPathsMatchRoutes 钉着文档里写到的路径与方法就是真正挂上去的那些。
//
// 加了接口忘了改文档，用户照着文档调会打到一个不存在的路径上，回来的 404
// 看起来像「服务没找到」；反过来的话，一个能用的接口没人知道它存在。
func TestSpecPathsMatchRoutes(t *testing.T) {
	doc, err := specPaths()
	if err != nil {
		t.Fatalf("文档解析失败：%v", err)
	}

	srv := &Server{panel: nil, token: "x"}
	real := map[string]map[string]bool{}
	for _, r := range srv.routes() {
		if real[r.pattern] == nil {
			real[r.pattern] = map[string]bool{}
		}
		real[r.pattern][strings.ToLower(r.method)] = true
	}

	for p, methods := range real {
		dm, ok := doc[p]
		if !ok {
			t.Errorf("代码里有 %s，文档里没写", p)
			continue
		}
		for m := range methods {
			if !dm[m] {
				t.Errorf("代码里有 %s %s，文档里没写", m, p)
			}
		}
		for m := range dm {
			if !methods[m] {
				t.Errorf("文档里写了 %s %s，代码里没有", m, p)
			}
		}
	}
	for p := range doc {
		if _, ok := real[p]; !ok {
			t.Errorf("文档里写了 %s，代码里没有", p)
		}
	}
}

// TestSpecIsValidJSON 顺带把整份文档过一遍解析。上面两条只解了各自要用的那几节，
// 别处的括号写错一样能让接口返回一份坏掉的文档。
func TestSpecIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(openAPISpec), &v); err != nil {
		t.Fatalf("文档不是合法 JSON：%v", err)
	}
	if v["openapi"] != "3.1.0" {
		t.Errorf("openapi 版本 = %v，想要 3.1.0", v["openapi"])
	}
	if _, ok := v["paths"].(map[string]any); !ok {
		t.Error("文档里没有 paths")
	}
}

// ── 令牌 ───────────────────────────────────────────────────────────────────

func TestBearer(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
		{"BEARER abc", "abc"},
		{"abc", "abc"},             // 少了方案名也认：手写这个头的人常漏
		{"  Bearer  abc  ", "abc"}, // 两边空白
		{"", ""},
		{"Bearer", "Bearer"}, // 只有一个词，不当方案名
	}
	for _, c := range cases {
		if got := bearer(c.in); got != c.want {
			t.Errorf("bearer(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if len(a) != TokenLen*2 {
		t.Errorf("令牌长度 = %d，想要 %d", len(a), TokenLen*2)
	}
	b, _ := NewToken()
	if a == b {
		t.Error("两次生成的令牌一样")
	}
}

// TestTokenPersists 钉着「重启不换令牌」。每次现生成的话，写好的脚本隔天就跑不通了。
func TestTokenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	first, err := Token(path)
	if err != nil {
		t.Fatalf("取令牌失败：%v", err)
	}
	again, err := Token(path)
	if err != nil {
		t.Fatalf("再取令牌失败：%v", err)
	}
	if first != again {
		t.Errorf("两次取到的令牌不同：%q / %q", first, again)
	}

	rotated, err := RotateToken(path)
	if err != nil {
		t.Fatalf("换令牌失败：%v", err)
	}
	if rotated == first {
		t.Error("换过之后还是原来那个")
	}
	after, _ := Token(path)
	if after != rotated {
		t.Errorf("换过之后取到的是 %q，想要 %q", after, rotated)
	}
}

func TestPortDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if got := Port(path); got != DefaultPort {
		t.Errorf("没设过时 Port = %d，想要 %d", got, DefaultPort)
	}
	if err := SetPort(path, 8123); err != nil {
		t.Fatalf("写端口失败：%v", err)
	}
	if got := Port(path); got != 8123 {
		t.Errorf("设过之后 Port = %d，想要 8123", got)
	}
	// 越界的值一律退回默认：宁可监听一个固定的号，也不要拿一个非法的值去 bind。
	if err := SetPort(path, 0); err != nil {
		t.Fatalf("写端口失败：%v", err)
	}
	if got := Port(path); got != DefaultPort {
		t.Errorf("0 时 Port = %d，想要 %d", got, DefaultPort)
	}
}

// ── 路由与鉴权 ─────────────────────────────────────────────────────────────

const apiFixtureYAML = `
services:
  - name: alpha
    dir: a
    kind: go
  - name: beta
    dir: b
    kind: go
`

// newTestServer 起一个连着临时清单的接口服务。全程不碰真实数据目录。
func newTestServer(t *testing.T, token string) *Server {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())

	dir := t.TempDir()
	base := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(base, []byte(apiFixtureYAML), 0o644); err != nil {
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
	p.SetSource("测试清单")
	return New(p, "127.0.0.1:0", token)
}

// settle 等队列跑空。
//
// 启停是异步的：接口回了 202 只说明排上了队，worker 还在后面慢慢跑，而它会往
// 清单旁边的 .pier 里写日志目录。不等到它收工就结束用例，tempdir 的清理会在
// 半路上撞见一个刚被创建出来的目录，报一句和被测行为毫无关系的「目录非空」。
//
// 等待是有依据的而不是睡够就完：动作在入队之前就登记在案了（见 panel.begin），
// 所以 202 回来时 BusyCount 一定大于 0，不会出现「还没开始就被当成跑完了」。
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

// call 发一个请求，返回状态码与解好的包体。
func call(t *testing.T, s *Server, method, path, token string) (int, map[string]any) {
	t.Helper()
	return callBody(t, s, method, path, token, "")
}

// callBody 与 call 相同，另外带一个请求体。空串表示不带体。
func callBody(t *testing.T, s *Server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s 的回包不是 JSON：%v\n%s", method, path, err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestHealthAndSpecNeedNoToken 钉着这两个入口不查令牌。
// 脚本在拿到令牌之前就要能问「Pier 在不在」。
func TestHealthAndSpecNeedNoToken(t *testing.T) {
	s := newTestServer(t, "secret")

	code, body := call(t, s, "GET", "/health", "")
	if code != http.StatusOK {
		t.Fatalf("/health 状态码 = %d，想要 200", code)
	}
	if body["ok"] != true || body["app"] != "pier" {
		t.Errorf("/health 回包 = %v", body)
	}
	if body["msg"] != "测试清单" {
		t.Errorf("/health 里的 msp = %v，想要「测试清单」", body["msg"])
	}

	code, _ = call(t, s, "GET", "/openapi.json", "")
	if code != http.StatusOK {
		t.Errorf("/openapi.json 状态码 = %d，想要 200", code)
	}
}

// TestAPIRoutesNeedToken 是这一层存在的理由：没有它，你随手打开的任何一个网页
// 都能把你正在跑的服务全停掉。
func TestAPIRoutesNeedToken(t *testing.T) {
	s := newTestServer(t, "secret")

	paths := []struct{ method, path string }{
		{"GET", "/api/state"},
		{"GET", "/api/services"},
		{"GET", "/api/services/alpha"},
		{"POST", "/api/services/start"},
		{"POST", "/api/services/stop"},
		{"POST", "/api/services/wait"},
		{"POST", "/api/services/alpha/start"},
		{"POST", "/api/services/alpha/stop"},
		{"GET", "/api/services/alpha/logs"},
	}
	for _, p := range paths {
		code, _ := call(t, s, p.method, p.path, "")
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s 不带令牌状态码 = %d，想要 401", p.method, p.path, code)
		}
		code, _ = call(t, s, p.method, p.path, "错的令牌")
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s 错令牌状态码 = %d，想要 401", p.method, p.path, code)
		}
	}

	code, body := call(t, s, "GET", "/api/services", "secret")
	if code != http.StatusOK {
		t.Fatalf("带令牌状态码 = %d，想要 200（%v）", code, body)
	}
	svcs, _ := body["services"].([]any)
	if len(svcs) != 2 {
		t.Fatalf("服务数 = %d，想要 2", len(svcs))
	}
}

// TestEmptyTokenRejectsEverything 钉着「令牌没生成时不放行」。
// 配置文件写坏了导致令牌是空串，绝不能被当成「不用令牌」。
func TestEmptyTokenRejectsEverything(t *testing.T) {
	s := newTestServer(t, "")
	code, _ := call(t, s, "GET", "/api/services", "")
	if code != http.StatusUnauthorized {
		t.Errorf("状态码 = %d，想要 401", code)
	}
}

func TestUnknownServiceIs404(t *testing.T) {
	s := newTestServer(t, "secret")

	for _, p := range []struct{ method, path string }{
		{"GET", "/api/services/nope"},
		{"POST", "/api/services/nope/start"},
		{"POST", "/api/services/nope/stop"},
		{"GET", "/api/services/nope/logs"},
	} {
		code, body := call(t, s, p.method, p.path, "secret")
		if code != http.StatusNotFound {
			t.Errorf("%s %s 状态码 = %d，想要 404", p.method, p.path, code)
		}
		if msg, _ := body["msg"].(string); !strings.Contains(msg, "nope") {
			t.Errorf("%s %s 的回包没说是哪个服务：%v", p.method, p.path, body)
		}
	}
}

// TestStartAcceptsAndStopConflicts 钉着这两个状态码。
//
// 202 而不是 200：排队成功不等于服务已经跑起来了，Maven 编译要几分钟，
// 回 200 会让人以为可以往下走了。409 用在「服务在清单里，只是此刻不能动它」。
func TestStartAcceptsAndStopConflicts(t *testing.T) {
	s := newTestServer(t, "secret")

	// 还没跑的服务：排进队列。
	code, body := call(t, s, "POST", "/api/services/alpha/stop", "secret")
	if code != http.StatusAccepted && code != http.StatusConflict {
		t.Fatalf("停一个没在跑的服务状态码 = %d，想要 202 或 409（%v）", code, body)
	}
	if code == http.StatusAccepted {
		if msg, _ := body["msg"].(string); msg == "" {
			t.Error("202 的回包里没有 msg，调用方不知道排了什么")
		}
	}

	// 停掉一个本来就没在跑的服务：Pier 要么说「已排入队列」（撤销），
	// 要么明说不能停。两种都行，但不能是 5xx 或者空回包。
	code, body = call(t, s, "POST", "/api/services/beta/start", "secret")
	if code != http.StatusAccepted {
		t.Fatalf("启动状态码 = %d，想要 202（%v）", code, body)
	}
	// 已经排上队了，立刻再排一次——同一个服务不能同时有两个动作，
	// 这时要么又是 202（排在后面），要么是 409 并说清原因。
	code, body = call(t, s, "POST", "/api/services/beta/start", "secret")
	if code != http.StatusAccepted && code != http.StatusConflict {
		t.Errorf("重复启动状态码 = %d，想要 202 或 409（%v）", code, body)
	}
	if code == http.StatusConflict {
		if msg, _ := body["msg"].(string); msg == "" {
			t.Error("409 的回包里没有 msg，调用方不知道为什么")
		}
	}
}

// TestLogsBadSince 钉着「since 不是数就说清楚」，而不是悄悄当成 0
// 把整份日志又给一遍——那种情况调用方会以为拿到了增量，日志里就会出现重复。
func TestLogsBadSince(t *testing.T) {
	s := newTestServer(t, "secret")
	code, body := call(t, s, "GET", "/api/services/alpha/logs?since=abc", "secret")
	if code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，想要 400（%v）", code, body)
	}
}

// TestStateReportsConfigFailure 钉着「清单坏掉时接口照样在，原因说得出」。
//
// 这一条对应 cmdApi 里那个刻意的选择：清单坏了不退出，把原因交给接口说出来。
// 直接退出的话，从脚本那一头看是「连不上」，和「Pier 没在跑」长得一模一样，
// 而真正的原因只留在启动者眼前的一行 stderr 上。
func TestStateReportsConfigFailure(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	dir := t.TempDir()
	base := filepath.Join(dir, "pier.yaml")
	// 端口写成一个字符串，解析必然失败。
	if err := os.WriteFile(base, []byte("services:\n  - name: a\n    port: 不是数\n"), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := panel.New()
	t.Cleanup(p.Close)
	if err := p.Load(base); err == nil {
		t.Fatal("坏掉的清单居然加载成功了")
	} else {
		p.SetLoadError(err.Error())
	}

	s := New(p, "127.0.0.1:0", "secret")
	code, body := call(t, s, "GET", "/api/services", "secret")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("清单坏掉时 /api/services 状态码 = %d，想要 503（%v）", code, body)
	}
	msg, _ := body["msg"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatal("503 的回包没写清单坏在哪")
	}
	// 说的得是那一行，而不是一句笼统的「加载失败」。
	if !strings.Contains(msg, "line 3") {
		t.Errorf("原因里没指出是哪一行：%q", msg)
	}

	// /api/state 也带着同一句原因，且不是一个空壳。
	code, body = call(t, s, "GET", "/api/state", "secret")
	if code != http.StatusOK {
		t.Fatalf("/api/state 状态码 = %d，想要 200（%v）", code, body)
	}
	st, _ := body["state"].(map[string]any)
	if st == nil {
		t.Fatal("/api/state 的回包里没有 state")
	}
	if st["ok"] != false {
		t.Errorf("state.ok = %v，想要 false", st["ok"])
	}
	if s, _ := st["error"].(string); !strings.Contains(s, "line 3") {
		t.Errorf("state.error = %q，没指出是哪一行", s)
	}

	// 探活照样通：脚本据此把「Pier 在跑但清单坏了」和「Pier 没在跑」分开。
	if code, _ := call(t, s, "GET", "/health", ""); code != http.StatusOK {
		t.Errorf("清单坏掉时 /health 状态码 = %d，想要 200", code)
	}
}

// ── 一次动一批 ─────────────────────────────────────────────────────────────

// newSetServer 起一个带分组与依赖的接口服务，专给这几条批量接口用。
func newSetServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PIER_HOME", t.TempDir())

	dir := t.TempDir()
	base := filepath.Join(dir, "pier.yaml")
	const yaml = `
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
	return New(p, "127.0.0.1:0", "secret")
}

// 空请求体就是「全部」：curl 手搓一次全部启动，不该先拼一个 {} 出来。
func TestStartSetEmptyBodyMeansAll(t *testing.T) {
	s := newSetServer(t)

	code, body := callBody(t, s, "POST", "/api/services/start", "secret", "")
	if code != http.StatusAccepted {
		t.Fatalf("状态码 = %d，想要 202（%v）", code, body)
	}
	if msg, _ := body["msg"].(string); msg == "" {
		t.Error("202 的回包里没有 msg，调用方不知道排了什么")
	}
}

// 分组与点名的选择要走通，且 msg 里说清楚这一批是哪几个。
func TestStartSetByGroup(t *testing.T) {
	s := newSetServer(t)

	code, body := callBody(t, s, "POST", "/api/services/start", "secret", `{"group":"前端"}`)
	if code != http.StatusAccepted {
		t.Fatalf("按分组启动状态码 = %d，想要 202（%v）", code, body)
	}
	msg, _ := body["msg"].(string)
	// web 依赖 api：按分组起 web 时要连带把跨组的 api 一起起，并说明白。
	if !strings.Contains(msg, "前置") || !strings.Contains(msg, "api") {
		t.Errorf("msg = %q，想要说明带上了前置 api", msg)
	}
}

// 三种「选法有问题」要分成三个状态码。
//
// 全揉成一句 409 的话，脚本只能读 msg 里的中文才分得出该改哪一头——
// 是请求写错了（400），还是清单里没这个东西（404）。
func TestSelectionFailuresAreTyped(t *testing.T) {
	s := newSetServer(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		// 键名写错是最危险的一种：当成「没有选择」的话，本来想动一个服务，
		// 结果把全部服务都停了。
		{"键名写错", `{"service":"api"}`, http.StatusBadRequest},
		{"两个字段都给", `{"names":["api"],"group":"前端"}`, http.StatusBadRequest},
		{"请求体不是对象", `["api"]`, http.StatusBadRequest},
		{"名字不存在", `{"names":["nope"]}`, http.StatusNotFound},
		{"分组不存在", `{"group":"前端组"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, path := range []string{"/api/services/start", "/api/services/stop"} {
				code, body := callBody(t, s, "POST", path, "secret", c.body)
				if code != c.want {
					t.Errorf("%s 状态码 = %d，想要 %d（%v）", path, code, c.want, body)
				}
				if msg, _ := body["msg"].(string); strings.TrimSpace(msg) == "" {
					t.Errorf("%s 的回包没写原因：%v", path, body)
				}
			}
		})
	}
}

// 分组名写错时要把现有的分组列出来：拿到回包的人多半正对着一份清单找自己写错
// 在哪儿，而「没有这个分组」四个字不会告诉他该写哪个。
func TestSelectionFailureListsGroups(t *testing.T) {
	s := newSetServer(t)

	_, body := callBody(t, s, "POST", "/api/services/start", "secret", `{"group":"前端组"}`)
	msg, _ := body["msg"].(string)
	for _, want := range []string{"前端组", "前端", "后端"} {
		if !strings.Contains(msg, want) {
			t.Errorf("msg = %q，少了 %q", msg, want)
		}
	}
}

// wait 的 ok 说的是「全都就绪了没有」，不是「请求处理成功没有」。
//
// 与 /api/state 一个规矩：状态码说的是答没答上来，答案本身在 ok 里。
// 全都就绪是 200 + ok=true，有没等到的是 200 + ok=false，外加每条一句为什么。
func TestWaitReportsNotReady(t *testing.T) {
	s := newSetServer(t)

	code, body := callBody(t, s, "POST", "/api/services/wait", "secret", `{"names":["api"]}`)
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（%v）", code, body)
	}
	if body["ok"] != false {
		t.Errorf("没配探针却 ok = %v，想要 false", body["ok"])
	}
	wait, _ := body["wait"].([]any)
	if len(wait) != 1 {
		t.Fatalf("wait = %v，想要一条", body["wait"])
	}
	one, _ := wait[0].(map[string]any)
	if one["name"] != "api" || one["ready"] != false || one["why"] != "no_probe" {
		t.Errorf("结果 = %v，想要 api 没就绪、原因是 no_probe", one)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "api") {
		t.Errorf("msg = %q，要把没等到的是谁写出来", msg)
	}
}

// 全都没配探针时，wait 当场就该答，不该等满窗口——等下去也不会有结果。
func TestWaitAnswersImmediatelyWithoutProbes(t *testing.T) {
	s := newSetServer(t)

	start := time.Now()
	code, body := callBody(t, s, "POST", "/api/services/wait", "secret", "")
	if code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200（%v）", code, body)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("等了 %v 才回来，没配探针的应当当场就答", el)
	}
	wait, _ := body["wait"].([]any)
	if len(wait) != 2 {
		t.Errorf("wait = %v，想要两个服务各一条", body["wait"])
	}
}

// timeout 写错要说清楚，而不是悄悄用默认的三分钟——那会让一次写错的调用
// 变成「等满三分钟然后失败」。
func TestWaitBadTimeout(t *testing.T) {
	s := newSetServer(t)

	code, body := callBody(t, s, "POST", "/api/services/wait?timeout=abc", "secret", "")
	if code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，想要 400（%v）", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "abc") {
		t.Errorf("msg = %q，要把写错的那个值念一遍", msg)
	}
	if code, _ := callBody(t, s, "POST", "/api/services/wait?timeout=30s", "secret", ""); code != http.StatusOK {
		t.Errorf("timeout=30s 状态码 = %d，想要 200", code)
	}
}

// ── 起一个真的监听 ─────────────────────────────────────────────────────────

// TestListenAndServe 走一遍真的 TCP：Handler 直调绕过了监听、地址与关闭，
// 而这几件事恰恰是最容易写错的地方（端口没释放的话下一次启动会报被占）。
func TestListenAndServe(t *testing.T) {
	s := newTestServer(t, "secret")
	if err := s.Listen(); err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	addr := s.Addr()
	if !strings.Contains(addr, ":") {
		t.Fatalf("监听地址 = %q", addr)
	}

	done := make(chan error, 1)
	go func() { done <- s.Serve() }()

	req, err := http.NewRequest("GET", "http://"+addr+"/health", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d，想要 200", resp.StatusCode)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	// 正常收场不算失败：Serve 返回 nil，命令行的退出码才是 0。
	if err := <-done; err != nil {
		t.Errorf("Serve 返回 %v，想要 nil", err)
	}
}

// TestListenConflictSaysWhy 钉着「端口被占时说的是哪个地址」。
// 多半是上一次的 Pier 还在跑，报一句「启动失败」帮不上忙。
func TestListenConflictSaysWhy(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	p := panel.New()
	t.Cleanup(p.Close)

	first := New(p, "127.0.0.1:0", "secret")
	if err := first.Listen(); err != nil {
		t.Fatalf("第一次监听失败：%v", err)
	}
	defer func() { _ = first.Close() }()

	second := New(p, first.Addr(), "secret")
	err := second.Listen()
	if err == nil {
		_ = second.Close()
		t.Fatal("同一个地址监听了两次都成功了")
	}
	if !strings.Contains(err.Error(), first.Addr()) {
		t.Errorf("错误里没有写地址：%v", err)
	}
}

// TestAddrBeforeListen 钉着「没监听时 Addr 给的是配置里那个」，
// 便于调用方在 Listen 之前就能把要打印的地址拼出来。
func TestAddrBeforeListen(t *testing.T) {
	s := New(nil, "", "t")
	want := DefaultAddr + ":" + itoa(DefaultPort)
	if got := s.Addr(); got != want {
		t.Errorf("Addr = %q，想要 %q", got, want)
	}
	// 空地址被补成默认值，而不是绑到所有网卡上。
	if strings.Contains(s.Addr(), "0.0.0.0") {
		t.Errorf("默认地址里出现了 0.0.0.0：%q", s.Addr())
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestConfigResolveIsShared 钉着三个入口（图形界面、命令行面板、本地接口）
// 用的是同一套「这次用哪份清单」的规则。
func TestConfigResolveIsShared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)

	path, src, err := config.Resolve("")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !strings.HasPrefix(path, home) {
		t.Errorf("默认清单 %q 不在 PIER_HOME（%q）之下", path, home)
	}
	if src == "" {
		t.Error("没给出来源说法")
	}

	// 命令行给了 --config 就用它，并且是绝对路径——之后要拿它去开目录。
	yaml := filepath.Join(t.TempDir(), "pier.yaml")
	got, src2, err := config.Resolve(yaml)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got != yaml {
		t.Errorf("指定路径时 Resolve = %q，想要 %q", got, yaml)
	}
	if src2 == src {
		t.Errorf("指定路径与默认数据的来源说法相同：%q", src2)
	}
}
