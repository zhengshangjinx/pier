package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	return Settings{Theme: "system", UpdateCheck: true}
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

// UpdateSettings 读出偏好、交给 fn 修改、再整份写回。
// 主题、SDK 各改各的，谁都不该因为只知道自己那一项就把别的项写没了。
func UpdateSettings(path string, fn func(*Settings)) error {
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
