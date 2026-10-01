package manage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/toolchain"
)

// ── SDK 管理 ───────────────────────────────────────────────────────────────
//
// 自动发现（toolchain/discover.go）覆盖的是「装在常见位置」的那些。公司内网镜像
// 解压出来的 JDK、自己编译的 Python 不在其中，靠这里手动补上；各语言的全局默认
// 也在这里设。两者都写进 ~/.pier/settings.json，服务级的选择仍然写在服务上，
// 优先级比它们高（见 toolchain.Resolver 的选择顺序）。

// sdkKinds 是「SDK 管理」页列出的类别与顺序，按「一个 Java 服务要用到的先后」排：
// 先 JDK，再编译它的 Maven；然后是前端、脚本语言，最后是 Go。
//
// 不含 pnpm：它不是独立安装的一套运行时，而是跟着选中的 node 走的。
var sdkKinds = []toolchain.Kind{
	toolchain.Java, toolchain.Maven, toolchain.Node, toolchain.Python, toolchain.Go,
}

// SDKItemOut 是「SDK 管理」里的一行。
type SDKItemOut struct {
	Kind string `json:"kind"`
	// Path 是安装根目录（Java 是 JAVA_HOME），也就是存进设置里的那个值。
	Path    string `json:"path"`
	Bin     string `json:"bin"`
	Label   string `json:"label"` // 「21.0.9 · Oracle」
	Version string `json:"version"`
	Vendor  string `json:"vendor"`
	Source  string `json:"source"`
	// Manual 为真表示这一条是手动添加的，可以删掉；扫出来的删不了，只能去卸。
	Manual bool `json:"manual"`
}

// SDKKindOut 是「SDK 管理」里的一个类别。
type SDKKindOut struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Default 是这一类别的全局默认 SDK 路径，空表示按项目声明与规则自动选。
	Default string       `json:"default"`
	Items   []SDKItemOut `json:"items"`
}

// SDKListOut 是「SDK 管理」页的全部内容。
type SDKListOut struct {
	OK    bool         `json:"ok"`
	Kinds []SDKKindOut `json:"kinds"`
}

// SDKAddOut 是手动添加的结果：加进去的那一条，连同它校验之后规范化的路径。
//
// 回传 Item 而不是只回一句「已添加」：界面「浏览…」选完之后要立刻把这一条选中，
// 而选中的值必须是规范化之后的路径（macOS 上选 .jdk 包时，存的是包里的
// Contents/Home），界面自己去猜这个转换只会猜错。
type SDKAddOut struct {
	OK   bool        `json:"ok"`
	Msg  string      `json:"msg"`
	Item *SDKItemOut `json:"item"`
}

// ToolchainOut 是「这个服务这次会用哪些 SDK」的答案，供表单下方实时显示。
type ToolchainOut struct {
	OK     bool            `json:"ok"`
	Msg    string          `json:"msg"`
	AbsDir string          `json:"absDir"`
	Tools  []proc.ToolInfo `json:"tools"`
}

// SDKList 列出各类别本机能用的 SDK，连同手动添加的与当前设的全局默认。
func (m *Manager) SDKList() (*SDKListOut, error) {
	st, err := loadSettings()
	if err != nil {
		return nil, err
	}
	out := &SDKListOut{OK: true, Kinds: make([]SDKKindOut, 0, len(sdkKinds))}
	for _, k := range sdkKinds {
		sdks := toolchain.Discover(k, st.SDKs[string(k)])
		row := SDKKindOut{Kind: string(k), Label: k.Label(),
			Default: st.SDKDefaults[string(k)], Items: make([]SDKItemOut, 0, len(sdks))}
		for _, s := range sdks {
			row.Items = append(row.Items, SDKItemOut{
				Kind: string(s.Kind), Path: s.Home, Bin: s.Bin, Label: s.Label(),
				Version: s.Version, Vendor: s.Vendor, Source: s.Source, Manual: s.Manual,
			})
		}
		out.Kinds = append(out.Kinds, row)
	}
	return out, nil
}

// SDKRescan 丢掉扫描缓存，下一次查询重新扫盘。刚装完一个 SDK 时用。
func (m *Manager) SDKRescan() string {
	toolchain.InvalidateDiscovery()
	return "已重新扫描"
}

// SDKAdd 把用户手动指定的目录记进「手动添加的 SDK」。
//
// 先真的校验一遍：目录里得有这一类别的可执行文件。不校验的话，记进去的只是一个
// 选不动的选项，而错误要到启动服务时才浮出来。
//
// 存的是校验之后的规范路径：macOS 上选到 .jdk 包本身时，SDK 的 Home 是包里的
// Contents/Home，存包路径的话，它和自动扫描出来的同一个 JDK 会被当成两个。
func (m *Manager) SDKAdd(kind, path string) (*SDKAddOut, error) {
	k, err := parseKind(kind)
	if err != nil {
		return nil, err
	}
	s, err := inspectSDK(k, path)
	if err != nil {
		return nil, err
	}
	err = updateSettings(func(st *config.Settings) {
		if st.SDKs == nil {
			st.SDKs = map[string][]string{}
		}
		for _, v := range st.SDKs[string(k)] {
			if v == s.Home {
				return
			}
		}
		st.SDKs[string(k)] = append(st.SDKs[string(k)], s.Home)
	})
	if err != nil {
		return nil, err
	}
	// 刚记下的路径不在缓存里，不清的话这次添加要等半分钟才在列表上出现。
	toolchain.InvalidateDiscovery()
	return &SDKAddOut{OK: true,
		Msg: fmt.Sprintf("已添加 %s %s", k.Label(), s.Label()),
		Item: &SDKItemOut{Kind: string(s.Kind), Path: s.Home, Bin: s.Bin, Label: s.Label(),
			Version: s.Version, Vendor: s.Vendor, Source: s.Source, Manual: true}}, nil
}

// SDKRemove 从手动添加的列表里删掉一条。自动扫到的不在这里，也删不掉。
func (m *Manager) SDKRemove(kind, path string) (string, error) {
	k, err := parseKind(kind)
	if err != nil {
		return "", err
	}
	path = strings.TrimSpace(path)
	err = updateSettings(func(st *config.Settings) {
		kept := make([]string, 0, len(st.SDKs[string(k)]))
		for _, v := range st.SDKs[string(k)] {
			if v != path {
				kept = append(kept, v)
			}
		}
		if len(kept) == 0 {
			delete(st.SDKs, string(k))
		} else {
			st.SDKs[string(k)] = kept
		}
		// 全局默认正指着它时一并清掉：留一个指向已删路径的默认值，只会让这一类
		// 每次解析都失败，而失败信息要到启动服务时才看得到。
		if st.SDKDefaults[string(k)] == path {
			delete(st.SDKDefaults, string(k))
		}
	})
	if err != nil {
		return "", err
	}
	toolchain.InvalidateDiscovery()
	return "已从手动添加里删掉 " + path, nil
}

// SDKDefault 设某一类别的全局默认；path 为空表示改回自动选。
func (m *Manager) SDKDefault(kind, path string) (string, error) {
	k, err := parseKind(kind)
	if err != nil {
		return "", err
	}
	path = strings.TrimSpace(path)
	if path != "" {
		// 设之前确认它真的能用：一个打错的路径会让这个类别每次都解析失败。
		if _, err := inspectSDK(k, path); err != nil {
			return "", err
		}
	}
	err = updateSettings(func(st *config.Settings) {
		if path == "" {
			delete(st.SDKDefaults, string(k))
			return
		}
		if st.SDKDefaults == nil {
			st.SDKDefaults = map[string]string{}
		}
		st.SDKDefaults[string(k)] = path
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return k.Label() + " 改回自动选择", nil
	}
	return fmt.Sprintf("%s 的全局默认已设为 %s", k.Label(), path), nil
}

// Toolchain 用表单上还没保存的内容解析一遍工具链，让「将使用 X，依据 Y」
// 在保存之前就看得见。
//
// 走的是与真正启动时同一套解析器（proc.Supervisor.Tools），只是喂进去的是表单里
// 那个还没落盘的服务：另写一份简化版的挑选逻辑，两边迟早会对不上，而这里给出
// 的答案是要让人据此决定「要不要钉一个版本」的。
func (m *Manager) Toolchain(in ServiceIn) (*ToolchainOut, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		return nil, errors.New("请先填写项目目录")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cfg.Dir(), dir)
	}
	svc := in.toService()
	svc.Dir = filepath.Clean(dir)
	// toService 只管清单里那几个字段，模板里选的 SDK 与服务名无关，得自己带上。
	svc.Toolchain = in.Toolchain
	sup := proc.New(cfg)
	tools, _, err := sup.Tools(svc)
	return &ToolchainOut{OK: true, AbsDir: svc.Dir, Tools: proc.ToolInfos(tools, err)}, nil
}

func parseKind(kind string) (toolchain.Kind, error) {
	k := toolchain.Kind(strings.TrimSpace(kind))
	for _, known := range sdkKinds {
		if k == known {
			return k, nil
		}
	}
	return "", fmt.Errorf("不认识的类别：%q", kind)
}

// inspectSDK 校验一个路径是不是这一类别的 SDK。空路径单独报错：
// Inspect 会说「请给出绝对路径」，而这里更该说的是「还没选」。
func inspectSDK(k toolchain.Kind, path string) (toolchain.SDK, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return toolchain.SDK{}, fmt.Errorf("请先填写 %s 的目录", k.Label())
	}
	return toolchain.Inspect(k, path)
}

func loadSettings() (config.Settings, error) {
	p, err := config.SettingsPath()
	if err != nil {
		return config.Settings{}, err
	}
	return config.LoadSettings(p)
}

func updateSettings(fn func(*config.Settings)) error {
	p, err := config.SettingsPath()
	if err != nil {
		return err
	}
	return config.UpdateSettings(p, fn)
}
