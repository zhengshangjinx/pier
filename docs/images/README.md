# 界面截图

README 里的截图都放在这个目录，跟着仓库一起提交（README 在 GitHub 上要能直接显示）。
界面改了要照着重拍时走 `tools/shoot/shoot.py`——它用演示数据把界面渲染成 PNG，
不需要真的起服务，也不需要开 Pier。

```bash
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
mkdir -p docs/images

python3 tools/shoot/shoot.py docs/images/overview.png 1382 880
```

尺寸用初始窗口那个（1382×880），和用户第一次打开看到的一致。需要本机装了 Google Chrome。

| 文件 | 拍的是什么 | 命令（在仓库根目录执行） |
| --- | --- | --- |
| `overview.png` | 主界面：服务列表、概览那一排、占用对比 | `tools/shoot/shoot.py docs/images/overview.png 1382 880` |
| `filter.png` | 页头的搜索与「全部 / 在跑 / 异常」筛选 | `tools/shoot/shoot.py docs/images/filter.png 1382 880 filter shop` |
| `logs.png` | 日志抽屉：日期选择、搜索高亮、复制全部 | `PIER_SHOT_FIND=shop tools/shoot/shoot.py docs/images/logs.png 1382 880 log demo-admin` |
| `logs-page.png` | 设置 · 日志管理：每个服务占多少、可清理 | `tools/shoot/shoot.py docs/images/logs-page.png 1382 880 logsPage` |
| `sdk.png` | SDK 管理：各类别的 SDK 与「将使用 X，依据 Y」 | `tools/shoot/shoot.py docs/images/sdk.png 1382 880 sdk` |
| `settings.png` | 偏好设置 · 数据：手上是哪份清单、怎么换一份 | `PIER_SHOT_JS='[...document.querySelectorAll(".dc-set-tab")].find(function(e){return e.textContent==="数据"}).click()' tools/shoot/shoot.py docs/images/settings.png 1382 880 settings` |
| `dark.png` | 深色主题 | `tools/shoot/shoot.py docs/images/dark.png 1382 880 "" "" dark` |
| `readonly.png` | 打开一份只读清单（编辑入口收起） | `tools/shoot/shoot.py docs/images/readonly.png 1382 880 readonly` |

例子里的服务名要能在演示数据里找到：整套演示数据（8 个服务、3 个分组、端口、日志样本）
都在 `gui/app.js` 的 `demoState` / `demoCall` 一带，是编的，不指向任何真实项目。

其余可用的弹窗种类（`log` / `occupant` / `add` / `edit` / `portPicker` / `groupNew` /
`groupRename` / `delete` / `select <分组>` / `quick <all|running|attention>` /
`settings <样子>`）见 `tools/shoot/shoot.py` 的文件头说明。偏好设置页那一栏是页面内
`useState`，演示钩子够不着，所以上面 `settings.png` 那条靠 `PIER_SHOT_JS` 去点那一栏。想拍成真实窗口那种排版（页面铺到标题栏底下、
顶上留出 28px 拖拽条）就再加一个 `PIER_SHOT_NATIVE=1`。

## 发之前先看一眼

截图里不该出现真实项目名、内网地址或本机用户名。演示数据是编的，但生成截图前顺手扫一眼
总是便宜的：

```bash
# 演示数据里的路径一律写成 /Users/you/...（注释里偶尔用 xxx），扫出别的就是漏改了
grep -rnoE "/Users/[A-Za-z0-9._-]+" gui/app.js | grep -vE "/Users/(you|xxx)" | sort -u
```

更直接的一条：把界面上出现的服务名、端口、Maven 模块名，和本机 `~/.pier/services.json`
里的对一遍。两边的交集应当是空的——演示数据本来就是编的，一旦重合就说明它又是从真实清单
抄来的了。
