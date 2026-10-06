package update

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// newTestSession 铺一份干净的数据目录，再建一个已经查到 0.3.0 的会话。
// 安装位直接写进去：测试二进制认不出安装形态，而这里要测的不是认安装位那件事。
func newTestSession(t *testing.T, current string) *Session {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)
	c := New(Options{Current: current})
	s := &Session{
		c:       c,
		install: Install{Kind: KindCLI, Target: "/opt/pier", Relaunch: "/opt/pier-gui"},
		release: Release{
			Version:   "0.3.0",
			Tag:       "v0.3.0",
			URL:       "https://example.test/releases/tag/v0.3.0",
			Published: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
			Notes:     "第一行\n第二行\n",
		},
		checkedAt: time.Now(),
	}
	return s
}

func TestStatusMergesSettingsAndRelease(t *testing.T) {
	s := newTestSession(t, "0.2.0")

	out := s.Status()
	if out.Current != "0.2.0" || out.Latest != "0.3.0" {
		t.Errorf("版本号不对：%s → %s", out.Current, out.Latest)
	}
	if !out.HasUpdate {
		t.Error("0.2.0 看到 0.3.0 却说没有新版")
	}
	if !out.AutoCheck {
		t.Error("新装的机器上自动检查默认是关的")
	}
	if !out.CanInstall {
		t.Errorf("认得出安装位却说不能装：%s", out.InstallHint)
	}
	if !strings.Contains(out.InstallHint, "/opt/pier") {
		t.Errorf("没说清这次会动哪儿：%s", out.InstallHint)
	}
	if out.PublishedAt != "2026-10-01" {
		t.Errorf("发布日期是 %q", out.PublishedAt)
	}
	// 说明原样带出去，只削掉首尾空行。界面拿它当 markdown 整篇渲染，
	// 在这里动它（截断、抹符号）都会让弹窗里显示的东西和发布页上不一致。
	if out.Notes != "第一行\n第二行" {
		t.Errorf("发布说明是 %q", out.Notes)
	}
	if !strings.HasPrefix(out.LastCheck, "今天 ") {
		t.Errorf("刚查过，说的是 %q", out.LastCheck)
	}

	// 跳过：写进偏好之后立刻反映到状态里，不必重启。
	if err := s.Skip("v0.3.0"); err != nil {
		t.Fatal(err)
	}
	if got := s.Status().Skipped; !got {
		t.Error("跳过之后状态里没体现，界面上那颗圆点会一直亮着")
	}
	if err := s.Skip(""); err != nil {
		t.Fatal(err)
	}
	if s.Status().Skipped {
		t.Error("撤销跳过之后还是跳过")
	}

	// 关掉自动检查。
	p, err := config.SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.UpdateSettings(p, func(st *config.Settings) { st.UpdateCheck = false }); err != nil {
		t.Fatal(err)
	}
	if s.Status().AutoCheck {
		t.Error("偏好里关掉了，状态里还是开着的")
	}
}

// 当前版本认不出来（dev、伪版本）时不比大小：宁可漏报，也不能拿一个编不出来的号
// 去催人升级。
func TestStatusHasNoUpdateForDevBuild(t *testing.T) {
	s := newTestSession(t, "dev")
	if s.Status().HasUpdate {
		t.Error("dev 构建也说有新版本")
	}
}

// 换不了自己的那种安装：状态里要说清为什么，界面据此只给「打开下载页」。
func TestStatusReportsWhyCannotInstall(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.why = "这一份是从源码编译的"

	out := s.Status()
	if out.CanInstall {
		t.Error("明知换不了却说能装")
	}
	if !strings.Contains(out.InstallHint, "从源码编译") {
		t.Errorf("没把原因带出来：%s", out.InstallHint)
	}
}

func TestStatusFormatsProgress(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.mu.Lock()
	s.received, s.total = 12*1024*1024+430*1024, 22*1024*1024
	s.downloading = true
	s.mu.Unlock()

	out := s.Status()
	if out.Progress != 56 {
		t.Errorf("百分比是 %d，按字节算是 56", out.Progress)
	}
	if out.ReceivedSize != "12.4 MB" || out.TotalSize != "22 MB" {
		t.Errorf("大小说法是 %s / %s", out.ReceivedSize, out.TotalSize)
	}

	// 服务端没说总量时不画进度条，也不该给出一个凭空的百分比。
	s.mu.Lock()
	s.total = 0
	s.mu.Unlock()
	if out := s.Status(); out.Progress != 0 {
		t.Errorf("不知道总量却给出了 %d%%", out.Progress)
	}
}

// 查到的那一版换了，已经下好的那份就不作数：留着它，界面上会出现
// 「已下载 0.3.0」和「最新版本 0.4.0」并排摆着。
func TestFinishCheckDropsStaleStage(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.finishDownload("0.3.0", "/tmp/stage", nil)
	if !s.Status().Done {
		t.Fatal("下载完了却没记下")
	}

	s.finishCheck(Result{Release: Release{Version: "0.4.0"}, Current: "0.2.0", CheckedAt: time.Now()}, nil)
	if out := s.Status(); out.Done {
		t.Errorf("查到 0.4.0 之后还端着 0.3.0 的产物：%+v", out)
	}

	// 查到的还是同一版（比如 304）时不能把它清掉：那会让用户白下一次。
	s.finishDownload("0.4.0", "/tmp/stage", nil)
	s.finishCheck(Result{Release: Release{Version: "0.4.0"}, Current: "0.2.0", NotModified: true, CheckedAt: time.Now()}, nil)
	if !s.Status().Done {
		t.Error("只是又问了一遍同一版，下载好的产物却被丢了")
	}
}

// 查失败只说一句，不改变已经查到的那一版：网络断了不该让人以为「没有新版本」。
func TestFinishCheckKeepsReleaseOnError(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.checking = true
	s.finishCheck(Result{}, errors.New("连不上 api.github.com"))

	out := s.Status()
	if out.CheckError == "" {
		t.Error("查失败了却什么都没记下")
	}
	if out.Latest != "0.3.0" || !out.HasUpdate {
		t.Errorf("一次查失败把上次查到的结果抹掉了：%+v", out)
	}
	if out.Checking {
		t.Error("查完了还说自己正在查")
	}
}

// 没有查到版本、或者正在查的时候点下载，要说一句话，不能静悄悄地什么都没发生。
func TestDownloadRefusesWithoutRelease(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.mu.Lock()
	s.release = Release{}
	s.mu.Unlock()

	s.Download()
	if out := s.Status(); out.Error == "" || out.Downloading {
		t.Errorf("没版本可下却什么都没说：%+v", out)
	}

	s2 := newTestSession(t, "0.2.0")
	s2.mu.Lock()
	s2.checking = true
	s2.mu.Unlock()
	s2.Download()
	if out := s2.Status(); !strings.Contains(out.Error, "检查") {
		t.Errorf("正在查的时候点下载，说的是 %q", out.Error)
	}
}

// 换不了自己的那种安装不给下载：装不上却让人先花几分钟下几十兆，是骗人。
func TestDownloadRefusesWhenCannotInstall(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.why = "这一份是从源码编译的"
	s.Download()
	if out := s.Status(); !strings.Contains(out.Error, "源码") {
		t.Errorf("说的是 %q", out.Error)
	}
}

// 取消不是失败：回到「等下载」那个状态，不该留一句红字。
func TestFinishDownloadTreatsCancelAsNotAnError(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	s.downloading = true
	s.finishDownload("", "", context.Canceled)

	out := s.Status()
	if out.Downloading || out.Done {
		t.Errorf("取消之后的状态不对：%+v", out)
	}
	if out.Error != "" {
		t.Errorf("用户自己按的取消留下了一句错误：%s", out.Error)
	}
}

// 发布页地址只认自己查到的那一版；没查到具体某一版时退回发布列表。
func TestNotesURL(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	if got := s.NotesURL(); got != "https://example.test/releases/tag/v0.3.0" {
		t.Errorf("发布页地址是 %q", got)
	}
	s.mu.Lock()
	s.release = Release{}
	s.mu.Unlock()
	if got := s.NotesURL(); got != ReleasesPage() {
		t.Errorf("没有具体某一版时该退回发布列表，给的是 %q", got)
	}
}

// 结果文件读一次不算数：用户看过、点掉之后才清，否则那句失败原因就再也没人看得见。
func TestDismissResultClearsFile(t *testing.T) {
	s := newTestSession(t, "0.2.0")
	path, err := ResultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeResult(path, ApplyResult{OK: false, Message: "换文件的时候失败了"}); err != nil {
		t.Fatal(err)
	}
	// 新起一个会话：启动时会把上次的结果读回来。
	s2 := NewSession(s.c)
	if s2.Status().Result == nil {
		t.Fatal("上次替换留下的结果没读回来")
	}
	if s2.Status().Result.Message != "换文件的时候失败了" {
		t.Errorf("读回来的是 %+v", s2.Status().Result)
	}
	if err := s2.DismissResult(); err != nil {
		t.Fatal(err)
	}
	if s2.Status().Result != nil {
		t.Error("点掉之后还挂在状态里")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("点掉之后文件还在，下次启动又会把那句话再说一遍")
	}
}

func TestHumanTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"没查过", time.Time{}, ""},
		{"今天", now, "今天 " + now.Format("15:04")},
		{"昨天", now.AddDate(0, 0, -1), "昨天 " + now.AddDate(0, 0, -1).Format("15:04")},
		{"更早", time.Date(2026, 3, 4, 9, 8, 0, 0, time.Local), "2026-03-04 09:08"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := humanTime(c.in); got != c.want {
				t.Errorf("说的是 %q，想要 %q", got, c.want)
			}
		})
	}
}

