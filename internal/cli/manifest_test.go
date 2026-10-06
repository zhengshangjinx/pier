package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// captureErr 把标准错误接走，跑完再把内容读回来。
//
// 报错走的是 stderr（fail 就是这么写的），不接走的话一片红字就混在测试输出里，
// 想看的那一句反而找不着。顺带把 stdout 也接走：报错的这几次本来就不该往那边
// 写东西，接走之后它要是写了就落在断言里，而不是悄悄印出来。
func captureErr(t *testing.T, fn func()) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	done := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(r)
		done <- string(raw)
	}()
	fn()
	w.Close()
	return <-done
}

// seedStore 在临时数据目录里铺一份清单，返回服务目录。
//
// 走真实的数据文件（services.json）而不是 --config 那份只读的 YAML：add / edit /
// rm / group 这四个动词的本职就是改盘上那一份，写坏了才是最要紧的失败。
func seedStore(t *testing.T, services ...*config.Service) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)
	// 服务目录与清单目录分开：清单里那些定义不该在工作目录里长出别的东西。
	dir := t.TempDir()
	for _, s := range services {
		if s.Dir == "" {
			s.Dir = dir
		}
	}
	raw, err := json.Marshal(map[string]any{
		"version": 1, "groups": []string{}, "services": services,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, config.StoreName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readService 从盘上把一条定义读回来。每一次断言都重新读：改完的东西在不在盘上，
// 与内存里那份算不算对是两件事，而命令行这边改完就退出，留在内存里的等于没改。
func readService(t *testing.T, name string) *config.Service {
	t.Helper()
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("读清单失败：%v", err)
	}
	svc, err := cfg.Find(name)
	if err != nil {
		t.Fatalf("清单里没有 %s：%v", name, err)
	}
	return svc
}

// fullService 是两条每一栏都有值的服务：编辑时「没提到的别动」这句话，只有在这种
// 每条都填满的定义上才测得出来。
//
// 两条而不是一条，是因为依赖必须落在清单里（加载时会校验「服务 api 依赖的 db 不在
// 清单里」），而依赖正是「命令行上没有开关、改端口时最容易被顺手抹掉」的那一栏。
func fullService(dir string) []*config.Service {
	return []*config.Service{
		{
			Name: "api", Dir: dir, Group: "前端", Note: "演示用", Kind: config.KindShell,
			Run: "sleep 300", Build: "echo build", Module: "shop-admin", Script: "dev",
			Port: 8080, Health: "http://localhost:8080/health",
			Env:       map[string]string{"A": "1"},
			Toolchain: map[string]string{config.KindNode: "/opt/node"},
			DependsOn: []string{"db:healthy"}, Restart: config.RestartOnFailure, Manual: true,
			Watch: config.Watch{On: true, Include: []string{"src/**"}},
		},
		// 带条件的前置要求那个服务自己有探针（等不到的东西不许写进依赖里），
		// 所以这一条得配上一个。
		{Name: "db", Dir: dir, Kind: config.KindShell, Run: "sleep 300",
			Health: "tcp://localhost:3306"},
	}
}

// 保存是整条替换：拿一份只填了一栏的 ServiceIn 去保存，其余字段会被空值盖掉。
// 所以「只改一项」在命令行上是先 ServiceInFrom 再覆盖——这条钉的就是那道工序的保真度。
func TestEditKeepsWhatItWasNotAskedAbout(t *testing.T) {
	seedStore(t, fullService(t.TempDir())...)
	before := readService(t, "api")

	if code := cmdEdit([]string{"api", "--port", "8081"}); code != 0 {
		t.Fatalf("pier edit 退出码 = %d，想要 0", code)
	}
	after := readService(t, "api")

	if after.Port != 8081 {
		t.Errorf("Port = %d，想要 8081", after.Port)
	}
	// 端口换了，探针里的端口跟着换（见 config.WithPort）。
	if want := "http://localhost:8081/health"; after.Health != want {
		t.Errorf("Health = %q，想要 %q", after.Health, want)
	}
	// 命令行上没有开关的那些栏目一个都不该动。
	if after.Group != before.Group {
		t.Errorf("Group = %q，想要 %q", after.Group, before.Group)
	}
	if after.Note != before.Note {
		t.Errorf("Note = %q，想要 %q", after.Note, before.Note)
	}
	if after.Kind != before.Kind || after.Run != before.Run || after.Build != before.Build {
		t.Errorf("类型/启动/编译被改了：%q / %q / %q", after.Kind, after.Run, after.Build)
	}
	if after.Module != before.Module || after.Script != before.Script {
		t.Errorf("模块/脚本被改了：%q / %q", after.Module, after.Script)
	}
	if after.Restart != before.Restart || after.Manual != before.Manual {
		t.Errorf("重启策略/手动标记被改了：%q / %v", after.Restart, after.Manual)
	}
	if !after.Watch.On || strings.Join(after.Watch.Include, ",") != "src/**" {
		t.Errorf("Watch 被改了：%+v", after.Watch)
	}
	if len(after.Env) != 1 || after.Env["A"] != "1" {
		t.Errorf("Env 被改了：%v（这是编辑表单里那些，命令行上没有入口）", after.Env)
	}
	if len(after.Toolchain) != 1 || after.Toolchain[config.KindNode] != "/opt/node" {
		t.Errorf("Toolchain 被改了：%v", after.Toolchain)
	}
	if strings.Join(after.DependsOn, ",") != "db:healthy" {
		t.Errorf("DependsOn 被改了：%v", after.DependsOn)
	}
}

// 明确给了 --health 就以给的为准：那是用户自己写的地址，不该被端口那一手改写掉。
func TestEditHealthFlagWinsOverPortSwap(t *testing.T) {
	seedStore(t, &config.Service{
		Name: "api", Dir: t.TempDir(), Kind: config.KindShell, Run: "sleep 300",
		Port: 8080, Health: "http://localhost:8080/health",
	})
	if code := cmdEdit([]string{"api", "--port", "9000", "--health", "http://localhost:9000/ready"}); code != 0 {
		t.Fatalf("退出码 = %d，想要 0", code)
	}
	svc := readService(t, "api")
	if svc.Health != "http://localhost:9000/ready" {
		t.Errorf("Health = %q，想要给进去的那一个", svc.Health)
	}
	// 空串是「不再探」，与「没给这一项」分得开（--health ""）。
	if code := cmdEdit([]string{"api", "--health", ""}); code != 0 {
		t.Fatalf("清空探针退出码 = %d，想要 0", code)
	}
	if svc := readService(t, "api"); svc.Health != "" {
		t.Errorf("Health = %q，想要空", svc.Health)
	}
}

// 改名走的是「先改名再覆盖保存」那条路。名字变了，日志目录也得跟着走，
// 否则改名之后看日志会发现上一次运行的那份留在旧目录里。
func TestEditRenameCarriesTheOtherFields(t *testing.T) {
	seedStore(t, fullService(t.TempDir())...)
	if code := cmdEdit([]string{"api", "--name", "api-server"}); code != 0 {
		t.Fatalf("退出码 = %d，想要 0", code)
	}
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Find("api"); err == nil {
		t.Error("旧名字还在清单里，改名的两半只做了一半")
	}
	got := readService(t, "api-server")
	if got.Group != "前端" || got.Port != 8080 || strings.Join(got.DependsOn, ",") != "db:healthy" {
		t.Errorf("改名把别的栏目带丢了：%+v", got)
	}
}

// 不带开关的 edit 说不清要干什么，宁可把用法说回去。
func TestEditNeedsSomethingToChange(t *testing.T) {
	seedStore(t, fullService(t.TempDir())...)
	cases := [][]string{
		{"api"},                       // 没说改什么
		{},                            // 没说改谁
		{"api", "web", "--port", "9"}, // 一次只改一个
		{"api", "--port", "0"},        // 端口范围
		{"api", "--port", "八千"},
		{"api", "--prot", "8081"}, // 打错的开关
		{"api", "--name", ""},     // 名字不能清空
		{"api", "-port", "8081"},  // 只认 --
		{"nope", "--note", "x"},   // 没有这条服务
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			captureErr(t, func() {
				if code := cmdEdit(args); code == 0 {
					t.Fatalf("cmdEdit(%q) 退出码 = 0，本该报错", args)
				}
			})
		})
	}
	// 报错的那几次一个字节都不该落盘。
	if svc := readService(t, "api"); svc.Port != 8080 || svc.Name != "api" {
		t.Errorf("报错的命令改到了盘上的东西：%+v", svc)
	}
}

// 一个目录里没有任何标记文件的项目（shell 项目正是如此）也要加得进来：
// 命令行上写明类型与启动命令就够了。
func TestAddShellProjectWithoutMarkerFiles(t *testing.T) {
	seedStore(t)
	dir := t.TempDir()

	out := capture(t, func() {
		if code := cmdAdd([]string{dir, "--name", "worker", "--kind", "shell",
			"--run", "sleep 300", "--port", "19200"}); code != 0 {
			t.Fatalf("退出码 = %d，想要 0", code)
		}
	})
	svc := readService(t, "worker")
	if svc.Kind != config.KindShell || svc.Run != "sleep 300" {
		t.Errorf("存下来的定义不对：%+v", svc)
	}
	if svc.Port != 19200 || svc.Dir != dir {
		t.Errorf("端口或目录不对：%d / %q", svc.Port, svc.Dir)
	}
	if !strings.Contains(out, "已保存") {
		t.Errorf("输出里没有回执：\n%s", out)
	}
	// 端口后面那半句括注说的是这个数是怎么来的。--port 给进去的那个既不是
	// 「项目声明的」也不是「挑了一个空闲的」，两种解释都会把人引偏。
	if !strings.Contains(out, "--port 指定的") {
		t.Errorf("没说清端口是哪儿来的：\n%s", out)
	}
	// 第一次加的时候不能出现「覆盖了」那句：它会让人以为原来有这么一条。
	if strings.Contains(out, "覆盖") {
		t.Errorf("第一次加就说覆盖了：\n%s", out)
	}

	// 同一个目录再加一遍：这是覆盖，得说出来——回执上那句「已保存」分不清
	// 「加进来了」与「原来那条被换掉了」。
	out = capture(t, func() {
		if code := cmdAdd([]string{dir, "--name", "worker", "--kind", "shell", "--run", "sleep 300"}); code != 0 {
			t.Fatalf("第二次加退出码 = %d，想要 0", code)
		}
	})
	if !strings.Contains(out, "覆盖") {
		t.Errorf("同名覆盖没说一句：\n%s", out)
	}
}

// 认不出的目录、不存在的目录、是个文件：这三件事都要在碰清单之前挡住，
// 而且不能把 manage 那两句界面话（「点右侧的『浏览…』」）搬到终端上来。
func TestAddRefusesBadInput(t *testing.T) {
	seedStore(t)
	empty := t.TempDir()
	file := filepath.Join(empty, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"目录不存在", []string{filepath.Join(empty, "没有这个目录")}, "不是有效目录"},
		{"那是个文件", []string{file}, "不是有效目录"},
		{"认不出类型", []string{empty}, "没认出项目类型"},
		{"不认识的开关", []string{empty, "--prot", "8080"}, "不认识开关"},
		{"端口不是数字", []string{empty, "--port", "八千"}, "端口号"},
		{"端口越界", []string{empty, "--port", "70000"}, "端口号"},
		{"两个目录", []string{empty, t.TempDir()}, "只接一个目录"},
		{"目录给了两次", []string{empty, "--dir", empty}, "给了两次"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := captureErr(t, func() {
				if code := cmdAdd(c.args); code == 0 {
					t.Fatalf("cmdAdd(%q) 退出码 = 0，本该报错", c.args)
				}
			})
			if !strings.Contains(out, c.want) {
				t.Errorf("报错内容 = %q，应当说到 %q", out, c.want)
			}
			if strings.Contains(out, "浏览…") {
				t.Errorf("把界面上的说法搬到终端来了：%q", out)
			}
		})
	}
	// 一条都不该落盘。
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("报错的命令往清单里写了东西：%+v", cfg.Services)
	}
}

// rm 是逐个删的：删掉的不影响删不掉的，有一个没删成退出码就是 1。
func TestRmDeletesWhatItCan(t *testing.T) {
	dir := t.TempDir()
	seedStore(t,
		&config.Service{Name: "api", Dir: dir, Kind: config.KindShell, Run: "sleep 300"},
		&config.Service{Name: "web", Dir: dir, Kind: config.KindShell, Run: "sleep 300"},
	)
	out := capture(t, func() {
		if code := cmdRm([]string{"api", "nope", "web"}); code != 1 {
			t.Fatalf("退出码 = %d，有一个没删成应当是 1", code)
		}
	})
	if !strings.Contains(out, "已删除 api") || !strings.Contains(out, "已删除 web") {
		t.Errorf("删成了的那两个没说：\n%s", out)
	}
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("还剩着：%+v", cfg.Services)
	}
	captureErr(t, func() {
		if code := cmdRm(nil); code == 0 {
			t.Error("不给名字的 rm 本该报错")
		}
	})
}

// 分组的增删改：改名时成员跟着走，删分组不删服务。
func TestGroupCommands(t *testing.T) {
	seedStore(t, &config.Service{
		Name: "api", Dir: t.TempDir(), Kind: config.KindShell, Run: "sleep 300", Group: "前端",
	})

	capture(t, func() {
		if code := cmdGroup([]string{"add", "后端"}); code != 0 {
			t.Fatalf("建分组退出码 = %d", code)
		}
	})
	cfg, err := loadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	// 建过的空分组也留着：刚建好还没加东西的那一个正是要看的人最关心的。
	// 「未分组」不在里面：它只在真有服务没写分组时才出现（这里那条 api 有分组）。
	if got := cfg.AllGroups(); strings.Join(got, ",") != "后端,前端" {
		t.Errorf("AllGroups = %v，想要 后端,前端", got)
	}

	capture(t, func() {
		if code := cmdGroup([]string{"rename", "前端", "客户端"}); code != 0 {
			t.Fatalf("改名退出码 = %d", code)
		}
	})
	if svc := readService(t, "api"); svc.Group != "客户端" {
		t.Errorf("成员没跟着走：Group = %q", svc.Group)
	}

	capture(t, func() {
		if code := cmdGroup([]string{"rm", "客户端"}); code != 0 {
			t.Fatalf("删分组退出码 = %d", code)
		}
	})
	// 删分组不删服务：成员退回「未分组」。
	if svc := readService(t, "api"); svc.Group != "" {
		t.Errorf("删分组之后成员该退回未分组，Group = %q", svc.Group)
	}

	// 列表：空清单与有分组各说一句话，别让人以为命令没跑。
	out := capture(t, func() {
		if code := cmdGroup(nil); code != 0 {
			t.Fatalf("列分组退出码 = %d", code)
		}
	})
	if !strings.Contains(out, "后端") {
		t.Errorf("列表里没有建过的那个分组：\n%s", out)
	}
	for _, args := range [][]string{{"add"}, {"add", "a", "b"}, {"rename", "a"}, {"rm"}, {"nope"}} {
		captureErr(t, func() {
			if code := cmdGroup(args); code == 0 {
				t.Errorf("cmdGroup(%q) 退出码 = 0，本该报错", args)
			}
		})
	}
}

// 帮助里写着能用的开关，代码就得认；代码认的，帮助里也得有。
// 两张表各写一份的话，最刺眼的那种错是「帮助里写了、敲下去说不认识」。
func TestAddEditFlagsMatchTheHelp(t *testing.T) {
	for _, name := range []string{"add", "edit"} {
		c, ok := findCommand(name)
		if !ok {
			t.Fatalf("帮助里没有 %s", name)
		}
		inHelp := map[string]bool{}
		for _, f := range c.flags {
			if v, found := strings.CutPrefix(f.form, "--"); found {
				inHelp[strings.TrimSpace(strings.SplitN(v, " ", 2)[0])] = true
			}
		}
		for _, f := range serviceFlagNames {
			if !inHelp[f] {
				t.Errorf("%s 认 --%s，帮助里却没写", name, f)
			}
			delete(inHelp, f)
		}
		for left := range inHelp {
			t.Errorf("%s 的帮助里写了 --%s，代码不认", name, left)
		}
	}
}

// parseFlags 收「--名字 值」与「--名字=值」两种写法，位置参数另列。
// 「没给」与「给了空串」必须分得开：--note "" 要的是后者。
func TestParseFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		vals map[string]string
		pos  []string
		bad  bool
	}{
		{name: "分开写", args: []string{"--name", "api"}, vals: map[string]string{"name": "api"}},
		{name: "等号写", args: []string{"--name=api"}, vals: map[string]string{"name": "api"}},
		{name: "位置参数混着开关", args: []string{"api", "--port", "8081"},
			vals: map[string]string{"port": "8081"}, pos: []string{"api"}},
		{name: "单个横杠算位置参数", args: []string{"-"}, pos: []string{"-"}},
		{name: "值里有等号", args: []string{"--run=go build -o a=b ."},
			vals: map[string]string{"run": "go build -o a=b ."}},
		{name: "值的开头是横杠", args: []string{"--run", "--watch"},
			vals: map[string]string{"run": "--watch"}},
		// 空串是「给了、值是空的」，与「没给」是两件事。
		{name: "给了空串", args: []string{"--note", ""}, vals: map[string]string{"note": ""}},
		{name: "同一个开关给两次以最后为准", args: []string{"--port", "1", "--port", "2"},
			vals: map[string]string{"port": "2"}},
		{name: "后面没跟值", args: []string{"--name"}, bad: true},
		{name: "只有一个横杠", args: []string{"-port", "8080"}, bad: true},
		{name: "没写开关名", args: []string{"--=x"}, bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vals, pos, err := parseFlags(c.args)
			if c.bad {
				if err == nil {
					t.Fatalf("parseFlags(%q) = %v，本该报错", c.args, vals)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFlags(%q) 报错：%v", c.args, err)
			}
			if len(vals) != len(c.vals) {
				t.Fatalf("parseFlags(%q) = %v，想要 %v", c.args, vals, c.vals)
			}
			for k, v := range c.vals {
				if got, ok := vals[k]; !ok || got != v {
					t.Errorf("parseFlags(%q)[%q] = %q / 在不在=%v，想要 %q", c.args, k, got, ok, v)
				}
			}
			if strings.Join(pos, " ") != strings.Join(c.pos, " ") {
				t.Errorf("位置参数 = %v，想要 %v", pos, c.pos)
			}
		})
	}
}

// 挑不认识的开关要挑得稳定：map 的遍历顺序是随机的，同一条命令两次跑出两个
// 不同的错字，读的人会以为自己写错了哪一处。
func TestUnknownFlagIsStable(t *testing.T) {
	vals := map[string]string{"zebra": "", "alpha": "", "port": ""}
	for i := 0; i < 20; i++ {
		if got := unknownFlag(vals, serviceFlagNames...); got != "alpha" {
			t.Fatalf("unknownFlag = %q，想要按字母序第一个 alpha", got)
		}
	}
	if got := unknownFlag(map[string]string{"port": "1"}, serviceFlagNames...); got != "" {
		t.Errorf("全是认识的，却挑出 %q", got)
	}
}

func TestParsePort(t *testing.T) {
	for _, raw := range []string{"1", " 8080 ", "65535"} {
		if _, err := parsePort(raw); err != nil {
			t.Errorf("parsePort(%q) 报错：%v", raw, err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "65536", "八千", "80 80", "8080.5"} {
		_, err := parsePort(raw)
		if err == nil {
			t.Errorf("parsePort(%q) 没报错", raw)
			continue
		}
		// 报错要把收到的东西原样说回去：只说「端口不合法」，写的人看不出
		// 是多打了一个空格还是少了一位数。
		if !strings.Contains(err.Error(), raw) {
			t.Errorf("parsePort(%q) 的报错里没有原值：%v", raw, err)
		}
	}
}
