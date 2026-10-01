package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 本文件按目录内容读出「跑起来之后监听哪个端口、健康检查打哪个地址」。
//
// 与 detect.go 的 DetectKind 是一件事的两半：那个回答「这是什么项目」，
// 这个回答「它占哪个端口」。两者都只看项目自己的文件，不看 Pier 的清单。
//
// 这些函数原先散在 internal/cli/detect.go 里，只有命令行的 detect / import 用得上。
// 界面上「添加应用」填完目录也要用同一套推导，而 manage 包够不着 cli 包里的
// 非导出函数，所以整体搬到这里——config 是 cli 和 manage 共同的下游，
// 放这里两边都够得着，也不会多出任何依赖边。

// Facts 是从项目自身的文件里读出来的、可以直接填进清单的事实。
//
// 每一项都带「出处」，因为这类推导是尽力而为的：端口可能是从 config.yaml 读到的，
// 也可能是压根没读到。让人看见这个数是从哪来的，才知道该不该信它。
type Facts struct {
	// Port 是项目自己声明的端口；0 表示没读到。
	Port int
	// PortFrom 说明 Port 是从哪个文件的哪个键读来的。
	PortFrom string
	// Health 是据此推出的健康检查地址；空表示推不出来。
	Health string
}

// DeriveFacts 按项目类型读出端口与健康检查地址。
//
// 各类型的取值位置不同，且都是各自生态里的惯例：
//   - Go：config.yaml 的 server.port，探针是 <server.path>/health
//   - Java：application.yml 的 server.port，探针打 management.server.port 的 /actuator/health
//   - Node：.env 的 VITE_PORT / PORT
func DeriveFacts(dir, kind string) Facts {
	switch kind {
	case KindGo:
		p := ReadGoPort(dir)
		if p <= 0 {
			return Facts{}
		}
		// 路由前缀是 server.path，例如 /admin；服务自己的健康接口挂在它下面。
		return Facts{
			Port:     p,
			PortFrom: "config.yaml 的 server.port",
			Health:   fmt.Sprintf("http://localhost:%d%s/health", p, ReadGoPath(dir)),
		}

	case KindJava:
		p := ReadSpringPort(dir)
		if p <= 0 {
			return Facts{}
		}
		// 探针必须打管理端口：Spring Boot 常把 Actuator 与业务端口分开，
		// 打业务端口会一直探不通。没单独配管理端口时两者是同一个。
		mgmt := ReadSpringManagementPort(dir)
		if mgmt <= 0 {
			mgmt = p
		}
		from := "application.yml 的 server.port"
		if mgmt != p {
			from += fmt.Sprintf("（健康检查打管理端口 %d）", mgmt)
		}
		return Facts{
			Port:     p,
			PortFrom: from,
			Health:   fmt.Sprintf("http://localhost:%d/actuator/health", mgmt),
		}

	case KindNode:
		p := ReadNodePort(dir)
		if p <= 0 {
			return Facts{}
		}
		return Facts{
			Port:     p,
			PortFrom: ".env 的 VITE_PORT",
			Health:   fmt.Sprintf("http://localhost:%d/", p),
		}
	}
	// Python / Shell 没有约定俗成的端口声明位置，不猜。
	return Facts{}
}

// ReadGoPort 从 config.yaml 的 server 段里取端口。
func ReadGoPort(dir string) int {
	return atoiTrim(nestedYAMLValue(filepath.Join(dir, "config.yaml"), []string{"server"}, "port"))
}

// ReadGoPath 取 config.yaml 里 server.path，即服务的路由前缀（如 "/admin"）。
// 健康探针要拼成 <前缀>/health，取不到就当作无前缀。
func ReadGoPath(dir string) string {
	return nestedYAMLValue(filepath.Join(dir, "config.yaml"), []string{"server"}, "path")
}

// ReadSpringPort 取 application.yml 里 server.port，即业务端口。
func ReadSpringPort(dir string) int {
	return readSpringPortIn(dir, []string{"server"}, "port")
}

// ReadSpringManagementPort 取 application.yml 里 management.server.port，
// 即 Actuator 所在的管理端口。Spring Boot 常把它与业务端口分开，
// 探针必须打管理端口，否则永远探不通。
func ReadSpringManagementPort(dir string) int {
	return readSpringPortIn(dir, []string{"management", "server"}, "port")
}

func readSpringPortIn(dir string, containers []string, leaf string) int {
	for _, name := range []string{"application.yml", "application.yaml"} {
		p := filepath.Join(dir, "src", "main", "resources", name)
		if v := nestedYAMLValue(p, containers, leaf); v != "" {
			return atoiTrim(v)
		}
	}
	return 0
}

// nestedYAMLValue 沿 containers 逐层深入，取出最内层的 leaf 键的值。
// 例如 containers 为 ["management","server"]、leaf 为 "port" 时，
// 读的是 management.server.port。
//
// 用缩进判断层级而不是完整解析 YAML：只需读一个标量，而且必须能容忍
// 带 @...@ 占位符的配置——那种文件交给严格解析器会直接报错。
func nestedYAMLValue(path string, containers []string, leaf string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	// levels 记录每一层已匹配到的键所在缩进，用来判断当前行是否还在该层内。
	levels := make([]int, 0, len(containers))
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		// 缩进回退说明已经离开了之前匹配到的层。
		for len(levels) > 0 && indent <= levels[len(levels)-1] {
			levels = levels[:len(levels)-1]
		}

		key, rest, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(rest)

		if len(levels) == len(containers) {
			// 已进入目标块，只认 leaf 本身
			if key == leaf {
				return unquote(rest)
			}
			continue
		}
		// 逐层匹配容器路径上的下一个键；容器键的值应写在更深一层，同行有值说明只是个标量，不进入
		if key == containers[len(levels)] && rest == "" {
			levels = append(levels, indent)
		}
	}
	return ""
}

// unquote 去掉 YAML 里常见的包裹引号。
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// ReadNodePort 从 .env 里取前端端口；Vite 工程的端口通常写在这里。
func ReadNodePort(dir string) int {
	for _, name := range []string{".env", ".env.development", ".env.local"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "#") {
				continue
			}
			for _, key := range []string{"VITE_PORT", "PORT"} {
				if rest, ok := strings.CutPrefix(line, key); ok {
					rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "="))
					if p := atoiTrim(rest); p > 0 {
						f.Close()
						return p
					}
				}
			}
		}
		f.Close()
	}
	return 0
}

// SanitizeName 把目录名转成适合做服务名的形式。
//
// 只留下小写字母、数字、连字符和下划线，其余一律换成连字符。
// 注意它不折叠连续的连字符，也不保证结果非空——纯中文的目录名会整个变成空串，
// 调用方拿到空结果时要自己决定退回什么。
func SanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func atoiTrim(s string) int {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
