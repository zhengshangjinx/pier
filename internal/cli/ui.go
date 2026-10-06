package cli

// 本文件实现 `pier ui` 的交互式面板。
//
// 三处关键取舍：
//
//   - 表格自己拼字符串，但「什么状态叫什么名字」复用 internal/view 里的文案。
//     面板要高亮、要随终端宽度伸缩，没法走 renderTable 的打印路径；可状态文案只能有
//     一份事实源，否则 status 命令、面板与图形界面迟早给出不一致的说法。
//   - 所有启停都丢进 tea.Cmd 里异步执行，结果用 Msg 回传。Supervisor.Start 内部会同步
//     跑完 Maven 编译，几十秒起步；写在 Update 里等于把整个界面冻住，连 ctrl+c 都没反应。
//   - 多个服务的启停排队执行，一次只跑一个。并发启动 Java 服务时日志会互相淹没，
//     出了问题看不出是谁失败的；排队还能把每个服务的进度单独标进表格。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// uiRefreshInterval 是状态自动刷新的间隔。一次 Status() 只做端口拨测和「每个运行中的
// 服务一次健康请求」，成本很低；2 秒足以让人觉得是实时的，又不至于让 CPU 一直空转。
const uiRefreshInterval = 2 * time.Second

// uiLogLoadLines 是一次从日志文件读入的最大行数。日志每次启动都会重建，正常只有几百行；
// 设上限只是为了防住某次异常输出把内存撑爆。
const uiLogLoadLines = 3000

// 操作动词。既当队列里的动作标记，也直接当界面文案用——中文界面下再套一层
// 「动词 -> 文案」的映射纯属多余。
const (
	uiActStart   = "启动"
	uiActStop    = "停止"
	uiActRestart = "重启"
)

// uiCursorOn / uiCursorOff 是选中行的前缀。未选中的行也占同样宽度，
// 上下移动时后面的列才不会跟着左右跳。
const (
	uiCursorOn  = "▸ "
	uiCursorOff = "  "
)

// uiHeaders 是表格列名。顺序即重要性：终端放不下时从最后一列往前丢。
var uiHeaders = []string{"服务", "类型", "状态", "端口", "PID", "运行时长", "健康", "说明"}

// 界面样式。用 ANSI 基本色（0-15）而不是真彩色，这样深色/浅色主题都能自然适配；
// 样式本身不参与宽度计算，所以一律「先按显示宽度补齐再着色」，见 uiTableRow。
var (
	uiTitleStyle  = lipgloss.NewStyle().Bold(true)
	uiHintStyle   = lipgloss.NewStyle().Faint(true)
	uiErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	uiOKStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	uiWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	uiDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	uiBusyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	uiCursorStyle = lipgloss.NewStyle().Bold(true)
)

// uiStatusMsg 是一次状态快照。
type uiStatusMsg struct {
	list []proc.Status
	err  error
}

// uiOpDoneMsg 是队列里某个启停操作的最终结果。
type uiOpDoneMsg struct {
	job uiJob
	err error
}

// uiTickMsg 由定时器发出，驱动自动刷新。
type uiTickMsg time.Time

// uiStdinEOFMsg 表示 stdin 已经读完，不会再有按键进来了。
type uiStdinEOFMsg struct{}

// uiLogMsg 携带读到的日志尾部。
type uiLogMsg struct {
	name  string
	lines []string
	err   error
}

// uiJob 是队列里的一项待执行操作。
type uiJob struct {
	act string
	svc *config.Service
}

// run 在后台执行这个操作并回传结果。
//
// 它必须整个丢进 tea.Cmd：Supervisor.Start 会同步等 Maven 编译结束，
// 几十秒内不返回，直接调用就会冻住按键响应。
func (j uiJob) run(sup *proc.Supervisor) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch j.act {
		case uiActStart:
			err = uiStartService(sup, j.svc)
		case uiActStop:
			err = sup.Stop(j.svc.Name)
		case uiActRestart:
			// 重启 = 先停后起。停止阶段的「本来就没在跑」不算失败，
			// 否则一个刚被人手工 kill 过的服务就永远重启不了。
			if serr := sup.Stop(j.svc.Name); serr != nil && !uiBenignStop(serr) {
				err = serr
			} else {
				err = uiStartService(sup, j.svc)
			}
		}
		return uiOpDoneMsg{job: j, err: err}
	}
}

// uiStartService 是启动前的最后一道闸：端口已被监听就绝不启动。
//
// 本机 3106 上跑着用户自己的前端 dev server，20351 之类也可能被 IDEA 里的实例占着。
// 往已占用的端口上再起一个实例，轻则启动即失败，重则把别人的服务顶掉；
// Pier 只该管自己启动的进程，别人的一律不碰。
func uiStartService(sup *proc.Supervisor, svc *config.Service) error {
	if svc.Port > 0 && proc.PortOpen(svc.Port) {
		return fmt.Errorf("端口 %d 已被占用（可能已在 IDEA、其它终端或 Pier 之外运行），未启动", svc.Port)
	}
	return sup.Start(svc)
}

// uiBenignStop 判断停止时的错误是不是「其实没什么问题」。
// 这两种情况都表示 Pier 没有可停的进程，而不是停止动作失败。
func uiBenignStop(err error) bool {
	return errors.Is(err, proc.ErrNotManaged) || errors.Is(err, proc.ErrAlreadyGone)
}

// uiModel 是面板的全部状态。bubbletea 的 Update 按值传递模型，所以这里的字段
// 都设计成可以被整体复制；map / slice 是共享引用，更新时用替换而不是原地改。
type uiModel struct {
	cfg *config.Config
	sup *proc.Supervisor

	// kinds 缓存服务类型名。kind 允许在配置里留空（按目录内容自动识别），
	// 而识别要读文件系统，不能每帧都做，所以进面板时算一次。
	kinds map[string]string

	statuses []proc.Status
	// loaded 区分「还没读到状态」和「配置里一个服务都没有」，
	// 两者都表现为空表格，但该说的话完全不同。
	loaded bool
	// refreshing 标记已有一次刷新在飞。健康探针单个最长 2 秒，串行探完可能超过
	// 刷新间隔；不挡住的话 tick 会把刷新叠成好几份，白白堆出一串过期的快照。
	refreshing bool

	width, height int
	cursor        int

	// busy 记录正在执行的操作：服务名 -> 动作。有了它，正在进行中的那个服务会显示
	// 「启动…」而不是一个静止的旧状态——Maven 编译期间用户最需要知道的就是「它在动」。
	busy map[string]string
	// queue 是还没开始执行的操作。一次只跑一个，理由见文件头。
	queue []uiJob

	// notice 是底部最近一条操作结果；noticeErr 决定用红还是灰。
	notice    string
	noticeErr bool

	// eofPending 记「stdin 已经读完，但首帧状态还没画出来」。
	// 读完了就没人能按键，面板该退场；只是要等状态画完，别让用户白等一场。
	eofPending bool

	// logMode 为真时界面切到日志视图。
	logMode   bool
	logName   string
	logPath   string
	logLines  []string
	logScroll int // 距文件末尾的行数，0 表示跟随最新

	// err 是最近一次读取失败（状态或日志）。面板本身就是排查问题的地方，
	// 读失败只在界面上报错、不退出。
	err error
}

func newUIModel(cfg *config.Config, sup *proc.Supervisor) uiModel {
	// 先用配置里的服务把表格行铺满，状态留空（loaded=false 表示还没读到真实状态）。
	//
	// 为什么不等 Status() 回来再建表：那一次查询要给每个服务跑一遍 lsof，
	// 给每个运行中的服务串行发健康请求（单个上限 2 秒），有 Java 服务在跑时
	// 好几秒都回不来。这段时间里如果是空列表，方向键会因为 len(statuses)-1 == -1
	// 而静默失灵，整个界面看着像卡死。行数其实在配置里就有，没必要等。
	seeded := make([]proc.Status, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		seeded = append(seeded, proc.Status{Service: svc})
	}
	return uiModel{
		cfg:      cfg,
		sup:      sup,
		kinds:    uiDetectKinds(cfg),
		statuses: seeded,
		busy:     map[string]string{},
	}
}

// uiDetectKinds 预先算好每个服务的类型名。识别失败不算错误：
// 面板上显示「-」就够了，真正启动时 Supervisor 会给出明确的报错。
func uiDetectKinds(cfg *config.Config) map[string]string {
	out := make(map[string]string, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc.Kind != "" {
			out[svc.Name] = svc.Kind
			continue
		}
		if k, err := config.DetectKind(svc.AbsDir()); err == nil {
			out[svc.Name] = k
		}
	}
	return out
}

// cmdUI 打开交互式面板。args 支持 --config X / --config=X，与其它子命令一致。
func cmdUI(args []string) int {
	cfgPath, rest := extractConfig(args)
	if err := noExtra("ui", rest); err != nil {
		return fail("%v", err)
	}
	cfg, sup, err := setup(cfgPath)
	if err != nil {
		return fail("%v", err)
	}

	// WithAltScreen 进入备用屏幕：退出时终端会自动回到原来的内容，
	// 面板刷屏不会弄脏用户之前的输出。
	//
	// WithInput 把输入钉死在 stdin 上。bubbletea 默认会在「stdin 不是终端」时改开
	// /dev/tty 取按键，可只要进程没有控制终端（重定向、管道、CI、nohup），
	// open /dev/tty 就会直接失败，面板连一帧都画不出来——一个只能看状态的窗口
	// 不该因为拿不到键盘就拒绝启动。交互式使用时 stdin 本来就是终端，
	// 这里与默认行为完全一致，raw 模式、退出恢复都照常。
	//
	// 外面再包一层 EOF 通知：管道或 /dev/null 读到结尾就不会再有按键进来了，
	// 面板该自己收场，而不是挂在那儿等外部信号来杀。
	var prog *tea.Program
	in := &uiEOFReader{File: os.Stdin, onEOF: func() { prog.Send(uiStdinEOFMsg{}) }}
	prog = tea.NewProgram(newUIModel(cfg, sup), tea.WithAltScreen(), tea.WithInput(in))
	if _, err := prog.Run(); err != nil {
		return fail("面板运行失败：%v", err)
	}
	return 0
}

// uiEOFReader 在 os.Stdin 读到 EOF 时通知面板。
//
// 内嵌 *os.File 而不是只留一个 io.Reader 字段，是为了保住 Fd()/Write()/Close()：
// bubbletea 靠类型断言 term.File 判断「输入是不是终端」，只有认出来才会进 raw 模式。
// 一旦退化成普通 io.Reader，真终端里方向键就会失灵——这个包装会得不偿失。
type uiEOFReader struct {
	*os.File
	once  sync.Once
	onEOF func()
}

func (f *uiEOFReader) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	if errors.Is(err, io.EOF) {
		// 只通知一次。读循环碰到 EOF 就退出了（bubbletea 显式容忍 io.EOF），
		// 不会反复调用到这里，once 是为了万一将来有人改坏这层假定时兜住。
		f.once.Do(f.onEOF)
	}
	return n, err
}

func (m uiModel) Init() tea.Cmd {
	// 首屏不阻塞在状态查询上：先把空表格画出来，状态由后台命令回填。
	// 这里走 statusCmd 而不是 requestRefresh：Init 里的模型副本回不到 Update，
	// refreshing 标记置了也没用；顶多多刷一次，无害。
	return tea.Batch(m.statusCmd(), uiTickCmd())
}

// uiTickCmd 排下一次自动刷新。用 tea.Tick 而不是 time.After，是为了让定时器
// 也走 bubbletea 的事件循环，退出时不会留下一个游离的 goroutine。
func uiTickCmd() tea.Cmd {
	return tea.Tick(uiRefreshInterval, func(t time.Time) tea.Msg { return uiTickMsg(t) })
}

// statusCmd 发起一次状态查询。只捕获 sup：tea.Cmd 在另一个 goroutine 里跑，
// 不能去读那些会被 Update 改动的字段。
func (m uiModel) statusCmd() tea.Cmd {
	sup := m.sup
	return func() tea.Msg {
		list, err := sup.Status()
		return uiStatusMsg{list: list, err: err}
	}
}

// requestRefresh 在「当前没有刷新在飞」时发起一次状态查询。
func (m uiModel) requestRefresh() (uiModel, tea.Cmd) {
	if m.refreshing {
		return m, nil
	}
	m.refreshing = true
	return m, m.statusCmd()
}

// loadLog 读取日志视图当前服务的日志尾部。
// 只捕获路径和名字：tea.Cmd 在另一个 goroutine 里跑，不能去读会被 Update 改动的字段。
func (m uiModel) loadLog() tea.Cmd {
	name, path := m.logName, m.logPath
	return func() tea.Msg {
		lines, err := uiReadLog(path, uiLogLoadLines)
		return uiLogMsg{name: name, lines: lines, err: err}
	}
}

// pump 在空闲时从队列里取出下一项操作开跑。
// 一次只跑一个，是为了让「谁在启动、成没成功」始终只有一个答案。
func (m uiModel) pump() (uiModel, tea.Cmd) {
	if len(m.queue) == 0 || len(m.busy) > 0 {
		return m, nil
	}
	job := m.queue[0]
	m.queue = m.queue[1:]
	m.busy = uiSetBusy(m.busy, job.svc.Name, job.act)
	return m, job.run(m.sup)
}

// enqueue 把操作排进队列并立刻尝试开跑（前一项还在跑就先等着）。
func (m uiModel) enqueue(jobs ...uiJob) (uiModel, tea.Cmd) {
	m.queue = append(m.queue, jobs...)
	return m.pump()
}

// uiSetBusy 更新 busy 记录，act 为空表示删除。
//
// 刻意复制一份再替换，而不是原地 delete/赋值：map 是引用类型，原地改会把改动
// 带到模型的其它副本上。bubbletea 的模型是按值传递的，「看着是局部写、其实是全局改」
// 这类问题一旦出现极难排查，而这里一次操作只动一两个键，复制的代价可以忽略。
func uiSetBusy(busy map[string]string, name, act string) map[string]string {
	out := make(map[string]string, len(busy)+1)
	for k, v := range busy {
		out[k] = v
	}
	if act == "" {
		delete(out, name)
		return out
	}
	out[name] = act
	return out
}

// pending 判断某服务是否已有操作在排队或执行中。连按两下 s 不该排出两次启动：
// 第二次注定以「已在运行」或「端口被占用」失败，纯属噪音。
func (m uiModel) pending(name string) bool {
	if _, ok := m.busy[name]; ok {
		return true
	}
	for _, j := range m.queue {
		if j.svc.Name == name {
			return true
		}
	}
	return false
}

// selected 返回光标所在服务的状态。
func (m uiModel) selected() (proc.Status, bool) {
	if m.cursor < 0 || m.cursor >= len(m.statuses) {
		return proc.Status{}, false
	}
	return m.statuses[m.cursor], true
}

func (m *uiModel) clampCursor() {
	if m.cursor >= len(m.statuses) {
		m.cursor = len(m.statuses) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *uiModel) clampLogScroll() {
	if m.logScroll > len(m.logLines) {
		m.logScroll = len(m.logLines)
	}
	if m.logScroll < 0 {
		m.logScroll = 0
	}
}

func (m *uiModel) setNotice(err error, format string, a ...any) {
	m.notice = fmt.Sprintf(format, a...)
	m.noticeErr = err != nil
}

// startHint 给启动动作补一句耗时预期。Java 服务的第一步是 Maven 编译，
// 几十秒的等待如果没有解释，用户会以为面板卡死了。
func (m uiModel) startHint(name string) string {
	if m.kinds[name] == config.KindJava {
		return "，Java 服务要先跑 Maven 编译，请稍候"
	}
	return ""
}

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case uiTickMsg:
		// 定时器要重新排下一次，否则只刷新一遍就不动了。
		cmds := []tea.Cmd{uiTickCmd()}
		m2, refresh := m.requestRefresh()
		cmds = append(cmds, refresh)
		if m2.logMode {
			cmds = append(cmds, m2.loadLog())
		}
		return m2, tea.Batch(cmds...)

	case uiStdinEOFMsg:
		// 输入结束了（管道写端关闭、`pier ui < /dev/null`）。没有键盘的交互面板
		// 留着只会永远挂着，所以直接退出；但首帧真实状态还没画出来时先记一笔，
		// 等它画完再退——否则重定向跑一次，日志里连服务状态都看不到。
		if m.loaded {
			return m, tea.Quit
		}
		m.eofPending = true
		return m, nil

	case uiStatusMsg:
		m.refreshing = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.loaded = true
			m.statuses = msg.list
			m.clampCursor()
		}
		if m.eofPending {
			// 输入早就结束了，首帧也画出来了，没有理由再留着。
			return m, tea.Quit
		}
		return m, nil

	case uiOpDoneMsg:
		name := msg.job.svc.Name
		m.busy = uiSetBusy(m.busy, name, "")
		switch {
		case msg.err == nil:
			m.setNotice(nil, "%s %s 完成", msg.job.act, name)
		case uiBenignStop(msg.err):
			// 「不是 Pier 启动的」「本来就已退出」不是失败，是说明。
			m.setNotice(nil, "%s %s：%v", msg.job.act, name, msg.err)
		default:
			m.setNotice(msg.err, "%s %s：%v", msg.job.act, name, msg.err)
		}
		next, cmd := m.pump()
		// 操作完成后立刻补一次刷新，不然表格要等下一个 tick 才反映变化，
		// 用户会以为没生效而重复按键。
		next2, refresh := next.requestRefresh()
		return next2, tea.Batch(cmd, refresh)

	case uiLogMsg:
		// 名字对不上说明是上一个服务的迟到结果，丢掉，否则日志会串台。
		if msg.name != m.logName {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.logLines = msg.lines
		m.clampLogScroll()
		return m, nil

	case tea.KeyMsg:
		if m.logMode {
			return m.updateLog(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m uiModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.statuses)-1 {
			m.cursor++
		}

	case "enter", "s":
		return m.toggleSelected()
	case "r":
		return m.restartSelected()
	case "l":
		return m.enterLog()
	case "a":
		return m.startAll()
	case "x":
		return m.stopAll()
	}
	return m, nil
}

// toggleSelected 按当前状态决定是启动还是停止。
func (m uiModel) toggleSelected() (tea.Model, tea.Cmd) {
	st, ok := m.selected()
	// 表格行虽然一进面板就有，但状态要到第一份快照回来才算数。在此之前一律不动手：
	// 拿占位状态去启动，可能会把已经在跑的服务再起一个。
	if !ok || !m.loaded {
		m.setNotice(nil, "状态还在读取中，稍候再操作")
		return m, nil
	}
	name := st.Service.Name
	if m.pending(name) {
		m.setNotice(nil, "%s 已有操作在处理中，请稍候", name)
		return m, nil
	}

	if st.Running {
		m.setNotice(nil, "正在停止 %s…", name)
		next, cmd := m.enqueue(uiJob{act: uiActStop, svc: st.Service})
		return next, cmd
	}
	// 端口被 Pier 之外的进程占着：既停不掉（不是我们的），也起不来（必然冲突），
	// 唯一正确的动作就是什么都不做，并说清楚原因。
	if st.PortOpen {
		m.setNotice(fmt.Errorf("端口被占用"),
			"%s 的端口 %d 被 Pier 之外的进程占用（可能是你自己的 dev server），已跳过",
			name, st.Service.Port)
		return m, nil
	}

	m.setNotice(nil, "正在启动 %s…%s", name, m.startHint(name))
	next, cmd := m.enqueue(uiJob{act: uiActStart, svc: st.Service})
	return next, cmd
}

func (m uiModel) restartSelected() (tea.Model, tea.Cmd) {
	st, ok := m.selected()
	if !ok || !m.loaded {
		m.setNotice(nil, "状态还在读取中，稍候再操作")
		return m, nil
	}
	name := st.Service.Name
	if m.pending(name) {
		m.setNotice(nil, "%s 已有操作在处理中，请稍候", name)
		return m, nil
	}
	m.setNotice(nil, "正在重启 %s…%s", name, m.startHint(name))
	next, cmd := m.enqueue(uiJob{act: uiActRestart, svc: st.Service})
	return next, cmd
}

// startAll 把所有「没在跑且端口空着」的服务排进队列。
// 跳过而非强行启动，与 cmdUp 的取舍一致：端口已响应的服务再起一个只会冲突。
func (m uiModel) startAll() (tea.Model, tea.Cmd) {
	if !m.loaded {
		m.setNotice(nil, "状态还在读取中，稍候再操作")
		return m, nil
	}
	jobs := make([]uiJob, 0, len(m.statuses))
	skipped := make([]string, 0)
	for _, st := range m.statuses {
		name := st.Service.Name
		switch {
		case m.pending(name):
			skipped = append(skipped, name+"(处理中)")
		case st.Running:
			skipped = append(skipped, name+"(已在运行)")
		case st.PortOpen:
			skipped = append(skipped, fmt.Sprintf("%s(端口 %d 被占用)", name, st.Service.Port))
		case st.Service.Manual:
			// 跳过就说出来。这一屏上没有别的地方写着它不参与全部启动，
			// 不说的话，「a」按下去少了一个，看着像漏了。
			skipped = append(skipped, name+"(不参与全部启停)")
		default:
			jobs = append(jobs, uiJob{act: uiActStart, svc: st.Service})
		}
	}
	if len(jobs) == 0 {
		m.setNotice(nil, "没有需要启动的服务。%s", uiSkipText(skipped))
		return m, nil
	}
	m.setNotice(nil, "依次启动 %d 个服务…%s", len(jobs), uiSkipText(skipped))
	next, cmd := m.enqueue(jobs...)
	return next, cmd
}

// stopAll 停掉所有 Pier 记录里在跑的服务。
// 只挑 Running 的：不在记录里的进程 Stop 也动不了，排进去只会刷一串无用提示。
func (m uiModel) stopAll() (tea.Model, tea.Cmd) {
	if !m.loaded {
		m.setNotice(nil, "状态还在读取中，稍候再操作")
		return m, nil
	}
	jobs := make([]uiJob, 0, len(m.statuses))
	skipped := make([]string, 0)
	for _, st := range m.statuses {
		name := st.Service.Name
		switch {
		case st.Running && m.pending(name):
			skipped = append(skipped, name+"(处理中)")
		// 与全部启动同一份名单：只排除启动的话，这里「x 停止全部」之后再「a 全部启动」，
		// 少的就是同一批服务，而且是在它没跑的时候才看得出来。
		case st.Running && st.Service.Manual:
			skipped = append(skipped, name+"(不参与全部启停)")
		case st.Running:
			jobs = append(jobs, uiJob{act: uiActStop, svc: st.Service})
		case st.PortOpen:
			// 明确说一句「外部占用」：用户以为 x 能把他自己开的前端也关掉，
			// 结果发现没关，不说清楚就会以为是 bug。
			skipped = append(skipped, fmt.Sprintf("%s(外部占用端口 %d，不归 Pier 管)", name, st.Service.Port))
		case st.Stale:
			skipped = append(skipped, name+"(进程已退出)")
		}
	}
	if len(jobs) == 0 {
		m.setNotice(nil, "没有由 Pier 启动的服务在运行。%s", uiSkipText(skipped))
		return m, nil
	}
	m.setNotice(nil, "依次停止 %d 个服务…%s", len(jobs), uiSkipText(skipped))
	next, cmd := m.enqueue(jobs...)
	return next, cmd
}

func uiSkipText(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return "（跳过 " + strings.Join(skipped, "、") + "）"
}

// enterLog 切到日志视图。先清空再读，避免旧服务的日志在加载期间残留。
//
// 看日志只读文件，不依赖运行状态，所以状态还没回来也放行：
// 服务至今没启动过时，日志文件本来就不存在，视图里会显示「暂无日志」。
func (m uiModel) enterLog() (tea.Model, tea.Cmd) {
	st, ok := m.selected()
	if !ok {
		m.setNotice(nil, "还没有可查看的服务")
		return m, nil
	}
	m.logMode = true
	m.logName = st.Service.Name
	// 最新的一份，不是今天那份：跨了零点还在跑的服务写的一直是启动那天的文件。
	m.logPath = proc.LogFile(m.cfg, st.Service.Name)
	m.logLines = nil
	m.logScroll = 0
	return m, m.loadLog()
}

func (m uiModel) updateLog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.logVisibleLines()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.logMode = false
		// 面板里可能刚发生过变化（比如服务自己退了），回去时顺手刷一次。
		next, cmd := m.requestRefresh()
		return next, cmd
	case "up", "k":
		if m.logScroll < len(m.logLines) {
			m.logScroll++
		}
	case "down", "j":
		if m.logScroll > 0 {
			m.logScroll--
		}
	case "pgup":
		m.logScroll += visible
		if m.logScroll > len(m.logLines) {
			m.logScroll = len(m.logLines)
		}
	case "pgdown":
		m.logScroll -= visible
		if m.logScroll < 0 {
			m.logScroll = 0
		}
	case "home":
		m.logScroll = len(m.logLines)
	case "end":
		m.logScroll = 0
	}
	return m, nil
}

// logVisibleLines 是日志视图正文区能放下的行数。
func (m uiModel) logVisibleLines() int {
	height := m.height
	if height <= 0 {
		height = 24
	}
	n := height - 6 // 标题、文件名、正文前后的空行、底部两行提示
	if n < 1 {
		n = 1
	}
	return n
}

func (m uiModel) View() string {
	if m.logMode {
		return uiPadHeight(m.logView(), m.height)
	}
	return uiPadHeight(m.tableView(), m.height)
}

func (m uiModel) tableView() string {
	width := m.width
	if width <= 0 {
		// 首帧还没收到 WindowSizeMsg，先按 80 列画，收到后会自动重画。
		width = 80
	}

	rows := make([][]string, 0, len(m.statuses))
	rowStyles := make([][]lipgloss.Style, 0, len(m.statuses))
	for i, st := range m.statuses {
		cells, styles := m.row(i, st)
		rows = append(rows, cells)
		rowStyles = append(rowStyles, styles)
	}

	// 列宽按全部行（含滚动后看不见的行）算，滚动时列宽才不会忽宽忽窄。
	widths := make([]int, len(uiHeaders))
	for i, h := range uiHeaders {
		widths[i] = runewidth.StringWidth(h)
	}
	for _, cells := range rows {
		for i, c := range cells {
			if w := runewidth.StringWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// 终端放不下时从最后一列往前丢：列的顺序就是重要性顺序，「说明」是唯一可牺牲的。
	cols := len(uiHeaders)
	for cols > 4 && uiTableWidth(widths[:cols]) > width {
		cols--
	}
	widths = widths[:cols]
	// 丢列还不够（终端极窄）就整体压缩列宽。宁可截断内容也不要换行：
	// 一旦某行折行，后面所有行的列对齐全乱了。
	uiShrinkWidths(widths, width)

	lines := []string{m.title(width)}
	if m.err != nil {
		lines = append(lines, uiErrStyle.Render(uiClip("读取失败："+m.err.Error(), width)))
	}
	lines = append(lines, "")

	if len(rows) == 0 {
		// 配置校验不允许零个服务，这里只是别让极端情况画出个空屏。
		lines = append(lines, uiDimStyle.Render("  配置里没有任何服务"))
	} else {
		lines = append(lines, uiTableRow(uiHeaders[:cols], widths, nil))
		sep := make([]string, cols)
		for i := range sep {
			sep[i] = strings.Repeat("-", widths[i])
		}
		lines = append(lines, uiDimStyle.Render(uiTableRow(sep, widths, nil)))

		// 内容比终端高时滚动，并保证选中行永远在可视区内——
		// 用键盘选中的东西跑到屏幕外，是最容易让人迷失的一种界面。
		start, end := m.visibleRange(len(rows))
		for i := start; i < end; i++ {
			lines = append(lines, uiTableRow(rows[i], widths, rowStyles[i]))
		}
		if end < len(rows) || start > 0 {
			lines = append(lines, uiHintStyle.Render(fmt.Sprintf("  （显示 %d-%d / 共 %d）", start+1, end, len(rows))))
		}
	}

	notice := m.notice
	if notice == "" {
		notice = "就绪"
		if !m.loaded {
			// 表格里的「读取中…」只说明单个服务，这行说明整体在干什么。
			notice = "正在读取服务状态…"
		}
	}
	noticeStyle := uiHintStyle
	if m.noticeErr {
		noticeStyle = uiErrStyle
	}
	lines = append(lines, "", noticeStyle.Render(uiClip(notice, width)))
	lines = append(lines, uiHintStyle.Render(uiClip(m.helpText(), width)))
	return strings.Join(lines, "\n")
}

func (m uiModel) title(width int) string {
	left := uiTitleStyle.Render("Pier 交互面板")
	right := uiHintStyle.Render("配置：" + m.cfg.Path)
	// 标题行也要按显示宽度截断，否则窄终端里路径会把整行顶得换行。
	return uiClip(left+"　"+right, width)
}

func (m uiModel) helpText() string {
	return "↑/↓ 选择　enter/s 启停　r 重启　l 日志　a 全部启动　x 全部停止　q 退出"
}

// visibleRange 返回表格应当显示的行区间 [start, end)。
func (m uiModel) visibleRange(total int) (int, int) {
	height := m.height
	if height <= 0 {
		height = 24
	}
	// 固定开销：标题、空行、表头、分隔、提示、帮助，再留一点余量。
	max := height - 10
	if max < 3 {
		max = 3
	}
	if total <= max {
		return 0, total
	}
	start := 0
	if m.cursor >= max {
		start = m.cursor - max + 1
	}
	if start > total-max {
		start = total - max
	}
	return start, start + max
}

// row 生成一行的单元格和逐列样式。
// 样式在这里定，是因为「状态」「健康」两列的颜色取决于状态本身，只有这里知道。
func (m uiModel) row(i int, st proc.Status) ([]string, []lipgloss.Style) {
	name := st.Service.Name

	marker := uiCursorOff
	nameStyle := uiDimStyle
	if i == m.cursor {
		marker = uiCursorOn
		nameStyle = uiCursorStyle
	}

	status, statusStyle := view.StatusText(st), uiStatusStyle(st)
	switch {
	case m.busy[name] != "":
		// 启停中的服务显示进行中的动作：Maven 编译要几十秒，
		// 这段时间状态列如果还停在旧值，会让人以为按键没生效。
		status, statusStyle = m.busy[name]+"…", uiBusyStyle
	case !m.loaded:
		// 状态还没回来时必须写明「读取中」。占位状态和「未启动」在结构上无法区分，
		// 直接按占位值画出来就等于告诉用户服务没起来——那是在撒谎。
		status, statusStyle = "读取中…", uiDimStyle
	}

	kind := m.kinds[name]
	if kind == "" {
		kind = "-"
	}
	health, healthStyle := uiHealthText(st)

	cells := []string{
		marker + name,
		kind,
		status,
		view.PortText(st),
		view.PIDText(st),
		view.UptimeText(st),
		health,
		view.NoteText(st),
	}
	styles := []lipgloss.Style{
		nameStyle,
		uiDimStyle,
		statusStyle,
		uiDimStyle,
		uiDimStyle,
		uiDimStyle,
		healthStyle,
		uiHintStyle,
	}
	return cells, styles
}

// uiStatusStyle 按 view.StateKey 取色。状态文案与颜色同源于一个状态键，
// 结构上不可能再出现「写着运行中、涂着红色」这种自相矛盾的界面。
func uiStatusStyle(st proc.Status) lipgloss.Style {
	switch view.StateKey(st) {
	case view.StateStarting:
		return uiWarnStyle
	case view.StateRunning:
		return uiOKStyle
	case view.StateStale:
		return uiErrStyle
	case view.StateExternal:
		return uiWarnStyle
	default:
		return uiDimStyle
	}
}

// uiHealthText 给出健康探针一列的文案。
// 「没配探针」「配了但服务没在跑」「探针没过」是三件不同的事，
// 不能都显示成「-」，否则看不出到底是没检查还是检查失败。
func uiHealthText(st proc.Status) (string, lipgloss.Style) {
	switch {
	case st.HasHealth && st.Healthy:
		return "✓ 通过", uiOKStyle
	case st.HasHealth:
		return "✗ 未通过", uiErrStyle
	case st.Service.Health != "":
		return "未检查", uiDimStyle
	default:
		return "-", uiDimStyle
	}
}

func (m uiModel) logView() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	lines := []string{uiTitleStyle.Render(uiClip(
		fmt.Sprintf("日志：%s", m.logName), width))}
	lines = append(lines, uiHintStyle.Render(uiClip("文件："+m.logPath, width)))

	visible := m.logVisibleLines()
	total := len(m.logLines)
	// logScroll 是「从末尾往回数被藏起来的行数」，和 tail 的心智一致：
	// 0 就是跟随最新，按上键才逐行往回翻。
	end := total - m.logScroll
	if end > total {
		end = total
	}
	if end < 0 {
		end = 0
	}
	start := end - visible
	if start < 0 {
		start = 0
	}

	switch {
	case len(m.logLines) == 0:
		lines = append(lines, "", uiDimStyle.Render("  （暂无日志，服务可能还没启动过）"))
	default:
		lines = append(lines, "")
		for _, l := range m.logLines[start:end] {
			// 日志行必须截断而不是让它折行：一折行，可视行数就不再等于行数，
			// 滚动和翻页会跟着错位。
			lines = append(lines, "  "+uiClip(strings.ReplaceAll(l, "\t", "    "), width-2))
		}
		// 用空白把正文区补满，滚动的提示行才不会跟着内容上下跳。
		for i := end - start; i < visible; i++ {
			lines = append(lines, "")
		}
	}

	pos := "跟随最新"
	if m.logScroll > 0 {
		pos = fmt.Sprintf("已上翻 %d 行", m.logScroll)
	}
	lines = append(lines, "", uiHintStyle.Render(uiClip(
		fmt.Sprintf("%s　共 %d 行", pos, total), width)))
	lines = append(lines, uiHintStyle.Render(uiClip(
		"↑/↓ 滚动　pgup/pgdn 翻页　home/end 到顶/到底　esc 返回　q 退出", width)))
	return strings.Join(lines, "\n")
}

// uiReadLog 读日志文件的最后 n 行。
// 日志按天分文件（一天一份，同一天里追加），单份不会无限长，整读再切行
// 比维护增量偏移简单得多，也不会因为文件被换掉而错位。
func uiReadLog(path string, n int) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 还没启动过，没有日志文件不是错误
		}
		return nil, err
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// uiTableRow 把一行单元格渲染成定宽字符串。样式传 nil 表示不着色。
//
// 必须先按显示宽度补齐、再着色：lipgloss 的样式会插入 ANSI 转义序列，
// 一旦先着色，runewidth 算出来的宽度就没法用了。
// 注意列数以 widths 为准而不是 cells：终端太窄时 cells 还是完整的一行，
// 但只有前 len(widths) 个会被画出来，末尾多补的间隔会把行顶出终端宽度、逼出折行。
func uiTableRow(cells []string, widths []int, styles []lipgloss.Style) string {
	last := len(widths)
	if len(cells) < last {
		last = len(cells)
	}
	var b strings.Builder
	for i := 0; i < last; i++ {
		cell := uiPad(uiClip(cells[i], widths[i]), widths[i])
		if i < len(styles) {
			cell = styles[i].Render(cell)
		}
		b.WriteString(cell)
		if i < last-1 {
			b.WriteString("  ")
		}
	}
	return b.String()
}

// uiTableWidth 是一行表格的总显示宽度（含列间两格间隔）。
func uiTableWidth(widths []int) int {
	if len(widths) == 0 {
		return 0
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	return total + 2*(len(widths)-1)
}

// uiShrinkWidths 在终端极窄时把列宽整体压到放得下：每次削当前最宽的一列，
// 每列留 4 格保底。削完仍放不下就由 uiClip 截断，至少不会把行弄折。
func uiShrinkWidths(widths []int, total int) {
	for uiTableWidth(widths) > total {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 4 {
			return
		}
		widths[widest]--
	}
}

// uiPad 按显示宽度补空格。中文是双宽字符，用 len() 补必然错位，
// 所以一律以 runewidth 的结论为准。
func uiPad(s string, w int) string {
	if n := w - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// uiClip 把过长的文本截到指定显示宽度，超出部分用省略号收尾。
func uiClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}

// uiPadHeight 把内容撑到终端底部。底部提示固定在最后一行，
// 表格长短变化时才不会整片上下跳。
func uiPadHeight(body string, height int) string {
	if height <= 1 {
		return body
	}
	// 少一行：末尾多出来的换行会把终端顶得滚动一行，画面反而抖得更厉害。
	if pad := height - 1 - (strings.Count(body, "\n") + 1); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	return body
}
