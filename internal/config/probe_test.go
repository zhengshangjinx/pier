package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile 在目录里铺一个文件，必要时连父目录一起建。
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写 %s 失败：%v", rel, err)
	}
}

// nestedYAMLValue 是这里唯一手写的解析器，也是最容易出错的一块：
// 它靠缩进判断层级，还得容忍 @...@ 占位符（严格解析器遇到会直接报错）。
// 下面把各条判断分支都钉住。
func TestNestedYAMLValue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"两层直取", "server:\n  port: 8080\n", "8080"},
		{"外层还有别的键", "app:\n  name: x\nserver:\n  path: /admin\n  port: 20351\n", "20351"},
		{"值带双引号", "server:\n  port: \"8080\"\n", "8080"},
		{"值带单引号", "server:\n  port: '8080'\n", "8080"},
		{"占位符照样读得出", "server:\n  port: 20351\n  name: @project.name@\n", "20351"},
		// 缩进回退后必须离开原来的层：server 之后的 topLevel 是同级键，
		// 它下面的 port 不该被当成 server.port。
		{"同名键在别的层里不算数", "server:\n  path: /a\ntopLevel:\n  port: 9999\n", ""},
		{"键在但值为空", "server:\n  port:\n", ""},
		{"只到一半", "server:\n", ""},
		{"注释行跳过", "server:\n  # port: 1\n  port: 8080\n", "8080"},
		{"同级写法的容器不进入", "server: 8080\n  port: 1\n", ""},
		{"文件不存在", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.yaml")
			if c.body != "" {
				writeFile(t, dir, "config.yaml", c.body)
			}
			if got := nestedYAMLValue(p, []string{"server"}, "port"); got != c.want {
				t.Fatalf("nestedYAMLValue = %q，想要 %q", got, c.want)
			}
		})
	}
}

func TestNestedYAMLValueThreeLevels(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "application.yml", "management:\n  server:\n    port: 20399\n  endpoint:\n    x: 1\n")
	if got := nestedYAMLValue(filepath.Join(dir, "application.yml"),
		[]string{"management", "server"}, "port"); got != "20399" {
		t.Fatalf("三层取值 = %q，想要 20399", got)
	}
}

func TestReadGoFacts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.yaml", "server:\n  path: /admin\n  port: 20351\n")
	if got := ReadGoPort(dir); got != 20351 {
		t.Fatalf("ReadGoPort = %d，想要 20351", got)
	}
	if got := ReadGoPath(dir); got != "/admin" {
		t.Fatalf("ReadGoPath = %q，想要 /admin", got)
	}
}

func TestReadSpringPorts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/main/resources/application.yml",
		"server:\n  port: 20241\nmanagement:\n  server:\n    port: 20299\n")
	if got := ReadSpringPort(dir); got != 20241 {
		t.Fatalf("ReadSpringPort = %d，想要 20241", got)
	}
	if got := ReadSpringManagementPort(dir); got != 20299 {
		t.Fatalf("ReadSpringManagementPort = %d，想要 20299", got)
	}
}

// 没单独配管理端口时读不到，调用方负责退回业务端口（见 DeriveFacts）。
func TestReadSpringManagementPortMissing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/main/resources/application.yml", "server:\n  port: 20241\n")
	if got := ReadSpringManagementPort(dir); got != 0 {
		t.Fatalf("ReadSpringManagementPort = %d，想要 0", got)
	}
}

// .yaml 与 .yml 都要认，且 .yml 里没有时要继续找 .yaml。
func TestReadSpringPortFallsBackToYamlExtension(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/main/resources/application.yaml", "server:\n  port: 20777\n")
	if got := ReadSpringPort(dir); got != 20777 {
		t.Fatalf("ReadSpringPort = %d，想要 20777", got)
	}
}

func TestReadNodePort(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"VITE_PORT", "VITE_PORT=3106\n", 3106},
		{"带引号", "VITE_PORT=\"3106\"\n", 3106},
		{"带空格", "VITE_PORT = 3106\n", 3106},
		{"PORT 兜底", "PORT=3006\n", 3006},
		{"注释不算", "# VITE_PORT=3106\n", 0},
		{"认不出的键不误伤", "VITE_PORTX=1\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, ".env", c.body)
			if got := ReadNodePort(dir); got != c.want {
				t.Fatalf("ReadNodePort = %d，想要 %d", got, c.want)
			}
		})
	}
}

func TestReadNodePortPrefersDotEnvOverDevelopment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "VITE_PORT=3106\n")
	writeFile(t, dir, ".env.development", "VITE_PORT=9999\n")
	if got := ReadNodePort(dir); got != 3106 {
		t.Fatalf("ReadNodePort = %d，想要 3106（.env 优先）", got)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Demo-Admin", "demo-admin"},
		{"server_admin", "server_admin"},
		{"a b", "a-b"},
		{"/leading/", "leading"},
		// 中文整个变成连字符，Trim 完是空串——调用方要自己兜底。
		{"模拟服务", ""},
	}
	for _, c := range cases {
		if got := SanitizeName(c.in); got != c.want {
			t.Fatalf("SanitizeName(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

// DeriveFacts 是「读端口」和「拼健康检查地址」两件事的合体，
// 各类型的取值位置与探针路径都不一样，逐个钉住。
func TestDeriveFacts(t *testing.T) {
	t.Run("Go 的探针要带 server.path 前缀", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "config.yaml", "server:\n  path: /admin\n  port: 20351\n")
		f := DeriveFacts(dir, KindGo)
		if f.Port != 20351 {
			t.Fatalf("Port = %d，想要 20351", f.Port)
		}
		if want := "http://localhost:20351/admin/health"; f.Health != want {
			t.Fatalf("Health = %q，想要 %q", f.Health, want)
		}
		if f.PortFrom == "" {
			t.Fatal("PortFrom 不该为空")
		}
	})

	t.Run("Java 的探针要打管理端口", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "src/main/resources/application.yml",
			"server:\n  port: 20241\nmanagement:\n  server:\n    port: 20299\n")
		f := DeriveFacts(dir, KindJava)
		if f.Port != 20241 {
			t.Fatalf("Port = %d，想要 20241", f.Port)
		}
		if want := "http://localhost:20299/actuator/health"; f.Health != want {
			t.Fatalf("Health = %q，想要 %q", f.Health, want)
		}
	})

	t.Run("Java 没配管理端口时退回业务端口", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "src/main/resources/application.yml", "server:\n  port: 20241\n")
		f := DeriveFacts(dir, KindJava)
		if want := "http://localhost:20241/actuator/health"; f.Health != want {
			t.Fatalf("Health = %q，想要 %q", f.Health, want)
		}
	})

	t.Run("Node", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, ".env", "VITE_PORT=3106\n")
		f := DeriveFacts(dir, KindNode)
		if f.Port != 3106 {
			t.Fatalf("Port = %d，想要 3106", f.Port)
		}
		if want := "http://localhost:3106/"; f.Health != want {
			t.Fatalf("Health = %q，想要 %q", f.Health, want)
		}
	})

	// 读不到端口就什么都不给：宁可让人自己填，也不要猜一个看似合理的值。
	t.Run("读不到就全空", func(t *testing.T) {
		dir := t.TempDir()
		if f := DeriveFacts(dir, KindGo); f.Port != 0 || f.Health != "" || f.PortFrom != "" {
			t.Fatalf("想要全空，得到 %+v", f)
		}
		if f := DeriveFacts(dir, KindPython); f.Port != 0 {
			t.Fatalf("Python 没有约定的端口声明位置，不该给值，得到 %+v", f)
		}
	})
}
