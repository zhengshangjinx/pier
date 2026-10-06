package panel

import (
	"sync"
	"time"
)

// 概览那两格下面的曲线：面板顺手攒下来的最近若干个采样点。
//
// 只活在内存里，不落盘：它回答的是「刚才那一下是不是真的飙过」，而跨过一次
// 重启之后，这些点对应的进程已经不在了——留着它，现在这批服务看上去就像一直
// 是那个形状。命令行只做一次快照，用不上也不需要它。
const (
	// historyPoints 是每一条曲线留多少个点。界面空闲时 5 秒刷一次、有动作时
	// 1 秒一次，所以这一条横跨几十秒到几分钟：够看出「刚才飙了一下」，
	// 又不至于把几百个点反复塞进每一次状态推送。
	historyPoints = 60
	// historyGap 是两次采样之间至少隔多久。
	//
	// 一次刷新里 State() 可能被连着调两遍（几处各自触发），不留这个间隔就会
	// 多出一串挤在同一毫秒上的点，曲线的横轴也就不再是时间。取得比忙时的
	// 1 秒略短：那已经是最快的采样节奏，不该因为几百微秒的抖动被砍掉一半。
	historyGap = 800 * time.Millisecond
)

// SampleOut 是曲线上的一个点。
type SampleOut struct {
	// At 是这一点的时刻（Unix 毫秒）。界面按它摆横轴，而不是按数组下标：
	// 采样的间隔本来就不匀，按下标画会把忙的那一分钟拉成一整段。
	At int64 `json:"at"`
	// CPU 是百分比（单核满载 100），MemBytes 是字节，口径与 UsageOut 一致。
	CPU      float64 `json:"cpu"`
	MemBytes int64   `json:"memBytes"`
}

// HistoryOut 是最近若干次采样，按「界面上看的是哪一页」分成几路。
type HistoryOut struct {
	// Services 是全部服务的合计，Self 是 Pier 自身。
	Services []SampleOut `json:"services"`
	Self     []SampleOut `json:"self"`
	// Groups 按分组名各存一份。分组页上那一格叫「本组占用」，它下面的曲线
	// 也得是这个分组的：只存一份合计的话，数字和曲线会各说各话。
	Groups map[string][]SampleOut `json:"groups,omitempty"`
}

// usageHistory 攒着上面那几路点。
//
// 自带一把锁，不共用 p.mu：这是「顺手记一笔」的旁路数据，记的时候不该和
// 启停、清单加载那些事排在一条队上。
type usageHistory struct {
	mu     sync.Mutex
	last   time.Time
	all    []SampleOut
	self   []SampleOut
	groups map[string][]SampleOut
}

// record 从一份刚算好的快照里记一笔。
//
// 取的是成品里的数，而不是在别处重算一遍：曲线上最新的那一点必须与格子里
// 那个数字出自同一次采样，否则两者摆在一起会互相打架。
func (h *usageHistory) record(out StateOut, now time.Time) {
	// 读不到用量的那几次不记。那时上面的数字全是 0，记下来就是曲线上一段掉到
	// 底的谷——而那不叫「没人占」，叫「没读着」，两者在图上长得一模一样。
	if !out.OK || out.MetricsErr != "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// 太密就不记：横轴是时间，一串挤在同一毫秒上的点会让它失真。
	if !h.last.IsZero() && now.Sub(h.last) < historyGap {
		return
	}
	h.last = now

	at := now.UnixMilli()
	pt := func(u UsageOut) SampleOut {
		return SampleOut{At: at, CPU: u.CPU, MemBytes: u.MemBytes}
	}
	h.all = pushSample(h.all, pt(out.Usage))
	h.self = pushSample(h.self, pt(out.Self))

	// 每次重新建一份，顺带丢掉已经不在了的分组（删掉的、改了名的）：
	// 留着的话，下一次出现同名的分组时，那条线会接着一段与它无关的历史。
	groups := make(map[string][]SampleOut, len(out.Groups))
	for _, g := range out.Groups {
		groups[g.Name] = pushSample(h.groups[g.Name], pt(g.Usage))
	}
	h.groups = groups
}

// snapshot 给出一份可以直接摆进 StateOut 的副本。
//
// 复制而不是把切片本身交出去：交出去的这一份会被序列化，而这里下一拍还会往
// 同一个数组里写。几十个点复制一份不值得省。
func (h *usageHistory) snapshot() *HistoryOut {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := &HistoryOut{Services: cloneSamples(h.all), Self: cloneSamples(h.self)}
	if len(h.groups) > 0 {
		out.Groups = make(map[string][]SampleOut, len(h.groups))
		for name, s := range h.groups {
			out.Groups[name] = cloneSamples(s)
		}
	}
	return out
}

// pushSample 追加一个点，满了就整体往前挪一格。
//
// 挪而不是重新分配：留下的点数是写死的几十个，一次 memmove 比每次采样
// 都扔掉旧数组便宜，占的内存也就一直是一份。
func pushSample(s []SampleOut, p SampleOut) []SampleOut {
	if len(s) == historyPoints {
		copy(s, s[1:])
		s[len(s)-1] = p
		return s
	}
	return append(s, p)
}

func cloneSamples(s []SampleOut) []SampleOut {
	if len(s) == 0 {
		return nil
	}
	return append([]SampleOut(nil), s...)
}
