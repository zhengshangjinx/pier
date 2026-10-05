package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
)

const (
	// ApplyVerb 是助手的隐藏动词。它不是给用户敲的，所以不进 pier --help，
	// 也不进任何一处用法说明；两个入口（命令行的分发、界面的 main）都在
	// 碰别的东西之前先认它一下。
	//
	// 助手跑的是**旧二进制**的代码——一次更新由上一版来安装。这正是要的性质：
	// 动手的永远是那份已经在用户机器上跑通过的代码。
	ApplyVerb = "_update-apply"

	// PlanName 是交给助手的那份「待会儿怎么做」。
	PlanName = "plan.json"

	// ResultName 是助手留下的结果。它跑在 Pier 已经退出的空档里，
	// 没有窗口能报错，这个文件是唯一能看到失败原因的通道。
	ResultName = "result.json"

	// helperSubdir 是助手那份副本放在版本目录的哪一层。
	helperSubdir = "helper"

	// stageSubdir 是解压出来的那棵树放在版本目录的哪一层。
	stageSubdir = "stage"

	// pollInterval 是助手等发起方退出的轮询间隔。
	pollInterval = 250 * time.Millisecond

	// waitTimeout 是这一等的上限。等不到就什么都不动——发起方还活着的时候
	// 换文件，换出来的是一份「旧进程指着新文件」的东西，比不换更糟。
	waitTimeout = 60 * time.Second
)

// Plan 描述一次替换的全部输入。它是发起方与助手之间唯一的合同：
// 助手是个新起的进程，除了这个文件没有别的东西可看。
type Plan struct {
	// Version 要装上去的版本号，只用于回报与日志。
	Version string `json:"version"`
	// Dir 是这条路线上的根目录（数据目录下的 cache/update）。下载、解压、
	// 计划、结果都从它推出来。
	Dir string `json:"dir"`
	// Stage 是解压好的那棵树。
	Stage string `json:"stage"`
	// Target 是安装位：macOS 整包是那个 .app，Windows 是安装根目录，
	// macOS 裸命令行是那个可执行文件，Linux 是装着两份可执行文件的那个目录。
	Target string `json:"target"`
	// Kind 是安装形态，决定 stage 里该找什么、怎么换。
	Kind Kind `json:"kind"`
	// Relaunch 是换完之后要拉回来的东西（整包是那个 .app，其余是可执行文件）。
	// 空表示不拉——命令行发起的替换就属于这种，用户在终端里自己再跑一次。
	Relaunch string `json:"relaunch,omitempty"`
	// ParentPID 是发起这次替换的进程，只用于日志。
	//
	// 助手等的不是一个 PID：判据是那把锁（见 waitForCallerExit）。
	ParentPID int `json:"parentPid,omitempty"`
}

// ApplyResult 是一次替换的结果，也是助手留给下一次启动的那句话。
type ApplyResult struct {
	// OK 报告这次替换成没成。
	OK bool `json:"ok"`
	// Message 是失败原因，或者成功时说的一句补充。要能直接摆给用户看。
	Message string `json:"message,omitempty"`
	// Version 是这次要装上去的版本号。
	Version string `json:"version,omitempty"`
	// RolledBack 报告失败之后有没有把原来的文件放回去。它为假时那次失败
	// 留下的是一份不确定的安装，说明里必须讲清楚现在是什么状态。
	RolledBack bool `json:"rolledBack,omitempty"`
	// When 是这件事发生的时刻。
	When time.Time `json:"when"`
}

// LogFunc 记一行。带时间戳的那一份由 NewLogger 提供。
type LogFunc func(format string, a ...any)

// NewLogger 把一个写入口包成「一行一句、前面带时刻」的记录口。
// 传 nil 得到一个什么都不做的，调用方不必处处判空。
func NewLogger(w io.Writer) LogFunc {
	if w == nil {
		return func(string, ...any) {}
	}
	return func(format string, a ...any) {
		fmt.Fprintf(w, "%s ", time.Now().Format("2006-01-02 15:04:05"))
		fmt.Fprintf(w, format, a...)
		fmt.Fprintln(w)
	}
}

// UpdateDir 返回下载、解压与计划文件的根目录。
func UpdateDir() (string, error) {
	d, err := config.Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Cache, "update"), nil
}

// VersionDir 返回某一版在这条路线上的目录。产物与解压出来的树都放在里面，
// 换完之后整棵 cache/update 都是随时可删的，下一次启动顺手清掉。
func VersionDir(dir, ver string) string { return filepath.Join(dir, ver) }

// StageDir 返回某一版解压出来的那棵树放在哪儿。
func StageDir(dir, ver string) string { return filepath.Join(VersionDir(dir, ver), stageSubdir) }

// Stage 把下好的产物解到这一版的目录里，返回那棵树的位置。
//
// 每次都从一个干净的目录开始：上一次没解完留下的半棵树，在它上面接着解，
// 出来的东西谁也说不清是哪些文件的组合——而下一步就是把它换到安装目录去。
func Stage(dir, ver, archive string) (string, error) {
	dst := StageDir(dir, ver)
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("清不掉上一次的解压结果：%w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("创建解压目录失败：%w", err)
	}
	if err := Extract(archive, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// ResultPath 返回结果文件的位置。它不带版本号：留下的只有最近一次那一份。
func ResultPath() (string, error) {
	dir, err := UpdateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ResultName), nil
}

// resultPath 是同一个位置，但按计划里记的根目录算——助手只拿得到计划文件，
// 于是它的所有落脚点都必须能从计划里推出来。
func (p Plan) resultPath() string { return filepath.Join(p.Dir, ResultName) }

// logPath 返回更新过程写的那份日志。位置与服务的日志同一个规矩：一天一份，
// 文件名就是那一天（见 config.LogDateLayout）。更新不是清单里的服务，
// 所以它不在 logs/<服务名>/ 那一堆里，自己占一层。
func logPath() (string, error) {
	d, err := config.Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Logs, "update", time.Now().Format(config.LogDateLayout)+".log"), nil
}

// check 报告这份计划能不能照着做。动手之前先把「缺什么」查清楚，
// 免得到了一半才发现文件不在——那时候已经没有干净的回退点了。
func (p Plan) check() error {
	switch {
	case p.Version == "":
		return errors.New("计划里没有版本号")
	case p.Dir == "":
		return errors.New("计划里没有落脚目录")
	case p.Target == "":
		return errors.New("计划里没有说清楚要换哪儿")
	}
	if fi, err := os.Stat(p.Stage); err != nil || !fi.IsDir() {
		return fmt.Errorf("解压出来的树不在 %s", p.Stage)
	}
	return nil
}

// ── 谁来做这件事 ───────────────────────────────────────────────────────────

// BeginApply 领下这次替换，返回一把必须由调用方一直握着的锁。
//
// 拿不到锁说明已经有一次替换在进行中。握住之后调用方就该准备退出了：
// 助手等的正是「这把锁被放开」——见 waitForCallerExit。
func BeginApply() (*proc.Claim, error) {
	path, err := applyLockPath()
	if err != nil {
		return nil, err
	}
	claim, ok, err := proc.TryClaim(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("已经有一次更新在进行中，等它做完再试")
	}
	return claim, nil
}

// CanReplaceInPlace 报告这份二进制能不能当场把运行中的自己换掉。
//
// unix 上可以：换的是目录项，跑着的进程手里那个 inode 不受影响。
// Windows 上不行：正在运行的 exe 是锁着的，改名都改不动，只能交给助手，
// 等本进程退出之后再由它动手（见 apply_windows.go）。
func CanReplaceInPlace() bool { return canReplaceInPlace() }

// Apply 按计划换文件，返回结果。它是这一整条路线上唯一动手的地方，
// 命令行与助手走的都是它——两条路上换文件的行为必须是同一份。
func Apply(p Plan, logf LogFunc) ApplyResult {
	if logf == nil {
		logf = NewLogger(nil)
	}
	res := ApplyResult{Version: p.Version, When: time.Now()}
	if err := p.check(); err != nil {
		res.Message = err.Error()
		logf("没法动手：%v", err)
		return res
	}
	rolled, err := applyPlatform(p, logf)
	res.RolledBack = rolled
	if err != nil {
		res.Message = err.Error()
		logf("失败：%v", err)
		if rolled {
			logf("原来的文件已经放回去了，安装目录没变。")
		}
		return res
	}
	res.OK = true
	logf("换好了，现在是 %s 版。", p.Version)
	return res
}

// Schedule 把这次替换交给一个脱离进程组的助手，返回过程日志的路径。
//
// 调用方在这之前必须先 BeginApply 拿到那把锁，而且拿到之后就该准备退出了：
// 助手动手的前提正是那把锁被放开。这里只负责把助手安置好，不动任何安装目录里的东西。
func Schedule(p Plan, self string) (logPathOut string, err error) {
	if err := p.check(); err != nil {
		return "", err
	}
	if self == "" {
		if self, err = os.Executable(); err != nil {
			return "", fmt.Errorf("认不出自己这份二进制在哪：%w", err)
		}
	}
	logFile, err := logPath()
	if err != nil {
		return "", err
	}
	dir := VersionDir(p.Dir, p.Version)
	helperDir := filepath.Join(dir, helperSubdir)
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		return "", fmt.Errorf("创建助手目录失败：%w", err)
	}
	helper := filepath.Join(helperDir, filepath.Base(self))
	// 复制而不是硬链：Windows 上同一个文件对象仍然算「正在运行」，硬链过去
	// 照样锁着要换的那份 exe；而且这份副本只活这一次，本来也没有共享的必要。
	if err := copyFile(self, helper); err != nil {
		return "", fmt.Errorf("安置助手失败：%w", err)
	}

	p.ParentPID = os.Getpid()
	planPath := filepath.Join(dir, PlanName)
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := config.WriteAtomic(planPath, append(raw, '\n')); err != nil {
		return "", fmt.Errorf("写计划文件失败：%w", err)
	}

	cmd := exec.Command(helper, ApplyVerb, planPath)
	// 新会话：调用方退出之后助手接着干，中间不能被终端或父进程的退出带走。
	proc.SetDetached(cmd)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("起不来更新助手：%w", err)
	}
	// 有意不 Wait：助手要活到调用方退出之后，而调用方马上就走。
	_ = cmd.Process.Release()
	return logFile, nil
}

// waitForCallerExit 等发起这次替换的那个进程退出，拿到之后那把锁就归助手了。
//
// 判据是锁不是 PID：锁由内核在进程退出的那一刻放开，连「还没被回收的僵尸」
// 也算已经退出；而 PID 会被系统复用，光比号码迟早认错人。同一把锁顺便把
// 「两个助手同时换文件」也挡住了。
func waitForCallerExit() (*proc.Claim, error) {
	path, err := applyLockPath()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		claim, ok, err := proc.TryClaim(path)
		if err != nil {
			return nil, err
		}
		if ok {
			return claim, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等了 %s，上一个 Pier 还没有退出，这次不换文件（安装目录没动）", waitTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// RunHelper 是隐藏动词的全部实现，返回进程退出码。
//
// 顺序是有讲究的：等发起方退出 → 换文件 → 写结果 → 再把人拉起来。
// 结果必须赶在拉起之前落盘：新起来的 Pier 一开机就读它，晚一步就是
// 「更新成功了却什么都没说」——而那句话是用户唯一能看到的东西。
func RunHelper(args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "用法：%s <%s>\n", ApplyVerb, PlanName)
		return 2
	}
	planPath := args[0]
	p, err := loadPlan(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读不了计划文件：%v\n", err)
		return 1
	}

	logFile, err := logPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "找不到日志目录：%v\n", err)
		return 1
	}
	f, err := openAppend(logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "写不了日志 %s：%v\n", logFile, err)
		return 1
	}
	defer f.Close()
	logf := NewLogger(f)
	logf("更新助手开始：%s（发起它的进程是 %d）", planPath, p.ParentPID)

	claim, err := waitForCallerExit()
	if err != nil {
		logf("%v", err)
		writeResult(p.resultPath(), ApplyResult{Message: err.Error(), Version: p.Version, When: time.Now()})
		return 1
	}
	// 握着不放：换文件的这段时间里不该有第二个助手插进来。
	defer claim.Release()

	res := Apply(p, logf)
	if err := writeResult(p.resultPath(), res); err != nil {
		logf("结果写不进去：%v", err)
	}
	if !res.OK {
		return 1
	}
	if p.Relaunch == "" {
		logf("这次不用把 Pier 拉回来。")
		return 0
	}
	if err := relaunch(p, logf); err != nil {
		// 文件已经换好了，但人没回来。把结果改成这句话再写一遍——
		// 这时候新的 Pier 还没起来，不必担心它读到半份。
		res.OK = false
		res.Message = "文件已经换好了，但没能把 Pier 拉起来：" + err.Error()
		writeResult(p.resultPath(), res)
		logf("%s", res.Message)
		return 1
	}
	logf("已经把 Pier 拉起来了。")
	if p.ParentPID > 0 {
		logf("（下一个启动的 Pier 会读到这份结果：%s）", p.resultPath())
	}
	return 0
}

// ── 计划与结果的读写 ───────────────────────────────────────────────────────

func loadPlan(path string) (Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// writeResult 把结果落盘。这是唯一的失败通道，所以它自己出问题只能回给调用方——
// 调用方至少还能写进日志。
func writeResult(path string, res ApplyResult) error {
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteAtomic(path, append(raw, '\n'))
}

// LoadResult 读上一次替换留下的结果。没有、读不动、读坏了都返回 ok 为假：
// 它要么不存在（没更新过），要么是上一次写到一半留下的，两种都不该拦住启动。
func LoadResult() (ApplyResult, bool) {
	path, err := ResultPath()
	if err != nil {
		return ApplyResult{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ApplyResult{}, false
	}
	var res ApplyResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return ApplyResult{}, false
	}
	return res, true
}

// ClearResult 把结果删掉。界面把这句话说给用户看过之后调它——
// 留着的话，下一次启动又会把那句早就说过的话再说一遍。
func ClearResult() error {
	path, err := ResultPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ── 两个平台都要用的小工具 ─────────────────────────────────────────────────

// openAppend 打开一个追加写的文件，目录不存在就建。
func openAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// startDetached 起一个脱离进程组的进程，然后把句柄放掉。
//
// 更新助手自己马上就要退出，被它拉起来的那个不能跟着一起走——起服务用的是
// 同一套做法（proc.SetDetached，unix 上是 setsid）。有意不 Wait：
// 等在这儿的话，这个刚起来的进程就成了助手的孩子，助手一退它就被带走了。
func startDetached(path string) error {
	cmd := exec.Command(path)
	proc.SetDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// copyFile 复制一个文件，权限位照原样（可执行位必须留住）。
//
// 先写到一个临时名字再改名过去：中途失败留下的是一个 .tmp，不会是一份
// 看起来像模像样的半截二进制——那东西一旦被执行，报的错和更新毫无关系。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// checkNotEmpty 检查一个文件在、不是空的，顺便可选地要求它有执行位。
// 「新树里少了一个东西」要在动手之前发现——换过去之后才发现，
// 换回来的就是一份打不开的安装。
func checkNotEmpty(path string, needExec bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("产物里缺少 %s", filepath.Base(path))
	}
	if fi.IsDir() {
		return fmt.Errorf("产物里的 %s 是目录，不是一个文件", filepath.Base(path))
	}
	if fi.Size() == 0 {
		return fmt.Errorf("产物里的 %s 是空的", filepath.Base(path))
	}
	if needExec {
		if fi.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("产物里的 %s 没有执行位", filepath.Base(path))
		}
	}
	return nil
}

// stageRoot 找出解压出来的那一层顶层目录。
//
// 三平台的产物都是「压缩包根上只有一个目录，里面才是东西」（见 package.sh），
// 但摊在根上的那种也认：打包方式变一次，这里不该跟着变。
func stageRoot(stage, marker string) (string, error) {
	if exists(filepath.Join(stage, marker)) {
		return stage, nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", fmt.Errorf("读不了解压目录：%w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if cand := filepath.Join(stage, e.Name()); exists(filepath.Join(cand, marker)) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("解压出来的产物里找不到 %s", marker)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// probeWritable 用一个临时文件试着往 dir 里写一下。
//
// 只看权限位是不够的：只读挂载、ACL、被系统保护的目录在模式位上都看不出问题，
// 真写一次才知道。这一步要发生在用户点「重启并安装」之前——等他点了、Pier 退了、
// 才发现写不进去，那就只剩一句解释了。
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".pier-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}
