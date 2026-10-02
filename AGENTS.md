# Pier

本地多项目服务的启停工具：命令行 `pier` + 图形界面（macOS / Windows / Linux 各一份）。
Go 模块名 `github.com/zhengshangjinx/pier`。

## 常用命令

非交互执行时本机 shell profile 有问题，先显式给 PATH：

```
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
go vet ./... && go test ./... -count=1     # 校验
./build-app.sh                             # 清空 build/ 后打包 build/Pier.app
./package.sh [版本号]                       # 按平台各打一份，产物全在 dist/

# Linux 那两份在本机要带镜像（不带的话基础镜像与 apt 都从官方源走，会卡死；
# 三个变量只是「往哪儿取」，不带版本号那一个照旧）。apt 那个必须两架构同步，
# 只有南大满足（原文见下），别换回华为云：
PIER_APT_MIRROR=mirror.nju.edu.cn PIER_GO_DL=https://mirror.nju.edu.cn/golang \
  PIER_GOPROXY=https://goproxy.cn,direct ./package.sh 0.1.0
tools/shoot/shoot.py /tmp/a.png 1382 880 [弹窗] [参数] [light|dark]   # 演示数据截图（尺寸用初始窗口那个）
```

## 三个平台

同一个 `gui` 包编三次，webview 分别走 WKWebView / WebView2 / WebKitGTK。
打包在 `package.sh`：macOS 用本机的 Xcode 工具链（`build-app.sh`），
Windows 用 Homebrew 的 mingw-w64（`CC` 和 `CXX` 都要给，cgo 编 `.cc` 用的是 `CXX`），
Linux 走 `tools/pkg/linux` 的容器（Ubuntu 22.04，因为 webview 的 cgo 指令写死了
`webkit2gtk-4.0`，24.04 起只剩 4.1）。

那三处官方源在连不上的网络上能逐个换掉：`tools/pkg/linux/Dockerfile` 顶上四个 build-arg
（`BASE_IMAGE` / `APT_MIRROR` / `GO_DL_URL` / `GOPROXY`），默认全是官方地址，
`package.sh` 会把同名的 `PIER_*` 环境变量转过去。这台机器上必须给：Docker Hub 的拉取
在这里一挂十几分钟也下不来（注意容器里的网络是通的，`docker pull` 走的是 daemon 自己
那一条路，两者不是一回事），基础镜像只能一次性 `docker import` 一份 jammy 的 rootfs
再打上 `ubuntu:22.04` 的标签；apt 与 Go 压缩包都走 `mirror.nju.edu.cn`（arm64 的 rootfs
源里写的是 `ports.ubuntu.com`，这条也要一起换，路径是 `/ubuntu-ports`），模块走
`goproxy.cn`。apt 那个镜像**必须是两个架构同步的**，南大是、华为云不是：`linux-libc-dev`
是 `Multi-Arch: same`，两个架构版本不一致就互相 `Breaks`，而华为云的 ubuntu-ports 落后
一档，`libgtk-3-dev:amd64` 整条依赖当场就装不上（报的是 `libc6-dev:amd64 : Depends:
linux-libc-dev:amd64 but it is not installable`，看着像缺包，其实是版本对不齐）。
这几条都是实测出来的：同一条线路上 `mirrors.aliyun.com` 只有 200 KB/s 上下（一个 17 MB
的索引要 90 秒），huaweicloud 与南大都是 3–8 MB/s；另外这条线路对**大传输**不客气——
几十 KB 的索引没事，整包几百兆会中途把连接掐掉（apt 的 `Acquire::Retries` 只管单个文件，
管不了这种整体断连），所以 Dockerfile 里下载写成「先只下、下不齐隔几秒再来一遍」，
已取到的 `.deb` 留在缓存里，重来只补缺的。另外 apt 那一步必须排在 Go 之前——下载 Go 用的是 curl，而基础镜像里没有 curl。

- Linux 那份在本机是**交叉编译**出来的：arm64 用系统 gcc 原生编，amd64 用
  `gcc-x86-64-linux-gnu`，GTK / WebKit 各装一份 `:amd64` 的头文件与 .pc（落在
  `/usr/lib/x86_64-linux-gnu`、`/usr/include/x86_64-linux-gnu`，与本机那套各占各的）。
  只有 `libwebkit2gtk-4.0-dev` 装不了 amd64：同族的 `libgtk-3-dev`、
  `libjavascriptcoregtk-4.0-dev`、`libsoup2.4-dev` 都标了 `Multi-Arch: same`，唯独它没标
  还显式写了 `Conflicts`，apt 一见就判互斥（实为打包疏漏，文件其实分得开）。交叉编译缺的
  就是它那一个 `.pc` 与一根 `.so` 软链，所以 `apt-get download` 取 `.deb`、`dpkg-deb -x`
  直接解到 `/`，**不登记进 dpkg 数据库**——登记了的话，此后每次 apt 操作都要面对一个它认为
  装不上的包。

- **平台差异一律靠文件后缀（build tag）分家，共用文件里不写 `runtime.GOOS` 分支**：
  分了家才看得见谁是谁，`runtime.GOOS` 一旦长进共用代码就再也摘不干净。
  成对的有 `window_<平台>.go`、`picker_<平台>.go`、`load_{windows,other}.go`、
  `internal/proc/sys_{unix,windows}.go`、`internal/toolchain/roots_<平台>.go`、
  `internal/config/shell_{unix,windows}.go`、`internal/sysopen/open_<平台>.go`。
  改 Windows 那一侧不该让 mac / Linux 编出来的东西有任何变化（底线）。
- 共用文件里不许出现平台专有的东西（`/usr/bin/osascript`、「访达」、`syscall.Kill`）。
  页面上那两处平台差异是后端注入的：有没有原生拖拽条 `window.__PIER_NATIVE__`，
  文件管理器叫什么 `window.__PIER_FILEMGR__`（`gui/app.go` 的 `nativeScript`）。
- 可执行位在 Windows 上不能看 `os.Stat` 的模式位（那里普通文件一律 0666），
  走 `internal/execpath`：按后缀认，且**不读 `PATHEXT`**——它常被改坏，而这里要认的是
  清单里写死的那个文件。
- 进程名那两把旋钮只在 unix 上拧：Windows 上 `namingByArgv0` 为假，那一段整个不跑。
- `pier-gui.exe` 必须带 `-H=windowsgui`，否则双击先弹一个黑控制台，关掉它界面也没了。
  Windows 的剪贴板写在 `gui/clipboard_windows.go` 的 C 里：`GlobalLock` 返回的是
  地址，Go 里那句 uintptr → unsafe.Pointer 会被 `go vet` 的 unsafeptr 拦下（拦得对，
  只是那块内存不归 GC 管），挪进 C 就不必写一行靠人判断的豁免。
- Windows 上的「浏览…」等三个选择框走系统自带的 Windows PowerShell 5.1
  （路径写死到 `%SystemRoot%`，不走 PATH：pwsh 7 没有 `-STA`），
  Linux 上走 zenity / kdialog / qarma。
- **Windows 上界面要落盘再导航，不能走 `SetHtml`**：`SetHtml` 在 Windows 上落到 WebView2 的
  `NavigateToString`，微软文档写明只收 2MB 以下的 HTML，超出的部分直接丢掉且不返回错误——
  表现就是窗口打开一片白，不看文档根本看不出是页面太大。整个界面拼出来约 2.2MB
  （antd 一个包就 1.8MB），正好越线。`gui/load_windows.go` 把它写到 `cache/ui/index.html`
  再 `Navigate` 到 `file://`；WKWebView 的 `loadHTMLString` 与 WebKitGTK 的 `load_html`
  都没有这条限制，`gui/load_other.go` 保持原样。走 `file://` 不影响与后端的通道：绑定是
  `window.chrome.webview.postMessage`，不挑来源（原先的 `about:blank` 同样是不透明源）。
  `TestPageExceedsWebView2SetHtmlLimit` 钉着这个前提。
- **Windows 的图标只能靠 exe 自己的资源段**：`go build` 出来的 exe 没有 `.rsrc`，
  任务栏、资源管理器与标题栏只给一个默认图标。`package.sh` 用 `tools/mkicon` 画一份多档
  `.ico`、`windres` 编成 COFF 落到 `gui/rsrc_windows_amd64.syso`（`.syso` 必须和包同目录
  才认）再编，编完删掉。`mkicon` 的输出来看扩展名：`.png` 出 1024 那一档给 macOS/Linux，
  `.ico` 出 16~256 一整组给 Windows，两边画的是同一份 `render()`——改几何要一起对。
  别指望 `go build -overlay`：它能在目录列举里看见新加的 `.syso`，但交给链接器的还是原路径。

## 约定

- 界面在 `gui/app.js`（React + antd UMD + htm，无打包器）与 `gui/app.css`。
  `gui/app_test.go` 守着一批规则：色值只能写在 app.js 的 `PALETTE`（品牌标记另有白名单），
  不许 `size="small"` 之类、不许字号字面量，每个 Modal 都要挂 `BODY_SCROLL`，
  界面读的字段名必须在后端 json 标签里存在。
- 品牌标记有两处，几何必须一致：`gui/app.js` 的 `Mark` 与 `tools/mkicon`。
- 数据全部在 `~/.pier/`（`internal/config/store.go`）：`services.json`（服务与分组，目录一律
  绝对路径）、`settings.json`（主题等界面偏好，启动时经 `w.Init` 注入为 `window.__PIER_SETTINGS__`；
  另有 `sdks`「SDK 管理」里手动添加的目录、`sdkDefaults` 各语言的全局默认）、
  `state.json`（进程状态）、`logs/`、`cache/bin/`。`PIER_HOME` 可整体换位置（测试一律设它，
  绝不碰真实数据）。环境变量与启动命令在表单「高级设置」里可编辑。
- 数据文件不存在就建一个空的，不做任何旧版兼容（开发中的产品，不考虑兼容性）。
  命令行 `--config x.yaml` 可以只读地直接用一份 YAML，界面收起编辑入口；
  `EnsureStore(store, yamls)` 能把 YAML 导入成数据文件，目前只有测试用它铺数据。
- gui 测试用仓库里的 `gui/testdata/pier.yaml`，不依赖工作空间里的真实清单。
- 窗口铺满标题栏（全尺寸内容视图），顶部 28px 是原生拖拽条（`gui/window_darwin.go`），
  页面在真实窗口里加 `.dc-native`，这一条里不放可点的东西。初始尺寸不写死一个数：
  `defaultWindowSize` 按主屏可用区域取比例再收进上下限（写死的尺寸小屏顶满、大屏局促），
  摆位由 `pierCenterWindow` 在铺满窗口之后重做一次——webview 自己在 SetSize 里的那次
  center 是在铺满之前算的，摆出来偏上。改窗口尺寸一律走 `resize`：
  webview 的 `SetSize` 是用一份写死的掩码调 `setStyleMask:`，会把「全尺寸内容视图」那一位
  整个换掉，内容视图随即退回标题栏下面——弹窗遮罩盖不到顶上那 28px，窗口就多出一条亮条；
  而换掩码时 AppKit 保的是内容区大小、会把外框缩掉一条，所以补样式时要按原样把外框定回去。
  拖拽条铺到窗口最顶之后会盖住红黄绿那一排，实测三个按钮仍然先拿到点击（`_NSThemeCloseWidget`），
  其余位置才是拖拽条。
- 工具链三层：`toolchain/discover.go` 扫本机有哪些 SDK，`project.go` 读项目自己声明的版本，
  `Resolver` 在其中挑一个。选择顺序是「服务上指定 → 项目声明 → 全局默认 → 本机兜底」，
  每一步都写进 `Tool.Reason`（透明，界面与 `pier doctor` 共用 `proc.ToolInfo`）；
  项目有要求而本机没有满足的版本时，照样选一个能用的，同时在 `Tool.Warn` 里说清楚。
  Java 的主版本要求从 pom 读（`maven.compiler.release` / `java.version` /
  `maven.compiler.target|source`，支持一层 `${}`，见 `config.Service.JavaMajor`）。
- 扫盘要按「界面从访达启动，PATH 上只有 /usr/bin:/bin」来写：别指望 `lookPath`，
  每个语言都要直接扫安装位置，而且 `Homebrew` 的位置一律带 `*`（`go@1.24`、`python@3.14`
  这类带版本的 formula 和无名的一样常见，只写 `opt/go` 会让装它的人看到一个空分组）。
  Go 还要扫 `~/go/pkg/mod/golang.org/toolchain@*`：go.mod 要更高版本时 go 命令会把整套
  SDK 下载到那里，那是真能跑的 Go。
- 「浏览…」手动指定时选到哪一级都认（根目录、Homebrew 的 libexec、bin 目录、bin 里那个
  可执行文件），见 `discover.go` 的 `rootsOf`；选到可执行文件要先解开软链，否则
  `/opt/homebrew/bin/go` 的上两级会解出一个没有版本号的假 GOROOT。去重比的是可执行文件的
  真实路径而不是 `Home`：Homebrew 的 `bin/python3` 一路链进框架，两条链的 `Home` 并不相同。
- 界面上的每套 SDK 都显示 `Bin` 而不是 `Home`：各类别的 `Home` 含义不一致（java/maven/go 是
  安装根目录，node/python 是要前置到 PATH 的 bin 目录），`Bin` 才是这次真正会跑的那个文件。
- 「SDK 管理」页（`gui/app.js` 的 `SDKPage` + `internal/manage/sdk.go`）列各类别扫到的 SDK，
  可手动添加（`SDKAdd` 会真的校验一次再落盘，路径取校验后的规范值）、可设各类别的全局默认。
  表单里每个类别一个下拉加「浏览…」，下面是 `manage.Toolchain` 实时算出的「将使用 X，依据 Y」——
  走的是与真正启动同一个解析器，另写一份简化版迟早会对不上。
- 侧栏只分两节：「服务」（全部服务、各个分组、新建分组，平铺）与「设置」（SDK 管理、日志）。分组前面那颗点
  是灰/绿/红三态，形状本身就说明它是分组，不给它单开一个标题；新页面归进这两节之一，别再加出第三种
  层级——标题、孤零零的一项、没标题的一项摞在一起，读的人得先猜这里有几层。分组也别再试缩进了
  （试过、撤了）：232 的字宽里让出 24px 之后名字列只剩 100px，悬停时「…」一出来（它要在名字右边占
  28px，数量不许挪，见 `navLabel`）就当场地省略成「demo-adm…」；而且一缩进，分组看着像是「全部服务」
  展开出来的子节点，可它不是——它是另一条看服务的口径。
- 「现在看的是哪份数据」是顶栏「⋯」里的**第一项**，不是侧栏底部的一行（挪过一次，别挪回去）。
  它和下面那三项——复制成 YAML、导出清单…、打开清单…——说的是同一件事：手上这份清单在哪、
  怎么把它带走、怎么换一份。分开摆的时候，得先在侧栏找到那行、再回顶栏点「⋯」才能把「换一份」做完。
  点这一项就是「打开数据目录」（只读时是「在访达中显示清单」），按来源换图标与说法。
  代价是清楚的：SDK 管理、日志那两页上没有「⋯」，那两页就看不到自己在看哪份数据了。认了——
  那两页本来也不跟清单里写了什么打交道（一个说本机装了哪些 SDK，一个按服务名读日志目录）。
  侧栏底部只剩主题那一行（`dc-side-foot`）：纯界面偏好，不跟着数据走，收尾的地方留给它正合适。
  别再给页脚加回带底带框的卡片——灰侧栏里最亮的就是它，比上面真要点的导航还抢眼。
- `gui/app.js` 的 `KIND_TOOLS`（服务类型 → 要用哪几套 SDK）是 `internal/config` 里各个
  `planXxx` 的第二份，靠 `gui/app_test.go` 的 `TestFormToolKindsMatchPlan` 守着，改一处要改两处。
  不含 pnpm：它跟着选中的 node 走，不是独立安装的一套。
- 日志区渲染 ANSI 颜色（`gui/app.js` 的 `ansiSpans`），终端配色在 `PALETTE.ansi`。
- 日志一个服务一个目录、一天一份：`logs/<服务名>/<日期>.log`（`config.LogDirFor` / `LogPathOn`；
  `LogDateLayout` 的字典序就是时间序，`LatestLog` 与过期判定直接比字符串）。分天的边界是零点而
  不是启动时刻，但**跨零点的服务不改文件名**——写的那一端在启动时就定死这一份（`proc.LogFile`），
  否则一次跨午夜的运行会被劈成两份，而日志是按「一次运行」读的。文件是**追加**不是清空：「日志里
  只有这次的输出」以前靠每次启动截断免费得到，改成追加之后这个保证由 `LogStartMarker` 那行启动
  标记 + `TrimToLastRun` 提供，写的一端和读的一端必须对得上（`TestStartAppendsToTodaysLog` 钉着）。
  保留 14 天，启动时顺手 `PruneLogs` 清掉自己超期的那些；过期判定比的是日期字符串，不是
  `time.Parse`（后者把本地日期当 UTC 解析，东八区会差一天），也不是完整时间戳（那样「满了 14 天」
  的时刻会跟着当前钟点漂）。
  **轮转只能靠「启动时选文件名」**：日志 fd 由 setsid 出去的独立进程继承（见上一条），Pier 这边
  没有一个还能往里写的句柄，进程内的 rotator 无从谈起。
- 「设置 · 日志」页（`gui/app.js` 的 `LogPage` + `panel.LogUsage`）列每个服务占了多少、几份、
  日期区间，可「清理超期」「清空」（单服务 / 全部）、可开目录、可直接看最新那份。大小由后端
  `view.Bytes` 格式化好（`LogUsageOut.Size`）：命令行 `pier logs --size` 与界面必须是同一个字符串，
  各自格式化迟早一个「23.6 MB」一个「24 MB」。
- Java 编译步骤（`mvn -pl <模块> -am install`）固定带 `-DskipDocker=true -Ddocker.skip=true
  -Ddockerfile.skip=true -Djib.skip=true`：本地起服务不打镜像（`config/derive.go` 的 `mavenSkipPackaging`）。
- 「停止」在任何阶段都可用（`internal/panel` 的 `Stop`）：排队中直接撤销；编译 / 拉起中取消
  `StartContext`，编译进程组整组结束；等待就绪中取消健康等待并停进程。被打断的启动流程事后回写
  状态会被忽略（`setPhase` / `finish` / `fail` 都先核对 op 还是不是当前那个）。
- 健康探针是**就绪信号，不是成败判据**：不是每个服务都有健康接口，地址也大半是推导出来的。
  等满 `proc.HealthWait`（180s）还没探通就置 `Status.ProbeExpired`，`view.StateKey` 随之退回
  「运行中」——服务在好好跑着，一直挂在「启动中」只会让人以为再等等就好；`panel.startOne` 这时
  走 `finish()` 而不是 `fail()`。界面在说明位用警示色说一句「健康探针未通过…」，「⋯」里多一项
  「不再检查健康」（`manage.ClearHealth`，只清地址、不动服务），这是这个状态唯一的收场方式。
  但 `needsAttention` 里**不含** probeExpired：那一格叫「异常」、副标题是「异常退出或端口冲突」，
  算进去会让一个词有两个意思，还会把正在跑的服务从「N / M 在跑」里踢出去。还在窗口内等着的那些
  只说一句「等待健康检查」——后端说明位那句「尚未通过健康探针」是同一件事的另一种说法，
  并排摆着读起来像卡了两下（后端的说法留着，命令行表格没有别的列能补这句）。
- 服务是独立进程，不内嵌在 Pier 里（`internal/proc/supervisor.go` 的 `StartContext`）：`Setsid`
  起在新会话，`Process.Release()` 之后不 Wait，日志 fd 由子进程继承——退出 Pier 不带走任何在跑的
  服务，日志也照写。**没有 stop-on-quit，别加**：停止只挂在「停止 / 全部停止 / pier down」上。
  PID/PGID 记在 `state.json`，重开的 Pier 靠它重新认领（`ProcessAlive && SameGroup`），资源占用
  也按这个进程组求和。唯一的空档是编译阶段：那时退出 Pier，编译进程成孤儿继续跑完，而服务不会被
  拉起、`state.json` 里也不留记录。
- 进程名有两把**互相独立**的旋钮，改哪把要看是谁在读：`argv[0]` 决定 `ps` 的 COMMAND 列、
  `pgrep`、`pkill -x`、`killall`；被 exec 的**那个文件自己的文件名**决定活动监视器、`top`、
  `ps -o ucomm`、`lsof` 的 `c` 字段（界面「被 … 占用」就是它）。`argv[0]` 那一把人人都有
  （`resolveRunExec` → `namedArgv`），文件名那一把**只有 node 拿得到**（`isNode`）：
  - **必须展开 shebang**，否则中间人会盖掉 argv[0]——`pnpm`/`npm`/`yarn` 都是
    `#!/usr/bin/env node`，env 用 `argv[0]=node` 再 exec 一次，设了白设。展开成
    「node 路径 + 脚本路径」绕开它，再把 node 硬链成 `<BinDir>/<服务名>`。
  - 硬链而不是拷贝：拷贝换了 inode，代码签名当场失效，进程起来就被 SIGKILL（实测 137）。
  - **只有 node 能硬链**，因为「挪个位置照跑」是特例不是常态，都实测过：java 挪出 JDK 就死在
    `dyld: Library not loaded: @rpath/libjli.dylib`（rpath 写死 `@executable_path/../lib`，
    只有放进 JDK 自己的 bin 才加载得起来，那等于往用户的 SDK 目录里写文件）；python 挪了就认
    不出 venv（CPython 靠可执行文件旁边的 `pyvenv.cfg`，硬链之后 `sys.prefix` 退回 base_prefix，
    venv 里的依赖一个都 import 不到）；`go` 挪了找不到 GOROOT（"binary is trimmed and GOROOT is
    not set"）；`/bin/sh` 在只读的系统卷上根本链不动。其余一律只改 argv[0]，别为一颗旋钮去冒
    「换个位置就起不来」的风险。java 还有一层：`mvn` 脚本末尾 `exec "$JAVACMD"` 把进程换成
    java，`spring-boot:run` 又再 fork 一个 JVM，名字由插件决定，本来也留不住。
  - 顺带的好处：改 argv[0] 对 python 是安全的（实测 `sys.executable` / `sys.prefix` 取自
    `_NSGetExecutablePath`，与 argv[0] 无关），所以 venv 照旧认得出来。
  - 只改**执行用的那份 argv**，`plan.Run` 原样不动：日志里那句「运行：」和界面上的「启动方式」
    要的是人能读的命令，真正 exec 的是什么另写一行「实际执行：」。
  - node 服务里被 pnpm fork 出来的子进程（vite、esbuild）仍然叫 node/esbuild，那是它自己的
    脚本起的，拦不住；顶层那个进程两把旋钮都是服务名。
  - 副作用：node 换了 exec 路径后 `process.execPath` 是 `~/.pier/cache/bin/<服务名>`，
    npm/pnpm 据此推的全局前缀会指偏（只影响运行时 `-g`，本地依赖解析照旧）。
  - 名字长度不必迁就：实测 24 字符的英文名与中文名在 `ps -o comm`、`pgrep -x`、`killall` 那里
    都能整名命中，没有 16 字符那道坎（MAXCOMLEN 在现代 macOS 上不挡这里）。但认领一律按
    PID/PGID，绝不按名字——服务叫什么、系统里显示成什么叫，都不影响「这是不是 Pier 起的那个」。
- 「被谁占着端口」一律按**进程组**认领（`proc.ManagedName`：`PID` 相等，或首领还活着
  （`ProcessAlive`）且 `SameGroup`）。不能只比 PID：端口常握在子进程手里，记录在案的 PID 只是
  那个壳，「停止」能回收整组靠的也是这一条。首领死了就不再按组号认——PGID 可能已被系统分给
  别人。`manage.managedName` 直接委托给它，不写第二份：两份判定迟早对不上，界面就会在「是自家
  服务」时给一个「结束进程」，那会在状态文件里留下一条指向已死进程的假记录。
- 资源占用只算 Pier 自己和它起的服务，不采整机（`internal/proc/metrics.go` 按进程组求和，没有
  Total/CPUs）。状态里的 `usage` 是全部服务相加、`self` 是 Pier 自身（不含系统托管的 WebView
  渲染进程），界面上的数字一律来自后端，不在界面里做推算。概览那排格子和下面那条「服务占用对比」
  都跟着当前这一页走（全部服务 / 某个分组）。
- 「服务占用对比」（`gui/app.js` 的 `UsageChart`）内存和 CPU 各一列并排，不做成二选一切换：
  两个指标的名次本来就不同（内存最大的往往不是 CPU 最高的），切一次就只能看见一半，而切过去的
  那半还常常是全 0，看着像功能没做。两列各按本列的最大值为满格，都是排行榜不是百分比条——满格
  只说「这个最大」，两列之间也不能互相量。行的顺序固定按内存排，不跟着 CPU 变：CPU 是每两秒刷
  一次的瞬时值，拿它排序名单会跟着刷新跳。
  列间那两条 1px 竖线（名字 | 内存 | CPU）画在每个格子自己的 `border-left` 上，不是容器上：
  行与行之间**不能留 gap**——留了缝，线就断成一段段虚线，而上面那排格子之间的线是通到底的，
  两块并排摆着一眼能看出不是一回事。行距改由格子上下 4px 的内边距给，总高和原来的
  「22 + 8」一样。列宽只在 `.dc-chart-rows` 上写一份，每行用 `grid-template-columns: subgrid`
  继承；每行各写一份列宽迟早会错开一格，而竖线错开一格比没有竖线更难看。
