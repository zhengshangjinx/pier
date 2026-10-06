// Package manage 是 Pier 的「清单编辑」业务层。
//
// 界面上能做的编辑动作都收在这里：新增/修改/删除应用、分组增删改、端口占用排查与
// 清场、目录识别与启动方案推导。这一层不碰任何界面技术——它返回 Go 值和人话消息，
// 由宿主决定怎么呈现（图形界面包成 JSON，命令行可以直接打表格）。
//
// 写操作的铁律：只写 pier.user.yaml，pier.yaml 一个字节都不改。
// 手工维护的那份清单是开发者已确认的环境事实，工具只读它。
package manage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/proc"
	"github.com/zhengshangjinx/pier/internal/view"
)

// ErrNoConfig 表示清单还没加载出来，此时任何需要清单的动作都做不了。
var ErrNoConfig = errors.New("尚未加载服务清单")

// Manager 持有当前清单，并在改完覆盖文件后请宿主重新加载。
//
// 它不自己加载清单：加载清单还牵着进程监管器、运行态这些宿主自己的东西，
// 不该由这一层知道。reload 由宿主注入，语义是「磁盘变了，把你手上那份换掉」。
type Manager struct {
	mu     sync.Mutex
	cfg    *config.Config
	reload func(path string) error
	// busy 由宿主注入：这个服务身上有没有还没结束的动作（排队中、编译中、启动中）。
	// 没装就是「永远不忙」。
	busy func(name string) bool
}

// New 建一个 Manager。reload 可以为 nil，那样写盘后就只是不刷新内存。
func New(reload func(path string) error) *Manager {
	return &Manager{reload: reload}
}

// SetBusyProbe 装上「这个服务身上有没有正在进行的动作」的判断，由宿主注入。
//
// 删除要看它。状态文件只记已经拉起来的进程，编译中的服务在那里没有记录，
// 光看状态文件会以为它没在跑——删掉定义之后那次启动照常完成，于是留下一个
// 界面上没有、命令行也停不掉的进程。宿主手上的动作簿记是唯一能看见这个窗口的地方。
func (m *Manager) SetBusyProbe(fn func(name string) bool) {
	m.mu.Lock()
	m.busy = fn
	m.mu.Unlock()
}

// SetConfig 由宿主在每次成功加载清单后调用。
func (m *Manager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// Config 返回当前清单。清单一旦加载完就当作只读，所以可以放心在锁外使用。
func (m *Manager) Config() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// reloadFrom 写盘之后重新加载，让内存里那份跟上磁盘。
func (m *Manager) reloadFrom(cfg *config.Config) error {
	if m.reload == nil {
		return nil
	}
	return m.reload(cfg.Path)
}

// lookup 取服务和它所属的清单。
func (m *Manager) lookup(name string) (*config.Service, *config.Config, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, nil, ErrNoConfig
	}
	svc, err := cfg.Find(name)
	if err != nil {
		return nil, nil, err
	}
	return svc, cfg, nil
}

// storeFor 从磁盘读出最新的数据文件，改动一律建立在它之上而不是内存里那份：
// 两者不一致时（比如命令行刚改过），以磁盘为准才不会把别处的改动盖掉。
func (m *Manager) storeFor() (*config.Config, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}
	if !cfg.IsStore() {
		return nil, fmt.Errorf("当前用的是 YAML 清单 %s，Pier 不改写它；去掉 --config 就会使用 Pier 自己的数据", cfg.Path)
	}
	return config.LoadStore(cfg.Path)
}

// commit 写回数据文件并让宿主重新加载。
func (m *Manager) commit(st *config.Config) error {
	if err := st.Save(); err != nil {
		return err
	}
	if err := m.reloadFrom(st); err != nil {
		return errors.New("已保存，但重新加载失败：" + err.Error())
	}
	return nil
}

func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

// busyFn 返回宿主装上的忙闲判断；没装就是「永远不忙」。
func (m *Manager) busyFn() func(string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy == nil {
		return func(string) bool { return false }
	}
	return m.busy
}

// runningPID 返回状态文件里这个服务记着的进程号；没在跑（或压根没记录）时为 0。
//
// 判的是「进程还在不在」而不是「状态文件里有没有这一条」：CrashLoop 之后
// 留下的死记录不该让服务变成删不掉的。
func runningPID(cfg *config.Config, name string) (int, error) {
	st, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		return 0, err
	}
	e := st.Services[name]
	if e == nil || !proc.ProcessAlive(e.PID) {
		return 0, nil
	}
	return e.PID, nil
}

// ── 端口占用：查出是谁占着，以及能不能把它清掉 ──────────────────────────────

// PortOwnerOut 是「端口被谁占了」这个问题的完整答案。
type PortOwnerOut struct {
	OK      bool            `json:"ok"`
	Msg     string          `json:"msg"`
	Service string          `json:"service"`
	Port    int             `json:"port"`
	Owner   *proc.PortOwner `json:"owner"`
}

// PortOwner 查出一个服务的端口此刻被谁监听。
func (m *Manager) PortOwner(name string) (*PortOwnerOut, error) {
	svc, _, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	if svc.Port <= 0 {
		return nil, fmt.Errorf("服务 %s 没有配置端口，无法判断占用情况", name)
	}
	owner, err := proc.PortOwnerOf(svc.Port)
	if err != nil {
		return nil, fmt.Errorf("端口 %d 上查不到监听进程（可能刚被释放）：%v", svc.Port, err)
	}
	out := &PortOwnerOut{OK: true, Service: name, Port: svc.Port, Owner: owner}
	// 先判断它是不是 Pier 自己起的：是的话正确的动作是「停止」，
	// 直接结束进程会在状态文件里留下一条指向已死进程的假记录。
	//
	// 状态文件读不出来时不能当成「不是自家服务」——这里只是「查不了」，
	// 而下面那个「结束进程」会再拦一次，所以先把话说清楚，别让人以为可以放心动手。
	managed, err := m.managedName(owner.PID)
	if err != nil {
		out.Msg = fmt.Sprintf("状态文件读不出来，无法确认 PID %d 是不是 Pier 自己启动的：%v", owner.PID, err)
		return out, nil
	}
	if managed != "" {
		owner.Managed, owner.Service = true, managed
	}
	return out, nil
}

// managedName 返回该 PID 对应的 Pier 服务名，不是 Pier 启动的则返回空。
//
// 判定交给 proc.ManagedName：那里是按进程组认的，端口握在子进程手里也认得出来。
// 这里不再自己写一份——两份判定迟早对不上，界面就会在「是自家服务」时
// 给一个「结束进程」，那会在状态文件里留下一条指向已死进程的假记录。
//
// 第二个返回值是「读不出状态文件」。它必须和「不是自家服务」分开：以前这两种
// 都回空串，于是状态文件一读不了，护栏就整条失守——界面会给自己起的服务
// 递上一个「结束进程」。分不清的时候就别动手。
func (m *Manager) managedName(pid int) (string, error) {
	cfg := m.Config()
	if cfg == nil {
		return "", ErrNoConfig
	}
	st, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		return "", err
	}
	return proc.ManagedName(st, pid), nil
}

// KillPortOwner 结束占着端口的那个进程。
//
// pid 与 started 都来自界面此前拿到的详情，started 用于核对「还是那一个进程」。
// 这里不信任界面传来的任何东西：动手前重新查一遍端口占用，确认 PID 确实是
// 此刻占着这个端口的那个，再走 KillExternal 的护栏。多这一步是因为界面上的
// 状态可能已经过期几十秒，而过期状态加上 PID 复用正是误杀的标准配方。
func (m *Manager) KillPortOwner(name, pidRaw, started string) (string, error) {
	svc, _, err := m.lookup(name)
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidRaw))
	if err != nil || pid <= 0 {
		return "", errors.New("无效的进程号")
	}
	if svc.Port <= 0 {
		return "", fmt.Errorf("服务 %s 没有配置端口", name)
	}

	cur, err := proc.PortOwnerOf(svc.Port)
	if err != nil {
		return "", fmt.Errorf("端口 %d 上已经没有监听进程了", svc.Port)
	}
	if cur.PID != pid {
		return "", fmt.Errorf("端口 %d 现在由 PID %d 占用，不是界面上的 PID %d，已放弃这次操作",
			svc.Port, cur.PID, pid)
	}
	managed, err := m.managedName(cur.PID)
	if err != nil {
		return "", fmt.Errorf("状态文件读不出来，无法确认 PID %d 是不是 Pier 自己启动的服务，已放弃这次操作：%v", cur.PID, err)
	}
	if managed != "" {
		return "", fmt.Errorf("PID %d 是 Pier 启动的服务 %s，请用「停止」而不是结束进程", cur.PID, managed)
	}

	if err := proc.KillExternal(pid, started); err != nil {
		return "", err
	}
	// 进程没了不等于端口立刻空出来：内核回收套接字需要一点点时间，
	// 而报「已结束」之后用户马上就会去点启动，这里等一下再回报更实在。
	if !proc.WaitPortReleased(svc.Port, 3*time.Second) {
		return fmt.Sprintf("已结束 PID %d，但端口 %d 仍被监听，可能有子进程还占着它", pid, svc.Port), nil
	}
	return fmt.Sprintf("已结束 PID %d，端口 %d 现已空出", pid, svc.Port), nil
}

// ── 添加 / 修改 / 删除应用 ─────────────────────────────────────────────────
//
// 全部写进 Pier 自己的数据文件（见 config.Store）。一个服务只在一处，
// 改就是改、删就是删，不再有「手写清单删不掉、只能隐藏」的中间态。

// ServiceIn 是界面提交的一份服务定义。
type ServiceIn struct {
	Name   string `json:"name"`
	Dir    string `json:"dir"`
	Group  string `json:"group"`
	Kind   string `json:"kind"`
	Run    string `json:"run"`
	Build  string `json:"build"`
	Module string `json:"module"`
	Script string `json:"script"`
	Port   int    `json:"port"`
	Health string `json:"health"`
	Note   string `json:"note"`
	// Env 与 Toolchain 来自表单。为 nil 表示调用方没管这两项（比如命令行），保留原值。
	// Toolchain 按类别（java / maven / node / python / go）给出服务上指定的 SDK 路径，
	// 值为空串表示不指定、按规则自动选；没出现的类别保留原值。
	Env       map[string]string `json:"env"`
	Toolchain map[string]string `json:"toolchain"`
	// DependsOn 是启动顺序上的前置服务名。和 Env 一样，为 nil 表示调用方没管这一项，
	// 保留原值；传空数组才表示「把前置清掉」——JSON 里 null 与 [] 本来就分得开，
	// 不利用这一点的话，「编辑备注」会把依赖悄悄抹掉。
	DependsOn []string `json:"dependsOn"`
	// Restart 是重启策略，空串表示不自动重启（也是默认值），所以不需要保留语义：
	// 表单每次都把它发全，缺省就等于用户没要。
	Restart string `json:"restart"`
	// Manual 表示它不参与全部启停。开关没有第三态，表单每次都把它发全，
	// 所以也不需要「这次没带这一项」那种保留语义。
	Manual bool `json:"manual"`
	// WatchOn 是「改完自动重启」那个开关；WatchInclude 是点名要盯的模式，
	// 留空表示按类型给一套默认（见 config.WatchPatterns）。
	//
	// 这两项与清单里的那一行不是同一形状：清单里是 `true` 或一串模式（`Watch`
	// 自己管着那两种写法的来回），而表单里是两个控件，各自发各自的。
	WatchOn      bool     `json:"watchOn"`
	WatchInclude []string `json:"watchInclude"`
	// OrigName 是「这次提交之前它叫什么」。表单里名称那一栏是可以改的，改了名字
	// 的那一次提交必须先改名再覆盖保存：直接按新名字 Upsert 会多出一条，旧的那条
	// 原样留在清单里，而用户以为自己只是改了个名字。
	//
	// 空串表示没改名（新增时也留空）。
	OrigName string `json:"origName"`
}

func (in ServiceIn) toService() *config.Service {
	return &config.Service{
		Name: strings.TrimSpace(in.Name), Dir: strings.TrimSpace(in.Dir),
		Group: strings.TrimSpace(in.Group), Kind: strings.TrimSpace(in.Kind),
		Run: strings.TrimSpace(in.Run), Build: strings.TrimSpace(in.Build),
		Module: strings.TrimSpace(in.Module), Script: strings.TrimSpace(in.Script),
		Port: in.Port, Health: strings.TrimSpace(in.Health), Note: strings.TrimSpace(in.Note),
		Restart: strings.TrimSpace(in.Restart), Manual: in.Manual,
		Watch: watchOf(in),
	}
}

// watchOf 把表单那两个控件收成清单里的那一行。
//
// 关掉开关就整个丢掉：留一个 `Include: ["src/**"]` 在盘上，下次打开表单会把它
// 填回去，看着像是「这服务盯着 src」——而它此刻并没有在盯。
func watchOf(in ServiceIn) config.Watch {
	if !in.WatchOn {
		return config.Watch{}
	}
	return config.Watch{On: true, Include: cleanPatterns(in.WatchInclude)}
}

// cleanPatterns 收拾一份模式单：去空白、去空串、去重，保持原本的先后。
//
// 去重和 cleanDeps 是同一个理由：同一句写两遍在界面上看不出来，
// 而它会让「盯着的模式」那一栏凭空长出一行重复的。
func cleanPatterns(in []string) []string {
	var out []string
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// cleanDeps 收拾一份前置清单：去空白、去空名、去重，保持原本的先后。
//
// 去重不是为了省事：界面上那句「将先启动 a、a」是照原样拼的，
// 重名一进去就会显示成这样，而用户根本分不清那是两条还是一条。
func cleanDeps(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SaveService 新增或修改一个服务。
//
// 收 ServiceIn 而不是一段 JSON 字符串：解析提交内容的格式是各个宿主的边界职责
// （网页界面收字符串、原生界面收对象），业务层只该看见已经成形的字段。
func (m *Manager) SaveService(in ServiceIn) (string, error) {
	svc := in.toService()

	if msg := validServiceName(svc.Name); msg != "" {
		return "", errors.New(msg)
	}
	if svc.Dir == "" {
		return "", errors.New("请填写项目目录")
	}
	if strings.HasPrefix(svc.Dir, "~") {
		return "", errors.New("目录不支持 ~，请写绝对路径或相对工作空间的路径")
	}
	if svc.Port < 0 || svc.Port > 65535 {
		return "", errors.New("端口必须在 1-65535 之间")
	}

	st, err := m.storeFor()
	if err != nil {
		return "", err
	}

	// 编辑时改了名字：先在手里这份清单上改掉，再照新名字覆盖保存，两件事合成
	// 一次 commit。分两次写的话，改名成功而保存失败会留下「名字是新的、内容是旧的」
	// 这种中间态，而用户看到的是一句失败提示——两边的说法对不上。
	//
	// 必须在端口查重之前：查重是按名字把自己排除掉的，还挂着旧名字的那一条
	// 会被当成别人、报「端口已经被服务 <自己> 用了」。
	renamed := false
	orig := strings.TrimSpace(in.OrigName)
	if orig != "" && orig != svc.Name {
		if err := m.renameGuard(st, orig); err != nil {
			return "", err
		}
		if err := st.RenameService(orig, svc.Name); err != nil {
			return "", err
		}
		renamed = true
	}

	// 端口不许和别的服务撞。不查的话，第二个服务起不来，报的却是「端口已被占用
	// （可能已在 IDEA 或其它终端运行）」——占着它的正是自己的另一个服务，
	// 这句话会把人引到完全错误的方向去。
	//
	// 查在这里而不是 validateServices：那个校验加载与保存共用，加进去会让一份
	// 已经带着重复端口的清单直接加载不了，人连改回来的机会都没有。
	// 端口 0 表示没配，可以重复；编辑自己时不算冲突。
	if svc.Port > 0 {
		for _, other := range st.Services {
			if other.Name != svc.Name && other.Port == svc.Port {
				return "", fmt.Errorf("端口 %d 已经被服务 %s 用了，换一个", svc.Port, other.Name)
			}
		}
	}

	// 环境变量与 JDK：调用方没传就原样沿用，传了就以传的为准。
	// 保存是整条替换，照 ServiceIn 新建一条写进去时不带上旧值，就等于把它们抹掉——
	// 只改一个备注也会让 demo-admin 丢掉 APP_ENV=dev、shop-admin 丢掉钉住的 JDK。
	// toolchain 只改提交里出现的类别，没出现的沿用。
	var oldEnv, oldTool map[string]string
	var oldDeps []string
	if old, err := st.Find(svc.Name); err == nil {
		oldEnv, oldTool, oldDeps = old.Env, old.Toolchain, old.DependsOn
	}
	svc.DependsOn = oldDeps
	if in.DependsOn != nil {
		svc.DependsOn = cleanDeps(in.DependsOn)
	}
	svc.Env = oldEnv
	if in.Env != nil {
		svc.Env = cleanEnv(in.Env)
	}
	svc.Toolchain = map[string]string{}
	for k, v := range oldTool {
		svc.Toolchain[k] = v
	}
	for k, v := range in.Toolchain {
		if v = strings.TrimSpace(v); v != "" {
			svc.Toolchain[k] = v
		} else {
			delete(svc.Toolchain, k)
		}
	}
	if len(svc.Toolchain) == 0 {
		svc.Toolchain = nil
	}
	for k := range svc.Env {
		if !envKeyRe.MatchString(k) {
			return "", fmt.Errorf("环境变量名 %q 不合法：只能用字母、数字、下划线，且不能以数字开头", k)
		}
	}

	// 用真实的启动方案校验一遍：命令推不出来的服务，存进去也只是个点不动的按钮。
	probe := *svc
	if !filepath.IsAbs(probe.Dir) {
		probe.Dir = filepath.Join(st.Dir(), probe.Dir)
	}
	plan, err := probe.Plan(st)
	if err != nil {
		return "", errors.New("这个目录没法推导出启动命令：" + err.Error())
	}
	if svc.Kind == "" {
		svc.Kind = plan.Kind
	}

	st.Upsert(svc)
	st.AddGroup(svc.Group)
	if err := m.commit(st); err != nil {
		return "", err
	}
	// 日志目录与编译产物是按名字放的，改名之后要跟着搬。放在 commit 之后：
	// 这一步出问题只在回执尾巴上记一句，不把「已经保存好了」翻成一次失败。
	if renamed {
		return fmt.Sprintf("已保存 %s（%s）%s", svc.Name, plan.String(),
			m.moveServiceAssets(st, orig, svc.Name)), nil
	}
	return fmt.Sprintf("已保存 %s（%s）", svc.Name, plan.String()), nil
}

// validServiceName 校验服务名，返回空串表示通过。
//
// 新增、改名、复制三处共用这一份：名字不只是个标签，它还是 logs/<名字>/ 这个
// 目录名、cache/bin/<名字> 这个文件名、命令行 up/down 的实参。空格和斜杠在
// 那几处各有各的麻烦，不如一开始就不让写。
func validServiceName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "请填写服务名"
	}
	if strings.ContainsAny(name, " \t/\\") {
		return "服务名不能包含空格或斜杠"
	}
	return ""
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cleanEnv 去掉键两端的空白与空键；值原样保留（值里的空格可能是有意的）。
func cleanEnv(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ClearHealth 去掉一个服务的健康检查地址，之后只看进程是否存活。
//
// 存在这一条是因为「探针没过」和「服务没起来」是两件事，却常常一起来：
// 添加应用时按目录内容推出来的地址（config.DeriveFacts）是个猜测，猜错了
// 就会一直探不通，而服务其实跑得好好的。改这一处不该逼人去开编辑表单——
// 那里要填的是十条字段，而他要改的只是一个「不需要」。
//
// 只动清单，不影响正在跑的进程：下次刷新状态时探针就不再打了。
func (m *Manager) ClearHealth(name string) (string, error) {
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	svc, err := st.Find(name)
	if err != nil {
		return "", err
	}
	if svc.Health == "" {
		return fmt.Sprintf("%s 本来就没有配健康检查", name), nil
	}
	svc.Health = ""
	if err := m.commit(st); err != nil {
		return "", err
	}
	return fmt.Sprintf("已去掉 %s 的健康检查，之后只看进程是否存活", name), nil
}

// DeleteService 删除一个服务。只删定义，不碰项目目录里的任何文件。
//
// 正在跑的服务拒绝删除。删掉定义之后进程还在，而界面上已经没有那一行能点
// 「停止」了：它只能去活动监视器手工杀，状态文件里还留着一条指向它的记录，
// 之后的每次「清理残留记录」都得重判一次。要删就先停，两边都不会留下对不上的账。
func (m *Manager) DeleteService(name string) (string, error) {
	cfg := m.Config()
	if cfg == nil {
		return "", ErrNoConfig
	}
	// 先看动作簿记，再看状态文件：编译中的服务只在簿记里露面。
	if m.busyFn()(name) {
		return "", fmt.Errorf("服务 %s 正在启动或停止中，等这一步结束再删除", name)
	}
	if pid, err := runningPID(cfg, name); err != nil {
		return "", err
	} else if pid > 0 {
		return "", fmt.Errorf("服务 %s 正在运行（PID %d），先停止再删除：pier down %s", name, pid, name)
	}
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	if !st.Remove(name) {
		return "", fmt.Errorf("没有名为 %s 的服务", name)
	}
	if err := m.commit(st); err != nil {
		return "", err
	}
	return fmt.Sprintf("已删除 %s（项目目录里的文件没有动）", name), nil
}

// ── 拖动排序 ───────────────────────────────────────────────────────────────
//
// 界面把「看得见的这几行现在的顺序」整份送过来，这一层转给 config 的部分重排。
// 为什么不是「把谁挪到谁前面」：筛选或分组页上，落点前后可能夹着看不见的服务，
// 「前面」指哪个位置就说不清了——整份送过来则没有歧义，界面看到什么就是什么。

// MoveServices 按 names 的顺序重排这几个服务。
func (m *Manager) MoveServices(names []string) (string, error) {
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	if err := st.ReorderServices(names); err != nil {
		return "", err
	}
	if err := m.commit(st); err != nil {
		return "", err
	}
	// 拖动是个高频动作，回执只在报错时有用，成功时不弹提示（界面那边也不弹）。
	return "已调整顺序", nil
}

// MoveGroups 按 names 的顺序重排这几个分组。
func (m *Manager) MoveGroups(names []string) (string, error) {
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	if err := st.ReorderGroups(names); err != nil {
		return "", err
	}
	if err := m.commit(st); err != nil {
		return "", err
	}
	return "已调整分组顺序", nil
}

// ── 分享与迁移 ─────────────────────────────────────────────────────────────
//
// 导出的是清单里的定义，不是界面上那份投影：ServiceOut 是给显示用的一层
// （端口是字符串、还标着运行状态和端口占用），照它拼出来的清单会带上 Pier
// 的运行状态，贴到别人机器上就不对了。

// ServiceYAML 把一条服务渲染成一段能贴进 pier.yaml 的片段（「⋯ → 复制成 YAML」）。
func (m *Manager) ServiceYAML(name string) (string, error) {
	svc, _, err := m.lookup(name)
	if err != nil {
		return "", err
	}
	return config.ServiceYAML(svc)
}

// ExportYAML 把整份清单渲染成一份可以直接拿去用的 pier.yaml。
func (m *Manager) ExportYAML() (string, error) {
	cfg := m.Config()
	if cfg == nil {
		return "", ErrNoConfig
	}
	return cfg.ExportYAML()
}

// ── 共享环境变量 ───────────────────────────────────────────────────────────
//
// 顶层那组变量是这份清单的属性，不是某一个服务的：改一处，所有服务下次启动
// 都拿到新的值。

// SaveSharedEnv 整份覆盖清单顶层的共享变量。
//
// 整份替换而不是逐条增删：界面上它就是一块多行文本，用户按下保存时手里拿的是
// 他自己写的那一整份。逐条合并的话，「删掉一行」与「他压根没写这一行」在数据上
// 长得一模一样，而这两种意图的结果正好相反。
func (m *Manager) SaveSharedEnv(env map[string]string) (string, error) {
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	// 名字当场校验：${} 只认 [A-Za-z_][A-Za-z0-9_]*，收下一个做不到的名字，
	// 等于答应了一件谁也没法兑现的事——写的人要到启动失败时才知道。
	for k := range env {
		if !config.ValidEnvName(k) {
			return "", fmt.Errorf("%s 不能当变量名（只能用字母、数字、下划线，且不以数字开头）", k)
		}
	}
	if len(env) == 0 {
		env = nil // 一条不剩就整段去掉，数据文件里不留一个空对象
	}
	st.Env = env
	if err := m.commit(st); err != nil {
		return "", err
	}
	if env == nil {
		return "已清空共享环境变量", nil
	}
	return fmt.Sprintf("已保存 %d 条共享环境变量，服务下次启动时生效", len(env)), nil
}

// ── 改名与复制 ─────────────────────────────────────────────────────────────

// renameGuard 是改名的一道闸：正在启停中的、正在运行的，一律不许改名。
//
// 改名只有一条路——在编辑表单里改名字（提交的还是一次 SaveService，只是带上
// OrigName），所以这道闸挂在 SaveService 上。改名等于换了个服务：进程、日志、
// 状态文件全按名字记账，改完名之后那一行进程就再也没有入口了（它还在往
// logs/<旧名>/ 里写）。要改就先停——这条和删除是同一个理由。
func (m *Manager) renameGuard(st *config.Config, name string) error {
	if m.busyFn()(name) {
		return fmt.Errorf("服务 %s 正在启动或停止中，等这一步结束再改名", name)
	}
	pid, err := runningPID(st, name)
	if err != nil {
		return err
	}
	if pid > 0 {
		return fmt.Errorf("服务 %s 正在运行（PID %d），先停止再改名：pier down %s", name, pid, name)
	}
	return nil
}

// moveServiceAssets 收拾改名留下的两处按名字存放的东西，返回要接在回执后面的
// 说明（没事发生就是空串）。
//
// 出问题时只记一句说明、不返回错误：改名本身已经落盘了，把它报成失败会让界面
// 显示「改名失败」，而列表里明明已经是新名字。
func (m *Manager) moveServiceAssets(cfg *config.Config, oldName, newName string) string {
	var notes []string
	oldDir, newDir := cfg.LogDirFor(oldName), cfg.LogDirFor(newName)
	if _, err := os.Stat(oldDir); err == nil {
		if _, err := os.Stat(newDir); err == nil {
			notes = append(notes, fmt.Sprintf("logs/%s 已经有同名目录，旧日志留在原处", newName))
		} else if err := os.Rename(oldDir, newDir); err != nil {
			notes = append(notes, "旧日志没能搬过去："+err.Error())
		}
	}
	// cache/bin/<名字> 是 node 服务的可执行文件（一个硬链），名字变了就再也用不上。
	// 下次启动会照新名字重新链一个，这里顺手清掉旧的。
	if err := os.Remove(filepath.Join(cfg.BinDir(), oldName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		notes = append(notes, "旧的编译产物没删掉："+err.Error())
	}
	if len(notes) == 0 {
		return ""
	}
	return "（" + strings.Join(notes, "；") + "）"
}

// DuplicateOut 是「复制一份」的结果。
//
// 名字单独给出来而不是夹在一句话里：界面拿到之后要把表单打开在这个新服务上，
// 从「已复制为 demo-admin-copy2」这句话里再抠出名字，迟早会随文案一起坏掉。
type DuplicateOut struct {
	Name string `json:"name"`
	Msg  string `json:"msg"`
}

// DuplicateService 复制一个服务。
//
// 端口重新挑一个：两个服务写同一个端口不会立刻报错，等启动时才会以「端口已被
// 占用（可能已在 IDEA 或其它终端运行）」的形式炸出来，而占着它的正是刚复制出来的
// 那个兄弟。健康检查地址同理——它八成写着旧端口，留着只会让新服务一直探不通，
// 所以直接清掉（不配健康检查时只看进程存活，这是诚实的默认）。
//
// 日志目录不复制：那是运行记录，不属于定义。
func (m *Manager) DuplicateService(name string) (*DuplicateOut, error) {
	st, err := m.storeFor()
	if err != nil {
		return nil, err
	}
	src, err := st.Find(name)
	if err != nil {
		return nil, err
	}
	newName := ""
	for i := 1; ; i++ {
		try := name + "-copy"
		if i > 1 {
			try = fmt.Sprintf("%s-copy%d", name, i)
		}
		if _, err := st.Find(try); err != nil {
			newName = try
			break
		}
	}

	cp := *src
	cp.Name = newName
	cp.Health = ""
	cp.Env = copyMap(src.Env)
	cp.Toolchain = copyMap(src.Toolchain)

	msg := "已复制为 " + newName
	if cp.Port > 0 {
		used := map[int]bool{}
		for _, p := range st.UsedPorts() {
			used[p] = true
		}
		// 从原服务的端口往后找：复制出来的那份通常在原来那个旁边，好认。
		if p := proc.FreePort(cp.Port+1, used); p > 0 {
			cp.Port = p
		} else {
			cp.Port = 0
			msg += "，但本机没有空端口了，端口留空自己填"
		}
	}
	st.Upsert(&cp)
	if err := m.commit(st); err != nil {
		return nil, err
	}
	return &DuplicateOut{Name: newName, Msg: msg}, nil
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ── 分组的新增 / 重命名 / 删除 ─────────────────────────────────────────────
//
// 分组本身不存成员，成员完全由服务的 group 字段决定。这里做的三件事都只是
// 在改那个字段（以及「有哪些分组」的那份声明），所以永远不会出现
// 「分组说有 3 个、点开只有 2 个」这种两处记录对不上的情况。

// CreateGroup 新建一个空分组。
func (m *Manager) CreateGroup(name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := validGroupName(name); err != "" {
		return "", errors.New(err)
	}
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	for _, g := range st.AllGroups() {
		if g == name {
			return "", fmt.Errorf("分组「%s」已经存在", name)
		}
	}
	st.AddGroup(name)
	if err := m.commit(st); err != nil {
		return "", err
	}
	return fmt.Sprintf("已新建分组「%s」，接下来可以把应用加进去", name), nil
}

// RenameGroup 给分组改名，成员跟着走。
func (m *Manager) RenameGroup(oldName, newName string) (string, error) {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" {
		return "", errors.New("没有指定要改名的分组")
	}
	if err := validGroupName(newName); err != "" {
		return "", errors.New(err)
	}
	if oldName == newName {
		return "分组名没有变化", nil
	}
	if oldName == config.UngroupedName {
		return "", fmt.Errorf("「%s」是内置分组，改不了；要给这些服务分组，请直接编辑它们的分组字段",
			config.UngroupedName)
	}
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	for _, g := range st.AllGroups() {
		if g == newName {
			return "", fmt.Errorf("分组「%s」已经存在，换一个名字", newName)
		}
	}
	n := st.RenameGroup(oldName, newName)
	if err := m.commit(st); err != nil {
		return "", err
	}
	return fmt.Sprintf("已把分组「%s」改名为「%s」，%d 个应用跟着移动", oldName, newName, n), nil
}

// DeleteGroup 删除分组。成员不会被一起删掉，而是退回「未分组」：
// 删分组和删应用是两回事，顺手删掉别人的服务是不可接受的。
func (m *Manager) DeleteGroup(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("没有指定要删除的分组")
	}
	if name == config.UngroupedName {
		return "", fmt.Errorf("「%s」是内置分组，删不掉", config.UngroupedName)
	}
	st, err := m.storeFor()
	if err != nil {
		return "", err
	}
	n := st.RemoveGroup(name)
	if err := m.commit(st); err != nil {
		return "", err
	}
	if n == 0 {
		return fmt.Sprintf("已删除空分组「%s」", name), nil
	}
	return fmt.Sprintf("已删除分组「%s」，其中 %d 个应用已移到「%s」", name, n, config.UngroupedName), nil
}

// validGroupName 校验分组名，返回空字符串表示通过。
func validGroupName(name string) string {
	if name == "" {
		return "请填写分组名"
	}
	// 分组名会出现在侧栏和命令行输出里，换行和制表符只会带来麻烦。
	if strings.ContainsAny(name, "\n\r\t") {
		return "分组名不能包含换行或制表符"
	}
	if len([]rune(name)) > 24 {
		return "分组名太长了，控制在 24 个字以内"
	}
	return ""
}

// ── 添加应用时的引导 ───────────────────────────────────────────────────────

// InspectOut 是「填了个目录，接下来该怎么填」的答案。
type InspectOut struct {
	OK      bool   `json:"ok"`
	Msg     string `json:"msg"`
	AbsPath string `json:"absPath"`
	RelPath string `json:"relPath"`
	// Found 列出在目录里实际找到的标志文件，让「识别为 go」这件事有据可查。
	Found []string `json:"found"`
	Kind  string   `json:"kind"`
	// Plan 是推导出的启动方案，存之前先让人看一眼将要执行什么。
	Plan string `json:"plan"`
	// Existing 是已占用端口，供界面避开。
	Existing []int `json:"existing"`
	// SuggestPort 是建议使用的端口：项目自己声明的那个，读不到才退回一个空闲端口。
	SuggestPort int `json:"suggestPort"`
	// PortFrom 说明 SuggestPort 是从哪儿来的，让这个数有据可查；
	// 由 FreePort 兜底时为空，界面据此说明「这只是个没被占用的端口」。
	PortFrom string `json:"portFrom"`
	// Health 是按端口推出来的健康检查地址，空表示推不出来。
	Health string `json:"health"`
	// SuggestName 是由目录名转出来的服务名，供界面预填。
	SuggestName string `json:"suggestName"`
	// Names 是已有的服务名，界面据此提示重名。
	Names []string `json:"names"`
	// Groups 是已有的分组，供下拉选择。
	Groups []string `json:"groups"`
	// Adopted 说明这次是从哪个正在跑的进程纳管来的（如「PID 1234（node）」），
	// 空表示是用户手填目录进来的。界面据此说一句「照这个进程推的」，
	// 并提醒它此刻还在跑、保存之后要先停掉才能由 Pier 接管。
	Adopted string `json:"adopted"`
}

// InspectHint 是表单上已经填好的、会影响启动方式的几栏。
//
// 推导启动命令不能只看目录：Java 多模块工程必须知道跑哪个子模块，手写了 run 的
// 就更不该再推。以前这里只看目录，于是编辑一个早就配好 module 的 Java 服务，
// 也会报「必须给出 module」——表单里明明填着。
type InspectHint struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Module string `json:"module"`
	Run    string `json:"run"`
	Build  string `json:"build"`
	Script string `json:"script"`
}

// InspectDir 判定一个目录是什么项目、能不能推导出启动命令。
//
// 目录不存在之类的情况不算错误：那正是界面要告诉用户的信息，所以照样返回
// 一份带 Msg 的结果。只有「清单没加载」「目录没填」这种没法继续的情况才报错。
func (m *Manager) InspectDir(dir string, hint InspectHint) (*InspectOut, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("请填写项目目录")
	}
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}

	abs := dir
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cfg.Dir(), abs)
	}
	abs = filepath.Clean(abs)

	out := &InspectOut{AbsPath: abs, RelPath: cfg.RelTo(abs)}
	out.Names = cfg.Names()
	out.Groups = cfg.Groups()
	out.Existing = cfg.UsedPorts()
	// 服务名由目录名转出来，界面拿它预填。纯中文之类的目录名转完是空串，
	// 那就干脆不给建议——留空让人自己填，好过预填一个空字符串。
	out.SuggestName = config.SanitizeName(filepath.Base(abs))

	fi, err := os.Stat(abs)
	if err != nil {
		out.Msg = "这个目录不存在。可以点右侧的「浏览…」从访达里选一个文件夹，也可以直接填相对清单目录的路径"
		return out, nil
	}
	if !fi.IsDir() {
		out.Msg = "这是一个文件，不是目录。请填项目所在的文件夹"
		return out, nil
	}

	for _, mk := range []struct {
		kind  string
		files []string
	}{
		{config.KindGo, []string{"go.mod"}},
		{config.KindJava, []string{"pom.xml", "build.gradle", "build.gradle.kts"}},
		{config.KindPython, []string{"pyproject.toml", "requirements.txt", "main.py"}},
		{config.KindNode, []string{"package.json"}},
	} {
		for _, f := range mk.files {
			if fileExists(filepath.Join(abs, f)) {
				out.Found = append(out.Found, f)
				if out.Kind == "" {
					out.Kind = mk.kind
				}
			}
		}
	}

	if out.Kind == "" {
		out.Msg = "没认出项目类型。目录里没有 go.mod / pom.xml / package.json / pyproject.toml，" +
			"请手动选择类型并填写启动命令"
		// 类型都没认出来，也就无从知道去哪读端口，只能退回空闲端口。
		out.SuggestPort = proc.FreePort(8080, portSet(out.Existing))
		out.OK = true
		return out, nil
	}

	// 试推一次启动方案。这一步才是真正的「能不能跑起来」，
	// 识别出类型只是识别出类型——比如 Python 目录里没有 main.py 就推不出命令。
	kind := out.Kind
	if k := strings.TrimSpace(hint.Kind); k != "" {
		kind = k
	}
	name := strings.TrimSpace(hint.Name)
	if name == "" {
		name = out.SuggestName
	}
	probe := &config.Service{Name: name, Dir: abs, Kind: kind,
		Module: strings.TrimSpace(hint.Module), Run: strings.TrimSpace(hint.Run),
		Build: strings.TrimSpace(hint.Build), Script: strings.TrimSpace(hint.Script)}
	if plan, err := probe.Plan(cfg); err == nil {
		out.Plan = plan.String()
	} else if kind == config.KindJava && probe.Module == "" && probe.Run == "" {
		// Java 推不出来几乎总是缺子模块。直接把 pom.xml 里声明的模块列出来，
		// 比一句「必须给出 module」有用得多——用户往往不记得模块叫什么。
		out.Msg = "识别为 Java（Maven）工程，还需要指定跑哪个子模块：在下方「高级」里填写 Maven 子模块"
		if mods := mavenModules(abs); len(mods) > 0 {
			out.Msg += "，这个工程里有：" + strings.Join(mods, "、")
		}
	} else {
		out.Msg = "识别为 " + kind + "，但推导启动命令失败：" + err.Error()
	}

	// 端口优先读项目自己的声明，而不是挑一个空闲端口塞给它。
	//
	// 这两件事看着都像「给个端口」，其实是两回事：项目在 config.yaml /
	// application.yml / .env 里写的那个端口是它非用不可的——启动脚本、服务发现、
	// 前端代理都按它来，Pier 换一个，服务自己照样listen原来的，健康探针就永远探不通。
	// 所以能读到就照抄，并且在 PortFrom 里写明出处；读不到才退回空闲端口，
	// 那种情况下 PortFrom 是空的，界面据此说明它只是个没被占用的端口。
	// Maven 多模块工程的端口写在子模块自己的配置里，指定了子模块就去那里读。
	factsDir := abs
	if probe.Module != "" {
		if sub := filepath.Join(abs, probe.Module); fileExists(filepath.Join(sub, "pom.xml")) {
			factsDir = sub
		}
	}
	if f := config.DeriveFacts(factsDir, kind); f.Port > 0 {
		out.SuggestPort, out.PortFrom, out.Health = f.Port, f.PortFrom, f.Health
	} else {
		out.SuggestPort = proc.FreePort(8080, portSet(out.Existing))
	}
	out.OK = true
	return out, nil
}

// mavenModules 读出 pom.xml 里 <modules> 声明的子模块名。读不到就返回空，
// 这只是给提示用的，不参与任何推导。
func mavenModules(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range mavenModuleRe.FindAllSubmatch(raw, -1) {
		if name := strings.TrimSpace(string(m[1])); name != "" {
			out = append(out, name)
		}
	}
	return out
}

var mavenModuleRe = regexp.MustCompile(`<module>\s*([^<\s]+)\s*</module>`)

func portSet(ports []int) map[int]bool {
	mp := make(map[int]bool, len(ports))
	for _, p := range ports {
		mp[p] = true
	}
	return mp
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// PortCandCount 是一次列给用户挑的候选端口个数。
// 给一批而不是一个：端口撞不撞车常常取决于同机别的项目，一次列几十个
// 让人自己挑，比每次只推一个、不合适再点一次要快得多。
const PortCandCount = 48

// portCandScan 是扫描范围：从起点往后看这么多端口。
// 扫太远没有意义，端口号差出两千个反而不好记。
const portCandScan = 2000

// PortCandOut 是端口选择的候选列表。
type PortCandOut struct {
	OK   bool `json:"ok"`
	From int  `json:"from"`
	// Free 是可以直接用的端口，按从小到大排列。
	Free []int `json:"free"`
	// Used 是清单里已经写掉的端口。它们此刻可能没在监听，但一启动就会撞车，
	// 所以必须和「被别人占着」区分开——两种冲突的解决办法完全不同。
	Used []int `json:"used"`
	// Taken 是扫描范围内正被某个进程监听的端口。
	Taken []int `json:"taken"`
	// ScanTo 是这一趟真正看过的最后一个端口，界面据此说明「只看了这一段」。
	// 它不等于 from+portCandScan：找满一批可用端口就停了，看过的往往远没那么多。
	ScanTo int `json:"scanTo"`
	// Hints 是常见端口的用途备注，纯提示，Pier 不据此做任何判断。
	Hints map[string]string `json:"hints"`
}

// portHints 是几个约定俗成的开发端口。写成提示而不是规则：
// 端口用途没有任何强制力，写死成规则只会在别人项目上闹笑话。
var portHints = map[string]string{
	"3000": "前端 dev server 常用（React/Vue/Next）",
	"3001": "前端 dev server 次选",
	"4200": "Angular 默认",
	"5000": "macOS 上常被 AirPlay 接收器占用",
	"5173": "Vite 默认",
	"5432": "PostgreSQL 默认",
	"6379": "Redis 默认",
	"8000": "Django / Python http.server 常用",
	"8080": "通用 HTTP 备用端口",
	"8081": "通用 HTTP 备用端口",
	"8888": "Jupyter 常用",
	"9000": "通用备用端口",
	"9090": "通用备用端口",
}

// PortCandidates 列出从 from 开始的一批可用端口，供界面弹窗选择。
func (m *Manager) PortCandidates(fromRaw string) (*PortCandOut, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}
	from, err := strconv.Atoi(strings.TrimSpace(fromRaw))
	if err != nil || from <= 0 || from > 65000 {
		from = 8080
	}

	// 一次问清本机所有监听端口，而不是对每个候选端口跑一次探测：
	// 列 48 个端口就要 48 次 lsof 的话，弹窗会明显地卡一下。
	listening := proc.ListeningInfo()
	used := portSet(cfg.UsedPorts())

	out := &PortCandOut{From: from, Hints: portHints,
		Free: []int{}, Used: []int{}, Taken: []int{}}
	// scanned 记的是这一趟真正看过的最后一个端口，不是「本来打算看到哪儿」。
	//
	// 找满 PortCandCount 个可用端口就收工了，那通常发生在前几十个号里；这时
	// 报一个 from+portCandScan 的上界，界面那句「只看了 X–Y 这一段」就是假的：
	// 用户会以为更靠后的端口也查过了，而那里到底有没有被占，这次根本没看。
	scanned := from
	for p := from; p <= 65535 && len(out.Free) < PortCandCount; p++ {
		if p > from+portCandScan {
			break
		}
		scanned = p
		_, taken := listening[p]
		if taken {
			out.Taken = append(out.Taken, p)
			continue
		}
		if used[p] {
			out.Used = append(out.Used, p)
			continue
		}
		out.Free = append(out.Free, p)
	}
	out.ScanTo = scanned
	out.OK = true
	return out, nil
}

// ── 端口发现 ───────────────────────────────────────────────────────────────
//
// 与上面的 PortCandidates 是相反的一问：那个问「有哪些端口可以给我用」，
// 这个问「此刻开着的是些什么」。后者是「我想管的东西已经在跑了，只是还没进清单」
// 那一步的入口——照着正在跑的进程把服务建出来，比照着记忆一笔一笔填表单准。

// ScannedPort 是端口扫描里的一行：一个正被监听的端口，以及它背后是什么。
type ScannedPort struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
	// Command 是监听进程的名字，如 node、java。
	Command string `json:"command"`
	User    string `json:"user"`
	// Dir 是监听进程的工作目录，查不到时为空。它是「这大概是个什么项目」的判断依据。
	Dir string `json:"dir"`
	// DirShort 是 Dir 的展示形式（主目录缩成 ~），与命令行共用同一个字符串。
	// 界面拿它渲染，拿 Dir 去开目录、去比对，两件事不要混。
	DirShort string `json:"dirShort"`
	// Service 是它对应的 Pier 服务名，空表示不属于 Pier。
	Service string `json:"service"`
	// Managed 为真表示这正是 Pier 启动的某个服务。
	Managed bool `json:"managed"`
	// Origin 是「谁把它拉起来的」，认不出来时为 nil。
	Origin *proc.Origin `json:"origin,omitempty"`
	// Known 是清单里那条目录正好是这个目录的服务名，空表示清单里没有。
	// 与 Service 不是一回事：那个说「此刻是 Pier 起的」，这个说「清单里有它的位置」。
	Known string `json:"known"`
}

// PortScanOut 是「本机此刻开着哪些端口」的答案。
type PortScanOut struct {
	OK    bool          `json:"ok"`
	Msg   string        `json:"msg"`
	Ports []ScannedPort `json:"ports"`
}

// ScanPorts 列出本机正在监听的 TCP 端口与各自背后的进程。
//
// 一次 lsof 问全表，再一次性把工作目录与来源查出来：逐个端口各问一遍的话，
// 一屏十几行就是几十次 exec，点开这一屏要等上好几秒。
func (m *Manager) ScanPorts() (*PortScanOut, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}
	// 先看依赖的系统命令在不在。不在的话下面拿到的是空表，而空表在这件事上
	// 等于「本机什么也没在跑」——那是个和事实相反、又看不出哪里不对的结论。
	if err := proc.PortToolsAvailable(); err != nil {
		return &PortScanOut{Msg: err.Error()}, nil
	}
	listening := proc.ListeningInfo()
	if listening == nil {
		return &PortScanOut{Msg: "读不到本机的监听端口列表"}, nil
	}

	pids := make([]int, 0, len(listening))
	for _, l := range listening {
		if l.PID > 0 {
			pids = append(pids, l.PID)
		}
	}
	dirs := proc.WorkDirs(pids)
	origins := proc.Origins(pids)

	state, err := proc.LoadState(cfg.StatePath())
	if err != nil {
		// 认不出自家服务不影响这一屏能不能用，只是少一列判断，照常列出来。
		state = nil
	}
	byDir := make(map[string]string, len(cfg.Services))
	for _, s := range cfg.Services {
		byDir[s.AbsDir()] = s.Name
	}

	out := &PortScanOut{OK: true, Ports: make([]ScannedPort, 0, len(listening))}
	for port, l := range listening {
		row := ScannedPort{Port: port, PID: l.PID, Command: l.Command, User: l.User,
			Dir: dirs[l.PID], DirShort: view.ShortPath(dirs[l.PID]), Known: byDir[dirs[l.PID]]}
		if l.PID > 0 {
			if name := proc.ManagedName(state, l.PID); name != "" {
				row.Managed, row.Service = true, name
			}
			if o, ok := origins[l.PID]; ok {
				row.Origin = &o
			}
		}
		out.Ports = append(out.Ports, row)
	}
	// 按端口升序。不按「能不能纳管」分堆：那个判断在这一屏上看得到（就是有没有
	// 那几列），而按它排序会让同一屏的顺序随进程起落跳来跳去。
	sort.Slice(out.Ports, func(i, j int) bool { return out.Ports[i].Port < out.Ports[j].Port })
	return out, nil
}

// AdoptPort 把一个已经在跑的端口收进清单。
//
// 它只推导、不落盘：照监听进程的工作目录走一遍 InspectDir，把结果当作表单的
// 预填值交回去，用户看一眼、改一改、点了保存才算数。
//
// 不直接建服务的理由很实在：从目录推出的东西只对「一个目录一个服务」的项目成立，
// 而 monorepo、多模块工程到处都是——那种目录照样推得出命令，起的却不是他要的那个。
// 表单是用户唯一能改这些的地方，把猜测摆进去让他确认，好过事后去删一条错的。
func (m *Manager) AdoptPort(portRaw, name string) (*InspectOut, error) {
	cfg := m.Config()
	if cfg == nil {
		return nil, ErrNoConfig
	}
	port, err := strconv.Atoi(strings.TrimSpace(portRaw))
	if err != nil || port <= 0 || port > 65535 {
		return nil, errors.New("无效的端口")
	}
	if err := proc.PortToolsAvailable(); err != nil {
		return nil, err
	}
	listening := proc.ListeningInfo()
	l, ok := listening[port]
	if !ok || l.PID <= 0 {
		return nil, fmt.Errorf("端口 %d 上已经没有监听进程了", port)
	}
	// 动手之前重新查一遍「此刻占着这个端口的是谁」，而不是信界面上带过来的那一行：
	// 那一屏可能已经过去几十秒，而这期间进程完全可能退出、端口被另一个人接走。
	if managed, err := m.managedName(l.PID); err == nil && managed != "" {
		return nil, fmt.Errorf("端口 %d 上的进程就是 Pier 的服务 %s，它已经在清单里了", port, managed)
	}

	dir := proc.WorkDirs([]int{l.PID})[l.PID]
	out := &InspectOut{OK: false, Names: cfg.Names(), Groups: cfg.Groups(), Existing: cfg.UsedPorts()}
	if dir == "" {
		out.Msg = fmt.Sprintf("查不到 PID %d 的工作目录（可能是别的用户的进程，或者它已经不在了），请手动填写项目目录", l.PID)
		return out, nil
	}

	// 目录交给 InspectDir 走同一条识别路径：纳管与手动添加必须得出同一个结果，
	// 各推一份的话，同一个目录从两个入口进去会看到两套不同的启动方式。
	out, err = m.InspectDir(dir, InspectHint{Name: name})
	if err != nil {
		return nil, err
	}
	// 端口强制用它正在监听的这个，而不是项目自己声明的那个：纳管的语义就是
	// 「它已经在这么跑了，照它现在的样子记下来」。项目里那个端口可能是它读环境变量
	// 之后的默认值，也可能是另一个 profile 用的，跟眼前这个进程没关系。
	out.SuggestPort, out.PortFrom = port, "正在监听的端口"
	out.Adopted = fmt.Sprintf("PID %d（%s）", l.PID, l.Command)
	return out, nil
}
