package cli

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhengshangjinx/pier/internal/config"
)

// maxScanDepth 是扫描的最大目录深度，防止误扫到整个磁盘。
const maxScanDepth = 6

// scanSkipDirs 是扫描时直接跳过的目录：构建产物与依赖目录里不会有服务。
var scanSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "target": true, "build": true,
	"dist": true, ".idea": true, ".pier": true, "out": true,
	"vendor": true, "__pycache__": true, ".venv": true, "历史代码": true,
}

// nodeScriptCandidates 是 Node 服务可能的启动脚本，按优先级排列。
var nodeScriptCandidates = []string{"dev", "start", "serve"}

// cmdDetect 扫描目录，识别项目类型，产出可直接粘贴进 pier.yaml 的服务定义。
//
// 识别是尽力而为：端口与模块名会尽量从既有配置里读出来，读不到就留空并注明，
// 需要人工补全——这比猜一个看似合理的值更安全。
func cmdDetect(args []string) int {
	cfgPath, rest := extractConfig(args)
	root := "."
	if len(rest) > 0 {
		root = rest[0]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fail("%v", err)
	}
	if fi, err := os.Stat(absRoot); err != nil || !fi.IsDir() {
		return fail("不是有效目录：%s", absRoot)
	}

	// dir 尽量写成相对配置文件目录的形式，配置才能跟着仓库一起走。
	baseDir := absRoot
	if cfg, err := loadConfig(cfgPath); err == nil {
		baseDir = cfg.Dir()
	}

	found, err := scan(absRoot)
	if err != nil {
		return fail("%v", err)
	}
	if len(found) == 0 {
		fmt.Printf("在 %s 下没有识别到可启动的项目。\n", absRoot)
		return 0
	}

	services := make([]*config.Service, 0, len(found))
	for _, f := range found {
		services = append(services, f.toService(baseDir))
	}
	out, err := yaml.Marshal(services)
	if err != nil {
		return fail("生成配置片段失败：%v", err)
	}

	fmt.Printf("扫描 %s，识别到 %d 个项目。\n", absRoot, len(services))
	fmt.Println("确认端口与健康检查路径后，粘贴到 pier.yaml 的 services 下：")
	fmt.Println()
	fmt.Print(string(out))
	fmt.Println()
	fmt.Println("提示：port / health 为空的需要按项目实际配置补全；health 留空则不做就绪探测。")
	return 0
}

// found 是一个识别到的项目。
type found struct {
	Dir    string
	Kind   string
	Script string
	Module string
	Port   int
	Note   string
}

func (f found) toService(baseDir string) *config.Service {
	dir := f.Dir
	if rel, err := filepath.Rel(baseDir, f.Dir); err == nil && !strings.HasPrefix(rel, "..") {
		dir = rel
	}
	name := f.Module
	if name == "" {
		name = filepath.Base(f.Dir)
	}
	svc := &config.Service{
		Name:   config.SanitizeName(name),
		Dir:    dir,
		Kind:   f.Kind,
		Module: f.Module,
		Script: f.Script,
		Port:   f.Port,
	}
	if f.Port > 0 {
		svc.Health = fmt.Sprintf("http://localhost:%d/", f.Port)
	}
	return svc
}

// scan 遍历目录，识别项目根。
func scan(root string) ([]found, error) {
	var out []found
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个目录读不了不该中断整次扫描
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && scanSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if depthOf(root, path) > maxScanDepth {
			return filepath.SkipDir
		}

		switch {
		case fileExistsIn(path, "go.mod"):
			// 只有含 main 包的才是可运行服务；纯库模块跳过。
			if hasMainPackage(path) {
				out = append(out, found{Dir: path, Kind: config.KindGo, Port: config.ReadGoPort(path)})
			}
		case fileExistsIn(path, "pom.xml"):
			out = append(out, scanMaven(path)...)
		case fileExistsIn(path, "package.json"):
			if f, ok := scanNode(path); ok {
				out = append(out, f)
			}
		case fileExistsIn(path, "pyproject.toml"), fileExistsIn(path, "requirements.txt"):
			if fileExistsIn(path, "main.py") {
				out = append(out, found{Dir: path, Kind: config.KindPython})
			}
		}
		return nil
	})
	return out, err
}

// scanMaven 处理 Maven 工程。多模块工程只在根目录产出一条，
// 并把带 spring-boot 插件的子模块各自列为一条服务——它们才是可独立启动的。
func scanMaven(root string) []found {
	modules := mavenModules(root)
	if len(modules) == 0 {
		// 单模块工程，自身就是服务
		if mavenIsRunnable(root) {
			return []found{{Dir: root, Kind: config.KindJava, Port: config.ReadSpringPort(root)}}
		}
		return nil
	}

	out := make([]found, 0, len(modules))
	for _, m := range modules {
		dir := filepath.Join(root, m)
		if !fileExistsIn(dir, "pom.xml") || !mavenIsRunnable(dir) {
			continue
		}
		out = append(out, found{
			Dir:    root, // Maven 必须在反应堆根目录执行，不能进子模块目录
			Kind:   config.KindJava,
			Module: m,
			Port:   config.ReadSpringPort(dir),
		})
	}
	return out
}

// scanNode 识别 Node 服务，并挑出实际存在的启动脚本。
func scanNode(dir string) (found, bool) {
	scripts := nodeScripts(dir)
	for _, c := range nodeScriptCandidates {
		if scripts[c] {
			return found{Dir: dir, Kind: config.KindNode, Script: c, Port: config.ReadNodePort(dir)}, true
		}
	}
	// 没有约定俗成的启动脚本（例如只有 build），不作为可启动服务。
	return found{}, false
}

// ---- 以下是各类「尽力而为」的信息提取 ----

// 端口与健康检查地址的推导（ReadGoPort / ReadSpringPort / ReadNodePort 等）
// 已经搬到 internal/config 的 probe.go：界面上「添加应用」也要用同一套推导，
// 而那边够不着这个包里的非导出函数。

// nodeScripts 读出 package.json 里声明的脚本名集合。
func nodeScripts(dir string) map[string]bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil
	}
	out := make(map[string]bool, len(pkg.Scripts))
	for k := range pkg.Scripts {
		out[k] = true
	}
	return out
}

// mavenModules 读出根 pom 声明的子模块名。
func mavenModules(root string) []string {
	raw, err := os.ReadFile(filepath.Join(root, "pom.xml"))
	if err != nil {
		return nil
	}
	var pom struct {
		Modules []string `xml:"modules>module"`
	}
	if err := xml.Unmarshal(raw, &pom); err != nil {
		return nil
	}
	sort.Strings(pom.Modules)
	return pom.Modules
}

// mavenIsRunnable 判断模块是否声明了 spring-boot-maven-plugin，即能否独立启动。
func mavenIsRunnable(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "spring-boot-maven-plugin")
}

// hasMainPackage 粗略判断 Go 模块里是否有 main 包。
func hasMainPackage(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			// 只往下看两层，避免在大型仓库里做全量扫描
			if depthOf(dir, path) > 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(raw), "package main") {
			found = true
		}
		return nil
	})
	return found
}

func fileExistsIn(dir, name string) bool {
	fi, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !fi.IsDir()
}

func depthOf(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}
