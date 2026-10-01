// mkicon 生成应用图标。
//
// 用代码画而不是塞一张图片进来：图标要跟着配色改，位图改起来得开设计软件；
// 而且源码里放二进制资源既看不出改了什么，也没法在 review 时比对。
//
// 图形是一座栈桥（Pier）：一条桥面、底下的桩、水面，最右那根桩伸出桥面做灯杆，
// 顶上一盏绿灯。和侧栏里的标记（gui/app.js 的 Mark）是同一套 200×200 视框里的几何，
// 改动时两处一起对。
//
// 画法：先在 4 倍尺寸上画，再降采样。圆角和三角形边缘靠超采样获得平滑过渡，
// 比手工写抗锯齿简单得多，也不需要任何图像库。
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const (
	finalSize = 1024 // 输出尺寸
	ss        = 4    // 超采样倍数
	canvas    = finalSize * ss
	// inset 是内容距画布边缘的留白。macOS 的图标规范要求四周留出空间，
	// 画满整个方块的图标在 Dock 里会显得比别家大一圈。
	inset = 100 * ss
	// cornerRatio 是圆角半径与内容边长的比值，取自 macOS 图标的实际比例。
	cornerRatio = 0.2237
	// designBox 是设计稿里图形的坐标空间。图形按 200×200 的视框给出，
	// 这里把内容区等比映射到同一个视框上，图标与设计稿才不会各画各的。
	designBox = 200.0
)

var (
	// 底板是主色的竖向渐变，上浅下深，与界面的主色同源。
	colorTileTop = color.RGBA{0x3F, 0x6B, 0xFF, 0xFF}
	colorTileBot = color.RGBA{0x14, 0x33, 0xD6, 0xFF}
	colorMark    = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	colorLight   = color.RGBA{0x3D, 0xDC, 0x97, 0xFF}
)

// 设计稿里的几何（200×200 视框，已含整体下移 6 个单位的居中偏移）。

// capsule 是一段两端半圆的粗线：圆角矩形在这里都是细长条，用「到中轴线段的距离」判断最省事。
type capsule struct{ ax, ay, bx, by, r float64 }

// 圆角矩形 → 中轴线段 + 半径：宽 16 的竖条半径 8，高 18 的桥面半径 9。
var bars = []capsule{
	{133, 77, 133, 136, 8},  // 灯杆（最右那根桩），顶端与灯的光晕留出一点缝，交叠处会泛白
	{51, 103, 149, 103, 9},  // 桥面
	{67, 114, 67, 136, 8},   // 左桩
	{100, 114, 100, 136, 8}, // 中桩
}

const (
	lightCX, lightCY = 133.0, 46.0
	lightR           = 11.0
	haloR            = 19.0
	haloAlpha        = 0.28
	waveAlpha        = 0.45
	waveHalf         = 4.0 // 水面线宽的一半
)

// waveY 是水面的波形：四个半波，与 SVG 里那条三次曲线的起伏基本重合。
func waveY(x float64) float64 {
	return 158 - 4.5*math.Sin(math.Pi*(x-46)/27)
}

func main() {
	out := "build/icon.png"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fail(err)
	}
	f, err := os.Create(out)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, render()); err != nil {
		fail(err)
	}
	fmt.Println("图标已生成：" + out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "生成图标失败："+err.Error())
	os.Exit(1)
}

func render() *image.RGBA {
	side := float64(canvas - 2*inset)
	radius := cornerRatio * side
	// 设计坐标 → 画布坐标的缩放。内容区对应设计稿的 200×200 视框。
	k := side / designBox
	toDesign := func(v float64) float64 { return (v - float64(inset)) / k }

	// 水面先采样成折线，逐像素求到折线的距离。
	var wave [][2]float64
	for x := 46.0; x <= 154.0001; x += 1 {
		wave = append(wave, [2]float64{x, waveY(x)})
	}

	big := image.NewRGBA(image.Rect(0, 0, canvas, canvas))
	for y := 0; y < canvas; y++ {
		for x := 0; x < canvas; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if !inRoundRect(fx, fy, float64(inset), side, radius) {
				continue
			}
			dx, dy := toDesign(fx), toDesign(fy)
			c := lerp(colorTileTop, colorTileBot, dy/designBox)
			if distToPolyline(dx, dy, wave) <= waveHalf {
				c = blend(c, colorMark, waveAlpha)
			}
			for _, b := range bars {
				if distToSegment(dx, dy, b.ax, b.ay, b.bx, b.by) <= b.r {
					c = colorMark
				}
			}
			if d := dist(dx, dy, lightCX, lightCY); d <= lightR {
				c = colorLight
			} else if d <= haloR {
				c = blend(c, colorLight, haloAlpha)
			}
			big.SetRGBA(x, y, c)
		}
	}
	return downsample(big, ss)
}

// lerp 在两种颜色之间按 t（0~1）线性插值，用于底板的竖向渐变。
func lerp(a, b color.RGBA, t float64) color.RGBA {
	t = math.Max(0, math.Min(1, t))
	mix := func(p, q uint8) uint8 { return uint8(math.Round(float64(p) + (float64(q)-float64(p))*t)) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xFF}
}

// blend 把 top 以 alpha 的不透明度叠到 base 上。
func blend(base, top color.RGBA, alpha float64) color.RGBA {
	return lerp(base, top, alpha)
}

func distToPolyline(x, y float64, pts [][2]float64) float64 {
	best := math.Inf(1)
	for i := 1; i < len(pts); i++ {
		best = math.Min(best, distToSegment(x, y, pts[i-1][0], pts[i-1][1], pts[i][0], pts[i][1]))
	}
	return best
}

// inRoundRect 判断点是否落在圆角正方形内。x、y 为画布坐标，origin 与 side 描述内容区。
func inRoundRect(x, y, origin, side, radius float64) bool {
	cx, cy := origin+side/2, origin+side/2
	// 到「内缩后的矩形」的距离，负值表示在内部
	dx := abs(x-cx) - (side/2 - radius)
	dy := abs(y-cy) - (side/2 - radius)
	if dx <= 0 && dy <= 0 {
		return true
	}
	if dx < 0 {
		dx = 0
	}
	if dy < 0 {
		dy = 0
	}
	return dx*dx+dy*dy <= radius*radius
}

// distToSegment 返回到线段的距离。
func distToSegment(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	len2 := dx*dx + dy*dy
	if len2 == 0 {
		return dist(x, y, ax, ay)
	}
	// 把点投影到线段所在直线上，参数夹到 [0,1] 保证落在线段内。
	t := ((x-ax)*dx + (y-ay)*dy) / len2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return dist(x, y, ax+t*dx, ay+t*dy)
}

func dist(x, y, ax, ay float64) float64 {
	dx, dy := x-ax, y-ay
	return math.Sqrt(dx*dx + dy*dy)
}

// downsample 把图按 n×n 取平均缩小，得到抗锯齿后的结果。
func downsample(src *image.RGBA, n int) *image.RGBA {
	w := src.Bounds().Dx() / n
	h := src.Bounds().Dy() / n
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sr, sg, sb, sa uint32
			for dy := 0; dy < n; dy++ {
				for dx := 0; dx < n; dx++ {
					r, g, b, a := src.At(x*n+dx, y*n+dy).RGBA()
					sr += r >> 8
					sg += g >> 8
					sb += b >> 8
					sa += a >> 8
				}
			}
			cnt := uint32(n * n)
			dst.SetRGBA(x, y, color.RGBA{
				uint8(sr / cnt), uint8(sg / cnt), uint8(sb / cnt), uint8(sa / cnt),
			})
		}
	}
	return dst
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
