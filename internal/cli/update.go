package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/update"
	"github.com/zhengshangjinx/pier/internal/version"
	"github.com/zhengshangjinx/pier/internal/view"
)

const (
	// checkUpToDate 与 checkHasUpdate 是 `pier update --check` 的两种正常结论。
	// 10 与既有的 0（正常）/ 1（出错）/ 2（未知命令）都不撞：脚本只看退出码
	// 就能决定要不要接着做，不必去读那几行字。
	checkUpToDate  = 0
	checkHasUpdate = 10
)

// cmdUpdate 是自更新的入口。检查那一段与界面走的是同一个 update.Client，
// 只有「落在哪儿」按平台分。
func cmdUpdate(args []string) int {
	check := false
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		default:
			return fail("update 不认识参数 %s", a)
		}
	}

	c := update.New(update.Options{})
	if !check {
		return installUpdate(c)
	}
	res, err := c.Check(context.Background())
	if err != nil {
		return fail("%v", err)
	}
	return reportCheck(os.Stdout, res)
}

// installUpdate 真的把新版本装上。
//
// 与界面共用同一条代码路径：检查、挑产物、下载、校验、解压都走 internal/update，
// 只有最后「怎么落地」按平台分——unix 上当场改名覆盖（跑着也能换），Windows 上
// 自己那份 exe 锁着，交给助手等本进程退出之后再换。
func installUpdate(c *update.Client) int {
	w := os.Stdout

	// 先认安装位再出网：认不出来（从源码编的、go install 装的）时，该说的话
	// 与网络一点关系都没有，让用户白等一次请求只是徒劳。
	inst, err := update.Detect()
	if err != nil {
		return fail("%v", err)
	}
	// 写不动就到此为止，别下几十兆。这一步发生在下载之前，正是为了这个。
	if err := inst.CheckWritable(); err != nil {
		return fail("%v", err)
	}
	// 界面开着的时候不动手：Windows 上换不了正在运行的 exe，unix 上换得动，
	// 但会把一个跑着旧代码、指着一份新安装的界面留在那儿，症状怪异，不如拦住。
	if update.GUIRunning() {
		return fail("Pier 界面正在运行，先在界面里退出，或用界面里的「重启并安装」")
	}

	res, err := c.Check(context.Background())
	if err != nil {
		return fail("%v", err)
	}
	if res.Current == "dev" {
		// Detect 已经挡过这一类了，这里只是别让「已经是最新版本」这种话
		// 出现在一次什么都没比的比较之后。
		return fail("这一份是本机构建，没有版本号，不比较版本")
	}
	if !res.HasUpdate {
		fmt.Fprintf(w, "已经是最新版本（%s）。\n", res.Current)
		return 0
	}
	fmt.Fprintf(w, "有新版本 %s，正在下载。\n", res.Release.Version)

	asset, err := res.Release.Pick(inst.Kind)
	if err != nil {
		return fail("%v", err)
	}
	archive, err := c.Download(context.Background(), res.Release, asset, newProgress(w))
	if err != nil {
		return fail("%v", err)
	}
	clearLine(w)
	fmt.Fprintf(w, "下载完成：%s（%s）\n", filepath.Base(archive), view.Bytes(asset.Size))

	stage, err := update.Stage(c.Dir(), res.Release.Version, archive)
	if err != nil {
		return fail("%v", err)
	}

	plan := update.Plan{
		Version: res.Release.Version,
		Dir:     c.Dir(),
		Stage:   stage,
		Target:  inst.Target,
		Kind:    inst.Kind,
		// 有意不拉起来：命令行发起的替换不该凭空弹出一个界面。
	}
	if update.CanReplaceInPlace() {
		out := update.Apply(plan, update.NewLogger(w))
		if !out.OK {
			return fail("%s", out.Message)
		}
		fmt.Fprintf(w, "已经换成 %s 了。\n", out.Version)
		return 0
	}

	// 这一个平台上换不了正在运行的自己：交给助手，等本进程退出之后由它动手。
	// 锁先握在手上——助手等的正是「这把锁被放开」，也就是「这个进程退出了」。
	claim, err := update.BeginApply()
	if err != nil {
		return fail("%v", err)
	}
	keepClaim = claim
	logPath, err := update.Schedule(plan, "")
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(w, "%s 会在本进程退出之后换上；过程写在 %s 里。\n", res.Release.Version, logPath)
	fmt.Fprintf(w, "换完之后跑一次 pier version 就能确认。\n")
	return 0
}

// keepClaim 让那把锁一直握到进程退出。
//
// 有意不 Release：助手等的就是这把锁被放开，而在这里放开就等于告诉它「可以换文件了」，
// 可这时候本进程还在跑。放在包级变量里是因为它一旦被回收，os.File 的终结器会把
// 文件关掉——那和提前放锁是一回事。
var keepClaim *proc.Claim

// newProgress 在终端同一行上刷下载进度。写入口不是终端时返回 nil：
// 重定向到文件里时，那些回车会把它写成一堆重复的碎片。
func newProgress(w io.Writer) update.Progress {
	if !isTerminal(w) {
		return nil
	}
	last := -1
	return func(got, total int64) {
		if total <= 0 {
			return
		}
		pct := int(got * 100 / total)
		if pct == last {
			return
		}
		last = pct
		fmt.Fprintf(w, "\r下载中 %s / %s（%d%%）", view.Bytes(got), view.Bytes(total), pct)
	}
}

// clearLine 把进度那一行擦掉，好让后面的话从行首正常写起。
func clearLine(w io.Writer) {
	fmt.Fprintf(w, "\r%s\r", strings.Repeat(" ", 72))
}

// isTerminal 报告这个写入口背后是不是一个终端。
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// reportCheck 打印检查结果并给出退出码。
//
// 单独拆出来是为了能测：这一段的全部意义就是那几个退出码，而它默认要连真网络。
// 结论只有三种——已是最新、有新版本、没得出结果。第三种里除了网络不通，
// 还有「这一份本机构建没有版本号，比不了」：都是「这个结论别信」，合成一个码，
// 免得脚本要认第四个。
func reportCheck(w io.Writer, res update.Result) int {
	latest := res.Release.Version
	if latest == "" {
		// tag 认不出来时把原始 tag 摆出来：空着看着像检查没做成。
		latest = res.Release.Tag
	}
	rows := [][2]string{
		{"当前版本", res.Current},
		{"最新版本", latest},
	}
	if !res.Release.Published.IsZero() {
		rows = append(rows, [2]string{"发布时间", res.Release.Published.Local().Format("2006-01-02")})
	}
	if res.Release.URL != "" {
		rows = append(rows, [2]string{"发布说明", res.Release.URL})
	}
	writeKV(w, rows)
	fmt.Fprintln(w)

	// 本机构建没有版本号，比不了。这话要说在前面：否则「已经是最新版本」听着像
	// 认真比过了，而实际上什么都没比。
	if res.Current == "dev" {
		fmt.Fprintln(w, "这一份是本机构建，没有版本号，不比较版本。")
		return 1
	}
	if !res.HasUpdate {
		fmt.Fprintln(w, "已经是最新版本。")
		return checkUpToDate
	}
	fmt.Fprintf(w, "有新版本 %s，运行 pier update 安装。\n", res.Release.Version)
	writeInstallHint(w, version.Read())
	return checkHasUpdate
}

// writeInstallHint 说一句「这一份该由谁来升级」。
//
// 从源码编的、go install 装的这两类，替换文件没有意义：下一次 go build / go install
// 又把它们盖回去，而用户会以为升级成功了。所以这两类不给「我们替你换」这条路，
// 只说清楚该怎么做——界面上说的是同一句。
func writeInstallHint(w io.Writer, b version.Build) {
	if s := update.WhyNoSelfUpdate(b); s != "" {
		fmt.Fprintln(w, s)
	}
}

// writeKV 打印一组「标签  值」。标签按显示宽度对齐——标签是中文，
// 按字节数补空格会歪（一个汉字三个字节、只占两格）。
func writeKV(w io.Writer, rows [][2]string) {
	width := 0
	for _, r := range rows {
		if n := runewidth.StringWidth(r[0]); n > width {
			width = n
		}
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s%s  %s\n", r[0], strings.Repeat(" ", width-runewidth.StringWidth(r[0])), r[1])
	}
}
