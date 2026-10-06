package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsName)

	s, err := LoadSettings(path)
	if err != nil || s.Theme != "system" {
		t.Fatalf("文件不存在时应当返回默认值 system：%+v %v", s, err)
	}
	if err := SaveSettings(path, Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(path); s.Theme != "dark" {
		t.Errorf("存进去又读出来不一致：%+v", s)
	}
	if err := SaveSettings(path, Settings{Theme: "purple"}); err == nil {
		t.Error("不认识的主题应当被拒绝")
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadSettings(path); err == nil || s.Theme != "system" {
		t.Errorf("文件坏了应当报错并退回默认值：%+v %v", s, err)
	}
}

// TestSettingsRememberPageAndWindow 核「上次停在哪一页、窗口摆在哪」这一对。
//
// 两个都要能空着读回来（老文件里没这两个键，那就是「全部服务」和「没记过」），
// 因为默认值这一处只管开关，不该顺手给窗口编一个尺寸出来——编出来的那个会被
// 当成「用户上次就是这么摆的」，从此每次启动都照它摆。
func TestSettingsRememberPageAndWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsName)

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Page != "" || s.Window != (WindowBox{}) {
		t.Errorf("默认值里不该有页面与窗口：%+v", s)
	}

	// 老文件：只有主题，没有这两个键。
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Page != "" || s.Window != (WindowBox{}) {
		t.Errorf("老文件读出来应当是空的一对：%+v", s)
	}

	box := WindowBox{W: 1200, H: 800, X: -900, Y: 80}
	want := Settings{Theme: "dark", Page: "示例服务", Window: box}
	if err := SaveSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != want.Page || got.Window != box {
		t.Errorf("存进去又读出来不一致：%+v", got)
	}

	// 位置为 0 的那两格不能被省略掉：窗口正好摆在屏幕左下角时 x/y 就是 0，
	// 省掉之后下次启动会跑到别处去（json 标签上有 omitempty 就是这个下场）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Window map[string]int `json:"window"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"w", "h", "x", "y"} {
		if _, ok := probe.Window[k]; !ok {
			t.Errorf("写出来的 window 少了一个 %q：%s", k, raw)
		}
	}
}

// TestUpdateSettingsKeepsEveryWriter 盯住「读—改—写」上那把锁。
//
// 改偏好的人不止一个，而且各改各的一项（见 UpdateSettings 的说明）。少了那把锁，
// 两个人会各拿着同一份旧内容往回写，后写的那一份把先写的改动抹掉，而两边都报成功
// ——这类丢失没有任何痕迹，只会在某天表现为「我明明关过那个开关」。
//
// 每个写者往同一个键上追加一条，最后数总数：这种形态下丢一条就是一次对撞，
// 数得出来，也不靠运气。
func TestUpdateSettingsKeepsEveryWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsName)
	const writers = 32

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := UpdateSettings(path, func(s *Settings) {
				if s.SDKs == nil {
					s.SDKs = map[string][]string{}
				}
				s.SDKs["java"] = append(s.SDKs["java"], fmt.Sprint(i))
			})
			if err != nil {
				t.Errorf("写偏好失败：%v", err)
			}
		}()
	}
	wg.Wait()

	s, err := LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.SDKs["java"]); got != writers {
		t.Errorf("留下 %d 条，%d 个写者各写了一条——中间有几次被别人的写盖掉了", got, writers)
	}
}
