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
