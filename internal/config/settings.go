package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SettingsName 是界面偏好文件名，和 services.json 在同一个数据目录里。
//
// 偏好原来存在 WebView 的 localStorage 里：那份存储藏在系统的 WebKit 目录深处，
// 清数据、换机器、排查问题时都找不到它。放进数据目录，所有东西就只在 ~/.pier 一处。
const SettingsName = "settings.json"

// Settings 是界面偏好。
type Settings struct {
	// Theme 取 light / dark / system，空值按 system。
	Theme string `json:"theme"`
	// SDKs 是「SDK 管理」里手动添加的 SDK 路径，按类别（java / maven / node / python / go）分。
	// 自动扫描覆盖不到的位置（公司内网镜像解压出来的 JDK、自己编译的 Python）靠它补上。
	SDKs map[string][]string `json:"sdks,omitempty"`
	// SDKDefaults 是各类别的全局默认 SDK（路径）。没设的类别按规则自动选。
	SDKDefaults map[string]string `json:"sdkDefaults,omitempty"`

	// APIToken 是本地 HTTP 接口的访问令牌，空表示还没生成过。
	//
	// 存下来而不是每次启动现生成：脚本是从别处读它的，每次重启换一个的话，
	// 写好的脚本隔天就跑不通了。
	APIToken string `json:"apiToken,omitempty"`
	// APIPort 是本地 HTTP 接口监听的端口，0 或越界时用默认值。
	APIPort int `json:"apiPort,omitempty"`

	// UpdateCheck 是「自动检查更新」开关，默认开。
	//
	// 刻意不加 omitempty：关掉之后要写进文件的是那个 false，加了就会被写没，
	// 下次读回来又变成默认的 true——用户关了一次，看着像没关住。
	UpdateCheck bool `json:"updateCheck"`
	// UpdateSkipped 是用户点名跳过的版本号（如 0.3.0），空表示没跳过谁。
	// 跳过是偏好，放这儿；「上次查到什么」是状态，放 update.json，两边不重复记。
	UpdateSkipped string `json:"updateSkipped,omitempty"`

	// Notify 是「服务出事时弹系统通知」开关，默认开。
	//
	// 与 UpdateCheck 同理，不加 omitempty：关掉之后要写进文件的是那个 false。
	//
	// 默认开是因为它只在真出事时响（异常退出、重启到上限、启动失败）：
	// 一个天天在跑的服务崩了，用户多半不在终端面前，而这件事只有通知能追到他。
	Notify bool `json:"notify"`

	// Page 是上次关窗时停在哪儿："" 表示「全部服务」，否则是分组名。
	//
	// 存的是名字而不是下标：分组会增删改名，一个位置第二天可能指到别的分组上。
	// 那一页已经没了就退回「全部服务」（见 gui/app.js 里挑初值那一段）。
	Page string `json:"page,omitempty"`

	// Window 是上次关窗时的窗口外框，全零表示还没记过（第一次运行）。
	//
	// 记在偏好里而不是另开一个文件：这是「我的窗口摆在哪」，与主题同一类东西，
	// 而且它必须和别的偏好共用一个「读—改—写」（见 UpdateSettings）。
	//
	// omitzero 只在整块为零时省掉它；里面四个数**不能**加 omitempty——
	// 窗口正好摆在屏幕左上角时 x/y 就是 0，省掉之后下次启动会跑到别处去。
	Window WindowBox `json:"window,omitzero"`
}

// WindowBox 是一块窗口外框：尺寸与屏幕坐标。
//
// 尺寸是**外框**（含标题栏与边框），不是内容区：三平台的 webview_set_size
// 收的都是外框（macOS 走 setFrame、Windows 用 AdjustWindowRectExForDpi 把客户区
// 换算成外框、GTK 直接 gtk_window_resize），而页面上拿到的 innerWidth 是内容区。
// 拿内容区的尺寸喂回去，Windows 上每重启一次就各缩掉一圈装饰边框。
// 所以这一份只在原生侧读写，界面不参与。
type WindowBox struct {
	W int `json:"w"`
	H int `json:"h"`
	X int `json:"x"`
	Y int `json:"y"`
}

// SettingsPath 返回默认数据目录下的偏好文件路径。
func SettingsPath() (string, error) {
	d, err := Dirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(d.Data, SettingsName), nil
}

// defaultSettings 是偏好文件的默认值。
//
// 新加的开关默认是开是关只写在这一个地方，往后加字段也往这儿加。
// 读文件那条路不用另写迁移：LoadSettings 先铺这份默认值再 Unmarshal，
// JSON 里没有的键不会覆盖已有的值——老用户的 settings.json 里没有 updateCheck，
// 读出来就是这里的 true。
func defaultSettings() Settings {
	return Settings{Theme: "system", UpdateCheck: true, Notify: true}
}

// LoadSettings 读取偏好；文件不存在时返回默认值，不算错误。
func LoadSettings(path string) (Settings, error) {
	s := defaultSettings()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, fmt.Errorf("读取偏好失败：%w", err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return defaultSettings(), fmt.Errorf("偏好文件 %s 已损坏：%w", path, err)
	}
	if !validTheme(s.Theme) {
		s.Theme = "system"
	}
	return s, nil
}

// settingsMu 串起对偏好文件的「读—改—写」。
//
// 写偏好的人不止一个，而且各改各的一项：界面上那个开关、更新里跳过的版本、
// 本地接口的令牌与端口、窗口外框那条轮询。整份读进来、改一项、整份写回去，
// 中间那一段若不挡着，两个人就会各拿着同一份旧内容往回写——后写的那一份
// 把先写的那个改动抹掉，而两边都报成功。窗口外框那条每几秒就写一次，
// 撞上别的写者的机会一点都不小。
//
// 锁活在进程里而不是文件上：要防的是同一个进程里两条协程对撞。
// 两个界面同时开着是另一回事（各自的窗口外框会互相盖），不值当为它引文件锁——
// 那要处理死锁与陈旧锁，代价远大于「两个窗口谁最后挪谁说了算」。
var settingsMu sync.Mutex

// UpdateSettings 读出偏好、交给 fn 修改、再整份写回。
// 主题、SDK 各改各的，谁都不该因为只知道自己那一项就把别的项写没了。
func UpdateSettings(path string, fn func(*Settings)) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s, err := LoadSettings(path)
	if err != nil {
		return err
	}
	fn(&s)
	return SaveSettings(path, s)
}

// DefaultSettings 读默认位置的偏好；读不到就给默认值，不报错（偏好坏了不该拦着服务启动）。
func DefaultSettings() Settings {
	if p, err := SettingsPath(); err == nil {
		if s, err := LoadSettings(p); err == nil {
			return s
		}
	}
	return defaultSettings()
}

// SaveSettings 整份写回偏好。
func SaveSettings(path string, s Settings) error {
	if !validTheme(s.Theme) {
		return fmt.Errorf("不认识的主题：%q", s.Theme)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(raw, '\n'))
}

func validTheme(t string) bool {
	return t == "light" || t == "dark" || t == "system"
}
