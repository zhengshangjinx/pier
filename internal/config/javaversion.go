package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// JavaMajor 读出 Java 服务要求的 Java 主版本（如 "21"），读不到返回空。
//
// 为什么要读：sdkman 的 current 是开发者全局默认的那一个 JDK，而不同项目要求的版本各不相同。
// 一个要求 21 的工程用 current=17 去编译，报的是「无效的目标发行版: 21」，
// 看上去像项目坏了，其实只是 JDK 挑错了。IDEA 能跑是因为它按 pom 挑 JDK，这里做同一件事。
//
// 先看子模块自己的 pom，再看反应堆根 pom（版本常写在根 pom 的 properties 里）。
// 依次认 maven.compiler.release、java.version、maven.compiler.target、maven.compiler.source，
// 值是 ${...} 占位符时顺着同一份 pom 的 properties 解一层，还解不开就当没读到，不猜。
func (s *Service) JavaMajor() string {
	root := s.AbsDir()
	var poms []string
	if m := strings.TrimSpace(s.Module); m != "" {
		poms = append(poms, filepath.Join(root, m, "pom.xml"))
	}
	poms = append(poms, filepath.Join(root, "pom.xml"))
	for _, p := range poms {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, key := range []string{"maven.compiler.release", "java.version", "maven.compiler.target", "maven.compiler.source"} {
			if v := javaMajorOf(resolvePomValue(string(raw), key)); v != "" {
				return v
			}
		}
	}
	return ""
}

// resolvePomValue 取 <key>值</key>，值是 ${other} 时再取一次 <other>。
func resolvePomValue(pom, key string) string {
	v := pomTag(pom, key)
	if m := pomPlaceholder.FindStringSubmatch(v); m != nil {
		v = pomTag(pom, m[1])
	}
	if strings.ContainsAny(v, "${}") {
		return ""
	}
	return v
}

var pomPlaceholder = regexp.MustCompile(`^\$\{([^}]+)\}$`)

func pomTag(pom, tag string) string {
	open, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(pom, open)
	if i < 0 {
		return ""
	}
	rest := pom[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// javaMajorOf 把 "21"、"17.0.2"、"1.8" 统一成主版本号 "21"、"17"、"8"。
func javaMajorOf(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 && parts[0] == "1" {
		return parts[1]
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return parts[0]
}
