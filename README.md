# Pier

中文 | [English](README.en.md)

**一条命令，启停本地所有项目服务。**

Pier 把「哪些目录、算什么类型、怎么起、占哪个端口」记成一份清单，之后不管是你还是 AI，
都能一条命令（或者界面上点一下）把整套服务起起来、看日志、停掉。
一个 Go 写的命令行 `pier`，外加一个图形界面（macOS / Windows / Linux 各一份），零运行时依赖。

![Pier 主界面](docs/images/overview.png)

## 名字与图标

**Pier** 是栈桥——伸进水里的那块平台：船靠上来、装卸完再开走。这里停靠的是本地服务，
一眼能看见哪几条还在跑；但栈桥不是船的一部分，服务也不是 Pier 的一部分——它们起在独立会话里，
关掉 Pier、关掉终端，在跑的服务照跑，日志照写。

图标画的就是这座栈桥：一条桥面、底下两根桩、水面，最右那根桩伸出桥面当灯杆，顶上一盏**绿灯**
——正是界面里「服务在跑」的那颗点。底色是主色（`#3F6BFF` → `#1433D6`）的竖向渐变。
刻意不用字母 P：蓝底白 P 第一眼读出来的是停车场。

图标是**用代码画的**（[`tools/mkicon`](tools/mkicon)），不是塞进来的位图——配色要能跟着一起改，
位图得开设计软件；二进制资源进了源码，也看不出改了什么。侧栏那个标记（`gui/app.js` 的 `Mark`）
是同一套 200×200 视框里的几何，改一处要改两处。

## 为什么做这个

现在很大一部分代码是 AI 在写，改完就要重启看效果。为了一次重启去打开 IDE，太重；
每次在命令行里重新敲一遍启动参数，又太麻烦；而让 AI 频繁启停服务时，开 IDE 更是小题大做
——它要的只是「把这个服务重新跑起来」。

真实的项目还常常不止一个服务：后端几个模块、前端一个 dev server、再加两个模拟服务，
有的要 JDK 21、有的要 JDK 8，端口还得错开。这些信息在 IDE 的运行配置里本来就有，
但它只活在 IDE 里。

Pier 把它们拿出来，变成一份清单加一个常驻的轻量入口：

- **对人**：双击图标就是一个面板，起停、看日志、看谁占着端口，都在一块屏幕上。
- **对 AI**：`pier up <名字>`、`pier logs <名字>` 就够了，不必先理解这个项目是怎么启动的。

服务是**独立进程**，不挂在 Pier 底下：关掉 Pier、甚至关掉整个终端，在跑的服务照跑，
日志照写；下次打开 Pier，它按 PID / 进程组重新认领回来。

## 功能

### 启停与进程

- 一条命令启动全部或指定服务：`pier up`、`pier down`、`pier restart`。
- 停止在任何阶段都可用：排队中撤销，编译 / 拉起中取消，等待就绪中停进程。
  被打断的启动流程不会再把状态写回来。
- 服务起在独立会话（`setsid`）里，日志文件句柄由子进程继承——**退出 Pier 不带走任何在跑的服务**，
  也没有 stop-on-quit。
- 端口被谁占着按**进程组**认领：端口常握在子进程手里，只比 PID 会认错，
  界面上就会出现一个指向别处的「结束进程」。
- 资源占用只统计 Pier 自己和它起的服务（按进程组求和），不采整机。

### 多语言与工具链

- 服务类型：Go、Java（Maven 多模块）、Node、Python，以及任意 shell 命令。
- 工具链四步解析：**服务上指定 → 项目自己声明 → 全局默认 → 本机兜底**，
  每一步都写清楚为什么选它（界面与 `pier doctor` 共用同一份说明）。
  项目要求而本机没有的版本，照样选一个能用的，同时把差别说出来。
- 本机装了哪些 SDK 是自己扫出来的，不依赖 `PATH`：从访达双击启动的程序只有 `/usr/bin:/bin`，
  指望 `lookPath` 会看到一个空列表。Go 还认 `~/go/pkg/mod/golang.org/toolchain@*`
  （`go.mod` 要求更高版本时 go 命令自己下的那套）。
- 「SDK 管理」页可以手动添加任意目录、给每个语言设全局默认。

### 日志

- 一个服务一个目录、一天一份：`logs/<服务名>/<日期>.log`，保留 14 天。
- 跨零点不改文件名——一次运行就是一份日志，不会被午夜劈成两半。
- 文件是追加写的，「只看这一次运行」由启动标记加截断保证。
- 界面里能按日期翻历史、跟着新增内容滚动、在日志里搜索（⌘F）并高亮命中，
  还能复制全部、切换自动换行；ANSI 颜色按终端配色渲染。
- 清理时会跳过正在运行的服务：日志句柄握在那个独立进程手里，删掉只是 unlink，
  空间要等它退出才真的空出来。

### 界面（macOS）

- 侧栏只有两节：**服务**（全部服务 / 各分组）与**设置**（SDK 管理、日志）。
- 服务可拖动排序，可改名（日志目录跟着搬），可「复制一份」接着改。
- 搜索与「全部 / 在跑 / 异常」筛选，批量启停跟着当前这一页走。
- 日志抽屉、端口占用（谁占着、直接结束）、健康探针未通过时的收场方式。
- 清单可以「复制成 YAML」贴进 `pier.yaml`，也可以导出成文件、或者打开另一份清单来用
  （打开的那份是只读的，随时能切回本机数据）。
- 几套 SDK 的「将使用 X，依据 Y」与实际启动走的是同一个解析器，不是另写一份简化版。

日志抽屉、SDK 管理与深色主题（其余几张见 [docs/images](docs/images/README.md)）：

![日志抽屉](docs/images/logs.png)

![SDK 管理](docs/images/sdk.png)

![深色主题](docs/images/dark.png)

### 导入现成的配置

- `pier import` 读取 IDEA 的 `.idea/workspace.xml`，把里面已经调好的运行配置转成 Pier 的服务定义
  ——工作目录、模块、启动项名字都是现成的，手工抄成 YAML 极易抄错。
- `pier detect` 扫一个目录，认出里面有哪些项目、各自该用什么类型和端口。

## 技术栈

| | |
| --- | --- |
| 语言 | Go 1.24，编译出单一二进制，零运行时依赖 |
| 命令行 | 自绘表格；交互式面板用 [bubbletea](https://github.com/charmbracelet/bubbletea) + [lipgloss](https://github.com/charmbracelet/lipgloss) |
| 图形界面 | 系统自带的 WebView（[webview_go](https://github.com/webview/webview_go)：macOS 用 WKWebView、Windows 用 WebView2、Linux 用 WebKitGTK）+ React 18 + [antd](https://ant.design/) 5 + [htm](https://github.com/developit/htm) |
| 前端构建 | **没有构建**：第三方库是 UMD 构建，连同样式与应用代码一起内联进一份 HTML。不需要 npm，不需要打包器，也不引 CDN（断网、代理、CDN 被墙都会让窗口打开后一片空白，而用户无从判断原因） |
| 配置 | YAML（[yaml.v3](https://github.com/go-yaml/yaml)）；界面编辑的那份存在数据目录里 |
| 数据 | 全部是 `~/.pier/` 下的 JSON 与日志文件，没有数据库，没有后台守护进程 |
| 平台 | macOS 11+ / Windows 10+ / Linux（图形界面要 GTK3 与 WebKit2GTK 4.0）；`./package.sh` 一次打出三边的安装包 |

## 安装

### 命令行

```bash
git clone https://github.com/zhengshangjinx/pier.git
cd pier
go build -o pier .
./pier --help
```

把 `pier` 放进 `PATH` 之后，在任意项目目录下都能直接用。也可以直接装：

```bash
go install github.com/zhengshangjinx/pier@latest
```

### 图形界面

```bash
./package.sh            # 按平台各打一份，产物全部落在 dist/
```

| 平台 | 产物 | 怎么装 |
| --- | --- | --- |
| macOS | `Pier-<版本>-macos-universal.dmg` / `.zip` | 打开 `.dmg`，把 Pier 拖进「应用程序」 |
| Windows | `Pier-<版本>-windows-amd64.zip` | 解压后双击 `install.cmd` |
| Linux | `Pier-<版本>-linux-amd64.tar.gz` / `-arm64.tar.gz` | 解压后 `sh install.sh` |

三个平台装完是同一个样子：**启动台/开始菜单/应用菜单里一个 Pier，终端里一个 `pier`**。
Windows 装到 `%LOCALAPPDATA%\Programs\Pier`，Linux 装到 `~/.local`，都不需要管理员；
macOS 就是那个 `.app` 本身——界面 `pier-gui` 与命令行 `pier` 都在它里面，
`.dmg` 里的「应用程序」替身拖过去就装完了。

macOS 上 `pier` 在 `.app` 里面，做一次软链就有命令了：

```bash
ln -s /Applications/Pier.app/Contents/MacOS/pier /usr/local/bin/pier   # 要 /usr/local/bin 的写权限
```

不想动 `/usr/local/bin`，就把 `alias pier=/Applications/Pier.app/Contents/MacOS/pier`
写进 `~/.zshrc`。

各平台的运行时依赖：macOS 直接能跑；Windows 的界面要系统自带的 WebView2 运行时
（Win11 自带，Win10 多半也随 Edge 装好了）；Linux 的界面要 GTK3 与 WebKit2GTK 4.0。
命令行那一份哪边都不依赖图形库。

没有图形界面的机器（CI、服务器）上装 macOS 那份只有命令行的
`Pier-<版本>-macos-universal.tar.gz` 即可；Windows 与 Linux 的包里两份都在，
不跑 `install.cmd` / `install.sh`、直接用 `bin\pier.exe` 也照样能用。

只想在本机打一份也行：macOS 上 `./build-app.sh` 打包出 `build/Pier.app`（里面已经带着
`pier`；只要本机有 Go 和 Xcode 命令行工具即可，`sips`、`iconutil`、`codesign` 都是系统自带的；
生成的 App 用临时签名，自己用不需要开发者证书）；Windows / Linux 上 `go build -o pier-gui ./gui`。

## 快速开始

1. 让 Pier 认识你的项目：打开 `Pier.app`，在「全部服务」里新建服务；
   或者用命令行 `pier import <项目目录>` / `pier detect <项目目录>` 生成一份。
2. 起服务：

   ```bash
   pier up              # 全部启动
   pier up api web      # 只启动这两个
   pier status          # 看现在什么状态
   pier logs api -f     # 跟着看日志
   pier down            # 全部停掉
   ```

3. 想用一份手写的清单（不进数据目录、保存到项目里、可提交给同事）：

   ```bash
   pier --config ./pier.yaml status
   pier --config ./pier.yaml up
   ```

## 命令一览

| 命令 | 说明 |
| --- | --- |
| `pier doctor` | 检查各语言工具链能不能解析，并说明选了哪一个、依据是什么 |
| `pier detect [目录]` | 扫描目录，识别项目类型并给出建议的启动项 |
| `pier import [目录]` | 读取 `.idea` 运行配置，转成 Pier 的服务定义 |
| `pier up [服务...]` | 启动服务（不带名字则全部） |
| `pier down [服务...]` | 停止服务 |
| `pier restart [服务...]` | 重启服务 |
| `pier status` | 列出所有服务的运行状态 |
| `pier logs <服务> [-f]` | 查看某个服务的日志，`-f` 跟随 |
| `pier logs --size` | 看日志占了多少磁盘 |
| `pier logs --clean [服务] [--all]` | 清理超过 14 天的日志；`--all` 清空 |
| `pier ui` | 打开终端里的交互式面板 |

所有子命令都接受 `--config <清单>`，用来临时指定一份 YAML 清单（只读）。

## 服务清单

```yaml
# pier.yaml
toolchain:
  java: /opt/homebrew/opt/openjdk@21   # 按语言指定全局默认，也可以写 sdkman 的候选名

services:
  - name: api
    dir: ./server            # 相对清单目录，或绝对路径（不支持 ~）
    kind: java               # go / java / node / python / shell，留空则按目录内容识别
    module: shop-admin       # Java 的 Maven 子模块
    port: 8080
    health: http://localhost:8080/actuator/health
    env:
      SPRING_PROFILES_ACTIVE: dev

  - name: web
    dir: ./web
    kind: node
    script: dev              # package.json 里的脚本名，默认 dev
    port: 5173
    group: 前端               # 面板里的分组，留空归入「未分组」
```

| 字段 | 说明 |
| --- | --- |
| `name` | 服务名，也是 `up` / `down` / `logs` 用的名字 |
| `dir` | 工作目录，相对清单文件所在目录（不支持 `~`） |
| `group` | 面板里的分组名，留空归入「未分组」 |
| `note` | 给人看的备注，Pier 不解释它的内容 |
| `kind` | `go` / `java` / `node` / `python` / `shell`，留空按目录内容识别 |
| `run` / `build` | 显式指定运行与编译命令，留空按类型推断 |
| `module` | Java 的 Maven 子模块名 |
| `script` | Node 的 package.json 脚本名 |
| `port` | 监听端口，用于状态展示与占用检测 |
| `health` | 就绪探针地址。注意它是**就绪信号而不是成败判据**：没有探通的服务照样在跑，超时后状态会退回「运行中」并附一句说明 |
| `env` | 追加到进程环境里的变量 |
| `toolchain` | 这条服务专属的工具链覆盖，优先于顶层 |

Java 服务的编译步骤固定带 `-DskipDocker=true -Ddocker.skip=true -Ddockerfile.skip=true -Djib.skip=true`
——本地起服务不打镜像。

## 数据目录

```
~/.pier/
├── services.json   服务与分组（界面上编辑的就是这份）
├── settings.json   界面偏好、手动添加的 SDK 目录、各语言的全局默认
├── state.json      进程状态（PID / PGID），用来在重开 Pier 后认领服务
├── logs/<服务名>/<日期>.log
└── cache/bin/      Node 服务改进程名用的硬链
```

数据文件不存在就建一个空的。设 `PIER_HOME` 可以把整个目录挪到别处（测试一律用它，不碰真实数据）。

## 开发

```bash
go vet ./... && go test ./... -count=1     # 校验
./build-app.sh                             # 打包 build/Pier.app
tools/shoot/shoot.py /tmp/a.png 1382 880   # 把界面渲染成截图（演示数据）
```

仓库里有一份 [`AGENTS.md`](AGENTS.md)，记着这个项目的各种取舍与约定（界面规则、日志规则、
进程名怎么改、为什么某些看起来更简单的做法行不通），改代码前值得先读一遍。

## 协议

[MIT](LICENSE)。用到的第三方库与各自的协议见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
