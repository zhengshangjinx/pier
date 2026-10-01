package proc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
func TestStartRollsBackWhenStateCannotBeWritten(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir()) // 别碰真实数据
	dir := t.TempDir()
	const token = "sleep 9876.5"
	manifest := filepath.Join(dir, "pier.yaml")
	yaml := "services:\n  - name: rollback-probe\n    dir: " + dir + "\n    kind: shell\n    run: \"" + token + "\"\n"
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.StatePath()+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}

	svc, _ := cfg.Find("rollback-probe")
	err = New(cfg).Start(svc)
	if err == nil {
		t.Fatal("状态文件写不进去，启动却报成功了")
	}
	if !strings.Contains(err.Error(), "回收") {
		t.Errorf("错误没有说明进程已被收掉：%v", err)
	}
	// 收干净了才返回：这里不该再有这个进程活着。
	if _, err := sysOutput("pgrep", "-f", token); err == nil {
		t.Errorf("状态没能记上，进程却还活着：%s", token)
	}
}
