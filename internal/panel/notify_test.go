//go:build !windows

package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/diag"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// notifier 收下这个面板发出去的通知。
type notifier struct {
	mu  sync.Mutex
	got [][2]string // 标题、正文
}

func newNotifier(p *Panel) *notifier {
	n := &notifier{}
	p.SetUserNotify(func(title, body string) {
		n.mu.Lock()
		n.got = append(n.got, [2]string{title, body})
		n.mu.Unlock()
	})
	return n
}

// titled 找出标题里带这个词的那些通知。
func (n *notifier) titled(part string) [][2]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out [][2]string
	for _, g := range n.got {
		if strings.Contains(g[0], part) {
			out = append(out, g)
		}
	}
	return out
}

// deadEntry 造一条「已经不在的那次运行」的状态记录。
func deadEntry(t *testing.T) *proc.Entry {
	t.Helper()
	pid := deadPID(t)
	return &proc.Entry{PID: pid, PGID: pid, StartedAt: time.Now().Add(-time.Minute)}
}

// writeLog 往这个服务的日志里铺一段内容。
func writeLog(t *testing.T, h *harness, name, body string) {
	t.Helper()
	cfg := h.p.Config()
	if err := os.MkdirAll(cfg.LogDirFor(name), 0o755); err != nil {
		t.Fatalf("建日志目录失败：%v", err)
	}
	log := proc.LogStartMarker(name) + " 2026-10-05T10:00:00+08:00\n" + body
	if err := os.WriteFile(cfg.LogPath(name), []byte(log), 0o644); err != nil {
		t.Fatalf("写日志失败：%v", err)
	}
}

// TestCrashSaysItOnce 钉着同一次崩溃只响一声。
//
// 巡检每三秒跑一遍，而「它不在了」这件事只要没被重新拉起来就一直成立。
// 不记账的话，一个崩了、额度也用光了的服务会每三秒弹一条，弹到用户把通知关掉为止——
// 而关掉之后就再也收不到别的了。
func TestCrashSaysItOnce(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"alpha": deadEntry(t)})
	n := newNotifier(h.p)

	h.p.recoverCrashed()
	h.p.recoverCrashed()
	drain(t, h.p)

	if got := n.titled("异常退出"); len(got) != 1 {
		t.Fatalf("发了 %d 条「异常退出」，想要 1 条：%v", len(got), got)
	}
}

// TestCrashSaysAgainAfterRestart 钉着「救回来又崩了要再说一声」。
//
// 去重认的是「哪一次运行」（PID + 启动时刻），不是服务名：一个反复崩的服务每崩
// 一次都是新的一件事，用户得知道它还在崩，而不是以为上次那条之后就没事了。
func TestCrashSaysAgainAfterRestart(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"alpha": deadEntry(t)})
	n := newNotifier(h.p)

	h.p.recoverCrashed()
	// 等第一次重启收尾再铺下一次：动作还在跑的时候巡检不说话（见 enqueueRestart），
	// 那是刻意的——上一件事还没有结果，说什么都是猜。
	drain(t, h.p)
	// 换一次运行：同一个进程号、另一个启动时刻，就是「又崩了一次」。
	dead := deadEntry(t)
	if err := proc.UpdateState(h.p.Config().StatePath(), func(s *proc.State) error {
		s.Services["alpha"] = dead
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
	h.p.recoverCrashed()
	drain(t, h.p)

	if got := n.titled("异常退出"); len(got) != 2 {
		t.Fatalf("发了 %d 条「异常退出」，想要 2 条：%v", len(got), got)
	}
}

// TestCrashWithoutOnFailureStaysSilent 钉着没开自动重启的服务崩了不打扰用户。
//
// 退出码拿不到（进程是 Release 出去的），所以 Pier 分不出「跑完了」和「崩了」——
// 一个跑一遍构建就退出的服务，在状态文件里和崩溃长得一模一样。替用户下结论说
// 「它出事了」，就是在报一次假的警。
func TestCrashWithoutOnFailureStaysSilent(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"beta": deadEntry(t)})
	n := newNotifier(h.p)

	h.p.recoverCrashed()
	drain(t, h.p)

	if got := n.titled("beta"); len(got) != 0 {
		t.Errorf("没开重启策略的 beta 也发了通知：%v", got)
	}
}

// TestRestartLimitSaysSo 钉着「到上限了」这件事要说出来。
//
// 这是三条通知里最要紧的一条：前两条说的是「正在救」，这一条说的是「救不动了，
// 你得自己看一眼」。额度用光时不说，用户看到的就只是服务一直没起来。
func TestRestartLimitSaysSo(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"alpha": deadEntry(t)})
	markRestarts(h, "alpha", restartLimit)
	n := newNotifier(h.p)

	h.p.recoverCrashed()
	drain(t, h.p)

	got := n.titled("异常退出")
	if len(got) != 1 {
		t.Fatalf("发了 %d 条「异常退出」，想要 1 条：%v", len(got), got)
	}
	if !strings.Contains(got[0][1], "上限") {
		t.Errorf("正文 = %q，没说到上限", got[0][1])
	}
}

// TestCrashBodyCarriesTheDiag 钉着通知正文里有那一句原因、那一句下一步和原文。
//
// 只报「异常退出」是句空话：用户手上是一个能跑的日志文件和一个不知道该看哪里的终端。
// 原文里那行字（端口号、模块名）才是「该去改哪一处」的答案。
func TestCrashBodyCarriesTheDiag(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"alpha": deadEntry(t)})
	n := newNotifier(h.p)
	writeLog(t, h, "alpha", "node:internal/modules/cjs/loader:1215\nError: Cannot find module 'express'\n")

	h.p.recoverCrashed()
	drain(t, h.p)

	got := n.titled("异常退出")
	if len(got) != 1 {
		t.Fatalf("发了 %d 条「异常退出」，想要 1 条：%v", len(got), got)
	}
	for _, want := range []string{"依赖没装", "Cannot find module 'express'"} {
		if !strings.Contains(got[0][1], want) {
			t.Errorf("正文里没有 %q：\n%s", want, got[0][1])
		}
	}
}

// TestAutoStartFailureIsReported 钉着「巡检排的那一次没起来」也要说。
//
// 一条链上的后一半：崩了救一次，救的那一次又没起来。前一条通知说「正在救」，
// 这一条说的是「救也没救起来」——而两条通知来自不同的代码路径（巡检那条只看
// 进程在不在，这一条要等启动真的失败），少了一条链路就断在半空。
func TestAutoStartFailureIsReported(t *testing.T) {
	h := restartHarness(t, nil)
	n := newNotifier(h.p)
	svc, _ := h.p.Config().Find("alpha")

	// 手工当一个巡检排的任务丢进去：目录不存在，编译那一步必然失败。
	// 直接调 startOne 而不是走队列，是为了这一条只验「失败了会不会说」。
	op := newOp("start", "running")
	h.p.mu.Lock()
	h.p.ops["alpha"] = op
	h.p.mu.Unlock()
	h.p.startOne(job{kind: "start", svc: svc, op: op, auto: true})

	got := n.titled("启动失败")
	if len(got) != 1 {
		t.Fatalf("发了 %d 条「启动失败」，想要 1 条：%v", len(got), got)
	}
	if !strings.Contains(got[0][0], "alpha") {
		t.Errorf("标题里没说是哪个服务：%q", got[0][0])
	}
}

// TestStartSaidByUserStaysSilent 钉着「用户自己点的那一下不弹通知」。
//
// 他正看着屏幕，界面上那一行已经写着为什么没起来——再弹一条系统通知，
// 就是同一句话在第二个地方又说了一遍。巡检排的那一次没人在看，不说才没人知道。
func TestStartSaidByUserStaysSilent(t *testing.T) {
	h := restartHarness(t, nil)
	n := newNotifier(h.p)
	svc, _ := h.p.Config().Find("alpha")

	op := newOp("start", "running")
	h.p.mu.Lock()
	h.p.ops["alpha"] = op
	h.p.mu.Unlock()
	h.p.startOne(job{kind: "start", svc: svc, op: op, auto: false})

	if got := n.titled("启动失败"); len(got) != 0 {
		t.Errorf("用户点的那一次也发了通知：%v", got)
	}
}

// TestSayStartFailDedupesPerOperation 钉着同一个动作只说一次、下一个动作能再说。
//
// 去重认的是动作（见 opKey），不是服务名：按服务名的话，一个反复起不来的服务
// 从第二次开始就永远安静了，而那恰恰是用户最需要被告知的情形。
func TestSayStartFailDedupesPerOperation(t *testing.T) {
	h := restartHarness(t, nil)
	n := newNotifier(h.p)
	svc, _ := h.p.Config().Find("alpha")

	op := newOp("start", "running")
	h.p.sayStartFail(svc, op, true, diag.Hit{}, false, "服务 alpha 启动失败")
	h.p.sayStartFail(svc, op, true, diag.Hit{}, false, "服务 alpha 启动失败")
	if got := n.titled("启动失败"); len(got) != 1 {
		t.Fatalf("发了 %d 条，想要 1 条：%v", len(got), got)
	}

	h.p.sayStartFail(svc, newOp("start", "running"), true, diag.Hit{}, false, "服务 alpha 启动失败")
	if got := n.titled("启动失败"); len(got) != 2 {
		t.Fatalf("下一个动作没再发：%v", got)
	}
}

// TestStartFailTrimsTheLogPath 钉着正文里不留那句「，详见 <日志路径>」。
//
// 系统通知点不开一条路径，而它常常比前半句还长，正文会被它占满。
// 日志在哪，界面那行的「查看日志」和命令行的提示都写着。
func TestStartFailTrimsTheLogPath(t *testing.T) {
	h := restartHarness(t, nil)
	n := newNotifier(h.p)
	svc, _ := h.p.Config().Find("alpha")
	tail := fmt.Sprintf("服务 alpha 编译失败，详见 %s", filepath.Join(t.TempDir(), "2026-10-05.log"))
	h.p.sayStartFail(svc, newOp("start", "running"), true, diag.Hit{}, false, tail)

	got := n.titled("启动失败")
	if len(got) != 1 {
		t.Fatalf("发了 %d 条：%v", len(got), got)
	}
	if strings.Contains(got[0][1], "详见") {
		t.Errorf("正文里还留着日志路径：%q", got[0][1])
	}
}

// TestCrashWithoutNotifierIsQuiet 钉着「没装通知器时什么都不做」。
//
// 命令行与 pier api 就是这个样子：它们没有地方弹通知，走的是同一个面板。
func TestCrashWithoutNotifierIsQuiet(t *testing.T) {
	h := restartHarness(t, map[string]*proc.Entry{"alpha": deadEntry(t)})
	// 不装通知器，只求它别炸。
	h.p.recoverCrashed()
	drain(t, h.p)
}
