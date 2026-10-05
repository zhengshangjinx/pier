package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 清单里的值可以引用别的变量：DATABASE_URL: jdbc:postgresql://${DB_HOST}:5432/app。
// 这一段只认 ${名字} 一种写法，别的 $ 一律原样留着——展开是显式触发的，
// 没写 ${} 的值一个字节都不改。

// UnknownVarError 说明 ${名字} 里的名字在当前环境里找不到。
//
// 单独一个类型，是为了让 ResolveEnvLayer 能分辨「这个名字眼下还不认得」与
// 「写法本身就不对」：前者可以等同一层里别的变量先解出来（A 引 B、B 引 C 都是常事），
// 后者当场就得停下——同一个错误多等几轮也还是错的。
type UnknownVarError struct{ Name string }

func (e *UnknownVarError) Error() string { return "${" + e.Name + "} 没有定义" }

// Expand 展开一个值里的 ${名字}。
//
// lookup 给不出来的名字报错，**不留空**：留空是把一个空串安安静静地传给子进程，
// 出来的是「连接串少了主机名」这种没人看得懂的现场；报错只说一行，但那一行就说清了
// 是哪个变量没定义。
//
// 只有 ${ 会被当成引用：孤零零的 $、$NAME、${!cmd} 里的那些都原样抄过去，
// 因为清单里的值常常就是给子进程看的 shell 片段，Pier 不该去改它。真要一个字面的
// ${，写成 $${（多出来的那个 $ 会被吃掉，后面的 { 照常留下）。这么写是为了让
// 「看不懂的写法一律报错」不至于变成一个死胡同：没有这条退路，值里带 ${ 的人
// 就没法写这个值了。
func Expand(s string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "$${") {
			b.WriteByte('$')
			i += 2 // 只吃掉两个 $，{ 留给下一轮照抄，合起来就是字面的 ${
			continue
		}
		if strings.HasPrefix(s[i:], "${") {
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("${ 没有收尾的 }：%s", s)
			}
			name := s[i+2 : i+end]
			if !ValidEnvName(name) {
				return "", fmt.Errorf("${%s} 不是一个变量名（只能字母、数字、下划线，且不以数字开头）", name)
			}
			v, ok := lookup(name)
			if !ok {
				return "", &UnknownVarError{Name: name}
			}
			b.WriteString(v)
			i += end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

// EnvList 把一份 map 形式的变量摊成按键排好序的列表。
//
// 固定顺序不是为了好看：合并时后写的覆盖先写的，顺序一变「谁盖住了谁」就跟着变。
// 同一份清单每次启动必须得到一模一样的环境，否则日志里那几行环境变量没法比对。
func EnvList(m map[string]string) []EnvKV {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]EnvKV, 0, len(keys))
	for _, k := range keys {
		out = append(out, EnvKV{Key: k, Value: m[k]})
	}
	return out
}

// ResolveEnvLayer 展开一整层变量（顶层共享段，或某个服务自己那段）里的 ${}。
//
// 层内的名字可以互相引用，所以这是求一个不动点而不是照 list 的顺序算一遍：
// map 的遍历顺序本来就是随机的，A 引 B、B 引 C 时按顺序算会时对时错。
// lookup 是这一层外面的环境（继承来的 + 工具链 + 端口），层里自己的名字优先。
//
// 解不出来就整层报错，一个变量都不返回：部分展开的结果比不展开更坏——它看起来
// 是成功的，而某个值里静静地留着一个 `${X}`，等子进程去撞。
func ResolveEnvLayer(layer []EnvKV, lookup func(string) (string, bool)) ([]EnvKV, error) {
	if len(layer) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(layer))
	pending := make(map[string]string, len(layer))
	for _, kv := range layer {
		if _, dup := pending[kv.Key]; !dup {
			keys = append(keys, kv.Key)
		}
		pending[kv.Key] = kv.Value // 同名以最后一条为准
	}
	sort.Strings(keys) // 报错落在哪一项上也要是确定的

	done := make(map[string]string, len(layer))
	// 层的名字优先于外面的：服务自己的 env 覆盖共享段，靠的就是这一条。
	lookupLayer := func(name string) (string, bool) {
		if v, ok := done[name]; ok {
			return v, true
		}
		return lookup(name)
	}

	// 每一轮至少解出一个才会有进展，所以最多这么多轮就够了；
	// 到点还没解完的，只可能是绕成了环。
	for pass := 0; pass < len(keys) && len(pending) > 0; pass++ {
		progress := false
		for _, k := range keys {
			v, ok := pending[k]
			if !ok {
				continue
			}
			expanded, err := Expand(v, lookupLayer)
			if err != nil {
				var unknown *UnknownVarError
				if errors.As(err, &unknown) {
					if _, later := pending[unknown.Name]; later {
						continue // 它引的是同一层里还没轮到的那一个，下一轮再说
					}
				}
				return nil, fmt.Errorf("%s：%w", k, err)
			}
			done[k] = expanded
			delete(pending, k)
			progress = true
		}
		if !progress {
			break
		}
	}
	if len(pending) > 0 {
		for _, k := range keys {
			v, ok := pending[k]
			if !ok {
				continue
			}
			_, err := Expand(v, lookupLayer)
			var unknown *UnknownVarError
			if errors.As(err, &unknown) {
				if _, inLayer := pending[unknown.Name]; inLayer {
					return nil, fmt.Errorf("%s 里的 ${%s} 绕回了这一层，展开不出来（自己引用自己，或几个变量互相引用）", k, unknown.Name)
				}
			}
			return nil, fmt.Errorf("%s：%w", k, err)
		}
	}

	out := make([]EnvKV, 0, len(done))
	for _, k := range keys {
		out = append(out, EnvKV{Key: k, Value: done[k]})
	}
	return out, nil
}
