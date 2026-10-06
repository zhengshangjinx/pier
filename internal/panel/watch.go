package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/watch"
)

// 文件监视：清单里给服务写了 watch，就盯着它自己的目录，改动之后重跑一次。
//
// 只在持有自动重启独占权的那个宿主里做（与崩溃自愈同一个门禁，见 ensureRestartLock）：
// 两个窗口各盯各的，一次改动会重起两次——第二次撞上第一次刚拉起来的进程，
// 用户在界面上看到的是服务在抽风。
const (
	// watchPoll 是扫一遍文件树的间隔。比 restartPoll 密得多：崩溃晚几秒知道没关系，
	// 而「改完等它重启」这件事慢一拍就有人盯着屏幕等。
	watchPoll = 500 * time.Millisecond
	// watchSettle 是「最后一次看到变化」之后要静多久才动手。
	//
	// 编辑器保存一个大文件不是一次写完的（先清空再写、或者写临时文件再改名），
	// 见到第一个字节就重启的话，起来的进程读到的是一份写了一半的源码。
	// 让变化先落定，代价是慢半拍——而慢半拍比编译一半的源码好解释得多。
	watchSettle = 400 * time.Millisecond
)

// restartRounds 是崩溃巡检隔几轮扫一次：那条路本来就慢（3 秒一轮），
// 与文件监视共用一条协程之后按轮数降频，不必为它再开一条。
const restartRounds = int(restartPoll / watchPoll)

// watchState 是一个服务此刻的监视状态。
type watchState struct {
	// key 是「这棵树是按什么建的」：目录或模式变了就得重来（旧的树盯着的
	// 已经不是这个服务了）。重建的第一趟只建立基线，不会触发重启。
	key  string
	tree *watch.Tree
	// pending 是上一次看到变化的那些文件，seen 是看到它们的时刻。
	// 两者一起构成那个「等它静下来」的窗口：下一趟没有新变化、又过了
	// watchSettle，才真的动手（见 checkOne）。
	pending []string
	seen    time.Time
}

// patrol 是持有独占权时才跑的那条巡检：查崩溃（每 restartPoll 一轮）、
// 盯文件（每一轮）。
//
// 两个节奏合在一条协程里，是为了让「谁持有独占权」只有一处判断：
// 分成两条的话，那两条会各自去抢同一把锁，抢到的次序不定，而它们要做的事
// 都建立在「这台机器上只有我在巡检」之上。
func (p *Panel) patrol() {
	t := time.NewTicker(watchPoll)
	defer t.Stop()
	round := 0
	for {
		select {
		case <-p.done:
			return
		case now := <-t.C:
			// 独占权每轮重问一次：持有它的那个窗口退出之后要能接过去。
			// 抢不到的这一轮什么都不做——另一个窗口正在巡检同一批服务。
			if !p.ensureRestartLock() {
				continue
			}
			if round%restartRounds == 0 {
				p.recoverCrashed()
			}
			round++
			if cfg := p.Config(); cfg != nil {
				p.checkWatch(cfg, now)
			}
		}
	}
}

// checkWatch 扫一遍配了监视的那几个服务，谁该重启就排一个进去。
func (p *Panel) checkWatch(cfg *config.Config, now time.Time) {
	live := make(map[string]bool, len(cfg.Services))
	for _, svc := range cfg.Services {
		if !svc.Watch.On {
			continue
		}
		live[svc.Name] = true
		p.checkOne(cfg, svc, now)
	}
	// 换过清单之后，被删掉或改回不监视的那些服务留着的树要丢掉：
	// 留着不会出错（下一轮不会再看它），但它盯着的目录可能已经不在了，
	// 而这份状态里的 map 会一直跟着进程。
	for name := range p.watching {
		if !live[name] {
			delete(p.watching, name)
		}
	}
}

func (p *Panel) checkOne(cfg *config.Config, svc *config.Service, now time.Time) {
	patterns := svc.WatchPatterns()
	key := svc.AbsDir() + "\x00" + strings.Join(patterns, "\x00")
	st := p.watching[svc.Name]
	if st == nil || st.key != key {
		st = &watchState{key: key, tree: watch.New(svc.AbsDir(), patterns, watchOutside(cfg))}
		p.watching[svc.Name] = st
	}

	changed, err := st.tree.Scan()
	if err != nil {
		return
	}
	if len(changed) > 0 {
		st.pending = changed
		st.seen = now
		return
	}
	if len(st.pending) == 0 || now.Sub(st.seen) < watchSettle {
		return
	}
	files := st.pending
	st.pending = nil
	p.watchRestart(cfg, svc, files)
}

// watchOutside 是永远不看的那几棵树：Pier 自己的日志与编译产物。
//
// 两者都在服务启动时被写（日志每次都在追加、go 的产物每次都重新落一遍），
// 而它们就在这台机器上、离被监视的目录只差一个数据目录。不排掉的话，
// 一个配了 watch 的服务会自己重启自己，一直转下去。
func watchOutside(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range []string{cfg.LogDir(), cfg.BinDir(), cfg.RuntimeDir()} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// watchRestart 排一次「因为文件变了」的重启。
//
// 与 enqueueRestart（崩溃自愈）刻意分开：那一条带额度记账（几分钟内最多几次），
// 而改代码改到第五次是常有的事——记在同一个账本里的话，改几行代码就把额度用光，
// 之后服务真崩了反倒不救了。这两件事的「太频繁」是两回事。
func (p *Panel) watchRestart(cfg *config.Config, svc *config.Service, files []string) {
	// 没在跑的服务不因为文件变了就自己起来：用户没让它跑，而改文件不等于
	// 「请把这个服务拉起来」。正在启动 / 正在停止的也不动（下面的 ops 检查）。
	state, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		return
	}
	if e, ok := state.Services[svc.Name]; !ok || !proc.EntryAlive(e) {
		return
	}

	p.mu.Lock()
	if op := p.ops[svc.Name]; op != nil && op.phase != "error" {
		// 身上已经有动作了：用户正动它，或者上一次自动重启还没完。
		// 这一趟让过去，等它有个结果——再排一个只会把刚排的那个顶掉。
		p.mu.Unlock()
		return
	}
	op := newOp("restart", "queued")
	p.ops[svc.Name] = op
	p.mu.Unlock()

	p.noteWatch(cfg, svc, files)
	// 端口接着上一次那个（见 resumePort）：当初是因为清单里那个被占着才让路的，
	// 而那次占用不会因为改了几行代码就走掉。
	p.jobs <- job{kind: "restart", svc: withPort(svc, p.resumePort(svc.Name)), op: op, auto: true}
	p.fireNotify()
}

// noteWatch 往这个服务今天的日志里记一行「为什么重启了」。
//
// 想重启的这一刻就写，不等重启完：重启会重写一份日志头，事后追记的那一行
// 会落在新的一次运行里，看起来像是它自己说的。
//
// 写不进去就什么都不做：这只是给人留的一条线索，不是这次重启的一部分。
//
// 目录不在就顺手建出来：服务跑着的时候它本来是在的，但「清空日志」会把整个
// 服务目录删掉——那时服务还在跑，重启也照旧，只是这一行会落到一个没有目录的路径上，
// 于是此后每一次自动重启都不留痕迹。
func (p *Panel) noteWatch(cfg *config.Config, svc *config.Service, files []string) {
	path := cfg.LogPath(svc.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "--- 文件有改动，自动重启（%s）\n", summarizeFiles(files))
}

// summarizeFiles 把变化的那几个文件说成一句人话。
//
// 只列前几个：一次重命名目录会带上几十个文件，全列出来会把日志头淹掉，
// 而这张单子的用处只是「是不是我要改的那个文件」。
func summarizeFiles(files []string) string {
	const max = 3
	if len(files) <= max {
		return strings.Join(files, "、")
	}
	return fmt.Sprintf("%s 等 %d 个", strings.Join(files[:max], "、"), len(files))
}
