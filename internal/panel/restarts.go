package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 自动重启的记账要落盘。
//
// 这一笔账原本只在内存里（一张 map），于是关掉窗口再打开，一个还在崩溃循环里的
// 服务就重新拥有全部额度，界面上写着「已自动重启 1 次」——而实际已经救过三次。
// 而这笔账要回答的正是「它已经自己救过几次、还要不要继续救」，归零之后这个数字
// 就没有意义了；偏偏「重开一次界面」是人碰上这种事最自然的动作。
//
// 存的只是时刻，不是结论：窗口多长、几次算满，仍然只由 panel 说了算，
// 所以这份文件跨过一次版本升级也不会变成另一种意思——窗口外的记录读回来就丢掉。

// restartsFileName 是记账文件的名字。它落在数据目录里，与 restart.lock 一样：
// 这笔账说的是「这台机器上的 Pier 救过它几次」，与手上这份清单是哪一个无关。
const restartsFileName = "restarts.json"

// restartsFile 是 restarts.json 的形状。
//
// 用「服务名 → 时刻」而不是「服务名 → 次数」：额度是按时间窗口算的（见 restartWindow），
// 存次数就丢掉了「这几次是多久以前的」，而一个服务五分钟前崩了三次，与三天前崩了三次，
// 该不该再救是两回事。
type restartsFile struct {
	Services map[string][]time.Time `json:"services"`
}

// restartsPath 返回记账文件的位置；算不出数据目录时返回空串。
func restartsPath() string {
	d, err := config.Dirs()
	if err != nil {
		return ""
	}
	return filepath.Join(d.Data, restartsFileName)
}

// loadRestarts 读回上一次的记账，窗口外的记录就地丢掉。
//
// 读不动、坏了、格式不对，一律当作「没有记录」：这笔账最多让人多拿到一个额度，
// 而为一个随时可重建的计数器报错（还是在启动的时候）比这件事本身更烦人。
// 与 state.json 的「坏了就当没有」是同一条规矩。
func loadRestarts(now time.Time) map[string][]time.Time {
	out := map[string][]time.Time{}
	path := restartsPath()
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var f restartsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return out
	}
	for name, at := range f.Services {
		if kept := keepRecent(at, now); len(kept) > 0 {
			out[name] = kept
		}
	}
	return out
}

// saveRestarts 把记账写回文件；写不动就算了——下一次记账还会再写一遍，
// 而没有任何一件事等得起它。
func saveRestarts(m map[string][]time.Time) {
	path := restartsPath()
	if path == "" {
		return
	}
	// 空记账不写一份空文件，而是把上一份删掉：删掉一个服务、或者所有记录都过期之后，
	// 留下一份空壳只会让下一个读它的人还要再滤一遍。
	if len(m) == 0 {
		_ = os.Remove(path)
		return
	}
	data, err := json.Marshal(restartsFile{Services: m})
	if err != nil {
		return
	}
	_ = config.WriteAtomic(path, data)
}

// keepRecent 只留下窗口内那几次，并按时间先后排好。
//
// 记账的两个入口（读文件、算额度）共用这一条：窗口的边界只写一遍。
// 文件是外面来的，顺序不能假定；排好之后 restartNote 里那句「第 N 次」才是有序的。
func keepRecent(at []time.Time, now time.Time) []time.Time {
	kept := make([]time.Time, 0, len(at))
	for _, t := range at {
		if now.Sub(t) < restartWindow {
			kept = append(kept, t)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Before(kept[j]) })
	return kept
}
