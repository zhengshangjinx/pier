#!/usr/bin/env python3
"""把 Pier 的界面渲染成截图，用来核对排版。

界面是 React + antd 拼出来的，六个弹窗只能靠点出来，而核对时没人去点。
「弹窗里整排显示 undefined」正是这么漏过去的：主面板看着好好的，
一开弹窗才发现两边字段名对不上。所以走这条路：

  1. PIER_DUMP_HTML 让 Go 侧导出带内联资源的整页 HTML（见 gui 的 TestDumpHTML）；
  2. 这个脚本往页面里注入三样东西（都是**只给预览用**的，不碰生产代码）：
     - rAF → setTimeout 垫片。无头 Chrome 的虚拟时间不驱动 requestAnimationFrame，
       不垫的话 rc-motion 的入场动画会冻在第一帧，.ant-modal 卡在 opacity:0，
       截出来只有一层遮罩；
     - 一段把过渡与动画整个关掉的 CSS。不能只挑浮层的类名去关：antd 的菜单项、
       Tag、Segmented 都带 .3s 的颜色过渡，从初始色过渡到主题色的中间态会被截进去
       （侧栏菜单项淡到几乎看不见、Tag 像蒙了一层），而且每次截到的进度不同，
       同一份代码的图忽好忽坏，很容易被误当成界面 bug；
     - 一段等 __pierDemo 挂上、并且服务列表真的回来了之后再去开弹窗的脚本。
  3. 无头 Chrome 截图。

用法：
    tools/shoot/shoot.py 输出.png 宽 高 [弹窗种类] [弹窗参数] [主题]

弹窗种类见 gui/app.js 里 window.__pierDemo.open 的分支：
    log / occupant / add / edit / portPicker / scan（扫本机端口）
    / adopt <端口>（从扫描结果里收一条：预演 + 填表，端口留空按演示数据里那个）
    / groupNew / groupRename / delete
    / select <分组> / logsPage / sdk
    / settings <样子>（偏好设置页；样子留空就是「有新版本、还没下」，
      见 app.js 里 __pierDemo.update 的分支：downloading / done / skipped /
      failed / checkfail / nostall / latest / result / resultOk）
    / filter <关键词> / quick <all|running|attention>（页头那行搜索与筛选）
    / notes（发布说明弹窗：整篇 markdown 的排版）
    / readonly <来源>（只读清单：页头说明位、收起的编辑入口与拖动）
      来源留空按「打开清单…」那种算，给「命令行指定」就拍命令行那种说法
    / firstRun <scan>（空清单的第一屏；给「scan」就连扫出来的名单一起拍，
      留空是刚打开、什么都还没扫的样子）
主题是 light / dark / system，留空表示跟随系统。

环境变量 PIER_SHOT_JS 是一段在演示钩子跑完之后执行的 JS，用来够到演示钩子
够不着的角落（例：点开偏好设置页里的「外观」那一栏）。

环境变量 PIER_SHOT_NATIVE=1 会给页面加上 .dc-native，也就是真实窗口里的排版
（页面铺到标题栏底下，顶上 28px 让给拖拽条）。默认不加：演示页没有那个窗口，
加了只会看到顶部空出一条。PIER_SHOT_FIND=词 会在日志抽屉开好之后往搜索框里
填上这个词，用来核对命中高亮压在 ANSI 配色上是什么样。

例：
    tools/shoot/shoot.py /tmp/a.png 1280 900
    tools/shoot/shoot.py /tmp/b.png 1280 900 occupant demo-web
    tools/shoot/shoot.py /tmp/c.png 1280 900 "" "" dark
    PIER_SHOT_NATIVE=1 tools/shoot/shoot.py /tmp/d.png 1280 900 add

需要本机装有 Google Chrome。本机 zsh profile 有问题，非交互执行时要显式给 PATH。
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# 只在预览里生效：把过渡与动画整个钉死，理由见文件头。
PIN = """
<style>
  *, *::before, *::after {
    transition: none !important;
    animation: none !important;
  }
  .ant-modal-mask, .ant-drawer-mask { opacity: .45 !important; }
  .ant-modal, .ant-drawer-content-wrapper, .ant-dropdown,
  .ant-select-dropdown, .ant-tooltip,
  .ant-zoom-enter, .ant-zoom-appear, .ant-fade-enter, .ant-fade-appear,
  .ant-slide-up-enter, .ant-slide-up-appear {
    opacity: 1 !important; transform: none !important;
  }
</style>
"""

RAF_SHIM = """
<script>
  // 无头 Chrome 的虚拟时间不驱动 rAF，动画会冻在第一帧。
  (function () {
    window.requestAnimationFrame = function (cb) {
      return setTimeout(function () { cb(Date.now()); }, 16);
    };
    window.cancelAnimationFrame = function (id) { clearTimeout(id); };
  })();
</script>
"""


def open_modal(kind, arg, theme=""):
    calls = []
    if os.environ.get("PIER_SHOT_NATIVE"):
        calls.append('document.documentElement.classList.add("dc-native")')
    if theme:
        calls.append('__pierDemo.theme("%s")' % theme)
    if kind:
        calls.append("__pierDemo.open(%s)" % (
            '"%s", "%s"' % (kind, arg) if arg else '"%s"' % kind))
    # 还有够不着的角落时（比如偏好设置页里「外观」那一栏，它是页面内的
    # 一个 useState，演示钩子没必要为它再开一个口子），直接给一段 JS。
    if os.environ.get("PIER_SHOT_JS"):
        calls.append(os.environ["PIER_SHOT_JS"])
    if not calls:
        return ""
    # 主题先切，再开弹窗：弹窗的浮层挂在 body 上，必须先有正确的算法。
    #
    # 必须等第一份状态真的回来了再开：演示数据是异步的，首次渲染时 services 还是
    # 空数组，这时按名字找服务会找不到，弹窗就打不开——而截图看上去只像是
    # 「这个弹窗没做」。为了让这种情况出声，app.js 那边的 find() 找不到时会
    # console.error，不再退回第一个服务。
    #
    # 等的是 ready()（有没有拿到数据）而不是「服务列表非空」：空清单那一屏
    # 要拍的正是一份一条服务都没有的清单。
    body = "; ".join(calls) + (find_step() if kind == "log" else "")
    return """
<script>
  (function () {
    var n = 0;
    var t = setInterval(function () {
      n++;
      if (n > 400) { clearInterval(t); document.title = "DEMO HOOK MISSING"; return; }
      if (!window.__pierDemo || !window.__pierDemo.ready()) return;
      clearInterval(t); %s;
    }, 25);
  })();
</script>
""" % body


def find_step():
    """PIER_SHOT_FIND=ERROR 时，把日志抽屉的搜索框填上这个词。

    输入框是受控的，直接改 el.value 不会让 React 知道；要拿到原型上的
    value setter 绕过去，再补一个 input 事件——React 是在根节点上监听
    冒泡的 input，这一步之后就当成用户敲的了。
    """
    word = os.environ.get("PIER_SHOT_FIND")
    if not word:
        return ""
    return """;
    (function () {
      var n = 0;
      var t = setInterval(function () {
        n++;
        if (n > 400) { clearInterval(t); document.title = "NO LOG TO SEARCH"; return; }
        var el = document.querySelector(".dc-log-bar input.ant-input");
        var pre = document.querySelector("pre.dc-log");
        if (!el || !pre || !pre.textContent) return;
        clearInterval(t);
        var set = Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype, "value").set;
        set.call(el, %s);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      }, 25);
    })()""" % json.dumps(word)


def build(out_png, w, h, kind, arg, theme=""):
    if not os.path.exists(CHROME):
        raise SystemExit("找不到 Google Chrome：%s" % CHROME)

    tmp = tempfile.mkdtemp(prefix="pier-shot-")
    raw = os.path.join(tmp, "raw.html")

    env = dict(os.environ)
    env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    env["PIER_DUMP_HTML"] = raw
    subprocess.run(["go", "test", "./gui", "-run", "TestDumpHTML", "-count=1", "-v"],
                   cwd=REPO, env=env, check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    html = open(raw, encoding="utf-8").read()
    # 垫片必须排在 app.js 前面，插在 </head> 之前最稳。
    if "</head>" not in html:
        raise SystemExit("导出的 HTML 里没有 </head>，注入点变了")
    html = html.replace("</head>", RAF_SHIM + PIN + "</head>", 1)
    html = html.replace("</body>", open_modal(kind, arg, theme) + "</body>", 1)

    page = os.path.join(tmp, "preview.html")
    open(page, "w", encoding="utf-8").write(html)

    # ?demo=1 必须在：真实窗口是用 SetHtml 喂的、不带查询串，
    # 只有带上了演示钩子才会挂出来。
    url = "file://" + page + "?demo=1"
    profile = os.path.join(tmp, "profile")
    shot = os.path.join(tmp, "shot.png")

    proc = subprocess.Popen([
        CHROME,
        "--headless=new", "--disable-gpu", "--no-sandbox",
        "--disable-dev-shm-usage",
        "--user-data-dir=" + profile,
        "--hide-scrollbars",
        "--force-device-scale-factor=" + os.environ.get("PIER_SHOT_SCALE", "2"),
        "--window-size=%d,%d" % (w, h),
        "--virtual-time-budget=9000",
        "--screenshot=" + shot,
        url,
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    # 无头 Chrome 不会自己退出：轮询产物出现，然后按自己的 user-data-dir 精确收掉。
    # 串行跑的时候千万别用宽泛的 pkill —— 会把别的实例一起带走。
    deadline = time.time() + 60
    while time.time() < deadline:
        if os.path.exists(shot) and os.path.getsize(shot) > 0:
            time.sleep(0.4)          # 等文件写完
            break
        if proc.poll() is not None and not os.path.exists(shot):
            raise SystemExit("Chrome 退出了但没产出截图")
        time.sleep(0.3)

    try:
        proc.terminate()
        proc.wait(timeout=10)
    except Exception:
        proc.kill()
    subprocess.run(["pkill", "-f", "--", "--user-data-dir=" + profile],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    if not os.path.exists(shot):
        raise SystemExit("没拿到截图")
    shutil.copy(shot, out_png)
    shutil.rmtree(tmp, ignore_errors=True)
    print("已写出 %s（%dx%d%s%s）" % (
        out_png, w, h, (" · " + kind) if kind else "",
        (" · " + theme) if theme else ""))


if __name__ == "__main__":
    if len(sys.argv) < 4:
        raise SystemExit(__doc__)
    build(sys.argv[1], int(sys.argv[2]), int(sys.argv[3]),
          sys.argv[4] if len(sys.argv) > 4 else "",
          sys.argv[5] if len(sys.argv) > 5 else "",
          sys.argv[6] if len(sys.argv) > 6 else "")
