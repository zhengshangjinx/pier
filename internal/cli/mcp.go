package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/mcp"
	"github.com/zhengshangjinx/pier/internal/panel"
)

// cmdMcp 在 stdin / stdout 上说 MCP，给 Claude Code、Cursor 这类客户端用。
//
// 与 cmdApi 是同一个位置上的两种做法，差别只在通道：那边是回环上的 HTTP 加一个
// 令牌，这边是一对管道。管道能成立，是因为用它的人就在本机、而且它就是把这个
// 进程起起来的那一个——进程的 stdin / stdout 交回给父进程，是操作系统给的，
// 不必再开一个端口去等谁来连，也就不必再保护那个端口。
//
// 它是个没有界面的宿主（和 pier api、pier ui 一样）：自己建一个面板、加载清单、
// 把七个工具挂上去。因此这里起的服务与界面里起的是同一回事。
//
// 有两条绝对的规矩：**stdout 上只能有 MCP 的消息**（客户端按行解析，混进去一句
// 「已启动」就整条通道错位），人看的字一律走 stderr。所有回包都经 Server.reply
// 一处写出，就是为了守得住这一条。
func cmdMcp(args []string) int {
	cfgPath, rest := extractConfig(args)
	if len(rest) > 0 {
		return fail("mcp 不接受参数：%s", rest[0])
	}

	path, src, err := config.Resolve(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	p := panel.New()
	defer p.Close()
	p.SetSource(src)
	// 清单加载失败也照样把服务起起来：客户端这时能收到 initialize 与 tools/list，
	// 调 list_services 会拿到「读不到清单：哪一行哪里不对」。直接退出的话，
	// 客户端那边看到的是「这个 MCP 服务端连不上」，而真正的原因只留在它自己
	// 那份日志里——与 pier api 同样的取舍。
	if err := p.Load(path); err != nil {
		p.SetLoadError(err.Error())
		fmt.Fprintf(os.Stderr, "清单加载失败：%v\n", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := mcp.New(mcp.Tools(p))
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		// 出错只说给 stderr：stdout 上那一条通道可能还开着，往那儿写一句
		// 非协议的话，客户端只会拿它去解析，然后报一句看不懂的错。
		fmt.Fprintf(os.Stderr, "pier mcp 退出：%v\n", err)
		return 1
	}
	return 0
}
