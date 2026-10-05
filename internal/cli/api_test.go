package cli

import (
	"strings"
	"testing"
)

// 这个接口能启停进程，绑到回环之外就等于把「在这台机器上跑什么」交给整个局域网。
// 下面这张表守的就是这一条：能绑的只有回环，别的连合成地址都不该成功。
func TestListenAddrOnlyLoopback(t *testing.T) {
	cases := []struct {
		name string
		opt  apiOptions
		want string // 空表示必须报错
		says string // 报错时要提到的东西
	}{
		{name: "默认", opt: apiOptions{port: 7717}, want: "127.0.0.1:7717"},
		{name: "回环加端口", opt: apiOptions{addr: "127.0.0.1:8080"}, want: "127.0.0.1:8080"},
		{name: "回环别的号", opt: apiOptions{addr: "127.0.0.2:8080"}, want: "127.0.0.2:8080"},
		{name: "不带端口就用 --port", opt: apiOptions{addr: "127.0.0.1", port: 9000}, want: "127.0.0.1:9000"},
		{name: "localhost", opt: apiOptions{addr: "localhost", port: 1}, want: "localhost:1"},
		{name: "IPv6 带方括号", opt: apiOptions{addr: "[::1]:8080"}, want: "[::1]:8080"},
		{name: "IPv6 裸写", opt: apiOptions{addr: "::1", port: 8080}, want: "[::1]:8080"},
		{name: "addr 里的端口压过 --port", opt: apiOptions{addr: "127.0.0.1:8080", port: 9000}, want: "127.0.0.1:8080"},

		{name: "全网卡", opt: apiOptions{addr: "0.0.0.0:7717"}, says: "0.0.0.0"},
		{name: "IPv6 全网卡", opt: apiOptions{addr: "[::]:7717"}, says: "「::」"},
		{name: "只写端口", opt: apiOptions{addr: ":7717"}, says: "没有主机名"},
		{name: "局域网地址", opt: apiOptions{addr: "192.168.1.5:7717"}, says: "192.168.1.5"},
		{name: "域名", opt: apiOptions{addr: "example.com:7717"}, says: "example.com"},
		{name: "端口越界", opt: apiOptions{addr: "127.0.0.1:70000"}, says: "1~65535"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := listenAddr(&c.opt)
			if c.want == "" {
				if err == nil {
					t.Fatalf("listenAddr(%+v) = %q，本该报错", c.opt, got)
				}
				if !strings.Contains(err.Error(), c.says) {
					t.Fatalf("错误里要提到 %q，实际是：%v", c.says, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("listenAddr(%+v) 报错：%v", c.opt, err)
			}
			if got != c.want {
				t.Fatalf("listenAddr(%+v) = %q，想要 %q", c.opt, got, c.want)
			}
		})
	}
}
