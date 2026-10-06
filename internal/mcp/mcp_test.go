package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// 这一份测的是协议那一层：怎么认版本、怎么回错、工具自己失败算谁的错。
// 工具做没做成另有一份（tools_test.go），这里用的几个工具都是假的。
//
// 收发走真的管道、真的一行一条消息，不直接调 dispatch：stdout 上只许有 MCP 消息
// 这条规矩（客户端按行解析）是这一段最容易破的一条，绕过它就测不到了。

// ── 假工具 ─────────────────────────────────────────────────────────────────

type echoArgs struct {
	Name string `json:"name"`
}

var errTest = fmt.Errorf("这件事没做成")

// fakeTools 是几个只有测试认得的工具：一条走得通、一条自己失败，
// 给了 started 就再挂一条卡住不走的（等取消）。
func fakeTools(started ...chan struct{}) []Tool {
	tools := []Tool{
		bind("echo", "回声", "把 name 回给你",
			`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`,
			func(_ context.Context, a echoArgs) (string, error) {
				return "你好 " + a.Name, nil
			}),
		bind("boom", "出错", "这一条总是失败",
			`{"type":"object","properties":{},"additionalProperties":false}`,
			func(_ context.Context, _ emptyArgs) (string, error) {
				return "", errTest
			}),
	}
	if len(started) > 0 {
		tools = append(tools, bind("block", "卡住", "一直等到被取消",
			`{"type":"object","properties":{},"additionalProperties":false}`,
			func(ctx context.Context, _ emptyArgs) (string, error) {
				started[0] <- struct{}{}
				<-ctx.Done()
				return "", ctx.Err()
			}))
	}
	return tools
}

type emptyArgs struct{}

// ── 收发 ───────────────────────────────────────────────────────────────────

// session 是一条活着的一问一答通道：stdin 不关，回包按条收。
//
// 必须这样测 tools/call：它在服务端是异步跑的（见 handleCall），而 stdin 一到头
// 就会把还在跑的那几条取消。把整段输入一次喂完再关，等于在每条调用刚发出时
// 就取消它——测不到回包，而那是这台服务端自己的规矩，不是被测代码的问题。
type session struct {
	t    *testing.T
	srv  *Server
	in   *io.PipeWriter
	repl chan map[string]any
	done chan error

	closed bool
}

func newSession(t *testing.T, tools []Tool) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{
		t:    t,
		srv:  New(tools),
		in:   inW,
		repl: make(chan map[string]any, 32),
		done: make(chan error, 1),
	}
	go func() {
		s.done <- s.srv.Serve(context.Background(), inR, outW)
		_ = outW.Close()
	}()
	go func() {
		defer close(s.repl)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				// stdout 上冒出非 MCP 的东西是硬伤：客户端按行解析，
				// 一行错位整条通道就废了。带出去让等着的那一步报出来。
				s.repl <- map[string]any{"__badline": line}
				continue
			}
			s.repl <- m
		}
	}()
	t.Cleanup(s.close)
	return s
}

func (s *session) send(lines ...string) {
	s.t.Helper()
	for _, line := range lines {
		if _, err := io.WriteString(s.in, line+"\n"); err != nil {
			s.t.Fatalf("写 stdin 失败：%v", err)
		}
	}
}

// next 等下一条回包。
func (s *session) next() map[string]any {
	s.t.Helper()
	select {
	case m, ok := <-s.repl:
		if !ok {
			s.t.Fatal("通道关了，回包不会再来了")
		}
		if bad, ok := m["__badline"]; ok {
			s.t.Fatalf("stdout 上有一行不是 JSON：%v", bad)
		}
		if m["jsonrpc"] != "2.0" {
			s.t.Fatalf("stdout 上有一行不是 JSON-RPC 2.0 的消息：%s", dump(s.t, m))
		}
		return m
	case <-time.After(10 * time.Second):
		s.t.Fatal("等了 10 秒没有回包")
		return nil
	}
}

// quiet 断言这一小段时间里没有回包。
func (s *session) quiet(d time.Duration) {
	s.t.Helper()
	select {
	case m, ok := <-s.repl:
		if ok {
			s.t.Fatalf("本不该有回包，却收到了：%s", dump(s.t, m))
		}
	case <-time.After(d):
	}
}

// call 发一条 tools/call，返回 id 对得上的那条回包。
func (s *session) call(id float64, name, args string) map[string]any {
	s.t.Helper()
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args))
	for i := 0; i < 8; i++ {
		m := s.next()
		if m["id"] == id {
			return m
		}
	}
	s.t.Fatalf("没有 id=%v 的回包", id)
	return nil
}

// close 关掉 stdin 并等 Serve 收场。可以重复调用。
func (s *session) close() {
	if s.closed {
		return
	}
	s.closed = true
	_ = s.in.Close()
	select {
	case err := <-s.done:
		// 客户端关掉 stdin 是正常收场（Serve 的注释里写着为什么）。
		if err != nil {
			s.t.Errorf("Serve 返回了 %v，想要 nil", err)
		}
	case <-time.After(10 * time.Second):
		s.t.Error("Serve 没在 10 秒内返回——多半是有一条消息卡住了")
	}
}

// ── 小件 ───────────────────────────────────────────────────────────────────

func dump(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		return "（解不开）"
	}
	return string(b)
}

// errCode 取出回包里的 JSON-RPC 错误码，没有错误时返回 0。
func errCode(m map[string]any) int {
	e, ok := m["error"].(map[string]any)
	if !ok {
		return 0
	}
	n, _ := e["code"].(float64)
	return int(n)
}

// textOf 取出 CallToolResult 里第一段文本。
func textOf(t *testing.T, m map[string]any) string {
	t.Helper()
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("回包里没有 result：%s", dump(t, m))
	}
	blocks, _ := res["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("result 里没有 content：%s", dump(t, m))
	}
	first, _ := blocks[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("第一段不是 text：%s", dump(t, m))
	}
	s, _ := first["text"].(string)
	return s
}

// ── 版本协商 ───────────────────────────────────────────────────────────────

// 客户端报的版本认得就原样回声。
//
// 规范要求的是「支持它就回同一个」：回声成我们自己的最新版本，客户端会以为
// 它那套更旧的规矩不能用了，于是白白降级或者干脆断开。
func TestInitializeEchoesKnownVersion(t *testing.T) {
	for _, v := range supported {
		s := newSession(t, fakeTools())
		s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + v + `"}}`)
		res, _ := s.next()["result"].(map[string]any)
		if got, _ := res["protocolVersion"].(string); got != v {
			t.Errorf("客户端报 %s，回的是 %s", v, got)
		}
		s.close()
	}
}

// 认不得的（太旧的、以及没有握手的那一代）回我们支持的最新那个，而不是报错。
//
// 报错的话客户端只能判「这个服务端坏了」；回一个版本号，它知道的是
// 「这个服务端只会这一套」，认不认由它自己决定。
func TestInitializeFallsBackToLatestKnown(t *testing.T) {
	for _, v := range []string{"2020-01-01", "2026-07-28", "随便"} {
		s := newSession(t, fakeTools())
		s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + v + `"}}`)
		res, _ := s.next()["result"].(map[string]any)
		if got, _ := res["protocolVersion"].(string); got != ProtocolVersion {
			t.Errorf("客户端报 %q，回的是 %s，想要 %s", v, got, ProtocolVersion)
		}
		s.close()
	}
}

// 连 params 都不给也要能握手：报头那一步没有参数可言。
func TestInitializeWithoutParams(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	res, ok := s.next()["result"].(map[string]any)
	if !ok {
		t.Fatal("initialize 没有 result")
	}
	if v, _ := res["protocolVersion"].(string); v != ProtocolVersion {
		t.Errorf("protocolVersion = %q，想要 %s", v, ProtocolVersion)
	}
	s.close()
}

// 握手的回包里三样都得有：capabilities、protocolVersion、serverInfo。
func TestInitializeResultShape(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	res, _ := s.next()["result"].(map[string]any)

	if v, _ := res["protocolVersion"].(string); v == "" {
		t.Error("没有 protocolVersion")
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("没有 capabilities：%s", dump(t, res))
	}
	// 只声明工具这一项。声明了别的就得实现，而客户端会照着声明去调。
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities 里没有 tools：%s", dump(t, caps))
	}
	if len(caps) != 1 {
		t.Errorf("capabilities 里多了没声明过的东西：%s", dump(t, caps))
	}
	info, ok := res["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("没有 serverInfo：%s", dump(t, res))
	}
	if info["name"] != "pier" {
		t.Errorf("serverInfo.name = %v，想要 pier", info["name"])
	}
	if v, _ := info["version"].(string); v == "" {
		t.Error("serverInfo 里没有 version")
	}
	s.close()
}

// ── tools/list ─────────────────────────────────────────────────────────────

// 工具的每一项都要有名字、说明和一个最外层是 object 的 schema。
//
// 最外层那个 "object" 是规范写死的常量：客户端照着它决定怎么渲染参数，
// 给一个别的类型（或者干脆不给）会被当场拒掉。
func TestToolsListShape(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res, _ := s.next()["result"].(map[string]any)
	list, ok := res["tools"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("tools/list 里没有 tools：%s", dump(t, res))
	}
	for _, item := range list {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" {
			t.Errorf("有一个工具没有名字：%s", dump(t, tool))
		}
		if d, _ := tool["description"].(string); d == "" {
			t.Errorf("%s 没有说明——模型靠这一句决定用不用它", name)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("%s 没有 inputSchema：%s", name, dump(t, tool))
		}
		if schema["type"] != "object" {
			t.Errorf("%s 的 inputSchema.type = %v，规范要求是 object", name, schema["type"])
		}
	}
	s.close()
}

// schema 里写的属性名必须与入参结构体的 json 标签一一对应。
//
// 两边走散的代价是一句谁也看不懂的错：schema 里写着 group、结构体上却是 groups，
// 模型照着 schema 写，服务端回一句「unknown field」。这条与 internal/api 那边
// 「文档里的字段与结构体对得上」是同一件事。
func TestSchemasMatchParams(t *testing.T) {
	for _, tool := range fakeTools(make(chan struct{}, 1)) {
		checkSchema(t, tool)
	}
}

// checkSchema 比一个工具的 schema 与它的入参结构体。
func checkSchema(t *testing.T, tool Tool) {
	t.Helper()
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Errorf("%s 的 schema 不是合法 JSON：%v", tool.Name, err)
		return
	}
	if schema.Type != "object" {
		t.Errorf("%s 的 schema 最外层不是 object", tool.Name)
	}

	in := map[string]bool{}
	rt := reflect.TypeOf(tool.Params()).Elem()
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Errorf("%s 的入参结构体里 %s 没有 json 标签", tool.Name, rt.Field(i).Name)
			continue
		}
		in[name] = true
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("%s 的 schema 里少了 %s", tool.Name, name)
		}
	}
	var extra []string
	for name := range schema.Properties {
		if !in[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("%s 的 schema 里多写了结构体上没有的属性：%v", tool.Name, extra)
	}
	for _, name := range schema.Required {
		if !in[name] {
			t.Errorf("%s 的 schema 把不存在的属性 %s 写成了必填", tool.Name, name)
		}
	}
}

// 每个工具都关掉 additionalProperties：多写的字段会被当场拒掉，这件事得在
// schema 里写着，模型才知道不能随手加一个。
func TestSchemasRejectUnknownProperties(t *testing.T) {
	for _, tool := range fakeTools() {
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatalf("%s 的 schema 解不开：%v", tool.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s 的 schema 没有关掉 additionalProperties", tool.Name)
		}
	}
}

// ── 错误 ───────────────────────────────────────────────────────────────────

// 看不懂的一行回 -32700，且 id 是 null（那时没有 id 可回）。
func TestParseError(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`这不是 JSON`)
	m := s.next()
	if code := errCode(m); code != codeParse {
		t.Errorf("错误码 = %d，想要 %d", code, codeParse)
	}
	if m["id"] != nil {
		t.Errorf("解析失败时 id = %v，想要 null", m["id"])
	}
	s.close()
}

// 不认识的方法回 -32601。
//
// 这一条同时是给新一代客户端看的：它们会先用 server/discover 探一下这个服务端
// 是哪一代，收到一个它们不认得的错误就退回 initialize 握手（见 dispatch 里那段
// 注释）。**这是规范写明的降级路径**，给 server/discover 编一个像样的回答
// 反而会让它们以为这边是新的一代，随后发的每条请求都答不上。
func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","id":3,"method":"server/discover"}`)
	if code := errCode(s.next()); code != codeMethodNotFound {
		t.Errorf("server/discover 的错误码 = %d，想要 %d", code, codeMethodNotFound)
	}
	s.close()
}

// 数据形状不对（不是 2.0、没有 method）回 -32600。
func TestInvalidRequest(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"id":4,"method":"ping"}`)
	if code := errCode(s.next()); code != codeInvalidRequest {
		t.Errorf("错误码 = %d，想要 %d", code, codeInvalidRequest)
	}
	s.close()
}

// 工具名不认识回 -32602：方法（tools/call）是认识的，只是这个工具没有。
func TestUnknownToolIsInvalidParams(t *testing.T) {
	s := newSession(t, fakeTools())
	m := s.call(5, "没有这个", `{}`)
	if code := errCode(m); code != codeInvalidParams {
		t.Errorf("错误码 = %d，想要 %d", code, codeInvalidParams)
	}
	if msg := errMsg(m); !strings.Contains(msg, "没有这个") {
		t.Errorf("错误里没说清是哪个工具：%q", msg)
	}
	s.close()
}

// 参数里多写一个字段当场拒掉，不当成「没给这个参数」。
//
// 代价差得远：把 group 写成 groups 而被当成「两个都没给」，动的就是整份清单。
func TestUnknownArgumentIsRejected(t *testing.T) {
	s := newSession(t, fakeTools())
	m := s.call(6, "echo", `{"名字":"a"}`)
	if code := errCode(m); code != codeInvalidParams {
		t.Errorf("错误码 = %d，想要 %d", code, codeInvalidParams)
	}
	s.close()
}

// 工具自己没做成写在 result 里带 isError，不是协议层的 error。
//
// 走协议层的话，客户端多半只把「工具调用失败」摆给用户，具体那句话被吞掉了——
// 而那句话才是模型能自己改的依据。
func TestToolFailureIsIsError(t *testing.T) {
	s := newSession(t, fakeTools())
	m := s.call(7, "boom", `{}`)
	if code := errCode(m); code != 0 {
		t.Fatalf("工具失败回了协议错误 %d，应该写在 result 里", code)
	}
	res, _ := m["result"].(map[string]any)
	if res["isError"] != true {
		t.Errorf("result.isError = %v，想要 true", res["isError"])
	}
	if got := textOf(t, m); got != errTest.Error() {
		t.Errorf("回执正文 = %q，想要 %q", got, errTest.Error())
	}
	s.close()
}

// arguments 为 null 与不给参数是一回事。
func TestNullArgumentsMeansEmpty(t *testing.T) {
	s := newSession(t, fakeTools())
	m := s.call(8, "boom", `null`)
	if code := errCode(m); code != 0 {
		t.Errorf("arguments 为 null 时回的是错误 %d", code)
	}
	s.close()
}

// ── 通知与 ping ────────────────────────────────────────────────────────────

// 通知没有回包，ping 立刻回一个空对象。
func TestNotificationsAreSilentAndPingAnswers(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.send(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"name":"a"}}}`)
	s.send(`{"jsonrpc":"2.0","id":9,"method":"ping"}`)

	m := s.next()
	if m["id"] != float64(9) {
		t.Fatalf("第一条回包是 %s，想要 ping——上面那两条通知都不该有回包", dump(t, m))
	}
	if res, ok := m["result"].(map[string]any); !ok || len(res) != 0 {
		t.Errorf("ping 的结果 = %v，想要一个空对象", m["result"])
	}
	s.quiet(200 * time.Millisecond)
	s.close()
}

// 空行是消息之间的分隔符，不是一条坏消息。
func TestBlankLinesAreIgnored(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send("", `{"jsonrpc":"2.0","id":10,"method":"ping"}`, "")
	if m := s.next(); m["id"] != float64(10) {
		t.Errorf("收到了 %s，想要 id=10 的 ping", dump(t, m))
	}
	s.quiet(200 * time.Millisecond)
	s.close()
}

// stdin 一到头，正在跑的那些立刻撒手：一次 wait_ready 可以等三分钟，
// 客户端走了还留着它，进程就一直不退。
func TestEOFStopsInflight(t *testing.T) {
	started := make(chan struct{}, 1)
	s := newSession(t, fakeTools(started))
	s.send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("那条卡住的工具没有跑起来")
	}
	// close 里那句断言管着这件事：关掉 stdin 之后 Serve 要在 10 秒内返回。
	s.close()
}

// ── 取消 ───────────────────────────────────────────────────────────────────

// 被取消的那一条不回包——这是规范明说的，不是可以自己发挥的地方。
func TestCancelledRequestGetsNoReply(t *testing.T) {
	started := make(chan struct{}, 1)
	s := newSession(t, fakeTools(started))

	// id 的写法故意与取消通知里的不一样（1 对 1.0）：JSON-RPC 的数字是同一个值，
	// 两处写法不同不该让取消落空。
	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("那条卡住的工具没有跑起来")
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1.0,"reason":"用户按了 Esc"}}`)

	// 取消之后紧跟一条 ping：它回得来，说明这次取消没有把通道带走，
	// 也没有在取消的那一条上回一句话。
	s.send(`{"jsonrpc":"2.0","id":12,"method":"ping"}`)
	if m := s.next(); m["id"] != float64(12) {
		t.Fatalf("取消之后先收到了 %s，想要 id=12 的 ping——被取消的那一条不该有回包", dump(t, m))
	}
	s.close()
}

// 取消一条已经跑完的请求什么都不该发生（通知在路上追上了回包）。
func TestCancellingUnknownRequestIsHarmless(t *testing.T) {
	s := newSession(t, fakeTools())
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":999}}`)
	s.send(`{"jsonrpc":"2.0","id":13,"method":"ping"}`)
	if m := s.next(); m["id"] != float64(13) {
		t.Errorf("收到了 %s，想要 id=13 的 ping", dump(t, m))
	}
	s.close()
}

// errMsg 取出错误里那句话。
func errMsg(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	if e == nil {
		return ""
	}
	s, _ := e["message"].(string)
	return s
}
