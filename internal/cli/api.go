package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/zhengshangjinx/pier/internal/api"
	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/panel"
)

// apiOptions 是 api 子命令的命令行参数。
type apiOptions struct {
	port      int // 0 表示按偏好里的值来
	addr      string
	showToken bool
	rotate    bool
}

// cmdApi 起一个只服务本机的 HTTP 接口。
//
// 它是个没有界面的宿主，和图形界面、pier ui 是同一个位置上的三种选择：
// 自己建一个面板、加载清单、把接口挂上去。因此这里起的服务和界面里起的是
// 同一回事——同一个状态文件、同一份日志、同一套依赖顺序与重启策略。
//
// 同时开着两个宿主也安全：自动重启的巡检只由拿到独占权的那个跑（见 panel.New）。
func cmdApi(args []string) int {
	cfgPath, rest := extractConfig(args)
	opt, err := parseApiArgs(rest)
	if err != nil {
		return fail("%v", err)
	}

	settings, err := config.SettingsPath()
	if err != nil {
		return fail("%v", err)
	}

	// 只看令牌、只换令牌都不需要把清单加载起来：脚本和部署流程要的是那个字符串，
	// 不该因为清单坏了就拿不到。
	if opt.showToken {
		token, err := api.Token(settings)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Println(token)
		return 0
	}
	if opt.rotate {
		token, err := api.RotateToken(settings)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("已换用新的访问令牌：%s\n", token)
		return 0
	}

	path, src, err := config.Resolve(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	p := panel.New()
	defer p.Close()
	p.SetSource(src)
	// 清单加载失败也照样把接口起起来。脚本这时能从 503 或 /api/state 的回包里
	// 读到确切原因（哪一行、哪里不对）；直接退出的话它只看到「连不上」，
	// 而那句原因只留在启动这个进程的人眼前的一行 stderr 上——多半是个后台任务，
	// 根本没人看。图形界面早就是这个做法：显示引导页而不是白屏。
	if err := p.Load(path); err != nil {
		p.SetLoadError(err.Error())
		fmt.Fprintf(os.Stderr, "清单加载失败：%v\n", err)
	}

	token, err := api.Token(settings)
	if err != nil {
		return fail("%v", err)
	}
	if opt.port == 0 {
		opt.port = api.Port(settings)
	}
	addr, err := listenAddr(opt)
	if err != nil {
		return fail("%v", err)
	}

	srv := api.New(p, addr, token)
	if err := srv.Listen(); err != nil {
		return fail("%v", err)
	}

	fmt.Printf("Pier 本地接口已监听 %s\n", srv.Addr())
	fmt.Printf("访问令牌：%s\n\n", token)
	fmt.Printf("  curl -H 'Authorization: Bearer %s' %s/api/services\n", token, baseURL(srv.Addr()))
	fmt.Printf("  接口说明：%s/openapi.json\n\n", baseURL(srv.Addr()))
	if !p.OwnsRestarts() {
		fmt.Println("注意：自动重启正由另一个 Pier 进程负责，这次不由本进程巡检。")
	}

	// Ctrl-C 收场。不这样做的话，终端里那个 ^C 只会杀掉前台进程组里的东西，
	// 而这个进程还要自己把监听端口放掉，否则下一次启动会报端口被占。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	fmt.Println("按 Ctrl-C 结束。")
	return codeOrZero(srv.Serve())
}

// listenAddr 把 --addr 与 --port 合成一个监听地址，并挡住回环之外的地址。
//
// 这个接口能启停进程，绑到 0.0.0.0 就等于把「在这台机器上跑什么」交给整个局域网，
// 而 net.Listen 自己对地址来者不拒——只能在这里拦，而且要拦在起监听之前。
func listenAddr(opt *apiOptions) (string, error) {
	host, port := api.DefaultAddr, opt.port
	if opt.addr != "" {
		h, p := splitAddr(opt.addr)
		host = h
		if p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return "", fmt.Errorf("端口要是一个 1~65535 之间的数，收到 %q", p)
			}
			port = n
		}
	}
	if !isLoopback(host) {
		if host == "" {
			return "", fmt.Errorf("接口只能监听回环地址（127.0.0.1 / ::1 / localhost）：地址里没有主机名，那样会绑到所有网卡上")
		}
		return "", fmt.Errorf("接口只能监听回环地址（127.0.0.1 / ::1 / localhost）：「%s」会让同网段的人也能启停你的服务", host)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// splitAddr 拆出 --addr 里的主机与端口。端口可以省略（那时用 --port），
// 写法上 [::1]:7717 与 ::1 两种都认。
//
// 拆不出来的一律当成主机名交给 isLoopback 去拒，这里不另判一次——
// 两处判断迟早给出两种说法，而用户只需要知道「这个地址不行」。
func splitAddr(s string) (host, port string) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return strings.Trim(s, "[]"), ""
}

// isLoopback 判断要监听的主机是不是回环。只认字面量，不做 DNS 解析：
// 一个会去查域名的判定，本身就多出一条能失败的路。
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// baseURL 把监听地址补成能直接粘进浏览器或 curl 的地址。
// 监听 0.0.0.0 时那个地址是敲不通的，要换成回环——而这里只会绑回环，
// 这一句纯粹是防着将来有人改地址时忘了这茬。
func baseURL(addr string) string {
	host, port, err := splitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = api.DefaultAddr
	}
	return "http://" + host + ":" + port
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return "", "", fmt.Errorf("缺少端口")
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}

// codeOrZero 把「接口服务退出」翻成退出码。正常收场（有人按了 Ctrl-C 或调了
// Close）不算失败。
func codeOrZero(err error) int {
	if err == nil {
		return 0
	}
	return fail("接口服务退出：%v", err)
}

// parseApiArgs 解析 api 的参数，支持 --key 值 与 --key=值 两种写法。
func parseApiArgs(args []string) (*apiOptions, error) {
	opt := &apiOptions{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val := a, ""
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "-") {
			key, val = k, v
		}
		take := func() (string, error) {
			if val != "" {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 后面要跟一个值", key)
			}
			i++
			return args[i], nil
		}
		switch key {
		case "--show-token":
			opt.showToken = true
		case "--rotate":
			opt.rotate = true
		case "--port", "-p":
			v, err := take()
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n <= 0 || n > 65535 {
				return nil, fmt.Errorf("端口要是一个 1~65535 之间的数，收到 %q", v)
			}
			opt.port = n
		case "--addr":
			v, err := take()
			if err != nil {
				return nil, err
			}
			opt.addr = strings.TrimSpace(v)
		default:
			return nil, fmt.Errorf("不认识的参数：%s", a)
		}
	}
	return opt, nil
}
