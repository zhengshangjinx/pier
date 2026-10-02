package proc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
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

// ProbeHealth 探测健康检查 URL，2xx/3xx 视为通过。
func ProbeHealth(url string) bool {
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

// WaitHealthy 轮询等待服务就绪。编译型服务进程起来后还要初始化，
// 只看进程存在会误报成功，所以启动后以健康探针为准。
func WaitHealthy(url string, timeout time.Duration) bool {
	return WaitHealthyContext(context.Background(), url, timeout)
}

// WaitHealthyContext 与 WaitHealthy 相同，但可以被取消（取消时返回 false）：
// 等待就绪期间点了「停止」，就不该再等满三分钟、更不该事后报一个「未通过健康探针」。
func WaitHealthyContext(ctx context.Context, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return false
		}
		if ProbeHealth(url) {
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
