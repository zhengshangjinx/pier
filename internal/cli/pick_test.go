package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// pickManifest 写一份带分组、依赖与 manual 的清单，返回读好的 Config。
//
// 目录用清单自己所在的那个：这些服务不会被真的起来，只要清单能通过校验就行。
func pickManifest(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	yaml := "services:\n" +
		"  - name: web\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n    group: 前端\n" +
		"  - name: admin\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n    group: 前端\n" +
		"  - name: api\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n    depends_on: [db]\n" +
		"  - name: db\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"  - name: mock\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n    manual: true\n"
	path := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func pickedNames(svcs []*config.Service) string {
	out := make([]string, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.Name)
	}
	return strings.Join(out, " ")
}

// 不带名字时的那一份「全部」是这一版新长出来的第二种含义，四条边界都要钉住：
// 跳过 manual、按依赖排序、不含未点名的分组、顺序与界面完全一致。
func TestPickServicesAll(t *testing.T) {
	cfg := pickManifest(t)

	// 启动顺着依赖：db 在 api 前面。manual 的 mock 不在里头。
	got, err := pickServices(cfg, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "web admin db api"; pickedNames(got) != want {
		t.Errorf("全部启动 = %q，想要 %q", pickedNames(got), want)
	}

	// 停止反着来：先把被依赖的停掉，后面还在跑的会对着一个关掉的端口刷错误。
	got, err = pickServices(cfg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "api db admin web"; pickedNames(got) != want {
		t.Errorf("全部停止 = %q，想要 %q", pickedNames(got), want)
	}
}

// 点名的照起，manual 也照起——那颗开关说的是「别在全部里带上我」，不是「我不能被起」。
func TestPickServicesByName(t *testing.T) {
	cfg := pickManifest(t)

	got, err := pickServices(cfg, []string{"mock", "web"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "mock web"; pickedNames(got) != want {
		t.Errorf("点名 = %q，想要 %q", pickedNames(got), want)
	}

	// 同一个名字写两遍只起一次：pier up api api 是打顺手了，不是要起两个。
	got, err = pickServices(cfg, []string{"api", "api", "db"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "api db"; pickedNames(got) != want {
		t.Errorf("重复的名字 = %q，想要 %q", pickedNames(got), want)
	}

	if _, err := pickServices(cfg, []string{"没有这个"}, false); err == nil {
		t.Error("不认识的服务名该报错")
	} else if !strings.Contains(err.Error(), "没有这个") {
		t.Errorf("报错里该有那个名字，拿到 %q", err)
	}
}

// @分组 挑的是界面上那一页里的全部服务，含标了 manual 的那些：
// 分组是一份点到具体几个服务的名单，与「别在全部里带上我」不冲突。
func TestPickServicesByGroup(t *testing.T) {
	cfg := pickManifest(t)

	got, err := pickServices(cfg, []string{"@前端"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "web admin"; pickedNames(got) != want {
		t.Errorf("@前端 = %q，想要 %q", pickedNames(got), want)
	}

	// 没写 group 的落进「未分组」，`@未分组` 挑到的就是它们——界面上的内置分组
	// 与命令行这里是同一份名单。
	got, err = pickServices(cfg, []string{"@" + config.UngroupedName}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "api db mock"; pickedNames(got) != want {
		t.Errorf("@未分组 = %q，想要 %q", pickedNames(got), want)
	}

	// 分组和服务名可以混着写，顺序按写的来。
	got, err = pickServices(cfg, []string{"db", "@前端"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "db web admin"; pickedNames(got) != want {
		t.Errorf("混着写 = %q，想要 %q", pickedNames(got), want)
	}

	// 不存在的分组要报出可用的那几组，否则打错一个字只能靠一篇文档去猜。
	_, err = pickServices(cfg, []string{"@后端"}, false)
	if err == nil {
		t.Fatal("不存在的分组该报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "后端") || !strings.Contains(msg, "前端") {
		t.Errorf("报错里该有写错的那个与现有的分组，拿到 %q", msg)
	}
}

// 一个服务真的就叫 @前端 时按服务算：那是一条写进清单里的名字，比分组这个说法更具体。
// 名字撞上语法时让更具体的那种赢，不这么做的人会在不知情的情况下起错一批服务。
func TestPickServicesNameWinsOverGroupSyntax(t *testing.T) {
	dir := t.TempDir()
	yaml := "services:\n" +
		"  - name: '@前端'\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n" +
		"  - name: web\n    dir: " + dir + "\n    kind: shell\n    run: sleep 60\n    group: 前端\n"
	path := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("清单里有个叫 @前端 的服务，该能读进来：%v", err)
	}

	got, err := pickServices(cfg, []string{"@前端"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "@前端"; pickedNames(got) != want {
		t.Errorf("= %q，想要 %q（该按服务名认，不是按分组）", pickedNames(got), want)
	}
}
