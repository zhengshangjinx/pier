package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// OverlayName 是可写覆盖文件的文件名，与 pier.yaml 同级。
//
// 为什么要有这份覆盖文件：界面里能新增、改端口、写备注，但 pier.yaml 是开发者
// 手写的、被当作已确认的环境事实，工具不该去改写它——一次自动重写就可能把注释、
// 排版和顺序全部抹掉。于是界面里的一切写入都落到这份单独的覆盖文件，
// 读取时再把两份合并。手写的始终是手写的，机器写的始终在另一个文件里。
//
// 放在清单同级而不是 ~/Library/Application Support 下，是因为：
//   - 覆盖是「针对这份清单」的，跟着清单走才不会在换工作空间时串味；
//   - 它需要能被手工编辑和阅读（界面里的「添加应用」本身就是照着它设计的），
//     藏进 Application Support 会让这件事变得很别扭。
const OverlayName = "pier.user.yaml"

// 服务定义的来源，决定界面里能不能真正删除它。
const (
	// OriginBase 来自 pier.yaml，只读；界面只能「隐藏」，不能删。
	OriginBase = "base"
	// OriginOverlay 来自 pier.user.yaml，界面可以真正删除。
	OriginOverlay = "overlay"
)

// Overlay 是覆盖文件的结构。
type Overlay struct {
	// Groups 是界面里显式建过的分组名，按创建顺序排列。
	//
	// 分组本来是可以从服务身上的 group 字段推出来的，之所以还要在这里记一份，
	// 是为了让**空分组**能存在：刚建好的分组一个人也没有，推出来的话它当场就消失了，
	// 用户会看到「新建分组」点了没反应。这份列表只管「存在哪些分组、按什么顺序排」，
	// 谁属于哪个分组仍然完全由服务自己的 group 字段决定。
	Groups []string `yaml:"groups"`
	// Hidden 列出要隐藏的 pier.yaml 服务名。
	// 手工写的服务删不掉（源文件只读），所以「删除」在界面上被如实呈现为「隐藏」。
	Hidden []string `yaml:"hidden"`
	// Services 里的服务会覆盖同名的 base 服务（就地替换，保持原位置），
	// 名字不冲突的则按书写顺序追加到末尾。
	Services []*Service `yaml:"services"`
}

// HasGroup 判断分组是否已在覆盖里声明过。
func (o *Overlay) HasGroup(name string) bool {
	for _, g := range o.Groups {
		if g == name {
			return true
		}
	}
	return false
}

// AddGroup 声明一个分组，重复声明不产生重复条目。
func (o *Overlay) AddGroup(name string) {
	if name = strings.TrimSpace(name); name != "" && !o.HasGroup(name) {
		o.Groups = append(o.Groups, name)
	}
}

// RenameGroup 改掉分组名，连同覆盖里所有属于它的服务一起改。
// 返回是否确实找到了这个分组。
//
// 只管覆盖文件内部：base 清单里那些服务的 group 字段由调用方补写成覆盖定义，
// 因为这一层拿不到 base 的原文。
func (o *Overlay) RenameGroup(oldName, newName string) bool {
	found := false
	for i, g := range o.Groups {
		if g == oldName {
			o.Groups[i] = newName
			found = true
		}
	}
	for _, s := range o.Services {
		if s != nil && strings.TrimSpace(s.Group) == oldName {
			s.Group = newName
			found = true
		}
	}
	return found
}

// RemoveGroup 撤掉一个分组的声明。
func (o *Overlay) RemoveGroup(name string) bool {
	for i, g := range o.Groups {
		if g == name {
			o.Groups = append(o.Groups[:i], o.Groups[i+1:]...)
			return true
		}
	}
	return false
}

// overlayPath 返回这份清单对应的覆盖文件路径。
// 不导出：路径在加载时就填进了 Config.OverlayPath 字段，外部一律读那个，
// 避免出现「字段」和「方法」两个同名入口各算一遍。
func (c *Config) overlayPath() string {
	return overlayPathFor(c.Path)
}

// overlayPathFor 返回清单同级的覆盖文件路径。
func overlayPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), OverlayName)
}

// LoadOverlay 读取覆盖文件；文件不存在视为空覆盖，不是错误。
func LoadOverlay(path string) (*Overlay, error) {
	o := &Overlay{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return o, nil
		}
		return nil, fmt.Errorf("读取 %s 失败：%w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return o, nil
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(o); err != nil {
		return nil, fmt.Errorf("解析 %s 失败：%w", path, err)
	}

	// 重名必须在读进来时就报错。合并时同名的只有最后一条会生效，
	// 另一条被静默丢掉——那意味着界面上怎么改都改不动某个服务，且没有任何提示。
	seen := map[string]bool{}
	for i, s := range o.Services {
		if s == nil || strings.TrimSpace(s.Name) == "" {
			return nil, fmt.Errorf("%s 第 %d 个服务缺少 name", path, i+1)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("%s 中服务名重复：%s", path, s.Name)
		}
		seen[s.Name] = true
	}
	return o, nil
}

// Save 原子写入覆盖文件：先写临时文件再改名，避免中途失败留下半份文件。
func (o *Overlay) Save(path string) error {
	var b strings.Builder
	b.WriteString("# Pier 覆盖文件 —— 由图形界面维护，也可以直接手写。\n")
	b.WriteString("# pier.yaml 是只读的，界面里新增或修改的服务一律写在这里。\n")
	b.WriteString("# groups 是界面里建过的分组（空分组也靠它保留），顺序即面板上的顺序。\n")
	b.WriteString("# hidden 里的名字会被隐藏（源文件里还在，只是不再显示、不再启停）。\n")
	b.WriteString("# services 里同名的会就地覆盖 pier.yaml 中的定义，新名字则追加到末尾。\n")
	b.WriteString("# dir 与本文件所在目录相对。\n\n")

	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(o); err != nil {
		return fmt.Errorf("序列化覆盖文件失败：%w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("序列化覆盖文件失败：%w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败：%w", path, err)
	}
	return os.Rename(tmp, path)
}

// Find 按名字取覆盖里的服务定义。
func (o *Overlay) Find(name string) *Service {
	for _, s := range o.Services {
		if s != nil && s.Name == name {
			return s
		}
	}
	return nil
}

// Upsert 写入或就地替换一个服务定义。
func (o *Overlay) Upsert(svc *Service) {
	for i, s := range o.Services {
		if s != nil && s.Name == svc.Name {
			o.Services[i] = svc
			return
		}
	}
	o.Services = append(o.Services, svc)
}

// Remove 从覆盖里删掉一个服务定义，返回是否确实删掉了。
func (o *Overlay) Remove(name string) bool {
	for i, s := range o.Services {
		if s != nil && s.Name == name {
			o.Services = append(o.Services[:i], o.Services[i+1:]...)
			return true
		}
	}
	return false
}

// IsHidden 判断某个名字是否被隐藏。
func (o *Overlay) IsHidden(name string) bool {
	for _, n := range o.Hidden {
		if n == name {
			return true
		}
	}
	return false
}

// Hide 隐藏一个名字；重复隐藏不产生重复条目。
func (o *Overlay) Hide(name string) {
	if !o.IsHidden(name) {
		o.Hidden = append(o.Hidden, name)
	}
}

// Unhide 取消隐藏，返回是否确实取消了。
func (o *Overlay) Unhide(name string) bool {
	for i, n := range o.Hidden {
		if n == name {
			o.Hidden = append(o.Hidden[:i], o.Hidden[i+1:]...)
			return true
		}
	}
	return false
}

// merge 把覆盖叠加到基础清单上。base 的顺序是主顺序：被覆盖的服务留在原位，
// 新增的追加到末尾；hidden 里的名字整个摘掉。
func (c *Config) merge(base []*Service, o *Overlay) {
	byName := make(map[string]*Service, len(o.Services))
	for _, s := range o.Services {
		if s != nil && s.Name != "" {
			byName[s.Name] = s
		}
	}

	out := make([]*Service, 0, len(base)+len(o.Services))
	seen := make(map[string]bool, len(base))
	for _, s := range base {
		if o.IsHidden(s.Name) {
			continue
		}
		if rep, ok := byName[s.Name]; ok {
			rep.Origin = OriginOverlay
			rep.rootDir = c.Dir()
			out = append(out, rep)
			seen[s.Name] = true
			continue
		}
		s.Origin = OriginBase
		out = append(out, s)
		seen[s.Name] = true
	}
	for _, s := range o.Services {
		// IsHidden 这一条不能省：一条「覆盖了 base 定义」的服务被隐藏时，
		// 它在 base 那一轮已经被跳过，这里若不再拦一次，它会从覆盖文件里绕回来，
		// 于是「隐藏」对一个改过端口或备注的服务完全不起作用。
		if s == nil || s.Name == "" || seen[s.Name] || o.IsHidden(s.Name) {
			continue
		}
		s.Origin = OriginOverlay
		s.rootDir = c.Dir()
		out = append(out, s)
	}
	c.Services = out
	c.Hidden = append([]string(nil), o.Hidden...)
	c.DeclaredGroups = append([]string(nil), o.Groups...)
}
