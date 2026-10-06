package config

import (
	"strings"
	"testing"
)

// load 从一段 YAML 载入一份清单，路径落在临时目录里。
func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := writeManifest(t, body, "")
	return Open(path)
}

// mustLoad 载入一份应当没问题的清单。
func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	c, err := load(t, body)
	if err != nil {
		t.Fatalf("清单没加载起来：%v", err)
	}
	return c
}

// orderNames 把一份顺序里的名字连起来，便于和期望值比对。
func orderNames(list []*Service) string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return strings.Join(out, " ")
}

// TestStartOrderPutsDependenciesFirst 钉着启动顺序。
//
// 依赖写在前还是后都该排出同一个结果：`depends_on` 说的是关系，
// 而人在清单里写服务时是按分组、按项目的次序写的，不是按依赖的拓扑序写的。
func TestStartOrderPutsDependenciesFirst(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: web
    dir: web
    kind: node
    depends_on: [api]
  - name: api
    dir: api
    kind: go
    depends_on: [db]
  - name: db
    dir: db
    kind: go
  - name: alone
    dir: alone
    kind: node
`)
	got := orderNames(c.StartOrder())
	// 同层的保持清单原顺序：web 依赖 api，api 依赖 db，于是 db 必须在最前；
	// alone 不依赖谁，按它在清单里的位置排在 db 之后、api 之前。
	want := "db alone api web"
	if got != want {
		t.Errorf("启动顺序 = %q，想要 %q", got, want)
	}
	if got := orderNames(c.StopOrder()); got != "web api alone db" {
		t.Errorf("停止顺序 = %q，想要启动顺序倒过来", got)
	}
}

// TestOrderIsIndependentOfDeclarationOrder 把依赖方写在前面，结果不变。
func TestOrderIsIndependentOfDeclarationOrder(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db]
  - name: db
    dir: db
    kind: go
`)
	if got := orderNames(c.StartOrder()); got != "db api" {
		t.Errorf("启动顺序 = %q，想要 %q", got, "db api")
	}
}

// TestStartOrderKeepsManifestOrderWithinALayer 钉着同层不打乱。
//
// 清单顺序是列在人眼前的顺序，界面上的「全部启动」按的就是它；
// 排完依赖之后再按名字排一次，用户看到的就是另一份名单了。
func TestStartOrderKeepsManifestOrderWithinALayer(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: zebra
    dir: z
    kind: go
  - name: apple
    dir: a
    kind: go
  - name: mango
    dir: m
    kind: go
`)
	if got := orderNames(c.StartOrder()); got != "zebra apple mango" {
		t.Errorf("启动顺序 = %q，想要按清单原顺序", got)
	}
}

// TestStartOrderCoversEveryone 钉着一个人都不能少。
//
// 循环的写法（每轮扫一遍、就绪的拿走）最容易在写错时静默丢人，
// 而丢掉的那个会在「全部启动」里凭空消失。
func TestStartOrderCoversEveryone(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: a
    dir: a
    kind: go
    depends_on: [b, c]
  - name: b
    dir: b
    kind: go
    depends_on: [c]
  - name: c
    dir: c
    kind: go
  - name: d
    dir: d
    kind: go
  - name: e
    dir: e
    kind: go
    depends_on: [d]
`)
	got := c.StartOrder()
	if len(got) != 5 {
		t.Fatalf("顺序里有 %d 个，想要 5：%s", len(got), orderNames(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s.Name] {
			t.Errorf("%s 出现了两次：%s", s.Name, orderNames(got))
		}
		seen[s.Name] = true
	}
	// 每个服务都得排在它的依赖之后。
	pos := map[string]int{}
	for i, s := range got {
		pos[s.Name] = i
	}
	for _, s := range c.Services {
		for _, d := range s.DependsOn {
			if pos[DepName(d)] > pos[s.Name] {
				t.Errorf("%s 排在了它的依赖 %s 前面：%s", s.Name, d, orderNames(got))
			}
		}
	}
}

// TestConditionDoesNotChangeOrder 钉着条件那半截不参与排序。
//
// 「mysql:healthy」指的仍然是 mysql 这个服务：写条件的人说的是「等它」，
// 不是换了一个依赖对象。按整串去比的话，这条依赖匹配不上任何服务，
// 排出来的顺序里 mysql 会被扔到最后——一个不会报错、只会把顺序搞反的坑。
func TestConditionDoesNotChangeOrder(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [mysql:healthy, redis:healthy]
  - name: mysql
    dir: mysql
    kind: shell
    health: tcp://localhost:3306
  - name: redis
    dir: redis
    kind: shell
    health: "cmd: redis-cli ping"
`)
	if got := orderNames(c.StartOrder()); got != "mysql redis api" {
		t.Errorf("启动顺序 = %q，想要 %q", got, "mysql redis api")
	}
	if got := orderNames(c.StopOrder()); got != "api redis mysql" {
		t.Errorf("停止顺序 = %q，想要 %q", got, "api redis mysql")
	}
}

// TestDepWaitersOnlyCountsConditional 钉着「只等声明了条件的那几条」。
//
// 这是这个功能里最容易过火的一处：一旦顺手把所有前置都等了，
// 一个 Java 服务就会把它后面整条链的启动时间串起来（见 cmdUp 顶上那段）。
func TestDepWaitersOnlyCountsConditional(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db, cache:healthy, mq]
  - name: db
    dir: db
    kind: shell
  - name: cache
    dir: cache
    kind: shell
    health: tcp://localhost:6379
  - name: mq
    dir: mq
    kind: shell
`)
	api, err := c.Find("api")
	if err != nil {
		t.Fatal(err)
	}
	waiters := api.DepWaiters()
	if len(waiters) != 1 || waiters[0] != "cache" {
		t.Errorf("要等的前置 = %v，想要只剩 cache", waiters)
	}
	// 没条件的照旧待在 dependsOn 里排顺序，不能被摘掉。
	if len(api.DependsOn) != 3 {
		t.Errorf("dependsOn 被动了：%v", api.DependsOn)
	}
}

// TestDepNameAndCond 钉着切分本身，边界都给上。
func TestDepNameAndCond(t *testing.T) {
	cases := []struct {
		in   string
		name string
		cond string
	}{
		{"mysql", "mysql", ""},
		{"mysql:healthy", "mysql", CondHealthy},
		{"  mysql : healthy  ", "mysql", CondHealthy},
		{"mysql:", "mysql", ""},
		// 名字里再出现冒号时只切第一个：报错该指向那个不认识的条件，
		// 而不是把整串当成一个不存在的服务名。
		{"mysql:healthy:extra", "mysql", "healthy:extra"},
	}
	for _, c := range cases {
		if got := DepName(c.in); got != c.name {
			t.Errorf("DepName(%q) = %q，想要 %q", c.in, got, c.name)
		}
		if got := DepCond(c.in); got != c.cond {
			t.Errorf("DepCond(%q) = %q，想要 %q", c.in, got, c.cond)
		}
	}
}

// TestHealthyConditionNeedsAProbe 钉着「等一个没有探针的服务」拦在加载这一步。
//
// 放过去的话，启动时会白等满一整个窗口再落一句「没等到」——而它从来没可能等到。
func TestHealthyConditionNeedsAProbe(t *testing.T) {
	_, err := load(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db:healthy]
  - name: db
    dir: db
    kind: shell
`)
	if err == nil {
		t.Fatal("依赖一个没有探针的服务居然加载成功了")
	}
	msg := err.Error()
	for _, want := range []string{"db", "探针", "tcp://"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报错里没提 %q：%s", want, msg)
		}
	}

	// 只是排顺序（没写条件）时不管对方有没有探针：那条依赖说的是次序，
	// 和探针无关，拿探针去拦它是拦错了对象。
	mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db]
  - name: db
    dir: db
    kind: shell
`)
}

// TestUnknownConditionIsRejected 钉着不认识的条件当场报错。
//
// 静默当成「只排顺序」是不行的：用户写下的是一件他以为会发生的事，
// 而它不会发生，事后又没有任何痕迹。
func TestUnknownConditionIsRejected(t *testing.T) {
	_, err := load(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db:started]
  - name: db
    dir: db
    kind: shell
    health: tcp://localhost:3306
`)
	if err == nil || !strings.Contains(err.Error(), "started") {
		t.Errorf("报错 = %v，想要指出 started 这个条件不认识", err)
	}
}

// TestCycleIsRejectedWithTheRingInTheMessage 钉着成环拦在加载这一步。
//
// 留给启动顺序去兜是不行的：拓扑排序遇到环要么死循环，要么随手丢掉几条依赖，
// 而「丢掉的恰好是你最需要的那条」是事后查不出来的。报错里要写出环本身，
// 用户看一眼就知道该删哪一条。
func TestCycleIsRejectedWithTheRingInTheMessage(t *testing.T) {
	_, err := load(t, `
services:
  - name: a
    dir: a
    kind: go
    depends_on: [b]
  - name: b
    dir: b
    kind: go
    depends_on: [c]
  - name: c
    dir: c
    kind: go
    depends_on: [a]
`)
	if err == nil {
		t.Fatal("成环的清单居然加载成功了")
	}
	msg := err.Error()
	for _, want := range []string{"a", "b", "c", "→"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报错里没写出环：%q", msg)
		}
	}
}

// TestSelfDependencyIsRejected 钉着自己依赖自己。
func TestSelfDependencyIsRejected(t *testing.T) {
	_, err := load(t, `
services:
  - name: a
    dir: a
    kind: go
    depends_on: [a]
`)
	if err == nil || !strings.Contains(err.Error(), "依赖了自己") {
		t.Errorf("报错 = %v，想要「依赖了自己」", err)
	}
}

// TestDependencyOnUnknownService 钉着写错名字时立刻报出来。
//
// 这条尤其要拦：写错的那个名字在清单里压根不存在，启动时「等它起来」
// 会变成一个谁也不知道在等谁的空转。
func TestDependencyOnUnknownService(t *testing.T) {
	_, err := load(t, `
services:
  - name: web
    dir: w
    kind: node
    depends_on: [api]
`)
	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Errorf("报错 = %v，想要指出 api 不在清单里", err)
	}
}

// TestDependencyOnLaterServiceIsFine 钉着依赖的名字排在它后面也认。
//
// 边读边查的实现会把「顺序不同」误报成「名字不存在」。
func TestDependencyOnLaterServiceIsFine(t *testing.T) {
	mustLoad(t, `
services:
  - name: web
    dir: w
    kind: node
    depends_on: [api]
  - name: api
    dir: a
    kind: go
`)
}

// TestRestartPolicyValidation 钉着重启策略只认那几个值。
//
// 写错一个词就静默不重启的话，用户会以为它开着了——而它恰恰是靠不住的
// 那个服务才需要的东西。
func TestRestartPolicyValidation(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: a
    dir: a
    kind: go
    restart: on-failure
  - name: b
    dir: b
    kind: go
`)
	if c.Services[0].Restart != RestartOnFailure {
		t.Errorf("restart = %q", c.Services[0].Restart)
	}
	if c.Services[1].Restart != "" {
		t.Errorf("没写的那个 restart = %q，想要空", c.Services[1].Restart)
	}

	_, err := load(t, `
services:
  - name: a
    dir: a
    kind: go
    restart: always
`)
	if err == nil || !strings.Contains(err.Error(), "always") {
		t.Errorf("报错 = %v，想要指出 always 不是可用值", err)
	}
}
