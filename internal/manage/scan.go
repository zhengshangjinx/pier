package manage

// 本文件是「把一个目录扫一遍，列出里面有什么项目」。
//
// 原先这套扫描住在 internal/cli/detect.go 里，只有 `pier detect` 用得上。界面上
// 「还没有任何服务」那一屏要的正是同一件事——列出认出来的项目、各自凭什么认出来的、
// 勾选后一次加进来——所以整段搬到这里，两个宿主共用：命令行把它变成 YAML，
// 界面把它变成 JSON 与一串复选框，而「什么算一个可启动的项目」只写了一遍。
//
// 识别一律是尽力而为，而且**每一条都必须能说出凭什么**：类型是看见哪个文件才认的、
// 名字是从哪儿转出来的、端口是从哪个配置里读到的。少了这几句，用户面对一份猜出来的
// 名单只敢逐条打开核对，那一屏就白做了。
//
// 端口只读项目自己写下的那个，读不到就空着。**不替它挑一个空闲端口**：挑出来的
// 会跟着清单一直走下去，被注入成 PORT、被拿去拼健康检查地址，而它凭空的出处
// 过两天没人记得；空着只是少一栏，编一个是一条看起来对、实际查不通的配置。

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/view"
)

// maxScanDepth 是扫描的最大目录深度，防止误扫到整个磁盘。
const maxScanDepth = 6

// scanSkipDirs 是扫描时直接跳过的目录：依赖与构建产物里不会有服务，
// 而它们往往是整棵树里最大的那几块。
var scanSkipDirs = map[string]bool{
	"node_modules": true, ".git": true, "target": true, "build": true,
	"dist": true, ".idea": true, ".pier": true, "out": true,
	"vendor": true, "__pycache__": true, ".venv": true, "历史代码": true,
}

// nodeScriptCandidates 是 Node 服务可能的启动脚本，按优先级排列。
var nodeScriptCandidates = []string{"dev", "start", "serve"}

// ScanOut 是「把这个目录扫一遍」的结果。
type ScanOut struct {
	OK      bool          `json:"ok"`
	Msg     string        `json:"msg"`
	AbsPath string        `json:"absPath"`
	Items   []ScanItemOut `json:"items"`
}

// ScanItemOut 是扫到的一个项目。
type ScanItemOut struct {
	AbsPath string `json:"absPath"`
	// DirShort 是 AbsPath 的展示形式（主目录缩成 ~），与命令行、端口那一屏
	// 共用 view.ShortPath：同一件事只换算一份，两边才不会有不同写法。
	DirShort string `json:"dirShort"`
	Name     string `json:"name"`
	// Kind 是认出来的类型（go / java / node / python）。
	Kind   string `json:"kind"`
	Module string `json:"module"`
	Script string `json:"script"`
	// Plan 是推导出的启动命令，让人在勾选之前就看得见加进来会执行什么。
	// 推不出来时为空（比如 Java 多模块缺子模块），界面据此说明。
	Plan string `json:"plan"`
	Port int    `json:"port"`
	// PortFrom 说明 Port 是从哪个文件的哪个键读来的；Port 为 0 时为空。
	PortFrom string `json:"portFrom"`
	Health   string `json:"health"`
	// Evidence 是「凭什么算一个项目」的几句话，例如「有 go.mod」。
	Evidence []string `json:"evidence"`
	// Skip 非空表示这一条不能加，值是原因（重名、清单里已经有）。
	// 界面据此把它勾掉并禁用，同时把那句话摆出来——不说为什么，
	// 用户只会以为扫描漏了它。
	Skip string `json:"skip"`
}

// ScanDir 把一个目录扫一遍，列出里面认出来的项目。
//
// 清单还没加载出来时也能扫（cfg 为 nil）：那种情况下少两样东西——启动命令推不出来、
// 也核不出「清单里是不是已经有它」——但「这儿有个什么项目」照样看得见。
// `pier detect` 正是会在一份读不出来的清单旁边跑。
func (m *Manager) ScanDir(root string) (*ScanOut, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("请选择一个目录")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("不是有效目录：%s", abs)
	}

	cfg := m.Config()
	found, err := scanProjects(abs)
	if err != nil {
		return nil, err
	}

	out := &ScanOut{OK: true, AbsPath: abs}
	if len(found) == 0 {
		out.Msg = fmt.Sprintf("在 %s 下没认出可启动的项目。只往下看了 %d 层，"+
			"node_modules、target 这类目录直接跳过。", abs, maxScanDepth)
		return out, nil
	}

	// 重名要在这里查出来，不能留给「全部添加」去撞：写清单是按名字覆盖的，
	// 两条都叫 admin 的话，后一条会把前一条悄悄顶掉——用户看到的是一次成功的添加。
	taken := map[string]bool{}
	for _, f := range found {
		item := m.scanItem(cfg, f)
		if item.Skip == "" {
			if taken[item.Name] {
				item.Skip = "这一批里有两条都叫「" + item.Name + "」，改个名字再加"
			} else {
				taken[item.Name] = true
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// scanFound 是扫描阶段认出来的一个项目，还没变成对外的形状。
type scanFound struct {
	Dir string
	// FactsDir 是读端口该去哪个目录：Maven 多模块工程的端口写在子模块自己的
	// 配置里，而服务要在反应堆根目录上跑（见 scanMaven）。
	FactsDir string
	Kind     string
	Module   string
	Script   string
	Evidence []string
}

// scanItem 把扫到的一项变成对外的形状：补上名字、端口、启动命令，以及「能不能加」。
func (m *Manager) scanItem(cfg *config.Config, f scanFound) ScanItemOut {
	item := ScanItemOut{AbsPath: f.Dir, DirShort: view.ShortPath(f.Dir),
		Kind: f.Kind, Module: f.Module, Script: f.Script, Evidence: f.Evidence}

	name := f.Module
	if name == "" {
		name = filepath.Base(f.Dir)
		item.Evidence = append(item.Evidence, "名字取自目录名 "+name)
	} else {
		item.Evidence = append(item.Evidence, "名字取自 Maven 子模块 "+name)
	}
	item.Name = config.SanitizeName(name)
	if item.Name == "" {
		// 纯中文之类的目录名转不出可用的服务名（SanitizeName 只留字母数字与连字符）。
		// 名字是必填项，给不出建议就直说，别让「全部添加」在这一条上失败。
		item.Skip = "目录名转不出可用的服务名，用「添加应用」自己起一个"
	}

	if cfg != nil {
		sameName := ""
		for _, s := range cfg.Services {
			if s.AbsDir() == f.Dir {
				item.Skip = "清单里已经有「" + s.Name + "」指着这个目录"
				break
			}
			if s.Name == item.Name {
				sameName = s.Name
			}
		}
		if item.Skip == "" && sameName != "" {
			item.Skip = "清单里已经有一条叫「" + sameName + "」的服务，改个名字再加"
		}
	}

	// 端口只读项目自己写的那个，读不到就空着（见文件头）。
	if f.FactsDir != "" {
		facts := config.DeriveFacts(f.FactsDir, f.Kind)
		item.Port, item.PortFrom, item.Health = facts.Port, facts.PortFrom, facts.Health
		// 端口这一条也写进「依据」。它是从项目自己的配置里读出来的，而摆在那里的
		// 一串数字与「随手挑了一个」看不出区别——读得对不对、能不能信，
		// 全看这一句写没写清楚是哪个文件的哪个键。
		if facts.PortFrom != "" {
			item.Evidence = append(item.Evidence, "端口读自 "+facts.PortFrom)
		}
	}

	// 启动命令推得出来才给。推不出来不影响把它列出来——用户看得见「这条我还没
	// 认出该怎么跑」，比一条不做声的空白有用。
	if cfg != nil && item.Name != "" {
		probe := &config.Service{Name: item.Name, Dir: f.Dir, Kind: f.Kind,
			Module: f.Module, Script: f.Script}
		if plan, err := probe.Plan(cfg); err == nil {
			item.Plan = plan.String()
		}
	}
	return item
}

// scanProjects 遍历目录，认出项目根。
func scanProjects(root string) ([]scanFound, error) {
	var out []scanFound
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
		case fileExists(filepath.Join(path, "go.mod")):
			// 只有含 main 包的才是可运行服务；纯库模块跳过。
			if hasMainPackage(path) {
				out = append(out, scanFound{Dir: path, FactsDir: path, Kind: config.KindGo,
					Evidence: []string{"有 go.mod", "里面有 main 包"}})
			}
		case fileExists(filepath.Join(path, "pom.xml")):
			out = append(out, scanMaven(path)...)
		case fileExists(filepath.Join(path, "package.json")):
			if f, ok := scanNode(path); ok {
				out = append(out, f)
			}
		case fileExists(filepath.Join(path, "pyproject.toml")), fileExists(filepath.Join(path, "requirements.txt")):
			if fileExists(filepath.Join(path, "main.py")) {
				out = append(out, scanFound{Dir: path, FactsDir: path, Kind: config.KindPython,
					Evidence: []string{"有 requirements.txt 或 pyproject.toml，还有 main.py"}})
			}
		}
		return nil
	})
	return out, err
}

// scanMaven 处理 Maven 工程。多模块工程只在根目录产出一条，
// 并把带 spring-boot 插件的子模块各自列为一条服务——它们才是可独立启动的。
func scanMaven(root string) []scanFound {
	modules := MavenModules(root)
	if len(modules) == 0 {
		// 单模块工程，自身就是服务
		if mavenIsRunnable(root) {
			return []scanFound{{Dir: root, FactsDir: root, Kind: config.KindJava,
				Evidence: []string{"有 pom.xml，且声明了 spring-boot-maven-plugin"}}}
		}
		return nil
	}

	out := make([]scanFound, 0, len(modules))
	for _, m := range modules {
		dir := filepath.Join(root, m)
		if !fileExists(filepath.Join(dir, "pom.xml")) || !mavenIsRunnable(dir) {
			continue
		}
		out = append(out, scanFound{
			Dir: root, // Maven 必须在反应堆根目录执行，不能进子模块目录
			// 端口去子模块里读：application.yml 在它自己的 src/main/resources 下。
			FactsDir: dir,
			Kind:     config.KindJava,
			Module:   m,
			Evidence: []string{"根 pom.xml 里声明了模块 " + m,
				m + " 里声明了 spring-boot-maven-plugin"},
		})
	}
	return out
}

// scanNode 识别 Node 服务，并挑出实际存在的启动脚本。
func scanNode(dir string) (scanFound, bool) {
	script := NodeScript(dir)
	if script == "" {
		// 没有约定俗成的启动脚本（例如只有 build），不作为可启动服务。
		return scanFound{}, false
	}
	return scanFound{Dir: dir, FactsDir: dir, Kind: config.KindNode, Script: script,
		Evidence: []string{"有 package.json，scripts 里有 " + script}}, true
}

// NodeScript 在 package.json 里挑一个约定俗成的启动脚本，没有就返回空串。
//
// 命令行导入 IDEA 运行配置时也要问同一个问题，所以这一份是导出的。
func NodeScript(dir string) string {
	scripts := nodeScripts(dir)
	for _, c := range nodeScriptCandidates {
		if scripts[c] {
			return c
		}
	}
	return ""
}

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

// MavenModules 读出 pom.xml 里 <modules> 声明的子模块名。
//
// 用正则而不是 XML 解析：pom 是手写的，注释、命名空间、老式的 DOCTYPE 都可能出现，
// 而这里要的只是「声明了哪几个模块」这一个事实，为一次解析失败赔上整棵树的识别不划算。
// 认错一个名字的代价也只是多一条候选，scanMaven 会去核对那个目录里到底有没有 pom.xml。
func MavenModules(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range mavenModuleRe.FindAllSubmatch(raw, -1) {
		if name := strings.TrimSpace(string(m[1])); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
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

func depthOf(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// ── 勾选之后一次全加 ───────────────────────────────────────────────────────

// ScanAddOut 是一次「把勾中的这些加进来」的结果。
//
// 一条失败不影响别的：一次扫出来的东西本来就各自独立，为一个端口撞车把它们全部
// 退回去，用户得一条条重来一遍。失败的那几条各自带着原因，界面照原样列出来。
type ScanAddOut struct {
	OK    bool     `json:"ok"`
	Msg   string   `json:"msg"`
	Added []string `json:"added"`
	// Failed 是没加成的，名字 + 原因。
	Failed []ScanAddFailed `json:"failed"`
}

// ScanAddFailed 是一条没加成的服务。
type ScanAddFailed struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// AddScanned 把扫出来、用户勾中的那几条一次写进清单。
//
// 走的是与 SaveService 同一份校验（fillService）：端口撞车、目录为空、名字不合法
// 的判据只有那一处写。整批只落盘一次，界面也只重新加载一次——一条一条提交的话，
// 五条就是五次写盘加五次重载，而中间那几次刷新出来的半份清单还会在界面上闪一下。
//
// 一条都加不成时不落盘，把每一条的原因原样交回去。
func (m *Manager) AddScanned(items []ServiceIn) (*ScanAddOut, error) {
	st, err := m.storeFor()
	if err != nil {
		return nil, err
	}
	out := &ScanAddOut{}
	for _, in := range items {
		svc := in.toService()
		if _, err := fillService(st, in, svc); err != nil {
			name := svc.Name
			if name == "" {
				name = strings.TrimSpace(in.Dir)
			}
			out.Failed = append(out.Failed, ScanAddFailed{Name: name, Reason: err.Error()})
			continue
		}
		// 上一条成功之后才写进手里这份清单：fillService 只读它，失败的那几条
		// 不会留下半截改动（见那里的注释）。写进去之后，下一条的端口查重就能
		// 看见它——同一批里两条都用 8080 是会被拦住的。
		st.Upsert(svc)
		st.AddGroup(svc.Group)
		out.Added = append(out.Added, svc.Name)
	}

	if len(out.Added) == 0 {
		out.Msg = "一条都没加成功。"
		return out, nil
	}
	if err := m.commit(st); err != nil {
		return nil, err
	}
	out.OK = true
	out.Msg = fmt.Sprintf("已添加 %d 个服务", len(out.Added))
	if n := len(out.Failed); n > 0 {
		out.Msg += fmt.Sprintf("，%d 个没加成", n)
	}
	return out, nil
}
