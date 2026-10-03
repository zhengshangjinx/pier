package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTryClaimIsExclusive 钉着「同一把锁只发给一个人」。
//
// 同一进程里再拿一次都不行，是刻意的：独占权说的是「这件事归谁做」，
// 而两个 Panel 打起来和两个进程打起来是一回事——都各自以为自己处理得很干净。
func TestTryClaimIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "lock")

	first, ok, err := TryClaim(path)
	if err != nil {
		t.Fatalf("第一次拿锁失败：%v", err)
	}
	if !ok {
		t.Fatal("第一次就没拿到")
	}

	second, ok, err := TryClaim(path)
	if err != nil {
		t.Fatalf("第二次拿锁报错了，本意是「拿不到」：%v", err)
	}
	if ok || second != nil {
		t.Error("同一把锁发给了两个人")
	}

	// 目录不存在时自己建：锁落在数据目录里，而数据目录可能是新建的。
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("锁目录没建出来：%v", err)
	}

	first.Release()
	third, ok, err := TryClaim(path)
	if err != nil || !ok {
		t.Fatalf("交回之后还拿不到：ok=%v err=%v", ok, err)
	}
	third.Release()
}

// TestClaimReleaseIsIdempotent 钉着重复交回不炸。
//
// 宿主可能既在收尾处调一次 Close，又在信号处理里调一次；Release 跟着被调两次。
func TestClaimReleaseIsIdempotent(t *testing.T) {
	c, ok, err := TryClaim(filepath.Join(t.TempDir(), "lock"))
	if err != nil || !ok {
		t.Fatalf("拿锁失败：ok=%v err=%v", ok, err)
	}
	c.Release()
	c.Release()

	// 没拿到锁时拿到的是一个 nil，宿主照样会去调 Release。
	var nilClaim *Claim
	nilClaim.Release()
	if nilClaim.Path() != "" {
		t.Error("nil 的 Path 不是空串")
	}
}

// TestClaimFileSurvivesRelease 钉着锁文件本身不删。
//
// 删掉的话，正在等锁的那个进程手里握着的是一个已经不在目录里的文件描述符，
// 两边各锁各的，锁就白加了。
func TestClaimFileSurvivesRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	c, ok, err := TryClaim(path)
	if err != nil || !ok {
		t.Fatalf("拿锁失败：ok=%v err=%v", ok, err)
	}
	c.Release()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("交回之后锁文件不见了：%v", err)
	}
}

// claimHelperEnv 让测试二进制把自己再起一份，专门去拿一次锁。
//
// 不另写一个小程序去 go run：internal 底下的包只有同一模块里的代码能引，
// 而写进仓库里的小程序会一直留在那儿。测试二进制本身就在模块里，
// 用它当第二个进程最省事。
const claimHelperEnv = "PIER_CLAIM_HELPER"

// TestClaimHelperProcess 不是用例，是被 re-exec 出来的那一份自己。
func TestClaimHelperProcess(t *testing.T) {
	path := os.Getenv(claimHelperEnv)
	if path == "" {
		t.Skip("不是被调用的那一份")
	}
	_, ok, err := TryClaim(path)
	fmt.Printf("拿锁结果 ok=%v err=%v\n", ok, err)
}

// TestClaimHoldsAcrossProcesses 换一个真进程再拿一次。
//
// 锁的整个意义就在跨进程，而 flock 对同一个进程里的重复加锁本来就会直接成功
// （Windows 那边的 LockFileEx 也一样）——只在本进程里验，等于什么都没验。
func TestClaimHoldsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	c, ok, err := TryClaim(path)
	if err != nil || !ok {
		t.Fatalf("拿锁失败：ok=%v err=%v", ok, err)
	}

	if got := claimInAnotherProcess(t, path); !strings.Contains(got, "ok=false") {
		t.Errorf("另一个进程拿到了锁：%q（本进程还握着 %s）", got, c.Path())
	}

	c.Release()
	if got := claimInAnotherProcess(t, path); !strings.Contains(got, "ok=true") {
		t.Errorf("交回之后另一个进程还是拿不到：%q", got)
	}
}

// claimInAnotherProcess 起一份测试二进制，让它去拿 path 这把锁，返回它说的话。
func claimInAnotherProcess(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestClaimHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(), claimHelperEnv+"="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("另一份进程没跑起来：%v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "拿锁结果") {
			return line
		}
	}
	t.Fatalf("另一份进程没报结果：\n%s", out)
	return ""
}
