package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/manage"
)

// cmdDetect 扫描目录，识别项目类型，产出可直接粘贴进 pier.yaml 的服务定义。
//
// 识别本身（认什么、凭什么认、端口从哪儿读）在 internal/manage 的 scan.go 里，
// 与界面上那一屏「选一个目录，我扫一遍」是同一份代码——两个宿主各自写一遍的话，
// 命令行认得出来的东西界面认不出来，用户会以为是界面的毛病。这里只负责把结果
// 摆成一份 YAML。
func cmdDetect(args []string) int {
	cfgPath, rest := extractConfig(args)
	root := "."
	if len(rest) > 0 {
		root = rest[0]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fail("%v", err)
	}
	if fi, err := os.Stat(absRoot); err != nil || !fi.IsDir() {
		return fail("不是有效目录：%s", absRoot)
	}

	// dir 尽量写成相对配置文件目录的形式，配置才能跟着仓库一起走。
	// 清单读不出来也照扫（那种情况下扫出来的每一条都没有「启动命令」，
	// 因为推导要用到清单里的工具链设置）。
	mgr := manage.New(nil)
	baseDir := absRoot
	if cfg, err := loadConfig(cfgPath); err == nil {
		mgr.SetConfig(cfg)
		baseDir = cfg.Dir()
	}

	out, err := mgr.ScanDir(absRoot)
	if err != nil {
		return fail("%v", err)
	}
	if len(out.Items) == 0 {
		fmt.Println(out.Msg)
		return 0
	}

	services := make([]*config.Service, 0, len(out.Items))
	for _, it := range out.Items {
		dir := it.AbsPath
		if rel, err := filepath.Rel(baseDir, it.AbsPath); err == nil && !strings.HasPrefix(rel, "..") {
			dir = rel
		}
		services = append(services, &config.Service{
			Name: it.Name, Dir: dir, Kind: it.Kind,
			Module: it.Module, Script: it.Script,
			Port: it.Port, Health: it.Health,
		})
	}
	raw, err := yaml.Marshal(services)
	if err != nil {
		return fail("生成配置片段失败：%v", err)
	}

	fmt.Printf("扫描 %s，识别到 %d 个项目。\n", absRoot, len(services))
	fmt.Println("确认端口与健康检查路径后，粘贴到 pier.yaml 的 services 下：")
	fmt.Println()
	fmt.Print(string(raw))
	fmt.Println()
	fmt.Println("提示：port / health 为空的需要按项目实际配置补全；health 留空则不做就绪探测。")
	return 0
}
