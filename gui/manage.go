package main

// 这一层只剩两件事：把界面传来的字符串交给 internal/manage，把结果包成 JSON。
//
// 业务逻辑（写覆盖文件、校验、分组改名、端口占用判定）全部在 internal/manage，
// 命令行的子命令用的是同一份实现。这里不再重复任何判断。
//
// 只有「让系统弹窗」这类纯界面动作留在本文件：它们依赖 macOS 的系统对话框，
// 换成原生界面时会被 NSOpenPanel 取代，不属于业务层。

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhengshangjinx/pier/internal/manage"
)

// ── 业务层的 JSON 封装 ─────────────────────────────────────────────────────

func (a *app) portOwner(name string) string {
	out, err := a.mgr.PortOwner(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

func (a *app) killPortOwner(name, pidRaw, started string) string {
	msg, err := a.mgr.KillPortOwner(name, pidRaw, started)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

// saveService 收的是一段 JSON 字符串——这是 webview 绑定的形状，不是业务层的。
// 解析在这一层做完，业务层只看见成形的字段。
func (a *app) saveService(payload string) string {
	var in manage.ServiceIn
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		return errJSON("提交的内容无法解析：" + err.Error())
	}
	msg, err := a.mgr.SaveService(in)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) deleteService(name string) string {
	msg, err := a.mgr.DeleteService(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

// renameService 改名。旧名字和新名字分开传，而不是塞进 ServiceIn：
// 改名只动名字这一处，表单里其余字段原样留在清单里。
func (a *app) renameService(oldName, newName string) string {
	msg, err := a.mgr.RenameService(oldName, newName)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

// duplicateService 复制一份。返回新服务的名字，界面拿它把表单打开在那一条上。
func (a *app) duplicateService(name string) string {
	out, err := a.mgr.DuplicateService(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// moveServices / moveGroups 收的是「这一页现在的顺序」（JSON 数组）。
// 传数组而不是「把 A 挪到 B 前面」：界面上看得见的那几行之间可能夹着看不见的
// 服务，只有整份顺序才说得清落点。
func (a *app) moveServices(namesJSON string) string {
	var names []string
	if err := json.Unmarshal([]byte(namesJSON), &names); err != nil {
		return errJSON("顺序无法解析：" + err.Error())
	}
	msg, err := a.mgr.MoveServices(names)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) moveGroups(namesJSON string) string {
	var names []string
	if err := json.Unmarshal([]byte(namesJSON), &names); err != nil {
		return errJSON("顺序无法解析：" + err.Error())
	}
	msg, err := a.mgr.MoveGroups(names)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) createGroup(name string) string {
	msg, err := a.mgr.CreateGroup(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) renameGroup(oldName, newName string) string {
	msg, err := a.mgr.RenameGroup(oldName, newName)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

func (a *app) deleteGroup(name string) string {
	msg, err := a.mgr.DeleteGroup(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

// inspectDir 的第二个参数是表单上已填的几栏（JSON），见 manage.InspectHint；
// 可以是空串，表示什么都还没填。
func (a *app) inspectDir(dir, hintJSON string) string {
	var hint manage.InspectHint
	if strings.TrimSpace(hintJSON) != "" {
		if err := json.Unmarshal([]byte(hintJSON), &hint); err != nil {
			return errJSON("表单内容解析失败：" + err.Error())
		}
	}
	out, err := a.mgr.InspectDir(dir, hint)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

func (a *app) portCandidates(fromRaw string) string {
	out, err := a.mgr.PortCandidates(fromRaw)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// ── 分享与迁移 ─────────────────────────────────────────────────────────────
//
// 这一段全是「把清单变成一段能带走的文字」或者反过来。生成 YAML 一个字都不在这里拼：
// 字段名、顺序、omitempty 全在 internal/config 的 yaml 标签上，这里另写一份的话，
// 导出的片段贴回清单里就会对不上。
//
// 文字一律给出去让界面走 pierCopyText 进剪贴板，不在这里直接写人家的剪贴板：
// 剪贴板是系统集成，和「打开浏览器」一样归宿主，而且那条路上已经有失败提示了。

// serviceYAML 生成一条服务的 YAML 片段，供人贴进任何一份清单。
func (a *app) serviceYAML(name string) string {
	text, err := a.mgr.ServiceYAML(name)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(map[string]any{"ok": true, "text": text})
}

// configYAML 生成整份清单的 YAML。
func (a *app) configYAML() string {
	text, err := a.mgr.ExportYAML()
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(map[string]any{"ok": true, "text": text})
}

// exportConfig 把当前清单另存为一份 YAML 文件。
//
// 落盘的位置交给系统的存储对话框（osascript 的 choose file name），
// 理由与 pickDirectory 那段相同：不引 NSOpenPanel，就不必给 build-app.sh
// 添一层 Objective-C 桥接。同名文件系统自己会问「要替换吗」。
func (a *app) exportConfig() string {
	text, err := a.mgr.ExportYAML()
	if err != nil {
		return errJSON(err.Error())
	}
	script := `set f to choose file name with prompt "导出清单" default name "pier.yaml"
return POSIX path of f`

	picked, canceled, err := runAppleScript(script)
	if err != nil {
		return errJSON("打开存储对话框失败：" + err.Error())
	}
	if canceled || picked == "" {
		return marshal(map[string]any{"ok": true, "canceled": true})
	}
	if err := os.WriteFile(picked, []byte(text), 0o644); err != nil {
		return errJSON("写入失败：" + err.Error())
	}
	return okJSON("已导出到 " + picked)
}

// openConfig 让用户挑一份文件当清单用。
//
// 按扩展名分流（.json 是本机数据、其余当 YAML），与 config.Open 那套判断同一口径：
// 选到 YAML 就整份只读，选回数据文件就又是可编辑的。命令行给不了 --config 的人
// （从访达双击启动的）靠这一个入口拿到同样的能力。
func (a *app) openConfig() string {
	script := `set f to choose file with prompt "选择一份清单（pier.yaml 或 Pier 的数据文件）"
return POSIX path of f`

	picked, canceled, err := runAppleScript(script)
	if err != nil {
		return errJSON("打开文件选择框失败：" + err.Error())
	}
	if canceled || picked == "" {
		return marshal(map[string]any{"ok": true, "canceled": true})
	}
	msg, err := a.panel.SetConfig(picked)
	if err != nil {
		return errJSON(err.Error())
	}
	return okJSON(msg)
}

// useLocalConfig 切回 Pier 自己的数据文件。
//
// 「打开清单…」选了 YAML 就整份只读，收起了全部编辑入口——没有这条退路的话，
// 从访达双击启动的人（没有命令行可以去掉 --config）只能重启一次才能回来。
func (a *app) useLocalConfig() string {
	path, src, err := resolveConfig("")
	if err != nil {
		return errJSON(err.Error())
	}
	if err := a.panel.Load(path); err != nil {
		return errJSON(err.Error())
	}
	a.panel.SetSource(src)
	return okJSON("已切回本机数据")
}

// ── 让系统弹出访达的目录选择框 ─────────────────────────────────────────────
//
// 让用户手打路径是很糟的体验：路径长、容易打错，而且他有现成的访达。
// 这里调 osascript 的 choose folder，弹的是系统原生选择框，支持拖拽、侧栏、最近使用。
//
// 不用 cgo 去调 NSOpenPanel：那需要引入 Objective-C 桥接，而 build-app.sh
// 现在只依赖 Go + Xcode 命令行工具就能出包，为一个小弹窗破坏这一点不划算。
func (a *app) pickDirectory(current string) string {
	cfg := a.mgr.Config()

	// 起点优先用用户已填的目录，其次工作空间根目录，最后回到用户主目录。
	start := strings.TrimSpace(current)
	if start != "" && !filepath.IsAbs(start) && cfg != nil {
		start = filepath.Join(cfg.Dir(), start)
	}
	if start == "" {
		if cfg != nil {
			start = cfg.Dir()
		} else if home, err := os.UserHomeDir(); err == nil {
			start = home
		}
	}
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		if cfg != nil {
			start = cfg.Dir()
		} else {
			start = ""
		}
	}

	script := `set p to choose folder with prompt "选择项目目录"`
	if start != "" {
		script += ` default location POSIX file ` + quoteAppleScript(start)
	}
	script += `
	return POSIX path of p`

	picked, canceled, err := runAppleScript(script)
	if err != nil {
		return errJSON("打开目录选择框失败：" + err.Error())
	}
	if canceled || picked == "" {
		return marshal(map[string]any{"ok": true, "canceled": true})
	}
	// POSIX path of 会给目录加上结尾斜杠，去掉它，存进数据文件和显示时都干净些。
	picked = filepath.Clean(strings.TrimSuffix(picked, "/"))

	out := map[string]any{"ok": true, "dir": picked}
	// 顺带把相对路径算出来，界面可以直接填进表单并显示成相对工作空间的路径。
	if cfg != nil {
		out["rel"] = cfg.RelTo(picked)
	}
	return marshal(out)
}

// runAppleScript 跑一段 osascript，返回它 print 出来的那一行（已去掉首尾空白）。
//
// 三种结果分开报：正常拿到值、用户取消、真出错。取消（AppleScript 的 -128，
// 中文系统上 stderr 还可能写成别的字样，所以两个都认）不是故障，
// 调用方据此安静地什么都不做——报一条红错误只会让人以为工具坏了。
func runAppleScript(script string) (out string, canceled bool, err error) {
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "-128") || strings.Contains(msg, "User canceled") {
			return "", true, nil
		}
		return "", false, errors.New(firstNonEmpty(msg, err.Error()))
	}
	return strings.TrimSpace(stdout.String()), false, nil
}

// quoteAppleScript 把字符串包成 AppleScript 的字符串字面量。
//
// AppleScript 里反斜杠和双引号都要转义。路径本身很少含这两个字符，
// 但真含了而没转义，轻则弹框报语法错，重则把路径截断成另一个目录。
func quoteAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
