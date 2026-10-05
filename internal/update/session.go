package update

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/version"
	"github.com/zhengshangjinx/pier/internal/view"
)

// 这个文件是界面那一侧的更新状态机。
//
// 命令行不需要它：那边是「一条命令跑到底」，查、下、换一路走完就结束。
// 界面不一样——检查、下载、取消、安装是四次独立的点击，中间还夹着后台那次自动检查，
// 状态得有个东西一直端着。这些判断摆在这里而不是 gui/app.go，是因为写进界面的
// 每一句，另一个界面都得重新实现一遍。

const (
	// AutoFirst 是界面起来之后第一次自动检查的等待。不与启动抢网络与磁盘：
	// 启动那几百毫秒里要读清单、探进程、认安装位，再挤一条出网的请求只会拖慢它。
	AutoFirst = 20 * time.Second

	// AutoEvery 是之后每次自动检查的间隔。
	//
	// 未登录访问 GitHub 接口的额度是每 IP 每小时 60 次，按这个节奏一天四次上下；
	// 带着 ETag 的条件请求命中 304 时还不计入这个额度。真正要防的是共用出口 IP
	// 那种情况，所以查失败一律安静地记下，下次到点再试。
	AutoEvery = 6 * time.Hour

	// notesRunes 是发布说明跟着状态带回界面的长度。
	//
	// 状态是每隔一秒轮询一次的东西，整篇说明每次都搬一遍没有意义；界面上那一格
	// 本来就只摆前几行，要看全文有「查看发布说明」。
	notesRunes = 800
)

// StatusOut 是界面上「更新」这一块要的全部信息，一次给全。
//
// 界面不做任何推算：百分比、大小、时刻的说法都在这儿算好。理由与日志占用那一处
// 相同——同一个数在两处各算一遍，迟早一处显示成「23.6 MB」、另一处显示成「24 MB」。
type StatusOut struct {
	// Current 是当前这份二进制的版本号。
	Current string `json:"current"`
	// Latest 是查到的最新版本号。认不出来（tag 不是版本号）时为空。
	Latest string `json:"latest"`
	// HasUpdate 报告有没有比当前更新的一版。当前版本认不出来时恒为假。
	HasUpdate bool `json:"hasUpdate"`
	// Skipped 报告用户点名跳过的正是这一版。界面上有新版的那颗圆点要避开它，
	// 否则「跳过」按下去看着像没生效。
	Skipped bool `json:"skipped"`
	// Checking 报告此刻正在查。
	Checking bool `json:"checking"`
	// Downloading 报告此刻正在下载（含解压）。
	Downloading bool `json:"downloading"`
	// Done 报告这一版的产物已经下好、解好，就等换文件了。
	Done bool `json:"done"`
	// Received 与 Total 是下载进度，单位是字节；Total 为 0 表示服务端没说总量。
	Received int64 `json:"received"`
	Total    int64 `json:"total"`
	// Progress 是百分比（0-100）。Total 为 0 时也是 0，界面据此不画进度条。
	Progress int `json:"progress"`
	// ReceivedSize 与 TotalSize 是上面两个数换算好的说法。
	ReceivedSize string `json:"receivedSize"`
	TotalSize    string `json:"totalSize"`
	// Asset 是这次要下的那份产物的名字，AssetSize 是它的大小。
	Asset     string `json:"asset"`
	AssetSize string `json:"assetSize"`
	// Error 是下载或解压这一步的失败原因，空表示没失败。
	Error string `json:"error"`
	// CheckError 是上一次检查的失败原因。
	//
	// 与 Error 分开记：检查失败不是用户做了什么的结果，绝大多数是网络不通，
	// 为此弹一个框只会让人烦，所以就摆一句在页面上，下次到点自己再试。
	CheckError string `json:"checkError"`
	// Result 是上一次替换留下的结果（失败原因唯一的去处）。
	Result *ApplyResult `json:"result,omitempty"`
	// LastCheck 是上次真的问到服务端的时刻，说法是「今天 14:32」。
	LastCheck string `json:"lastCheck"`
	// PublishedAt 是这一版的发布日期（2026-10-01），没给时为空。
	PublishedAt string `json:"publishedAt"`
	// Notes 是发布说明的前几行。
	Notes string `json:"notes"`
	// AutoCheck 是「自动检查更新」开关此刻的状态。
	AutoCheck bool `json:"autoCheck"`
	// CanInstall 报告这份 Pier 能不能自己换自己。为假时界面只给「打开下载页」，
	// 原因写在 InstallHint 里。
	CanInstall bool `json:"canInstall"`
	// InstallHint 说明这次替换会动哪儿；换不了的时候说的是为什么换不了。
	InstallHint string `json:"installHint"`
}

// Session 是界面上更新这一块的全部状态。
type Session struct {
	c *Client
	// install 是这份 Pier 的安装位，why 为真时它没有意义。
	install Install
	// why 是「这份 Pier 换不了自己」的原因，空表示能换。它只在启动时算一次：
	// 安装位在一次运行里不会变，每次问一遍只是多几次系统调用。
	why string

	mu sync.Mutex
	// release 与 checkedAt 来自这次查到的一版，或者上一回进程留下的 update.json。
	release   Release
	checkedAt time.Time
	// checkErr 是上一次检查的失败原因。
	checkErr string
	// checking 报告查询在飞。
	checking bool
	// downloading / done / stagedVer / stage 是产物这一条线。
	downloading bool
	done        bool
	stagedVer   string
	stage       string
	asset       Asset
	received    int64
	total       int64
	// errText 是下载或解压失败的原因。
	errText string
	// cancel 停得掉正在跑的这次下载。
	cancel context.CancelFunc
	// claim 是「正在换文件」那把锁，从这次替换开始一直握到本进程退出。
	//
	// 有意不放开：助手等的正是它被放开的那一刻（见 waitForCallerExit）。
	// 提前交回去，助手会以为发起方已经走了，接着去换一个还活着的进程的文件。
	claim *proc.Claim
	// result 是上一次替换留下的结果。
	result *ApplyResult
}

// NewSession 建一个会话：认一次安装位，把上次查到的状态与上次替换的结果读回来。
//
// 认不出安装位不是错误，是这次运行的一个事实：能查到有没有新版本，但换不了，
// 界面据此只说一句实话。
func NewSession(c *Client) *Session {
	s := &Session{c: c}
	if inst, err := Detect(); err == nil {
		s.install = inst
	} else {
		s.why = err.Error()
	}
	st := c.State()
	s.release, s.checkedAt = st.Release, st.CheckedAt
	if res, ok := LoadResult(); ok {
		s.result = &res
	}
	return s
}

// Status 拼一份界面要的完整状态。
func (s *Session) Status() StatusOut {
	s.mu.Lock()
	release, checkedAt := s.release, s.checkedAt
	checkErr, errText := s.checkErr, s.errText
	checking, downloading, done := s.checking, s.downloading, s.done
	received, total := s.received, s.total
	asset, result := s.asset, s.result
	s.mu.Unlock()

	// 开关每次现读：用户在偏好设置里改完，下一次轮询就该看见新值。
	// settings.json 很小，一秒读一次不构成代价；缓存下来反而多出一份会过期的真相。
	pref := config.DefaultSettings()
	out := StatusOut{
		Current:      s.c.Current(),
		Latest:       release.Version,
		HasUpdate:    version.Newer(release.Version, s.c.Current()),
		Skipped:      pref.UpdateSkipped != "" && pref.UpdateSkipped == release.Version,
		Checking:     checking,
		Downloading:  downloading,
		Done:         done,
		Received:     received,
		Total:        total,
		ReceivedSize: view.Bytes(received),
		TotalSize:    view.Bytes(total),
		Asset:        asset.Name,
		AssetSize:    view.Bytes(asset.Size),
		Error:        errText,
		CheckError:   checkErr,
		Result:       result,
		LastCheck:    humanTime(checkedAt),
		PublishedAt:  dayOf(release.Published),
		Notes:        notesPreview(release.Notes, notesRunes),
		AutoCheck:    pref.UpdateCheck,
		CanInstall:   s.why == "",
	}
	if total > 0 {
		out.Progress = int(received * 100 / total)
		if out.Progress > 100 {
			out.Progress = 100
		}
	}
	if s.why != "" {
		out.InstallHint = s.why
	} else {
		out.InstallHint = "这次会替换 " + s.install.Target + "。"
	}
	return out
}

// Check 触发一次检查，立刻返回；结果落在状态里，界面下一次轮询就看得见。
//
// 在后台跑而不是当场等：绑定回调走的是界面线程，一条 30 秒的 GET 会把这 30 秒里
// 所有的点击都卡住——包括那个「取消」。
func (s *Session) Check() {
	s.mu.Lock()
	if s.checking {
		s.mu.Unlock()
		return
	}
	s.checking = true
	s.mu.Unlock()

	go func() {
		res, err := s.c.Check(context.Background())
		s.finishCheck(res, err)
	}()
}

// AutoCheck 按「起来之后先等一会儿、之后每隔一段时间」的节奏自动检查，
// 直到 stop 被关掉（窗口关了）。
//
// 每一轮都重新读一次开关：用户在偏好设置里把自动检查关掉之后，不必重启界面。
// 手动那个按钮不走这儿，所以关掉的只是「自动」这件事。
func (s *Session) AutoCheck(first, every time.Duration, stop <-chan struct{}) {
	if !sleepUntil(first, stop) {
		return
	}
	for {
		if config.DefaultSettings().UpdateCheck {
			s.Check()
		}
		if !sleepUntil(every, stop) {
			return
		}
	}
}

// Download 开始下载并解压这一版的产物，立刻返回；进度落在状态里。
//
// 下载与解压合成一件事：这样「重启并安装」按下去的时候什么都不用等。用户点那一下
// 是知情同意的唯一凭据，让他先盯着进度条走两分钟再换文件，那个同意就打了折。
//
// 已经下好的那份产物不会重下（见 Client.Download），所以中途退出再点一次，
// 接上的是磁盘上已经验过的那一份。
func (s *Session) Download() {
	s.mu.Lock()
	if s.downloading || s.done {
		// 已经在下了、或者已经下好了：再点一次不该有任何变化。
		s.mu.Unlock()
		return
	}
	switch {
	case s.checking:
		s.errText = "正在检查更新，等这次查完再下载"
	case s.why != "":
		s.errText = s.why
	case s.release.Version == "":
		s.errText = "还没有查到新版本"
	default:
		s.errText = ""
	}
	if s.errText != "" {
		s.mu.Unlock()
		return
	}
	asset, err := s.release.Pick(s.install.Kind)
	if err != nil {
		s.errText = err.Error()
		s.mu.Unlock()
		return
	}
	release := s.release
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.downloading = true
	s.asset = asset
	s.received, s.total = 0, 0
	s.mu.Unlock()

	go func() {
		defer cancel()
		archive, err := s.c.Download(ctx, release, asset, s.progress)
		if err == nil {
			var stage string
			if stage, err = Stage(s.c.Dir(), release.Version, archive); err == nil {
				s.finishDownload(release.Version, stage, nil)
				return
			}
		}
		s.finishDownload("", "", err)
	}()
}

// Cancel 停掉正在跑的这次下载。
//
// 已经下到一半的那份留在磁盘上（见 Client.Download 的续传），下次再点接着下。
// 界面不用自己把状态拨回去：取消会让下载那一头出错返回，状态由同一个出口改。
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Apply 把「换文件」交给助手，并把过程日志的路径回报给界面。
//
// 界面这一侧一律走助手，从不当场替换自己：当场换掉的是磁盘上的文件，而跑着的
// 那一份还指着旧代码、旧资源，界面上会出现半新半旧的东西。助手等的正是本进程退出。
// 调用方拿到这句话之后就该退出了（见 gui/main.go 注入的 quit）。
func (s *Session) Apply() (string, error) {
	s.mu.Lock()
	release, stage, done, why := s.release, s.stage, s.done, s.why
	s.mu.Unlock()

	if why != "" {
		return "", errors.New(why)
	}
	if !done {
		return "", errors.New("还没下载好，先点「下载更新」")
	}
	// 换完之后回不来的安装形态，界面这一侧就不动手：窗口一关，用户面对的是一台
	// 什么都没有的机器。认不出安装位时上面那句已经挡住了，这里防的是另一半——
	// 认出来了、也换得动，但没有任何东西能在换完之后把界面拉回来。
	if s.install.Relaunch == "" {
		return "", errors.New("这一份 Pier 换完之后不会自己回来，请在终端里运行 pier update：\n  " + ReleasesPage())
	}
	// 写不写得动要在用户点下去之前就知道（见 Install.CheckWritable）。
	if err := s.install.CheckWritable(); err != nil {
		return "", err
	}
	claim, err := BeginApply()
	if err != nil {
		return "", err
	}
	logPath, err := Schedule(Plan{
		Version:  release.Version,
		Dir:      s.c.Dir(),
		Stage:    stage,
		Target:   s.install.Target,
		Kind:     s.install.Kind,
		Relaunch: s.install.Relaunch,
	}, "")
	if err != nil {
		// 助手没起来，这次替换当场作废：把锁交回去，否则本进程剩下的时间里
		// 连重试一次都做不到。
		claim.Release()
		return "", err
	}
	s.mu.Lock()
	s.claim = claim
	s.mu.Unlock()
	return logPath, nil
}

// Skip 记下「这一版我不装」，传空串表示把跳过撤销。
//
// 版本号先规范化：界面上摆的是 0.3.0，而手改过 settings.json 的人可能写成 v0.3.0，
// 两边必须落成同一个串，否则「跳过」会在下一次检查里认不出来，圆点照样亮着。
func (s *Session) Skip(ver string) error {
	if ver != "" {
		v, ok := version.Canon(ver)
		if !ok {
			return fmt.Errorf("认不出版本号 %q", ver)
		}
		ver = v
	}
	p, err := config.SettingsPath()
	if err != nil {
		return err
	}
	return config.UpdateSettings(p, func(st *config.Settings) { st.UpdateSkipped = ver })
}

// NotesURL 是这次这一版的发布页；没有具体某一版时退回发布列表。
//
// 只认自己查到的那一版，不接受调用方传一个地址进来：那等于把界面上的一个参数
// 变成「用系统默认程序打开任意网址」。
func (s *Session) NotesURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release.URL != "" {
		return s.release.URL
	}
	return ReleasesPage()
}

// DismissResult 收掉上一次替换留下的那条消息，用户看过之后才调。
//
// 不在这之前顺手清掉：那是失败原因唯一的去处（助手跑在 Pier 已经退出的空档里，
// 没有窗口能报错），读一次就删等于「更新失败了，而界面上一句都不说」。
func (s *Session) DismissResult() error {
	s.mu.Lock()
	s.result = nil
	s.mu.Unlock()
	return ClearResult()
}

// finishCheck 收下这次检查的结果。
func (s *Session) finishCheck(res Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checking = false
	if err != nil {
		s.checkErr = err.Error()
		return
	}
	s.checkErr = ""
	s.release, s.checkedAt = res.Release, res.CheckedAt
	// 查到的那一版换了，已经下好的那份就不作数了：留着它，界面上会出现
	// 「已下载 0.3.0」和「最新版本 0.4.0」并排摆着，谁也说不清点下去装的是哪个。
	if s.done && s.stagedVer != res.Release.Version {
		s.done, s.stagedVer, s.stage, s.asset = false, "", "", Asset{}
	}
}

// finishDownload 收下这次下载的结果。err 为 nil 表示产物已经解好、就等换文件。
func (s *Session) finishDownload(ver, stage string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloading = false
	s.cancel = nil
	if err == nil {
		s.done, s.stagedVer, s.stage, s.errText = true, ver, stage, ""
		return
	}
	// 用户自己按的取消不是失败：回到「等下载」那个状态，一句话都不用说。
	if errors.Is(err, context.Canceled) {
		s.errText = ""
		return
	}
	s.errText = err.Error()
}

// progress 是下载途中的进度回调，一秒里会被调很多次。
func (s *Session) progress(got, total int64) {
	s.mu.Lock()
	s.received, s.total = got, total
	s.mu.Unlock()
}

// sleepUntil 睡够 d，或者被 stop 叫醒；返回是不是睡够了。
func sleepUntil(d time.Duration, stop <-chan struct{}) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
		return false
	}
}

// humanTime 把时刻说成「今天 14:32」这种。零值返回空串——没查过就什么都不说，
// 而不是摆一个 0001-01-01 出来。
//
// 比日期比的是本地日历日（各自 Format 出来的那串），不做「减 24 小时」那种算术：
// 夏令时切换那天，两个日期相差 23 或 25 小时，减出来的日子会差一天。
func humanTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	day := t.Format(config.LogDateLayout)
	switch day {
	case time.Now().Format(config.LogDateLayout):
		return t.Format("今天 15:04")
	case time.Now().AddDate(0, 0, -1).Format(config.LogDateLayout):
		return t.Format("昨天 15:04")
	default:
		return t.Format("2006-01-02 15:04")
	}
}

// dayOf 是发布日期的说法，只到天。零值返回空串。
func dayOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(config.LogDateLayout)
}

// notesPreview 把发布说明截成界面上那一格摆得下的样子。
//
// 尽量在整行之间断开，不在半句话中间切；截过就补一个省略号，让人知道后面还有。
// 最后一条换行离截断处太远时就不管它了——那一行本身就超长，按它截等于把内容
// 砍掉一大半，还不如老老实实截在字数上。
func notesPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if i := strings.LastIndex(cut, "\n"); i >= max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t\r\n") + "…"
}
