// Package config 定义并加载 Pier 的服务清单。
//
// 清单描述「哪些目录、算什么类型、怎么起、占哪个端口」。它平时存在 Pier 自己的
// 数据文件里（见 store.go），由界面编辑；YAML 清单只在命令行用 --config 显式指定时出现，只读。
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 服务类型。不论哪种类型，启动最终都归结为「可选编译 + 运行」两步，
// Kind 只决定这两步如何推断，以及需要注入哪套工具链环境。
const (
	KindGo     = "go"
	KindJava   = "java"
	KindNode   = "node"
	KindPython = "python"
	KindShell  = "shell"
)

// DefaultConfigName 是向上查找配置时使用的文件名。
const DefaultConfigName = "pier.yaml"

// DefaultScript 是 Node 服务未指定 script 时使用的 package.json 脚本名。
const DefaultScript = "dev"

// 重启策略。留空表示不自动重启，这也是默认：多数开发服务器退出就是用户想让它退出。
const (
	// RestartOnFailure 表示进程没经过「停止」就消失了，就把它再拉起来。
	//
	// 只有这一种取值。服务是 setsid 出去的独立进程，Pier 不 Wait 它（见 supervisor.go），
	// 因而拿不到退出码——「失败」与「正常退出」在这里分不开，想区分也区分不了。
	// 所以这里定义的不是「失败才重启」，而是「不是我叫它停的，就再起一次」。
	RestartOnFailure = "on-failure"
)

// UngroupedName 是没写 group 的服务在界面上的分组名。
// 分组完全由清单里的 group 字段决定，不按目录、不按类型猜——
// 猜出来的分组看着聪明，但一旦猜错，用户没有任何地方能把它改回来。
const UngroupedName = "未分组"

// Service 是一个可启停的服务定义。
type Service struct {
	// Name 是服务的唯一标识，也是 up/down/logs 等命令使用的名字。
	Name string `yaml:"name" json:"name"`
	// Dir 是服务工作目录，相对配置文件所在目录；必须是绝对路径或相对路径，不支持 ~。
	Dir string `yaml:"dir" json:"dir"`
	// Group 是面板里的分组名；留空归入「未分组」。
	Group string `yaml:"group" json:"group,omitempty"`
	// Note 是给人看的自由备注，面板上原样显示，Pier 不解释它的内容。
	Note string `yaml:"note" json:"note,omitempty"`
	// Kind 取 KindGo/KindJava/KindNode/KindPython/KindShell；留空则按目录内容自动识别。
	Kind string `yaml:"kind" json:"kind,omitempty"`
	// Run 显式指定启动命令；留空则按 Kind 推断。
	Run string `yaml:"run" json:"run,omitempty"`
	// Build 显式指定编译命令，启动前先跑一遍（装依赖这类前置步骤也写在这里）；
	// 留空则按 Kind 推断，Node/Python 默认没有这一步。
	Build string `yaml:"build" json:"build,omitempty"`
	// Module 是 Maven 子模块名（如 shop-admin），Java 服务用它定位要跑哪个模块。
	Module string `yaml:"module" json:"module,omitempty"`
	// Script 是 package.json 里的脚本名，Node 服务用它决定跑哪个脚本。
	Script string `yaml:"script" json:"script,omitempty"`
	// Port 是该服务监听的端口，用于状态展示与占用检测；可选。
	Port int `yaml:"port" json:"port,omitempty"`
	// Health 是就绪探针 URL，如 http://localhost:20351/admin/health；可选。
	Health string `yaml:"health" json:"health,omitempty"`
	// Env 是追加到进程环境的变量，优先级高于继承来的环境。
	Env map[string]string `yaml:"env" json:"env,omitempty"`
	// Toolchain 是该服务专属的工具链覆盖，优先于顶层 toolchain。
	// 不同项目常需要不同 JDK（例如某个工程要求 21、另一个要求 8），
	// 因此版本必须能按服务指定，不能只留一个全局值。
	Toolchain map[string]string `yaml:"toolchain" json:"toolchain,omitempty"`

	// DependsOn 是启动顺序上的前置服务名：它们先起来，本服务才轮到。
	//
	// 只影响顺序，不改变「能不能起」：前置起失败了本服务照样会起。
	// 这里不做编排——一个本地启停工具替用户判断「依赖没好就别起了」，
	// 在真实项目里只会让人更费解（前置的健康检查没过、端口还没通，
	// 而服务本身其实完全起得来）。顺序是确定的收益，判定不是。
	DependsOn []string `yaml:"depends_on" json:"depends_on,omitempty"`
	// Restart 是进程意外退出后的重启策略，取值见 RestartOnFailure；留空不重启。
	Restart string `yaml:"restart" json:"restart,omitempty"`

	// Origin 记录这条定义来自 pier.yaml 还是覆盖文件，由加载时填充。
	// 界面据此决定「删除」是能真删，还是只能隐藏。
	Origin string `yaml:"-" json:"-"`

	// rootDir 是配置文件所在目录，加载时填充，用于把 Dir 解析成绝对路径。
	rootDir string `yaml:"-" json:"-"`
}

// Config 是整份服务清单。
type Config struct {
	// Path 是配置文件的绝对路径。
	Path string `yaml:"-"`
	// Toolchain 按工具类别指定绝对路径或 sdkman 候选版本名，如 java: "17.0.12-oracle"。
	Toolchain map[string]string `yaml:"toolchain"`
	// Env 是所有服务都会拿到的共享变量，服务自己的 env 覆盖它。
	//
	// 一组服务共用同一个数据库地址、同一套注册中心配置是常态，写在每个服务下面
	// 意味着改一处要改十几处，而漏掉的那个服务会用着旧地址连上去。
	Env map[string]string `yaml:"env" json:"env,omitempty"`
	// Services 是服务定义列表，顺序即面板与 status 的展示顺序。
	// 这是 pier.yaml 与覆盖文件合并之后的结果。
	Services []*Service `yaml:"services"`
	// Hidden 是被覆盖文件隐藏掉的名字。源文件里的定义还在，只是不参与展示与启停。
	Hidden []string `yaml:"-"`
	// DeclaredGroups 是覆盖文件里显式声明过的分组名，按声明顺序排列。
	// 它让空分组也能存在；谁属于哪个分组仍然只看服务自己的 group 字段。
	DeclaredGroups []string `yaml:"-"`
	// OverlayPath 是本次加载用到的覆盖文件路径，供界面展示。
	OverlayPath string `yaml:"-"`
	// baseNames 是 pier.yaml 原始定义里的服务名。
	// 「删除」要靠它区分两种语义：源文件里有这个名字就只能隐藏，
	// 否则把覆盖里那条一删，源文件里的定义就又冒出来了——界面说着「已删除」，
	// 列表里却还在，这比不能删更让人费解。
	baseNames map[string]bool `yaml:"-"`

	// store 表示来自 Pier 的数据文件（见 store.go），可以修改并写回。
	store bool
	// root 是相对目录的基准。YAML 清单用清单所在目录；数据文件只存绝对路径，这里是主目录兜底。
	root string
	// 数据文件的日志、编译产物、进程状态放在系统目录，由 LoadStore 填好；
	// YAML 清单留空，沿用清单旁边的运行目录。
	logDir, binDir, statePath string
}

// Load 读取配置文件。path 为空时从当前目录向上查找 DefaultConfigName，
// 这样在项目的任意子目录里都能直接执行 Pier。
func Load(path string) (*Config, error) {
	if path == "" {
		found, err := findUpward()
		if err != nil {
			return nil, err
		}
		path = found
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析配置路径失败：%w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败：%w", err)
	}

	var c Config
	// KnownFields 让拼错的键名直接报错，而不是被静默忽略。
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", abs, err)
	}

	c.Path = abs
	c.baseNames = make(map[string]bool, len(c.Services))
	for _, s := range c.Services {
		if s == nil {
			continue
		}
		s.rootDir = filepath.Dir(abs)
		c.baseNames[s.Name] = true
	}
	if err := c.validate(); err != nil {
		return nil, err
	}

	// 基础清单先自检通过，再叠加覆盖文件——顺序反过来的话，一个坏掉的覆盖文件
	// 会让人以为手写的清单出了问题，排查方向整个是错的。
	overlayPath := overlayPathFor(abs)
	ov, err := LoadOverlay(overlayPath)
	if err != nil {
		return nil, err
	}
	base := c.Services
	c.merge(base, ov)
	c.OverlayPath = overlayPath
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s 与 %s 合并后不合法：%w", abs, overlayPath, err)
	}
	return &c, nil
}

// Dir 返回相对目录的基准。YAML 清单是清单所在目录；数据文件是它记着的工作空间根。
func (c *Config) Dir() string {
	if c.root != "" {
		return c.root
	}
	return filepath.Dir(c.Path)
}

// findUpward 从当前目录逐级向上查找配置文件。
func findUpward() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, DefaultConfigName)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 %s：请在配置文件所在目录或其子目录下运行，或用 --config 指定", DefaultConfigName)
		}
		dir = parent
	}
}

func (c *Config) validate() error {
	if len(c.Services) == 0 {
		return fmt.Errorf("%s 中没有任何服务定义", c.Path)
	}
	return c.validateServices()
}

// validateServices 逐条检查服务定义。和 validate 分开，是因为数据文件允许一个服务都没有。
func (c *Config) validateServices() error {
	seen := make(map[string]bool, len(c.Services))
	for i, s := range c.Services {
		if s == nil {
			return fmt.Errorf("第 %d 个服务定义为空", i+1)
		}
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("第 %d 个服务缺少 name", i+1)
		}
		if seen[s.Name] {
			return fmt.Errorf("服务名重复：%s", s.Name)
		}
		seen[s.Name] = true

		if strings.TrimSpace(s.Dir) == "" {
			return fmt.Errorf("服务 %s 缺少 dir", s.Name)
		}
		if strings.HasPrefix(s.Dir, "~") {
			return fmt.Errorf("服务 %s 的 dir 不支持 ~，请写相对配置文件目录的路径或绝对路径", s.Name)
		}
		if s.Kind != "" && !validKind(s.Kind) {
			return fmt.Errorf("服务 %s 的 kind 无效：%s（可用：%s）", s.Name, s.Kind,
				strings.Join([]string{KindGo, KindJava, KindNode, KindPython, KindShell}, "、"))
		}
		if s.Kind == KindJava && s.Module == "" && s.Run == "" {
			return fmt.Errorf("服务 %s 是 Java 服务，必须给出 module（Maven 子模块名）或直接写 run", s.Name)
		}
		if s.Restart != "" && s.Restart != RestartOnFailure {
			return fmt.Errorf("服务 %s 的 restart 无效：%s（可用：%s，或留空表示不自动重启）", s.Name, s.Restart, RestartOnFailure)
		}
		if msg := healthProblem(s.Health); msg != "" {
			return fmt.Errorf("服务 %s 的 health %s", s.Name, msg)
		}
		for _, d := range s.DependsOn {
			if strings.TrimSpace(d) == "" {
				return fmt.Errorf("服务 %s 的 depends_on 里有空名字", s.Name)
			}
		}
	}

	// 依赖的校验排在上面那个循环之外：一条依赖指向的名字可能排在它后面，
	// 边读边查会把「顺序不同」误报成「名字不存在」。
	for _, s := range c.Services {
		for _, d := range s.DependsOn {
			if d == s.Name {
				return fmt.Errorf("服务 %s 依赖了自己", s.Name)
			}
			if !seen[d] {
				return fmt.Errorf("服务 %s 依赖的 %s 不在清单里", s.Name, d)
			}
		}
	}
	if cycle := c.dependencyCycle(); len(cycle) > 0 {
		return fmt.Errorf("服务依赖成环：%s", strings.Join(cycle, " → "))
	}
	return nil
}

// healthProblem 检查健康探针地址能不能真的用，返回一句「哪里不对」，没问题时空串。
// 留空是「这类服务没有健康接口」，正当，不算问题。
//
// 少了 scheme 的写法（localhost:8080/health）最坑：url.Parse 收得下——它把 localhost
// 当成 scheme——于是探针每次都发不出去，服务一路挂到探针过期才被人看见，而那已经是
// 三分钟之后的事，人不会把这两件事连起来。挡在存下来的那一刻：那时用户还知道
// 自己想填什么。
func healthProblem(raw string) string {
	if raw == "" {
		return ""
	}
	// 地址里不该有空格：存下去之后是 client.Get 直接用的，它只会回一句
	// 「invalid character " " in host name」，而那时人已经忘了自己填过什么。
	if strings.ContainsAny(raw, " \t\n") {
		return "里不能有空格（空格要写成 %20）"
	}
	// 漏了 scheme 的两种写法是同一件事，报出来的样子却不一样：localhost:8080/health
	// 会被 url.Parse 收下（localhost 成了 scheme），127.0.0.1:8080/health 则直接报错
	// （冒号落在第一段路径里）——后者的原话是「first path segment in URL cannot contain
	// colon」，对着一个在填表单的人等于没说。所以先补上 http:// 试一次，成立就按这件事说。
	//
	// 只对纯 ASCII 这么判：url.Parse 对中文域名照收不误，不拦一道就会建议人家
	// 去访问 http://我的服务 ，那种「建议」比不说还乱。
	if !strings.Contains(raw, "://") && strings.IndexFunc(raw, func(r rune) bool { return r > 127 }) < 0 {
		if u, err := url.Parse("http://" + raw); err == nil && u.Host != "" {
			return "要写成完整的地址，补上 http:// —— http://" + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("不是一个能用的地址：%v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Sprintf("只认 http 与 https，这里是 %s://", u.Scheme)
	}
	if u.Host == "" {
		return "里没有主机名，比如 http://localhost:8080/health"
	}
	return ""
}

// dependencyCycle 找出一条依赖环并原样返回，如 [a b a]；没有环时返回 nil。
//
// 成环必须在这里拦下，不能留给启动顺序去兜：拓扑排序遇到环时要么死循环，
// 要么随手丢掉几条依赖，而「丢掉的恰好是你最需要的那条」是查不出来的。
// 把环本身写进报错里，用户看一眼就知道该删哪条。
func (c *Config) dependencyCycle() []string {
	deps := make(map[string][]string, len(c.Services))
	for _, s := range c.Services {
		deps[s.Name] = s.DependsOn
	}
	const (
		white = 0 // 还没走到
		gray  = 1 // 正在这条路径上
		black = 2 // 子树已走完
	)
	state := make(map[string]int, len(c.Services))
	var path []string

	var walk func(string) []string
	walk = func(name string) []string {
		state[name] = gray
		path = append(path, name)
		for _, d := range deps[name] {
			switch state[d] {
			case gray:
				// 截出环的那一段：从 d 第一次进路径的位置到这里。
				for i, p := range path {
					if p == d {
						return append(append([]string{}, path[i:]...), d)
					}
				}
				return append(append([]string{}, path...), d)
			case white:
				if found := walk(d); found != nil {
					return found
				}
			}
		}
		path = path[:len(path)-1]
		state[name] = black
		return nil
	}

	for _, s := range c.Services {
		if state[s.Name] == white {
			if found := walk(s.Name); found != nil {
				return found
			}
		}
	}
	return nil
}

func validKind(k string) bool {
	switch k {
	case KindGo, KindJava, KindNode, KindPython, KindShell:
		return true
	}
	return false
}

// StartOrder 返回按依赖排好的启动顺序：每个服务都排在它依赖的那些之后。
//
// 同一层里保持清单里的原顺序，而不是按名字排序——清单顺序是列在人眼前的顺序，
// 界面上「全部启动」按的就是它，排完依赖之后它不该再变一次。
//
// 成环时不会卡住（validate 已经拦在前面，这里只是不让它死循环）：剩下的按原顺序补在后面。
func (c *Config) StartOrder() []*Service {
	done := make(map[string]bool, len(c.Services))
	out := make([]*Service, 0, len(c.Services))
	for len(out) < len(c.Services) {
		progress := false
		for _, s := range c.Services {
			if done[s.Name] {
				continue
			}
			ready := true
			for _, d := range s.DependsOn {
				if !done[d] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			done[s.Name] = true
			out = append(out, s)
			progress = true
		}
		if !progress {
			for _, s := range c.Services {
				if !done[s.Name] {
					done[s.Name] = true
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// StopOrder 是启动顺序倒过来。
//
// 反着停是因为反过来才对：先起的那一批往往是后面那些要连的东西
// （数据库、注册中心、网关），先停它们等于让还在跑的服务对着一个已经关掉的端口。
// 不反的话，成批停止时日志里会多出一串没有意义的连接错误。
func (c *Config) StopOrder() []*Service {
	order := c.StartOrder()
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// Find 按名字取服务。
func (c *Config) Find(name string) (*Service, error) {
	for _, s := range c.Services {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("没有名为 %s 的服务（可用：%s）", name, strings.Join(c.Names(), "、"))
}

// Names 返回所有服务名，顺序与配置一致。
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Services))
	for _, s := range c.Services {
		out = append(out, s.Name)
	}
	return out
}

// AbsDir 返回服务的绝对工作目录。
func (s *Service) AbsDir() string {
	if filepath.IsAbs(s.Dir) {
		return filepath.Clean(s.Dir)
	}
	return filepath.Clean(filepath.Join(s.rootDir, s.Dir))
}

// GroupName 返回用于展示的分组名，没写 group 的归入「未分组」。
func (s *Service) GroupName() string {
	if g := strings.TrimSpace(s.Group); g != "" {
		return g
	}
	return UngroupedName
}

// IsOverlay 表示这条定义来自可写的覆盖文件，界面能真正删除它。
func (s *Service) IsOverlay() bool { return s.Origin == OriginOverlay }

// Groups 按服务出现顺序返回分组名，重复的只留一次。
// 顺序即清单顺序，这样侧栏的排列和开发者写清单时的思路一致，而不是按字典序打乱。
func (c *Config) Groups() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range c.Services {
		g := s.GroupName()
		if seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}

// InBase 表示某个服务名在只读的 pier.yaml 里有定义。
// 界面据此决定「删除」是能真删（只存在于覆盖里），还是只能隐藏。
func (c *Config) InBase(name string) bool { return c.baseNames[name] }

// AllGroups 返回面板上应该出现的全部小组，按展示顺序：
// 先排覆盖文件里显式声明过的（建过的分组，空着也留着），
// 再接上只在服务里有、没声明过的分组，按它们在清单里第一次出现的顺序。
// 顺序稳定且可预期，比按字典序排要贴近开发者写清单时的思路。
func (c *Config) AllGroups() []string {
	var out []string
	seen := map[string]bool{}
	add := func(g string) {
		if g == "" || seen[g] {
			return
		}
		seen[g] = true
		out = append(out, g)
	}
	for _, g := range c.DeclaredGroups {
		add(strings.TrimSpace(g))
	}
	for _, s := range c.Services {
		add(s.GroupName())
	}
	return out
}

// CountInGroup 返回某个分组下的服务数。
func (c *Config) CountInGroup(group string) int {
	n := 0
	for _, s := range c.Services {
		if s.GroupName() == group {
			n++
		}
	}
	return n
}

// RelTo 把绝对路径写成相对清单目录的形式，写进覆盖文件时才不会把
// 本机的绝对路径固化下来；不在清单目录之下时原样返回绝对路径。
func (c *Config) RelTo(abs string) string {
	if c.store {
		return abs // 数据文件只存绝对路径
	}
	root := c.Dir()
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs
	}
	return rel
}

// UsedPorts 返回清单里已经写掉的全部端口，按升序排列。
// 新增服务时靠它避开那些「此刻没在监听、但一启动就会撞车」的端口。
func (c *Config) UsedPorts() []int {
	var out []int
	for _, s := range c.Services {
		if s.Port > 0 {
			out = append(out, s.Port)
		}
	}
	sort.Ints(out)
	return out
}
