package panel

import (
	"testing"
	"time"
)

// snap 造一份「采到了数」的快照，用量按传入的序号区分，方便看出留下的是哪几个点。
func snap(i int) StateOut {
	return StateOut{
		OK:     true,
		Usage:  UsageOut{CPU: float64(i), MemBytes: int64(i) * 1024},
		Self:   UsageOut{CPU: 1, MemBytes: 2048},
		Groups: []GroupOut{{Name: "组一", Usage: UsageOut{CPU: float64(i) * 2}}},
	}
}

// TestHistoryKeepsTheNewestPoints 钉住「只留最近几十个点」这件事本身。
//
// 曲线是给「刚才飙了一下」用的，攒得越久越没用；而且它每拍都跟着状态推送
// 走一遍，留多少直接决定每次推送要序列化多少字节。
func TestHistoryKeepsTheNewestPoints(t *testing.T) {
	var h usageHistory
	base := time.Now()
	for i := 0; i < historyPoints+10; i++ {
		h.record(snap(i), base.Add(time.Duration(i)*time.Second))
	}

	got := h.snapshot()
	if len(got.Services) != historyPoints {
		t.Fatalf("应当只留 %d 个点，实际 %d 个", historyPoints, len(got.Services))
	}
	// 砍掉的是最早的那几个，留下的是序号 10 起的那一段。
	if got.Services[0].CPU != 10 {
		t.Errorf("留下的第一个点应当是第 10 个（CPU 10），实际 %v", got.Services[0].CPU)
	}
	if last := got.Services[len(got.Services)-1]; last.CPU != float64(historyPoints+9) {
		t.Errorf("最后一个点应当是刚记下的那个，实际 CPU %v", last.CPU)
	}
	if last := got.Services[len(got.Services)-1]; last.At != base.Add(time.Duration(historyPoints+9)*time.Second).UnixMilli() {
		t.Errorf("每个点都要带上自己的时刻，实际 %d", last.At)
	}
	if len(got.Self) != historyPoints {
		t.Errorf("自身那一路也要同样地砍，实际 %d 个", len(got.Self))
	}
	if len(got.Groups["组一"]) != historyPoints {
		t.Errorf("分组那一路也要同样地砍，实际 %d 个", len(got.Groups["组一"]))
	}
	if got.Groups["组一"][0].CPU != 20 {
		t.Errorf("分组用量存的是组内合计，实际 %v", got.Groups["组一"][0].CPU)
	}
}

// TestHistorySkipsCrowdedAndUnreadableSamples 钉住「哪些采样不进曲线」。
//
// 两条各有各的理由，而且都会静静地画出一条错的线：挤在同一毫秒上的点会把
// 横轴拉花，读不到用量的那几次会在曲线上留下一段假谷。
func TestHistorySkipsCrowdedAndUnreadableSamples(t *testing.T) {
	var h usageHistory
	base := time.Now()
	h.record(snap(1), base)

	// 同一拍里被连着调了第二次：不再记。
	h.record(snap(2), base.Add(historyGap/2))
	if got := h.snapshot(); len(got.Services) != 1 {
		t.Fatalf("挨得太近的第二次采样不该进曲线，实际 %d 个点", len(got.Services))
	}
	// 隔够了就记。
	h.record(snap(3), base.Add(historyGap))
	if got := h.snapshot(); len(got.Services) != 2 {
		t.Fatalf("隔够了就该记，实际 %d 个点", len(got.Services))
	}

	// 读不到用量：这时上面的数字全是 0，记下来是一条假的掉底。
	h.record(StateOut{OK: true, MetricsErr: "ps 不在", Usage: UsageOut{}}, base.Add(time.Minute))
	// 清单都没加载好：连服务清单都没有，谈不上是谁在占。
	h.record(StateOut{OK: false, Error: "清单没加载"}, base.Add(2*time.Minute))

	got := h.snapshot()
	if len(got.Services) != 2 {
		t.Fatalf("这两种都该跳过，实际 %d 个点", len(got.Services))
	}
	if got.Services[1].CPU != 3 {
		t.Errorf("跳过之后最后一个点还是上一次成功采到的那个，实际 %v", got.Services[1].CPU)
	}
}

// TestHistoryDropsGoneGroups 钉住分组改名 / 删掉之后那段曲线跟着走。
//
// 留着它不会报错，只会在下次出现同名分组时把两段无关的历史接在一起——
// 看上去像那个分组一直在跑，而中间那段根本不是它。
func TestHistoryDropsGoneGroups(t *testing.T) {
	var h usageHistory
	base := time.Now()
	one := StateOut{OK: true, Groups: []GroupOut{{Name: "组一"}, {Name: "组二"}}}
	h.record(one, base)
	h.record(StateOut{OK: true, Groups: []GroupOut{{Name: "组一"}}}, base.Add(time.Minute))

	got := h.snapshot()
	if _, ok := got.Groups["组二"]; ok {
		t.Error("清单里已经没有这个分组了，它那段曲线也该丢掉")
	}
	if len(got.Groups["组一"]) != 2 {
		t.Errorf("还在的分组要接着记，实际 %d 个点", len(got.Groups["组一"]))
	}
}
