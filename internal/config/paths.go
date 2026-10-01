package config

import (
	"path/filepath"
	"time"
)

// RuntimeDirName 是存放日志、状态与编译产物的目录名，位于配置文件同级。
const RuntimeDirName = ".pier"

// LogDateLayout 是日志文件名的日期格式：一天一个文件，文件名就是那一天。
// 用「年-月-日」而不是时间戳：日志是按天翻的，人要能一眼读出这是哪天，
// 也要能直接 sort 出先后。
const LogDateLayout = "2006-01-02"

// RuntimeDir 返回运行期产物根目录。
func (c *Config) RuntimeDir() string {
	return filepath.Join(c.Dir(), RuntimeDirName)
}

// LogDir 返回服务日志目录。
func (c *Config) LogDir() string {
	if c.logDir != "" {
		return c.logDir
	}
	return filepath.Join(c.RuntimeDir(), "logs")
}

// BinDir 返回编译产物目录。Go 服务编译到这里，以便进程名可辨识。
func (c *Config) BinDir() string {
	if c.binDir != "" {
		return c.binDir
	}
	return filepath.Join(c.RuntimeDir(), "bin")
}

// StatePath 返回进程状态文件路径。
func (c *Config) StatePath() string {
	if c.statePath != "" {
		return c.statePath
	}
	return filepath.Join(c.RuntimeDir(), "state.json")
}

// LogDirFor 返回某个服务的日志目录，一天一个文件放在里面。
//
// 一个服务一个目录，而不是把 <服务名>-<日期>.log 摊在 logs/ 下：
// 两段信息用同一个连字符连起来之后就分不开了（demo-admin-2026-10-01.log
// 到底是哪个服务、哪一天，得靠猜），分开放两段各自独立、也各自能整段引用。
func (c *Config) LogDirFor(name string) string {
	return filepath.Join(c.LogDir(), name)
}

// LogPathOn 返回服务在 t 这一天写的那份日志。
//
// 按天分文件是为了让单个文件有上界：一个长期跑着的服务原先只往一个文件里追加，
// 跑上几周就是几百兆，读它的界面要整读、打开它的人要等，而里面绝大多数内容
// 早就没人看了。分天之后，一天最多一份，能按天数清理。
//
// 注意跨零点的那次运行仍然写它启动那天的文件——服务进程手里的 fd 是启动时
// 打开的，Pier 之后就不管它了，换不了。所以这里的口径是「哪天启动的写哪天」，
// 不是「日期变了就换一份」。
func (c *Config) LogPathOn(name string, t time.Time) string {
	return filepath.Join(c.LogDirFor(name), t.Format(LogDateLayout)+".log")
}

// LogPath 返回指定服务今天那份日志的路径。
// 已经跑着的服务可能还在写更早的那份，要拿到真正在写的那个用 proc.LogFile。
func (c *Config) LogPath(name string) string {
	return c.LogPathOn(name, time.Now())
}

// LogPathDate 返回某一天的日志路径，日期就是 LogDateLayout 那种写法
// （文件名去掉 .log 之后的那串）。
//
// 不经过 time.Time：调用方手里本来就是「哪一天」这个名字，解成时间再格式化
// 平白多一层时区，而这里自始至终只是一个文件名。调用方仍需自己确认这个串
// 长得像日期——它会被拼进路径。
func (c *Config) LogPathDate(name, date string) string {
	return filepath.Join(c.LogDirFor(name), date+".log")
}
