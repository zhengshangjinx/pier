package config

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Watch 是「改完自动重启」的配置：这个服务要不要盯着自己的目录，盯哪些文件。
//
// 清单里两种写法：
//
//	watch: true                    # 按服务类型给一套默认（go 盯 *.go 与 go.mod）
//	watch: ["src/**", "pom.xml"]   # 自己点名盯哪些
//
// 只认这两种，不是一个对象（`{include: [...], exclude: [...]}`）：要说的事一行就说完，
// 而对象形状会让人以为 exclude 也是可配的。排除哪些目录是定死的（node_modules、
// target、dist 这些，见 internal/watch），它能挡掉大半自激——编译产物一落地就再重启
// 一次，改了代码反而看着像卡在循环里。那不是留给用户去调的东西。
type Watch struct {
	// On 表示这个服务要盯着自己的目录。
	On bool
	// Include 是要盯的模式，相对服务目录；留空表示按类型给一套默认（见 WatchPatterns）。
	Include []string
}

// WatchPatterns 是这个服务实际要盯的模式。
//
// 返回空切片表示「这个目录里的文件都算数」——shell 服务与认不出类型的目录就是这一档：
// 用户说了要盯，而我们没有把握说哪些文件算源码，那就整个目录（排掉的目录仍然排掉）。
func (s *Service) WatchPatterns() []string {
	if !s.Watch.On {
		return nil
	}
	if len(s.Watch.Include) > 0 {
		return s.Watch.Include
	}
	kind := s.Kind
	if kind == "" {
		// 类型是认出来的，那默认模式也得跟着认——否则一个没写 kind 的 go 服务
		// 会掉进「整个目录都算数」那一档，把编译产物也算进去。
		if detected, err := DetectKind(s.AbsDir()); err == nil {
			kind = detected
		}
	}
	return DefaultWatchPatterns(kind)
}

// DefaultWatchPatterns 是按服务类型给的那套默认。
//
// 挑的是「改了它，跑着的进程就该换一份」的那些文件：源码加上声明依赖的那个文件
// （go.mod / pom.xml / package.json）。配置、模板、静态资源不在里头——它们是运行时
// 读的，重启并不需要，而把它们算进来会让「改一个 SQL 文件」也重起一次。
func DefaultWatchPatterns(kind string) []string {
	switch kind {
	case KindGo:
		return []string{"*.go", "go.mod", "go.sum"}
	case KindJava:
		// 只盯 src/main 与 src/test：Maven 的产物在 target（本来就排掉），
		// 而 src/main 之外的东西（文档、脚本）改了不影响这次运行。
		return []string{"src/main/**", "src/test/**", "pom.xml"}
	case KindNode:
		// 前端与 Node 服务的源码就在根上或者 src 下，按扩展名认比按目录认稳。
		return []string{
			"*.js", "*.mjs", "*.cjs", "*.ts", "*.mts", "*.jsx", "*.tsx",
			"*.vue", "*.svelte", "*.css", "*.html", "package.json",
		}
	case KindPython:
		return []string{"*.py", "requirements.txt", "pyproject.toml", "setup.py", "setup.cfg"}
	}
	// shell 与认不出的类型：不猜，整个目录（见 WatchPatterns 的注释）。
	return nil
}

// IsZero 让「没配 watch」的服务在清单里不占一行。
//
// 只有 YAML 那一侧认它：yaml.v3 会问这个方法，而 encoding/json 的 omitempty 只看
// 切片、映射、指针这些空得出来的种类，结构体字段一律照写——所以数据文件里关掉之后
// 是 `"watch": false`。那是 Pier 自己管的文件，写一个 false 比省掉一行更直白；
// 用户手写、导出的那份 YAML 是干净的。
func (w Watch) IsZero() bool { return !w.On && len(w.Include) == 0 }

// Watchable 表示这个服务配了监视。
func (w Watch) Watchable() bool { return w.On }

func (w *Watch) UnmarshalYAML(node *yaml.Node) error {
	var v any
	if err := node.Decode(&v); err != nil {
		return err
	}
	return w.fromNative(v)
}

func (w Watch) MarshalYAML() (any, error) { return w.native(), nil }

func (w *Watch) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	return w.fromNative(v)
}

func (w Watch) MarshalJSON() ([]byte, error) { return json.Marshal(w.native()) }

// native 是它在清单里的样子：不用写模式就是一个 true，写了就是一串模式。
//
// 两种写法来回转都走这一个函数，YAML 与 JSON 各自的那两个方法只是把值递进来——
// 形状有两处实现的话，「读出 true、写回去成了 [true]」这类事迟早会发生。
func (w Watch) native() any {
	if !w.On {
		return false
	}
	if len(w.Include) == 0 {
		return true
	}
	return w.Include
}

// fromNative 是 native 的反面，两种清单格式共用同一份解读。
func (w *Watch) fromNative(v any) error {
	switch v := v.(type) {
	case nil:
		// `watch:` 后面空着。当没配——写了一半的配置不该让服务起不来。
		*w = Watch{}
	case bool:
		*w = Watch{On: v}
	case []any:
		patterns := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("watch 的模式只能是一串字符串，收到 %v", e)
			}
			if s == "" {
				return fmt.Errorf("watch 里有一个空模式")
			}
			patterns = append(patterns, s)
		}
		if len(patterns) == 0 {
			// `watch: []`：一个都不盯，与不配是同一件事。留一个空的 On 只会
			// 让状态里写着「正在监视」，而它什么也没盯。
			*w = Watch{}
			return nil
		}
		*w = Watch{On: true, Include: patterns}
	default:
		return fmt.Errorf("watch 只认 true 或一组模式（如 [\"src/**\"]），收到 %v", v)
	}
	return nil
}
