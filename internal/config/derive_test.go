package config

import (
	"strings"
	"testing"
)

// 本地起 Java 服务的编译步骤不能触发打镜像：docker-maven-plugin 之类绑在 package 阶段的插件
// 会让 install 失败（本机没 Docker，或 Apple 芯片加载不了 x86 原生库），而这与启动服务毫无关系。
func TestJavaBuildSkipsImagePlugins(t *testing.T) {
	svc := &Service{Name: "shop-admin", Dir: "/tmp/x", Kind: KindJava, Module: "shop-admin"}
	p, err := svc.planJava()
	if err != nil {
		t.Fatal(err)
	}
	build := strings.Join(p.Build, " ")
	for _, want := range []string{"-DskipDocker=true", "-Ddocker.skip=true", "-Ddockerfile.skip=true", "-Djib.skip=true",
		"-pl shop-admin -am install"} {
		if !strings.Contains(build, want) {
			t.Errorf("编译命令里缺少 %q：%s", want, build)
		}
	}
	// 连续推导两次，互不串味（共享切片被 append 改写是这类代码的常见坑）。
	other := &Service{Name: "b", Dir: "/tmp/y", Kind: KindJava, Module: "shop-app"}
	p2, _ := other.planJava()
	if strings.Contains(strings.Join(p.Build, " "), "shop-app") || !strings.Contains(strings.Join(p2.Build, " "), "-pl shop-app") {
		t.Errorf("两次推导互相影响了：%v / %v", p.Build, p2.Build)
	}
}

// Node / Python 也要读 build：装依赖这类前置步骤只在这两类里常见，而表单那一栏
// 的占位符写的正是「如 pnpm install」「如 pip install -r requirements.txt」——
// 后端不读它的话，那一栏就是个按了不响的按钮：填了、存了、启动时一句话都没有。
func TestBuildRunsOnNodeAndPython(t *testing.T) {
	cases := []struct {
		name      string
		svc       *Service
		wantBuild string
	}{
		{
			name:      "node 的 build 是装依赖",
			svc:       &Service{Name: "web", Dir: "/tmp/x", Kind: KindNode, Build: "pnpm install", Run: "pnpm dev"},
			wantBuild: "pnpm install",
		},
		{
			name:      "python 的 build 是建 venv",
			svc:       &Service{Name: "api", Dir: "/tmp/x", Kind: KindPython, Build: "pip install -r requirements.txt", Run: "python3 app.py"},
			wantBuild: "pip install -r requirements.txt",
		},
		{
			// build 与 run 都留给推断时，build 那一步仍然在（两者互不影响）。
			name:      "只写 build、run 照推断",
			svc:       &Service{Name: "web", Dir: "/tmp/x", Kind: KindNode, Build: "pnpm install"},
			wantBuild: "pnpm install",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.svc.Plan(nil)
			if err != nil {
				t.Fatalf("推导失败：%v", err)
			}
			got := strings.Join(p.Build, " ")
			if !strings.Contains(got, tc.wantBuild) {
				t.Errorf("编译命令里没有 %q：%v", tc.wantBuild, p.Build)
			}
			if len(p.Run) == 0 {
				t.Error("运行命令不该为空")
			}
		})
	}

	// 这两类默认没有编译这一步：留空时 Build 必须是空的，不能凭空多跑一条命令。
	for _, svc := range []*Service{
		{Name: "web", Dir: "/tmp/x", Kind: KindNode, Run: "pnpm dev"},
		{Name: "api", Dir: "/tmp/x", Kind: KindPython, Run: "python3 app.py"},
	} {
		p, err := svc.Plan(nil)
		if err != nil {
			t.Fatalf("%s 推导失败：%v", svc.Kind, err)
		}
		if len(p.Build) != 0 {
			t.Errorf("%s 留空 build 时不该有编译步骤：%v", svc.Kind, p.Build)
		}
	}
}
