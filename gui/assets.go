package main

import (
	_ "embed"
	"strings"
)

// 界面用的第三方库，全部随二进制一起打包。
//
// 为什么内置而不是引 CDN：这个工具的整个卖点就是「不需要环境、双击就能用」，
// 引 CDN 等于把「能不能打开界面」押在本机网络上——断网、公司代理、
// CDN 被墙，任何一种都会让窗口打开后一片空白，而用户完全无从判断原因。
//
// 用的是 UMD 构建，因此不需要 npm、不需要打包器，构建链仍然只有 Go + Xcode。
// 代价是体积：整个界面约 2MB，对一个本地工具来说换来的确定性是划算的。
//
//go:embed assets/react.min.js
var jsReact string

//go:embed assets/react-dom.min.js
var jsReactDOM string

//go:embed assets/dayjs.min.js
var jsDayjs string

//go:embed assets/htm.min.js
var jsHtm string

//go:embed assets/antd.min.js
var jsAntd string

//go:embed assets/reset.css
var cssReset string

//go:embed app.css
var cssApp string

//go:embed ui.html
var uiTemplate string

//go:embed app.js
var appJS string

// styleMarker 与 scriptMarker 是 ui.html 里等着被替换成资源的两处位置。
//
// 分成两处而不是一处，是被一个真实的故障逼出来的：原先只在 <head> 里放一个标记，
// 五个包全内联在那儿，于是 app.js 在 <div id="root"> 还没被解析出来时就执行了，
// React 抛 #299「Target container is not a DOM element」，界面一片空白。
// 样式属于 head，脚本属于 body 末尾，各自待在各自该在的地方，这类问题就不会再有。
const (
	styleMarker  = "<!-- @@STYLES@@ -->"
	scriptMarker = "<!-- @@SCRIPTS@@ -->"
)

// buildHTML 把模板、第三方库和应用代码拼成一整份 HTML。
//
// 之所以拼成一份而不是让页面去加载文件：webview 是用 SetHtml 直接喂字符串的，
// 背后没有文件服务器，页面里的相对路径没有东西可以解析。
//
// 安全性说明：第三方库里若出现字面量 "</script"，内联进 <script> 块会把
// HTML 从这里截断。当前的几个包都不含这个序列，由 TestAssetsInlineSafely 守着。
func buildHTML() string {
	var styles, scripts strings.Builder
	styles.Grow(len(cssReset) + len(cssApp) + 64)
	scripts.Grow(len(appJS) + len(jsAntd) + len(jsReact) + len(jsReactDOM) + 4096)

	// 自己的样式排在 antd 的 reset 之后：reset 会重置 html/body 的外边距，
	// 反过来的话布局样式会被它盖掉。
	styles.WriteString("<style>\n" + cssReset + "\n" + cssApp + "\n</style>\n")

	// 顺序不能动：htm 要在 React 之后（它要绑 createElement），
	// antd 要在 React 与 dayjs 之后（UMD 包直接读全局变量）。
	for _, js := range []string{jsReact, jsReactDOM, jsDayjs, jsHtm, jsAntd, appJS} {
		scripts.WriteString("<script>\n" + js + "\n</script>\n")
	}

	out := strings.Replace(uiTemplate, styleMarker, styles.String(), 1)
	return strings.Replace(out, scriptMarker, scripts.String(), 1)
}
