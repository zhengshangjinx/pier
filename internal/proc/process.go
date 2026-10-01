package proc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
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

// ProcessAlive 判断进程是否真的还在运行。
//
// 注意它带一个副作用：如果该进程是 Pier 的子进程且已经退出，这里会顺手把它回收掉。
// 这一步不能省——子进程退出后若没人 Wait，会以僵尸态继续占着 PID，
// 而 kill(pid, 0) 对僵尸依然返回成功。结果是「等服务退出」永远等不到，
// 停止一个服务要白白耗完宽限期再报一句「未能停止」。
// pier-gui 是常驻进程，它启动的服务正是它的子进程，这个坑一定会踩到。
//
// 不是自己子进程时 Wait4 返回 ECHILD，直接落到 kill 探测，语义不变。
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	var ws syscall.WaitStatus
	if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err == nil && wpid == pid {
		return false // 刚刚回收掉的，就是它已经退出了
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// SameGroup 校验 pid 仍属于记录在案的进程组。
// 进程退出后 PID 可能被系统复用，仅凭 PID 发信号有误杀无关进程的风险，
// 因此动手前必须确认它还在原来的进程组里。
func SameGroup(pid, pgid int) bool {
	if pid <= 0 || pgid <= 0 {
		return false
	}
	g, err := syscall.Getpgid(pid)
	return err == nil && g == pgid
}

// KillGroup 向整个进程组发信号；进程组不可用时退化为只发给该进程。
// 服务实际是 mvn / pnpm / sh 这类会派生子进程的壳，只杀壳会留下孤儿占着端口。
func KillGroup(pgid, pid int, sig syscall.Signal) error {
	if pgid > 0 {
		if err := syscall.Kill(-pgid, sig); err == nil {
			return nil
		}
	}
	return syscall.Kill(pid, sig)
}

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
// Listener 是一个正监听某端口的进程。
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

// ListeningInfo 一次性列出本机所有 LISTEN 端口及其归属进程。
//
// 用 -F 字段模式而不是默认的表格：命令名里可能有空格，按列切会错位。
// -F 的输出是「字段字母 + 值」逐行排列，p 起一个新进程记录，
// 其后的 c / L 属于该进程，n 行给出该进程的一个监听地址。
//
// 状态刷新每两秒就会调一次，所以这里一次问全，调用方不必再为每个服务各跑一次 lsof。
// 返回 nil 表示 lsof 不可用（没装，或没有任何监听端口时它以非零码退出）。
func ListeningInfo() map[int]Listener {
	out, err := sysOutput("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcuLn")
	if err != nil {
		return nil
	}
	listeners := make(map[int]Listener)
	var cur Listener
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 1 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			cur = Listener{}
			if n, err := strconv.Atoi(val); err == nil {
				cur.PID = n
			}
		case 'c':
			cur.Command = val
		case 'L':
			cur.User = val
		case 'n':
			// 名字列形如 *:3106、127.0.0.1:20351、[::1]:8080、*:3106->*:5555。
			// 只取本地那一侧的端口：箭头右边是连过去的对端，不是监听端口。
			if i := strings.Index(val, "->"); i >= 0 {
				val = val[:i]
			}
			colon := strings.LastIndexByte(val, ':')
			if colon < 0 {
				continue
			}
			port, err := strconv.Atoi(val[colon+1:])
			if err != nil || port <= 0 || port > 65535 {
				continue
			}
			// 同一个端口被多个进程用 SO_REUSEPORT 监听时，保留先出现的那个：
			// 界面上一次只该对一个进程做决定。
			if _, dup := listeners[port]; !dup {
				l := cur
				l.Port = port
				listeners[port] = l
			}
		}
	}
	return listeners
}

// ListeningPorts 一次性列出本机所有处于 LISTEN 状态的 TCP 端口。
//
// 判断「端口是否被占」必须以系统里的监听套接字为准，而不是去 connect 一下。
// connect 只能证明「连得上」，证明不了「没被占」：实测本机的 Vite 开发服务器
// 绑在 IPv6 通配地址上、且新连接会超时（accept 队列或防火墙），connect 探测
// 得到的是「端口空闲」，可端口明明被占着——那会让「启动前先看端口」的保护
// 形同虚设，甚至误判成可以再起一个实例。
//
// 返回 nil 表示 lsof 不可用，此时调用方应退回 connect 探测。
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
	// lsof 不可用时只能退回连接探测，并接受它的局限：
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

// PIDsOnPort 返回占用指定端口的进程号，供状态提示「端口被谁占了」。
func PIDsOnPort(port int) []int {
	if port <= 0 {
		return nil
	}
	out, err := sysOutput("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t")
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}
