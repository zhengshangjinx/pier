package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 本文件解析 health 字段——「这个服务怎样才算就绪」的三种写法。
//
// 解析放在 config 而不是 proc：探针写成什么样是清单里的事，而「这一串是个什么」
// 要有一个判定，被清单校验（healthProblem）、端口替换（withPortInProbe）与真正
// 探它的那两个消费者共用。proc 那边只管怎么探（见 proc.WaitHealthyContext）。
//
// 分开写两份判定的代价是「填写时放过、跑起来不认」：用户存下一条 Pier 自己解析
// 不了的探针，界面写着已保存，等到启动时才发现它永远不通过——那时人已经忘了
// 自己填过什么。

// 健康探针的三种写法。
const (
	// ProbeHTTP 是一个 http(s) 地址，GET 到 2xx/3xx 算就绪。
	ProbeHTTP = "http"
	// ProbeTCP 是一个 host:port，连得上算就绪。
	//
	// 数据库、注册中心、消息队列这些没有 HTTP 接口，而「它能不能连了」恰恰是
	// 本地起后端时最需要知道的一件事——只有 http 探针时，这一件事完全没法表达，
	// 于是只能把 wait 写进启动脚本或者干脆不检查。
	ProbeTCP = "tcp"
	// ProbeCmd 是一条命令，退出码 0 算就绪。
	//
	// 给的是「探针这件事没有统一答案」的那一半：pg_isready、redis-cli ping、
	// 自己写的一个小脚本，都是某个服务自己的就绪定义，写死在 Pier 里既写不全也写不对。
	ProbeCmd = "cmd"
)

// Probe 是一条健康探针解析之后的样子，Kind 决定其余哪个字段有效。
type Probe struct {
	Kind string
	// URL 是 http(s) 的完整地址。
	URL string
	// Addr 是 tcp 的 host:port。
	Addr string
	// Cmd 是 cmd 要跑的那条命令。
	Cmd string
}

// ParseProbe 解析清单里的 health 字段。留空返回零值 Probe 与 nil——那是
// 「这类服务没有健康接口」，是正当写法，不是错。
//
// 三种写法：
//
//	http://localhost:8080/health   GET 一下，2xx/3xx 算就绪（https 同）
//	tcp://localhost:3306           连得上算就绪
//	cmd: pg_isready -h localhost   跑一条命令，退出码 0 算就绪
//
// 返回的错误是**一句接着「服务 X 的 health」读下去的话**：唯一的消费方是
// healthProblem，它把这句拼进清单的错误里（见那里的说明）。
func ParseProbe(raw string) (Probe, error) {
	if raw == "" {
		return Probe{}, nil
	}
	// cmd 排在空格检查前面：它后面就是一整条命令，里面有空格是常态。
	if cmd, ok := cmdProbe(raw); ok {
		if cmd == "" {
			return Probe{}, errors.New("的 cmd: 后面要写命令，退出码 0 就算就绪，比如 cmd: pg_isready -h localhost")
		}
		return Probe{Kind: ProbeCmd, Cmd: cmd}, nil
	}
	// 地址里不该有空格：存下去之后是直接拿去的，它只会回一句
	// 「invalid character " " in host name」，而那时人已经忘了自己填过什么。
	if strings.ContainsAny(raw, " \t\n") {
		return Probe{}, errors.New("里不能有空格（空格要写成 %20）")
	}
	// 漏了 scheme 的两种写法是同一件事，报出来的样子却不一样：localhost:8080/health
	// 会被 url.Parse 收下（localhost 成了 scheme），127.0.0.1:8080/health 则直接报错
	// （冒号落在第一段路径里）——后者的原话是「first path segment in URL cannot contain
	// colon」，对着一个在填表单的人等于没说。所以先补上 http:// 试一次，成立就按这件事说。
	//
	// 只对纯 ASCII 这么判：url.Parse 对中文域名照收不误，不拦一道就会建议人家
	// 去访问 http://我的服务 ，那种「建议」比不说还乱。
	if !strings.Contains(raw, "://") && strings.IndexFunc(raw, func(r rune) bool { return r > 127 }) < 0 {
		if u, err := url.Parse("http://" + raw); err == nil && u.Host != "" {
			// tcp 那句只取主机与端口：原样补一个 tcp:// 会把路径也带上，
			// 而 tcp 探针只连主机端口，那个路径写在那儿是句废话。
			return Probe{}, errors.New("要写成完整的地址，补上 http:// —— http://" + raw +
				"\n（只要端口连得上就算就绪的话写 tcp://" + u.Host + "，要跑命令判就写 cmd: …）")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Probe{}, fmt.Errorf("不是一个能用的地址：%v", err)
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return Probe{}, errors.New("里没有主机名，比如 http://localhost:8080/health")
		}
		return Probe{Kind: ProbeHTTP, URL: raw}, nil
	case "tcp":
		// 端口是必填的：连一个没有端口的目标无从谈起（url 那边没有默认值可补）。
		if u.Hostname() == "" || u.Port() == "" {
			return Probe{}, errors.New("的 tcp 地址要写成 tcp://主机:端口，比如 tcp://localhost:3306")
		}
		return Probe{Kind: ProbeTCP, Addr: u.Host}, nil
	}
	return Probe{}, fmt.Errorf("只认 http、https、tcp 与 cmd:，这里是 %s://", u.Scheme)
}

// cmdProbe 认出 cmd: 这种写法，返回后面那条命令；不是这种写法时第二个返回值为假。
//
// 大小写不计较：写成 Cmd: 的人不会觉得自己写的是另一样东西，而按原样收下会让它
// 掉进下面那条 http 的路，报出来的话（「只认 http、https、tcp 与 cmd:」）对着一个
// 明明写了 cmd 的人等于没说。
func cmdProbe(raw string) (string, bool) {
	if len(raw) < 4 || !strings.EqualFold(raw[:4], "cmd:") {
		return "", false
	}
	return strings.TrimSpace(raw[4:]), true
}
