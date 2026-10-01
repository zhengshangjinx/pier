package proc

import (
	"fmt"
	"os/exec"
	"strings"
)

// 系统自带命令（lsof、ps）一律按绝对路径找，不依赖 PATH。
//
// 这一条是被「端口占用查不出来」逼出来的。原先这里写的是 exec.Command("lsof", ...)，
// 而 lsof 住在 /usr/sbin —— 这个目录在 PATH 里并不是理所当然的：
// Pier 打包成 .app 从访达启动时，继承的是 launchd 给的那份最小环境，
// PATH 里有什么全看系统版本和用户自己的 shell 配置，跟终端里跑完全是两回事。
//
// 失败的后果特别隐蔽。lsof 起不来 → err 非 nil → 上游把「查不到」当成
// 「端口上没人监听」，界面于是渲染出一排「—」，用户看着和「确实没人占用」
// 长得一模一样。这个工具的全部卖点就是「本身不需要什么环境」，
// 那它自己依赖的环境就得自己兜住，不能指望用户去配 PATH。
//
// ps 同理，它是「启动于」「命令行」两栏的来源，缺了同样是一片「—」。
var sysBinCand = map[string][]string{
	// /usr/sbin 在前：这是 macOS 上 lsof 的实际位置，先试它省掉一次 stat。
	"lsof": {"/usr/sbin/lsof", "/usr/bin/lsof", "/bin/lsof", "/usr/local/sbin/lsof"},
	"ps":   {"/bin/ps", "/usr/bin/ps", "/sbin/ps"},
}

// sysBin 返回系统命令的绝对路径：先按已知位置逐个试，都不在才回落到 PATH。
//
// 回落是留给非常规安装的，macOS 上正常走不到那里。
func sysBin(name string) (string, error) {
	for _, c := range sysBinCand[name] {
		if isExecutable(c) {
			return c, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("找不到系统命令 %s（已试过 %v）", name, sysBinCand[name])
}

// sysOutput 执行系统命令并取其标准输出。
//
// 单独包一层是为了让调用点保持一行：这些命令的取法完全一致，
// 散在五处各写一遍容易漏改——而漏掉任何一处，那一处就退回成依赖 PATH 的老样子。
func sysOutput(name string, args ...string) ([]byte, error) {
	bin, err := sysBin(name)
	if err != nil {
		return nil, err
	}
	return exec.Command(bin, args...).Output()
}

// PortToolsAvailable 报告端口占用与进程信息查询所依赖的系统命令是否齐备。
//
// 导出是给调用方一个「先把话说明白」的机会。lsof 一旦缺失，端口占用查询会退化成
// connect 探测，而 connect 探测对绑在 IPv6 通配地址上的服务会漏判——实测本机的
// Vite 开发服务器就是这种，连不上，于是占着的端口被报成空闲（见 PortOpen 的注释）。
// 那种情况下界面照常渲染，只是一排「—」，用户没有任何线索知道是工具缺了。
func PortToolsAvailable() error {
	var missing []string
	for _, name := range []string{"lsof", "ps"} {
		if _, err := sysBin(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少系统命令 %s，端口占用与进程信息查询不可用", strings.Join(missing, "、"))
	}
	return nil
}
