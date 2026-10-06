package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Pier 自己管理的数据，全部收在一个目录里（默认 ~/.pier，和 ~/.docker、~/.m2 同一个惯例）：
//
//	~/.pier/
//	├── services.json   服务与分组，界面是唯一的编辑入口
//	├── settings.json   界面偏好（主题等）
//	├── state.json      正在跑的进程，重启 Pier 后靠它认回来
//	├── logs/           每个服务一份日志
//	└── cache/bin/      Go 服务的编译产物，删了也能重新编译
//
// 找、备份、清理都只看这一处。服务定义不写进工作空间里的 YAML：
//   - 界面是唯一的编辑入口，一个服务只在一处，不再有「手写清单 + 界面覆盖文件」
//     两份拼起来、删不掉只能隐藏的别扭；
//   - 工作空间目录保持干净，里面不再出现任何 Pier 的文件（清单、覆盖、日志、编译产物）；
//   - 数据文件由程序整份写出，不存在「机器改写了人手写的注释和排版」这个问题。
//
// 格式用 JSON 而不是数据库：数据量是几十条服务，整份读写足够快，出问题时
// 也能直接打开看。写入走「临时文件 + 改名」，中途崩溃不会留下半份文件。
//
// YAML 清单可以导入成数据文件（见 EnsureStore），导入只读不写原文件。

// StoreName 是数据文件名，位于数据目录下。
const StoreName = "services.json"

// storeVersion 是数据文件的格式版本。以后改格式时靠它判断要不要迁移。
const storeVersion = 1

// storeFile 是数据文件的磁盘格式。服务目录一律存绝对路径：没有「相对谁」这个隐含前提，
// 打开文件一眼就知道每个服务在哪。
type storeFile struct {
	Version   int               `json:"version"`
	Toolchain map[string]string `json:"toolchain,omitempty"`
	// Env 是共享给所有服务的变量，与 YAML 清单顶层的 env 是同一个东西。
	Env map[string]string `json:"env,omitempty"`
	// Groups 是分组的展示顺序，空分组也靠它保留。成员仍然只看服务自己的 group 字段。
	Groups   []string   `json:"groups"`
	Services []*Service `json:"services"`
}

// AppDirs 是 Pier 在本机用到的几个目录，全部在同一个数据目录下。
type AppDirs struct {
	Data  string // 数据目录本身：services.json、settings.json、state.json
	Logs  string // 服务日志
	Cache string // 编译产物
}

// Dirs 返回数据目录，默认 ~/.pier。PIER_HOME 可以整体换一个位置，测试与临时试用靠它，
// 不碰用户真实数据。
func Dirs() (AppDirs, error) {
	if home := strings.TrimSpace(os.Getenv("PIER_HOME")); home != "" {
		return dirsUnder(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return AppDirs{}, fmt.Errorf("找不到用户主目录：%w", err)
	}
	return dirsUnder(filepath.Join(home, ".pier")), nil
}

func dirsUnder(root string) AppDirs {
	return AppDirs{Data: root, Logs: filepath.Join(root, "logs"), Cache: filepath.Join(root, "cache")}
}

// DefaultStorePath 返回数据文件的默认位置。
func DefaultStorePath() (string, error) {
	d, err := Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data, StoreName), nil
}

// IsStorePath 判断一个路径指的是数据文件而不是 YAML 清单。
func IsStorePath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

// Resolve 决定这次用哪份清单，并给出它的来源说法。
//
// 命令行给了 --config 就用它（YAML 清单只读，编辑入口要收起来）；否则用 Pier
// 自己的数据文件，不存在就建一个空的。图形界面、命令行面板与本地接口都必须
// 按同一条规则选，否则同一个参数在三处会加载出三份不同的清单。
func Resolve(flagPath string) (path, source string, err error) {
	if p := strings.TrimSpace(flagPath); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		return p, "命令行指定", nil
	}
	store, err := DefaultStorePath()
	if err != nil {
		return "", "", err
	}
	if _, err := EnsureStore(store, nil); err != nil {
		return "", "", err
	}
	return store, "本机数据", nil
}

// Open 按扩展名打开：.json 是 Pier 的数据文件，其余当作 YAML 清单（只读，命令行 --config 用）。
func Open(path string) (*Config, error) {
	if IsStorePath(path) {
		return LoadStore(path)
	}
	return Load(path)
}

// dirsForStore 返回某份数据文件配套的日志与缓存目录：就在数据文件旁边。
func dirsForStore(storePath string) AppDirs {
	return dirsUnder(filepath.Dir(storePath))
}

// LoadStore 读取数据文件。文件不存在时返回的错误满足 errors.Is(err, os.ErrNotExist)。
func LoadStore(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据文件路径失败：%w", err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读取数据文件失败：%w", err)
	}
	var sf storeFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return nil, fmt.Errorf("数据文件 %s 已损坏：%w", abs, err)
	}
	if sf.Version > storeVersion {
		return nil, fmt.Errorf("数据文件 %s 是更新版本的 Pier 写的（格式 %d），请升级 Pier", abs, sf.Version)
	}

	// 相对目录没有意义（数据文件不在任何工作空间里），万一手改进来了就按主目录解析。
	root, _ := os.UserHomeDir()
	d := dirsForStore(abs)
	c := &Config{
		Path: abs, Toolchain: sf.Toolchain, Env: sf.Env, DeclaredGroups: sf.Groups,
		store: true, root: root,
		logDir: d.Logs, binDir: filepath.Join(d.Cache, "bin"),
		statePath: filepath.Join(d.Data, "state.json"),
	}
	for _, s := range sf.Services {
		if s == nil {
			continue
		}
		s.rootDir = root
		c.Services = append(c.Services, s)
	}
	// 数据文件允许一个服务都没有：全新安装就是这样，界面会引导添加第一个。
	if err := c.validateServices(); err != nil {
		return nil, fmt.Errorf("数据文件 %s 不合法：%w", abs, err)
	}
	return c, nil
}

// IsStore 表示这份清单来自 Pier 的数据文件，可以修改并写回。
func (c *Config) IsStore() bool { return c.store }

// Save 把当前内容整份写回数据文件。只有来自数据文件的清单能写回：
// YAML 清单按约定是只读的，Pier 不改写人手写的文件。
func (c *Config) Save() error {
	if !c.store {
		return errors.New("这份清单来自 YAML 文件，Pier 不改写它")
	}
	if err := c.validateServices(); err != nil {
		return err
	}
	sf := storeFile{Version: storeVersion, Toolchain: c.Toolchain, Env: c.Env, Groups: c.DeclaredGroups, Services: c.Services}
	if sf.Groups == nil {
		sf.Groups = []string{}
	}
	if sf.Services == nil {
		sf.Services = []*Service{}
	}
	raw, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(c.Path, append(raw, '\n'))
}

// ServiceYAML 把一条服务渲染成一段能直接贴进 pier.yaml 的片段。
//
// 字段不在这里另写一份映射，直接 marshal Service——用的就是清单自己的 yaml 标签。
// 手写一份字段表的话，将来加字段要记得改两处，忘了的那次表现为「复制出去的那份
// 少了端口」，而且不会报任何错。
func ServiceYAML(s *Service) (string, error) {
	if s == nil {
		return "", errors.New("没有这条服务")
	}
	cp := *s
	cp.Origin = "" // 记录的是「它从哪份文件来」，跟着清单一起走没有意义
	raw, err := yaml.Marshal(&cp)
	if err != nil {
		return "", fmt.Errorf("生成 YAML 失败：%w", err)
	}
	return string(raw), nil
}

// ExportYAML 把整份清单渲染成一份干净的 pier.yaml。
//
// 只写 toolchain 与 services，别的都不写：DeclaredGroups 在基础清单里根本没有
// 对应的键（它是覆盖文件才有的东西，见 overlay.go），写出来就是一份 Pier 自己
// 都读不回去的文件。空分组因此在导出时丢掉——这是已知的取舍，导出的是「服务」，
// 一个没有任何服务的空分组本来也不带走什么。
func (c *Config) ExportYAML() (string, error) {
	// 顶层的 env 一并导出：它是清单的一部分，不带出去的话，「复制成 YAML」拿到的
	// 那份贴到别的机器上会少掉所有共享变量，而那些变量在导出前的界面上是看不见的
	// ——服务跑不起来，却找不到少在哪儿。
	out := struct {
		Toolchain map[string]string `yaml:"toolchain,omitempty"`
		Env       map[string]string `yaml:"env,omitempty"`
		Services  []*Service        `yaml:"services"`
	}{Toolchain: c.Toolchain, Env: c.Env, Services: c.Services}
	raw, err := yaml.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("生成 YAML 失败：%w", err)
	}
	// 头两行说明这份文件是什么、从哪来：导出之后它就离开 Pier 了，
	// 下次见到它的人（很可能是别的机器上的自己）得知道它是干什么的。
	head := "# Pier 服务清单\n" +
		"# 从 " + filepath.Base(c.Path) + " 导出，" + time.Now().Format("2006-01-02") + "\n"
	return head + string(raw), nil
}

// WriteAtomic 先写到同目录的临时文件再改名：中途崩溃或断电，
// 磁盘上要么是旧的完整文件、要么是新的完整文件，不会是半份。
//
// 导出是给数据目录里的另外几份文件用的（更新状态、更新结果都在那儿）：
// 数据目录下每一个文件都该用同一把写法，各自写一份迟早有一处漏掉 Sync 或改名。
func WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建数据目录失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	defer os.Remove(tmp.Name())
	// CreateTemp 建出来是 0600，和目录里 state.json、日志的 0644 不一致；统一成 0644。
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("写数据文件失败：%w", err)
	}
	return nil
}

// ── 编辑 ─────────────────────────────────────────────────────────────────
//
// 分组本身不存成员，成员完全由服务的 group 字段决定；这里改的只是那个字段，
// 以及 DeclaredGroups 这份「有哪些分组、按什么顺序」的声明。

// Upsert 新增或替换一个服务。已有同名的就地替换，保持它在列表里的位置。
// 数据文件里的目录一律转成绝对路径再存。
func (c *Config) Upsert(svc *Service) {
	svc.rootDir = c.Dir()
	if c.store {
		svc.Dir = svc.AbsDir()
	}
	for i, s := range c.Services {
		if s.Name == svc.Name {
			c.Services[i] = svc
			return
		}
	}
	c.Services = append(c.Services, svc)
}

// Remove 删掉一个服务，返回是否确实删了。
//
// 顺带把别人 depends_on 里指向它的那一条去掉：留着就是一条指向不存在服务的依赖，
// 下次加载会被判成不合法——用户只是删了个服务，回来却整份清单都读不开了。
func (c *Config) Remove(name string) bool {
	for i, s := range c.Services {
		if s.Name == name {
			c.Services = append(c.Services[:i], c.Services[i+1:]...)
			c.dropDependencyOn(name)
			return true
		}
	}
	return false
}

// dropDependencyOn 把各服务的 depends_on 里指向 name 的那一条摘掉。
//
// 比的是名字那一半（DepName）：写成 name:healthy 的那条也要摘掉，否则删掉一个服务
// 之后，清单里留着一条指向不存在服务的依赖，下次启动直接判成「清单有问题」。
func (c *Config) dropDependencyOn(name string) {
	for _, s := range c.Services {
		if len(s.DependsOn) == 0 {
			continue
		}
		kept := s.DependsOn[:0]
		for _, d := range s.DependsOn {
			if DepName(d) != name {
				kept = append(kept, d)
			}
		}
		s.DependsOn = kept
	}
}

// RenameService 给一个服务改名，位置原地不动。
//
// 位置必须保住：改名不是「删一条再加一条」，列表里那一行的位置、它所属的分组、
// 端口、环境变量都还是原来那份。新名字已经被占用时报错，由调用方决定怎么措辞。
func (c *Config) RenameService(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	for _, s := range c.Services {
		if s.Name == newName {
			return fmt.Errorf("服务名 %s 已经被占用了", newName)
		}
	}
	for _, s := range c.Services {
		if s.Name == oldName {
			s.Name = newName
			// 别人 depends_on 里写的是旧名字，跟着一起改。不改的话这条依赖会变成
			// 指向一个不存在的服务——改名的那个服务自己好好的，坏掉的是依赖它的那些。
			//
			// 只换名字那一半，条件原样留着（TrimPrefix 而不是整条替换）：写成
			// old:healthy 的那条要变成 new:healthy，整条换成 new 的话条件就掉了，
			// 而条件掉了的表现是「这次启动不再等它」——日志里一个字都不会有。
			for _, o := range c.Services {
				for i, d := range o.DependsOn {
					if DepName(d) == oldName {
						o.DependsOn[i] = newName + strings.TrimPrefix(d, oldName)
					}
				}
			}
			return nil
		}
	}
	return fmt.Errorf("没有名为 %s 的服务", oldName)
}

// ReorderServices 按 names 给出的顺序，重排这些名字此刻占着的那几个位置。
//
// 是「部分重排」而不是「整表替换」：names 只给出要被挪动的那一些，其余服务
// 一个都不动。界面在分组页或筛选之后拖动时，手里只有看得见的那几行，整表替换
// 会把看不见的那些（别的分组的、被筛掉的）全挤到一起——用户只动了一行，回来的
// 却是另一个列表。
//
// 落点按位置算：names 里的第一个填进这些名字原来占着的第一个位置，依此类推。
// 所以「拖到可见列表的末尾」就是把最后一个位置给它，而这个位置后面可能还夹着
// 看不见的服务——那正是界面上看到的结果。
//
// 服务名不齐（少了、多了、有重名、有不存在的）时整个拒绝：调用方按界面上的
// 顺序生成 names，对不上说明它手上的列表已经过期，照着填只会把顺序搅乱。
func (c *Config) ReorderServices(names []string) error {
	if len(names) == 0 {
		return errors.New("没有给出要排序的服务")
	}
	slots := make([]int, 0, len(names))
	byName := make(map[string]*Service, len(names))
	for _, name := range names {
		if _, dup := byName[name]; dup {
			return fmt.Errorf("服务名 %s 在排序里出现了两次", name)
		}
		at := -1
		for i, s := range c.Services {
			if s.Name == name {
				at, byName[name] = i, s
				break
			}
		}
		if at < 0 {
			return fmt.Errorf("没有名为 %s 的服务", name)
		}
		slots = append(slots, at)
	}
	// 把这些格子按先后排好，再按界面上的顺序一个个填进去——填的是格子，不是
	// 每个名字自己原来那格（那样填等于什么都没做）。
	sort.Ints(slots)
	for i, at := range slots {
		c.Services[at] = byName[names[i]]
	}
	return nil
}

// ReorderGroups 按 names 给出的顺序重排分组，语义与 ReorderServices 相同。
//
// 顺序存在 DeclaredGroups 里，而 AllGroups 会把「只在服务里出现、没声明过」的
// 分组接在末尾——那些分组光靠重排是挪不动的，所以这里先把当前的全部分组补写进
// 声明里（「未分组」除外，它是内置的桶，永远排在声明过的分组后面），再按位置填。
func (c *Config) ReorderGroups(names []string) error {
	if len(names) == 0 {
		return errors.New("没有给出要排序的分组")
	}
	cur := c.AllGroups()
	seen := make(map[string]bool, len(names))
	for _, g := range names {
		if g == UngroupedName {
			return fmt.Errorf("「%s」是内置的分组，位置固定在最后", UngroupedName)
		}
		if seen[g] {
			return fmt.Errorf("分组「%s」在排序里出现了两次", g)
		}
		seen[g] = true
		found := false
		for _, x := range cur {
			if x == g {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("没有名为「%s」的分组", g)
		}
	}
	// 补写：把当前的全部顺序落成一份显式声明，之后再谈位置。
	declared := make([]string, 0, len(cur))
	for _, g := range cur {
		if g != "" && g != UngroupedName {
			declared = append(declared, g)
		}
	}
	c.DeclaredGroups = nil
	for _, g := range declared {
		c.AddGroup(g)
	}

	// 在声明里找位置：names 里的第一个填进它们原来占着的第一个格子。
	slots := make([]int, 0, len(names))
	for _, name := range names {
		at := -1
		for i, g := range c.DeclaredGroups {
			if g == name {
				at = i
				break
			}
		}
		if at < 0 {
			return fmt.Errorf("分组「%s」不在可排序的清单里", name)
		}
		slots = append(slots, at)
	}
	sort.Ints(slots)
	out := append([]string(nil), c.DeclaredGroups...)
	for i, name := range names {
		out[slots[i]] = name
	}
	c.DeclaredGroups = out
	return nil
}

// AddGroup 声明一个分组，重复声明不产生重复条目。
func (c *Config) AddGroup(name string) {
	name = strings.TrimSpace(name)
	if name == "" || name == UngroupedName {
		return
	}
	for _, g := range c.DeclaredGroups {
		if g == name {
			return
		}
	}
	c.DeclaredGroups = append(c.DeclaredGroups, name)
}

// RenameGroup 改掉分组名，成员跟着走。返回跟着移动的服务数。
func (c *Config) RenameGroup(oldName, newName string) int {
	for i, g := range c.DeclaredGroups {
		if g == oldName {
			c.DeclaredGroups[i] = newName
		}
	}
	n := 0
	for _, s := range c.Services {
		if s.GroupName() == oldName {
			s.Group = newName
			n++
		}
	}
	return n
}

// RemoveGroup 撤掉分组，成员退回「未分组」。返回被移出的服务数。
func (c *Config) RemoveGroup(name string) int {
	for i, g := range c.DeclaredGroups {
		if g == name {
			c.DeclaredGroups = append(c.DeclaredGroups[:i], c.DeclaredGroups[i+1:]...)
			break
		}
	}
	n := 0
	for _, s := range c.Services {
		if s.GroupName() == name {
			s.Group = ""
			n++
		}
	}
	return n
}

// ── 创建与导入 ───────────────────────────────────────────────────────────

// EnsureStore 保证数据文件存在，返回这次是从哪份 YAML 导入的（没有导入则为空）。
//
// 数据文件已经在：什么都不做。不在：yaml 里给了存在的清单就连同它的覆盖文件一起
// 读出合并后的结果写进去（被隐藏的不导入），否则建一个空的。
// YAML 读不出来时报错而不是悄悄建一个空的，原文件一律只读不写。
func EnsureStore(storePath string, yaml []string) (string, error) {
	if _, err := os.Stat(storePath); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取数据文件失败：%w", err)
	}

	for _, p := range yaml {
		p = strings.TrimSpace(p)
		if p == "" || IsStorePath(p) {
			continue
		}
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		src, err := Load(p)
		if err != nil {
			return "", fmt.Errorf("从 YAML 导入失败（原文件没有改动）：%w", err)
		}
		home, _ := os.UserHomeDir()
		c := newStore(storePath, home)
		c.Toolchain = src.Toolchain
		c.Env = src.Env
		for _, g := range src.AllGroups() {
			c.AddGroup(g)
		}
		for _, s := range src.Services {
			cp := *s
			cp.Origin = ""
			cp.Dir = s.AbsDir() // YAML 里的相对路径相对 YAML 所在目录，导入时就地转成绝对路径
			c.Upsert(&cp)
		}
		if err := c.Save(); err != nil {
			return "", err
		}
		return src.Path, nil
	}

	home, _ := os.UserHomeDir()
	return "", newStore(storePath, home).Save()
}

// OpenDefault 打开默认位置的数据文件，不存在就建一个空的。
func OpenDefault() (*Config, error) {
	path, err := DefaultStorePath()
	if err != nil {
		return nil, err
	}
	if _, err := EnsureStore(path, nil); err != nil {
		return nil, err
	}
	return LoadStore(path)
}

// newStore 在内存里建一份空的数据文件清单。
func newStore(storePath, root string) *Config {
	abs, _ := filepath.Abs(storePath)
	d := dirsForStore(abs)
	return &Config{Path: abs, store: true, root: root,
		logDir: d.Logs, binDir: filepath.Join(d.Cache, "bin"),
		statePath: filepath.Join(d.Data, "state.json")}
}
