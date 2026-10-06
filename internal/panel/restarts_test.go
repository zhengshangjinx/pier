package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadRestartsDropsExpired 钉着窗口外的记录读回来就丢。
//
// 文件里存的是时刻而不是结论：过去多久算数、几次算满，仍然只由 panel 说了算
// （见 restarts.go）。一条三天前的记录还占着今天的额度，是把上一次运行的账
// 算到了这一次头上，而这两件事之间早就没有关系了。
func TestLoadRestartsDropsExpired(t *testing.T) {
	t.Setenv("PIER_HOME", t.TempDir())
	now := time.Now()
	// 手工造一份「上次运行时写的」文件：saveRestarts 只负责写，不负责滤。
	saveRestarts(map[string][]time.Time{
		// 次序故意反着放：文件可能被手工改过，读回来不能假定它已经排好。
		"fresh": {now.Add(-time.Minute), now.Add(-2 * time.Minute)},
		"stale": {now.Add(-restartWindow - time.Minute)},
	})

	got := loadRestarts(now)
	if n := len(got["fresh"]); n != 2 {
		t.Errorf("窗口内的记录读回来 %d 条，想要 2 条", n)
	}
	if n := len(got["stale"]); n != 0 {
		t.Errorf("窗口外的记录还留着 %d 条", n)
	}
	// 排好之后 restartNote 里那句「第 N 次」才是有序的。
	if ok := len(got["fresh"]) == 2 && got["fresh"][0].Before(got["fresh"][1]); !ok {
		t.Errorf("读回来的记录没按时间排：%v", got["fresh"])
	}
}

// TestLoadRestartsToleratesGarbage 钉着坏文件一律当没有。
//
// 为一个随时可重建的计数器报错（还是在启动的时候）比这件事本身更烦人，
// 而这份文件写坏的代价只是某个服务多拿到一个额度。与 state.json 同一条规矩。
func TestLoadRestartsToleratesGarbage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)
	if err := os.WriteFile(filepath.Join(home, restartsFileName), []byte("{不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadRestarts(time.Now()); len(got) != 0 {
		t.Errorf("坏文件读出来 %v，想要空", got)
	}
}

// TestSaveRestartsDropsEmptyFile 钉着账本空了就把文件删掉，不留一份空壳。
//
// 删掉一个服务、或者所有记录都过期之后，留下的空壳只会让下一个读它的人
// 还要再滤一遍，而它自己不带任何信息。
func TestSaveRestartsDropsEmptyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIER_HOME", home)
	path := filepath.Join(home, restartsFileName)

	saveRestarts(map[string][]time.Time{"alpha": {time.Now()}})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("记了一笔之后文件该在：%v", err)
	}
	saveRestarts(map[string][]time.Time{})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("账本空了之后文件还在（stat 的错 = %v）", err)
	}
}
