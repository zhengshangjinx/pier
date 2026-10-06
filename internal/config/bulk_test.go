package config

import (
	"testing"
)

// 「全部」是这一版里第一次有了第二种含义的词，所以它的边界要钉住：
// 标了 manual 的不在名单里，没标的原样都在，顺序也不许动
// （调用方给的是排好的 StartOrder / StopOrder，这里重排一次就白排了）。
func TestBulkLeavesOutManualOnes(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
  - name: mock
    dir: mock
    kind: go
    manual: true
  - name: web
    dir: web
    kind: node
`)
	if !c.Services[1].Manual {
		t.Fatal("manifest 里的 manual: true 没读进来")
	}
	if c.Services[0].Manual || c.Services[2].Manual {
		t.Error("没写 manual 的被当成了手动服务")
	}

	if got := orderNames(Bulk(c.Services)); got != "api web" {
		t.Errorf("全部启停的名单 = %q，想要 %q", got, "api web")
	}
	// 顺序不许动：传进去什么次序，出来就是什么次序。
	if got := orderNames(Bulk(c.StopOrder())); got != "web api" {
		t.Errorf("按停的顺序过一遍 = %q，想要 %q", got, "web api")
	}
	// 一个都不剩也是合法结果：全都是手动的清单，按「全部启动」就该什么都不起。
	all := &Config{Services: []*Service{{Name: "a", Manual: true}}}
	if got := Bulk(all.Services); len(got) != 0 {
		t.Errorf("全是手动服务时 = %v，想要空", orderNames(got))
	}
}

// StartOrder / StopOrder 不过滤 manual：它们是「顺序」，不是「名单」。
//
// 起一个有前置的服务时，前置要跟着起来（panel 的依赖闭包走的就是这份顺序），
// 崩了要自动重启的服务也照着重启——两件事都跟「参不参与全部」无关。
// 过滤要是长进这两个函数里，这两条会静默失效：前置没起、崩溃的服务不再被拉起来。
func TestOrderStillCoversManualServices(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: api
    dir: api
    kind: go
    depends_on: [db]
  - name: db
    dir: db
    kind: go
    manual: true
`)
	if got := orderNames(c.StartOrder()); got != "db api" {
		t.Errorf("启动顺序 = %q，想要 %q", got, "db api")
	}
	if got := orderNames(c.StopOrder()); got != "api db" {
		t.Errorf("停止顺序 = %q，想要 %q", got, "api db")
	}
}

// InGroup 与 CountInGroup 数的是同一批人：分组名按展示用的那个比，
// 没写 group 的落进「未分组」——`@未分组` 挑到的就是它们。
func TestInGroupMatchesCountAndUngrouped(t *testing.T) {
	c := mustLoad(t, `
services:
  - name: alpha
    dir: a
    kind: go
    group: 前端
  - name: beta
    dir: b
    kind: node
    group: 前端
  - name: gone
    dir: g
    kind: go
`)
	if got := orderNames(c.InGroup("前端")); got != "alpha beta" {
		t.Errorf("前端这一组 = %q，想要 %q", got, "alpha beta")
	}
	if n := c.CountInGroup("前端"); n != len(c.InGroup("前端")) {
		t.Errorf("数出来 %d 个，列出 %d 个", n, len(c.InGroup("前端")))
	}
	if got := orderNames(c.InGroup(UngroupedName)); got != "gone" {
		t.Errorf("未分组 = %q，想要 %q", got, "gone")
	}
	if got := c.InGroup("没有这一组"); len(got) != 0 {
		t.Errorf("不存在的分组 = %q，想要空", orderNames(got))
	}
}
