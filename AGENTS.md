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
tools/smoke-update.sh [cli|bundle]         # 临时目录里造一份假安装，把 pier update 走一遍
```

## 三个平台

同一个 `gui` 包编三次，webview 分别走 WKWebView / WebView2 / WebKitGTK。
打包在 `package.sh`：macOS 用本机的 Xcode 工具链（`build-app.sh`），
Windows 用 Homebrew 的 mingw-w64（`CC` 和 `CXX` 都要给，cgo 编 `.cc` 用的是 `CXX`），
Linux 走 `tools/pkg/linux` 的容器（Ubuntu 22.04，因为 webview 的 cgo 指令写死了
`webkit2gtk-4.0`，24.04 起只剩 4.1）。

**三个平台的产物是同一个形状：下载一份、装一次、得到一个图标加一个命令。**
界面二进制一律叫 `pier-gui`、命令行一律叫 `pier`（Windows 上加 `.exe`）。Windows 那份
曾经在压缩包根上并排摆着 `pier-gui.exe` 与 `pier.exe`，两个都写着「应用程序」，光看名字
分不出该点哪个——收成一份就是为这个。`pier-gui.exe` **不改名**：`gui/app.go` 里那句
「pier-gui 启动：…」是三平台共用的，为 Windows 改文件名就得动共用代码，正好踩上面那条
底线；三个平台同名本身也更好认。

- macOS：`build-app.sh` 把两份都编进 `Pier.app/Contents/MacOS/`。**必须排在 `codesign`
  之前**——codesign 签的是整个 bundle，签完再往里塞文件签名当场失效（`--verify` 报
  unsealed contents）。`package.sh` 里那份只有命令行的 `.tar.gz` 直接从
  `$APP/Contents/MacOS/pier` 取，**不另编一遍**：dmg、zip、tar.gz 三个产物里是同一个
  文件，两处各写一遍编译参数迟早一处带 `-s -w` 一处不带。取出来那份带着 ad-hoc 签名，
  不影响单独运行。用户侧就一句
  `ln -s /Applications/Pier.app/Contents/MacOS/pier /usr/local/bin/pier`。
- Windows：`pier.exe` 落在 `bin\` 里，压缩包根上只留 `pier-gui.exe` + `install.cmd`
  + `README.txt`。安装脚本在 `tools/pkg/windows/install.{cmd,ps1}`：`.cmd` 是双击入口，
  `.ps1` 干活。**`.ps1` 必须存成带 BOM 的 UTF-8**——Windows PowerShell 5.1 对没有 BOM
  的脚本按系统代码页（简体中文上是 GBK）解码，里面的中文会变成乱码；**`.cmd` 反过来
  绝不能有 BOM**（cmd.exe 把 BOM 那三个字节当成一条命令去执行），也不要在里面 echo 中文
  或加 `chcp 65001`（前者跟着控制台代码页走，后者会改变 cmd 接着读这个文件的解码方式）。
  `rem` 注释里的中文倒是安全：cmd 把整行吞掉，UTF-8 的多字节序列里也不会出现 `&`、`|`、
  `>` 这些能改变解析的字符。但**注释里不能出现 `%`**——`rem` 行照样做变量展开，
  `%~dp0` 那种会被当成命令执行并报致命错误（SS64 上写明的一条）。
- Windows 改 PATH 有三条不能走的路：**不能用 `setx`**（超过 1024 字符的值被静默截断）；
  **不能用 `[Environment]::SetEnvironmentVariable(..., "User")`**（写回去一律是 REG_SZ，
  而 Windows 自带的用户 PATH 里本来就有 REG_EXPAND_SZ 的项——`%USERPROFILE%\AppData\
  Local\Microsoft\WindowsApps` 就是一条——一改就变成不展开的字面量，Store 装的 python、
  `winget` 那些别名会跟着从 PATH 上掉下去）；**绝不能碰系统级 PATH**（那要管理员，
  也会影响别的账户）。所以 `install.ps1` 直接读写 `HKCU\Environment`：读的时候给
  `RegistryValueOptions::DoNotExpandEnvironmentNames`，写的时候沿用
  `GetValueKind` 拿到的原类型。改完还要广播一次 `WM_SETTINGCHANGE`，否则资源管理器
  （以及从它启动的终端）不重读环境变量，用户新开的窗口里照样找不到 `pier`，
  得注销一次——而「装完就能用」正是这个脚本存在的全部意义。
- Linux：`tools/pkg/linux/install.sh` 装到 `~/.local`，本来就是这个形状，没动过。

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

## 更新与版本

- **版本号靠注入，不靠猜**：`internal/version.Version` 由打包脚本用 `-X` 钉进去，
  注入点一共 8 处 `go build`（`build-app.sh` 那份占 4 处，含 universal 的两份中间产物），
  Linux 的版本号还要经 `PIER_VERSION` 传进容器。**必须同时钉第二个变量
  `internal/version.released=1`**：允许原地替换自己的只有「从 Releases 下载来的产物」这一类，
  而从 go 1.24 起，在打了 tag 的提交上直接 `go build`，`debug.ReadBuildInfo().Main.Version`
  里也有真版本号（工作区脏时还带 `+dirty`）——光看版本号分不出打包产物与自编的，
  而且 `BuildInfo.Settings` 里**没有** `-ldflags` 这一项，反查不出来，记号只能由脚本钉。
  `build-app.sh` 只在 `PIER_VERSION` 给过时才钉：本机调试直接跑它，编出来的就是 dev。
- **`PIER_UPDATE_BASE_URL` 只给测试用**（`internal/update` 的 `baseURLEnv`）：它把 API 基址
  顶到一个本地假 release 上，`tools/smoke-update.sh` 与手工验收走这条路——不发一版就试不了
  整条更新链路。正常使用不要设它，设了就等于把「更新从哪儿来」交给那个地址。
- **换文件的是助手，不是 Pier 自己**：隐藏动词 `_update-apply`（不进 `usageText`，
  `gui/main.go` 里也赶在碰 webview 之前拦一道）。它由**当前这一份二进制**复制到
  `cache/update/<版本>/helper/` 之后重新执行——动手的永远是那份已经在用户机器上跑通的旧代码。
  它等 Pier 退出（等的是 `gui.lock` 被放开）、按平台换文件、把结果写进 `result.json`、
  再按 `Plan.Relaunch` 决定要不要拉回来。**它只能是 Go，不能是运行时生成的
  shell / PowerShell 脚本**：换文件、回滚、读回结果每一步都要判错，写在 Go 里能单测；
  Windows 上运行时生成的 `.ps1` 没法保证「带 BOM 的 UTF-8」（见上文），中文会变成乱码。
- 平台差异照旧只落在 `apply_<平台>.go`：`apply_darwin.go` / `apply_windows.go` /
  `apply_linux.go` / `apply_other.go`，另有 `asset_<平台>.go` 认「该下哪份产物」。
  共用文件里不出现 `runtime.GOOS`，也不出现 `/Applications`、`ditto`、`powershell` 这些字眼；
  `apply_unix.go` 只放两个 unix 平台共有的一句（跑着也能 `rename` 覆盖自己）。
- **有界面在跑时命令行拒绝替换**（`update.GUIRunning()` 看 `gui.lock`）：Windows 上换不了
  正在运行的 exe；unix 上换得动，但会把一个跑着旧代码、指着一份新安装的界面留在那儿，
  症状比拦住更难解释。
- 界面上的轮询只有 `App` 里那一处（空闲 60 秒、忙时 1 秒），侧栏页脚与偏好设置页读的是
  同一份 `update.StatusOut`。别在两处各起一个定时器：一前一后对不上，圆点会和页面里的
  说法打架。`up.result` 要在界面里单独接一个变量（`res`）再读：`gui/app_test.go` 的字段
  白名单只认顶层 json 标签，`up.result.version` 会被拿去和 `StatusOut` 对，对不上。
- **发布说明整篇带出来、在弹窗里按 markdown 渲染**：`StatusOut.Notes` 是原文，只削首尾空行，
  不在后端截断（以前截 800 字，界面上摆着的是一段「## 升级方式」这样的原文，读不成句）。
  渲染器是 `gui/app.js` 里手写的 `mdBlocks` / `mdInline`，与 `ansiSpans` 同一个理由：
  这个界面没有打包器、运行时不连 CDN，为一段说明引一个 markdown 库要连带维护
  `THIRD_PARTY_NOTICES`。**出来的是 React 元素，整条路上没有一处 `innerHTML`**；
  认不出的语法当普通段落原样显示——多几个看得见的符号，好过把一句话渲染成别的意思。
  正文里的链接只留文字不做成可点的：界面跑在 webview 里，点一个真链接会把界面本身
  导航走，要看原文走弹窗底下那颗「在浏览器中打开」（它仍然只开自己认的那一版，
  见 `NotesURL`）。演示模式下 `__pierDemo.open("notes")` 直接开这一屏，排版靠截图核对。
- **发版说明里要提一句**：从 0.2.0（更新器之前那一版）升上来的人得手动装一次，
  之后不再有这个断层。

## 环境变量

- **六层叠加，顺序只有一处**（`proc.buildEnv`）：继承来的环境 → 工具链（`JAVA_HOME`、`PATH`）
  → 服务目录里的 `.env` → `PORT` → 清单顶层 `env` → 服务自己的 `env`，后面的压前面的。
  中间三层的位置各有理由：`.env` 排在工具链之后，是为了让里面一个手写的 `JAVA_HOME`
  盖不掉「SDK 管理」里说好了要用的那一份（界面上写着 A、跑起来是 B 是最难查的一类）；
  它又排在清单之前，因为清单是 Pier 自己的配置，用户在那儿写下的就是他要的。
  顶层那组是共享段（`config.Config.Env`），服务自己的同名变量覆盖它，改一处所有服务都变。
- **只读 `.env` 这一份**，不猜 `.env.local` / `.env.development` 该取哪个：那几个后缀是
  各个框架自成一套的地方（Vite 与 Next 的优先级正好相反），替用户决定只会在两边打架时
  更难解释。语义照 dotenv 的通行做法：**只补缺，不覆盖进程环境里已经有的**、
  值两端成对的引号去掉一层、`#` 起注释但**只认整行**——值里行尾的 `#` 留着
  （密码里带 `#` 是常事，切掉一次就再也登不上，连报错都没有）。同名键以最后一条为准。
  行首的 UTF-8 BOM 必须去掉：Windows 上记事本存的文件常带着它，不去掉的话第一个键名
  前面多出三个字节，那个变量永远注入不进去，而文件看上去完全正常。
  读不动就拦住启动——那份文件没生效，服务起来是另一副样子，而现场看上去一切正常；
  报错要指到哪份文件的哪一行。
- **`${}` 只在写了才展开**（`internal/config/expand.go`）：没写 `$` 的值一个字节都不改
  ——清单里的值常常就是给子进程看的 shell 片段，顺手把 `$HOME` 展开了会把一件看起来
  对的事情搞坏。取值的那一侧是「已经算好的环境」（含共享段、`.env`、工具链变量），
  `${PORT}` 取这个服务的端口。名字必须是 `[A-Za-z_][A-Za-z0-9_]*`（`ValidEnvName`，
  `.env` 的键名校验用的是同一个函数）。**展开失败报错，不留空**：留一个空值，服务会
  带着一个看着正常、其实是空串的配置连上去。`$${X}` 是写出字面的 `${X}` 的办法
  （转义要有，否则一报错就是一条死路）；`$$` 与 `$NAME` 原样留着。
- **一层里解到不动点**（`ResolveEnvLayer`），不按 map 顺序解一遍：map 的遍历顺序是随机的，
  而同一层里的变量可以互相引用（`A=${B}`、`B=${C}`）。解不出来的（绕成环、或引用了外头
  也没有的名字）整层都不返回——半份结果看着像成功了，某个值里却静静地躺着一个 `${X}`，
  等子进程去撞。
- **清单写了 `port` 就注入 `PORT`**：`${PORT}` 与子进程自己读到的 `PORT` 是同一个值。
  没写端口就不动它——继承来的那个可能是用户自己导出的，也可能是别人的工具在用的。
- **日志里只说变量名，不说值**：`.env` 里装的常常是密钥。被跳过的那几个（环境里已经有、
  所以没覆盖）也要列出来，否则「我改了 .env 却没生效」只能靠猜。
- 界面那块在「偏好设置 · 数据」里（`SharedEnv` 走 `panel.StateOut`），**整份替换**而不是
  逐条增删：它就是一整块多行文本，逐条合并会让「删掉一行」与「他没写这一行」没法区分。
  只读清单时整块收起（连读都不读）。业务层是 `manage.SaveSharedEnv`，名字当场校验。

## 出事与通知

- **诊断只有一份**：`internal/diag` 是一张纯文本规则表，扫日志尾部（只扫最后一次运行，
  见 `TrimToLastRun`），命中就给「一句原因 + 一句下一步 + 原文那一行」。规则按「越具体越靠前」
  排，`BUILD FAILURE` 垫底当兜底——它是 Maven 收尾时一定会打的那句，摆在前面会把上面
  更具体的那几条全吃掉。两个消费方：界面挂在错误行下面（`ServiceOut.Diag`，
  `gui/app_test.go` 的字段白名单要单列一个变量名 `dg`，写成 `s.diag.reason` 对不上），
  命令行 `pier up` / `pier status` 一起打印。`Status.Stale`（进程没了记录还在）也走它。
- **通知只在真出事时发**：异常退出、自动重启到上限、自动重启起不来。不发更新、
  不发「检查过了」——网络不通就弹框只会让人烦。开关是 `Settings.Notify`（默认开，
  偏好设置「通用」里那一项），界面这一层判（`notifyService`），后端不判。
- **去重按「服务名 + 用途」**（`panel.told`），值取这件事的编号：崩溃用 `PID@启动时刻`
  （同一个死进程只报一次，重新拉起来算新的一次），起不来用 `operation.id`
  ——进程压根没起来，没有东西可以认，只能给动作编号。**别拿指针当编号**：地址会被
  后来的分配复用，两次不同的失败就会被认成同一次。没有这一层的话，额度用光的服务
  会每三秒弹一条。
- **用户自己点的那一下起不来不发通知**（`job.auto` 为假就跳过）：他正看着屏幕，
  界面上那行已经写着为什么。不发还有第二个理由——手工启动失败常常是「端口被占」，
  而那时日志里一个字都没写过，读出来的诊断是上一件事的，比不说更坏。
- **诊断进通知正文前要摘掉「，详见 <路径>」**（`trimDetailPath`）：系统通知点不开路径，
  而它常比前半句还长，会把正文占满。
- 平台差异照旧只落在 `notify_<平台>.go`，共用文件里不出现平台专有字符串。
  macOS 走 `osascript -e 'display notification …'`（同 `picker_darwin.go` 一条路，
  不为一行通知引 cgo）；Linux 走 `notify-send`，没装就什么都不做；
  其余平台 `notify_other.go` 直接返回错误。
- **Windows 的 toast 必须给 `CreateToastNotifier` 一个注册过的 AUMID**，没有就静默丢掉
  ——不报错、不显示，只是什么都没有。Pier 没有自己的（`install.ps1` 建的快捷方式不带
  `AppUserModelID`），所以借 PowerShell 自己那一串（`toastAppID`，每台机器上都注册着）。
  代价只是落款写着 Windows PowerShell；通知标题里本来就带着是哪个服务出的事。
- **Windows 上不生成 `.ps1`**：脚本整段走 `-EncodedCommand`（base64 的 UTF-16LE）传，
  绕开「`.ps1` 必须带 BOM」那条规矩——运行时生成的脚本保不了这个证（见上文），
  而且这条路中间不经过任何代码页，中文标题原样到达。顺带脚本不出现在进程列表的命令行里。
- **发通知不能挡住巡检那条协程**（每三秒跑一遍）：界面这一层在另一条协程里发。
  发不出去一律丢掉、不报错——用户关了通知权限、没装 notify-send，都是他自己的选择。

## 约定

- 界面在 `gui/app.js`（React + antd UMD + htm，无打包器）与 `gui/app.css`。
  `gui/app_test.go` 守着一批规则：色值只能写在 app.js 的 `PALETTE`（品牌标记另有白名单），
  不许 `size="small"` 之类、不许字号字面量，每个 Modal 都要挂 `BODY_SCROLL`，
  界面读的字段名必须在后端 json 标签里存在。
- 品牌标记有两处，几何必须一致：`gui/app.js` 的 `Mark` 与 `tools/mkicon`。
- 数据全部在 `~/.pier/`（`internal/config/store.go`）：`services.json`（服务、分组与共享环境变量，目录一律
  绝对路径）、`settings.json`（主题等界面偏好，启动时经 `w.Init` 注入为 `window.__PIER_SETTINGS__`；
  另有 `sdks`「SDK 管理」里手动添加的目录、`sdkDefaults` 各语言的全局默认、`updateCheck`
  自动检查开关与 `updateSkipped` 跳过的版本）、`state.json`（进程状态）、
  `update.json`（上次查到哪一版，见「更新与版本」）、`gui.lock`（界面开着时占着）、
  `restarts.json`（自动重启的记账，与 `restart.lock` 一样属于「这台机器」而不是某一份
  清单，见「出事与通知」）、
  `logs/`、`cache/bin/`、`cache/update/`。`PIER_HOME` 可整体换位置（测试一律设它，
  绝不碰真实数据）。环境变量与启动命令在表单「高级设置」里可编辑。
  偏好的默认值只在 `config.defaultSettings()` 写一份（`gui/app.go` 的 `settingsScript`
  直接用 `config.DefaultSettings()`），老用户的 settings.json 里没有新键时**靠
  `LoadSettings` 的「先铺默认值再 Unmarshal」拿到默认值**，不另写迁移。
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
- 侧栏只分两节：「服务」（全部服务、各个分组、新建分组，平铺）与「设置」（SDK 管理、日志管理）；
  页脚那行「偏好设置」不算第三节，它是收尾的入口，理由见下。分组前面那颗点
  是灰/绿/红三态，形状本身就说明它是分组，不给它单开一个标题；新页面归进这两节之一，别再加出第三种
  层级——标题、孤零零的一项、没标题的一项摞在一起，读的人得先猜这里有几层。分组也别再试缩进了
  （试过、撤了）：232 的字宽里让出 24px 之后名字列只剩 100px，悬停时「…」一出来（它要在名字右边占
  28px，数量不许挪，见 `navLabel`）就当场地省略成「demo-adm…」；而且一缩进，分组看着像是「全部服务」
  展开出来的子节点，可它不是——它是另一条看服务的口径。
- 「现在看的是哪份数据」连同它的其余四项（复制成 YAML、导出清单…、打开清单…、清理残留记录）
  都收在**偏好设置的「数据」一栏**（`SettingsPage` 里 `tab === "data"` 那两块）里，主区顶栏上
  没有「⋯」了（挪过两回：侧栏底部 → 顶栏「⋯」 → 这里。前两处都摆错了位置）。
  这一组说的是同一件事：手上这份清单在哪、怎么把它带走、怎么换一份，而这件事跟
  「对这一页的服务做什么」没有关系——摆在主区顶上，就是每时每刻都占着最显眼的那一排
  一颗与启停无关的按钮，点开还看得见一组跟屏幕上这一页无关的操作。顶栏现在只剩启停、
  从端口添加、添加应用，那三件的确是按页生效的。
  清单那一行平时写「本机数据 ~/.pier」，只读时写文件名加来源标签（见 `cfgName` / `cfgShort`，
  只读那份不在数据目录里，别照抄 `~/.pier`）；那一格的按钮跟着来源换说法：平时「打开数据目录」，
  只读时「在访达中显示清单」。**只读时才出现**的「回到本机数据」不要挪走也不要去掉：打开的
  若是 YAML，整份就只读、编辑入口全收起，没有这一颗，双击启动的人只能重启一次 Pier。
  代价说清楚：只读之后主区里不再有一眼可见的「现在看的是哪一份」，所以 `App` 顶上那条只读告警
  （`dc-hidden-bar`）里写着去哪儿切回来——两个来源的退路不一样，那句话跟着来源走，
  只写「去掉 --config」对「打开清单…」进来的人是一句没有门的话。
- 侧栏底部是「偏好设置」那一行（`dc-side-foot`）：更新、外观、数据都收在它打开的那一页里，
  主题那条 Segmented 已经从页脚撤了。文案用「偏好设置」而不是「设置」——上面已经有一节叫
  「设置」（SDK 管理 / 日志管理），同名并列会让人以为点进去是同一处。右侧那一格平时是当前版本号，
  有新版时换成主色圆点加最新版本号：自动更新最怕的就是「查过了，而没人知道」。
  别再给页脚加回带底带框的卡片——灰侧栏里最亮的就是它，比上面真要点的导航还抢眼。
  左栏那三栏（通用 / 外观 / 数据）是页面内的 `useState`，不落盘：它记的是「刚才在看哪一栏」，
  不是什么偏好。
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
- 「设置 · 日志管理」页（`gui/app.js` 的 `LogPage` + `panel.LogUsage`）列每个服务占了多少、几份、
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
