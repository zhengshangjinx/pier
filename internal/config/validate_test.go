package config

import (
	"strings"
	"testing"
)

// 健康探针地址要在存下来的那一刻就挡一道。少了 scheme 的那种写法（localhost:8080/health）
// url.Parse 是收得下的——它把 localhost 当成了 scheme——于是探针永远发不出去，
// 服务一路挂到「探针过期」才被人看见，而那时人不会把这两件事连起来。
func TestHealthProblem(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
		// want 是错的那几句必须提到的东西；空着表示只判 ok。
		want string
	}{
		{name: "http 正常", in: "http://localhost:8080/health", ok: true},
		{name: "https 正常", in: "https://api.example.com/healthz", ok: true},
		{name: "带端口与查询串", in: "http://127.0.0.1:3000/health?full=1", ok: true},
		{name: "漏了 scheme：把补好的一句给他", in: "localhost:8080/health", want: "http://localhost:8080/health"},
		{name: "漏了 scheme 的 ip", in: "127.0.0.1:8080/health", want: "http://127.0.0.1:8080/health"},
		{name: "别的协议不收", in: "ftp://example.com/health", want: "http"},
		{name: "只有 scheme 没有主机", in: "http://", want: "主机名"},
		{name: "斜杠吃掉主机名", in: "http:///health", want: "主机名"},
		{name: "压根不是个地址", in: "http://[::1", want: "不是一个能用的地址"},
		{name: "带空格", in: "http://localhost:8080/my health", want: "空格"},
		{name: "两头带空格", in: " http://localhost:8080/health ", want: "空格"},
		// tcp：连得上就算就绪。数据库、缓存、消息队列这些没有 HTTP 接口，
		// 而「它能不能连了」正是本地起后端最想知道的。
		{name: "tcp 正常", in: "tcp://localhost:3306", ok: true},
		{name: "tcp 带地址", in: "tcp://127.0.0.1:6379", ok: true},
		{name: "tcp 没有端口", in: "tcp://localhost", want: "端口"},
		{name: "tcp 没有主机", in: "tcp://:3306", want: "tcp://主机:端口"},
		{name: "tcp 端口不是数字", in: "tcp://localhost:abc", want: "不是一个能用的地址"},
		// cmd：退出码 0 算就绪。命令里带空格是常态，不能被上面那条空格检查吃掉。
		{name: "cmd 正常", in: "cmd: pg_isready -h localhost", ok: true},
		{name: "cmd 大小写", in: "Cmd: redis-cli ping", ok: true},
		{name: "cmd 没写命令", in: "cmd:", want: "命令"},
		{name: "cmd 只有空格", in: "cmd:   ", want: "命令"},
		// 留空是「这类服务没有健康接口」，正当。
		{name: "留空", in: "", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthProblem(tc.in)
			if tc.ok {
				if got != "" {
					t.Fatalf("%q 应当通过，却报：%s", tc.in, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("%q 应当被拒绝", tc.in)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Errorf("报错里没提 %q：%s", tc.want, got)
			}
		})
	}
}

// 校验要真的接到服务上：报错里得有服务名和 health，用户才知道改哪一条。
func TestValidateServicesChecksHealth(t *testing.T) {
	bad := &Config{Services: []*Service{
		{Name: "alpha", Dir: "/tmp/a", Kind: KindGo, Health: "localhost:1001/health"},
	}}
	err := bad.validateServices()
	if err == nil {
		t.Fatal("health 写成 localhost:1001/health 应当被拒绝")
	}
	if msg := err.Error(); !strings.Contains(msg, "alpha") || !strings.Contains(msg, "health") {
		t.Errorf("报错里要说清是哪一条的哪一项：%s", msg)
	}

	good := &Config{Services: []*Service{
		{Name: "alpha", Dir: "/tmp/a", Kind: KindGo, Health: "http://localhost:1001/health"},
	}}
	if err := good.validateServices(); err != nil {
		t.Errorf("正常的 health 不该被拦：%v", err)
	}

	// 留空是「这类服务没有健康接口」，是常见且正当的写法，不能顺手报错。
	none := &Config{Services: []*Service{{Name: "alpha", Dir: "/tmp/a", Kind: KindGo}}}
	if err := none.validateServices(); err != nil {
		t.Errorf("health 留空不该被拦：%v", err)
	}
}
