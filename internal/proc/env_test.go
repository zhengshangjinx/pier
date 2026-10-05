package proc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// envFixture 铺一份清单并加载它，返回配置、第一个服务，以及服务的目录
// （好往里放一份 .env）。清单用 shell 类型：它不需要任何工具链，
// 于是这一组测试只考验环境的拼装，与这台机器上装了什么 SDK 无关。
func envFixture(t *testing.T, manifest string) (*config.Config, *config.Service, string) {
	t.Helper()
	// 一律不碰真实数据目录，这条仓库里已经是在哪都守的规矩。
	t.Setenv("PIER_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pier.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.Services[0], dir
}

func writeDotenv(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.EnvFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// envOf 把一份环境摊成 map，好按名字取。
func envOf(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue // 继承来的环境里偶尔有这类项，与这里要看的东西无关
		}
		out[k] = v
	}
	return out
}

// 这一条盯的是各层的先后：继承 < 工具链 < .env < PORT < 清单共享 < 服务自己。
// 每一层都放一个同名的值进去，最后只看谁赢——顺序改了这里就会红。
func TestBuildEnvLayerOrder(t *testing.T) {
	t.Setenv("PIER_TEST_INHERITED", "来自环境")
	t.Setenv("PIER_TEST_LAYER", "来自环境")

	cfg, svc, dir := envFixture(t, `
env:
  SHARED_ONLY: 共享的
  PIER_TEST_LAYER: 来自共享
  GREETING: ${DB_NAME}，从 ${DB_HOST} 来
  FROM_DOTENV: ${DB_NAME}
services:
  - name: api
    kind: shell
    dir: ./app
    port: 3000
    run: echo hi
    env:
      OWN: 自己的
      SHARED_ONLY: 来自服务
      PIER_TEST_LAYER: 来自服务
      DB: ${DB_HOST}:${PORT}
`)
	writeDotenv(t, dir, "DB_NAME=阿碧\nDB_HOST=127.0.0.1\nPIER_TEST_INHERITED=来自 .env\n")

	env, note, err := New(cfg).buildEnv(svc)
	if err != nil {
		t.Fatal(err)
	}
	got := envOf(t, env)

	for k, want := range map[string]string{
		// .env 只补缺：环境里已经有的一律不动（dotenv 的通行做法）
		"PIER_TEST_INHERITED": "来自环境",
		"DB_NAME":             "阿碧",
		"DB_HOST":             "127.0.0.1",
		// 清单的两段一层压一层
		"PIER_TEST_LAYER": "来自服务",
		"SHARED_ONLY":     "来自服务",
		"OWN":             "自己的",
		// 共享段与 .env 之间：清单里明写的更算数
		"FROM_DOTENV": "阿碧",
		// ${} 从已经算好的环境里取，两者是同一个值
		"GREETING": "阿碧，从 127.0.0.1 来",
		"DB":       "127.0.0.1:3000",
		// 端口注入
		"PORT": "3000",
	} {
		if got[k] != want {
			t.Errorf("%s = %q，想要 %q", k, got[k], want)
		}
	}

	// 日志里只说名字，不说值：.env 里装的常常是密钥。
	if !strings.Contains(note, "DB_NAME") || !strings.Contains(note, "DB_HOST") {
		t.Errorf("说明里该列出注入的变量名，得到 %q", note)
	}
	if strings.Contains(note, "阿碧") || strings.Contains(note, "127.0.0.1") {
		t.Errorf("说明里不该出现值，得到 %q", note)
	}
	// 被跳过的那些必须说出来，否则「我改了 .env 却没生效」就只能靠猜。
	if !strings.Contains(note, "PIER_TEST_INHERITED") {
		t.Errorf("说明里该点出没被覆盖的那个变量，得到 %q", note)
	}
}

func TestBuildEnvNoDotenv(t *testing.T) {
	cfg, svc, _ := envFixture(t, `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
`)
	env, note, err := New(cfg).buildEnv(svc)
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Errorf("目录里没有 .env 就不该写这一行日志，得到 %q", note)
	}
	if _, ok := envOf(t, env)["PORT"]; ok {
		t.Error("清单上没写端口，不该凭空多出一个 PORT")
	}
}

// 环境里本来就有一个 PORT 时，清单没写端口就别去动它：那可能是用户自己
// 导出的，也可能是别人的工具在用的。
func TestBuildEnvKeepsInheritedPort(t *testing.T) {
	t.Setenv("PORT", "9999")
	cfg, svc, _ := envFixture(t, `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
`)
	env, _, err := New(cfg).buildEnv(svc)
	if err != nil {
		t.Fatal(err)
	}
	if got := envOf(t, env)["PORT"]; got != "9999" {
		t.Errorf("PORT = %q，想要没写过端口时保持 9999", got)
	}
}

// 没写 ${} 的值一个字节都不能改：清单里的值常常就是给子进程看的 shell 片段，
// 「顺手把 $HOME 展开了」会把一件看起来对的事情搞坏。
func TestBuildEnvLeavesPlainValuesAlone(t *testing.T) {
	cfg, svc, dir := envFixture(t, `
env:
  RAW: 价格是 $5，路径 $HOME/bin，两个美元 $$，字面的 $${NOT_A_VAR}
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
`)
	writeDotenv(t, dir, "KEEP=a\\nb\n")
	env, _, err := New(cfg).buildEnv(svc)
	if err != nil {
		t.Fatal(err)
	}
	got := envOf(t, env)
	if want := "价格是 $5，路径 $HOME/bin，两个美元 $$，字面的 ${NOT_A_VAR}"; got["RAW"] != want {
		t.Errorf("RAW = %q，想要 %q", got["RAW"], want)
	}
	// .env 里的值原样进环境，不做转义。
	if got["KEEP"] != `a\nb` {
		t.Errorf("KEEP = %q，想要原样的 a\\nb", got["KEEP"])
	}
}

func TestBuildEnvExpansionFailures(t *testing.T) {
	cases := []struct {
		what     string
		manifest string
		says     string
	}{
		{
			what: "共享段引用了没有定义的名字",
			manifest: `
env:
  URL: http://${NOT_DEFINED}/
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
`,
			says: "${NOT_DEFINED} 没有定义",
		},
		{
			// 共享段是先算好的那一份，看不见服务自己的变量。这不是实现上的将就：
			// 同一个名字在每个服务下都不一样的话，它本来也不该写在共享段里。
			// 报错里要带上「哪一段」——两段挨着写在同一个文件里，只说变量名，
			// 看的人还得自己猜是哪一段出的错。
			what: "共享段引用了服务自己的变量",
			manifest: `
env:
  URL: http://${DB_HOST}/shop
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
    env:
      DB_HOST: 127.0.0.1
`,
			says: "的共享环境变量：URL：${DB_HOST} 没有定义",
		},
		{
			what: "服务那段引用了没有定义的名字",
			manifest: `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
    env:
      A: ${NOPE}
`,
			says: "的环境变量：A：${NOPE} 没有定义",
		},
		{
			what: "自己引用自己",
			manifest: `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
    env:
      A: ${A}
`,
			says: "绕回了这一层",
		},
		{
			what: "写法本身不对",
			manifest: `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
    env:
      A: ${未定义 的写法}
`,
			says: "不是一个变量名",
		},
	}
	for _, c := range cases {
		cfg, svc, _ := envFixture(t, c.manifest)
		env, _, err := New(cfg).buildEnv(svc)
		if err == nil {
			t.Errorf("%s：应当报错，得到环境 %v", c.what, env)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s：报错是 %q，里面没提 %q", c.what, err, c.says)
		}
	}
}

// .env 读不动、写坏了要拦住启动：那份文件没生效，服务起来是另一副样子，
// 而现场看上去一切正常。
func TestBuildEnvBadDotenv(t *testing.T) {
	cfg, svc, dir := envFixture(t, `
services:
  - name: api
    kind: shell
    dir: ./app
    run: echo hi
`)
	writeDotenv(t, dir, "A=\"没有收尾\n")
	_, _, err := New(cfg).buildEnv(svc)
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), config.EnvFileName) || !strings.Contains(err.Error(), "第 1 行") {
		t.Errorf("报错该指到是哪份文件的哪一行，得到 %q", err)
	}
}

func TestApplyDotenv(t *testing.T) {
	base := []string{"A=1", "PATH=/bin"}
	out, added, kept := applyDotenv(base, []config.EnvKV{
		{Key: "B", Value: "2"}, {Key: "A", Value: "覆盖不了"}, {Key: "C", Value: "3"},
	})

	if got := envOf(t, out); got["A"] != "1" || got["B"] != "2" || got["C"] != "3" || got["PATH"] != "/bin" {
		t.Errorf("补进去的结果不对：%v", got)
	}
	if strings.Join(added, ",") != "B,C" {
		t.Errorf("注入的该是 B、C，得到 %v", added)
	}
	if strings.Join(kept, ",") != "A" {
		t.Errorf("跳过的该是 A，得到 %v", kept)
	}
	// 原切片不能被就地改坏：调用方还在用它。
	if base[0] != "A=1" || len(base) != 2 {
		t.Errorf("入参被动过了：%v", base)
	}
}

func TestDotenvNote(t *testing.T) {
	if got := dotenvNote(nil, nil); got != "" {
		t.Errorf("什么都没做时不该有这一行，得到 %q", got)
	}
	if got := dotenvNote([]string{"A"}, nil); !strings.HasPrefix(got, ".env 注入 A") {
		t.Errorf("得到 %q", got)
	}
	got := dotenvNote([]string{"A", "B"}, []string{"C"})
	for _, want := range []string{"A、B", "C", "没覆盖"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q 里该提到 %q", got, want)
		}
	}
	if got := dotenvNote(nil, []string{"C"}); !strings.Contains(got, "C") {
		t.Errorf("得到 %q，里面该提到 C", got)
	}
}
