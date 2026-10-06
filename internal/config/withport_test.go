package config

import "testing"

// 「换一个端口起」全靠这一个函数：它同时管着两件事——返回的是副本（清单一个字
// 都不动），以及健康探针地址里的端口跟着换。两条都会以「安静地做错」的方式失败，
// 所以逐条钉住。

func TestWithPortCopiesAndRewritesHealth(t *testing.T) {
	svc := &Service{
		Name:   "api",
		Port:   8080,
		Health: "http://localhost:8080/actuator/health",
		Env:    map[string]string{"PORT": "${PORT}", "BASE": "http://localhost:8080/x"},
		Run:    "node server.js",
	}

	got := WithPort(svc, 8081)

	if got == svc {
		t.Fatal("WithPort 返回了同一个指针：换端口会被写回清单里那一份")
	}
	if got.Port != 8081 {
		t.Errorf("Port = %d，想要 8081", got.Port)
	}
	if want := "http://localhost:8081/actuator/health"; got.Health != want {
		t.Errorf("Health = %q，想要 %q", got.Health, want)
	}
	if svc.Port != 8080 || svc.Health != "http://localhost:8080/actuator/health" {
		t.Errorf("原服务被改了：Port=%d Health=%q", svc.Port, svc.Health)
	}
	// 其余字段照抄，一样都不许丢：换端口这件事只跟端口有关。
	if got.Name != svc.Name || got.Run != svc.Run {
		t.Errorf("副本丢了字段：%+v", got)
	}
	// 值里的字面 URL 不动：那多半是给别人用的配置，替它改只会把一件看着对的事搞坏。
	if got.Env["BASE"] != "http://localhost:8080/x" {
		t.Errorf("Env 里的地址被改了：%q", got.Env["BASE"])
	}
}

func TestWithPortNoopCases(t *testing.T) {
	svc := &Service{Name: "api", Port: 8080, Health: "http://localhost:8080/h"}
	cases := []struct {
		name string
		svc  *Service
		port int
	}{
		{"端口相同", svc, 8080},
		{"端口为 0（没挑到空闲的）", svc, 0},
		{"端口为负", svc, -1},
		{"服务是 nil", nil, 8081},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithPort(c.svc, c.port); got != c.svc {
				t.Errorf("应当原样返回，拿到的是 %+v", got)
			}
		})
	}
}

// 探针里的端口只在「它真的是端口」时才换。路径里出现同一个数字是常事，
// 按字符串替换会把它一起改掉——那是个改错了也不会有人发现的地方。
func TestWithPortInProbe(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"普通地址", "http://localhost:8080/health", "http://localhost:8081/health"},
		{"带路径与查询串", "http://localhost:8080/api/v2/health?deep=1", "http://localhost:8081/api/v2/health?deep=1"},
		{"换的是 IP", "http://127.0.0.1:8080/", "http://127.0.0.1:8081/"},
		{"端口不是那个数就不动", "http://localhost:9000/health", "http://localhost:9000/health"},
		{"路径里有同一个数字", "http://localhost:8080/api/8080/x", "http://localhost:8081/api/8080/x"},
		{"不带端口", "http://localhost/health", "http://localhost/health"},
		{"本来就不是 URL", "反正是空着", "反正是空着"},
		{"空串", "", ""},
		// tcp 探针走的是同一条路：它也是个带端口的地址，跟着换才探得到换过端口的那一份。
		{"tcp 探针", "tcp://localhost:8080", "tcp://localhost:8081"},
		{"tcp 探针端口不是那个数", "tcp://localhost:5432", "tcp://localhost:5432"},
		// cmd 探针是一条命令，里面的端口是用户写的字，不做替换：把命令里所有的
		// 8080 换成 8081，或者只换其中一处，两种都可能在改一件不该改的事。
		{"cmd 探针不动", "cmd: curl -f http://localhost:8080/health", "cmd: curl -f http://localhost:8080/health"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withPortInProbe(c.raw, 8080, 8081); got != c.want {
				t.Errorf("withPortInProbe(%q) = %q，想要 %q", c.raw, got, c.want)
			}
		})
	}
}
