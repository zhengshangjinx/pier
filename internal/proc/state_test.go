package proc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 状态文件的每次更新都是「读出整份、改一处、写回整份」，两个人同时来就会丢更新：
// 各自读出一份旧内容、各自写回，后写的那个把前一个刚记下的进程整条抹掉。
func TestUpdateStateKeepsConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	const n = 16

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("svc-%d", i)
			// 停顿一下：不加锁的话，「读」和「写」之间必然被别的协程插进来。
			err := UpdateState(path, func(st *State) error {
				time.Sleep(time.Millisecond)
				st.Services[name] = &Entry{PID: os.Getpid()}
				return nil
			})
			if err != nil {
				t.Errorf("更新 %s 失败：%v", name, err)
			}
		}(i)
	}
	wg.Wait()

	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("读状态文件失败：%v", err)
	}
	if len(st.Services) != n {
		t.Errorf("并发写了 %d 条，状态文件里只剩 %d 条：丢了记录的服务就成了界面看不见、"+
			"命令行也停不掉的孤儿", n, len(st.Services))
	}
}

// fn 返回错误时这次更新整个作废，磁盘上不能留下半份改动。
func TestUpdateStateAbortsOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := UpdateState(path, func(st *State) error {
		st.Services["keep"] = &Entry{PID: os.Getpid()}
		return nil
	}); err != nil {
		t.Fatalf("第一次更新失败：%v", err)
	}

	wantErr := errors.New("不写了")
	if err := UpdateState(path, func(st *State) error {
		st.Services["half"] = &Entry{PID: os.Getpid()}
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("错误没有原样返回：%v", err)
	}

	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("读状态文件失败：%v", err)
	}
	if _, ok := st.Services["half"]; ok {
		t.Error("fn 报错之后改动还是落盘了")
	}
	if _, ok := st.Services["keep"]; !ok {
		t.Error("fn 报错把上一次的内容也弄丢了")
	}
}

// 起一个真进程，再让状态写不进去，看它会不会被收掉。
//
// 进程已经拉起来、状态却没记上，这个服务就成了孤儿：界面按清单渲染看不见它，
// pier down 也找不着它，只能去活动监视器手工杀——正是这次要堵的那类问题。
// 制造写入失败的办法是在 state.json.tmp 的位置先放一个目录（Save 走的是
// 「写 .tmp 再改名」），不需要动权限，root 下跑也一样成立。

// RunningService 是「这条记录说的那个进程此刻听在哪个端口上」的唯一答案，
// 探针、日志头、界面都从它拿。三种「没有可换的」都要原样退回清单那一份：
// 退回同一个指针最省事，也说明它一分钱都没多花。
func TestRunningServiceSwapsPortFromTheEntry(t *testing.T) {
	svc := &config.Service{Name: "api", Port: 8080, Health: "http://localhost:8080/health"}

	swapped := RunningService(svc, &State{Services: map[string]*Entry{
		"api": {PID: 1, Port: 8081},
	}})
	if swapped == svc {
		t.Fatal("记录里写着别的端口，应当给一份换过的副本")
	}
	if swapped.Port != 8081 {
		t.Errorf("Port = %d，想要 8081", swapped.Port)
	}
	// 探针地址跟着换：不换就会去探旧端口上那个陌生进程。
	if want := "http://localhost:8081/health"; swapped.Health != want {
		t.Errorf("Health = %q，想要 %q", swapped.Health, want)
	}

	cases := []struct {
		name  string
		svc   *config.Service
		state *State
	}{
		{"没有状态文件那一份", svc, nil},
		{"记录里没写端口", svc, &State{Services: map[string]*Entry{"api": {PID: 1}}}},
		{"这个服务没有记录", svc, &State{Services: map[string]*Entry{}}},
		{"服务是 nil", nil, &State{Services: map[string]*Entry{"api": {PID: 1, Port: 8081}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RunningService(c.svc, c.state); got != c.svc {
				t.Errorf("应当原样返回清单里那一份，拿到的是 %+v", got)
			}
		})
	}
}

// 重启时该用哪个端口：上一次是从清单里那个让路出来的、而它现在还被占着，就接着让路后的
// 那个；已经不占了（占着的那个进程走了）就回到清单里写的那个——临时选择不该变成永久选择。
//
// 占端口这件事要拿真实的监听来立：判定读的就是真实的监听表，注入一个假的表
// 就等于把这条判定换成「我假设它会这么写」。
func TestResumePortOnlyWhileTheManifestPortIsStillTaken(t *testing.T) {
	hold := func() (int, func()) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("占端口失败：%v", err)
		}
		release := func() { _ = ln.Close() }
		t.Cleanup(release)
		return ln.Addr().(*net.TCPAddr).Port, release
	}
	manifest, release := hold()
	other, releaseOther := hold()
	releaseOther()

	svc := &config.Service{Name: "api", Port: manifest}
	state := &State{Services: map[string]*Entry{"api": {PID: 1, Port: other}}}

	if got := ResumePort(svc, state); got != other {
		t.Errorf("清单里那个还占着时 ResumePort = %d，想要接着用 %d", got, other)
	}

	release()
	if got := ResumePort(svc, state); got != 0 {
		t.Errorf("清单里那个空出来之后 ResumePort = %d，想要 0（回清单）", got)
	}

	cases := []struct {
		name  string
		svc   *config.Service
		state *State
	}{
		{"记录里那个就是清单里那个", svc, &State{Services: map[string]*Entry{"api": {PID: 1, Port: manifest}}}},
		{"记录里没写端口", svc, &State{Services: map[string]*Entry{"api": {PID: 1}}}},
		{"这个服务没有记录", svc, &State{Services: map[string]*Entry{}}},
		{"清单里没写端口", &config.Service{Name: "api"}, state},
		{"没有状态文件那一份", svc, nil},
		{"服务是 nil", nil, state},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResumePort(c.svc, c.state); got != 0 {
				t.Errorf("ResumePort = %d，想要 0（回清单）", got)
			}
		})
	}
}
