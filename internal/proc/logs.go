package proc

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 日志的存放与清理。
//
// 布局是 logs/<服务名>/<年-月-日>.log，一天一份（见 config.LogPathOn）。
// 这里管的是这份布局的三件事：找出此刻该读哪一份、统计占了多少、按天数清理。
//
// 为什么清理要按天数而不是按大小：按大小的滚动（保留最后 N 兆）会把
// 「三天前的报错」和「刚才的报错」一起截掉，而排查时缺的恰恰是前者；
// 按天数则是「最近两周的都留着」，取舍发生在「多久以前」这个维度上，
// 和人的记忆对得上。

// LogKeepDays 是日志默认保留的天数。超期不用等用户动手：启动服务时顺手清一次
// （只清这个服务的），设置页里另有一个「清理全部超期日志」的入口。
//
// 两周是「够回溯一周前那次失败，又不至于让日志目录无限长」的经验值。
const LogKeepDays = 14

// LogServiceOut 是一个服务的日志占用，供「日志」设置页按服务列出。
type LogServiceOut struct {
	Name string `json:"name"`
	// Bytes 与 Files 是这个服务所有日志文件加起来的。
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
	// Oldest / Newest 是它覆盖的日期区间（取自文件名），没有日志时为空。
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

// LogUsageOut 是日志目录的整体占用。
type LogUsageOut struct {
	OK    bool   `json:"ok"`
	Dir   string `json:"dir"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// KeepDays 由后端给出而不是让界面写死：界面上那句「保留最近 N 天」
	// 必须和真正在清理时用的天数出自同一个数。
	KeepDays int `json:"keepDays"`
	// Services 按占用从大到小排。要清理的时候，先看的就是最大的那几个。
	Services []LogServiceOut `json:"services"`
}

// LogCleanOut 是一次清理的结果。数字都来自真正删掉的那些文件，
// 不是删之前的估算——「释放了 0 B」这种话必须是真的。
type LogCleanOut struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// LatestLog 在服务的日志目录里找出最新的那份日志，返回路径；一份都没有时返回空串。
//
// 按文件名比较而不是修改时间：文件名就是日期，而布局是「年-月-日」，
// 字典序恰好等于时间序。跑着的服务可能还在写更早的那份（跨零点不换文件，
// 见 config.LogPathOn），所以「最新的一份」才是它此刻真正在写的那个。
func LatestLog(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(dir, best)
}

// LogFile 返回服务此刻该读的那份日志：最新的一份；从没启动过时返回今天那份的
// 路径——文件还不存在，但「它会在哪」是个有意义的答案，界面拿它去访达里定位。
func LogFile(cfg *config.Config, name string) string {
	if latest := LatestLog(cfg.LogDirFor(name)); latest != "" {
		return latest
	}
	return cfg.LogPath(name)
}

// LogDates 列出这个服务的日志目录里有哪几天，从新到旧。
//
// 抽屉里的日期选择要的就是它：让人在真正有日志的那些天之间挑，
// 而不是丢一张日历让他一天天去试哪天有东西。
func LogDates(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 一份日志都没有就是这个样子，不是错误。
		return []string{}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if d, ok := logDate(e.Name()); ok {
			out = append(out, d)
		}
	}
	// 文件名是「年-月-日」，字典序就是时间序（见 LatestLog），倒过来即从新到旧。
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// LogStartMarker 是每次启动写进日志的分隔行前缀（见 Supervisor.StartContext）。
//
// 同一天里重启多次会往同一份文件追加，所以「本次运行的输出从哪开始」要靠它认。
// 写在 proc 里给两边共用：写的一方改了格式，读的一方若还在找旧的那串，
// 界面就会把上一次运行的日志一起显示出来——正是分天之前那种误导。
func LogStartMarker(name string) string {
	return "=== " + name + " 启动于"
}

// TrimToLastRun 把日志内容截到「最后一次启动」之后。
//
// 文件里可能有今天早先几次运行的输出（同一天追加到同一份）。当前这次只写了三行时，
// 上面那些旧内容会一起显示出来，读的人很容易把它当成这次的失败原因——
// 分天之前每次启动都清空文件，正是为了避免这个。分天之后清不得（会把今天的记录
// 一起清掉），改从这里截。找不到标记（老文件、手工放进去的文件）就原样返回。
func TrimToLastRun(text, name string) string {
	marker := LogStartMarker(name)
	if i := strings.LastIndex(text, marker); i >= 0 {
		return text[i:]
	}
	return text
}

// LogUsage 统计整棵日志树的占用，按服务分组。
func LogUsage(root string, keepDays int, now time.Time) LogUsageOut {
	out := LogUsageOut{OK: true, Dir: root, KeepDays: keepDays}
	entries, err := os.ReadDir(root)
	if err != nil {
		// 目录还不存在就是一份日志都没有，不是错误：没启动过任何服务就是这样。
		out.Services = []LogServiceOut{}
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		svc := LogServiceOut{Name: e.Name()}
		dir := filepath.Join(root, e.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			svc.Bytes += fi.Size()
			svc.Files++
			// 文件名里的日期就是这份日志覆盖的那天。取不到就跳过——
			// 区间只是给人一个「这一摊有多旧」的直觉，不值得为它猜。
			if d, ok := logDate(f.Name()); ok {
				if svc.Oldest == "" || d < svc.Oldest {
					svc.Oldest = d
				}
				if d > svc.Newest {
					svc.Newest = d
				}
			}
		}
		out.Bytes += svc.Bytes
		out.Files += svc.Files
		out.Services = append(out.Services, svc)
	}
	sort.SliceStable(out.Services, func(i, j int) bool {
		if out.Services[i].Bytes != out.Services[j].Bytes {
			return out.Services[i].Bytes > out.Services[j].Bytes
		}
		return out.Services[i].Name < out.Services[j].Name
	})
	return out
}

// PruneLogs 删掉超过 keepDays 天的日志文件，返回删掉的文件数与释放的字节数。
// name 为空表示所有服务，否则只清这一个；skip 里的服务（正在跑或正在启动的）
// 一律不动，理由见 Panel.busyNames。
//
// 清理是尽力而为：读不了、删不掉的条目一律跳过，不让它挡住启动流程。
func PruneLogs(root, name string, skip map[string]bool, keepDays int, now time.Time) LogCleanOut {
	var out LogCleanOut
	if keepDays <= 0 || skip[name] {
		return out
	}
	cutoff := pruneCutoff(now, keepDays)
	_ = filepath.WalkDir(pruneBase(root, name), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || skip[serviceOf(root, path)] {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !logExpired(d.Name(), fi.ModTime(), cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return nil
		}
		out.Files++
		out.Bytes += fi.Size()
		return nil
	})
	dropEmptyDirs(root)
	return out
}

// ClearLogs 清空日志：name 为空时清掉整棵日志树，否则只清一个服务。
// skip 里的服务不动，同 PruneLogs。
//
// 与 PruneLogs 的区别是它不看日期，说清就清干净——留给「日志目录已经很大了，
// 现在就腾出来」这种时候用。删完把空的子目录一并收掉，免得留下一个空壳服务名。
func ClearLogs(root, name string, skip map[string]bool) LogCleanOut {
	var out LogCleanOut
	targets := []string{}
	if name != "" {
		if skip[name] {
			return out
		}
		targets = append(targets, filepath.Join(root, name))
	} else {
		entries, err := os.ReadDir(root)
		if err != nil {
			return out
		}
		for _, e := range entries {
			if skip[e.Name()] {
				continue
			}
			targets = append(targets, filepath.Join(root, e.Name()))
		}
	}
	for _, t := range targets {
		out.Files, out.Bytes = removeTree(t, out.Files, out.Bytes)
	}
	return out
}

// removeTree 统计并删掉一棵子树，返回累计的文件数与字节数。
//
// 报的是「删前减删后」而不是「删前」：RemoveAll 删一半失败（权限、占用）
// 是很常见的，那两个数必须跟着实际情况缩水——「释放了 12 MB」而实际只少了 3 MB，
// 比不报还糟。
func removeTree(path string, files int, bytes int64) (int, int64) {
	before, beforeBytes := treeSize(path)
	_ = os.RemoveAll(path)
	after, afterBytes := treeSize(path)
	return files + before - after, bytes + beforeBytes - afterBytes
}

// treeSize 数一棵子树里有几个文件、一共多少字节；路径不存在就是 0。
func treeSize(path string) (int, int64) {
	n := 0
	var size int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		n++
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
		}
		return nil
	})
	return n, size
}

// serviceOf 从日志文件的路径里取出它属于哪个服务：root 下面的第一层目录名。
// 直接摊在 root 下的文件取文件名，它不属于任何一个服务。
func serviceOf(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return rel
}

// pruneBase 返回清理的起点：指定了服务就只清它那个目录。
func pruneBase(root, name string) string {
	if name == "" {
		return root
	}
	return filepath.Join(root, name)
}

// logDate 从 <年-月-日>.log 里取出那一天，返回给的是同格式的字符串。
func logDate(name string) (string, bool) {
	base, ok := strings.CutSuffix(name, ".log")
	if !ok {
		return "", false
	}
	if _, err := time.Parse(config.LogDateLayout, base); err != nil {
		return "", false
	}
	return base, true
}

// pruneCutoff 返回保留期的截止日，结果是一个「哪一天」的字符串。
//
// 按天算而不是按「此刻往前推 N×24 小时」算：文件名里只有日期，拿 now 的
// 时分秒去比，会让「正好第 N 天」的那份在当天的某个时刻突然消失——
// 同一个文件删不删取决于几点跑的清理，这不是条能讲清楚的规则。
//
// 返回字符串而不是时间，是为了绕开时区：time.Parse 解出来的日期是 UTC 午夜，
// 拿它去和本地时间的 cutoff 比，同一份日志在 UTC+8 和 UTC-5 的机器上会得到
// 相反的结论。文件名是「年-月-日」，字典序恰好等于时间序，比字符串就没有
// 时区这回事了（LatestLog 用的也是这个性质）。
func pruneCutoff(now time.Time, keepDays int) string {
	return now.AddDate(0, 0, -keepDays).Format(config.LogDateLayout)
}

// logExpired 判断一份日志是否已经超出保留期。
//
// 过期与否看文件名里的日期，而不是文件的修改时间：mtime 会被拷贝、备份、
// 解压这些与内容无关的动作改掉，而文件名里的日期是这份日志自己的属性。
// 认不出日期名（比如手工放进去的文件）才退回 mtime，同样只取到「哪一天」。
func logExpired(name string, mod time.Time, cutoff string) bool {
	if d, ok := logDate(name); ok {
		return d < cutoff
	}
	return mod.Format(config.LogDateLayout) < cutoff
}

// dropEmptyDirs 收掉日志根目录下已经空掉的服务目录。
// 用 os.Remove 而不是 RemoveAll：它删不掉非空目录，这一条正好当作保护。
func dropEmptyDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = os.Remove(filepath.Join(root, e.Name()))
		}
	}
}
