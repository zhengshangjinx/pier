// Package mcp 把 Pier 的面板接到 MCP 上，走 stdio。
//
// 为什么自己写而不是引一个 SDK：这一层要做的事很少——三条方法、七个工具，
// 全在一份 JSON-RPC 的收发上。而它手里握着的东西很重：启停本机进程。一份看得完、
// 测得透、不必跟着别人升级的两百行，比一份依赖更让人放心。
//
// 三个地方照规范来，不自己发明（其余地方可以简单）：
//
//   - 版本协商：客户端报的版本认得就原样回声，认不得就回我们支持的最新那个。
//     规范要求的是「回一个自己支持的」而不是报错，两者对客户端的意义不同。
//   - tools/list 的参数 schema：JSON Schema 2020-12，最外层必须是 object。
//   - 错误的写法：协议层的问题走 JSON-RPC 的 error（-32700…-32603），
//     工具自己没做成写在 result 里带 isError。后者模型看得见，才能自己改。
//
// 只走 stdio，不开端口、不发令牌：用它的进程就在本机，多开一个端口就多一份
// 要保护的东西。这与 internal/api 那条路正好相反——那边是给外部的脚本用的，
// 所以那边两样都要。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/zhengshangjinx/pier/internal/version"
)

// ProtocolVersion 是默认用的协议版本，也是协商不上时回过去的那个。
//
// 取「带 initialize 握手的那一代里最新的一个」：MCP 从 2026-07-28 起改了做法
// （版本、身份、能力随每条请求走 _meta，握手整个没有了），这边做的是握手那一代。
// 报一个做法不同的新版本号不算升级，客户端会按新规矩发请求，而这边的回答
// 它一条都认不出来。
const ProtocolVersion = "2025-11-25"

// supported 是认的协议版本，新的在前。
//
// 只声明 tools 这一项能力，而工具的形状（tools/list 的字段、call 的回包、
// 错误的写法）在这几个版本里没动过，所以客户端报哪一个都能原样回声。
var supported = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 的错误码。只说协议层的事：这件事工具做没做成，不走这里。
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Tool 是一个能做的事。
type Tool struct {
	Name        string
	Title       string
	Description string
	// Schema 是入参的 JSON Schema（2020-12），最外层是 object。摆在这里而不是
	// 从结构体生成：给模型读的那几句说明（哪个必填、空着是什么意思）本来就要
	// 一句句写，生成器省下的那点字远不抵它自己带来的规矩。
	Schema json.RawMessage
	// Params 返回入参结构体的零值，请求里的 arguments 解进它。
	Params func() any
	// Call 干这件事，返回值是给模型看的文本。返回 error 表示这件事没做成，
	// 会被翻成 isError——协议本身没错。
	Call func(context.Context, any) (string, error)
}

// bind 把「一个入参结构体 + 一段逻辑」包成一个工具。
//
// 省掉每个工具一次类型断言，也让 schema 里写的属性名与结构体的 json 标签
// 有个能对得上的地方（测试拿 Params 回来逐个核，见 TestToolSchemasMatchParams）。
func bind[T any](name, title, desc, schema string, call func(context.Context, T) (string, error)) Tool {
	return Tool{
		Name:        name,
		Title:       title,
		Description: desc,
		Schema:      json.RawMessage(schema),
		Params:      func() any { return new(T) },
		Call: func(ctx context.Context, in any) (string, error) {
			a, ok := in.(*T)
			if !ok {
				return "", fmt.Errorf("内部错误：%s 的入参类型对不上", name)
			}
			return call(ctx, *a)
		},
	}
}

// Server 是一个跑在 stdio 上的服务端。
//
// 请求是并着跑的：一次 wait_ready 可以等上三分钟，而这三分钟里客户端还可能
// 发取消通知（用户按了 Esc）或者 ping。串行处理的话，那些消息要排在它后面，
// 取消也就永远送不到——界面上的表现是「点了取消没反应」。
type Server struct {
	tools []Tool

	out io.Writer
	wmu sync.Mutex // 护住 out：几条请求并着跑，各自写各自的回包

	mu       sync.Mutex
	inflight map[string]context.CancelFunc
	wg       sync.WaitGroup
}

// New 建一个服务端。
func New(tools []Tool) *Server {
	return &Server{tools: tools, inflight: map[string]context.CancelFunc{}}
}

// Serve 从 r 读消息、往 w 写回包，直到 r 到头（客户端关了 stdin）。
//
// 到头的处理是「先取消、再等一等」：还在跑的那几条要立刻撒手（否则一次
// wait_ready 会把进程多留三分钟），等它们把回包写完，是为了别把一条已经算出来的
// 结果丢在半截——规范的措辞是「SHOULD exit promptly」，这个 promptly 是对
// 客户端而言的，不是「立刻拔掉自己的线」。
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.out = w
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		// 空行不是消息（消息之间的分隔符而已），不为此回一条解析错误。
		if len(bytes.TrimSpace(line)) > 0 {
			s.dispatch(ctx, line)
		}
		if err != nil {
			s.stopAll()
			s.wg.Wait()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (s *Server) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.inflight {
		cancel()
	}
}

// ── 收 ─────────────────────────────────────────────────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// dispatch 认一条消息，该回的回、该跑的跑。
func (s *Server) dispatch(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		// 解析不了就没有 id 可言，按规范回一个 null id。
		s.reply(nil, nil, &rpcError{Code: codeParse, Message: "看不懂这一行：" + err.Error()})
		return
	}
	// id 缺省即通知。通知一律没有回包——除了「方法不认识」这一条，那时
	// 多半是客户端写错了，说一句比装作没看见强。
	note := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if note {
			return
		}
		s.reply(req.ID, nil, &rpcError{Code: codeInvalidRequest, Message: `要一个 JSON-RPC 2.0 的请求：{"jsonrpc":"2.0","id":1,"method":"…"}`})
		return
	}

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized":
		// 握手收尾，没有回包。这里什么都不必做：我们不在收到它之前往外发请求。
	case "ping":
		// 规范要的是「立刻回一个空对象」。
		s.reply(req.ID, struct{}{}, nil)
	case "tools/list":
		s.handleList(req)
	case "tools/call":
		s.handleCall(ctx, req)
	case "notifications/cancelled":
		s.handleCancelled(req)
	default:
		// 认不出来的方法按 -32601 回。
		//
		// 这一条同时是给新一代客户端看的：它们会先用 server/discover 探一下
		// 这个服务端是哪一代，收到一个「不是它认得的那几个现代错误」就退回
		// initialize 握手（这一步是规范写明的，不是我们猜的）。所以这里
		// 千万不能为了「支持现代协议」给 server/discover 编一个像样的回答，
		// 那会让客户端以为这边是新的一代，随后发的每条请求都答不上。
		if note {
			return
		}
		s.reply(req.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "不认识的方法：" + req.Method})
	}
}

func (s *Server) handleInitialize(req request) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	s.reply(req.ID, initializeResult{
		ProtocolVersion: negotiate(p.ProtocolVersion),
		Capabilities:    serverCapabilities{},
		ServerInfo:      implementation{Name: "pier", Title: "Pier", Version: version.Current()},
		Instructions:    instructions,
	}, nil)
}

// negotiate 挑一个两边都认的协议版本。
//
// 客户端报的认不出来时回我们最新的那个（而不是报错）：规范写的是「回一个自己
// 支持的版本」，由客户端决定认不认——它不认就会断开，那时它知道的是
// 「这个服务端太旧」，而不是「这个服务端坏了」。
func negotiate(got string) string {
	if slices.Contains(supported, got) {
		return got
	}
	return supported[0]
}

func (s *Server) handleList(req request) {
	tools := make([]toolInfo, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, toolInfo{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	// 不分页：七个工具一次给完，nextCursor 不出现就是「没有了」。
	s.reply(req.ID, map[string]any{"tools": tools}, nil)
}

func (s *Server) handleCall(ctx context.Context, req request) {
	if len(req.ID) == 0 {
		// tools/call 是个请求，不是通知。没有 id 就没有可回的地方，
		// 照旧把这件事做掉——真跑起来反而更坏：一个没有回执的动作。
		return
	}
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.reply(req.ID, nil, &rpcError{Code: codeInvalidParams, Message: "tools/call 要 {name, arguments}：" + err.Error()})
		return
	}
	t, ok := s.tool(p.Name)
	if !ok {
		// 工具名不认识是「参数不对」，不是「方法不存在」：方法（tools/call）
		// 是认识的，只是这个工具没有。规范里给的例子就是这个码。
		s.reply(req.ID, nil, &rpcError{Code: codeInvalidParams, Message: "没有这个工具：" + p.Name})
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	key := idKey(req.ID)
	s.mu.Lock()
	s.inflight[key] = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			cancel()
		}()
		res, e := call(ctx, t, p.Arguments)
		// 被取消的那一条不回包：规范明说「不要为被取消的请求发回包」，
		// 而且客户端那边已经把这次调用忘掉了。
		if ctx.Err() != nil {
			return
		}
		s.reply(req.ID, res, e)
	}()
}

// call 解参数、跑工具、把结果摆成 CallToolResult。
func call(ctx context.Context, t Tool, args json.RawMessage) (callResult, *rpcError) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	in := t.Params()
	dec := json.NewDecoder(bytes.NewReader(args))
	// 只认 schema 里写过的那些键：模型把 group 写成 groups 是常事，
	// 当成「这次没指定」于是动了全部服务，代价比当场报错大得多。
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		return callResult{}, &rpcError{
			Code:    codeInvalidParams,
			Message: fmt.Sprintf("%s 的参数不对：%v", t.Name, err),
		}
	}
	text, err := t.Call(ctx, in)
	if err != nil {
		// 工具没做成：写在 result 里带 isError，让模型自己看见、自己改。
		// 走协议层的 error 的话，客户端多半只把「工具调用失败」摆给用户，
		// 具体那句话反倒被吞掉了。
		return callResult{Content: []contentBlock{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return callResult{Content: []contentBlock{{Type: "text", Text: text}}}, nil
}

func (s *Server) tool(name string) (Tool, bool) {
	for _, t := range s.tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

func (s *Server) handleCancelled(req request) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return
	}
	s.mu.Lock()
	cancel := s.inflight[idKey(p.RequestID)]
	s.mu.Unlock()
	// 找不到就是已经跑完了（通知在路上追上了回包），什么都不必做。
	if cancel != nil {
		cancel()
	}
}

// idKey 把 JSON-RPC 的 id 归一成一个字符串键。
//
// 不能直接拿原文当键：同一个 id 在客户端那边可能是 1，也可能是 1.0；
// 而取消通知里的 requestId 是另一条消息里写出来的，两处的写法没有保证。
// 先解成 JSON 的值再格式化，1 与 1.0 才会落到同一个键上；类型也带上，
// 字符串 "1" 与数字 1 按规范本来就是两个不同的 id。
func idKey(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return fmt.Sprintf("%T:%v", v, v)
}

// ── 发 ─────────────────────────────────────────────────────────────────────

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// reply 写一条回包。这是往 stdout 写的唯一一处——那条规矩（stdout 上只许有
// 合法的 MCP 消息）就靠这一处守得住，人看的字一律走 stderr。
func (s *Server) reply(id json.RawMessage, result any, e *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.out == nil {
		return
	}
	enc := json.NewEncoder(s.out)
	// 日志正文里少不了 < > &，转义成 < 是合法 JSON，但模型读的时候
	// 是一堆码点。回给模型的字原样给。
	enc.SetEscapeHTML(false)
	_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: e})
}

// ── 形状 ───────────────────────────────────────────────────────────────────

type implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// serverCapabilities 只声明工具这一项。
//
// 资源、提示、日志这些一条都不声明：声明了就得实现，而客户端会照着声明去调。
type serverCapabilities struct {
	Tools struct{} `json:"tools"`
}

type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type toolInfo struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// instructions 是握手时一起交给客户端的一段话，客户端多半会放进系统提示里。
//
// 只说「这个服务端管的是什么、拿它做的三件事的先后」——工具自己的说明在
// 各自的 description 里，这一段重复一遍只会占地方。
const instructions = "Pier 管着本机这几个项目的启停：服务是独立进程，起起来之后" +
	"就一直跑着，这个会话结束也不会带走它们。改了代码要生效，用 restart_service。" +
	"起完一个服务不确定它好了没有，用 wait_ready 等它通过健康探针——start_service " +
	"只是排进队列，它回来时服务多半还没起来。起不来的时候，read_logs 里有原因，" +
	"service_status 里的诊断那句会给一个下一步。"
