package config

import "strings"

// CondHealthy 是 depends_on 里唯一认的条件：等这个前置的健康探针通过。
//
// 只有这一个，是因为只有它背后有一个真的信号可等。编写「启动完成」那种条件要
// 反过来问服务自己，而 Pier 不常驻、服务也未必听话；与其造一个查不出来的承诺，
// 不如把能兑现的那一个写清楚，其余的照旧只排顺序。
const CondHealthy = "healthy"

// DepName 返回一条依赖里指的服务名：「mysql:healthy」取「mysql」。
//
// 条件与名字写在同一个字符串里（`depends_on: [mysql:healthy]`），是为了让
// 「依赖谁」和「等到什么程度」在清单里挨着——拆成两个字段的话，
// 两边对不上时（写了条件却没写是谁）又要多一条校验，而且顺序一对一的保证
// 只能靠人眼守。
func DepName(d string) string {
	name, _, _ := strings.Cut(d, ":")
	return strings.TrimSpace(name)
}

// DepCond 返回一条依赖声明的条件，没写就是空串。
func DepCond(d string) string {
	_, cond, _ := strings.Cut(d, ":")
	return strings.TrimSpace(cond)
}

// DepWaiters 返回那些声明了条件、要等到就绪的前置名字，顺序照清单里写的。
//
// 只等声明了条件的：等待会把启动串起来，而「全部启动」原本是一条流水线——
// 一个起得慢的 Java 服务足以把排在它后面的每一个都压住，那不是用户要的代价。
// 默认只排顺序（见 Service.DependsOn 的说明），要等就明写。
func (s *Service) DepWaiters() []string {
	var out []string
	for _, d := range s.DependsOn {
		if DepCond(d) == CondHealthy {
			out = append(out, DepName(d))
		}
	}
	return out
}

// DepRefs 把一串依赖去掉条件，只留服务名。排序、成环、改名、删除看的是名字，
// 条件那半截对它们没有意义。
func DepRefs(ds []string) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, DepName(d))
	}
	return out
}
