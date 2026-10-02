package proc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry 是状态文件里一个服务的运行记录。
type Entry struct {
	PID int `json:"pid"`
	// PGID 是这条服务那棵树的组号：unix 上是 setsid 之后的进程组号（等于首进程 PID），
	// 端口归属、资源求和的键都用它。Windows 上没有可读的进程组，这里写的就是首进程 PID，
	// 「还是不是那个进程」另由 Ident 核对。
	PGID int `json:"pgid"`
	// Command 记录本次启动实际执行的命令，便于事后核对跑的是哪个模块。
	Command string `json:"command"`
	// StartedAt 用于计算运行时长。
	StartedAt time.Time `json:"startedAt"`
	// LogPath 是本次启动使用的日志文件。
	LogPath string `json:"logPath"`
	// Ident 是「这个进程还是当初记录的那一个」的辅助凭据，unix 上留空。
	//
	// unix 靠 PGID 就够：进程组号等于首进程 PID，PID 被系统复用时组号对不上。
	// Windows 没有进程组号可查，改记进程创建时间——**PID 会被复用，创建时间不会**，
	// 这是那边唯一能挡住「杀错一个恰好拿到同一个号的无辜进程」的东西。
	//
	// omitempty 不能去掉：unix 上它是空串，省掉之后 state.json 的内容与加这个字段
	// 之前逐字节相同（TestStateFileUnchangedByPlatformFields 钉着）。
	Ident string `json:"ident,omitempty"`
}

// State 是状态文件的根结构。只有 Pier 启动的服务才会出现在这里；
// 在 IDEA 或其它终端里手工起的服务不在其中，Pier 也不会去动它们。
type State struct {
	Services map[string]*Entry `json:"services"`
}

// LoadState 读取状态文件；文件不存在视为空状态。
func LoadState(path string) (*State, error) {
	s := &State{Services: map[string]*Entry{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取状态文件失败：%w", err)
	}
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("状态文件 %s 已损坏：%w", path, err)
	}
	if s.Services == nil {
		s.Services = map[string]*Entry{}
	}
	return s, nil
}

// Save 原子写入状态文件：先写临时文件再改名，避免中途中断留下半个文件。
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建状态目录失败：%w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入状态文件失败：%w", err)
	}
	return os.Rename(tmp, path)
}

// ── 状态文件的更新 ─────────────────────────────────────────────────────────
//
// state.json 的每次更新都是「读出整份、改一处、写回整份」（Save 是整份替换），
// 而写它的人不止一个：界面自己的启停队列与状态刷新、命令行的一次 pier down，
// 甚至同时开着的第二个界面。没有一把跨进程的锁，两边各自读出一份旧内容、
// 各自写回，后写的那个就把前一个刚记下的进程整条抹掉——服务明明在跑，
// 状态文件里却没有它，界面看不见、命令行也停不掉，只能去活动监视器手工杀。
//
// 锁落在状态文件旁边的 <名字>.lock 上，用 flock：它跟着打开的文件描述符走，
// 进程崩了由内核释放，不会留下需要人工清理的残留；那份文件本身不装任何数据，
// 删掉也无妨（下次 open 时重新建）。

// UpdateState 在锁的保护下对状态文件做一次「读—改—写」。
//
// fn 只改内存里的那份状态，落盘由这里负责。故意不提供「拿着锁想干什么干什么」的
// 形式：锁只该跨过读写这一小段，编译、等服务退出这类慢动作必须留在锁外，
// 否则停一个服务就能把这段时间里别人的启停全堵上。
func UpdateState(path string, fn func(*State) error) error {
	f, err := lockState(path)
	if err != nil {
		return err
	}
	defer unlockState(f)

	st, err := LoadState(path)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return st.Save(path)
}

func lockState(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建状态目录失败：%w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开状态锁失败：%w", err)
	}
	// 加锁与解锁各平台不一样（flock / LockFileEx），见 sys_unix.go 与 sys_windows.go。
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("锁定状态文件失败：%w", err)
	}
	return f, nil
}

func unlockState(f *os.File) {
	unlockFile(f)
	_ = f.Close()
}

// ManagedName 返回该进程属于哪个 Pier 启动的服务，不属于时返回空串。
//
// 不能只比 PID：服务实际是 mvn / pnpm / sh 这类会派生子进程的壳，端口往往握在
// 子进程手里（vite 的 node、spring-boot 的 JVM），记录在案的 PID 只是那个壳。
// 同一个进程组才算自己人——Setsid 之后整棵树的 PGID 就是首进程的 PID，
// 这一条在 state.json 里记着，也正是「停止」回收整组的依据。
func ManagedName(st *State, pid int) string {
	if st == nil || pid <= 0 {
		return ""
	}
	for name, e := range st.Services {
		if e == nil {
			continue
		}
		if e.PID == pid {
			return name
		}
		// 首领还活着才算数：记录留着而进程早没了时，PGID 可能已经被系统分给了
		// 别人，光比组号会认错。这和 Status 判「在跑」是同一个口径。
		if inEntryGroup(pid, e) {
			return name
		}
	}
	return ""
}

// RunningNames 返回状态文件里此刻确实还在跑的那些服务名。
//
// 「在跑」的判定和 Status、ManagedName 是同一条：进程还在，且进程组号对得上。
// 清理日志要拿它当护栏——跑着的服务手里的日志 fd 是启动那一刻打开的，
// 删掉文件只是从目录里摘掉，进程照样往里写，回收的空间也只有等它退出才真的空出来。
func RunningNames(path string) (map[string]bool, error) {
	st, err := LoadState(path)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for name, e := range st.Services {
		if sameEntry(e) {
			out[name] = true
		}
	}
	return out, nil
}

// Prune 清理其中进程已不存在的记录，返回被清理的服务名。
// 服务崩溃或被人手工 kill 后，状态文件不会自动更新，读到时才顺手清理。
func (s *State) Prune() []string {
	var dead []string
	for name, e := range s.Services {
		if !ProcessAlive(e.PID) {
			delete(s.Services, name)
			dead = append(dead, name)
		}
	}
	return dead
}
