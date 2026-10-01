package proc

import (
	"fmt"
	"strconv"
	"strings"
)

// Usage 是一组进程合计的资源占用。
type Usage struct {
	// CPU 是这一组进程当前的 CPU 占用合计，单位百分比，单核满载是 100。
	// 与 ps / top 同一个口径，多核机器上合计可以超过 100。
	//
	// 它是「最近一小段时间」的均值，不是「自启动以来的均值」：实测把一个烧满一核的
	// 进程 SIGSTOP 住，ps 报的 %cpu 一秒后掉到 3.5、五秒后归零。这一条决定了采样器
	// 不必保存上一次样本再自己求差——一次 ps 就是一份当下的读数。
	CPU float64
	// MemBytes 是常驻内存（RSS）合计。ps 的 rss 列单位是 KB，在这里就换算成字节，
	// 免得每个消费方各换算一次、各错一次。
	//
	// 各进程的 RSS 相加会重复计入共享的库页，所以它比真实占用偏高，
	// 只适合用来横向比「谁比谁大」，不适合当成「一共吃了多少内存」。
	MemBytes int64
	// Procs 是这一组里的进程数。
	Procs int
}

// add 把一行进程记录并进来。
func (u *Usage) add(cpu float64, rssKB int64) {
	u.CPU += cpu
	u.MemBytes += rssKB * 1024
	u.Procs++
}

// Metrics 是一次资源采样。
//
// 它算的是「一组进程」而不是「一个进程」，这是这里的核心口径：Pier 启动服务时
// 设了 Setsid，整个服务树独占一个进程组（进程组号等于首进程 PID），而记录在案的
// PID 往往是 mvn / pnpm 这类壳，真正吃资源的在孙进程里。只按 PID 读，
// Java 服务会得到一个接近零的假数字。
type Metrics struct {
	// Groups 按进程组号聚合，含本机所有进程组。服务的进程组号记在 state.json 的
	// Entry.PGID 里，调用方拿它来这里取自己关心的那几组。
	Groups map[int]Usage
	// Self 是 root 那棵进程树的合计，已扣掉 exclude 里的子树。
	//
	// 不另算整机合计：界面只关心 Pier 自己和它起的服务。整机的数字要看活动监视器，
	// 在这里摆一份只会让人拿它去和服务的数字比，而两者的口径并不一样
	// （全机 RSS 相加会把共享库重复计入好几百遍，见 Usage.MemBytes）。
	Self Usage
}

// procFields 是采样要的那几列。
//
// 每列都带 `=`：不带的话 ps 会自己加表头，而表头里有 %cpu 这样的名字，
// 解析时就得先判断「这行是不是表头」。带 `=` 直接就没有表头。
const procFields = "pid=,ppid=,pgid=,%cpu=,rss="

// procRow 是 ps 输出里的一行。
type procRow struct {
	pid, ppid, pgid int
	cpu             float64
	rssKB           int64
}

// parseProcRows 解析 ps -axo procFields 的输出。
//
// 逐行按空白切，不按列宽切：命令行里有空格会带偏定宽解析，而这里取的又都是
// 最左边几列数字，按词切最稳。行数不对、数字解析不出来的一律跳过——少一行是
// 少一个进程的数，为它把整次采样作废没必要；反过来，「一行都解析不出来」由调用方
// 当成错误处理，那才是真的出问题了。
func parseProcRows(out []byte) []procRow {
	var rows []procRow
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		cpu, e4 := strconv.ParseFloat(f[3], 64)
		rss, e5 := strconv.ParseInt(f[4], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
			continue
		}
		rows = append(rows, procRow{pid: pid, ppid: ppid, pgid: pgid, cpu: cpu, rssKB: rss})
	}
	return rows
}

// SampleMetrics 采一次全机进程用量（按进程组聚合，外加 root 那棵树）。
//
// root 是要单独核算的那棵进程树的根（Pier 传自己的 PID），exclude 是要从这棵树里
// 摘掉的进程号（Pier 传它正在跑的那些服务）。
//
// 这层摘除不能省。服务确实是 Pier 的子进程（Supervisor.Start 用 cmd.Start 起的），
// 不摘的话「面板自身」就等于「面板 + 所有服务」——而这一项存在的全部意义恰恰是回答
// 「到底是面板在吃资源，还是它起的程序在吃」，数字反了结论就正好相反。
// 摘的是整棵子树：服务的孙进程同样属于那个服务。
//
// 一个已知的边界：macOS 上 WebView 的渲染与网络进程是 launchd 托管的 XPC 服务，
// 父进程是 1、且各自独占会话，既不在 root 这棵树里、也不在 Pier 的进程组里，
// 所以「面板自身」不含它们。界面上的措辞要如实说明，不能让这个数看起来是全部。
func SampleMetrics(root int, exclude map[int]bool) (*Metrics, error) {
	out, err := sysOutput("ps", "-axo", procFields)
	if err != nil {
		return nil, fmt.Errorf("读取进程用量失败：%w", err)
	}
	rows := parseProcRows(out)
	// 一行都解析不出来说明不是「个别行异常」，而是 ps 的用法或输出格式变了。
	// 这时候返回全零的采样比返回错误危险得多：界面上会是一片「0.0%」，
	// 看着像「什么都不占」，与「读不到」完全不是一回事。
	if len(rows) == 0 {
		return nil, fmt.Errorf("ps 没有给出任何可解析的进程记录")
	}

	m := &Metrics{Groups: make(map[int]Usage, len(rows))}
	byPID := make(map[int]procRow, len(rows))
	kids := make(map[int][]int, len(rows))
	for _, r := range rows {
		g := m.Groups[r.pgid]
		g.add(r.cpu, r.rssKB)
		m.Groups[r.pgid] = g
		byPID[r.pid] = r
		kids[r.ppid] = append(kids[r.ppid], r.pid)
	}

	// 从 root 往下走一遍，摘掉 exclude 里的子树。
	//
	// seen 不只是去重：ps 一次输出里的父子关系是自洽的、不会成环，但真拿到异常数据时，
	// 没有它就是一段转不出去的循环。root 自身也在这个集合里，Self 要把它算上。
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, k := range kids[pid] {
			if seen[k] || exclude[k] {
				continue
			}
			seen[k] = true
			queue = append(queue, k)
		}
	}
	for pid := range seen {
		if r, ok := byPID[pid]; ok {
			m.Self.add(r.cpu, r.rssKB)
		}
	}
	return m, nil
}
