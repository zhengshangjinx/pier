package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
	"github.com/zhengshangjinx/pier/internal/update"
)

// 这一组是清单的增删改：add / rm / edit / group。
//
// 落盘一律走 internal/manage 里那几条现成的方法，一条判据都不另写——端口撞车、
// 目录存不存在、正在跑的服务不许删、改名的同时搬日志目录，规则全长在那里，
// 界面与命令行共用同一份。这一层只负责把命令行上的字变成那几个参数，
// 再把结果说成人话。

// serviceFlagNames 是 add 与 edit 共用的一张参数表。
//
// 认哪些参数由这一份说了算，两处各写一张迟早会有一边多一个；报错时也照着它说
// 「认识哪些」，用户不必去翻帮助。
var serviceFlagNames = []string{
	"name", "dir", "kind", "port", "health", "group", "run", "build", "module", "script", "note",
}

// manifestManager 打开清单，准备一个能写的 Manager。
//
// reload 传 nil：命令行这边改完就退出，没有内存里那份要刷新。能不能写由
// storeFor 判（--config 指定的 YAML 会被它拦住并说明原因），这里不另判一次。
func manifestManager(cfgPath string) (*manage.Manager, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	mgr := manage.New(nil)
	mgr.SetConfig(cfg)
	return mgr, nil
}

// windowHint 提醒一句「界面要重开窗口才看得见」。
//
// 读清单的是常驻的界面进程，而它只在启动与切换清单时读一次（panel.Load 只有
// 那两处）。所以命令行刚加的这一条，已经开着的窗口里还没有——这句是唯一说得清
// 那件事的地方，不说的话，用户会以为自己哪一步没做对。
//
// 窗口没开着就一个字都不说：一句用不上的话只会让人以为哪里出了毛病。
func windowHint() {
	if update.GUIRunning() {
		fmt.Println("Pier 界面正开着：它要重开窗口才会读到这次改动。")
	}
}

// parseFlags 把「--名字 值」形式的一串参数收成一张表，位置参数另列。
//
// 这一组要接十来个形状完全一样的开关，逐个手写 if 会把真正有区别的那几处
// （谁覆盖谁、空值是什么意思）淹掉。这里只管配对，那几处判断留在各自的命令里。
//
// 「没给」是键不在表里，「给了空串」是键在、值是空的——`--note ""` 要的就是后者，
// 所以不能拿值本身当有没有给过。
func parseFlags(rest []string) (map[string]string, []string, error) {
	vals := map[string]string{}
	var pos []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		if !strings.HasPrefix(a, "--") {
			return nil, nil, fmt.Errorf("只认 -- 开头的开关，收到的是 %q", a)
		}
		name, val, has := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if name == "" {
			return nil, nil, fmt.Errorf("%q 里没写开关名", a)
		}
		if !has {
			if i+1 >= len(rest) {
				return nil, nil, fmt.Errorf("--%s 后面要跟一个值", name)
			}
			i++
			val = rest[i]
		}
		vals[name] = val
	}
	return vals, pos, nil
}

// unknownFlag 从参数表里挑出一个不认识的开关，挑不出来就返回空串。
//
// 排序之后取第一个：map 的遍历顺序是随机的，同一条命令两次跑出两个不同的错字，
// 读的人会以为自己写错了哪一处。
func unknownFlag(vals map[string]string, known ...string) string {
	bad := make([]string, 0, len(vals))
	for k := range vals {
		if !slices.Contains(known, k) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return bad[0]
}

// parsePort 解析 --port 的值。
//
// 端口是命令行上最容易写错的一个数，报错要把收到的东西原样说回去：只说一句
// 「端口不合法」，写的人看不出是多打了一个空格还是少了一位数。
func parsePort(raw string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("要写一个 1-65535 之间的端口号，收到的是 %q", raw)
	}
	return p, nil
}

// cmdAdd 认出一个目录里的项目，连定义一起加进清单。
//
// 「猜」那一部分一律走 manage.InspectDir：与界面上「填完目录自动带出类型、端口、
// 启动方式」是同一份代码。两个宿主各写一遍的话，命令行加得进来的项目界面加不进来，
// 而用户会以为是界面的毛病。
func cmdAdd(args []string) int {
	cfgPath, rest := extractConfig(args)
	vals, pos, err := parseFlags(rest)
	if err != nil {
		return fail("add：%v", err)
	}
	if k := unknownFlag(vals, serviceFlagNames...); k != "" {
		return fail("add 不认识开关 --%s（看帮助：pier add -h）", k)
	}
	if len(pos) > 1 {
		return fail("add 只接一个目录，多出来的是：%s", strings.Join(pos[1:], "、"))
	}
	// 参数先验完再去看目录：端口写错了却先报一句「没认出项目类型」，
	// 会把人引到目录上去找问题，而毛病在手上这条命令里。
	port, hasPort := 0, false
	if v, ok := vals["port"]; ok {
		p, err := parsePort(v)
		if err != nil {
			return fail("--port %v", err)
		}
		port, hasPort = p, true
	}
	dir := vals["dir"]
	if len(pos) == 1 {
		if strings.TrimSpace(dir) != "" {
			return fail("目录给了两次：%s 与 --dir %s，留一个", pos[0], dir)
		}
		dir = pos[0]
	}
	if strings.TrimSpace(dir) == "" {
		// 不带参数就从当前目录猜，这是这个动词最顺手的用法：站在项目里敲一句
		// pier add，它自己认出来这是什么。
		dir = "."
	}
	// 相对路径按用户所在的这个目录解，不按清单目录：命令行上的 . 就是他此刻站的地方，
	// 而 InspectDir 与 SaveService 收相对路径时是按清单目录解的（那是界面那条路）。
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fail("解析目录失败：%v", err)
	}
	// 先在命令行这一侧把目录本身验掉（与 pier detect 同一句），不让 InspectDir 那两句
	// 界面话冒出来：「可以点右侧的『浏览…』」在终端里指不到任何东西，
	// 而它后半句「填相对清单目录的路径」在这个动词上正好是反的——命令行上的相对路径
	// 是按当前目录解的（见上）。
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return fail("不是有效目录：%s", abs)
	}

	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	insp, err := mgr.InspectDir(abs, manage.InspectHint{
		Name: vals["name"], Kind: vals["kind"], Module: vals["module"],
		Run: vals["run"], Build: vals["build"], Script: vals["script"],
	})
	if err != nil {
		return fail("%v", err)
	}
	// 上面那一次 stat 已经兜掉了 OK 为假的两条路（目录不在、那是个文件），这一句是给
	// 以后新加的理由留的：说一句界面话，也好过拿着半份结果往下走。
	if !insp.OK {
		return fail("%s", insp.Msg)
	}
	// 推不出启动命令就别往下走了。SaveService 也会拦住，但它那里报的是「这个目录
	// 没法推导出启动命令：…」，而手上这句更具体（Java 缺子模块时它把模块名列出来了）。
	if insp.Plan == "" {
		return fail("%s\n  命令行上：类型用 --kind、启动命令用 --run、Java 的子模块用 --module", insp.Msg)
	}

	name := strings.TrimSpace(vals["name"])
	if name == "" {
		name = insp.SuggestName
	}
	if name == "" {
		return fail("目录名 %s 转不出可用的服务名，用 --name 起一个", filepath.Base(abs))
	}

	svc := &config.Service{
		Name: name, Dir: abs, Group: strings.TrimSpace(vals["group"]), Kind: vals["kind"],
		Module: vals["module"], Run: vals["run"], Build: vals["build"],
		Script: vals["script"], Port: insp.SuggestPort, Health: insp.Health,
		Note: vals["note"],
	}
	if hasPort {
		// 换了端口，探针里的端口跟着换（见 config.WithPort）。不换的话它会去探项目
		// 原来那个端口，那儿可能正躺着另一个进程，回来一句「健康」——而那句话说的是别人。
		svc = config.WithPort(svc, port)
	}

	msg, err := mgr.SaveService(manage.ServiceInFrom(svc))
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(msg)
	// 同名的那条会被覆盖（Upsert 就是这么写的）。说一句是必须的：回执上那句
	// 「已保存」分不清「加进来了」与「原来那条被换掉了」，而后者可能刚刚冲掉
	// 用户改过好几轮的端口与备注。
	if slices.Contains(insp.Names, name) {
		fmt.Printf("  清单里本来就有一条叫 %s，这次把它整条覆盖了\n", name)
	}
	fmt.Printf("  目录 %s\n", abs)
	if svc.Port > 0 {
		fmt.Printf("  端口 %d%s\n", svc.Port, portFromNote(insp, svc.Port, hasPort))
	}
	windowHint()
	return 0
}

// portFromNote 是端口后面那半句括注。
//
// 项目自己声明的端口与随手挑一个空闲端口，在命令行上看着都是一串数字，差别全在
// 这句话上：只有前者是「非用它不可」（启动脚本、代理、服务发现都按它来），
// 后者换掉毫无代价。
//
// given 为真表示这个数是 --port 给进来的：那两种解释它都不是，拿其中任何一句
// 去说它都是错的。
func portFromNote(insp *manage.InspectOut, port int, given bool) string {
	if given {
		return "（--port 指定的）"
	}
	if insp.PortFrom != "" && port == insp.SuggestPort {
		return "（" + insp.PortFrom + "）"
	}
	return "（没被占用的端口，项目自己没声明）"
}

// cmdEdit 改一条已经存在的服务的某几栏。
//
// 一次只改一个服务、且至少要指明改哪一栏：不带开关的 `pier edit api` 到底想干什么
// 是说不清的，与其猜，不如把用法说回去。
func cmdEdit(args []string) int {
	cfgPath, rest := extractConfig(args)
	vals, pos, err := parseFlags(rest)
	if err != nil {
		return fail("edit：%v", err)
	}
	if k := unknownFlag(vals, serviceFlagNames...); k != "" {
		return fail("edit 不认识开关 --%s（看帮助：pier edit -h）", k)
	}
	if len(pos) == 0 {
		return fail("edit 要一个服务名，例如：pier edit api --port 8081")
	}
	if len(pos) > 1 {
		return fail("edit 一次只改一个服务，多出来的是：%s", strings.Join(pos[1:], "、"))
	}
	name := pos[0]
	if len(vals) == 0 {
		return fail("没说要改什么。后面跟至少一个开关，例如：pier edit %s --port 8081", name)
	}

	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	svc, err := mgr.Config().Find(name)
	if err != nil {
		return fail("%v", err)
	}
	// 从旧定义整条装一遍再覆盖：保存是整条替换，只填一个端口就拿去保存，
	// 备注、分组、依赖、手动标记会被空值一起抹掉。
	in := manage.ServiceInFrom(svc)
	in.OrigName = name

	if v, ok := vals["name"]; ok {
		if in.Name = strings.TrimSpace(v); in.Name == "" {
			return fail("--name 不能是空的；要清掉名字只能删了重加")
		}
	}
	if v, ok := vals["dir"]; ok {
		if strings.TrimSpace(v) == "" {
			return fail("--dir 不能是空的；要清掉目录只能删了重加")
		}
		if in.Dir, err = filepath.Abs(v); err != nil {
			return fail("解析目录失败：%v", err)
		}
	}
	if v, ok := vals["kind"]; ok {
		in.Kind = strings.TrimSpace(v)
	}
	if v, ok := vals["group"]; ok {
		in.Group = strings.TrimSpace(v)
	}
	if v, ok := vals["run"]; ok {
		in.Run = v
	}
	if v, ok := vals["build"]; ok {
		in.Build = v
	}
	if v, ok := vals["module"]; ok {
		in.Module = v
	}
	if v, ok := vals["script"]; ok {
		in.Script = v
	}
	if v, ok := vals["note"]; ok {
		in.Note = v
	}
	if v, ok := vals["port"]; ok {
		p, err := parsePort(v)
		if err != nil {
			return fail("--port %v", err)
		}
		in.Port = p
	}
	if v, ok := vals["health"]; ok {
		in.Health = strings.TrimSpace(v)
	} else if _, ok := vals["port"]; ok {
		// 端口改了而探针没跟着改，它就会去探旧端口上那个陌生进程：那可能正好返回
		// 200，于是列表里写着「健康」，说的却是别人——比探不通更坏（见 config.WithPort）。
		// 明确给了 --health 就以给的为准，那是用户自己写的地址。
		old := &config.Service{Port: svc.Port, Health: in.Health}
		in.Health = config.WithPort(old, in.Port).Health
	}

	msg, err := mgr.SaveService(in)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(msg)
	windowHint()
	return 0
}

// cmdRm 从清单里删掉一个或几个服务。
//
// 只删定义，项目目录里的文件一个都不动；正在跑的服务由 manage 那边拦住并说明
// 先停哪一个。几个名字是逐个删的，一个失败不影响其余的——每条定义各是各的，
// 不存在「删了一半」这种中间态。
func cmdRm(args []string) int {
	cfgPath, rest := extractConfig(args)
	vals, pos, err := parseFlags(rest)
	if err != nil {
		return fail("rm：%v", err)
	}
	if k := unknownFlag(vals); k != "" {
		return fail("rm 不认识开关 --%s（看帮助：pier rm -h）", k)
	}
	if len(pos) == 0 {
		return fail("rm 要一个服务名，例如：pier rm api")
	}
	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	failed, changed := false, false
	for _, name := range pos {
		msg, err := mgr.DeleteService(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s：%v\n", name, err)
			failed = true
			continue
		}
		fmt.Println(msg)
		changed = true
	}
	// 一条都没删成时不提窗口那句：清单没变过，重开窗口也就没什么可看的。
	if changed {
		windowHint()
	}
	if failed {
		return 1
	}
	return 0
}

// cmdGroup 是分组的增删改查。
//
// 分组本身不存成员，成员由服务的 group 字段决定（见 internal/manage 里那一段）。
// 所以「把服务放进分组」不在这里：那是 pier edit <服务> --group <名字>。
func cmdGroup(args []string) int {
	cfgPath, rest := extractConfig(args)
	if len(rest) == 0 {
		return groupList(cfgPath)
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "add":
		return groupAdd(cfgPath, subArgs)
	case "rename":
		return groupRename(cfgPath, subArgs)
	case "rm":
		return groupRm(cfgPath, subArgs)
	}
	return fail("group 不认识 %s。有 add / rename / rm；不带子命令则列出全部分组（看帮助：pier group -h）", sub)
}

// groupList 列出全部分组。
//
// 空分组也列：刚建好还没加东西的那一个正是要看的人最关心的一条，
// 而「0 个服务」这句话本身就说明了它还没派上用场。
func groupList(cfgPath string) int {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	groups := cfg.AllGroups()
	if len(groups) == 0 {
		fmt.Println("还没有分组，建一个：pier group add 前端")
		return 0
	}
	// 中文是双宽字符，补空格按显示宽度算，否则这一列参差不齐。
	width := 0
	for _, g := range groups {
		if w := runewidth.StringWidth(g); w > width {
			width = w
		}
	}
	for _, g := range groups {
		n := len(cfg.InGroup(g))
		fmt.Printf("  %s  %d 个服务\n", runewidth.FillRight(g, width), n)
	}
	return 0
}

func groupAdd(cfgPath string, args []string) int {
	if len(args) != 1 {
		return fail("用法：pier group add <分组名>")
	}
	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	msg, err := mgr.CreateGroup(args[0])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(msg)
	windowHint()
	return 0
}

func groupRename(cfgPath string, args []string) int {
	if len(args) != 2 {
		return fail("用法：pier group rename <旧名字> <新名字>")
	}
	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	msg, err := mgr.RenameGroup(args[0], args[1])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(msg)
	windowHint()
	return 0
}

// groupRm 删掉一个分组，成员退回「未分组」，不会被一起删掉。
func groupRm(cfgPath string, args []string) int {
	if len(args) != 1 {
		return fail("用法：pier group rm <分组名>")
	}
	mgr, err := manifestManager(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	msg, err := mgr.DeleteGroup(args[0])
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(msg)
	windowHint()
	return 0
}
