package main

// 这一层只剩两件事：把界面传来的字符串交给 internal/manage，把结果包成 JSON。
//
// 业务逻辑（写覆盖文件、校验、分组改名、端口占用判定）全部在 internal/manage，
// 命令行的子命令用的是同一份实现。这里不再重复任何判断。
//
// 只有「让系统弹窗」这一件事留在本文件：它按平台各写一份（picker_<平台>.go），
// 不属于业务层。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhengshangjinx/pier/internal/config"
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

func (a *app) portScan() string {
	out, err := a.mgr.ScanPorts()
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// adoptPort 只做一次「能不能收进来」的预演：它读目录、认类型、挑一个空闲端口，
// 返回的是检查结果，不落盘。真正写清单的还是 saveService——用户要在表单里
// 看过一遍再按保存，而不是点一下「纳管」清单就悄悄变了。
func (a *app) adoptPort(portRaw, name string) string {
	out, err := a.mgr.AdoptPort(portRaw, name)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
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

// scanDir 扫一个目录，列出里面认出来的项目（空清单那一屏用的就是它）。
func (a *app) scanDir(dir string) string {
	out, err := a.mgr.ScanDir(dir)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// addScanned 把用户勾中的那几条一次加进来。收的是 JSON 数组（webview 绑定的形状），
// 解析在这一层做完，业务层只看见成形的字段。
func (a *app) addScanned(payload string) string {
	var items []manage.ServiceIn
	if err := json.Unmarshal([]byte(payload), &items); err != nil {
		return errJSON("提交的内容无法解析：" + err.Error())
	}
	out, err := a.mgr.AddScanned(items)
	if err != nil {
		return errJSON(err.Error())
	}
	return marshal(out)
}

// saveSharedEnv 收的是界面那块多行文本解析出来的对象（见 app.js 的 textToEnv），
// 与 saveService 一样，解析留在这一层，业务层只看见成形的字段。
func (a *app) saveSharedEnv(payload string) string {
	var in struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(payload), &in); err != nil {
		return errJSON("提交的内容无法解析：" + err.Error())
	}
	msg, err := a.mgr.SaveSharedEnv(in.Env)
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
// 落盘的位置交给系统的存储对话框，各平台用哪个命令弹见 picker_<平台>.go。
// 同名文件由对话框自己问「要替换吗」。
func (a *app) exportConfig() string {
	text, err := a.mgr.ExportYAML()
	if err != nil {
		return errJSON(err.Error())
	}

	picked, canceled, err := pickSavePath("pier.yaml")
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
	picked, canceled, err := pickOpenPath("选择一份清单（pier.yaml 或 Pier 的数据文件）")
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
	path, src, err := config.Resolve("")
	if err != nil {
		return errJSON(err.Error())
	}
	if err := a.panel.Load(path); err != nil {
		return errJSON(err.Error())
	}
	a.panel.SetSource(src)
	return okJSON("已切回本机数据")
}

// ── 让系统弹出选择框 ───────────────────────────────────────────────────────
//
// 让用户手打路径是很糟的体验：路径长、容易打错，而且他有现成的文件管理器。
// 各平台用哪个命令弹这些框见 picker_<平台>.go，这里只有两件共用的事：
// 算起点，以及把回来的路径收拾干净。
//
// 不用 cgo 去调各家的原生接口：那要为三个平台各引一套桥接，
// 而 build-app.sh 现在只依赖 Go + 各平台的命令行工具就能出包，
// 为一个小弹窗破坏这一点不划算。
//
// 这一段只负责算起点：优先用用户已填的目录，其次工作空间根目录，最后回到用户主目录。
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

	picked, canceled, err := pickDir(start)
	if err != nil {
		return errJSON("打开目录选择框失败：" + err.Error())
	}
	if canceled || picked == "" {
		return marshal(map[string]any{"ok": true, "canceled": true})
	}

	out := map[string]any{"ok": true, "dir": picked}
	// 顺带把相对路径算出来，界面可以直接填进表单并显示成相对工作空间的路径。
	if cfg != nil {
		out["rel"] = cfg.RelTo(picked)
	}
	return marshal(out)
}

// cleanPath 收拾选择框回来的那串路径。
//
// 去空白是因为命令行工具的输出常带一个换行；Clean 是为了结尾斜杠——
// AppleScript 的 POSIX path 一定会带，存进数据文件和显示时都不干净。
func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return filepath.Clean(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
