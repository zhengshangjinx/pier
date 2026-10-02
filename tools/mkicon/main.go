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
//
// 输出格式看扩展名：.png 出 1024 一档（macOS 的 .icns 与 Linux 的图标都由它缩），
// .ico 出 Windows 要的一整组尺寸。两边画的是同一份 render()。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
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
	var err error
	if strings.EqualFold(filepath.Ext(out), ".ico") {
		err = writeICO(out, render())
	} else {
		err = writePNG(out, render())
	}
	if err != nil {
		fail(err)
	}
	fmt.Println("图标已生成：" + out)
}

func writePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// icoSizes 是 ICO 里装的几档。
//
// 16 与 32 是任务栏和标题栏，48 是资源管理器的中图标，256 是大图标视图。
// 256 那一档不用 PNG 压：见 dibEntry 的说明。
var icoSizes = []int{16, 24, 32, 48, 64, 128, 256}

// writeICO 把 render() 的结果写成一个多档 ICO。
//
// 与 PNG 那条路分开写、也不共用 downsample：见 resample 的说明。
func writeICO(path string, src *image.RGBA) error {
	entries := make([][]byte, len(icoSizes))
	for i, size := range icoSizes {
		entries[i] = dibEntry(resample(src, size))
	}

	var buf bytes.Buffer
	// ICONDIR：保留位、类型（1 是图标不是光标）、档数。
	buf.Write([]byte{0, 0, 1, 0})
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(entries)))

	// 目录在前、图像在后，所以每档的偏移要把整个目录先算进去。
	offset := 6 + 16*len(entries)
	for i, e := range entries {
		size := icoSizes[i]
		// 宽高各一个字节，256 在这里写 0——这个字段是 uint8，256 装不下，
		// 约定用 0 表示，不是漏填。
		buf.WriteByte(byte(size % 256))
		buf.WriteByte(byte(size % 256))
		buf.WriteByte(0) // 调色板颜色数，真彩色图标恒为 0
		buf.WriteByte(0) // 保留位
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(e)))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(e)
	}
	for _, e := range entries {
		buf.Write(e)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// dibEntry 把一张图编成 ICO 里的一档，格式是 BITMAPINFOHEADER + 像素 + 掩码。
//
// 不用 PNG 压：PNG 装在 ICO 里要 Vista 之后才认，BMP 从 Windows 95 起就是这个格式。
// 这里画的是图标不是照片，多出来的几十 KB 不值得拿兼容性去换。
func dibEntry(img *image.RGBA) []byte {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	// 1bpp 掩码，每行按 4 字节对齐。32 位图标其实用不到它（透明靠 alpha 通道），
	// 但这个字段在格式里是必须有的，全填 0 表示「没有额外的镂空」。
	maskRow := (w + 31) / 32 * 4

	var buf bytes.Buffer
	// 字段一个一个按各自的宽度写，不合并成 uint32：中间那几个是 WORD，
	// 合并之后两个 16 位字段谁在高位就得靠心算，写反了是一份看着没问题的坏图标。
	put := func(v ...any) {
		for _, x := range v {
			_ = binary.Write(&buf, binary.LittleEndian, x)
		}
	}
	put(
		uint32(40), // biSize
		uint32(w),  // biWidth
		// 高度要写成两倍——图像之后还跟着掩码，格式把这个总和当成高度。
		uint32(h*2),
		uint16(1),            // biPlanes
		uint16(32),           // biBitCount
		uint32(0),            // biCompression = BI_RGB
		uint32(w*h*4),        // biSizeImage
		uint32(0), uint32(0), // 分辨率
		uint32(0), uint32(0), // 调色板
	)

	// 像素自下而上、每像素 BGRA。
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	for i := 0; i < maskRow*h; i++ {
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

// resample 把图按区域平均缩到 size×size。
//
// 不能沿用 downsample：那是按整数倍降的，而 ICO 要的 24 与 48 都不是 1024 的整除数，
// 取整会让这两档偏掉一两个像素。区域平均对任意比例都成立。
//
// 颜色按 alpha 加权再还原（RGBA() 给的是已乘 alpha 的值）：直接对未加权的值取平均，
// 圆角外侧那些全透明的像素（颜色是 0）会把边缘往黑里拉，缩到 16 那一档就是一圈黑边。
func resample(src *image.RGBA, size int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		y0, y1 := y*sh/size, (y+1)*sh/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < size; x++ {
			x0, x1 := x*sw/size, (x+1)*sw/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sr, sg, sb, sa, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, b, a := src.At(xx, yy).RGBA()
					sr, sg, sb, sa = sr+uint64(r>>8), sg+uint64(g>>8), sb+uint64(b>>8), sa+uint64(a>>8)
					n++
				}
			}
			// 把 alpha 加权平均再还原成直通值；整块全透明时颜色无从谈起，留 0。
			var out color.RGBA
			if sa > 0 {
				out = color.RGBA{
					R: uint8(sr * 0xFF / sa),
					G: uint8(sg * 0xFF / sa),
					B: uint8(sb * 0xFF / sa),
					A: uint8(sa / n),
				}
			}
			dst.SetRGBA(x, y, out)
		}
	}
	return dst
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
