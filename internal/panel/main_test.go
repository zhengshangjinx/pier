package panel

import (
	"os"
	"testing"
)

// TestMain 把数据目录整个换到临时位置。
//
// New 会去数据目录里抢 restart.lock、读 restarts.json（见 ensureRestartLock），
// 这不换位置的话每跑一次测试都会在开发机的 ~/.pier 里留下一把锁和一份记账；
// 而那份记账还会倒过来影响下一个用例——前一次跑剩下的时刻会让某个服务的额度
// 凭空少几次，用例之间就开始互相干扰。仓库的规矩是「测试一律设 PIER_HOME，
// 绝不碰真实数据」，这一条由这里统一兜住，不用每个用例自己记得设。
//
// 要自己换一个位置的用例照旧用 t.Setenv，它在用例结束时恢复成这里的这个。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pier-panel-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("PIER_HOME", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
