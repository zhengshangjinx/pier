//go:build !windows

// 这一份真的拉起替身进程（拿 sleep 当替身），也真的占住端口。
//
// 「清单里那个端口被别人占着」是这一切的前提，必须拿一个真在监听的 socket 来造：
// 换端口起的两处判断（挑一个空闲的、确认清单里那个还被占着）读的都是真实的
// 监听表，编几个号码出来测不到它们。与 panel_test.go 同一套写法。

package panel

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

// portYAML 里 alpha 写着一个端口、beta 没有。
//
// 两者的区别正是「换一个端口起」的第一个岔路口：没有端口就没有可换的，
// 这时该说的是「先在表单里填一个」，而不是随便挑一个塞给它。
const portYAML = `
services:
  - name: alpha
    dir: .
    kind: shell
    run: sleep 300
    port: %d
  - name: beta
    dir: .
    kind: shell
    run: sleep 300
`

// 端口为什么不用 :0 让内核发一个。
//
// 这一段用例对端口有两个别处没有的要求：「清单里那个 +1」得是空的（StartOnPort
// 从那儿往上找让路端口，再做一次占用确认），以及「放开之后它得一直是空的」
// （resumePort 当场要读到 0）。内核发的号落在临时端口段里（macOS 从 49152 起、
// Linux 默认从 32768 起），本机随便谁随手一次 :0——包括并行跑着的其它用例——
// 就能把这两条同时毁掉，而拿走的那个端口与被测的事毫无关系。
// 所以这一段取在临时端口段以下，内核不往这儿发。
const (
	portBandFirst = 20000
	portBandLimit = 30000
	portBandSize  = 100
)

// portBand 划出这个测试进程专属的一段端口（[portBandBase, +portBandSize)）。
//
// 段头那个端口一直绑着不放：它就是这一段的占位，谁先绑到归谁，并行跑着的另一个
// 测试进程随即往下一段去。留在这个变量里而不是就地丢掉，是因为 net 的 fd 上有终结器
// ——没人引用的 Listener 会被 GC 关掉，那一段就跟着回到「空闲」。
//
// 不划段、让各个进程都从同一号往上扫，就回到那个问题上：谁抢到哪一号是不定的，
// 我们放开清单里那个端口的一瞬间，正在往下扫的另一个人正好把它绑走，resumePort
// 随即读到「还占着」——用例红在一件与被测逻辑无关的事上。并行压测里几个
// panel.test 全挤在同一串号上是常态。
var (
	portBandOnce   sync.Once
	portBandAnchor net.Listener
	portBandBase   int
)

func portBand(t *testing.T) int {
	t.Helper()
	portBandOnce.Do(func() {
		for base := portBandFirst; base+portBandSize <= portBandLimit; base += portBandSize {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base))
			if err != nil {
				continue
			}
			portBandAnchor, portBandBase = ln, base
			return
		}
	})
	if portBandAnchor == nil {
		t.Fatalf("%d 起找不到一段空闲端口", portBandFirst)
	}
	return portBandBase
}

// holdPort 真的占住一个端口，返回它的号码与一个提前放开它的函数（用例结束时
// 也会再放一次，重复关闭无妨）。
//
// 放开这一步要能提前做，是因为「占用的进程走了之后该回到清单里那个端口」
// 正是要验的一半——那一条只能在占用消失之后跑。
//
// 只在自己那一段里找：段内没有别的进程会来看（见 portBand），拿到手的几个号
// 也就一直是我们几个。
func holdPort(t *testing.T) (int, func()) {
	t.Helper()
	base := portBand(t)
	for port := base + 1; port < base+portBandSize; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		release := func() { _ = ln.Close() }
		t.Cleanup(release)
		return port, release
	}
	t.Fatalf("%d 这一段里找不到一个能绑的端口", base)
	return 0, nil
}

// freePort 要一个此刻没人听的端口号。
//
// 拿到手就立刻放掉，所以它随时可能被别的进程占去——这一份用例只用它当一个
// 「和清单里那个不一样」的号码，不拿它去启动任何东西。
func freePort(t *testing.T) int {
	t.Helper()
	p, release := holdPort(t)
	release()
	return p
}

// portHarness 起一个面板，alpha 的清单端口是 manifest。
func portHarness(t *testing.T, manifest int) *harness {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, config.DefaultConfigName)
	if err := os.WriteFile(base, []byte(fmt.Sprintf(portYAML, manifest)), 0o644); err != nil {
		t.Fatalf("写清单失败：%v", err)
	}
	p := newOwnedPanel(t)
	if err := p.Load(base); err != nil {
		t.Fatalf("加载清单失败：%v", err)
	}
	return &harness{t: t, dir: dir, base: base, p: p}
}

// putEntry 往状态文件里放一条 alpha 的记录，e 传 nil 表示状态文件里空着。
func putEntry(t *testing.T, h *harness, e *proc.Entry) {
	t.Helper()
	if err := proc.UpdateState(h.p.Config().StatePath(), func(s *proc.State) error {
		s.Services = map[string]*proc.Entry{}
		if e != nil {
			s.Services["alpha"] = e
		}
		return nil
	}); err != nil {
		t.Fatalf("写状态文件失败：%v", err)
	}
}

// entryOf 读出状态文件里 alpha 那一条，没有就返回 nil。
func entryOf(t *testing.T, h *harness) *proc.Entry {
	t.Helper()
	state, err := proc.LoadState(h.p.Config().StatePath())
	if err != nil {
		t.Fatalf("读状态文件失败：%v", err)
	}
	return state.Services["alpha"]
}

// TestStartOnPortRefusesWhatItCannotSwap 钉住四个「不行」，以及它们都在入队之前。
//
// 这些话是用户点下去的那一刻就要看到的：挑端口、确认占用都当场做完，
// 而不是排完队、等编译都跑完了才失败。
func TestStartOnPortRefusesWhatItCannotSwap(t *testing.T) {
	manifest, _ := holdPort(t)
	taken, _ := holdPort(t)
	h := portHarness(t, manifest)

	cases := []struct {
		name string
		svc  string
		port int
		want string
	}{
		{"服务名字不认识", "根本没有这个服务", 0, "没有名为"},
		{"清单里没配端口", "beta", 0, "没有配端口"},
		{"换的还是清单里那一个", "alpha", manifest, "不必换"},
		{"换到别人正占着的端口上", "alpha", taken, "已经被占用"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.p.StartOnPort(c.svc, c.port)
			if err == nil {
				t.Fatal("应当被拒绝")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误 = %q，应当说到 %q", err, c.want)
			}
			if st := h.p.State(); st.BusyCount != 0 {
				t.Errorf("被拒之后还留下了 %d 个排队动作", st.BusyCount)
			}
		})
	}
}

// TestStartOnPortRunsOnAnotherPort 是这一条的主线：清单里那个端口被占着，
// 换一个端口起，运行记录里是换后的那个，界面上两个值都摆得出来。
func TestStartOnPortRunsOnAnotherPort(t *testing.T) {
	manifest, _ := holdPort(t)
	h := portHarness(t, manifest)
	t.Cleanup(func() {
		_, _ = h.p.Stop("alpha")
		drain(t, h.p)
	})

	// 先走一遍不换的那条路：这个端口被占着，它起不来。这正是用户会去点
	// 「换一个端口起」的那一刻。
	if _, err := h.p.Start("alpha"); err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	drain(t, h.p)
	if e := entryOf(t, h); e != nil {
		t.Fatalf("端口被占着，不该真的起来，记录里却有 %+v", e)
	}

	if _, err := h.p.StartOnPort("alpha", 0); err != nil {
		t.Fatalf("换一个端口起失败：%v", err)
	}
	drain(t, h.p)

	e := entryOf(t, h)
	if e == nil {
		t.Fatal("换过端口之后状态里应当有这条记录")
	}
	if e.Port <= 0 || e.Port == manifest {
		t.Fatalf("记录里的端口 = %d，想要换过之后的（清单里是 %d）", e.Port, manifest)
	}
	swapped := e.Port

	// 界面上要能同时看见两个值，以及那句解释它们为什么对不上。
	s := h.find(t, "alpha")
	if s.Port != manifest {
		t.Errorf("ServiceOut.Port = %d，想要清单里那个 %d——编辑表单预填的是它，改掉就会被写回清单", s.Port, manifest)
	}
	if s.RunPort != swapped {
		t.Errorf("ServiceOut.RunPort = %d，想要这次在用的 %d", s.RunPort, swapped)
	}
	want := fmt.Sprintf("清单里写的是 %d，这次用的是 %d", manifest, swapped)
	if s.PortNote != want {
		t.Errorf("PortNote = %q，想要 %q", s.PortNote, want)
	}

	// 重启接着在这个端口上：让路的那个原因（清单里那个还占着）还在，
	// 换过一次又不接着用，下一次重启就白换一次。
	if _, err := h.p.Restart("alpha"); err != nil {
		t.Fatalf("重启失败：%v", err)
	}
	drain(t, h.p)
	e = entryOf(t, h)
	if e == nil {
		t.Fatal("重启之后记录不该消失")
	}
	if e.Port != swapped {
		t.Errorf("重启后的端口 = %d，想要接着用 %d（清单里那个还占着）", e.Port, swapped)
	}
}

// TestResumePortOnlyWhileTheManifestPortIsStillTaken 钉住「换过的端口不是永久选择」。
//
// 让路的原因只有一个：清单里那个端口被别人占着。占着的就接着用换后的那个，
// 已经不占了就正好回来——不按「现在还被占着吗」来判，一次临时的让路就会
// 变成一个用户从没同意过的改动，而清单是用户写的。
func TestResumePortOnlyWhileTheManifestPortIsStillTaken(t *testing.T) {
	manifest, release := holdPort(t)
	other := freePort(t)
	h := portHarness(t, manifest)

	// 上一次是从清单里那个端口让路出来的，换到了 other 上。
	putEntry(t, h, &proc.Entry{PID: os.Getpid(), PGID: os.Getpid(), StartedAt: time.Now(), Port: other})
	if got := h.p.resumePort("alpha"); got != other {
		t.Errorf("清单里那个还占着时 resumePort = %d，想要接着用 %d", got, other)
	}

	// 占着它的那个进程走了：下一次启动回到清单说的那个端口上。
	release()
	if got := h.p.resumePort("alpha"); got != 0 {
		t.Errorf("清单里那个空出来之后 resumePort = %d，想要 0（回到清单）", got)
	}

	cases := []struct {
		name  string
		svc   string
		entry *proc.Entry
	}{
		{"记录里的端口和清单一样", "alpha", &proc.Entry{PID: os.Getpid(), Port: manifest}},
		{"记录里没写端口", "alpha", &proc.Entry{PID: os.Getpid()}},
		{"没有记录", "alpha", nil},
		{"这个服务本来就没配端口", "beta", &proc.Entry{PID: os.Getpid(), Port: other}},
		{"名字不认识", "根本没有这个服务", &proc.Entry{PID: os.Getpid(), Port: other}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			putEntry(t, h, c.entry)
			if got := h.p.resumePort(c.svc); got != 0 {
				t.Errorf("resumePort = %d，想要 0", got)
			}
		})
	}
}
