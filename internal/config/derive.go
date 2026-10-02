package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhengshangjinx/pier/internal/toolchain"
)

// Plan 描述一个服务「怎么起」：可选的编译步骤、运行命令，以及需要注入哪几套工具链环境。
type Plan struct {
	// Kind 是最终采用的类型（可能是自动识别出来的）。
	Kind string
	// Build 是可选的编译命令；nil 表示不需要单独编译。
	Build []string
	// Run 是运行命令，必定非空。
	Run []string
	// Tools 是启动该服务所需的工具链，用于注入 JAVA_HOME / PATH。
	Tools []toolchain.Kind
}

// Plan 解析服务的启动方案：显式配置的 run / build 优先，其余按 Kind 推断。
func (s *Service) Plan(c *Config) (*Plan, error) {
	kind := s.Kind
	if kind == "" {
		detected, err := DetectKind(s.AbsDir())
		if err != nil {
			return nil, fmt.Errorf("服务 %s 未指定 kind，自动识别也失败：%w", s.Name, err)
		}
		kind = detected
	}

	switch kind {
	case KindGo:
		return s.planGo(c), nil
	case KindJava:
		return s.planJava()
	case KindNode:
		return s.planNode(), nil
	case KindPython:
		return s.planPython()
	case KindShell:
		if s.Run == "" {
			return nil, fmt.Errorf("服务 %s 是 shell 类型，必须显式给出 run", s.Name)
		}
		return &Plan{Kind: kind, Run: shellCmd(s.Run)}, nil
	}
	return nil, fmt.Errorf("服务 %s 的 kind 无效：%s", s.Name, kind)
}

// planGo 编译到运行期产物目录再运行。
//
// 刻意不用 `go run`：go run 按「源文件名」给临时可执行文件命名（main.go 会变成进程名 main），
// 在 ps 和活动监视器里认不出是哪个服务；显式 -o 才能得到稳定、可辨识的进程名。
func (s *Service) planGo(c *Config) *Plan {
	bin := filepath.Join(c.BinDir(), s.Name)
	p := &Plan{Kind: KindGo, Tools: []toolchain.Kind{toolchain.Go}}
	if s.Build != "" {
		p.Build = shellCmd(s.Build)
	} else {
		p.Build = []string{"go", "build", "-o", bin, "."}
	}
	if s.Run != "" {
		p.Run = shellCmd(s.Run)
	} else {
		p.Run = []string{bin}
	}
	return p
}

// planJava 用 Maven 启动 Spring Boot 模块，编译与运行必须分成两步：
//
//   - 编译用 `-pl <模块> -am install`：-am 把兄弟模块一并构建，install 让它们进入本地仓库，
//     这样只跑目标模块时才能解析到依赖。
//   - 运行用 `-pl <模块> spring-boot:run`，刻意不带 -am：-am 会把 spring-boot:run
//     也施加到所有被选中的模块上，而兄弟模块没有可运行的主类，必然失败。
//
// 不走 package + java -jar：构件名是 ${deployment.name}-${profile.active}，由 Maven 属性拼出，
// 在这里复刻一遍等于把构建细节抄成第二份事实源。
func (s *Service) planJava() (*Plan, error) {
	if s.Run == "" && s.Module == "" {
		return nil, fmt.Errorf("服务 %s 是 Java 类型，必须给出 module（Maven 子模块名）或直接写 run", s.Name)
	}
	p := &Plan{Kind: KindJava, Tools: []toolchain.Kind{toolchain.Java, toolchain.Maven}}
	if s.Build != "" {
		p.Build = shellCmd(s.Build)
	} else if s.Run == "" {
		p.Build = append([]string{"mvn", "-DskipTests"}, append(mavenSkipPackaging, "-pl", s.Module, "-am", "install")...)
	}
	if s.Run != "" {
		p.Run = shellCmd(s.Run)
	} else {
		p.Run = []string{"mvn", "-pl", s.Module, "spring-boot:run"}
	}
	return p, nil
}

// mavenSkipPackaging 关掉常见的「打包时顺手构建镜像」插件。
//
// 本地起服务只需要把兄弟模块装进本地仓库，不需要镜像；但不少工程把 docker-maven-plugin、
// dockerfile-maven、jib、fabric8 绑在 package 阶段，install 会连带触发。它们要么要求本机
// 跑着 Docker、要么像 spotify 的插件那样在 Apple 芯片上加载不了 x86 的原生库，直接让编译失败——
// 而失败原因和「启动服务」毫无关系。这几个属性对没用这些插件的工程没有任何影响。
var mavenSkipPackaging = []string{"-DskipDocker=true", "-Ddocker.skip=true", "-Ddockerfile.skip=true", "-Djib.skip=true"}

// planNode 按仓库既有的锁文件选择包管理器，不擅自改用另一个。
func (s *Service) planNode() *Plan {
	script := s.Script
	if script == "" {
		script = DefaultScript
	}
	p := &Plan{Kind: KindNode, Tools: []toolchain.Kind{toolchain.Node, toolchain.Pnpm}}
	if s.Run != "" {
		p.Run = shellCmd(s.Run)
		return p
	}

	dir := s.AbsDir()
	pm := "pnpm"
	if !fileExists(filepath.Join(dir, "pnpm-lock.yaml")) && fileExists(filepath.Join(dir, "package-lock.json")) {
		pm = "npm"
		p.Tools = []toolchain.Kind{toolchain.Node}
	}
	p.Run = []string{pm, "run", script}
	return p
}

func (s *Service) planPython() (*Plan, error) {
	p := &Plan{Kind: KindPython, Tools: []toolchain.Kind{toolchain.Python}}
	if s.Run != "" {
		p.Run = shellCmd(s.Run)
		return p, nil
	}
	if fileExists(filepath.Join(s.AbsDir(), "main.py")) {
		p.Run = []string{"python3", "main.py"}
		return p, nil
	}
	return nil, fmt.Errorf("服务 %s 是 Python 类型但目录下没有 main.py，请显式指定 run", s.Name)
}

// shellCmd 把用户写的整条命令交给 shell 执行，以便使用管道、重定向等 shell 语法。
// 进程组会在停止时整组回收，多出的这层 shell 不会成为漏网的孤儿。
func shellCmd(s string) []string {
	return ShellArgv(s)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// String 便于在日志与 status 中展示将要执行的命令。
func (p *Plan) String() string {
	out := ""
	if len(p.Build) > 0 {
		out += "编译: " + joinArgv(p.Build) + "　"
	}
	out += "运行: " + joinArgv(p.Run)
	return out
}

func joinArgv(argv []string) string {
	return DisplayArgv(argv)
}
