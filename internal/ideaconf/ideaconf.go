// Package ideaconf 解析 IDEA 的 .idea/workspace.xml，把里面已有的运行配置
// 还原成 Pier 的服务定义。
//
// 这样做的价值在于「与 IDEA 行为一致」：启动项的名称、工作目录、Maven 模块
// 都直接沿用开发者在 IDEA 里已经调好的那份，避免手工对照时抄错。
package ideaconf

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 运行配置类型。只处理能作为常驻服务启动的几种；
// 普通 Application（一次性工具类，如密钥生成器）不是服务，导入时会被跳过。
const (
	TypeGoApplication    = "GoApplicationRunConfiguration"
	TypeSpringBoot       = "SpringBootApplicationConfigurationType"
	TypeNpm              = "js.build_tools.npm"
	TypePlainApplication = "Application"
)

// projectDirVar 是 IDEA 在配置里引用项目根目录的占位符。
const projectDirVar = "$PROJECT_DIR$"

// RunConfig 是从 IDEA 运行配置里还原出的一条启动项。
type RunConfig struct {
	Name string
	Type string
	// Dir 是工作目录（已把 $PROJECT_DIR$ 展开为绝对路径）。
	Dir string
	// GoPackage 是 Go 运行配置里选中的包路径（kind=PACKAGE 时的 package 值）。
	GoPackage string
	// Module 是 Maven 模块名，Java 服务靠它定位要跑哪个模块。
	Module string
	// MainClass 是 Spring Boot 的主类。
	MainClass string
	// Script 是 npm 运行配置里选中的脚本名（如 dev）。
	Script string
	// Env 是运行配置里声明的环境变量。
	Env map[string]string
}

// IsService 判断该配置是否是常驻服务。一次性工具类不算。
func (r RunConfig) IsService() bool {
	switch r.Type {
	case TypeGoApplication, TypeSpringBoot, TypeNpm:
		return true
	}
	return false
}

// ideaXML 只映射解析所需的部分，其余节点一律忽略。
type ideaXML struct {
	Components []struct {
		Name           string `xml:"name,attr"`
		Configurations []struct {
			Name    string `xml:"name,attr"`
			Type    string `xml:"type,attr"`
			Module  string `xml:"module,attr"`
			Options []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"option"`
			WorkingDirectory struct {
				Value string `xml:"value,attr"`
			} `xml:"working_directory"`
			Package struct {
				Value string `xml:"value,attr"`
			} `xml:"package"`
			// npm 运行配置不用 working_directory，而是用 package-json 指向 package.json。
			PackageJSON struct {
				Value string `xml:"value,attr"`
			} `xml:"package-json"`
			Scripts struct {
				Script []struct {
					Value string `xml:"value,attr"`
				} `xml:"script"`
			} `xml:"scripts"`
			Envs struct {
				Env []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:"value,attr"`
				} `xml:"env"`
			} `xml:"envs"`
		} `xml:"configuration"`
	} `xml:"component"`
}

// runManagerComponent 是运行配置所在的组件名。
const runManagerComponent = "RunManager"

// Load 从 .idea/workspace.xml 读取运行配置。
// dir 可以是项目根目录（含 .idea）或 .idea 目录本身。
func Load(dir string) ([]RunConfig, error) {
	wsPath, err := locate(dir)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(wsPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败：%w", wsPath, err)
	}

	var doc ideaXML
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", wsPath, err)
	}

	projectDir := filepath.Dir(filepath.Dir(wsPath)) // <项目根>/.idea/workspace.xml
	out := make([]RunConfig, 0)
	for _, c := range doc.Components {
		if c.Name != runManagerComponent {
			continue
		}
		for _, cfg := range c.Configurations {
			rc := RunConfig{
				Name:   cfg.Name,
				Type:   cfg.Type,
				Module: cfg.Module,
			}
			rc.Dir = expand(cfg.WorkingDirectory.Value, projectDir)

			switch cfg.Type {
			case TypeGoApplication:
				rc.GoPackage = cfg.Package.Value
			case TypeNpm:
				// npm 配置把位置写在 package-json 里：通常直接指向 package.json，
				// 取它所在目录；若指向的是目录本身则直接用。
				if p := expand(cfg.PackageJSON.Value, projectDir); p != "" {
					if fi, err := os.Stat(p); err == nil && fi.IsDir() {
						rc.Dir = p
					} else {
						rc.Dir = filepath.Dir(p)
					}
				}
				if len(cfg.Scripts.Script) > 0 {
					rc.Script = cfg.Scripts.Script[0].Value
				}
			case TypeSpringBoot:
				// Java 运行配置不写工作目录。Maven 必须在反应堆根目录执行，
				// 所以这里固定用项目根，而不是子模块目录。
				if rc.Dir == "" {
					rc.Dir = projectDir
				}
			}
			if rc.Dir == "" && rc.Module != "" {
				rc.Dir = projectDir
			}
			for _, o := range cfg.Options {
				switch o.Name {
				case "SPRING_BOOT_MAIN_CLASS", "MAIN_CLASS_NAME":
					rc.MainClass = o.Value
				}
			}
			if len(cfg.Envs.Env) > 0 {
				rc.Env = make(map[string]string, len(cfg.Envs.Env))
				for _, e := range cfg.Envs.Env {
					rc.Env[e.Name] = e.Value
				}
			}
			out = append(out, rc)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 中没有找到任何运行配置", wsPath)
	}
	return out, nil
}

// locate 定位 workspace.xml：dir 可能是项目根，也可能是 .idea 目录本身。
func locate(dir string) (string, error) {
	cands := []string{
		filepath.Join(dir, ".idea", "workspace.xml"),
		filepath.Join(dir, "workspace.xml"),
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("在 %s 下没找到 .idea/workspace.xml", dir)
}

// expand 把 $PROJECT_DIR$ 换成项目根目录的绝对路径。
func expand(v, projectDir string) string {
	if v == "" {
		return ""
	}
	if strings.Contains(v, projectDirVar) {
		return filepath.Clean(strings.ReplaceAll(v, projectDirVar, projectDir))
	}
	return filepath.Clean(v)
}
