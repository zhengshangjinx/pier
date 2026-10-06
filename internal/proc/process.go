package proc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// healthTimeout 是单次健康探针的请求超时。
const healthTimeout = 2 * time.Second

// healthPoll 是等待健康探针通过时的轮询间隔。
const healthPoll = 500 * time.Millisecond

// HealthWait 是启动后等待健康探针的上限。Java 服务首次启动要等 Maven 与 Spring 初始化，
// 给得宽一些；编译已经在同步的编译步骤里完成，这里等的只是运行期初始化。
//
// 放在这里而不是各入口自己定义：命令行、终端面板、图形界面等的是同一件事，
// 三个地方各写一个 180s，改的时候必然漏掉一两个。
const HealthWait = 180 * time.Second

// stopGrace 是收到 TERM 后等待进程自行退出的宽限期，超时才强杀。
const stopGrace = 5 * time.Second

// pollInterval 是等待进程退出时的轮询间隔。
const pollInterval = 100 * time.Millisecond

// WaitGone 在超时内轮询等待进程退出，返回是否已退出。
func WaitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !ProcessAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// Listener 是一个正在监听某端口的进程。
//
// json 标签必须显式写出来，且与 PortOwner 保持同一套小写命名。
// 不写标签时 Go 会导出成 Port/PID/Command，而界面读的是小写——
// 结果不是报错，是界面上一排「—」和一个 undefined 的按钮文案，
// 后端查得好好的，看起来却像功能没做。这个坑已经踩过一次。
type Listener struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
	User    string `json:"user"`
	// Service 是占用者所属的 Pier 服务名，不属于 Pier 启动的服务时为空。
	// 端口常握在子进程手里，光看 PID 认不出来，得按进程组认（见 ManagedName）；
	// 名字本身不参与判断——服务叫什么、系统里显示成什么叫，都不影响认领。
	Service string `json:"service"`
}

// ListeningPorts 一次性列出本机所有处于 LISTEN 状态的 TCP 端口。
//
// 判断「端口是否被占」必须以系统里的监听套接字为准，而不是去 connect 一下。
// connect 只能证明「连得上」，证明不了「没被占」：实测本机的 Vite 开发服务器
// 绑在 IPv6 通配地址上、且新连接会超时（accept 队列或防火墙），connect 探测
// 得到的是「端口空闲」，可端口明明被占着——那会让「启动前先看端口」的保护
// 形同虚设，甚至误判成可以再起一个实例。
//
// 返回 nil 表示查不到监听表，此时调用方应退回 connect 探测。
func ListeningPorts() map[int]bool {
	info := ListeningInfo()
	if info == nil {
		return nil
	}
	ports := make(map[int]bool, len(info))
	for p := range info {
		ports[p] = true
	}
	return ports
}

// PortOpen 探测本机端口是否已被监听，用于状态展示与「启动前先看看端口」。
func PortOpen(port int) bool {
	if port <= 0 {
		return false
	}
	if ports := ListeningPorts(); ports != nil {
		return ports[port]
	}
	// 监听表读不到时只能退回连接探测，并接受它的局限：
	// 绑在 IPv6 通配地址上的服务可能连不上，从而被漏判为「端口空闲」。
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ProbeHealth 探一次就绪，返回这一次是否通过。
//
// 参数是这条服务的工作目录与清单里 health 那一串原文（三种写法见
// config.ParseProbe）。解析放这儿做是为了让调用方不必各自先解析一遍：
// 它那几个调用点拿到的都是配置/状态文件里的原值。解析不了的当作「没就绪」，
// 不报错——清单在加载时已经拦过一遍（healthProblem），走到这里还解析不了的
// 多半是状态文件里留下的旧值，为它中断启动不值得。
//
// dir 只有 cmd 探针用得上（见 probeCmd），但要一路传下来而不是在那儿读一次
// 进程的当前目录：这一串是从清单里念出来的，探它的是谁（界面、命令行、还是
// 那条三秒一次的巡检）跟它该在哪儿跑没有关系。
func ProbeHealth(dir, raw string) bool {
	return probeHealthContext(context.Background(), dir, raw)
}

// probeHealthContext 是 ProbeHealth 的带 ctx 版本：cmd 探针是一条要跑起来的命令，
// 取消时得连它派生的进程一起收掉（见 probeCmd）。
func probeHealthContext(ctx context.Context, dir, raw string) bool {
	p, err := config.ParseProbe(raw)
	if err != nil {
		return false
	}
	switch p.Kind {
	case config.ProbeHTTP:
		return probeHTTP(p.URL)
	case config.ProbeTCP:
		return probeTCP(p.Addr)
	case config.ProbeCmd:
		return probeCmd(ctx, dir, p.Cmd)
	}
	return false
}

// DepTarget 是等一条前置就绪时要知道的两件事：探什么，以及那条命令在哪个目录里跑。
//
// 两个字段写在一起而不是让调用方各取一次：目录要和探针取自同一条服务，
// 分头去查迟早一处按清单查、一处按在跑的那一份查，而这两者的差别只在前置
// 让过路的那一种情况下才看得出来（见下）。
type DepTarget struct {
	Probe string
	Dir   string
}

// DepProbe 给出等这个前置就绪时该探的东西，第二个返回值表示它在清单里有没有。
//
// 按「此刻在跑的那一份」算（RunningService）：前置上一次是从别的端口让路起的话，
// 清单里那个端口上站着的可能是别人——探它会得到一个与这次启动无关的答案。
// 目录跟着一起取，理由同上：让过路的那一份与清单里那一份连工作目录都该是同一条。
//
// 界面与命令行等的是同一件事，判定就写这一份：各写一份迟早一处按清单探、
// 一处按状态探，而这两者的差别只在前置让过路的那一种情况下才看得出来。
func DepProbe(cfg *config.Config, name string) (DepTarget, bool) {
	if cfg == nil {
		return DepTarget{}, false
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return DepTarget{}, false
	}
	state, err := LoadState(cfg.StatePath())
	if err != nil {
		// 状态文件读不动就退回清单里那一串：与其说「没有可探的」，不如探一次。
		return DepTarget{Probe: svc.Health, Dir: svc.AbsDir()}, true
	}
	run := RunningService(svc, state)
	return DepTarget{Probe: run.Health, Dir: run.AbsDir()}, true
}

// probeHTTP 发一次 GET，2xx/3xx 视为通过。
func probeHTTP(url string) bool {
	if url == "" {
		return false
	}
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 必须读完并关闭，否则连接无法复用，轮询时会不断新建连接。
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// probeTCP 连一次就算通过，连上立刻断开。
//
// 刻意不去读任何东西：tcp 探针要回答的只是「这个端口后面有人在听」，
// 多说一句（比如发一个协议握手）就要认识每种服务的协议，那正是它要避开的事。
func probeTCP(addr string) bool {
	if addr == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", addr, healthTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// probeCmd 跑一条命令，退出码 0 算通过。
//
// 命令照旧交给 shell（config.ShellArgv）：写在清单里的是一整条命令，可能带管道、
// 重定向、`&&`，各平台用哪个 shell 与服务的 run 完全一致——探针能用的写法，
// 不该和启动命令的写法有两套规矩。输出一律丢掉，探针只回答一个是非题。
//
// 在服务自己的工作目录里跑（dir），和 run / build 一样。不这么写的话，
// 命令里的相对路径解的是「Pier 自己的当前目录」：命令行下是用户敲命令的那一层，
// 界面从访达启动时是 `/`——同一份清单在两条路上探出不同的答案，而后者几乎必然
// 让探针永远不通过，界面上只会看到「启动了但没通过探针」。
//
// 进程组与超时按 runSync 那一套来，理由是同一个：命令派生的子进程（pg_isready
// 这类工具自己 fork 的、或 shell 起的一串）不会因为杀掉 shell 就跟着结束，
// 轮询每 500ms 起一条，漏掉一次就是一份越积越多的僵尸。
func probeCmd(ctx context.Context, dir, cmdline string) bool {
	if cmdline == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	argv := config.ShellArgv(cmdline)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	setSyncGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	// 超时被杀时退出码非 0，本来就算「没就绪」；这一步是为了把子进程一并收掉。
	if ctx.Err() != nil && cmd.Process != nil {
		killSyncTree(cmd.Process.Pid)
	}
	return err == nil
}

// WaitHealthy 轮询等待服务就绪。编译型服务进程起来后还要初始化，
// 只看进程存在会误报成功，所以启动后以健康探针为准。
//
// dir 是这条服务的工作目录，只有 cmd 探针用得上（见 probeCmd）。
func WaitHealthy(dir, raw string, timeout time.Duration) bool {
	return WaitHealthyContext(context.Background(), dir, raw, timeout)
}

// WaitHealthyContext 与 WaitHealthy 相同，但可以被取消（取消时返回 false）：
// 等待就绪期间点了「停止」，就不该再等满三分钟、更不该事后报一个「未通过健康探针」。
func WaitHealthyContext(ctx context.Context, dir, raw string, timeout time.Duration) bool {
	if raw == "" {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return false
		}
		if probeHealthContext(ctx, dir, raw) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(healthPoll):
		}
	}
}
