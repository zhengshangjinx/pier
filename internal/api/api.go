// Package api 把面板接到本地 HTTP 上。
//
// 两条硬约束，都不是可选项：
//
//   - 只监听回环地址。这个接口能启停进程，绑到 0.0.0.0 等于把「在这台机器上
//     跑什么」交给整个局域网。
//   - 每个 /api 接口都要令牌。令牌挡的不是能读你数据目录的本地用户——那种人
//     直接敲一句 `pier down` 就行，拦住他没有意义；它挡的是浏览器里任意一个
//     页面：页面里的 JS 可以往 127.0.0.1 发请求，但带自定义头的跨源请求必须先
//     过一次预检，而这里从不答 CORS，预检过不去，请求就发不出来。
//     没有这一条，你随手打开的任何一个网页都能把你正在跑的服务全停掉。
//
// 这一层只做「HTTP 怎么摆」，判断全在 internal/panel：同一份 State、同一套
// 启停入口，命令行、图形界面和这里走的是同一条路。
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/panel"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// DefaultPort 是接口默认监听的端口。
//
// 挑一个不常见的号：这里占的是开发机上最容易撞车的那一段（3000、5173、8080），
// 而 Pier 服务的正是这些端口上的东西，撞上了反而要先解释谁占着谁。
const DefaultPort = 7717

// DefaultAddr 是接口默认的监听地址。只给回环，理由见包注释。
const DefaultAddr = "127.0.0.1"

// TokenLen 是令牌的字节数，十六进制写出来是 64 个字符。
const TokenLen = 32

// Server 是一个跑起来（或准备跑）的接口服务。
type Server struct {
	panel *panel.Panel
	token string
	addr  string

	ln   net.Listener
	http *http.Server
}

// New 建一个接口服务。token 为空时所有 /api 请求都会被拒，这在测试里有用。
func New(p *panel.Panel, addr, token string) *Server {
	if strings.TrimSpace(addr) == "" {
		addr = fmt.Sprintf("%s:%d", DefaultAddr, DefaultPort)
	}
	s := &Server{panel: p, token: token, addr: addr}
	s.http = &http.Server{
		Handler: s.Handler(),
		// 这个服务只服务本机上的脚本，不需要为慢客户端留长超时；
		// 给一个上限是为了让一个卡住的连接不至于一直占着。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// Addr 返回实际监听的地址。Listen 之前是配置里那个，之后是内核分配的那个
// （端口写 0 时两者不一样，调用方要报的是后者）。
func (s *Server) Addr() string {
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

// Listen 开始监听。端口被占这类问题的原因要能原样给用户看到：
// 多半是上一次的 Pier 还在跑，而不是「启动失败」四个字。
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败：%w", s.addr, err)
	}
	s.ln = ln
	return nil
}

// Serve 在已经监听的地址上开始服务，阻塞到 Close 被调用为止。
func (s *Server) Serve() error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close 停掉服务并释放端口。
func (s *Server) Close() error {
	if s.http == nil {
		return nil
	}
	return s.http.Close()
}

// NewToken 生成一个访问令牌。
func NewToken() (string, error) {
	var b [TokenLen]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成访问令牌失败：%w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Token 读出偏好里存的令牌，没有就生成一个存回去。
//
// 存在数据目录而不是每次启动现生成：脚本是从别处读这个令牌的，
// 每次重启就换一个的话，写好的脚本隔天就跑不通了。
func Token(settingsPath string) (string, error) {
	var out string
	err := config.UpdateSettings(settingsPath, func(s *config.Settings) {
		// 回调没有返回错误值的余地，生成失败就让它空着，
		// 由下面那句校验报出来，不会静默放行一个空令牌。
		if strings.TrimSpace(s.APIToken) == "" {
			s.APIToken, _ = NewToken()
		}
		out = s.APIToken
	})
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", errors.New("生成访问令牌失败")
	}
	return out, nil
}

// RotateToken 换一个令牌，返回新的那个。
func RotateToken(settingsPath string) (string, error) {
	var out string
	err := config.UpdateSettings(settingsPath, func(s *config.Settings) {
		s.APIToken, _ = NewToken()
		out = s.APIToken
	})
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", errors.New("生成访问令牌失败")
	}
	return out, nil
}

// Port 从偏好里读出接口端口，没设过就用默认值。
func Port(settingsPath string) int {
	s, err := config.LoadSettings(settingsPath)
	if err != nil || s.APIPort <= 0 || s.APIPort > 65535 {
		return DefaultPort
	}
	return s.APIPort
}

// SetPort 把接口端口写进偏好。
func SetPort(settingsPath string, port int) error {
	return config.UpdateSettings(settingsPath, func(s *config.Settings) { s.APIPort = port })
}

// ── 路由 ───────────────────────────────────────────────────────────────────

// route 是一条接口。方法写在模式里（Go 1.22 起的 ServeMux 语法），
// 路径参数用 {名字} 取。
type route struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// routes 是这张接口的全部。OpenAPI 文档由它生成，不另写一份：
// 两处各写一遍的话，加了接口忘了改文档，用户照着文档调就会调到一个不存在的路径上。
func (s *Server) routes() []route {
	return []route{
		{"GET", "/health", s.handleHealth},
		{"GET", "/openapi.json", s.handleOpenAPI},
		{"GET", "/api/state", s.handleState},
		{"GET", "/api/services", s.handleServices},
		{"POST", "/api/services/start", s.actSet("start")},
		{"POST", "/api/services/stop", s.actSet("stop")},
		{"POST", "/api/services/wait", s.handleWaitSet},
		{"GET", "/api/services/{name}", s.handleService},
		{"POST", "/api/services/{name}/start", s.action("start")},
		{"POST", "/api/services/{name}/stop", s.action("stop")},
		{"POST", "/api/services/{name}/restart", s.action("restart")},
		{"GET", "/api/services/{name}/logs", s.handleLogs},
	}
}

// Handler 组装出对外的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.HandleFunc(r.method+" "+r.pattern, s.guard(r))
	}
	return mux
}

// guard 给一条接口套上令牌校验。
//
// /health 与 /openapi.json 不查：前者是「你在不在」的探活，脚本在拿到令牌之前
// 就要能问；后者是一份静态的接口说明，里面没有任何本机信息。
func (s *Server) guard(r route) http.HandlerFunc {
	if r.pattern == "/health" || r.pattern == "/openapi.json" {
		return r.handler
	}
	return func(w http.ResponseWriter, req *http.Request) {
		if !s.authorized(req) {
			// 不说令牌错在哪：对的就是对的，错的没必要告诉对方差在哪一位。
			writeJSON(w, http.StatusUnauthorized, response{Msg: "缺少或错误的访问令牌（Authorization: Bearer <令牌>）"})
			return
		}
		r.handler(w, req)
	}
}

// authorized 核对 Authorization 头。
//
// 比较用固定时间的实现：逐字节短路比对的耗时随相同前缀的长度变化，
// 理论上能一位一位把令牌试出来。这里的威胁模型不至于这么讲究，
// 但换一个函数就够了，没有理由不换。
func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	got := bearer(r.Header.Get("Authorization"))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// bearer 从 Authorization 头里取出令牌。方案名大小写不敏感，
// 少了它也行——curl 手写这个头的人十有八九会漏掉。
func bearer(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, ' '); i > 0 && strings.EqualFold(h[:i], "bearer") {
		h = h[i+1:]
	}
	return strings.TrimSpace(h)
}

// ── 回包 ───────────────────────────────────────────────────────────────────

// response 是所有接口共用的外壳，与图形界面收到的形状一致
// （那边是 ok / msg，见 gui 的 msgOut）：同一件事在两个入口说法不同，
// 写脚本的人得为每个入口记一套。
//
// 载荷各用各的字段名，不叫笼统的 data：日志是 text，服务列表是 services，
// 状态是 state。名字本身就是文档。
type response struct {
	OK  bool   `json:"ok"`
	Msg string `json:"msg,omitempty"`

	App      string             `json:"app,omitempty"`
	State    *panel.StateOut    `json:"state,omitempty"`
	Services []panel.ServiceOut `json:"services,omitempty"`
	Service  *panel.ServiceOut  `json:"service,omitempty"`
	Log      *panel.LogOut      `json:"log,omitempty"`
	Wait     []proc.WaitResult  `json:"wait,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	// 日志正文里难免有 < > &，转成 < 这类转义是合法的 JSON，
	// 但用 curl 看着像乱码，而这一层本来就是给人看着调的。
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func ok(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func fail(w http.ResponseWriter, status int, format string, a ...any) {
	writeJSON(w, status, response{Msg: fmt.Sprintf(format, a...)})
}

// ── 处理函数 ───────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ok(w, response{OK: true, App: "pier", Msg: s.panel.Source()})
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	st := s.panel.State()
	ok(w, response{OK: st.OK, Msg: st.Error, State: &st})
}

func (s *Server) handleServices(w http.ResponseWriter, _ *http.Request) {
	st := s.panel.State()
	if !st.OK {
		writeJSON(w, http.StatusServiceUnavailable, response{Msg: st.Error})
		return
	}
	ok(w, response{OK: true, Services: st.Services})
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	svc, _, err := s.panel.Lookup(r.PathValue("name"))
	if err != nil {
		fail(w, http.StatusNotFound, "%v", err)
		return
	}
	st := s.panel.State()
	for i := range st.Services {
		if st.Services[i].Name == svc.Name {
			ok(w, response{OK: true, Service: &st.Services[i]})
			return
		}
	}
	fail(w, http.StatusNotFound, "清单里没有 %s", svc.Name)
}

// action 把一个启停动作包成处理函数。
//
// 返回 202 而不是 200：排队成功不等于已经跑起来了。Maven 编译要几分钟，
// 这个请求一秒不到就回来了，回 200 会让人以为服务已经可用。
//
// 前置服务会一并排进队列（见 panel.Start），所以回的是队列里那句话，
// 少了哪一步在 msg 里能看出来。
func (s *Server) action(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, _, err := s.panel.Lookup(name); err != nil {
			fail(w, http.StatusNotFound, "%v", err)
			return
		}
		var msg string
		var err error
		switch kind {
		case "start":
			msg, err = s.panel.Start(name)
		case "stop":
			msg, err = s.panel.Stop(name)
		case "restart":
			msg, err = s.panel.Restart(name)
		}
		if err != nil {
			// 409：服务在清单里，只是此刻不能动它（已经在跑、正在编译）。
			fail(w, http.StatusConflict, "%v", err)
			return
		}
		if msg == "" {
			msg = fmt.Sprintf("已把 %s 排入队列", name)
		}
		writeJSON(w, http.StatusAccepted, response{OK: true, Msg: msg})
	}
}

// actSet 是「一次动一批」的那两条：POST /api/services/start 与 …/stop。
//
// 请求体是一份选择（见 panel.Selection）：{"names":["api"]}、{"group":"前端"}，
// 或者空着——空请求体就是「全部」，与界面上那颗「全部启动」、命令行不带名字的
// pier up / down 是同一份名单。curl 手搓一次全部启动，不必先拼一个 {} 出来。
func (s *Server) actSet(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sel, read := readSelection(w, r)
		if !read {
			return
		}
		var msg string
		var err error
		switch kind {
		case "start":
			msg, err = s.panel.StartSet(sel)
		case "stop":
			msg, err = s.panel.StopSet(sel)
		}
		if err != nil {
			failSelection(w, err)
			return
		}
		if msg == "" {
			msg = "已排入队列"
		}
		// 与单个服务那三条一样回 202：排进队列不等于已经起来。
		writeJSON(w, http.StatusAccepted, response{OK: true, Msg: msg})
	}
}

// handleWaitSet 等一批服务通过健康探针：POST /api/services/wait。
//
// 这是「起完再等就绪」那一步的答案。POST …/start 立刻回 202，此后要自己轮询
// /api/state——而轮询里能看到的只有「在不在跑」，看不出探针通没通，于是每个
// 调用方都得自己写一段「每 500 毫秒看一次，最多看三分钟」。
func (s *Server) handleWaitSet(w http.ResponseWriter, r *http.Request) {
	sel, read := readSelection(w, r)
	if !read {
		return
	}
	var timeout time.Duration
	if v := strings.TrimSpace(r.URL.Query().Get("timeout")); v != "" {
		d, err := proc.ParseWaitTimeout(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "timeout %v", err)
			return
		}
		timeout = d
	}
	out, err := s.panel.WaitSetCtx(r.Context(), sel, timeout)
	if err != nil {
		failSelection(w, err)
		return
	}
	ready := 0
	for _, r := range out {
		if r.Ready {
			ready++
		}
	}
	// 回 200 而不是 4xx 就算问到了：与 /api/state 一个规矩——状态码说的是
	// 「这个请求答没答上来」，答案本身（全就绪了没有）在 ok 与 wait 里。
	// ok 为假配的那句话与命令行 pier wait 的非 0 退出是同一个意思。
	if ready == len(out) {
		ok(w, response{OK: true, Msg: fmt.Sprintf("%d 个服务已就绪", ready), Wait: out})
		return
	}
	ok(w, response{OK: false, Msg: "没等到：" + view.WaitMissed(out, timeout), Wait: out})
}

// readSelection 读请求体里的选择，读不动时已经把话说出去了。
//
// 只认认得的字段（DisallowUnknownFields）：写错了键名（{"service":"api"}）当场
// 报错，比当成「没有选择」于是启动全部服务要安全得多。
func readSelection(w http.ResponseWriter, r *http.Request) (panel.Selection, bool) {
	var sel panel.Selection
	// 64 KB 顶到天上去了：这份请求体是一串名字，正常几十个字节。
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sel); err != nil {
		if errors.Is(err, io.EOF) {
			return sel, true
		}
		fail(w, http.StatusBadRequest, "请求体要是一个 JSON 对象，例如 {\"group\":\"前端\"}：%v", err)
		return sel, false
	}
	return sel, true
}

// failSelection 把一次批量动作的错误翻成状态码。
//
// 三种错要分得开：请求写法不对（400）、清单里没有（404）、清单都没加载起来（503）。
// 全揉成一句 409 的话，脚本只能去读 msg 里的中文才分得出该改哪一头。
func failSelection(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, panel.ErrBadSelection):
		fail(w, http.StatusBadRequest, "%v", err)
	case errors.Is(err, panel.ErrUnknownSelection):
		fail(w, http.StatusNotFound, "%v", err)
	case errors.Is(err, manage.ErrNoConfig):
		fail(w, http.StatusServiceUnavailable, "%v", err)
	default:
		fail(w, http.StatusConflict, "%v", err)
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, _, err := s.panel.Lookup(name); err != nil {
		fail(w, http.StatusNotFound, "%v", err)
		return
	}
	q := r.URL.Query()
	var since int64
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, "since 需要是一个非负的字节数，收到 %q", v)
			return
		}
		since = n
	}
	out, err := s.panel.Logs(name, strings.TrimSpace(q.Get("date")), since)
	if err != nil {
		fail(w, http.StatusInternalServerError, "%v", err)
		return
	}
	ok(w, response{OK: out.OK, Log: &out})
}
