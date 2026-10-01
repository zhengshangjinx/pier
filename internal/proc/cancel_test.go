package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
)

// 编译中点了停止：编译要立刻结束，而且它派生的子进程也得一起没——
// mvn 是个脚本，真正干活的是它派生的 java，只杀脚本的话 java 会变成孤儿继续编译。
func TestStartContextCancelKillsWholeBuildGroup(t *testing.T) {
	dir := t.TempDir()
	childPID := filepath.Join(dir, "child.pid")
	yaml := "services:\n  - name: slow\n    dir: " + dir + "\n    kind: go\n" +
		"    build: \"sleep 60 & echo $! > " + childPID + "; wait\"\n" +
		"    run: \"sleep 60\"\n"
	manifest := filepath.Join(dir, "pier.yaml")
	if err := os.WriteFile(manifest, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(manifest)
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := cfg.Find("slow")
	sup := New(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.StartContext(ctx, svc) }()

	// 等编译真的跑起来（子进程把自己的 PID 写出来）再取消。
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		raw, _ := os.ReadFile(childPID)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	if pid == 0 {
		select {
		case err := <-done:
			t.Fatalf("编译步骤没有跑起来，StartContext 提前返回：%v", err)
		default:
			t.Fatal("编译步骤没有跑起来")
		}
	}
	begin := time.Now()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("取消后应当返回 context.Canceled，实际 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消之后编译没有结束")
	}
	if d := time.Since(begin); d > 7*time.Second {
		t.Errorf("取消到结束用了 %s，太慢", d)
	}
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Error("编译派生的子进程在取消后还活着，成了孤儿")
	}
	if st, _ := LoadState(cfg.StatePath()); st != nil && st.Services["slow"] != nil {
		t.Error("取消后不该留下运行记录")
	}
}
