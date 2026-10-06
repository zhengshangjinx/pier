package main

import (
	"testing"

	"github.com/zhengshangjinx/pier/internal/config"
)

// TestRestoreWindowBox 盯住「记下来的那块外框还能不能照搬」这一道判断。
//
// 记下来的东西一定来自上一次真实的窗口，所以这里要防的不是它「本来就对」，
// 而是它跨了几次启停之后还把窗口摆得住：屏幕换小了要收得回来，记了个不成形的
// 值要当没记过——照搬一块零尺寸的外框，窗口开出来是一根线。
func TestRestoreWindowBox(t *testing.T) {
	cases := []struct {
		name string
		in   config.WindowBox
		ok   bool
	}{
		{"没记过", config.WindowBox{}, false},
		{"只记了位置没记尺寸", config.WindowBox{X: 120, Y: 80}, false},
		{"尺寸是负的", config.WindowBox{W: -100, H: -100}, false},
		{"只有高度不成形", config.WindowBox{W: 1200, H: 0}, false},
		{"正常的一块", config.WindowBox{W: 1200, H: 800, X: 120, Y: 80}, true},
		// 摆到副屏左侧时 x 是负的，那是合法位置，不能当成坏值。
		{"位置为负", config.WindowBox{W: 1200, H: 800, X: -900, Y: -200}, true},
		{"小到拿不住", config.WindowBox{W: 40, H: 30}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := restoreWindowBox(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v，想要 %v", ok, c.ok)
			}
			if !ok {
				if got != (config.WindowBox{}) {
					t.Errorf("说不记的时候应当给一块空的，实际 %+v", got)
				}
				return
			}
			if got.X != c.in.X || got.Y != c.in.Y {
				t.Errorf("位置被改动了：%+v，想要 (%d, %d)", got, c.in.X, c.in.Y)
			}
			// 下限只往上抬，收不到下限以上的尺寸不该被动过。
			if want := max(c.in.W, windowFloorW); got.W != want {
				t.Errorf("宽 = %d，想要 %d（下限 %d）", got.W, want, windowFloorW)
			}
			if want := max(c.in.H, windowFloorH); got.H != want {
				t.Errorf("高 = %d，想要 %d（下限 %d）", got.H, want, windowFloorH)
			}
			// 屏幕量得到的时候还要收进屏幕。这一条在量不到屏幕的机器上不成立
			// （比如没有图形会话的构建机上 screenVisible 返回 0），所以只在
			// 量到时才核——量不到时上面的下限就是唯一的约束。
			if sw, sh := screenVisible(); sw > 0 && sh > 0 {
				if float64(got.W) > sw || float64(got.H) > sh {
					t.Errorf("尺寸 %dx%d 超出了屏幕 %.0fx%.0f", got.W, got.H, sw, sh)
				}
			}
		})
	}
}
