// Pier 面板的界面代码。
//
// 用 React + antd 的 UMD 构建写成，没有 JSX、没有打包器：模板用 htm 的标签模板，
// 它把 `${...}` 插值成 createElement 的参数，写起来和 JSX 差不多，
// 但浏览器直接就能跑。这样 build-app.sh 仍然只要 Go + Xcode 命令行工具。
//
// 为什么不用 @ant-design/icons：它是单独的包，不在 antd 的 UMD 里，
// 引进来又要多一份内联资源。这里用到图标就那么几个，直接内联 SVG 更省事。
(function () {
  "use strict";

  var e = React.createElement;
  var html = htm.bind(e);
  var A = antd;

  // ── 与后端的桥 ───────────────────────────────────────────────────────────
  //
  // 绑定名逐字写出来（而不是拼字符串），一是为了让 TestBindingsMatchUI
  // 能直接从源码里把它们解析出来比对，二是漏了哪个一眼就能看见。
  var BRIDGE = {
    state: window.pierState,
    start: window.pierStart,
    stop: window.pierStop,
    restart: window.pierRestart,
    startAll: window.pierStartAll,
    stopAll: window.pierStopAll,
    logs: window.pierLogs,
    prune: window.pierPrune,
    logUsage: window.pierLogUsage,
    pruneLogs: window.pierPruneLogs,
    clearLogs: window.pierClearLogs,
    reveal: window.pierReveal,
    revealLog: window.pierRevealLog,
    revealLogs: window.pierRevealLogs,
    revealConfig: window.pierRevealConfig,
    openHealth: window.pierOpenHealth,
    portOwner: window.pierPortOwner,
    killPortOwner: window.pierKillPortOwner,
    inspectDir: window.pierInspectDir,
    saveService: window.pierSaveService,
    deleteService: window.pierDeleteService,
    clearHealth: window.pierClearHealth,
    renameService: window.pierRenameService,
    duplicateService: window.pierDuplicateService,
    // 拖动排序：送过去的是「这一页现在的顺序」，见 App 里 dragSvc 那一段。
    moveServices: window.pierMoveServices,
    moveGroups: window.pierMoveGroups,
    pickDirectory: window.pierPickDirectory,
    portCandidates: window.pierPortCandidates,
    // 扫本机在听的端口，把其中一条收进清单
    portScan: window.pierPortScan,
    adoptPort: window.pierAdoptPort,
    createGroup: window.pierCreateGroup,
    renameGroup: window.pierRenameGroup,
    deleteGroup: window.pierDeleteGroup,
    // 分享与迁移：清单变成一段文字，或者换一份清单来用
    serviceYAML: window.pierServiceYAML,
    configYAML: window.pierConfigYAML,
    exportConfig: window.pierExportConfig,
    openConfig: window.pierOpenConfig,
    useLocalConfig: window.pierUseLocalConfig,
    setChrome: window.pierSetChrome,
    copyText: window.pierCopyText,
    // 通知：Dock 角标与图标弹跳（见 App 里 attentionAll 那一段）
    badge: window.pierBadge,
    bounce: window.pierBounce,
    saveSettings: window.pierSaveSettings,
    sdkList: window.pierSDKList,
    sdkAdd: window.pierSDKAdd,
    sdkRemove: window.pierSDKRemove,
    sdkDefault: window.pierSDKDefault,
    sdkRescan: window.pierSDKRescan,
    toolchain: window.pierToolchain,
    // 更新：查、下、取消、换，以及跳过某一版（见偏好设置页）
    updateStatus: window.pierUpdateStatus,
    updateCheck: window.pierUpdateCheck,
    updateDownload: window.pierUpdateDownload,
    updateCancel: window.pierUpdateCancel,
    updateApply: window.pierUpdateApply,
    updateSkip: window.pierUpdateSkip,
    updateNotes: window.pierUpdateNotes,
    updateClearResult: window.pierUpdateClearResult,
    // 只有「重启并安装」用得上：安排好了、那句话也显示出来了，再把窗口关掉
    quit: window.pierQuit
  };

  var missingBindings = Object.keys(BRIDGE).filter(function (k) {
    return typeof BRIDGE[k] !== "function";
  });

  // 桥接断了就必须报错，绝不能悄悄退回演示数据：演示数据和真实状态长得一模一样，
  // 一旦自动回退，后端整个没接上也会显示成「一切正常」，那比直接报错危险得多。
  // 演示模式只认显式的 ?demo=1。
  var demoQuery = new URLSearchParams(location.search).has("demo");
  var DEMO = demoQuery || missingBindings.length > 0;
  var bridgeBroken = missingBindings.length > 0 && !demoQuery;
  // 窗口形态与文件管理器的叫法都由宿主注入（见 gui/app.go 的 nativeScript）：
  // 前者决定页面要不要给标题栏让出顶上那一条，后者只是措辞，别处一样是这三家。
  var NATIVE = window.__PIER_NATIVE__ === true;
  var FILEMGR = window.__PIER_FILEMGR__ || "访达";
  if (!DEMO && NATIVE) document.documentElement.classList.add("dc-native");

  // call 统一处理「后端返回 JSON 字符串」这件事：解析、并把 ok:false 变成异常，
  // 这样调用方可以用 try/catch 而不是每次手写 if (!r.ok)。
  async function call(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    if (DEMO) return demoCall(name, args);
    var fn = BRIDGE[name];
    if (typeof fn !== "function") throw new Error("后端没有绑定 " + name + "，这个功能用不了");
    var raw = await fn.apply(null, args);
    var out;
    try {
      out = typeof raw === "string" ? JSON.parse(raw) : raw;
    } catch (err) {
      throw new Error(name + " 返回的不是 JSON：" + String(raw).slice(0, 200));
    }
    if (out && out.ok === false) throw new Error(out.msg || out.error || "操作失败");
    return out;
  }

  // ── 演示数据 ─────────────────────────────────────────────────────────────
  // 只在 ?demo=1 时使用。每个弹窗都要覆盖到，否则演示模式下点开弹窗只会看到一句报错，
  // 排版就没法在不连后端的情况下检查了。

  // 各类别的可执行文件名，只给演示数据拼路径用（真的去读时是 internal/toolchain 的事）。
  var DEMO_EXE = { java: "java", maven: "mvn", node: "node", python: "python3", go: "go" };

  // 演示用的「这个服务实际会用哪套 SDK」。字段与 proc.ToolInfo 一一对应。
  // 带 warn 的那条专门留着：项目要的版本本机没有时，这一行会多出一个警示标记，
  // 而那种情况平时很难碰上，不摆一份出来就永远没人看过它长什么样。
  function demoRuntime(kind, label, version, home, source, reason, warn) {
    // bin 由 home 推出来：界面上真正显示的是它（各类别的 home 含义不一致，
    // 见 internal/toolchain 的 tool()）。Maven 例外，项目自带时跑的是 mvnw。
    var bin = kind === "maven" && source === "项目自带"
      ? home.replace(/\/$/, "") + "/mvnw"
      : home.replace(/\/$/, "") + "/bin/" + (DEMO_EXE[kind] || kind);
    return { kind: kind, label: label, version: version, home: home, bin: bin, source: source,
      reason: reason, warn: warn || "", error: "" };
  }
  var DEMO_JAVA = demoRuntime("java", "JDK 21.0.9 · Oracle", "21.0.9",
    "/Users/you/.sdkman/candidates/java/21.0.9-oracle", "sdkman", "按 pom 要求的 JDK 21");
  var DEMO_JAVA_OLD = demoRuntime("java", "JDK 17.0.12 · Oracle", "17.0.12",
    "/Users/you/.sdkman/candidates/java/17.0.12-oracle", "sdkman",
    "本机版本最高的", "pom 要求 JDK 21，本机装的都满足不了，先拿这个顶着");
  var DEMO_MAVEN_WRAPPER = demoRuntime("maven", "Maven Wrapper", "Wrapper",
    "/Users/you/workspace/shop", "项目自带", "项目自带的 mvnw");
  var DEMO_MAVEN = demoRuntime("maven", "Maven 3.9.9", "3.9.9",
    "/Users/you/.sdkman/candidates/maven/3.9.9", "sdkman", "本机版本最高的");
  var DEMO_NODE = demoRuntime("node", "Node.js 22.20.0", "22.20.0",
    "/Users/you/.nvm/versions/node/v22.20.0", "nvm", "按 .nvmrc 要求");
  var DEMO_PY = demoRuntime("python", "Python 3.12.10", "3.12.10",
    "/Users/you/workspace/mock-payment/.venv", "项目虚拟环境", "项目虚拟环境");
  var DEMO_GO = demoRuntime("go", "Go 1.24.0", "1.24.0",
    "/opt/homebrew/Cellar/go/1.24.0", "Homebrew", "按 go.mod 的 go 指令");

  // 演示用的 SDK 清单，字段与 manage.SDKList 一致。
  function demoSdks() {
    var sdk = function (kind, path, label, version, vendor, source, manual) {
      return { kind: kind, path: path, bin: path + "/bin/" + kind, label: label,
        version: version, vendor: vendor, source: source, manual: !!manual };
    };
    var h = "/Users/you";
    return { ok: true, kinds: [
      { kind: "java", label: "JDK", default: h + "/.sdkman/candidates/java/21.0.9-oracle", items: [
        sdk("java", h + "/.sdkman/candidates/java/21.0.9-oracle", "21.0.9 · Oracle", "21.0.9", "Oracle", "sdkman"),
        sdk("java", "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home",
          "21.0.4 · Temurin", "21.0.4", "Temurin", "系统 JDK 目录"),
        sdk("java", h + "/.sdkman/candidates/java/17.0.12-oracle", "17.0.12 · Oracle", "17.0.12", "Oracle", "sdkman"),
        sdk("java", h + "/Library/Java/JavaVirtualMachines/zulu-8.jdk/Contents/Home",
          "1.8.0_472 · Zulu", "1.8.0_472", "Zulu", "IDEA 下载"),
        sdk("java", "/Users/you/sdk/jdk-21.0.2", "21.0.2", "21.0.2", "", "手动添加", true)
      ]},
      { kind: "maven", label: "Maven", default: "", items: [
        sdk("maven", h + "/.sdkman/candidates/maven/3.9.9", "3.9.9", "3.9.9", "", "sdkman"),
        sdk("maven", "/opt/homebrew/Cellar/maven/3.9.6", "3.9.6", "3.9.6", "", "Homebrew")
      ]},
      { kind: "node", label: "Node.js", default: "", items: [
        sdk("node", h + "/.nvm/versions/node/v22.20.0", "22.20.0", "22.20.0", "", "nvm"),
        sdk("node", h + "/.nvm/versions/node/v20.11.1", "20.11.1", "20.11.1", "", "nvm"),
        sdk("node", "/opt/homebrew/Cellar/node/24.1.0", "24.1.0", "24.1.0", "", "Homebrew")
      ]},
      { kind: "python", label: "Python", default: "", items: [
        sdk("python", h + "/.pyenv/versions/3.12.10", "3.12.10", "3.12.10", "", "pyenv"),
        sdk("python", "/opt/homebrew/Cellar/python@3.11/3.11.9", "3.11.9", "3.11.9", "", "Homebrew")
      ]},
      { kind: "go", label: "Go", default: "/opt/homebrew/Cellar/go/1.24.0", items: [
        sdk("go", "/opt/homebrew/Cellar/go/1.24.0", "1.24.0", "1.24.0", "", "Homebrew"),
        sdk("go", h + "/sdk/go1.22.5", "1.22.5", "1.22.5", "", "手动添加", true)
      ]}
    ]};
  }

  // 演示模式下代替后端那次解析。真实的挑选逻辑在 internal/toolchain，
  // 这里只求「界面拿到的形状是对的」：按类型给一组固定结果，服务上钉了哪个
  // 就把它换成钉的那个并写明「服务上指定」——表单下方那行提示正是靠它验的。
  function demoToolchain(inv) {
    if (!inv.dir) return { ok: false, msg: "请先填写项目目录" };
    var by = { go: [DEMO_GO], java: [DEMO_JAVA, DEMO_MAVEN],
      node: [DEMO_NODE], python: [DEMO_PY], shell: [] };
    var tools = by[inv.kind] || [];
    // 「项目要的版本本机没有」这一条也留一份演示：它平时最难碰上，
    // 不摆出来，表单里那个警示色标记就永远没人核对过。
    if (inv.name === "shop-app") {
      tools = tools.map(function (t) { return t.kind === "java" ? DEMO_JAVA_OLD : t; });
    }
    var pin = (inv.toolchain || {})[inv.kind];
    if (pin) {
      tools = tools.map(function (t) {
        return t.kind !== inv.kind ? t : Object.assign({}, t, {
          home: pin, bin: pin + "/bin/" + t.kind, source: "服务上指定", reason: "服务上指定"
        });
      });
    }
    return { ok: true, absDir: inv.dir, tools: tools };
  }

  // 演示用的只读开关。放在这里而不是往拿到的那份 state 上打补丁：状态每 5 秒
  // 重新取一遍，补在那一份上的字段下一跳就没有了——而截图恰恰是在那之后拍的，
  // 拍出来的会是一张平平无奇的可编辑页面，还看不出哪里不对。
  var demoReadOnly = false;
  var demoReadOnlySrc = "";

  // 演示数据里「清单已经写掉的那几个端口」，就是 demoState 里那八个服务声明的。
  // 写一份共用的：后端那边 `Existing` 处处都是 cfg.UsedPorts()，同一屏数据里两份
  // 清单若是各写各的，迟早一份多一个少一个，而看的人只会去信其中一份。
  var DEMO_EXISTING = [47811, 47812, 47813, 47821, 47822, 47823, 47831, 47832];

  function demoState() {
    var mk = function (o) {
      return Object.assign({
        dir: "/Users/you/workspace/" + o.name,
        kind: "go", port: 0, group: "示例后端", statusKey: "stopped", statusText: "未启动",
        portText: "", pid: 0, uptime: "", health: "", healthy: false, hasHealth: false,
        // 探针等满一个窗口还没通过（服务在跑）。它和 hasHealth 是两件事：
        // 没配探针的服务永远是 false。
        probeExpired: false,
        note: "", running: false, portOpen: false, stale: false,
        // 后端给的是 proc.LogFile：这个服务这次运行写的那一份，不是「今天那份」。
        logPath: "/Users/you/.pier/logs/" + o.name + "/2026-10-01.log",
        userNote: "", run: "", build: "", module: "", script: "",
        // 只读时每条服务都不可编辑，和真实清单一样：只翻 readOnly 会摆出一份
        // 自相矛盾的画面（顶上写着「只读不写」，每一行却还挂着「编辑这个应用」）。
        editable: !demoReadOnly,
        occupant: null, op: "", opErr: "", opKind: "", toolchain: null,
        // 依赖与重启：默认没有前置、不自动重启，需要演的那几条各自覆盖。
        dependsOn: [], restart: "", restartNote: "",
        runtimes: [DEMO_GO],
        usage: { cpu: 0, memBytes: 0, procs: 0 }
      }, o);
    };
    return {
      ok: true,
      configPath: demoReadOnly ? "/Users/you/Desktop/pier.yaml" : "/Users/you/.pier/services.json",
      // 目录跟着清单走，不是写死 ~/.pier：只读那份在桌面上，真实的 configDir
      // 也就是那份 YAML 所在的目录。摆一份自相矛盾的数据（pier.yaml 配 ~/.pier）
      // 会被截图原样带出去，看的人只会去查后端。
      configDir: demoReadOnly ? "/Users/you/Desktop" : "/Users/you/.pier",
      configSource: demoReadOnly ? (demoReadOnlySrc || "手动指定") : "本机数据",
      readOnly: demoReadOnly,
      ungroupedName: "未分组",
      // 分组用量是组内服务相加的结果，这里的数字必须真的加起来对得上：
      // 演示数据是用来核对排版的，界面上摆一份自相矛盾的数据，核对的人
      // 只会去查后端，而不是去查这份 fixture。
      groups: [
        { name: "示例后端", count: 3, builtin: false,
          usage: { cpu: 1.6, memBytes: 402653184, procs: 5 } },
        { name: "示例服务", count: 3, builtin: false,
          usage: { cpu: 2.6, memBytes: 805306368, procs: 4 } },
        { name: "模拟服务", count: 2, builtin: false,
          usage: { cpu: 0.2, memBytes: 134217728, procs: 2 } },
        { name: "未分组", count: 0, builtin: true,
          usage: { cpu: 0, memBytes: 0, procs: 0 } }
      ],
      usage: { cpu: 4.4, memBytes: 1342177280, procs: 11 },
      self: { cpu: 0.3, memBytes: 100663296, procs: 1 },
      metricsError: "",
      busyCount: 1,
      services: [
        mk({ name: "demo-admin", port: 47811, statusKey: "running", statusText: "运行中",
          pid: 48213, uptime: "12m30s", health: "http://localhost:47811/admin/health",
          hasHealth: true, healthy: true, running: true, portOpen: true,
          // 跑通了后端就不再多说一句「通过了」，那句说明位留给健康地址本身
          // （见 view.NoteText），演示数据照着后端的样子写。
          note: "http://localhost:47811/admin/health",
          userNote: "本地调试用，数据库指向 dev 库",
          usage: { cpu: 0.4, memBytes: 268435456, procs: 3 } }),
        mk({ name: "demo-app", port: 47812, statusKey: "starting", statusText: "启动中",
          pid: 48220, uptime: "8s", op: "等待就绪", opKind: "start", running: true, portOpen: true,
          health: "http://localhost:47812/app/health", hasHealth: true,
          // 这一条同时演「有前置」和「崩过、被拉起来了」：说明位那句是照
          // internal/panel 的 restartNote 原样写的，界面上不另拼一份。
          dependsOn: ["demo-admin"], restart: "on-failure",
          restartNote: "进程退出后已自动重启 1 次",
          note: "尚未通过健康探针",
          usage: { cpu: 1.2, memBytes: 134217728, procs: 2 } }),
        mk({ name: "demo-web", kind: "node", port: 47813, statusKey: "external",
          statusText: "外部运行", portOpen: true, runtimes: [DEMO_NODE],
          occupant: { port: 47813, pid: 8142, command: "node", user: "you" },
          note: "端口被 Pier 之外的进程占用" }),
        mk({ name: "shop-admin", kind: "java", port: 47821, group: "示例服务",
          module: "shop-admin", health: "http://localhost:47821/actuator/health",
          env: { SPRING_PROFILES_ACTIVE: "dev" },
          toolchain: { java: "/Users/you/.sdkman/candidates/java/21.0.9-oracle" },
          runtimes: [DEMO_JAVA, DEMO_MAVEN_WRAPPER],
          hasHealth: true, note: "Maven 首次编译约 3 分钟" }),
        mk({ name: "shop-app", kind: "java", port: 47822, group: "示例服务",
          statusKey: "running", statusText: "运行中", pid: 51007, uptime: "3m12s",
          running: true, portOpen: true, hasHealth: true, healthy: true,
          health: "http://localhost:47822/actuator/health",
          // 这一条演示「项目要的版本本机没有」：圆点后面会多出一个警示标记。
          runtimes: [DEMO_JAVA_OLD, DEMO_MAVEN],
          userNote: "本地调试用，数据库指向 dev 库",
          usage: { cpu: 2.6, memBytes: 805306368, procs: 4 } }),
        mk({ name: "shop-web", kind: "node", port: 47823, group: "示例服务",
          statusKey: "stale", statusText: "已退出", portOpen: true, stale: true,
          runtimes: [DEMO_NODE],
          // 崩了几次、额度也用完了的样子。这一句是界面上唯一能看出「它不会
          // 自己再起来了」的地方，只有真跑过一次崩循环才见过，摆一份出来。
          restart: "on-failure",
          restartNote: "进程退出后已自动重启 3 次，已达上限（10 分钟 3 次），暂停自动重启",
          opErr: "服务 shop-web 编译失败，详见 /Users/you/.pier/logs/shop-web/2026-10-01.log",
          occupant: { port: 47823, pid: 9310, command: "node", user: "you" } }),
        // 这一条专演「探针没探通、服务照跑」：状态是「运行中」（不是永远挂在
        // 启动中），说明里挂一句警示色的解释，动作区没有停止之外的麻烦事，
        // 「⋯」里多一项「不再检查健康」。这正是「不是所有服务都有健康检查地址」
        // 那个问题的样子，摆一份出来才看得到它长什么样。
        mk({ name: "mock-payment", kind: "python", port: 47831, group: "模拟服务",
          statusKey: "running", statusText: "运行中", pid: 67214, uptime: "4m18s",
          running: true, portOpen: true,
          health: "http://localhost:47831/health", hasHealth: true, healthy: false,
          probeExpired: true,
          note: "健康探针未通过：地址可能不对，或这个服务没有健康接口",
          runtimes: [DEMO_PY],
          usage: { cpu: 0.2, memBytes: 134217728, procs: 2 } }),
        mk({ name: "mock-vod", kind: "python", port: 47832, group: "模拟服务",
          runtimes: [DEMO_PY] })
      ]
    };
  }

  // 演示用的端口扫描结果。四种样子各摆一条：Pier 正跑着的、清单里已经有位置的
  // （目录对得上）、能收进来的、以及查不到工作目录的。
  //
  // 端口号沿用 demoState 里那几个，别处（端口候选、占用详情）用的也是它们——
  // 同一屏数据里 47813 一会儿是这个一会儿是那个，核对的人只会去查后端。
  function demoPortScan() {
    return { ok: true, msg: "", ports: [
      { port: 47811, pid: 48213, command: "demo-admin", user: "you",
        dir: "/Users/you/workspace/demo-admin", dirShort: "~/workspace/demo-admin",
        service: "demo-admin", managed: true, known: "",
        origin: { kind: "panel", label: "Pier 面板", chain: ["Pier 面板"] } },
      { port: 47812, pid: 48220, command: "demo-app", user: "you",
        dir: "/Users/you/workspace/demo-app", dirShort: "~/workspace/demo-app",
        service: "demo-app", managed: true, known: "",
        origin: { kind: "panel", label: "Pier 面板", chain: ["Pier 面板"] } },
      // 清单里有 demo-web、此刻开着它的却是别人：managed 为假而 known 有值，
      // 这一行不给「纳管」——收了就是第二条抢同一个端口的服务。
      { port: 47813, pid: 8142, command: "node", user: "you",
        dir: "/Users/you/workspace/demo-web", dirShort: "~/workspace/demo-web",
        service: "", managed: false, known: "demo-web",
        origin: { kind: "terminal", label: "iTerm", chain: ["npm", "iTerm"] } },
      { port: 47822, pid: 51007, command: "java", user: "you",
        dir: "/Users/you/workspace/shop/shop-app", dirShort: "~/workspace/shop/shop-app",
        service: "shop-app", managed: true, known: "",
        origin: { kind: "panel", label: "Pier 面板", chain: ["Pier 面板"] } },
      { port: 47823, pid: 9310, command: "node", user: "you",
        dir: "/Users/you/workspace/shop/shop-web", dirShort: "~/workspace/shop/shop-web",
        service: "", managed: false, known: "shop-web",
        origin: { kind: "editor", label: "VS Code", chain: ["VS Code"] } },
      { port: 47831, pid: 67214, command: "python3", user: "you",
        dir: "/Users/you/workspace/shop/mock-payment", dirShort: "~/workspace/shop/mock-payment",
        service: "mock-payment", managed: true, known: "",
        origin: { kind: "panel", label: "Pier 面板", chain: ["Pier 面板"] } },
      // 这一条是这一屏存在的理由：清单里没有、目录也没人认领，而它正听着端口。
      { port: 47840, pid: 55501, command: "node", user: "you",
        dir: "/Users/you/workspace/shop/simulator-payment", dirShort: "~/workspace/shop/simulator-payment",
        service: "", managed: false, known: "",
        origin: { kind: "editor", label: "VS Code", chain: ["VS Code"] } },
      // 认不出来的一条（既没有宿主、也读不到工作目录）。
      { port: 47855, pid: 612, command: "postgres", user: "postgres",
        dir: "", dirShort: "", service: "", managed: false, known: "" }
    ] };
  }

  // 演示用的端口候选。三份名单必须互相对得上：used 是清单里写掉、此刻没人听的，
  // taken 是此刻正被监听的——后端先看监听再看清单，同一个号只会进其中一份，
  // 而 free 更不能与它们重叠。三份各写各的时候，47813 既是这张卡上的可用按钮，
  // 又是旁边那张卡上的「清单已用」，两张表并排摆着一眼就看出是假的。
  //
  // used 从 DEMO_EXISTING 里减掉 taken 得来，不另写一份；taken 就是 demoPortScan
  // 里正听着端口的那几个（含 47813、47823 这两个别人占着的），多一个 47840：
  // 它是「清单里没有、正被占着」那一条，在这一屏里正好该出现在已占用的名单上。
  //
  // 停法也照后端：凑够 48 个可用的就收工，所以 scanTo 是最后一个真看过的号，
  // 不是「本来打算看到哪儿」——那个上界会让界面说出一段没查过的范围。
  var DEMO_TAKEN = [47811, 47812, 47813, 47822, 47823, 47831, 47840];

  function demoPortCandidates() {
    var used = DEMO_EXISTING.filter(function (p) { return DEMO_TAKEN.indexOf(p) < 0; });
    var free = [];
    for (var p = 47800; free.length < 48; p++) {
      if (used.indexOf(p) < 0 && DEMO_TAKEN.indexOf(p) < 0) { free.push(p); }
    }
    return { ok: true, from: 47800, free: free, used: used, taken: DEMO_TAKEN,
      scanTo: free[free.length - 1],
      hints: { "47800": "通用 HTTP 备用端口", "47808": "通用备用端口" } };
  }

  // 演示模式下的更新状态。
  //
  // 做成一份能被改的：偏好设置页上「有新版本」「下载中」「下载好了」
  // 「上次换文件失败」这几种样子，靠等是等不出来的（真要去 GitHub 上查一次，
  // 还得正好有一版新的），而它们恰恰是这一页最需要核对排版的地方。
  // 出题口见页面末尾的 window.__pierDemo.update。
  var demoUpdate = null;

  function demoUpdateState() {
    if (!demoUpdate) {
      demoUpdate = {
        current: "0.2.0", latest: "0.3.0", hasUpdate: true, skipped: false,
        checking: false, downloading: false, done: false,
        received: 0, total: 0, progress: 0, receivedSize: "0 B", totalSize: "0 B",
        asset: "Pier-0.3.0-macos-universal.zip", assetSize: "22.1 MB",
        error: "", checkError: "",
        result: null,
        lastCheck: "今天 14:32", publishedAt: "2026-10-01",
        // 发布说明是多行的：这一格要按原样换行显示，不能挤成一行。
        notes: "这一版加了自动更新：\n· 启动后自己检查新版本，侧栏上会亮一颗点\n· 界面上直接下载、校验、重启安装",
        autoCheck: true, canInstall: true,
        installHint: "这次会替换 /Applications/Pier.app。"
      };
    }
    return demoUpdate;
  }

  async function demoCall(name, args) {
    await new Promise(function (r) { setTimeout(r, 120); });
    switch (name) {
      case "state": return demoState();
      case "logs":
        // 刻意带上 Maven 的 ANSI 颜色码：日志区要能把它们渲染成颜色，而不是一串「⌧[1;34m」。
        //
        // 演示模式也照真后端的契约来：dates 里多摆几天（截图时能看到日期选择、
        // 也能切到历史那天），text 按日期给，reset 表示「整段替换」。
        return { ok: true, path: "/tmp/demo.log", truncated: false,
          date: args[1] || "2026-09-29",
          dates: ["2026-09-29", "2026-09-27", "2026-09-24"],
          offset: 0, reset: true,
          text: Array.from({ length: 40 }, function (_, i) {
            return "[\x1b[1;34mINFO\x1b[m] 2026-09-29 00:12:" + String(i).padStart(2, "0") +
              "  Pier 演示输出的第 " + (i + 1) + " 行";
          }).concat([
            "[\x1b[1;34mINFO\x1b[m] shop-common .......................... \x1b[1;32mSUCCESS\x1b[m [  0.480 s]",
            "[\x1b[1;34mINFO\x1b[m] shop-data ............................ \x1b[1;31mFAILURE\x1b[m [  0.083 s]",
            "[\x1b[1;34mINFO\x1b[m] shop-admin ........................... \x1b[1;33mSKIPPED\x1b[m",
            "[\x1b[1;34mINFO\x1b[m] \x1b[1;31mBUILD FAILURE\x1b[m",
            "[\x1b[1;31mERROR\x1b[m] Failed to execute goal \x1b[32morg.apache.maven.plugins:maven-compiler-plugin:3.13.0:compile\x1b[m on project \x1b[36mshop-data\x1b[m: 无效的目标发行版: 21"
          ]).join("\n") };
      case "portOwner":
        return { ok: true, service: args[0], port: 47813,
          owner: { pid: 8142, uid: 501, user: "you", command: "node",
            args: "node /Users/you/workspace/demo/node_modules/.bin/vite --port 47813",
            started: "Mon Sep 29 00:12:33 2026", managed: false, service: "",
            // 认出来的宿主连着写：近的在前。摆一条编辑器起的，正好是「该去编辑器里
            // 关它，而不是在这里结束进程」那种情况。
            origin: { kind: "editor", label: "VS Code", chain: ["VS Code"] } } };
      case "portScan":
        return demoPortScan();
      case "adoptPort":
        // 读不到工作目录的那一条（47855）照真正的后端那样回一句失败：
        // 演示数据里留着它，是为了这条失败路径也能在界面上看一遍。
        if (parseInt(args[0], 10) === 47855) {
          return { ok: false,
            msg: "查不到 PID 612 的工作目录（可能是别的用户的进程，或者它已经不在了），请手动填写项目目录" };
        }
        return { ok: true, absPath: "/Users/you/workspace/shop/simulator-payment",
          relPath: "shop/simulator-payment", found: ["package.json"], kind: "node",
          plan: "npm run dev（package.json 的 dev 脚本）",
          existing: DEMO_EXISTING,
          suggestPort: parseInt(args[0], 10) || 47840, portFrom: "正在监听的端口",
          health: "http://localhost:" + (parseInt(args[0], 10) || 47840) + "/",
          suggestName: "simulator-payment",
          names: demoState().services.map(function (s) { return s.name; }),
          groups: ["示例后端", "示例服务", "模拟服务"],
          adopted: "PID 55501（node）" };
      case "inspectDir":
        return { ok: true, absPath: args[0], relPath: "shop/simulator-payment",
          found: ["package.json"], kind: "node",
          plan: "npm run dev（package.json 的 dev 脚本）",
          existing: DEMO_EXISTING,
          suggestPort: 5173, portFrom: ".env 的 VITE_PORT",
          health: "http://localhost:5173/", suggestName: "simulator-payment",
          names: demoState().services.map(function (s) { return s.name; }),
          groups: ["示例后端", "示例服务", "模拟服务"] };
      case "portCandidates":
        return demoPortCandidates();
      case "pickDirectory":
        return { ok: true, dir: "/Users/you/workspace/shop/simulator-payment",
          rel: "shop/simulator-payment" };
      case "logUsage":
        // 字节数与换算好的文字都要给：界面只负责排版，换算在后端（view.Bytes），
        // 和命令行 `pier logs --size` 是同一个数。合计与分项也要真的对得上。
        return { ok: true, dir: "/Users/you/.pier/logs", bytes: 24771197, size: "23.6 MB",
          files: 36, keepDays: 14,
          services: [
            { name: "shop-admin", bytes: 16777216, size: "16 MB", files: 14,
              oldest: "2026-09-18", newest: "2026-10-01" },
            { name: "shop-app", bytes: 5452595, size: "5.2 MB", files: 9,
              oldest: "2026-09-22", newest: "2026-10-01" },
            { name: "demo-web", bytes: 1677722, size: "1.6 MB", files: 7,
              oldest: "2026-09-25", newest: "2026-10-01" },
            { name: "demo-admin", bytes: 655360, size: "640 KB", files: 4,
              oldest: "2026-09-28", newest: "2026-10-01" },
            { name: "mock-payment", bytes: 98304, size: "96 KB", files: 2,
              oldest: "2026-09-30", newest: "2026-10-01" }
          ] };
      case "pruneLogs": return { ok: true, msg: args[0]
        ? "已删除 3 个日志文件，释放 2.4 MB"
        : "已删除 11 个日志文件，释放 8.1 MB" };
      case "clearLogs": return { ok: true, msg: args[0]
        ? "已清空 " + args[0] + " 的日志，释放 1.6 MB"
        : "已清空全部服务的日志，释放 23.6 MB" };
      case "clearHealth":
        return { ok: true, msg: "已去掉 " + args[0] + " 的健康检查，之后只看进程是否存活" };
      case "sdkList": return demoSdks();
      case "sdkAdd":
        return { ok: true, msg: "已添加（演示模式，没有真的写文件）",
          item: { kind: args[0], path: args[1] + "/Contents/Home", bin: args[1] + "/bin/" + args[0],
            label: "21.0.2", version: "21.0.2", vendor: "", source: "手动添加", manual: true } };
      case "sdkRemove": return { ok: true, msg: "已从手动添加里删掉 " + args[1] };
      case "sdkDefault":
        return { ok: true, msg: args[1]
          ? "已把它设为这一类的全局默认"
          : "已改回自动选择" };
      case "sdkRescan": return { ok: true, msg: "已重新扫描" };
      case "toolchain": return demoToolchain(JSON.parse(args[0] || "{}"));
      case "updateStatus": return demoUpdateState();
      // 检查要有个在飞的过程，否则「检查更新」按下去看不出任何变化，
      // 那一格转不转圈正是截图时要看的东西。
      case "updateCheck":
        demoUpdateState().checking = true;
        setTimeout(function () {
          var u = demoUpdateState();
          u.checking = false; u.lastCheck = "今天 14:35";
        }, 900);
        return { ok: true, msg: "" };
      case "updateDownload": {
        var d = demoUpdateState();
        d.downloading = true; d.error = "";
        d.total = 23173530;
        var step = function (got) {
          var u = demoUpdateState();
          if (!u.downloading) return;
          u.received = got; u.progress = Math.round(got * 100 / u.total);
          u.receivedSize = (got / 1048576).toFixed(1) + " MB";
          u.totalSize = "22.1 MB";
          if (got < u.total) setTimeout(function () { step(got + 3400000); }, 220);
          else { u.downloading = false; u.done = true; u.progress = 100;
            u.receivedSize = "22.1 MB"; }
        };
        setTimeout(function () { step(900000); }, 220);
        return { ok: true, msg: "" };
      }
      case "updateCancel": {
        var c = demoUpdateState();
        c.downloading = false; c.received = 0; c.progress = 0; c.receivedSize = "0 B";
        return { ok: true, msg: "" };
      }
      case "updateApply": return { ok: true,
        msg: "更新已经安排好了，换文件的过程写在 /Users/you/.pier/logs/update/2026-10-01.log 里。" };
      case "updateSkip": return { ok: true, msg: args[0] ? "这一版不再提示" : "恢复提示" };
      case "updateClearResult": demoUpdateState().result = null; return { ok: true, msg: "" };
      case "updateNotes": return { ok: true, msg: "" };
      case "quit": return { ok: true, msg: "" };
      case "saveSettings":
        // 开关改完立刻反映到状态里：这一页上的开关和后端那份偏好必须是同一个事实，
        // 演示模式下也让它们对得上。
        if (args[0]) {
          var patch = JSON.parse(args[0]);
          if (patch.updateCheck !== undefined) demoUpdateState().autoCheck = !!patch.updateCheck;
        }
        return { ok: true, msg: "" };
      case "createGroup": return { ok: true, msg: "已新建分组「" + args[0] + "」" };
      case "renameGroup": return { ok: true, msg: "已把分组「" + args[0] + "」改名为「" + args[1] + "」" };
      case "deleteGroup": return { ok: true, msg: "已删除分组「" + args[0] + "」" };
      case "killPortOwner": return { ok: true, msg: "已结束 PID " + args[1] + "，端口现已空出" };
      case "saveService": return { ok: true, msg: "已保存（演示模式，没有真的写文件）" };
      case "deleteService": return { ok: true, msg: "已处理（演示模式）" };
      case "renameService": return { ok: true, msg: "已把 " + args[0] + " 改名为 " + args[1] };
      // name 单独给：界面拿它把表单开在新复制出来的那一条上。
      case "duplicateService":
        return { ok: true, name: args[0] + "-copy",
          msg: "已复制为 " + args[0] + "-copy（端口换成 5174，健康检查已清掉）" };
      case "moveServices": return { ok: true, msg: "已调整顺序" };
      case "moveGroups": return { ok: true, msg: "已调整分组顺序" };
      // 导出的那几段文字由后端生成（字段名只在内部站得住脚），演示模式给一份
      // 形状一样的：截图时看的是「复制/导出的按钮在哪、点了说什么」，不是内容。
      case "serviceYAML":
        return { ok: true, text: "services:\n  - name: " + args[0] +
          "\n    group: 示例服务\n    kind: node\n    port: 47813\n" };
      case "configYAML":
        return { ok: true, text: "# Pier 清单 · 导出 2026-10-01\ntoolchain: {}\nservices:\n" };
      case "exportConfig": return { ok: true, msg: "已导出到 /Users/you/Desktop/pier.yaml" };
      case "openConfig": return { ok: true, msg: "已加载 /Users/you/Desktop/pier.yaml" };
      case "useLocalConfig": return { ok: true, msg: "已切回本机数据" };
      case "prune": return { ok: true, msg: "没有需要清理的记录" };
      default: return { ok: true, msg: "" };
    }
  }

  // ── 状态 ─────────────────────────────────────────────────────────────────

  // 状态刷新间隔。原来是 2 秒，代价是每次刷新都要在 Go 侧 spawn 一个 lsof
  // （ListeningInfo 要问全本机监听端口），实测常态吃掉 2% 以上的 CPU，
  // 而「哪个服务在跑」这件事 5 秒的延迟完全够用。
  //
  // 但不能拿这个空闲间隔去承担动作反馈：卡片上那个「启动中」的转圈、
  // 以及排队→启动中→等待就绪这几个阶段，全都来自 state 里的 op 字段，
  // 也就是只能靠轮询送过来。固定 5 秒的后果是点了「启动」之后按钮要愣上
  // 好几秒才转起来，中间几个阶段还会被并进同一次刷新里——看起来就像
  // 点了没反应。所以有动作在跑时改走 STATE_POLL_BUSY_MS 快轮询，
  // 空闲时仍旧 5 秒：费 CPU 的 lsof 只在真有动作的那几秒里多跑几次。
  //
  // 快档不能没有上限：一个服务起不来时会卡在「等待就绪」上，而后端的
  // HealthWait 是 180 秒（internal/proc/process.go），所以忙时最多也就
  // 多跑三分钟，且这三分钟里用户正盯着界面等结果，这个代价是值的。
  var STATE_POLL_MS = 5000;
  var STATE_POLL_BUSY_MS = 1000;

  // 更新状态的轮询间隔。它和服务状态那两档不是一回事：这条问的是本机
  // 「查到哪一版、下到哪儿了」，一次本地绑定调用，不出网——真正出网的检查
  // 六小时才一次（见 internal/update 的 AutoEvery）。
  //
  // 闲时一分钟一问是为了让侧栏那颗圆点自己冒出来：自动检查在后台查到新版本时
  // 没有任何东西通知页面，只有这条轮询能把它带上来。
  var UPDATE_POLL_MS = 60000;

  // 状态点的配色。键来自 internal/view 的 statusKey，加状态时两边要一起加。
  //
  // 颜色交给 antd 的语义色（Badge 的 status 取值），不再自己写十六进制：
  // 这样深浅两套主题下由同一套算法推出来，不会出现「暗色下状态点看不清」。
  // starting 用 warning 而不是 processing：processing 是 antd 的蓝色脉冲，
  // 而命令行与 TUI 里「启动中」一直是橙色，三端配色不能在这里分岔。
  var TONE = {
    running: "success",
    starting: "warning",
    external: "default",
    stale: "error",
    stopped: "default"
  };
  function tone(key) { return TONE[key] || TONE.stopped; }

  var KIND_LABEL = { go: "Go", java: "Java", node: "Node", python: "Python", shell: "Shell" };

  // 各类型的服务要用到哪几套 SDK。界面拿它决定「专属设置」里摆哪几个下拉、
  // 预演时把哪几个类别报上去。
  //
  // 这是 internal/config 里各个 planXxx 的第二份，只能靠 gui/app_test.go 的
  // TestFormToolKindsMatchPlan 守着——摆错了不会报错，只会让人对着一个与服务
  // 无关的空下拉发呆，或者更糟：以为某个版本已经钉住了。
  //
  // 不含 pnpm：它跟着选中的 node 走，不是独立安装的一套（见 internal/manage/sdk.go）。
  var KIND_TOOLS = {
    go: ["go"], java: ["java", "maven"], node: ["node"], python: ["python"], shell: []
  };

  // ── 小工具 ───────────────────────────────────────────────────────────────

  // 后端返回的字段读不出来时，界面必须喊出来。
  //
  // 之前踩过的坑：后端把字段名写成小写、界面按大写读，结果不是报错，
  // 是一排「—」和一个写着 undefined 的按钮——看上去像功能没做，
  // 实际是两边字段名对不上。这类静默失败必须有声音。
  function pick(obj, key, fallback) {
    var v = obj == null ? undefined : obj[key];
    if (v === undefined || v === null || v === "") {
      console.error("Pier 界面：读不到字段 " + key + "，实际拿到的是", obj);
      return fallback;
    }
    return v;
  }

  function isNum(v) { return typeof v === "number" && isFinite(v); }

  // 后端拿 "-" 当「这一项没有」的空值（见 internal/view 的 UptimeText 等）。
  // "-" 在 JS 里是真值，所以不能用 `if (v)` 判断有没有——那样未启动的服务
  // 会渲染出「运行 -」。要不要显示某一项，一律先过这个。
  function hasVal(v) {
    return v !== undefined && v !== null && v !== "" && v !== "-";
  }

  // 字节数换成「512 MB」「1.2 GB」。换算只写这一份，别处一律调它——
  // 两处各写一遍，迟早有一处把 1024 写成 1000。
  function fmtMem(bytes) {
    if (!isNum(bytes) || bytes <= 0) return "0 MB";
    var mb = bytes / 1048576;
    if (mb < 1024) return Math.round(mb) + " MB";
    return (mb / 1024).toFixed(1) + " GB";
  }

  // CPU 百分比留一位小数。
  //
  // 不留整数位：一个空闲的开发服务器常在 0.1~0.9 之间，抹成整数就全是「0%」，
  // 而「面板到底占了多少」这个问题恰恰就落在这一档上——全 0 看着像没测到。
  function fmtCPU(v) {
    if (!isNum(v)) return "-";
    return (Math.round(v * 10) / 10).toFixed(1) + "%";
  }

  // ── 图标 ─────────────────────────────────────────────────────────────────
  // 内联 SVG，笔画风格统一（1.6 的线宽、圆头圆角），尺寸随字号走。

  function Ico(props) {
    return html`<svg viewBox="0 0 16 16" width=${props.size || 14} height=${props.size || 14}
      fill="none" stroke="currentColor" stroke-width="1.6"
      stroke-linecap="round" stroke-linejoin="round"
      className=${props.className} style=${{ verticalAlign: "-2px", flex: "none" }}>${props.children}<//>`;
  }
  var IconPlay = function (p) { return html`<${Ico} ...${p}><path d="M4.5 3.2v9.6l8-4.8z"/><//>`; };
  var IconStop = function (p) { return html`<${Ico} ...${p}><rect x="4" y="4" width="8" height="8" rx="1.5"/><//>`; };
  var IconRestart = function (p) { return html`<${Ico} ...${p}><path d="M13 8a5 5 0 1 1-1.6-3.7"/><path d="M13 2.5V5h-2.5"/><//>`; };
  var IconMore = function (p) { return html`<${Ico} ...${p}><circle cx="3.5" cy="8" r="1"/><circle cx="8" cy="8" r="1"/><circle cx="12.5" cy="8" r="1"/><//>`; };
  var IconFolder = function (p) { return html`<${Ico} ...${p}><path d="M2 4.5A1.5 1.5 0 0 1 3.5 3h2.2l1.3 1.6h5.5A1.5 1.5 0 0 1 14 6.1v5.4A1.5 1.5 0 0 1 12.5 13h-9A1.5 1.5 0 0 1 2 11.5z"/><//>`; };
  var IconEdit = function (p) { return html`<${Ico} ...${p}><path d="M11.2 2.6l2.2 2.2-7.6 7.6-2.9.7.7-2.9z"/><//>`; };
  var IconTrash = function (p) { return html`<${Ico} ...${p}><path d="M2.8 4.5h10.4"/><path d="M6.5 4.5V3.2h3v1.3"/><path d="M4.2 4.5l.6 8.3h6.4l.6-8.3"/><//>`; };
  var IconDoc = function (p) { return html`<${Ico} ...${p}><path d="M4 2.5h5l3 3v8H4z"/><path d="M9 2.5v3h3"/><//>`; };
  var IconPlus = function (p) { return html`<${Ico} ...${p}><path d="M8 3.4v9.2M3.4 8h9.2"/><//>`; };
  var IconHeart = function (p) { return html`<${Ico} ...${p}><path d="M8 13S2.5 9.7 2.5 6.2A2.7 2.7 0 0 1 8 4.6a2.7 2.7 0 0 1 5.5 1.6C13.5 9.7 8 13 8 13z"/><//>`; };
  var IconGrid = function (p) { return html`<${Ico} ...${p}><rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1.2"/><rect x="9" y="2.5" width="4.5" height="4.5" rx="1.2"/><rect x="2.5" y="9" width="4.5" height="4.5" rx="1.2"/><rect x="9" y="9" width="4.5" height="4.5" rx="1.2"/><//>`; };
  var IconSun = function (p) { return html`<${Ico} ...${p}><circle cx="8" cy="8" r="2.8"/><path d="M8 1.8v1.4M8 12.8v1.4M1.8 8h1.4M12.8 8h1.4M3.6 3.6l1 1M11.4 11.4l1 1M3.6 12.4l1-1M11.4 4.6l1-1"/><//>`; };
  var IconMoon = function (p) { return html`<${Ico} ...${p}><path d="M13.2 9.6A5.5 5.5 0 0 1 6.4 2.8a5.5 5.5 0 1 0 6.8 6.8z"/><//>`; };
  var IconMonitor = function (p) { return html`<${Ico} ...${p}><rect x="2" y="3" width="12" height="8" rx="1.5"/><path d="M6 13.5h4M8 11v2.5"/><//>`; };
  var IconBroom = function (p) { return html`<${Ico} ...${p}><path d="M10.5 2.5L7.8 7.2"/><path d="M5.2 7.2h5.2l1.1 6.3H4.1z"/><path d="M6.6 10.2v3.3M9 10.2v3.3"/><//>`; };
  var IconAlert = function (p) { return html`<${Ico} ...${p}><circle cx="8" cy="8" r="5.8"/><path d="M8 5v3.4M8 10.8v.1"/><//>`; };
  var IconLayers = function (p) { return html`<${Ico} ...${p}><path d="M8 2.5l5.5 2.8L8 8.1 2.5 5.3z"/><path d="M2.5 8.2L8 11l5.5-2.8"/><path d="M2.5 11L8 13.8l5.5-2.8"/><//>`; };
  var IconRefresh = function (p) { return html`<${Ico} ...${p}><path d="M13.2 8a5.2 5.2 0 1 1-2.1-4.2"/><path d="M12.3 1.5l1.1 2.5-2.6.6"/><//>`; };
  var IconPulse = function (p) { return html`<${Ico} ...${p}><path d="M1.8 8.4h2.6l1.5-4 2.6 7.4 1.7-4.6h4"/><//>`; };
  var IconGauge = function (p) { return html`<${Ico} ...${p}><path d="M2.6 11.5a5.6 5.6 0 1 1 10.8 0"/><path d="M8 9.2l2.4-2.6"/><circle cx="8" cy="9.6" r=".6"/><//>`; };
  var IconChip = function (p) { return html`<${Ico} ...${p}><rect x="4.5" y="4.5" width="7" height="7" rx="1.5"/><path d="M6.6 2.6v1.9M9.4 2.6v1.9M6.6 11.5v1.9M9.4 11.5v1.9M2.6 6.6h1.9M2.6 9.4h1.9M11.5 6.6h1.9M11.5 9.4h1.9"/><//>`; };
  var IconSearch = function (p) { return html`<${Ico} ...${p}><circle cx="7.2" cy="7.2" r="4.1"/><path d="M10.3 10.3l3.2 3.2"/><//>`; };
  var IconCopy = function (p) { return html`<${Ico} ...${p}><rect x="5.6" y="2.6" width="7.8" height="7.8" rx="1.6"/><path d="M10.4 10.4v1.4a1.6 1.6 0 0 1-1.6 1.6H4.2a1.6 1.6 0 0 1-1.6-1.6V7.2a1.6 1.6 0 0 1 1.6-1.6h1.4"/><//>`; };
  // 齿轮：八颗齿的轮盘加一个轴孔。齿是折线不是圆弧——16px 上弧线看不出来，
  // 而折线的每个转角都被 Ico 的 stroke-linejoin 磨圆，正好是齿轮的样子。
  var IconGear = function (p) { return html`<${Ico} ...${p}><path d="M14.5 8l-2.25 1.76.28 2.83-2.77-.33L8 14.5l-1.76-2.24-2.77.33.28-2.83L1.5 8l2.25-1.76-.28-2.83 2.77.33L8 1.5l1.76 2.24 2.77-.33-.28 2.83z"/><circle cx="8" cy="8" r="2.2"/><//>`; };
  var IconDownload = function (p) { return html`<${Ico} ...${p}><path d="M8 2.2v7.4"/><path d="M4.9 6.8L8 9.9l3.1-3.1"/><path d="M2.6 11.2v1.7a1.5 1.5 0 0 0 1.5 1.5h7.8a1.5 1.5 0 0 0 1.5-1.5v-1.7"/><//>`; };

  // 品牌标记：和应用图标（tools/mkicon）同一套几何，改动时两处一起对。
  //
  // 图形是一座栈桥：一条桥面、底下的桩、水面，最右那根桩伸出桥面做灯杆，
  // 顶上一盏绿灯——就是界面里「服务在跑」的那颗点。底色用主色的竖向渐变，
  // 与「添加应用」那一个实心按钮同色，整页只有这两处是满铺的主色。
  // 刻意不用字母 P：蓝底白 P 第一眼读出来的是停车场。
  function Mark(props) {
    var n = props.size || 26;
    return html`<svg width=${n} height=${n} viewBox="0 0 200 200" style=${{ flex: "none", display: "block" }}>
      <defs>
        <linearGradient id="pier-mark-bg" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color="#3F6BFF"/><stop offset="1" stop-color="#1433D6"/>
        </linearGradient>
      </defs>
      <rect width="200" height="200" rx="45" fill="url(#pier-mark-bg)"/>
      <g transform="translate(0 6)">
        <path d="M46 152c9-6 18-6 27 0s18 6 27 0 18-6 27 0 18 6 27 0" fill="none" stroke="#FFFFFF"
          stroke-opacity=".45" stroke-width="8" stroke-linecap="round"/>
        <rect x="125" y="63" width="16" height="75" rx="8" fill="#FFFFFF"/>
        <rect x="42" y="88" width="116" height="18" rx="9" fill="#FFFFFF"/>
        <rect x="59" y="100" width="16" height="38" rx="8" fill="#FFFFFF"/>
        <rect x="92" y="100" width="16" height="38" rx="8" fill="#FFFFFF"/>
        <circle cx="133" cy="40" r="19" fill="#3DDC97" fill-opacity=".28"/>
        <circle cx="133" cy="40" r="11" fill="#3DDC97"/>
      </g>
    </svg>`;
  }

  // ── 主题 ─────────────────────────────────────────────────────────────────

  // 三段式主题切换。文案取「亮色 / 暗色 / 系统」而不是「跟随系统」：
  // 侧栏只有 248 宽，四个字在等分的 Segmented 里会被 antd 省略成「跟随…」。
  var THEMES = [
    { key: "light", label: "亮色", icon: IconSun },
    { key: "dark", label: "暗色", icon: IconMoon },
    { key: "system", label: "系统", icon: IconMonitor }
  ];
  var THEME_KEY = "pier.theme";

  function systemDark() {
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  }

  // 主题存在 ~/.pier/settings.json 里，由后端在页面脚本之前注入到 window.__PIER_SETTINGS__，
  // 第一帧就是对的主题。演示页没有后端，退回 localStorage。
  function initialTheme() {
    var saved = window.__PIER_SETTINGS__;
    if (saved && saved.theme) return saved.theme;
    try { return localStorage.getItem(THEME_KEY) || "system"; } catch (err) { return "system"; }
  }

  function useTheme() {
    var st = React.useState(initialTheme);
    var pref = st[0], setPref = st[1];
    var sys = React.useState(systemDark), sysDark = sys[0], setSysDark = sys[1];

    React.useEffect(function () {
      if (!window.matchMedia) return;
      var mq = window.matchMedia("(prefers-color-scheme: dark)");
      var on = function (ev) { setSysDark(ev.matches); };
      mq.addEventListener("change", on);
      return function () { mq.removeEventListener("change", on); };
    }, []);

    var set = function (v) {
      setPref(v);
      if (DEMO) {
        try { localStorage.setItem(THEME_KEY, v); } catch (err) { /* 隐私模式下写不进去，不影响使用 */ }
        return;
      }
      // 送一份补丁而不是一个位置参数：settings.json 里还有「SDK 管理」那一摊，
      // 而这一句只知道自己改了主题。再加设置项时不必再动这里。
      call("saveSettings", JSON.stringify({ theme: v }))
        .catch(function (ex) { console.error("Pier 界面：保存主题失败", ex); });
    };
    var dark = pref === "dark" || (pref === "system" && sysDark);
    return { pref: pref, set: set, dark: dark };
  }

  // 配色。
  //
  // 观感参考的是 bigmodel.cn 控制台：灰底、一块浮起来的白色圆角面板、
  // 发丝级的边框，主色是一种饱和度更高的蓝。
  //
  // 与上一版「一律不覆盖 antd」的取舍：那时出问题的是自造色值散落在 CSS 和
  // 各处内联样式里，等于在 antd 之外另立了一套互不协调的规范。现在改成
  // 只在这里定义一次，再通过 ConfigProvider 的 token / components 交给 antd——
  // 走的仍然是 antd 的入口，算法、派生色、暗色适配都还归它管。
  //
  // 这两个对象是全界面唯一的色值来源。app_test.go 会扫界面源码，
  // 除白名单外出现任何色值都会失败，所以新颜色只能加在这里。
  var PALETTE = {
    light: {
      primary: "#134CFF",
      text: "#131212",
      textSecondary: "#5E5E66",
      textTertiary: "#8D8E99",
      bgLayout: "#F3F4F6",
      bgContainer: "#FFFFFF",
      // 只管发丝线（卡片边、页头分隔线）。输入框和按钮的描边不在这里改，
      // 继续用 antd 默认那档——参考页的描边更淡，但淡到那个程度控件就找不着边了。
      //
      // 比 antd 默认的 #f0f0f0 深一档：主区是一块白面板，卡片也是纯白，
      // 卡片之间就只剩这条线可分。试过给卡片加浅底色，2% 的色差等于没做，
      // 起作用的始终是这条边。
      borderSecondary: "#E4E5EA",
      fill: "#F2F3F5",
      fillSelected: "#EBECF0",
      // 滚动条滑块。系统默认可视化滚动条是 15px 宽、还带一圈立体描边，
      // 在一片扁平留白里非常扎眼。色值由 app.css 的 ::-webkit-scrollbar 用；
      // 那边还会把它再收窄一半，见那边的说明。
      //
      // 静止这一档刻意压到几乎看不见，跟发丝线同档：滚动条是「需要时才被注意到」
      // 的东西，一直显示一根明确的灰条就是在跟内容抢注意力。要找回手感靠 hover，
      // 按下鼠标时它会明显一档。
      //
      // 反过来也不能再浅——浅色主题下唯一压深底的地方是日志区（.dc-log 恒为
      // #14161a），那里靠的就是这一档的浅灰显形。所以这个值同时受两头约束：
      // 白底上要隐得掉，深底上要看得见。往浅里调之前先想日志区。
      scrollThumb: "#E2E3E8",
      scrollThumbHover: "#BFC1CA",
      // 「服务占用对比」那两根条的填充色。刻意比主色浅、比主色灰：
      // 那条排行横跨一整行、每个服务占一行，是版面里面积最大的一块，
      // 用主色铺满等于让一个「顺带看一眼」的东西盖过按钮和状态点。
      // 它是量值不是强调，按「同一组数据用同一个色相」取蓝，只降饱和与明度。
      // 白底上对比度 4.7:1，够得着标记与底色 3:1 的下限。
      chartBar: "#3B6BE8"
    },
    dark: {
      // 暗色下主色要提亮一档：原色压在深底上会糊成一片。
      primary: "#3D6BFF",
      text: "#F2F3F5",
      textSecondary: "#A8A9B3",
      textTertiary: "#7C7D87",
      // 不跟着 antd 的暗色算法走纯黑，抬一档更像「面板」而不是「黑洞」。
      bgLayout: "#101114",
      bgContainer: "#17181D",
      borderSecondary: "#2E3037",
      fill: "#1F2026",
      fillSelected: "#26272E",
      // 与浅色同理：静止时贴着面板底色（bgContainer #17181D）只差一档，
      // 靠 hover 提亮来提示可拖。这里没有「深底浅底两头兼顾」的问题——
      // 深色下日志区反而是同一片深底，不用另外照顾。
      scrollThumb: "#2A2C33",
      scrollThumbHover: "#43464F",
      // 同浅色那一档的道理，只是深底上要反过来提亮：压在 #17181D 上是 5.1:1，
      // 与主色的距离比浅色下更明显，因为暗色里主色本身已经偏亮。
      chartBar: "#5B84EE"
    },
    // 日志区的终端配色（16 色，顺序同 ANSI 的 30–37、90–97）。
    // 日志区恒为深底（见 app.css 的 .dc-log），不跟着主题走，所以只有这一套，
    // 各色都按深底提亮过：Maven、Spring、npm 输出的 INFO / ERROR / SUCCESS 靠它区分。
    ansi: [
      "#7F848E", "#F07178", "#A6D189", "#E5C07B", "#6CB6FF", "#C792EA", "#56C2C8", "#D7DAE0",
      "#9AA0AA", "#FF8A93", "#C3E88D", "#FFD580", "#8ECBFF", "#DDA6F5", "#7FDBE3", "#FFFFFF"
    ],
    // 搜索命中的两档底色：所有命中一档，当前那一处再深一档、再加一圈。
    //
    // 只叠背景、不动前景：正文的颜色是日志自己带的（Maven 把 ERROR 写成红的），
    // 为了高亮把它统一换成一种颜色，恰好抹掉读日志时最要紧的那条线索。
    //
    // 两档都是**不透明**的实色，不用半透明。日志区恒为 #14161a（见 app.css 的
    // .dc-log），底下永远是同一个色，半透明买不到任何东西，只会让结果更贴近底色：
    // 原来那两档 18% / 46% 的琥珀压上去是 #3A352B 和 #746447，和底色只差一点点亮度、
    // 又没什么彩度，屏幕上看着就是「这儿好像有点不一样」——等于没高亮。
    //
    // 现在是「够彩、但压得住浅色字」的一块：日志正文的默认色 #d7dae0 在两块底上
    // 分别还有 7.2:1 与 5.2:1，白字 7.2:1，两块底各自和 #14161a 拉开了 1.8 倍与 2.5 倍
    // 的亮度。琥珀这个色相是选的：它和十六档 ANSI 里最常见的蓝（INFO）、绿、红都不同色，
    // 而灰阶底把彩度全让给了它，所以底色是中性的时候它一眼就能看出来。
    searchHit: "#5A3C10",
    searchHitOn: "#7A4E14",
    // 当前那一处的描边。两档底色的亮度差在满屏命中里还是不够「就这一处」，
    // 而描边不占位置、不换前景色，是能加在同一个片段上的第三层信息。
    searchHitEdge: "#E5C07B"
  };

  // 把日志里的 ANSI 转义序列变成带颜色的片段。
  //
  // Maven、Spring Boot、Vite 都会往输出里写 ESC[1;34m 这类终端颜色码。原样放进 <pre>
  // 就是满屏「⌧[1;34mINFO⌧[m」，比没有颜色更难读。这里只认 SGR（…m 结尾）里的
  // 前景色与粗体，其余控制序列（清行、光标移动）一律丢弃；背景色、256 色参数被跳过而不是
  // 误读成前景色。产出的是 React 元素，不拼 HTML，日志里有什么字符都不会被当成标签。
  //
  // 先切出「一段纯文本 + 它当时的颜色」（ansiRuns），再由 ansiSpans 渲染：
  // 搜索高亮要在这些片段的内部再切一刀，两件事各管一层。切完再分就晚了——
  // 高亮跨在两条颜色之间时，原样拼回去会把颜色弄丢。
  var ANSI_RE = /\x1b\[([0-9;?]*)([A-Za-z])/g;
  function ansiRuns(text) {
    var runs = [], fg = -1, bold = false, last = 0, m;
    var push = function (t, at) {
      if (!t) return;
      runs.push({ t: t, at: at, fg: fg, bold: bold });
    };
    ANSI_RE.lastIndex = 0;
    while ((m = ANSI_RE.exec(text))) {
      push(text.slice(last, m.index), last);
      last = ANSI_RE.lastIndex;
      if (m[2] !== "m") continue;
      var codes = m[1] === "" ? [0] : m[1].split(";").map(Number);
      for (var i = 0; i < codes.length; i++) {
        var c = codes[i];
        if (c === 0) { fg = -1; bold = false; }
        else if (c === 1) bold = true;
        else if (c === 22) bold = false;
        else if (c === 39) fg = -1;
        else if (c >= 30 && c <= 37) fg = c - 30;
        else if (c >= 90 && c <= 97) fg = c - 90 + 8;
        else if (c === 38 || c === 48) i += codes[i + 1] === 5 ? 2 : 4;
      }
    }
    push(text.slice(last), last);
    return runs;
  }

  // 一次最多标出多少处命中。
  //
  // 不是为了省内存，是为了省 DOM：搜一个「e」，两兆的日志里有十万处命中，
  // 高亮要把每个片段都切成元素，切出来的是十万个 span。到上限就停，
  // 界面上照实写「500+」，而不是悄悄少标几个。
  var MAX_HITS = 500;

  // findHits 找出 text 里所有匹配的位置（不分大小写），返回 [{start, end}]。
  function findHits(text, q) {
    var out = [];
    if (!q) return out;
    var low = text.toLowerCase(), ql = q.toLowerCase();
    for (var i = low.indexOf(ql); i >= 0; i = low.indexOf(ql, i + ql.length)) {
      out.push({ start: i, end: i + q.length });
      if (out.length >= MAX_HITS) break;
    }
    return out;
  }

  // ansiSpans 渲染日志正文：颜色来自 ANSI，命中处再叠一层底色。
  //
  // hits 是 findHits 的结果，current 是当前那一处（其余命中浅一档）。
  // 没有查询词时和以前完全一样：不带颜色的文本直接给字符串，
  // 只有上过色的片段才包 span——绝大多数行不产生任何元素。
  function ansiSpans(text, hits, current) {
    var out = [], key = 0;
    var runs = ansiRuns(text);
    for (var r = 0; r < runs.length; r++) {
      var run = runs[r], end = run.at + run.t.length, from = 0, pieces = [];
      for (var i = 0; hits && i < hits.length; i++) {
        if (hits[i].end <= run.at) continue;
        if (hits[i].start >= end) break;
        var s = Math.max(hits[i].start, run.at) - run.at;
        var en = Math.min(hits[i].end, end) - run.at;
        if (s > from) pieces.push({ t: run.t.slice(from, s), hit: -1 });
        pieces.push({ t: run.t.slice(s, en), hit: i });
        from = en;
      }
      if (from < run.t.length) pieces.push({ t: run.t.slice(from), hit: -1 });
      for (var j = 0; j < pieces.length; j++) {
        var p = pieces[j];
        if (!p.t) continue;
        if (run.fg < 0 && !run.bold && p.hit < 0) { out.push(p.t); continue; }
        var style = {
          color: run.fg >= 0 ? PALETTE.ansi[run.fg] : undefined,
          fontWeight: run.bold ? 600 : undefined,
          background: p.hit < 0 ? undefined
            : (p.hit === current ? PALETTE.searchHitOn : PALETTE.searchHit),
          // 描边用 inset 阴影画，不用 border：border 会把这一段的每个字往外推 1px，
          // 一行里命中与没命中就对不齐了，而等宽正文里错开一个像素比没有高亮更难读。
          boxShadow: p.hit === current ? "inset 0 0 0 1px " + PALETTE.searchHitEdge : undefined
        };
        // data-dc-hit 只挂在当前那一处：跳转时要找的就是它。
        out.push(e("span", { key: key++, style: style, "data-dc-hit": p.hit === current ? "1" : undefined },
          p.t));
      }
    }
    return out;
  }

  function themeConfig(dark) {
    var p = dark ? PALETTE.dark : PALETTE.light;
    return {
      algorithm: dark ? A.theme.darkAlgorithm : A.theme.defaultAlgorithm,
      token: {
        colorPrimary: p.primary,
        colorText: p.text,
        colorTextSecondary: p.textSecondary,
        colorTextTertiary: p.textTertiary,
        colorBgLayout: p.bgLayout,
        colorBgContainer: p.bgContainer,
        colorBorderSecondary: p.borderSecondary,
        // 圆角比 antd 默认略大一档：控件 6、卡片 8、面板与弹窗 12。
        borderRadius: 6,
        borderRadiusLG: 8,
        borderRadiusSM: 4,
        // 滚动条滑块的两个颜色。antd 不认识这两个键，但会把它们原样并进
        // useToken() 的返回值里——借这条通道把 PALETTE 的色值递到运行期，
        // 再由 app.js 写成 CSS 自定义属性给 ::-webkit-scrollbar 用。
        // 走 token 而不是在 app.js 里另取一次 PALETTE，是为了让「当前是深是浅」
        // 只有一个来源：这里拿到的就是 ConfigProvider 真正生效的那一套。
        dcScrollThumb: p.scrollThumb,
        dcScrollThumbHover: p.scrollThumbHover,
        // 同理，把「服务占用对比」的填充色也递到运行期，让界面不必自己判深浅。
        dcChartBar: p.chartBar,
        // 系统字体栈。antd 默认那份在 macOS 上会先命中 Helvetica，
        // 中文回退得比 -apple-system 早，字形不如苹方稳定。
        fontFamily: '-apple-system, BlinkMacSystemFont, "PingFang SC", ' +
          '"Helvetica Neue", "Microsoft YaHei", sans-serif'
      },
      components: {
        // 侧栏透明，让页面底色直接透过去——这一版整页只有一块「白」，
        // 就是右边的主面板。
        Layout: { siderBg: "transparent", bodyBg: p.bgLayout, headerBg: "transparent" },
        // 导航项 36px 高、圆角 8，选中态只换底色不换字色。
        Menu: {
          itemHeight: 36,
          itemBorderRadius: 8,
          itemMarginInline: 0,
          itemMarginBlock: 2,
          itemPaddingInline: 8,
          itemColor: p.text,
          itemHoverBg: p.fill,
          itemSelectedBg: p.fillSelected,
          itemSelectedColor: p.text,
          subMenuItemBg: "transparent",
          groupTitleColor: p.textTertiary,
          activeBarWidth: 0,
          groupTitleFontSize: 12,
          iconMarginInlineEnd: 10
        },
        Segmented: { borderRadius: 8, borderRadiusSM: 6, itemSelectedBg: p.bgContainer },
        // antd 默认给按钮垫一道 0 2px 0 的投影，是它那套「拟物」遗留。
        // 扁平的发丝线骨架里它会让每个按钮底下都多一条灰边，一排按钮看着像贴上去的。
        Button: { primaryShadow: "none", defaultShadow: "none", dangerShadow: "none", fontWeight: 500 },
        Tag: { defaultBg: p.fill },
        Modal: { borderRadiusLG: 12 },
        // antd 默认每栏下边距 24、标签下留 8，再叠上每栏底下那行说明，一屏只放得下四五栏，
        // 表单显得松散。收到 16 / 6，说明文字另在 app.css 里压小一号。
        Form: { itemMarginBottom: 16, verticalLabelPadding: "0 0 6px" }
      }
    };
  }

  // ── 概览 ─────────────────────────────────────────────────────────────────

  // 状态点的实际颜色。TONE 给的是 antd 的语义名，自绘的圆点要落到具体色上，
  // 这里统一从 token 取，不另立一套。
  function toneColor(token, key) {
    var t = tone(key);
    if (t === "success") return token.colorSuccess;
    if (t === "warning") return token.colorWarning;
    if (t === "error") return token.colorError;
    return token.colorTextQuaternary;
  }

  // 「需要你看一眼」的服务：上次退出异常、端口被别人占着、或者上一个动作失败了。
  //
  // 健康探针没过**不算**在这里面。那个桶在界面上叫「异常」，副标题写的是
  // 「异常退出或端口冲突」；探针没探通的服务其实在好好跑着，把它塞进去会让
  // 那一格数字变成两种意思，而它真正要人去做的只有一件事——看一眼地址对不对。
  // 所以这一条只落在那一行的说明里（见 ServiceRow 的 warn 位）。
  function needsAttention(svc) {
    return svc.statusKey === "stale" || svc.statusKey === "external" || !!svc.opErr;
  }

  // 按状态数一遍。只是计数，不碰任何资源数字——那些仍然只来自后端。
  function tally(list) {
    var out = { running: 0, starting: 0, attention: 0, idle: 0 };
    list.forEach(function (svc) {
      if (needsAttention(svc)) out.attention++;
      else if (svc.statusKey === "running") out.running++;
      else if (svc.statusKey === "starting") out.starting++;
      else out.idle++;
    });
    return out;
  }

  // 运行中的圆点带一圈缓慢外扩的光晕，其余状态是实心点。
  // 光晕只给「活着」的服务：一屏里只有它们在动，扫一眼就知道哪些在跑。
  function Dot(props) {
    var token = A.theme.useToken().token;
    return html`<span className="dc-dot" data-live=${props.live ? "1" : null}
      title=${props.title || null}
      style=${{ color: toneColor(token, props.status) }}/>`;
  }

  // 侧栏分组那颗三态点旁边一个字都没有：颜色是它唯一的区别，色觉障碍的用户
  // 和读屏都拿不到它说的是什么。服务行里的那颗点不用这个——那里紧挨着
  // 「运行中」「已退出」，颜色只是把那行字又描了一遍。
  function groupStatusText(k) {
    if (k === "stale") return "有服务需要关注";
    if (k === "running") return "有服务在运行";
    return "全部已停止";
  }

  function Stat(props) {
    var token = A.theme.useToken().token;
    return html`<div className="dc-stat">
      <div className="dc-stat-label" style=${{ fontSize: token.fontSizeSM }}>
        ${props.icon ? e(props.icon, { size: 13 }) : null}${props.label}
      </div>
      <div className="dc-stat-value"
        style=${{ fontSize: token.fontSizeHeading3, color: props.color || token.colorText }}>
        ${props.value}
        ${props.unit ? html`<span className="dc-stat-unit"
          style=${{ fontSize: token.fontSize, color: token.colorTextTertiary }}>${props.unit}</span>` : null}
      </div>
      ${"" /* 副标题是一行省略号，而这一格里被截掉的往往正是「是哪几个服务」——
             补一个 title，悬停能把全句读全。 */}
      <div className="dc-stat-sub" title=${typeof props.sub === "string" ? props.sub : null}
        style=${{ fontSize: token.fontSizeSM, color: token.colorTextTertiary }}>
        ${props.sub}
      </div>
      ${props.children}
    </div>`;
  }

  // 占比：个位数留一位小数，两位数取整。「0.4%」比「0%」有用，
  // 而「67%」也没必要写成「66.7%」。只出现在悬停提示里，不占版面。
  function fmtShare(pct) {
    if (!isNum(pct) || pct <= 0) return "0%";
    return (pct < 10 ? Math.round(pct * 10) / 10 : Math.round(pct)) + "%";
  }

  // 各服务的资源占用对比：概览那排格子下面的一整条。
  //
  // 内存和 CPU 各占一列，都摆着，不做成「二选一切换」：它俩问的是两个问题——
  // 谁占得多、谁在烧——切一次就只能看见一个，而切过去的那个还往往是全 0
  // （一组刚起来的服务就是这样），看着像功能没做。两列并排，「这组没人吃 CPU」
  // 和「内存都堆在 demo-web 上」是同一眼看完的。
  //
  // 两列各按各的最大值为满格，都是排行榜：条长回答「谁比谁大多少」，要绝对值看
  // 右边的数。它不是百分比条，满格只说明「这个最大」；两列之间也不能互相量——
  // 能横向比的是同一列里的上下两行。
  //
  // 行的顺序固定按内存排，不跟着 CPU 变：CPU 是每两秒刷一次的瞬时值，拿它排序，
  // 名单会跟着刷新一跳一跳（侧栏那个「…」的毛病同此，见 navLabel）。要一眼找出
  // CPU 的头一名，看那条最长的 CPU 条就够了。
  //
  // 只列真正在跑的服务：没起来的没有用量，摆一排零长的空条只会把「谁在吃」
  // 淹没在噪声里。一个都没有时整条不渲染，上方那格总数为 0 已经说完了。
  //
  // 版面上克制到什么程度，是这一条最主要的取舍。它横跨一整行、每个服务占一行，
  // 服务一多就是版面里面积最大的一块；如果每条都用主色铺满，整页最亮的东西
  // 会是一个「顺带看一眼」的排行。所以：
  //   - 填充用比主色低一档的蓝（PALETTE 的 chartBar），不跟按钮抢；
  //   - 条高 6px，数据那一端圆角、基线那一端方角——从同一条基线长出来，
  //     上下几行的起点才连成一条线；
  //   - 数字紧贴自己那根条，中间只隔 8px，两根条之间空 28px：
  //     读到哪个数属于哪一列，靠的是距离，不是靠对齐猜；
  //   - 表头给出每列的最大值。条是按本列最大值归一化的，不给参照的话
  //     「满格」只是个相对说法，说不了这一列到底有多大。
  function UsageChart(props) {
    var token = A.theme.useToken().token;

    var rows = [];
    props.services.forEach(function (svc) {
      var u = svc.usage || {};
      if (!svc.running || !(u.procs > 0)) return;
      rows.push({ name: svc.name, mem: u.memBytes || 0, cpu: u.cpu || 0 });
    });
    if (!rows.length) return null;

    var totalMem = rows.reduce(function (a, r) { return a + r.mem; }, 0);
    var totalCPU = rows.reduce(function (a, r) { return a + r.cpu; }, 0);
    var topMem = 0, topCPU = 0;
    rows.forEach(function (r) {
      topMem = Math.max(topMem, r.mem);
      topCPU = Math.max(topCPU, r.cpu);
    });
    rows.sort(function (a, b) { return b.mem - a.mem; });

    // 一条「读数」：轨道 + 数字。数字用等宽数字，一列扫下来位次对得齐。
    var cell = function (v, top, text, key) {
      return html`<span className="dc-chart-cell" key=${key}>
        <span className="dc-chart-track" style=${{ background: token.colorFillTertiary }}>
          <span className="dc-chart-fill"
            style=${{ width: (top > 0 ? v / top * 100 : 0) + "%", background: token.dcChartBar }}/>
        </span>
        <span className="dc-chart-val" style=${{ fontSize: token.fontSizeSM,
          color: token.colorTextSecondary }}>${text}</span>
      </span>`;
    };

    var head = function (label, top, text) {
      return html`<span className="dc-chart-cell">
        <span className="dc-chart-col" style=${{ fontSize: token.fontSizeSM,
          color: token.colorTextTertiary }}>${label}</span>
        <span className="dc-chart-col dc-chart-val" style=${{ fontSize: token.fontSizeSM,
          color: token.colorTextTertiary }}>最大 ${text}</span>
      </span>`;
    };

    return html`<div className="dc-stat dc-chart">
      <div className="dc-chart-rows">
        <div className="dc-chart-row dc-chart-head">
          <span className="dc-chart-label" style=${{ fontSize: token.fontSizeSM,
            color: token.colorTextSecondary }}>服务占用对比</span>
          ${head("内存", topMem, fmtMem(topMem))}
          ${head("CPU", topCPU, fmtCPU(topCPU))}
        </div>
        ${rows.map(function (r) {
          // 悬停里补两个「占本页合计」的百分比：条长只说了「谁比谁大多少」，
          // 占合计多少是另一回事，两个指标各给一份，不厚此薄彼。
          var title = r.name + "：" + fmtMem(r.mem) + " · " + fmtCPU(r.cpu) + " CPU" +
            (totalMem > 0 || totalCPU > 0
              ? "，占本页合计 内存 " + fmtShare(totalMem > 0 ? r.mem / totalMem * 100 : 0) +
                " · CPU " + fmtShare(totalCPU > 0 ? r.cpu / totalCPU * 100 : 0)
              : "");
          return html`<div className="dc-chart-row" key=${r.name} title=${title}>
            <span className="dc-chart-name">${r.name}</span>
            ${cell(r.mem, topMem, fmtMem(r.mem), "m")}
            ${cell(r.cpu, topCPU, fmtCPU(r.cpu), "c")}
          </div>`;
        })}
      </div>
    </div>`;
  }

  // 首页顶部的一排概览。
  //
  // 它回答两个问题：一是「我的服务现在怎么样」，二是机器变卡的时候，
  // 是 Pier 这个面板本身占的，还是它起的服务占的。所以后两格是「服务占用」和
  // 「Pier 自身」并排，一眼能比出高下。不摆整机的数：那是活动监视器的事，
  // 而且全机 RSS 相加把共享库重复计入了几百遍，和这两格根本不是一个口径。
  //
  // 「服务占用」跟着当前这一页走：全部服务页是全部服务的合计，分组页是这个分组的。
  // 同一个数字在屏幕上只出现一遍——分组页上，分组标题那一行就不再重复它（见列表那段）。
  //
  // 数据全部来自后端：资源数字按进程组求和，界面上不做任何推算。
  function Overview(props) {
    var data = props.data, list = props.services;
    var self = data.self || {};
    var usage = props.usage || {};
    var token = A.theme.useToken().token;
    var c = tally(list);
    var n = list.length || 1;

    var bar = [
      { k: "running", v: c.running, color: token.colorSuccess, label: "运行中" },
      { k: "starting", v: c.starting, color: token.colorWarning, label: "启动中" },
      { k: "attention", v: c.attention, color: token.colorError, label: "异常" }
    ];
    var flagged = list.filter(needsAttention).map(function (x) { return x.name; });

    return html`<div className="dc-overview">
      <div className="dc-stats">
        <${Stat} icon=${IconLayers} label="服务状态" value=${c.running + c.starting}
          unit=${"/ " + list.length + " 在跑"}
          sub=${bar.filter(function (b) { return b.v > 0; }).map(function (b) {
            return b.v + " " + b.label; }).join(" · ") || "全部未启动"}>
          <div className="dc-bar" style=${{ background: token.colorFillSecondary }}>
            ${bar.map(function (b) {
              return b.v ? html`<span key=${b.k}
                style=${{ width: (b.v / n * 100) + "%", background: b.color }}/>` : null;
            })}
          </div>
        <//>
        <${Stat} icon=${IconAlert} label="需要关注" value=${flagged.length}
          color=${flagged.length ? token.colorError : null}
          unit=${flagged.length ? "个服务" : "一切正常"}
          sub=${flagged.length ? flagged.join("、") : "没有异常退出或端口冲突"}/>
        ${data.metricsError ? html`<div className="dc-stat dc-stat-wide">
          <${A.Alert} type="warning" showIcon message="读不到资源占用"
            description=${data.metricsError}/>
        </div>` : html`<${React.Fragment}>
          ${"" /* 数值那一格放 CPU、副行放内存与进程数：CPU 是会跳的那个数，
                 「现在谁在吃」看它；内存只比相对大小（见下面那句说明），降一档。 */}
          <${Stat} icon=${IconPulse} label=${props.scope === "all" ? "服务占用" : "本组占用"}
            value=${fmtCPU(usage.cpu)} unit="CPU"
            sub=${usage.procs ? fmtMem(usage.memBytes) + " · " + usage.procs + " 个进程" : "没有在跑的进程"}/>
          <${Stat} icon=${IconGauge} label="Pier 自身" value=${fmtCPU(self.cpu)} unit="CPU"
            sub=${fmtMem(self.memBytes) + " · " + (self.procs || 0) + " 个进程"}/>
        <//>`}
        ${"" /* 对比条排在四格下面、同一张卡里：它是「服务占用」那一格的展开，
               拆成第二张卡就成了另起一件事。读不到用量时整条不出现（上面的告警已经说了）。 */}
        ${data.metricsError ? null : html`<${UsageChart} services=${list}/>`}
      </div>
      ${"" /* 这句话必须留着，而且不能挪进 tooltip：「Pier 自身」只数得到 Pier 自己的进程。
             界面用的 WebView 是系统托管的 XPC 服务（父进程是 launchd、各自独占会话），
             实测 Pier 进程 68MB 时它那几个渲染进程加起来还有 148MB——
             不点破这一点，这一格会被当成面板的全部开销，正好少算了大头。
             内存那半句同理：各进程 RSS 之和会把共享的库重复计入，不说就会被当成「一共吃了多少」。 */}
      ${data.metricsError ? null : html`<div className="dc-footnote"
        style=${{ fontSize: token.fontSizeSM, color: token.colorTextQuaternary }}>
        内存是各进程常驻内存之和，共享的库会重复计入，只宜比大小；「Pier 自身」不含系统托管的 WebView 渲染进程。
      </div>`}
    </div>`;
  }

  // ── 服务行 ───────────────────────────────────────────────────────────────

  // 一个服务一行，而不是一张卡片：卡片之间的留白和描边在服务一多时
  // 会占掉一半的屏幕，而这里要的是「一屏扫完所有服务的状态」。
  // 列宽固定、数字等宽，上下两行的端口和资源能对齐着看。
  function ServiceRow(props) {
    var s = props.svc, act = props.act;
    var token = A.theme.useToken().token;
    var busy = !!s.op;
    var live = s.statusKey === "running" || s.statusKey === "starting";

    // 后端用 "-" 表示「这项没有」，而 "-" 在 JS 里是真值——早先直接
    // 用真值判断，未启动的服务于是渲染成了「运行 -」。一律先过 hasVal。
    var ids = [];
    if (s.pid > 0) ids.push("PID " + s.pid);
    if (hasVal(s.uptime)) ids.push("运行 " + s.uptime);

    // 资源占用只对在跑的服务有意义，而且只跟在后端报「运行中」时取。
    //
    // 数字为 0 也照常显示，不做「0 就省略」：省略之后，一个空闲的服务和
    // 一个没测到的服务在列表上长得一模一样，而这两件事不一样。
    //
    // 采样整体失败（metricsError 非空）时反倒要整段不显示。这时后端给的是
    // 全 0，而 0 在这一行上的意思是「什么都不占」——比不显示更误导。
    //
    // 进程数不是可有可无的凑数：一个 Go 服务是一条进程，一个 Maven 起的
    // Java 服务是一棵树，只报「CPU 1.2%」看不出后面这种，而 Pier 记的那个
    // PID 只是树根，正是最容易让人以为「一个应用就一个进程」的地方。
    var usage = s.running && !props.metricsErr && s.usage
      ? "CPU " + fmtCPU(s.usage.cpu) + " · " + fmtMem(s.usage.memBytes) + " · " +
        (s.usage.procs || 0) + " 进程"
      : "";

    // 这个服务实际会用哪套 SDK，跟在类型标签后面：类型标签已经说了「Java」，
    // 「Java 21 · Temurin」才把「哪个 Java」补齐——版本不对导致的编译失败，
    // 十次里有九次是一次都没打开过表单就点下去的。
    //
    // 一行里摆不下全部理由与警告，压进 title：它们平时是噪音，
    // 只有真出问题的时候才有人要找。警告用警示色加一个小标记，不能也压进去。
    //
    // title 里给的是 bin 而不是 home：那才是这次真正会跑的可执行文件，而且
    // 各类别的 home 含义并不一致（Java 是 JAVA_HOME，Node/Python 是要前置到 PATH
    // 的那个 bin 目录，见 internal/toolchain 的 tool()）。
    var rt = (s.runtimes || []).filter(function (r) { return hasVal(r.label) || hasVal(r.error); });
    var rtText = rt.map(function (r) { return hasVal(r.label) ? r.label : r.error; }).join(" · ");
    var rtTip = rt.map(function (r) {
      if (hasVal(r.error)) return r.error;
      return r.label + "（" + [r.reason, r.warn].filter(hasVal).join("；") + "）\n" + (r.bin || r.home);
    }).join("\n");
    var rtWarn = rt.some(function (r) { return hasVal(r.warn) || hasVal(r.error); });

    // 第二行的说明：状态、健康、占用者、备注，按重要程度排。每一项是
    // { t: 文字, warn: 要不要用警示色 }。
    //
    // 健康探针只在自己这一路不顺的时候出声。探通了就是「运行中」的默认样子，
    // 绿点加「运行中」已经说完，每个健康服务都挂一句「健康检查通过」只会把
    // 真正不一样的那几行淹掉。不顺有两种：还在等（编译型服务起来后要初始化
    // 一两分钟，正常），和等满了窗口还没过——那时服务其实在好好跑着，
    // 出问题的是探针这一路（地址填错，或者这个服务压根没有健康接口）。
    // 后一种挑警示色，否则它会安安静静地一直跑下去，没人去想当初那个地址
    // 是不是猜的。措辞由后端的 view.NoteText 给，这里不另写一份。
    var bits = [];
    // 还在等探针的这一段只留一句。后端的说明位这时写的是「尚未通过健康探针」，
    // 和「等待健康检查」是同一件事的两种说法，并排摆着读起来像卡了两下；
    // 留「等待健康检查」，它说的是在等什么，比「还没通过」多一个字的信息。
    var waitingHealth = s.hasHealth && s.statusKey === "starting" && !s.probeExpired;
    if (waitingHealth) bits.push({ t: "等待健康检查" });
    // 端口被自己占着是应该的（服务正跑着），不说成「被…占用」——那句话读起来
    // 像是有人抢了端口。真被别人占了才报，是别的 Pier 服务就直接说服务名：
    // 它派生的子进程在系统里叫 node / java，光看那个名字认不出是谁。
    if (s.occupant && isNum(s.occupant.pid) && s.occupant.service !== s.name) {
      var holder = s.occupant.service || s.occupant.command;
      bits.push({ t: "被 " + [holder, "PID " + s.occupant.pid].filter(Boolean).join(" · ") + " 占用" });
    }
    // 自动重启过就要说出来。一个崩了又被拉起来的服务，在界面上和「一直好好跑着」
    // 长得一模一样，而这两件事要看的程度差得远；到顶之后「不救了」更是只能从
    // 这一句里看出来。措辞由后端的 restartNote 给，这里不另写一份。
    if (hasVal(s.restartNote)) bits.push({ t: s.restartNote, warn: true });
    if (hasVal(s.userNote)) bits.push({ t: s.userNote });
    else if (hasVal(s.note) && !waitingHealth) bits.push({ t: s.note, warn: !!s.probeExpired });

    var menu = {
      items: [
        { key: "logs", label: "查看日志", icon: e(IconDoc) },
        { key: "dir", label: "在" + FILEMGR + "中打开目录", icon: e(IconFolder) },
        // 不带这条服务的运行状态、也不写盘，只把清单里的定义抄成一段 YAML，
        // 所以谁都能点：只读清单下「复制出去贴到别处」正是它唯一的用处。
        { key: "yaml", label: "复制成 YAML", icon: e(IconDoc) },
        s.hasHealth ? { key: "health", label: "打开健康检查地址", icon: e(IconHeart) } : null,
        // 探针没过的时候，最该做的动作是「地址写错了」或者「这个服务没有健康
        // 接口」——两种都不该改服务本身，关掉探针就好。摆在最显眼的位置，
        // 因为一个探不进去的服务会一直顶着「健康探针未通过」，不点它没法收场。
        s.hasHealth && s.probeExpired && s.editable
          ? { key: "clearHealth", label: "不再检查健康", icon: e(IconHeart), disabled: busy } : null,
        // 命令行指定的 YAML 清单是只读的，那时收起编辑与删除。
        // 有操作在跑时只收起会改动它的两项；看日志、开目录任何时候都要能点——
        // 编译、启动那几十秒恰恰是最需要盯着日志的时候。
        s.editable ? { key: "edit", label: "编辑这个应用", icon: e(IconEdit), disabled: busy } : null,
        // 复制出来的是另一条服务，跟这一条在不在跑无关，所以 busy 时才收起。
        s.editable ? { key: "duplicate", label: "复制一份", icon: e(IconCopy), disabled: busy } : null,
        s.editable ? { type: "divider" } : null,
        s.editable ? { key: "delete", label: "删除这个应用", icon: e(IconTrash), danger: true, disabled: busy } : null
      ].filter(Boolean),
      onClick: function (mi) { act(mi.key, s); }
    };

    // 动作区按状态给不同的主按钮：能启动的时候不该先让人去点停止。
    // 「启动」用浅底的主色而不是实心蓝：一列里七八个实心蓝按钮会抢走
    // 整屏的注意力，真正该跳出来的是右上角那个「添加应用」。
    //
    // 不管在什么阶段都能停：排队、编译、等待就绪时主按钮就是「停止」，点了会打断那次启动。
    // 只有已经在停止中，才换成转圈的「停止中」。进度文案挪到名字下面那行（见 dc-row-op）。
    var stopping = busy && s.opKind === "stop";
    var primary;
    if (stopping) {
      primary = html`<${A.Button} loading disabled>${s.op}<//>`;
    } else if (busy || live) {
      primary = html`<${A.Button} icon=${e(IconStop)}
        onClick=${function () { act("stop", s); }}>停止<//>`;
    } else if (s.statusKey === "external") {
      primary = html`<${A.Button} color="danger" variant="filled"
        onClick=${function () { act("occupant", s); }}>端口被占<//>`;
    } else {
      primary = html`<${A.Button} color="primary" variant="filled" icon=${e(IconPlay)}
        onClick=${function () { act("start", s); }}>启动<//>`;
    }

    var sub = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };

    // 拖动排序。draggable 只在这一行上开，落点那条线画在行自己的上下边缘
    // （见 app.css 的 dc-row-drop-*），不插占位元素——插一个进去会让整列
    // 往下一跳，鼠标底下的行跟着动，落点就飘了。
    var dnd = props.dnd;
    var cls = "dc-row"
      + (dnd && dnd.moving === s.name ? " is-dragging" : "")
      + (dnd && dnd.target && dnd.target.name === s.name
        ? (dnd.target.after ? " dc-row-drop-after" : " dc-row-drop-before") : "");

    return html`<div className=${cls} draggable=${!!dnd}
      onDragStart=${dnd ? function (ev) { dnd.start(s, ev); } : null}
      onDragOver=${dnd ? function (ev) { dnd.over(s, ev); } : null}
      onDrop=${dnd ? function (ev) { dnd.drop(s, ev); } : null}
      onDragEnd=${dnd ? dnd.end : null}>
      <div className="dc-row-main">
        <${Dot} status=${s.statusKey} live=${s.statusKey === "running"}/>
        <div className="dc-row-text">
          <div className="dc-row-title">
            <span className="dc-row-name" title=${s.name}>${s.name}</span>
            <span className="dc-kind" style=${{ fontSize: token.fontSizeSM,
              color: token.colorTextSecondary, background: token.colorFillTertiary }}>
              ${KIND_LABEL[s.kind] || s.kind}</span>
            ${rtText ? html`<span className="dc-runtime" title=${rtTip}
              style=${{ fontSize: token.fontSizeSM,
                color: rtWarn ? token.colorWarning : token.colorTextTertiary }}>
              ${rtWarn ? e(IconAlert, { size: 11 }) : null}${rtText}</span>` : null}
          </div>
          <div className="dc-row-sub" style=${sub}>
            ${busy && !stopping ? html`<span className="dc-row-op" style=${{ color: token.colorPrimary }}>
              <span className="dc-spin"/>${s.op}</span>`
              : html`<span style=${{ color: toneColor(token, s.statusKey), fontWeight: 500 }}>${s.statusText}</span>`}
            ${bits.map(function (b, i) { return html`<span key=${i} className="dc-row-bit"
              title=${b.t} style=${b.warn ? { color: token.colorWarning } : null}>${b.t}</span>`; })}
          </div>
          <div className="dc-row-inline" style=${sub}>
            ${[s.port > 0 ? ":" + s.port : ""].concat(ids, usage ? [usage] : [])
              .filter(Boolean).join(" · ")}
          </div>
          ${"" /* 失败原因后面那串「详见 /…/xxx.log」换成一个能点的「查看日志」：
                 路径在这里既看不全也点不动，用户要的是日志内容本身。 */}
          ${s.opErr ? html`<div className="dc-row-err" style=${{ fontSize: token.fontSizeSM,
            color: token.colorError }}>${e(IconAlert, { size: 12 })}
            <span>${s.opErr.replace(/[，,]\s*详见\s+\S+\s*$/, "")}</span>
            <${A.Button} type="link" className="dc-row-err-link"
              onClick=${function () { act("logs", s); }}>查看日志<//></div>` : null}
        </div>
      </div>

      <div className="dc-row-meta">
        <div className="dc-row-ids">
          ${s.port > 0 ? html`<span className="dc-mono dc-port">:${s.port}</span>`
            : html`<span style=${{ color: token.colorTextQuaternary }}>无端口</span>`}
          ${ids.length ? html`<span style=${sub}>${ids.join(" · ")}</span>` : null}
        </div>
        <div style=${sub}>${usage || (live || s.statusKey === "external" ? "" : "未运行")}</div>
      </div>

      <div className="dc-row-actions">
        ${primary}
        ${"" /* 这三颗只有图标。名字只补 aria-label 不补 title 的那两处：
                 antd 的 Tooltip 已经给了悬停提示，再挂一个原生 title 会两个一起冒出来。 */}
        ${busy ? html`<${A.Tooltip} title="查看日志">
          <${A.Button} type="text" icon=${e(IconDoc)} aria-label=${"查看 " + s.name + " 的日志"}
            onClick=${function () { act("logs", s); }}/>
        <//>` : live ? html`<${A.Tooltip} title="重启">
          <${A.Button} type="text" icon=${e(IconRestart)} aria-label=${"重启 " + s.name}
            onClick=${function () { act("restart", s); }}/>
        <//>` : null}
        <${A.Dropdown} menu=${menu} trigger=${["click"]} placement="bottomRight">
          <${A.Button} type="text" icon=${e(IconMore)}
            title="更多操作" aria-label=${s.name + " 的更多操作"}/>
        <//>
      </div>
    </div>`;
  }

  // ── 端口占用详情 ─────────────────────────────────────────────────────────

  function PortOwnerModal(props) {
    var open = props.open, onClose = props.onClose;
    // 变量名刻意叫 poData：app.js 里每一种后端返回都有自己的名字
    // （状态是 data、端口候选是 cand、日志是 logData），这样
    // TestUIFieldNamesExistInBackend 才能按名字精确核对字段，
    // 而不是被一堆重名的 data 搅成一锅粥。
    var st = React.useState(null), poData = st[0], setData = st[1];
    var ls = React.useState(true), loading = ls[0], setLoading = ls[1];
    var es = React.useState(""), err = es[0], setErr = es[1];
    var ks = React.useState(false), killing = ks[0], setKilling = ks[1];
    var name = props.name;

    React.useEffect(function () {
      if (!open || !name) return;
      setLoading(true); setErr(""); setData(null);
      call("portOwner", name).then(function (r) {
        setData(r); setLoading(false);
      }).catch(function (ex) {
        setErr(ex.message); setLoading(false);
      });
    }, [open, name]);

    var owner = poData && poData.owner;

    // 字段读不出来时要说清楚，而不是渲染出一排「—」和一个 undefined 的按钮。
    // 这正是「后端查得到、界面显示不出来」那类问题的样子。
    var broken = owner && (!isNum(owner.pid) || owner.pid <= 0);
    if (broken) {
      console.error("端口占用详情：owner.pid 读不出来，界面与后端的字段名可能对不上。owner =", owner);
    }

    var doKill = async function () {
      setKilling(true);
      try {
        var r = await call("killPortOwner", name, String(owner.pid), owner.started || "");
        props.message.success(r.msg || "已结束");
        props.onDone();
        onClose();
      } catch (ex) {
        props.message.error(ex.message);
        setKilling(false);
      }
    };

    var footer = [html`<${A.Button} key="c" onClick=${onClose}>关闭<//>`];
    if (owner && !broken) {
      if (owner.managed) {
        footer.push(html`<${A.Button} key="s" type="primary" danger
          onClick=${function () { onClose(); props.onStopService(owner.service); }}>
          停止服务 ${owner.service}<//>`);
      } else {
        footer.push(html`<${A.Button} key="k" danger type="primary"
          loading=${killing} onClick=${doKill}>结束进程 ${owner.pid}<//>`);
      }
    }

    // 「谁把它拉起来的」。链上认出来的那几个从近到远连着写（近的在前），
    // 与命令行 `pier ports` 那一列是同一个说法。这一行直接决定下一步该做什么：
    // 自己刚在编辑器里起的，去编辑器里关；别人起的才轮到下面那颗「结束进程」。
    //
    // 认不出来时不留空行：摆一句「认不出来」，它说的是「这条链路上没有认得出的
    // 宿主」——那是个真答案，和「这一栏没查」不是一回事。
    var originText = "认不出来";
    if (owner && owner.origin && (owner.origin.chain || []).length) {
      originText = owner.origin.chain.join(" ← ");
    }

    var rows = [];
    if (owner && !broken) {
      rows = [
        { key: "port", label: "端口", children: String(pick(poData, "port")) },
        { key: "cmd", label: "进程名", children: pick(owner, "command") },
        { key: "pid", label: "PID", children: String(owner.pid) },
        { key: "origin", label: "启动来源", children: originText },
        { key: "user", label: "属主", children: pick(owner, "user") },
        { key: "start", label: "启动于", children: pick(owner, "started") },
        { key: "args", label: "命令行", children: html`<${A.Typography.Text} code
          style=${{ wordBreak: "break-all" }}>${pick(owner, "args")}<//>` }
      ];
    }

    return html`<${A.Modal} open=${open} onCancel=${onClose} footer=${footer} width=${SIZE.modalOwner}
      styles=${{ body: BODY_SCROLL }}
      title="端口占用详情">
      ${loading ? html`<${A.Skeleton} active paragraph=${{ rows: 4 }}/>` : null}
      ${err ? html`<${A.Alert} type="info" showIcon message=${err}/>` : null}
      ${broken ? html`<${A.Alert} type="error" showIcon message="读不出占用进程的信息"
        description=${"后端返回的 owner 里没有可用的 pid。这通常是界面与后端的字段名对不上，请把这条报给开发者；控制台里有原始返回。"}/>` : null}
      ${owner && !broken ? html`<${React.Fragment}>
        ${owner.managed ? html`<${A.Alert} type="warning" showIcon style=${{ marginBottom: 14 }}
          message=${"PID " + owner.pid + " 是 Pier 自己启动的服务「" + owner.service + "」"}
          description="这种情况应该用「停止」，直接结束进程会让状态文件里留下一条指向已死进程的假记录。"/>`
        : html`<${A.Alert} type="warning" showIcon style=${{ marginBottom: 14 }}
          message="结束进程是不可撤销的"
          description="请先确认下面这个进程确实是要清掉的那个。如果它是你在另一个终端里正调试的服务，结束它会让那边的会话中断。"/>`}
        ${"" /* 标签列宽度要装得下最长的那一条（「启动来源」四个字）。antd 给标签格
                左右各留 24px，剩下的才是文字的：92 只够三个字，第四个会折到下一行，
                一列里独独那一格变成两行高。 */}
        <${A.Descriptions} column=${1} bordered
          styles=${{ label: { width: 112 } }} items=${rows}/>
      <//>` : null}
    <//>`;
  }

  // ── 扫描本机端口 ─────────────────────────────────────────────────────────

  // 这一屏回答「本机此刻开着哪些端口、各自是谁」，用处是把已经在跑、清单里
  // 却没有的那个服务收进来。那种情况下用户手上只有一条命令行，而清单要的是
  // 目录、类型、启动命令、端口四样——这四样恰好都能从那个进程身上问出来。
  //
  // 「纳管」不在这一屏写清单：后端那一步只做一次预演（不落盘），把结果当作
  // 表单的预填值交出去，用户看一眼、改一改、点了保存才算数。从目录推出来的
  // 东西只对「一个目录一个服务」的项目成立，直接落盘的话，monorepo 里收进来的
  // 那一条名字和命令都是猜的，而删一条错的服务比在表单里改一遍麻烦。
  function PortScanModal(props) {
    var open = props.open, onClose = props.onClose;
    // 变量名 scan 与 sp 是 TestUIFieldNamesExistInBackend 的索引：前者对应
    // manage.PortScanOut，后者是里面的每一条 manage.ScannedPort。换成 data
    // 之类的通用名，这一屏读的字段就再也核对不出来了。
    var ss = React.useState(null), scan = ss[0], setScan = ss[1];
    var ls = React.useState(false), loading = ls[0], setLoading = ls[1];
    var es = React.useState(""), err = es[0], setErr = es[1];
    var bs = React.useState(""), busy = bs[0], setBusy = bs[1];
    var token = A.theme.useToken().token;
    var sm = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };

    var load = function () {
      setLoading(true); setErr("");
      return call("portScan").then(function (r) {
        setScan(r); setLoading(false);
      }).catch(function (ex) {
        setScan(null); setErr(ex.message); setLoading(false);
      });
    };
    React.useEffect(function () { if (open) load(); }, [open]);

    // 纳管：后端重新看一眼这个端口上此刻是谁，照它的目录走一遍识别，再把那份
    // 预填值交给表单。重查一次是必要的——这一屏可能已经摆了几十秒，这期间进程
    // 完全可能退出、端口被另一个人接走。
    var adopt = async function (sp) {
      setBusy(String(sp.port));
      try {
        var info = await call("adoptPort", String(sp.port), "");
        setBusy("");
        onClose();
        props.onAdopt(info, sp);
      } catch (ex) {
        setBusy("");
        props.message.error(ex.message);
      }
    };

    var ports = (scan && scan.ports) || [];
    // 一条都没有不等于「本机什么都没开」：读不到监听表时后端只回一句话（msg），
    // 把那种情况渲染成一个空列表，读到的会是一个与事实相反的结论。
    var emptyText = scan ? (scan.msg || "本机没有正在监听的 TCP 端口") : "";

    // 一个端口该不该收，看的是它此刻归谁：Pier 正在跑的服务本来就在清单里，
    // 目录和某条服务对得上的也已经有主了（多半是同名不同实例）。这两种都不给
    // 「纳管」，否则会多出一条和原来那条抢同一个端口的服务。
    var footer = [
      html`<${A.Button} key="r" icon=${e(IconRefresh)} loading=${loading}
        onClick=${load}>重新扫描<//>`,
      html`<${A.Button} key="c" type="primary" onClick=${onClose}>关闭<//>`
    ];

    return html`<${A.Modal} open=${open} onCancel=${onClose} footer=${footer}
      width=${SIZE.modalScan} styles=${{ body: BODY_SCROLL }}
      title="扫描本机端口">
      ${"" /* 这一屏列的是「正在监听」的 TCP 端口，来源是系统自己的监听表；
             每一行后面那几样（目录、来源、是不是 Pier 起的）都是额外查出来的，
             查不到就直说查不到，不拿空值凑数——一个空的目录列会让人以为
             那个进程没有工作目录。 */}
      ${err ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
        message="扫描失败" description=${err}/>` : null}
      ${loading && !scan ? html`<${A.Skeleton} active paragraph=${{ rows: 5 }}/>` : null}
      ${scan && !ports.length ? html`<${A.Empty} className="dc-empty"
        description=${emptyText}/>` : null}
      ${ports.length ? html`<div className="dc-list">
        ${ports.map(function (sp) {
          var tag = sp.managed ? "Pier 服务：" + sp.service
            : (sp.known ? "清单里已有：" + sp.known : "");
          var origin = sp.origin && (sp.origin.chain || []).length
            ? sp.origin.chain.join(" ← ") : "认不出来";
          return html`<div className="dc-row dc-scan-row" key=${sp.port}>
            <div className="dc-row-text">
              <div className="dc-row-title">
                <span className="dc-mono dc-port">:${sp.port}</span>
                <span className="dc-row-name">${sp.command || "未知进程"}</span>
                <span className="dc-kind" style=${{ fontSize: token.fontSizeSM,
                  color: token.colorTextSecondary, background: token.colorFillTertiary }}>
                  ${"PID " + sp.pid}</span>
                ${tag ? html`<span className="dc-kind" style=${{ fontSize: token.fontSizeSM,
                  color: token.colorPrimary, background: token.colorPrimaryBg }}>
                  ${tag}</span>` : null}
              </div>
              <div className="dc-row-sub" style=${sm}>
                <span className="dc-mono" title=${sp.dir || ""}>
                  ${sp.dirShort || sp.dir || "查不到工作目录"}</span>
              </div>
              <div className="dc-row-sub" style=${sm}>${"启动来源：" + origin}</div>
            </div>
            <div className="dc-row-actions">
              ${sp.managed || sp.known ? null : html`<${A.Button}
                color="primary" variant="filled"
                loading=${busy === String(sp.port)}
                onClick=${function () { adopt(sp); }}>纳管<//>`}
            </div>
          </div>`;
        })}
      </div>` : null}
    <//>`;
  }

  // ── 端口选择 ─────────────────────────────────────────────────────────────

  function PortPickerModal(props) {
    var open = props.open, onClose = props.onClose, onPick = props.onPick;
    var st = React.useState(null), cand = st[0], setData = st[1];
    var ls = React.useState(false), loading = ls[0], setLoading = ls[1];
    var fs = React.useState(""), from = fs[0], setFrom = fs[1];
    var sel = React.useState(null), picked = sel[0], setPicked = sel[1];

    // 字号取自 antd 的 token，不写死 12px——写死就又是在 antd 之外自己定一套。
    var token = A.theme.useToken().token;

    var load = function (start) {
      setLoading(true);
      call("portCandidates", String(start || 8080)).then(function (r) {
        setData(r); setLoading(false);
        // 输入框改成后端真正用的那个起点：填进来的数它不一定收（0、70000 这些
        // 都会被退回 8080），而下面那句「只看了 X–Y 这一段」说的是它查过的范围。
        // 两边各说各的，输入框里就留着一个从没查过的数。
        if (r && r.from) setFrom(String(r.from));
      }).catch(function (ex) {
        props.message.error(ex.message); setLoading(false);
      });
    };

    React.useEffect(function () {
      if (!open) return;
      setPicked(null);
      var base = props.suggest || 8080;
      setFrom(base);
      load(base);
    }, [open]);

    var free = (cand && cand.free) || [];
    var hints = (cand && cand.hints) || {};

    return html`<${A.Modal} open=${open} onCancel=${onClose} width=${SIZE.modalPicker} title="选择一个空闲端口"
      styles=${{ body: BODY_SCROLL }}
      okText=${picked ? "使用 " + picked : "确定"}
      okButtonProps=${{ disabled: !picked }}
      onOk=${function () { if (picked) { onPick(picked); onClose(); } }}>
      <${A.Space} direction="vertical" size=${12} style=${{ width: "100%" }}>
        ${"" /* 起止数字用 InputNumber 配一个真按钮。原先是一个 Input 顶着
                 addonAfter="开始找"：那是装饰性的 addon，长得像按钮却点不动，
                 旁边还另摆了一个功能相同的「重新查找」按钮，一个动作画了两个控件。 */}
        <${A.Space.Compact}>
          <${A.InputNumber} value=${from} min=${1} max=${65535}
            onChange=${function (v) { setFrom(v); }}
            onPressEnter=${function () { load(from); }}
            addonBefore="从" style=${{ width: 220 }}/>
          <${A.Button} onClick=${function () { load(from); }}>查找<//>
        <//>
        <${A.Alert} type="info" showIcon
          message=${"只看了 " + ((cand && cand.from) || "—") + "–" + ((cand && cand.scanTo) || "—") +
            " 这一段。端口会不会被别的项目占着，只有启动的时候才知道，所以「可以用的」也只是个起点。"}/>
        ${loading ? html`<${A.Skeleton} active paragraph=${{ rows: 5 }}/>` : (!cand ? null : html`<${React.Fragment}>
          ${"" /* 两张表并排摆着、各带一个标题，而不是把「已被占用」收进折叠面板里：
                  挑端口时要同时看见这两边，才知道手上这个号是不是别人已经占了。
                  收起来的那一组看着像可看可不看的补充说明，而它是同一件事的另一半。 */}
          <section className="dc-card dc-pick-card">
            <div className="dc-card-head">
              <span className="dc-card-title">可以用的端口</span>
              <span className="dc-card-sub">此刻没有进程监听，清单里也还没用到</span>
            </div>
            <${A.Space} size=${[8, 8]} wrap align="start">
              ${free.map(function (p) { return html`<${A.Button} key=${p}
                type=${picked === p ? "primary" : "default"}
                onClick=${function () { setPicked(p); }}
                style=${{ height: "auto", padding: "4px 12px", textAlign: "center" }}>
                <span style=${{ display: "block", fontWeight: 600 }}>${p}</span>
                ${hints[String(p)] ? html`<span
                  style=${{ display: "block", fontSize: token.fontSizeSM, opacity: .65 }}>${hints[String(p)]}</span>` : null}
              <//>`; })}
              ${free.length ? null : html`<span style=${{ color: token.colorTextTertiary }}>
                这一段里没有可以用的端口，把起点往后挪一挪再找。<//>`}
            <//>
          </section>
          ${((cand.used || []).length || (cand.taken || []).length) ? html`<section className="dc-card dc-pick-card">
            <div className="dc-card-head">
              <span className="dc-card-title">已被占用的端口</span>
              <span className="dc-card-sub">清单里已经写掉的，和此刻正被别人占着的</span>
            </div>
            <${A.Space} size=${[4, 4]} wrap>
              ${(cand.used || []).map(function (p) {
                return html`<${A.Tag} key=${"u" + p} color="orange">${p} 清单已用<//>`;
              })}
              ${(cand.taken || []).map(function (p) {
                return html`<${A.Tag} key=${"t" + p}>${p} 已被占用<//>`;
              })}
            <//>
          </section>` : null}
        <//>`)}
      <//>
    <//>`;
  }

  // ── 添加 / 编辑应用 ──────────────────────────────────────────────────────

  // 高级设置里的占位示例按类型给：给 Node 服务看 mvn 命令，本身就是在把别的语言的东西塞给它。
  var ADV_HINTS = {
    java: { run: "如 mvn -pl shop-admin spring-boot:run", build: "如 mvn -pl shop-admin -am install", env: "SPRING_PROFILES_ACTIVE=local" },
    node: { run: "如 pnpm dev", build: "如 pnpm install", env: "NODE_ENV=development" },
    go: { run: "如 ./bin/api -config config.yaml", build: "如 go build -o bin/api ./cmd/api", env: "APP_ENV=dev" },
    python: { run: "如 python main.py", build: "如 pip install -r requirements.txt", env: "PYTHONUNBUFFERED=1" },
    shell: { run: "如 ./start.sh", build: "如 ./build.sh", env: "APP_ENV=dev" }
  };
  var ADV_HINT_DEFAULT = { run: "如 ./start.sh", build: "如 make build", env: "APP_ENV=dev" };

  // 下拉框（分组、类型）的空值用 undefined 而不是空串：空串会被当成「选中了一个空选项」，
  // 占位文字「未分组」「自动识别」就不显示了，看着像控件坏了。
  // dependsOn 用 [] 而不是 undefined：多选下拉的空值就是空数组。
  var EMPTY_FORM = {
    name: "", dir: "", group: undefined, kind: undefined, run: "", build: "",
    module: "", script: "", port: null, health: "", note: "", env: "",
    dependsOn: [], restart: undefined
  };

  // 环境变量在表单里是多行文本（一行一个 KEY=VALUE），存的时候是对象。
  // 两种写法的互转只在这里：空行、# 开头的注释行跳过，值里的等号原样保留。
  function envToText(env) {
    return Object.keys(env || {}).sort().map(function (k) { return k + "=" + env[k]; }).join("\n");
  }
  function textToEnv(text) {
    var out = {};
    String(text || "").split("\n").forEach(function (line) {
      var t = line.trim();
      if (!t || t.charAt(0) === "#") return;
      var i = t.indexOf("=");
      if (i <= 0) return;
      out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
    });
    return out;
  }

  // 弹窗比窗口还高时的兜底，每个 Modal 都要挂。
  //
  // antd 的 Modal 不限制自身高度，内容一长就把底部的「取消 / 确定」顶到窗口外面去，
  // 而弹窗本身看上去完全正常——只是按钮点不着，很容易被当成「保存坏了」。
  // 添加应用那个表单实测 784px 高，窗口才 780px，正好踩中。
  //
  // 这是 antd 文档给的做法：给 body 一个上限让它自己滚。内容短的时候
  // maxHeight 不起作用，所以小弹窗挂上也没有副作用。
  //
  // 这里**不能**用百分比。百分比的 max-height 只在包含块高度确定时才算得出来，
  // 而 Modal 的每一层（.ant-modal-wrap 里的 .ant-modal、.ant-modal-content）
  // 高度都是 auto，一路传下来百分比就退化成 none，等于没写。
  // 所以只能减一个绝对的像素数，那这个数就必须跟着窗口算。
  //
  // 减掉的是弹窗自己占掉的纵向空间：顶距 + 页头 + 页脚 + Modal 底部的
  // padding-bottom（antd 留的 24px，让最后一行能滚出视口）。
  // 顶距不能按 antd 默认的 100px 写死——那条在 app.css 里已经换成了
  // clamp(16px, 8vh, 100px)，矮窗口下会缩，这里必须减同一个表达式，
  // 否则窗口一矮，算出来的上限比实际可用空间大，按钮又会被顶出去。
  var MODAL_TOP = "clamp(16px, 8vh, 100px)";
  var MODAL_CHROME = "160px"; // 64(页头，含 app.css 里加大的标题下边距) + 72(页脚) + 24(底部留白)
  var BODY_SCROLL = {
    maxHeight: "calc(100vh - " + MODAL_TOP + " - " + MODAL_CHROME + ")",
    overflowY: "auto",
    // 横向必须是 clip，不能省。
    //
    // CSS 里有一条容易忘的规矩：overflow-x/y 只要有一个不是 visible，
    // 另一个的 visible 就会算成 auto。所以只写 overflow-y: auto 的话，
    // 横向也一并成了 auto——内容只要比 body 宽一点点，下边就会冒出一条
    // 横向滚动条。表单里那几组 antd 的 <Row gutter={16}> 正是这种内容，
    // 实测 .ant-modal-body 是 scrollWidth 849 对 clientWidth 841。
    // 那 8px 的根子已经在那里拔掉了（见 TwoCol），这里再兜一道：
    // 差几个像素就出滚动条这种事，不该靠「每个容器都刚好不超宽」来保证。
    //
    // 用 clip 而不是 hidden 是图它的语义（不建立滚动容器），但要清楚
    // **WebKit 现在把 clip 当 hidden 用**：实测 overflow-x 算出来是 hidden，
    // scrollLeft 仍能被推动 8px。所以它挡的是滚动条，不是滚动本身。
    // 真要一点都不滚，靠的是上面那个 TwoCol 把宽度做对。
    overflowX: "clip"
  };

  // 弹窗和抽屉一律用 vw 定宽，不用像素。
  //
  // 窗口是能拖的，写死的宽度在窄窗口下会顶满、在大窗口下又缩成中间一小块，
  // 两头都难看——「弹窗不会跟着窗口变」就是这么来的。
  // vw 那一段负责跟着窗口长，px 那一段是上限：表单的行宽超过 ~1000px
  // 之后标签和输入框离得太远，扫视很累，全屏时铺满整个窗口并不好读。
  //
  // 宽度归这里一处管，是因为这七个值之间是有相对关系的
  // （详情 < 挑选 = 扫描 < 表单，抽屉最宽），散在七个组件里改一个就失去平衡。
  var SIZE = {
    // 只问一句话的：新建/重命名分组、删除确认。
    modalNarrow: "min(460px, 44vw)",
    modalConfirm: "min(520px, 48vw)",
    // 端口占用详情：字段值都是路径和命令行，要能换行读。
    modalOwner: "min(760px, 62vw)",
    // 挑选空闲端口：一屏里尽量多列几个候选。
    modalPicker: "min(920px, 70vw)",
    // 扫描本机端口：与「挑选」同样是几列文字加一颗按钮，宽度对齐它。
    modalScan: "min(920px, 70vw)",
    // 添加/编辑应用：三行两列的表单，最宽的那个。
    modalForm: "min(1080px, 76vw)",
    // 日志抽屉：越宽一行能放下的日志越长，给到最大。
    drawer: "min(1080px, 82vw)"
  };

  // 表单里成对出现的那两列。
  //
  // 不用 antd 的 <Row gutter={16}> + <Col span={12}>：Row 是靠左右各
  // -gutter/2 的负外边距去抵消 Col 的 padding 的，于是它天生就比容器宽
  // 16px。放在页面里无所谓，放在弹窗体里就正好撑出一条横向滚动条——
  // 实测 .ant-modal-body 的 scrollWidth 849 对 clientWidth 841，多出来的
  // 那 8px 就是它。grid 的 gap 不占额外宽度，容器多宽它就多宽。
  function TwoCol(props) {
    return html`<div style=${{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16 }}>
      ${props.children}
    <//>`;
  }

  function ServiceFormModal(props) {
    var open = props.open, editing = props.editing, onClose = props.onClose;
    var form = A.Form.useForm()[0];
    var is = React.useState({}), info = is[0], setInfo = is[1];
    var ps = React.useState(false), portOpen = ps[0], setPortOpen = ps[1];
    var bs = React.useState(false), saving = bs[0], setSaving = bs[1];
    var insp = React.useState(false), inspecting = insp[0], setInspecting = insp[1];
    var ng = React.useState(""), newGroup = ng[0], setNewGroup = ng[1];
    var cg = React.useState(false), creatingGroup = cg[0], setCreatingGroup = cg[1];
    // 「专属设置」里那几个 SDK 下拉要列出本机有什么可选，打开表单时取一次。
    // 取一次而不是每次改字段都取：扫盘要读若干目录，而这份列表在一次编辑里不会变
    // （变了的话是这个弹窗外面发生的事，见「重新扫描」）。
    var sk = React.useState([]), sdkKinds = sk[0], setSdkKinds = sk[1];
    var se = React.useState(""), sdkErr = se[0], setSdkErr = se[1];
    // tc 是「这个服务钉住哪几套 SDK」：类别 → 安装目录。它就是专属设置里那几个
    // 下拉的值，也是提交时发出去的 toolchain。
    //
    // 存成 state 而不是挂进表单：它不是一栏表单字段，类别个数还随服务类型变，
    // 硬塞进 Form 只会多出一层名字映射，而那一层正是最容易和下拉对不上的地方。
    var tcs = React.useState({}), tc = tcs[0], setTc = tcs[1];
    // 扫盘失败要说出来。以前这里把错误吞掉、把列表清空，于是在下拉里看到的
    // 和「本机确实一套 SDK 都没装」一模一样——一个说「扫不了」、一个说「没有」，
    // 后面该做的事完全不同（去看 SDK 管理页的报错，还是去装一个）。
    var loadSdks = function () {
      return call("sdkList")
        .then(function (r) { setSdkKinds((r && r.kinds) || []); setSdkErr(""); })
        .catch(function (ex) { setSdkKinds([]); setSdkErr(ex.message); });
    };
    React.useEffect(function () {
      if (open) loadSdks();
    }, [open]);
    var kindNow = A.Form.useWatch("kind", form);
    // 决定显示哪一组类型专属设置：手选的优先，其次目录识别出来的，再次编辑时原来的。
    var kindEff = kindNow || (info && info.kind) || (editing && editing.kind) || "";
    var advHint = ADV_HINTS[kindEff] || ADV_HINT_DEFAULT;

    // 截图核对用：端口选择平时只能靠点「选择…」打开，钩子打不开就拍不到。
    React.useEffect(function () {
      if (open && props.openPort) setPortOpen(true);
    }, [open, props.openPort]);

    React.useEffect(function () {
      if (!open) return;
      setInfo({});
      // 重名检查要看的名单。先用上层手里这份（它已经是最新的状态），
      // 等「浏览…」或目录变化触发 inspect 时再换成后端刚取回来的那一份。
      // 不铺这一下的话，编辑态（不碰目录栏）打开表单时名单是空的，重名校验形同虚设。
      namesRef.current = props.knownNames || [];
      // 钉住的 SDK 也跟着回填。不回填的话，编辑一个已经指定了 JDK 的服务时
      // 下拉会显示成空的，看着像「没钉」，一保存就真把它删了。
      setTc(Object.assign({}, (editing && editing.toolchain) || {}));
      if (props.adopt) {
        // 从「扫描本机端口」收进来的那一条：目录、名字、类型、健康地址全由后端
        // 照那个进程推好了，这里照填。端口直接抄它此刻正在听的那个——纳管的
        // 意思就是「照它现在的样子记下来」，另挑一个端口收进来就没有意义了。
        //
        // 走的是与手动填目录完全相同的一次识别（见 manage.AdoptPort），所以
        // info 里那份说明和「填了目录之后」看到的一模一样，不必另写一套。
        var d = props.adopt.info || {};
        namesRef.current = d.names || [];
        setInfo(d);
        form.setFieldsValue(Object.assign({}, EMPTY_FORM, {
          dir: d.relPath || d.absPath || "", name: d.suggestName || "",
          kind: d.kind || undefined, port: props.adopt.port || d.suggestPort || null,
          health: d.health || ""
        }));
        // 「它此刻还在跑」必须说在明处：这个端口现在握着的是那个老进程，
        // 保存只是把它记进清单，要等 Pier 真的把它拉起来才会换人。不说的话，
        // 用户会以为点了保存就已经接管了。
        if (d.adopted) {
          props.message.info("已照 " + d.adopted + " 填好，它此刻还在跑；" +
            "保存后先停掉它，再在这里启动，才由 Pier 接管。");
        }
        return;
      }
      // 编辑时用后端回传的原值预填，包括用户手写的 run/build/module/script——
      // 不回填的话，保存一次就会把这些字段悄悄清空。
      form.setFieldsValue(editing ? {
        name: editing.name, dir: editing.dir, group: editing.group || undefined, kind: editing.kind || undefined,
        run: editing.run, build: editing.build, module: editing.module, script: editing.script,
        port: editing.port > 0 ? editing.port : null, health: editing.health, note: editing.userNote,
        env: envToText(editing.env),
        // 依赖与重启也要回填。不回填的话，编辑一个配了前置的服务时那两个下拉是空的，
        // 看着像「没配」，一保存就真把它清掉了——和上面那几栏是同一个坑。
        dependsOn: editing.dependsOn || [], restart: editing.restart || undefined
      } : Object.assign({}, EMPTY_FORM, { group: props.defaultGroup || undefined }));
      // 依赖项只认端口号这个数。写成整个 adopt 对象的话，上层每 2 秒刷一次状态都会
      // 换一个新对象，这一处预填就会跟着重跑一遍——用户刚改好的那几栏会被抹回原样。
    }, [open, editing, props.adopt ? props.adopt.port : 0]);

    // ── 「将使用 X，依据 Y」的那次预演 ───────────────────────────────────────
    //
    // 拿表单里还没保存的内容去问一遍后端，走的正是启动时那套解析器
    // （internal/proc.Supervisor.Tools）。它答的是「这个服务这次会用哪一套 SDK、
    // 凭的是哪条依据」——版本选错属于编译到一半才炸的那类错，能在点保存之前
    // 看见，就值得多问这一次。真正的挑选逻辑只在 internal 里有一份，界面复刻
    // 一遍的话，两边迟早在某个 corner case 上分叉。
    var pr = React.useState(null), preview = pr[0], setPreview = pr[1];
    var previewTimer = React.useRef(null);

    // 只需要提交当前类型用得上的那几个类别：没出现的类别后端会沿用服务上原来的值，
    // 而发一个空串的意思是「把这个钉子删掉」——切换类型时不该顺手删掉另一种语言
    // 已经钉好的设置。
    var pinsFor = function (pins) {
      var map = pins || tc;
      var out = {};
      (KIND_TOOLS[kindEff] || []).forEach(function (k) { out[k] = map[k] || ""; });
      return out;
    };

    // pins 显式传入是为了绕开 state 的滞后：打开表单那一刻刚 setTc 过，
    // 这个闭包里的 tc 还是上一份，用它去问就等于「钉住的 JDK 没算进去」。
    var resolvePreview = function (pins) {
      clearTimeout(previewTimer.current);
      if (!open) { setPreview(null); return; }
      if (!String(form.getFieldValue("dir") || "").trim()) { setPreview(null); return; }
      previewTimer.current = setTimeout(function () {
        // 取值放在这里而不是上面：这 250ms 里用户可能又改了字，用排队那一刻的
        // 快照去问，答的就是上一版内容。
        var v = form.getFieldsValue(true);
        call("toolchain", JSON.stringify({
          name: v.name || "", dir: v.dir || "", group: v.group || "", kind: v.kind || "",
          run: v.run || "", build: v.build || "", module: v.module || "", script: v.script || "",
          toolchain: pinsFor(pins)
        })).then(setPreview).catch(function (ex) { setPreview({ ok: false, msg: ex.message }); });
      }, 250);
    };

    React.useEffect(function () {
      if (!open) { setPreview(null); return; }
      resolvePreview((editing && editing.toolchain) || {});
      return function () { clearTimeout(previewTimer.current); };
    }, [open, editing]);

    var inspect = async function (dir) {
      if (!dir || !String(dir).trim()) return;
      setInspecting(true);
      try {
        // 表单上已填的类型、子模块、启动命令一并带上：推导启动方式离不开它们，
        // 只看目录的话，一个早就配好子模块的 Java 服务一编辑就会被报「推导失败」。
        // 取值用 getFieldsValue(true)：「高级」里那几栏没展开过时不算已注册字段，
        // 不带 true 会漏掉它们。
        var filled = form.getFieldsValue(true);
        var d = await call("inspectDir", String(dir).trim(), JSON.stringify({
          name: filled.name || "", kind: filled.kind || "", module: filled.module || "",
          run: filled.run || "", build: filled.build || "", script: filled.script || ""
        }));
        namesRef.current = d.names || [];
        setInfo(d);
        if (!editing) {
          // 只补空字段，不覆盖用户已经填好的内容。
          //
          // 「填了目录之后」这句话要按字面兑现：目录是第一个填的，服务名、类型、
          // 端口、健康检查都由它推出来。以前只有类型和端口会自动补，服务名要手打，
          // 而目录名本来就够推一个合用的名字了。
          var cur = form.getFieldsValue();
          var patch = {};
          if (!cur.kind && d.kind) patch.kind = d.kind;
          if (!cur.name && d.suggestName) patch.name = d.suggestName;
          if (!cur.port && d.suggestPort) patch.port = d.suggestPort;
          if (!cur.health && d.health) patch.health = d.health;
          if (Object.keys(patch).length) form.setFieldsValue(patch);
        }
        if (d.msg) props.message.info(d.msg);
        if (d.plan) props.message.success("识别到的启动方式：" + d.plan);
      } catch (ex) {
        setInfo({ msg: ex.message });
      }
      setInspecting(false);
    };

    // 就地新建分组。建完顺手选上——分组名多半是看着目录才想起来的，
    // 建完还要回下拉里再点一次，那这一栏就还是两趟。
    //
    // 名字合法性不在这里做：后端 CreateGroup 已经在校验，重名也由它报错。
    // 前端再判一遍只会多出一份要跟着改的规则。
    var createGroup = async function () {
      var v = String(newGroup || "").trim();
      if (!v) return;
      setCreatingGroup(true);
      try {
        var r = await call("createGroup", v);
        form.setFieldsValue({ group: v });
        setNewGroup("");
        props.message.success(r.msg || "已新建分组");
        // 下拉里的分组来自上层 state，不重新取一次就看不到刚建的那个。
        props.onSaved();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setCreatingGroup(false);
    };

    var browse = async function () {
      try {
        var r = await call("pickDirectory", form.getFieldValue("dir") || "");
        if (r.canceled || !r.dir) return;
        form.setFieldsValue({ dir: r.rel || r.dir });
        setInfo({});
        inspect(r.rel || r.dir);
      } catch (ex) {
        props.message.error(ex.message);
      }
    };

    // sdkOf 找出某一类别在本机有哪些可选。列表是打开表单时取回来的那一份。
    var sdkOf = function (kind) {
      for (var i = 0; i < sdkKinds.length; i++) if (sdkKinds[i].kind === kind) return sdkKinds[i];
      return null;
    };

    var setPin = function (kind, path) {
      var next = Object.assign({}, tc);
      if (path) next[kind] = path; else delete next[kind];
      setTc(next);
      resolvePreview(next);
    };

    // 「浏览…」：先让后端确认选中的目录真的是这一类 SDK，再当作选中值。
    //
    // 选中的值必须是后端校验之后规范化过的路径——macOS 上选 .jdk 包本身时，
    // JDK 的 Home 是包里的 Contents/Home。界面自己去猜这个转换只会猜错，
    // 而且猜出来的路径和自动扫到的那一条会被当成两个不同的 JDK。
    var browseSDK = async function (kind) {
      try {
        var r = await call("pickDirectory", tc[kind] || "");
        if (r.canceled || !r.dir) return;
        var add = await call("sdkAdd", kind, r.dir);
        props.message.success(add.msg || "已添加");
        setPin(kind, add.item ? add.item.path : r.dir);
        // 下拉里要立刻能看到刚加的这一条，所以重新取一份列表。
        loadSdks();
      } catch (ex) {
        props.message.error(ex.message);
      }
    };

    var submit = async function () {
      var v;
      try { v = await form.validateFields(); } catch (ex) { return; }
      setSaving(true);
      try {
        var r = await call("saveService", JSON.stringify(Object.assign({}, v, {
          port: v.port ? parseInt(v.port, 10) : 0,
          env: textToEnv(v.env), toolchain: pinsFor(),
          // 这两栏每次都发全：空数组表示「没有前置」，空串表示「不自动重启」，
          // 都是明确的意图，与「这次提交没带这一项」分得开（见 manage.ServiceIn）。
          dependsOn: v.dependsOn || [], restart: v.restart || "",
          // 名称那一栏改了就叫一次改名。送的是「编辑前叫什么」，由后端把改名和
          // 覆盖保存放进同一次写盘：分两次发的话，中间任何一步失败都会留下
          // 名字和内容对不上的半截状态。
          origName: editing ? editing.name : ""
        })));
        props.message.success(r.msg || "已保存");
        props.onSaved();
        onClose();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setSaving(false);
    };

    var groups = props.groups || [];
    var detected = info && info.kind ? info.kind : "";

    // 服务名唯一性是靠重名覆盖来实现的：同名再存一次会静默改掉原来那条。
    // 后端一直把已有名字放在 info.names 里，只是没人用。这里就地拦一下——
    // 覆盖本身是合法操作（编辑时就要覆盖自己），所以只在新增时拦。
    // 已有名字走 ref 而不是直接读 info：inspect 里 setInfo 是异步的，
    // 紧接着 setFieldsValue 触发的那次校验读到的还是上一次的 info，
    // 自动填进去的名字就撞不上重名提示——而自动填正是最容易撞名的路径。
    // 校验必须看到「刚刚拿回来的那份名单」，所以名单在 inspect 里同步写进 ref。
    var namesRef = React.useRef([]);
    var nameRules = [
      { required: true, message: "请填写服务名称" },
      { pattern: /^[^\s/\\]+$/, message: "不能包含空格或斜杠" }
    ];
    // 编辑态也查重名：名称那一栏现在可以改（改完在保存里连同改名一起落盘，
    // 见 manage.ServiceIn.OrigName），重名了要当场说，而不是等保存回来报错。
    // 编辑时把自己排除在外——不然打开表单的第一秒它就在说「已经有叫它的服务了」。
    nameRules.push({
      validator: function (_, v) {
        var self = editing ? editing.name : "";
        if (v && v !== self && namesRef.current.indexOf(v) >= 0) {
          return Promise.reject(new Error("已经有叫 " + v + " 的服务了，换个名字；" +
            "想改它请直接编辑那一条"));
        }
        return Promise.resolve();
      }
    });

    // 端口那栏的说明随来源变。读到项目自己声明的端口时有一件事必须点破：
    // 它可能正被清单里别的服务占着。以前建议端口来自 FreePort，天然避开已占用的，
    // 现在改成照抄项目声明的值，这个冲突就由这次改动带进来了——所以得说清楚，
    // 不然要等两个服务抢端口的时候才发现。
    // 端口读自项目自己的配置时，它可能正被清单里别的服务占着：以前建议端口来自
    // FreePort，天然避开已占用的；照抄项目声明的值就会把这个冲突带进来，所以要当场点破，
    // 不然要等两个服务抢端口的时候才发现。这是表单里唯一保留在输入框下方的提示。
    var portClash = !!(info && info.portFrom &&
      (info.existing || []).indexOf(info.suggestPort) >= 0);

    // 「将使用 X，依据 Y」。答的是一个服务运行时真正会用到的那几套 SDK，
    // 连同「凭什么挑中它」——这一行存在的意义，就是让「为什么不是我要的那个版本」
    // 在点保存之前就有答案。
    var previewBlock = function () {
      if (!preview) return null;
      var head = html`<div className="dc-tc-head">将使用</div>`;
      // 整段解析失败（目录还没填、pom 读不出来）时后端只回一句话，
      // 那时没有工具可列，就把这句话当成答案。
      if (hasVal(preview.msg)) return html`<div className="dc-tc">
        ${head}
        <div className="dc-tc-row"><span className="dc-tc-warn">
          ${e(IconAlert, { size: 11 })}${preview.msg}</span></div>
      </div>`;
      var list = (preview.tools || []).filter(function (t) { return hasVal(t.label) || hasVal(t.error); });
      if (!list.length) return html`<div className="dc-tc">
        ${head}
        <div className="dc-tc-row"><span className="dc-tc-why">这个类型不需要额外的 SDK</span></div>
      </div>`;
      return html`<div className="dc-tc">
        ${head}
        ${list.map(function (t, i) {
          if (hasVal(t.error)) return html`<div className="dc-tc-row" key=${i}>
            <span className="dc-tc-warn">${e(IconAlert, { size: 11 })}${t.error}</span></div>`;
          return html`<${React.Fragment} key=${i}>
            <div className="dc-tc-row">
              <span className="dc-tc-name">${t.label}</span>
              <span className="dc-tc-why">${[t.reason, t.warn].filter(hasVal).join("；")}</span>
            </div>
            ${hasVal(t.bin) ? html`<div className="dc-tc-row">
              <span className="dc-tc-path dc-mono">${t.bin}</span></div>` : null}
          <//>`;
        })}
      </div>`;
    };
    return html`<${A.Modal} open=${open} onCancel=${onClose} width=${SIZE.modalForm}
      title=${editing ? "编辑应用 " + editing.name : "添加应用"}
      okText=${editing ? "保存" : "添加"} cancelText="取消"
      confirmLoading=${saving} onOk=${submit}
      styles=${{ body: BODY_SCROLL }}>
      ${"" /* 四张卡片自上而下：所有类型都有的基本信息 → 只对当前类型生效的专属设置 →
             跨服务的依赖与重启 → 所有类型通用的高级设置。标签在左、统一四个字以内，
             排成一列对齐；说明一律写成输入框里的灰色占位文字，不在每栏底下再挂一行。
             短内容（名称、类型、分组、端口、子模块、JDK、前置、重启）两两一行，
             路径、地址、命令、备注各占一行。不画必填星号：星号会把那一栏的标签挤歪，
             漏填时校验信息照样会出来。 */}
      <${A.Form} form=${form} layout="horizontal" className="dc-form" colon=${false}
        requiredMark=${false} labelAlign="left" labelCol=${{ flex: "0 0 68px" }}
        wrapperCol=${{ flex: "1 1 0", style: { minWidth: 0 } }}
        onValuesChange=${function () { resolvePreview(); }}>
        <section className="dc-card">
          <div className="dc-card-head">
            <span className="dc-card-title">基本信息</span>
            <span className="dc-card-sub">所有类型的服务都要填</span>
          </div>
          ${"" /* 与下面「端口」那一栏同一个坑：带 name 的 Form.Item 直接包 Space.Compact 时，
                 value/onChange 注入给了 Space.Compact，它不转发，里面的 Input 就既显示不出
                 编辑时回填的目录、也回传不了手敲或「浏览…」选的值。name 必须落在只包 Input 的
                 那层 noStyle 的 Form.Item 上。 */}
          <${A.Form.Item} label="项目目录">
            <${A.Space.Compact} block>
              <${A.Form.Item} name="dir" noStyle rules=${[{ required: true, message: "请填写项目目录" }]}>
                <${A.Input} autoFocus=${!editing}
                  placeholder="项目文件夹的绝对路径，填完自动识别类型、端口与启动命令"
                  onBlur=${function (ev) { inspect(ev.target.value); }}/>
              <//>
              <${A.Button} onClick=${browse} loading=${inspecting}>浏览…<//>
            <//>
          <//>

          ${info && (info.msg || info.kind) ? html`<${A.Alert} className="dc-card-alert"
            type=${info.kind ? "success" : "warning"} showIcon
            message=${info.kind
              ? "识别为 " + (KIND_LABEL[info.kind] || info.kind) + "（依据 " +
                (info.found || []).join("、") + "）"
              : (info.msg || "没有识别出项目类型")}
            description=${info.plan ? "启动方式：" + info.plan : (info.absPath || "")}/>` : null}

          <${TwoCol}>
            <${A.Form.Item} name="name" label="服务名称" rules=${nameRules}>
              <${A.Input} placeholder="命令行与面板里的标识，如 order-service"/>
            <//>
            <${A.Form.Item} name="kind" label="服务类型">
              <${A.Select} allowClear placeholder=${detected ? "已按目录识别" : "留空按目录自动识别"} options=${[
                { value: "go", label: "Go" }, { value: "java", label: "Java" },
                { value: "node", label: "Node" }, { value: "python", label: "Python" },
                { value: "shell", label: "Shell" }]}/>
            <//>
          <//>

          <${TwoCol}>
            <${A.Form.Item} name="group" label="所属分组">
              <${A.Select} allowClear showSearch placeholder="未分组，侧栏按它归类"
                popupRender=${function (menu) {
                  // 就地新建：原本要另开弹窗建完分组再回来重填这一栏，
                  // 而分组名往往正是看着目录才想起来的。
                  //
                  // 用下拉里的一行输入框而不是再套一个 Modal，是因为套 Modal 会多出
                  // 一层弹窗嵌套（Esc、遮罩点击、z-index 都要单独理），
                  // 而这一步本来就只有「输个名字」一件事。
                  return html`<${React.Fragment}>
                    ${menu}
                    <${A.Divider} style=${{ margin: "4px 0" }}/>
                    <${A.Space.Compact} block style=${{ padding: "0 8px 6px" }}>
                      <${A.Input} placeholder="新建分组" value=${newGroup}
                        onChange=${function (ev) { setNewGroup(ev.target.value); }}
                        onPressEnter=${createGroup}/>
                      <${A.Button} onClick=${createGroup} loading=${creatingGroup}>新建<//>
                    <//>
                  <//>`;
                }}
                options=${groups.filter(function (g) { return !g.builtin; })
                  .map(function (g) { return { value: g.name, label: g.name }; })}/>
            <//>
            ${"" /* 端口这一栏必须是「外层 Form.Item 管标签、内层 noStyle 的 Form.Item 管值」。
                 写成单个带 name 的 Form.Item 包住 Space.Compact 是不行的：Form.Item 把
                 value/onChange 注入给它的直接子元素，而直接子元素是 Space.Compact，
                 它不转发这两个 prop，夹在里面的 InputNumber 就既收不到表单的值、
                 也回传不了用户的输入。 */}
            <${A.Form.Item} label="监听端口" validateStatus=${portClash ? "warning" : ""}
              help=${portClash ? "读自 " + info.portFrom + "，但清单里已有服务在用这个端口，保存前先确认" : null}>
              <${A.Space.Compact} block>
                <${A.Form.Item} name="port" noStyle>
                  <${A.InputNumber} style=${{ width: "100%" }} min=${1} max=${65535}
                    placeholder="留空表示不监听端口"/>
                <//>
                <${A.Button} onClick=${function () { setPortOpen(true); }}>选择…<//>
              <//>
            <//>
          <//>

          ${"" /* 选填，而且「探不通」不是失败：探针只用来把「启动中」提前变成
                 「运行中」，它没探通时服务照跑，界面只在那一行说明里挂一句
                 「健康探针未通过」并给一个「不再检查健康」。这段话说在栏位下面
                 而不是塞进 placeholder：placeholder 一输入就没了，而这一段要
                 在人看着自己刚填的地址时起作用。 */}
          <${A.Form.Item} name="health" label="健康检查" extra=${"选填。填了就等到探通才显示「运行中」；填错或服务没有这个接口也不影响运行，只会一直在说明里提示，随时可以关掉。"}>
            <${A.Input} placeholder="http://localhost:8080/health，留空只看进程是否存活"/>
          <//>

          <${A.Form.Item} name="note" label="备注说明">
            <${A.Input.TextArea} autoSize=${{ minRows: 2, maxRows: 5 }}
              placeholder="写给自己看的说明，面板上原样显示，如：本地调试用，数据库指向 dev 库"/>
          <//>
        </section>

        ${"" /* 类型专属的设置只在对应类型下出现：Java 的子模块，Node 的脚本，
               再加上这个类型的服务要用到的那几套 SDK。其余类型的字段仍然渲染、
               只是隐藏，这样切换类型不会把已经填的值清掉。
               shell 类型既不编译也不依赖运行时，没有专属设置，整张卡片收起来。 */}
        <section className="dc-card" hidden=${!(KIND_TOOLS[kindEff] || []).length}>
          <div className="dc-card-head">
            <span className="dc-card-title">专属设置</span>
            <span className="dc-card-sub">${"只对 " + (KIND_LABEL[kindEff] || kindEff) + " 服务生效"}</span>
          </div>
          <div hidden=${kindEff !== "java"}>
            <${TwoCol}>
              <${A.Form.Item} name="module" label="子模块">
                <${A.Input} placeholder="Maven 多模块里要跑的模块，如 shop-admin"/>
              <//>
              <div/>
            <//>
          </div>
          <div hidden=${kindEff !== "node"}>
            <${TwoCol}>
              <${A.Form.Item} name="script" label="启动脚本">
                <${A.Input} placeholder="package.json 里的脚本，默认 dev"/>
              <//>
              <div/>
            <//>
          </div>

          ${sdkErr ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
            message="读取 SDK 清单失败" description=${sdkErr + "。下面这些下拉里是空的，不代表本机没装。"}/>` : null}

          ${"" /* 每个类别一个下拉，摆法和 IDEA 的项目 SDK 一样：选中就是钉死这一套，
                 留空就按项目自己的声明去挑。默认值来自后端扫出来的那份清单，
                 界面不自己拼——手动加过的那些必须也在里面，否则「浏览…」选中的
                 目录在别处根本看不见。 */}
          ${(KIND_TOOLS[kindEff] || []).map(function (k) {
            var kindInfo = sdkOf(k) || {};
            var opts = (kindInfo.items || []).map(function (si) {
              return { value: si.path, label: si.label, title: si.path + "　" + si.source };
            });
            // 服务上钉着的路径可能已经不在扫出来的列表里（换过一次机器、
            // 或者那个 SDK 被卸了）。不补一条进去的话，下拉显示为空，
            // 看着像「没钉」，而一保存就真的把它删了。
            if (tc[k] && !opts.some(function (o) { return o.value === tc[k]; })) {
              opts.unshift({ value: tc[k], label: tc[k],
                title: "这个目录不在扫出来的列表里，可能已经删了" });
            }
            return html`<${A.Form.Item} key=${k} label=${kindInfo.label || k}>
              <${A.Space.Compact} block>
                <${A.Select} allowClear showSearch style=${{ width: "100%" }}
                  value=${tc[k] || undefined}
                  placeholder="留空自动选择"
                  options=${opts}
                  notFoundContent=${"本机没扫到 " + (kindInfo.label || k) + "，点「浏览…」手动指定目录"}
                  onChange=${function (v) { setPin(k, v); }}/>
                <${A.Button} onClick=${function () { browseSDK(k); }}>浏览…<//>
              <//>
            <//>`;
          })}

          ${previewBlock()}
        </section>

        ${"" /* 跨服务的两件事：谁先起来、它自己退出之后要不要再拉起来。都不影响这个
               服务怎么跑，但一个管「全部启动」的次序，一个管进程意外退出之后要不要
               管——按类型分家的那张表放不下它们，塞进「高级设置」又会把那句
               「留空按类型自动推断」的说明搅浑。 */}
        <section className="dc-card">
          <div className="dc-card-head">
            <span className="dc-card-title">依赖与重启</span>
            <span className="dc-card-sub">留空表示不依赖谁、退出后也不自动重启</span>
          </div>
          <${TwoCol}>
            ${"" /* 只列清单里已有的服务，不给自由输入：写错一个名字的后果是这份清单
                   直接加载不了（「依赖的 X 不在清单里」），而报错要等到下次启动才看得到。
                   把自己排掉，是因为「依赖了自己」同样是一条加载不了的清单。 */}
            <${A.Form.Item} name="dependsOn" label="前置服务">
              <${A.Select} mode="multiple" allowClear showSearch
                placeholder="它们先起来，本服务才轮到"
                options=${(props.knownNames || []).filter(function (n) {
                  return n !== (editing ? editing.name : "");
                }).map(function (n) { return { value: n, label: n }; })}/>
            <//>
            ${"" /* 只有这一个可选值。Pier 不常驻，「总是重启」与「失败时重启」在这里
                   没有差别，多摆一个只会让人琢磨它们差在哪。 */}
            <${A.Form.Item} name="restart" label="退出之后">
              <${A.Select} allowClear placeholder="不自动重启"
                options=${[{ value: "on-failure", label: "失败时自动重启" }]}/>
            <//>
          <//>
        </section>

        <section className="dc-card">
          <div className="dc-card-head">
            <span className="dc-card-title">高级设置</span>
            <span className="dc-card-sub">一般不用填，留空按类型自动推断；填了就以这里为准</span>
          </div>
          <${A.Form.Item} name="run" label="启动命令">
            <${A.Input} className="dc-mono" placeholder=${"留空自动推断，" + advHint.run}/>
          <//>
          <${A.Form.Item} name="build" label="编译命令">
            <${A.Input} className="dc-mono" placeholder=${"启动前先执行，留空自动推断，" + advHint.build}/>
          <//>
          <${A.Form.Item} name="env" label="环境变量">
            <${A.Input.TextArea} autoSize=${{ minRows: 3, maxRows: 8 }} className="dc-mono"
              placeholder=${"一行一个 KEY=VALUE，# 开头为注释，优先于继承来的环境\n" + advHint.env}/>
          <//>
        </section>
      <//>

      <${PortPickerModal} open=${portOpen} onClose=${function () { setPortOpen(false); }}
        suggest=${form.getFieldValue("port") || 8080} message=${props.message}
        onPick=${function (p) { form.setFieldsValue({ port: p }); }}/>
    <//>`;
  }

  // ── SDK 管理 ─────────────────────────────────────────────────────────────
  //
  // 全机一页：本机扫到的 SDK 按语言列出来，每条能设成这一类的全局默认，
  // 手动加进来的能删掉。
  //
  // 这一页管的是「没别的说法时用哪一个」。服务上钉住的那一份优先级比它高，
  // 仍然在表单里选——所以这里显示的是默认值，不是「所有服务都会用这个」。
  // 扫描、校验、路径规范化全在 internal 里做，界面只负责列出来和把点击转过去：
  // 这里多一个判断，另一份界面就要重新实现一遍。
  function SDKPage(props) {
    // 变量名不能叫 data：gui/app_test.go 的 TestUIFieldNamesExistInBackend
    // 按变量名比对字段，data 那个名字上挂的是面板状态结构。
    var sd = React.useState(null), sdks = sd[0], setSdks = sd[1];
    var er = React.useState(""), err = er[0], setErr = er[1];
    var bs = React.useState(""), busy = bs[0], setBusy = bs[1];
    // token 用 useToken 自己取，和界面别处一样。它读的是 ConfigProvider 的上下文，
    // 不持有任何状态，所以在这里再调一次不会像 useTheme 那样分裂成两份。
    var token = A.theme.useToken().token;
    var sm = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };

    var load = function () {
      call("sdkList")
        .then(function (r) { setSdks(r); setErr(""); })
        .catch(function (ex) { setErr(ex.message); });
    };
    React.useEffect(function () { load(); }, []);

    // busy 按「这一类 + 这一条」记，而不是一个全局的 true：
    // 判断哪一行在转比让整页一起变灰更值得——一次只动得了一条。
    var run = async function (key, fn) {
      setBusy(key);
      try {
        var r = await fn();
        if (r && r.msg) props.message.success(r.msg);
        load();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setBusy("");
    };

    var addSDK = function (kind) {
      run(kind + ":add", async function () {
        var pick = await call("pickDirectory", "");
        if (pick.canceled || !pick.dir) return null;
        return call("sdkAdd", kind, pick.dir);
      });
    };

    // 设默认 / 取消默认走同一个入口：后端拿空路径表示「改回自动选」。
    var setDefault = function (kind, path) {
      run(kind + ":" + path, function () { return call("sdkDefault", kind, path); });
    };

    // 类别标题上那句「现在用哪个」。默认值那一条自己带着「全局默认」标记，
    // 所以这里说的是它的名字而不是再抄一遍路径：路径长得能把整行撑满，
    // 而这一行要回答的是「不特别指定时用的是哪个」。
    var defaultLabel = function (ki) {
      var items = ki.items || [];
      for (var i = 0; i < items.length; i++) {
        if (items[i].path === ki.default) return "全局默认：" + items[i].label;
      }
      return hasVal(ki.default) ? "全局默认：" + ki.default : "自动选择";
    };

    var kinds = (sdks && sdks.kinds) || [];

    return html`<${React.Fragment}>
      ${err ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
        message="读取 SDK 清单失败" description=${err}/>` : null}

      ${!sdks && !err ? html`<${A.Skeleton} active paragraph=${{ rows: 5 }}/>` : null}

      ${sdks ? html`<div className="dc-sdk-note">
        <span>这里是各类 SDK 的全局默认：服务上没单独指定、项目里也没有声明要求时，就用它。</span>
        <${A.Button} icon=${e(IconRefresh)} loading=${busy === "__rescan"}
          onClick=${function () { run("__rescan", function () { return call("sdkRescan"); }); }}>
          重新扫描<//>
      </div>` : null}

      ${kinds.map(function (ki) { return html`<section className="dc-group-block" key=${ki.kind}>
        <div className="dc-group-head">
          <div className="dc-group-title">
            <span className="dc-group-name" style=${{ fontSize: token.fontSizeLG }}>${ki.label}</span>
            <span className="dc-count" style=${{ fontSize: token.fontSizeSM,
              color: token.colorTextSecondary, background: token.colorFillTertiary }}>
              ${(ki.items || []).length}</span>
            <span className="dc-sdk-now" style=${sm}>${defaultLabel(ki)}</span>
          </div>
          <div className="dc-group-acts">
            <${A.Button} type="text" className="dc-quiet" icon=${e(IconPlus)}
              loading=${busy === ki.kind + ":add"}
              onClick=${function () { addSDK(ki.kind); }}>添加 SDK…<//>
          </div>
        </div>
        <div className="dc-list">
          ${(ki.items || []).length === 0
            ? html`<div className="dc-list-empty" style=${sm}>
                ${"本机没扫到 " + ki.label + "。装在别处的可以点「添加 SDK…」手动指定目录。"}
              </div>`
            : (ki.items || []).map(function (si) {
                var isDefault = si.path === ki.default;
                var key = ki.kind + ":" + si.path;
                return html`<div className="dc-row dc-sdk-row" key=${si.path}>
                  <div className="dc-row-text">
                    <div className="dc-row-title">
                      <span className="dc-row-name">${si.label}</span>
                      ${hasVal(si.source) ? html`<span className="dc-kind" style=${{ fontSize: token.fontSizeSM,
                        color: token.colorTextSecondary, background: token.colorFillTertiary }}>
                        ${si.source}</span>` : null}
                      ${isDefault ? html`<span className="dc-kind"
                        style=${{ fontSize: token.fontSizeSM, color: token.colorPrimary,
                          background: token.colorPrimaryBg }}>全局默认</span>` : null}
                    </div>
                    <div className="dc-row-sub" style=${{ fontSize: token.fontSizeSM, color: token.colorTextTertiary }}>
                      <span className="dc-mono dc-sdk-path" title=${si.path}>${si.path}</span>
                    </div>
                  </div>
                  <div className="dc-row-actions">
                    ${isDefault
                      ? html`<${A.Button} type="text" className="dc-quiet"
                          loading=${busy === key}
                          onClick=${function () { setDefault(ki.kind, ""); }}>取消默认<//>`
                      : html`<${A.Button} type="text" className="dc-quiet"
                          loading=${busy === key}
                          onClick=${function () { setDefault(ki.kind, si.path); }}>设为默认<//>`}
                    ${"" /* 扫出来的删不掉：删了下次扫描它还在，那个按钮只会让人以为坏了。
                           要拿掉得去卸装。只有手动加进来的才有这一项。 */}
                    ${si.manual ? html`<${A.Button} type="text" className="dc-quiet"
                      danger icon=${e(IconTrash)} title="从手动添加的列表里删掉"
                      onClick=${function () {
                        props.modal.confirm({
                          title: "不再使用这个 SDK？",
                          content: si.path + "　只是从 Pier 的手动列表里去掉，磁盘上的文件不动。",
                          okText: "删掉", okButtonProps: { danger: true }, cancelText: "取消",
                          onOk: function () {
                            return run(key, function () { return call("sdkRemove", ki.kind, si.path); });
                          }
                        });
                      }}/>` : null}
                  </div>
                </div>`;
              })}
        </div>
      </section>`; })}
    <//>`;
  }

  // ── 日志 ─────────────────────────────────────────────────────────────────
  //
  // 全机一页：日志一个服务一个目录、一天一个文件，这里列的是「这一摊占了多少、
  // 覆盖到哪天」，以及两个清理动作。
  //
  // 清理刻意分成两档，因为它们是两个不同的问题：
  //   - 「清理超期」是日常维护，只清超过保留天数的（后端给的 keepDays），
  //     保留期内的一律不动。这档平时根本用不着点——每个服务启动时已经顺手
  //     清过自己那一个了（见 internal/proc 的 PruneLogs）。
  //   - 「清空」是「磁盘现在就紧张」，不看日期。
  // 两档都要人确认一次，而且都把「会删掉哪些」写在确认框里：清日志不可撤销，
  // 「清空」连正在写的那份也删（服务手里的 fd 还开着，它照写不误，
  // 只是那一段从此不在任何文件里）。
  //
  // 所有数字都来自后端，包括换算好的「1.2 GB」——那是 view.Bytes 算的，
  // 与命令行 `pier logs --size` 是同一个。
  function LogPage(props) {
    // 变量名不能叫 data：gui/app_test.go 的 TestUIFieldNamesExistInBackend
    // 按变量名比对字段。lu 对应 panel.LogUsageOut，luSvc 是其中一条。
    var st = React.useState(null), lu = st[0], setLu = st[1];
    var er = React.useState(""), err = er[0], setErr = er[1];
    var bs = React.useState(""), busy = bs[0], setBusy = bs[1];
    var token = A.theme.useToken().token;
    var sm = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };

    var load = function () {
      call("logUsage")
        .then(function (r) { setLu(r); setErr(""); })
        .catch(function (ex) { setErr(ex.message); });
    };
    React.useEffect(function () { load(); }, []);

    // busy 记的是「哪一个动作在跑」，不是一句全局的 true：
    // 一次只动得了一条，让整页一起变灰只会让人以为点坏了。
    var run = async function (key, fn) {
      setBusy(key);
      try {
        var r = await fn();
        if (r && r.msg) props.message.success(r.msg);
        load();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setBusy("");
    };

    var confirmClear = function (name, size) {
      props.modal.confirm({
        title: name ? "清空 " + name + " 的日志？" : "清空全部日志？",
        content: name
          ? "会删掉 " + size + "，包括今天正在写的那份。"
          : "所有服务的日志都会删掉，合计 " + size + "，包括正在写的那几份。",
        okText: "清空", okButtonProps: { danger: true }, cancelText: "取消",
        onOk: function () {
          return run("clear:" + name, function () { return call("clearLogs", name); });
        }
      });
    };

    var services = (lu && lu.services) || [];

    return html`<${React.Fragment}>
      ${err ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
        message="读取日志占用失败" description=${err}/>` : null}

      ${!lu && !err ? html`<${A.Skeleton} active paragraph=${{ rows: 5 }}/>` : null}

      ${lu ? html`<div className="dc-sdk-note">
        <span>${"一天一个文件，放在各自的目录里；每个服务启动时顺手清掉自己超过 "
          + lu.keepDays + " 天的。"}</span>
        <${A.Space.Compact}>
          <${A.Button} icon=${e(IconBroom)} loading=${busy === "prune:"}
            onClick=${function () { run("prune:", function () { return call("pruneLogs", ""); }); }}>
            清理超期<//>
          <${A.Button} icon=${e(IconFolder)}
            onClick=${function () { call("revealLogs").catch(function () {}); }}>
            打开日志目录<//>
        </${A.Space.Compact}>
      </div>` : null}

      ${lu ? html`<section className="dc-group-block">
        <div className="dc-group-head">
          <div className="dc-group-title">
            <span className="dc-group-name" style=${{ fontSize: token.fontSizeLG }}>占用</span>
            ${"" /* 别处这个位置上是一颗光秃秃的数字（那一组有几个服务）。这一页数的
                   是文件，同一颗数字换个页面就换了个含义，读的人得先猜。 */}
            <span className="dc-count" style=${{ fontSize: token.fontSizeSM,
              color: token.colorTextSecondary, background: token.colorFillTertiary }}>
              ${lu.files + " 个文件"}</span>
            <span className="dc-sdk-now" style=${sm}>${"合计 " + lu.size + "　" + lu.dir}</span>
          </div>
          ${services.length ? html`<div className="dc-group-acts">
            <${A.Button} type="text" className="dc-quiet" danger icon=${e(IconTrash)}
              loading=${busy === "clear:"}
              onClick=${function () { confirmClear("", lu.size); }}>全部清空<//>
          </div>` : null}
        </div>
        <div className="dc-list">
          ${services.length === 0
            ? html`<div className="dc-list-empty" style=${sm}>
                ${"还没有任何日志。服务启动后，输出会写在 " + lu.dir +
                  " 下面，一个服务一个目录、一天一个「日期.log」。"}
              </div>`
            : services.map(function (luSvc) {
                var empty = !luSvc.files;
                return html`<div className="dc-row dc-log-row" key=${luSvc.name}>
                  <div className="dc-row-text">
                    <div className="dc-row-title">
                      <span className="dc-row-name" title=${luSvc.name}>${luSvc.name}</span>
                      <span className="dc-kind" style=${{ fontSize: token.fontSizeSM,
                        color: token.colorTextSecondary, background: token.colorFillTertiary }}>
                        ${luSvc.files + " 个文件"}</span>
                    </div>
                    <div className="dc-row-sub" style=${sm}>
                      <span className="dc-mono">
                        ${hasVal(luSvc.newest)
                          ? (luSvc.oldest === luSvc.newest ? luSvc.newest
                            : luSvc.oldest + " ~ " + luSvc.newest)
                          : "没有日志"}</span>
                    </div>
                  </div>
                  <div className="dc-log-size" style=${{ fontSize: token.fontSizeLG }}>${luSvc.size}</div>
                  <div className="dc-row-actions">
                    ${"" /* 这一页只管存储，看内容原本得回服务列表；都站在这条日志前面了
                           还要绕一圈说不过去，所以把同一个抽屉接过来。 */}
                    <${A.Button} type="text" className="dc-quiet" disabled=${empty}
                      onClick=${function () { props.onOpenLog(luSvc.name); }}>查看<//>
                    <${A.Button} type="text" className="dc-quiet" icon=${e(IconFolder)}
                      disabled=${empty} title=${"在" + FILEMGR + "中显示最新那份"}
                      onClick=${function () { call("revealLog", luSvc.name).catch(function () {}); }}/>
                    <${A.Button} type="text" className="dc-quiet" danger
                      disabled=${empty} loading=${busy === "clear:" + luSvc.name}
                      onClick=${function () { confirmClear(luSvc.name, luSvc.size); }}>清空<//>
                  </div>
                </div>`;
              })}
        </div>
      </section>` : null}
    <//>`;
  }

  // ── 分组的新建 / 改名 ────────────────────────────────────────────────────

  function GroupModal(props) {
    var open = props.open, mode = props.mode, target = props.target;
    var st = React.useState(""), val = st[0], setVal = st[1];
    var bs = React.useState(false), busy = bs[0], setBusy = bs[1];

    React.useEffect(function () {
      if (open) setVal(mode === "rename" ? (target || "") : "");
    }, [open, mode, target]);

    var submit = async function () {
      var v = val.trim();
      if (!v) { props.message.warning("请填写分组名"); return; }
      setBusy(true);
      try {
        var r = mode === "rename"
          ? await call("renameGroup", target, v)
          : await call("createGroup", v);
        props.message.success(r.msg || "完成");
        // 改完名要把当前页跟过去。不跟的话 selected 还停在这个分组的旧名字上，
        // 而侧栏里已经没有那一项了——主区就成了一块空白，看着像分组连同
        // 里面的服务一起没了。删分组那边有同样的兜底（改选中「全部服务」）。
        if (mode === "rename" && v !== target) props.onRenamed(v);
        props.onSaved();
        props.onClose();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setBusy(false);
    };

    return html`<${A.Modal} open=${open} onCancel=${props.onClose} width=${SIZE.modalNarrow}
      styles=${{ body: BODY_SCROLL }}
      title=${mode === "rename" ? "重命名分组「" + target + "」" : "新建分组"}
      okText=${mode === "rename" ? "改名" : "新建"} confirmLoading=${busy} onOk=${submit}>
      <${A.Form} layout="vertical">
        <${A.Form.Item} label="分组名" extra=${mode === "rename"
          ? "这个分组下的应用会一起移到新名字下。"
          : "分组只是个标签，之后可以随时改名或删掉。新建后可以把应用加进来。"}>
          <${A.Input} value=${val} autoFocus
            onChange=${function (ev) { setVal(ev.target.value); }}
            onPressEnter=${submit} placeholder="例如 支付相关"/>
        <//>
      <//>
    <//>`;
  }

  // ── 删除确认 ─────────────────────────────────────────────────────────────

  // 等一个服务真的停下来。
  //
  // 「停止」是排队执行的，调用返回只说明排上了，那时删还是会被后端挡回来
  // （见 manage.DeleteService 的护栏）。这里按忙时轮询的节奏看一眼，
  // 等进程没了、动作也清了才算停完。超时不当成功——宁可让人再点一次，
  // 也不能报「已删除」而进程还在。
  async function waitStopped(name) {
    for (var i = 0; i < 20; i++) {
      await new Promise(function (r) { setTimeout(r, STATE_POLL_BUSY_MS); });
      var d = await call("state");
      var s = ((d && d.services) || []).filter(function (x) { return x.name === name; })[0];
      if (!s) return true; // 定义已经不在了（比如别处删掉了）
      if (!s.running && !s.op) return true;
    }
    return false;
  }

  function DeleteModal(props) {
    var open = props.open, svc = props.svc;
    var bs = React.useState(false), busy = bs[0], setBusy = bs[1];
    if (!svc) return null;
    // 正在跑（或正在启动）的服务不能直接删：删掉定义之后进程还在那儿跑，
    // 而界面上已经没有那一行能点「停止」了，它就成了谁也停不掉的孤儿。
    // 后端也会挡（那边还看得见排队中的动作），这里说的是同一件事，
    // 并且把「先停再删」做成一次点击，而不是让人自己分两步去凑。
    var running = !!(svc.running || svc.statusKey === "starting");

    var run = async function () {
      setBusy(true);
      try {
        if (running) {
          await call("stop", svc.name);
          if (!await waitStopped(svc.name)) {
            throw new Error(svc.name + " 还没停下来，等它停稳了再删一次");
          }
        }
        var r = await call("deleteService", svc.name);
        props.message.success(r.msg || "已删除");
        props.onSaved();
        props.onClose();
      } catch (ex) {
        props.message.error(ex.message);
      }
      setBusy(false);
    };

    return html`<${A.Modal} open=${open} onCancel=${props.onClose} width=${SIZE.modalConfirm}
      styles=${{ body: BODY_SCROLL }}
      title=${"删除应用 " + svc.name} confirmLoading=${busy}
      okText=${running ? "停止并删除" : "确认删除"} okButtonProps=${{ danger: true }} onOk=${run}>
      ${running
        ? html`<${A.Alert} type="warning" showIcon
            message="这个应用正在运行"
            description=${"删除会先把它停掉，再删掉这条记录。项目目录里的代码和文件都不会动；" +
              "进程不停就删记录的话，它会变成一个 Pier 再也看不到、也停不掉的进程。"}/>`
        : html`<${A.Alert} type="warning" showIcon
            message="删除后它不再出现在 Pier 里，也不能再从这里启停"
            description="只删除 Pier 里的这条记录，项目目录里的代码和文件都不会动。要恢复就再「添加应用」一次。"/>`}
    <//>`;
  }

  // ── 日志抽屉 ─────────────────────────────────────────────────────────────

  // 抽屉里那摊东西的「空」状态。写成常量是因为它要被比较、被复位，
  // 每次现造一个的话引用每次都不同，复位那一步就失去了意义。
  var LOG_EMPTY = { text: "", path: "", date: "", dates: [], offset: 0, truncated: false };

  function LogDrawer(props) {
    var open = props.open, name = props.name;
    var st = React.useState(LOG_EMPTY), d = st[0], setD = st[1];
    // load 会被那个几秒一次的定时器反复调用，而它读的是「手上已经有哪一段」。
    // 从 state 里读会读到定时器建立时那个闭包里的旧值，于是每次都从同一个
    // offset 取，同一段内容被接第二遍。真正的读数放 ref，state 只管触发重绘。
    var dRef = React.useRef(LOG_EMPTY);
    var commit = function (next) { dRef.current = next; setD(next); };

    var es = React.useState(""), err = es[0], setErr = es[1];
    var as = React.useState(true), auto = as[0], setAuto = as[1];
    var ls = React.useState(false), loading = ls[0], setLoading = ls[1];
    var ws = React.useState(true), wrap = ws[0], setWrap = ws[1];
    var qs = React.useState(""), q = qs[0], setQ = qs[1];
    var hs = React.useState(0), hitIdx = hs[0], setHitIdx = hs[1];
    // 用户明确选过的那一天；空串表示还没选过，跟着最新那份走。第一次读到
    // 哪一天就把这一天定下来，之后每一轮都带着它去读——增量只在「还是同一个
    // 文件」时才成立，而服务跨零点并不会换文件（见 config.LogPathOn）。
    var pick = React.useRef("");

    // 日志跟随到最新一行：错误总在末尾，打开就得看到它，而不是停在开头让人往下翻。
    // 但用户往上翻着看时不能每次刷新都被拽回底部——离底部不到一屏才跟随。
    var preRef = React.useRef(null);
    var findRef = React.useRef(null);
    var follow = React.useRef(true);
    React.useEffect(function () { if (open) follow.current = true; }, [open, name]);

    // ⌘F 聚焦「在日志里找…」。这一条挂在抽屉自己身上而不是主界面上：
    // 「抽屉开着吗」只有这里知道，主界面要管就得再多传一个状态下去。
    //
    // 必须 preventDefault：不然 WebView 可能接手去开它自己的查找栏，
    // 那个搜的是整张页面，日志正文里的人名一个也搜不到——看起来就像搜索坏了。
    React.useEffect(function () {
      if (!open) return;
      var onKey = function (ev) {
        if (!(ev.metaKey || ev.ctrlKey) || ev.key !== "f") return;
        ev.preventDefault();
        var box = findRef.current;
        if (box && box.focus) box.focus();
      };
      document.addEventListener("keydown", onKey);
      return function () { document.removeEventListener("keydown", onKey); };
    }, [open]);
    React.useLayoutEffect(function () {
      var el = preRef.current;
      if (el && follow.current) el.scrollTop = el.scrollHeight;
    }, [d.text]);
    var onScroll = function (ev) {
      var el = ev.currentTarget;
      follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < el.clientHeight / 2;
    };

    // fromTimer 为真表示这是自动刷新的那一轮：只跟「最新那份」。
    // 历史日志不会再变，每几秒去读一遍是白读。
    var load = function (fromTimer) {
      if (!name) return;
      var prev = dRef.current;
      if (fromTimer && pick.current !== (prev.dates[0] || prev.date)) return;
      var want = pick.current;
      // since 只在同一天里成立：换了一天就是另一个文件，从同一个字节数往后
      // 读会读到不相干的位置上去。
      var since = want === prev.date ? prev.offset : 0;
      call("logs", name, want, since).then(function (r) {
        var next = {
          text: r.reset ? (r.text || "") : prev.text + (r.text || ""),
          path: r.path || prev.path,
          date: r.date || want,
          dates: r.dates || prev.dates,
          // 后端给的是它读完之后的文件长度；读的过程中又写进来的那些字节
          // 留给下一轮，不会重也不会漏。
          offset: r.offset || 0,
          truncated: r.reset ? !!r.truncated : prev.truncated
        };
        if (!pick.current) pick.current = next.date;
        commit(next);
        setErr("");
        setLoading(false);
      }).catch(function (ex) { setErr(ex.message); setLoading(false); });
    };

    // 换一个服务（或关掉再开）时先把上一条的正文整个清掉再拉新的。
    // 不清的话，从 a 切到 b 的那一瞬间屏幕上是 a 的日志，而标题已经写着 b 了——
    // 看起来就像这些内容是 b 输出的，读日志时最怕这种张冠李戴。
    React.useEffect(function () {
      pick.current = "";
      dRef.current = LOG_EMPTY;
      setD(LOG_EMPTY);
      setErr("");
      setQ("");
      setHitIdx(0);
      setLoading(!!name);
    }, [open, name]);

    React.useEffect(function () {
      if (!open) return;
      load();
      if (!auto) return;
      var t = setInterval(function () { load(true); }, STATE_POLL_MS);
      return function () { clearInterval(t); };
    }, [open, name, auto]);

    var hits = React.useMemo(function () { return findHits(d.text, q); }, [d.text, q]);
    // 正文变了之后命中数会缩水，而当前那一处的下标还停在原处——夹一下，
    // 免得数着「7 / 5」。
    var cur = hits.length ? Math.min(hitIdx, hits.length - 1) : -1;
    var spans = React.useMemo(function () { return ansiSpans(d.text, hits, cur); }, [d.text, hits, cur]);

    // 跳到某一处之后把它滚到看得见的位置。等 DOM 画完再滚（effect 在提交之后），
    // 而且要手动算位置：scrollIntoView 会把所有能滚的祖先一起滚了，
    // 抽屉正文那一层也会跟着动一下。
    var pending = React.useRef(false);
    var scrollToHit = function () {
      var box = preRef.current, el = box && box.querySelector('[data-dc-hit="1"]');
      if (!box || !el) return;
      var br = box.getBoundingClientRect(), er = el.getBoundingClientRect();
      box.scrollTop += (er.top - br.top) - (br.height - er.height) / 2;
    };
    React.useEffect(function () {
      if (!pending.current) return;
      pending.current = false;
      scrollToHit();
    }, [cur, q]);
    var jump = function (step) {
      if (!hits.length) return;
      // 跳过去之后就别再被自动跟随拽回底部了。
      follow.current = false;
      var next = (cur + step + hits.length) % hits.length;
      // 只有一处命中时下标不变，也就不会再触发上面那个 effect，直接滚。
      if (next === cur) { scrollToHit(); return; }
      pending.current = true;
      setHitIdx(next);
    };

    var pickDate = function (v) {
      pick.current = v || "";
      setHitIdx(0);
      load();
    };
    var copyAll = function () {
      if (!d.text) return;
      call("copyText", d.text).then(function () {
        props.message.success("已复制 " + d.text.split("\n").length + " 行");
      }).catch(function (ex) { props.message.error(ex.message); });
    };

    // 能选的就是真有日志的那几天（外加手上这一天：还没写过日志时后端也会
    // 给出「今天那份」的路径）。不摆一张日历让人一天天去试哪天有东西。
    var dates = d.dates.slice();
    if (d.date && dates.indexOf(d.date) < 0) dates.unshift(d.date);
    var dateOpts = dates.map(function (x) { return { value: x, label: x }; });
    var live = !!d.date && d.date === (d.dates[0] || d.date);
    var count = hits.length >= MAX_HITS ? MAX_HITS + "+" : String(hits.length);

    // 日期下拉的宽度是按内容量出来的：下拉自己的内边距加箭头要吃掉约 50px，
    // 剩下的得放得下 "2026-09-29"（约 76px）。给 124 时正好卡在边界上，截出来
    // 的图里是「2026-09-…」——日期少一位就看不出看的是哪天了。
    //
    // 这条注释只能写在这儿：htm 的模板里没有注释这回事，`//` 会原样变成一个
    // 文本节点印在抽屉顶上（踩过）。
    return html`<${A.Drawer} open=${open} onClose=${props.onClose} width=${SIZE.drawer}
      className="dc-log-drawer"
      title=${"日志 · " + (name || "")}
      extra=${html`<${A.Space}>
        <span title=${live ? undefined : "看的是历史日志，它不会再有新内容"}>
          <${A.Checkbox} checked=${auto && live} disabled=${!live}
            onChange=${function (ev) { setAuto(ev.target.checked); }}>自动刷新<//>
        <//>
        <${A.Button} onClick=${function () { load(); }}>立即刷新<//>
        <${A.Button} onClick=${function () { call("revealLog", name); }}>${"在" + FILEMGR + "中显示"}<//>
      <//>`}>
      ${err ? html`<${A.Alert} type="error" showIcon message=${err}/>` : null}
      <div className="dc-log-bar">
        <${A.Select} value=${d.date || undefined} onChange=${pickDate} options=${dateOpts}
          style=${{ width: 140 }} disabled=${!dateOpts.length}/>
        <${A.Input} value=${q} allowClear placeholder="在日志里找…" ref=${findRef}
          title="⌘F"
          style=${{ width: 220 }}
          onChange=${function (ev) {
            // 换了词就从头数起，并且把第一处滚进视野——不然改了词之后
            // 画面停在原地，看不出到底找没找到。
            setQ(ev.target.value);
            setHitIdx(0);
            pending.current = true;
          }}/>
        ${q ? html`<${React.Fragment}>
          <span className="dc-log-count">${hits.length ? (cur + 1) + " / " + count : "没找到"}</span>
          <${A.Button} type="text" disabled=${!hits.length}
            onClick=${function () { jump(-1); }}>↑<//>
          <${A.Button} type="text" disabled=${!hits.length}
            onClick=${function () { jump(1); }}>↓<//>
        <//>` : null}
        <span className="dc-log-gap"/>
        <${A.Checkbox} checked=${wrap}
          onChange=${function (ev) { setWrap(ev.target.checked); }}>自动换行<//>
        <${A.Button} onClick=${copyAll} disabled=${!d.text}>复制全部<//>
      </div>
      ${d.truncated ? html`<${A.Alert} type="info" showIcon style=${{ marginBottom: 10 }}
        message=${"日志很长，这里只显示末尾部分。完整内容用「在" + FILEMGR + "中显示」打开。"}/>` : null}
      ${d.text ? html`<pre className=${"dc-log" + (wrap ? "" : " dc-log-nowrap")} ref=${preRef}
          onScroll=${onScroll}>${spans}</pre>`
        : loading ? html`<div className="dc-log-wait"><${A.Spin}/></div>`
        : html`<${A.Empty} description="还没有日志。这个服务启动过之后才会有输出。"/>`}
    <//>`;
  }

  // ── 偏好设置 ─────────────────────────────────────────────────────────────
  //
  // 侧栏页脚那一行打开的页面。更新状态由 App 轮询之后传下来，这一页不再起第二个
  // 定时器：两处各轮一次，迟早一前一后对不上，侧栏那颗圆点就会和这一页说的
  // 不是同一件事。
  //
  // 这里只做排版：什么算有新版本、能不能替换自己、失败是什么原因，全在
  // internal/update 里定，后端给什么就摆什么。

  function SettingsPage(props) {
    // 变量名不能改叫别的：gui/app_test.go 的 TestUIFieldNamesExistInBackend
    // 按变量名比对字段，up 上挂的是 update.StatusOut，data 上挂的是 panel.StateOut
    // （「数据」那一栏读的 configPath / configDir / configSource / readOnly 都在后者里）。
    var up = props.up;
    var data = props.data;
    var th = props.th;
    var token = A.theme.useToken().token;
    var sm = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };
    // 左栏停在哪个分类是页面内的临时状态，不落盘：它记的是「刚才在看哪一栏」，
    // 不是什么偏好。重开一次回到「通用」，正是应该的。
    var sec = React.useState("general"), tab = sec[0], setTab = sec[1];
    var bs = React.useState(""), busy = bs[0], setBusy = bs[1];

    // 每个动作跑完都把状态重读一遍，而不是在前端自己改一份：自己改出来的那份
    // 迟早和后端对不上，而这一页说的每一句都该是后端刚算出来的。
    var run = async function (key, fn) {
      setBusy(key);
      try { await fn(); } catch (ex) { props.message.error(ex.message); }
      setBusy("");
      props.reload();
    };

    // 「数据」那一栏的几个动作另有一步：刷的是面板状态（props.refresh），
    // 不是上面那份更新状态。换一份清单、清掉一批残留记录之后，侧栏的服务列表、
    // 主区的标题和数字全都要跟着重算，只刷本页会让它们停在上一份数据上。
    var runSrc = async function (key, fn) {
      setBusy(key);
      try { await fn(); } catch (ex) { props.message.error(ex.message); }
      setBusy("");
      props.refresh();
    };

    var saveAuto = async function (v) {
      setBusy("auto");
      try { await call("saveSettings", JSON.stringify({ updateCheck: !!v })); }
      catch (ex) { props.message.error(ex.message); }
      setBusy("");
      props.reload();
    };

    // 换文件只有这一条路：助手等 Pier 退出、换、再拉回来。
    //
    // 顺序要摆对——先把「已经安排好了」显示出来，过一会儿再关窗口。反过来的话，
    // 用户看到的就是「点了一下，窗口没了」，而这次的安排写在哪儿一句都没说。
    var apply = async function () {
      setBusy("apply");
      try {
        var r = await call("updateApply");
        props.message.success(r.msg || "更新已经安排好了");
        setTimeout(function () { call("quit").catch(function () {}); }, 800);
      } catch (ex) {
        props.message.error(ex.message);
        setBusy("");
        props.reload();
      }
    };

    var showNotes = function () {
      call("updateNotes").catch(function (ex) { props.message.error(ex.message); });
    };

    var row = function (label, value, extra) {
      return html`<div className="dc-set-row">
        <span className="dc-set-key" style=${sm}>${label}</span>
        <span className="dc-set-val">${value}${extra || null}</span>
      </div>`;
    };

    // 下载或解压那一步的失败原因。检查失败（网络不通）不走这里，它只是
    // 页面上的一句话，不弹框——下次到点会自己再试。
    var updAlert = up && up.error && !up.checking && !up.downloading && !up.done
      ? html`<${A.Alert} type="warning" showIcon className="dc-card-alert" message=${up.error}/>`
      : null;

    // 换了这一版之后的那一块。四种样子互斥：下载好了 / 正在下 / 跳过了 / 可以下。
    var updBody = null;
    if (up && up.hasUpdate && up.canInstall) {
      if (up.done) {
        updBody = html`<${React.Fragment}>
          <div className="dc-set-note-head">${up.latest + " 已经下载好了"}</div>
          <div style=${sm}>点下去 Pier 会退出，替换完成后自己回来。</div>
          <${A.Button} type="primary" icon=${e(IconRestart)} loading=${busy === "apply"}
            onClick=${apply}>重启并安装<//>
        </${React.Fragment}>`;
      } else if (up.downloading) {
        updBody = html`<${React.Fragment}>
          <div className="dc-set-note-head">${"正在下载 " + up.latest}</div>
          ${"" /* 服务端没给总量时不画进度条：一条不动的空条比没有条更让人以为卡住了。 */}
          ${up.total > 0 ? html`<${A.Progress} percent=${up.progress} showInfo=${false}/>` : null}
          <div style=${sm}>${(up.total > 0 ? up.receivedSize + " / " + up.totalSize
            : "已下载 " + up.receivedSize) + (hasVal(up.asset) ? " · " + up.asset : "")}</div>
          <${A.Button} onClick=${function () {
            run("cancel", function () { return call("updateCancel"); });
          }}>取消<//>
        </${React.Fragment}>`;
      } else if (up.skipped) {
        updBody = html`<${React.Fragment}>
          <div className="dc-set-note-head">${"已跳过 " + up.latest}</div>
          <div style=${sm}>不再提示这一版；出了更新的版本还会照常提示。</div>
          <${A.Button} onClick=${function () {
            run("skip", function () { return call("updateSkip", ""); });
          }}>不再跳过<//>
        </${React.Fragment}>`;
      } else {
        updBody = html`<${React.Fragment}>
          <div className="dc-set-note-head">
            ${up.latest + " 已发布" + (hasVal(up.publishedAt) ? " · " + up.publishedAt : "")}</div>
          ${hasVal(up.notes) ? html`<div className="dc-set-notes" style=${sm}>${up.notes}</div>` : null}
          <${A.Space}>
            <${A.Button} type="primary" icon=${e(IconDownload)} loading=${busy === "download"}
              onClick=${function () {
                run("download", function () { return call("updateDownload"); });
              }}>${up.error ? "重试下载" : "下载更新"}<//>
            <${A.Button} onClick=${function () {
              run("skip", function () { return call("updateSkip", up.latest); });
            }}>跳过这个版本<//>
          <//>
        </${React.Fragment}>`;
      }
    }

    var version = !up ? html`<${A.Skeleton} active paragraph=${{ rows: 3 }}/>` : html`<${React.Fragment}>
      ${row("当前版本", hasVal(up.current) ? up.current : "-")}
      ${row("最新版本", hasVal(up.latest) ? up.latest : "-",
        up.hasUpdate
          ? html`<${A.Tag} color="processing">有新版本<//>`
          : hasVal(up.latest) ? html`<span style=${sm}>已是最新</span>` : null)}
      ${row("上次检查", hasVal(up.lastCheck) ? up.lastCheck : "还没有查过")}
      ${up.checkError ? html`<div className="dc-set-hint" style=${{ color: token.colorWarning }}>
        ${"上次没能问到：" + up.checkError}</div>` : null}
      <${A.Space} className="dc-set-acts">
        <${A.Button} icon=${e(IconRefresh)} loading=${busy === "check"}
          onClick=${function () {
            run("check", function () { return call("updateCheck"); });
          }}>检查更新<//>
        <${A.Button} type="text" className="dc-quiet" onClick=${showNotes}>查看发布说明<//>
      <//>
    </${React.Fragment}>`;

    // 换不了自己的那两种（自己编的、go install 装的）：不摆「下载更新」按钮。
    // 装不上却让人先花几分钟下几十兆，是骗人的；原因就在卡片副标题上。
    var noInstall = up && up.hasUpdate && !up.canInstall;

    var general = html`<${React.Fragment}>
      <section className="dc-card">
        <div className="dc-card-head">
          <span className="dc-card-title">版本</span>
          <span className="dc-card-sub">${up ? up.installHint : "正在读取…"}</span>
        </div>
        ${updAlert}
        ${version}
        ${updBody ? html`<div className="dc-set-note"
          style=${{ background: token.colorFillTertiary }}>${updBody}</div>` : null}
        ${noInstall ? html`<${A.Alert} type="info" showIcon className="dc-card-alert"
          message="这一份 Pier 不会自己替换自己"
          description="新版本可以从发布页手动装一次，装的时候会盖掉同一位置上的那一份。"/>` : null}
      </section>

      <section className="dc-card">
        <div className="dc-card-head">
          <span className="dc-card-title">自动检查</span>
          <span className="dc-card-sub">隔几小时问一次 GitHub，有新版本就在侧栏亮一颗点</span>
        </div>
        <div className="dc-set-row">
          <span className="dc-set-key">自动检查更新</span>
          <span className="dc-set-flex"/>
          <${A.Switch} checked=${up ? up.autoCheck : true} loading=${busy === "auto"}
            onChange=${saveAuto}/>
        </div>
        <div className="dc-set-hint" style=${sm}>关掉之后只是不再自己去问；上面的「检查更新」随时可用。</div>
      </section>
    </${React.Fragment}>`;

    var appearance = html`<section className="dc-card">
      <div className="dc-card-head">
        <span className="dc-card-title">主题</span>
        <span className="dc-card-sub">跟随系统时会随系统外观切换</span>
      </div>
      <${A.Segmented} value=${th.pref} onChange=${th.set}
        options=${THEMES.map(function (t) { return { value: t.key, label: t.label, icon: e(t.icon, { size: 13 }) }; })}/>
    </section>`;

    // 「数据」那一栏：手上这份清单在哪、怎么把它带走、怎么换一份，以及进程记录的收尾。
    //
    // 前四件事原来挤在顶栏的「⋯」里——主区每时每刻最显眼的那一排摆着一颗和启停无关的
    // 按钮，而点开之后是一组「关于这份数据」的操作，跟当时屏幕上那一页的服务没关系。
    // 挪到这里之后顶栏只剩启停与添加（那两件确实是按页生效的），而这一页本来
    // 就只在要改设置的时候才打开。
    //
    // 代价是清楚的：打开只读清单之后，那一页上不再有一眼可见的「现在看的是哪一份」。
    // 所以只读时主区顶上那条告警里写着去哪儿切回来，这一栏里那份是完整的。
    var cfgReadOnly = !!(data && data.readOnly);
    var cfgPath = (data && data.configPath) || "";
    var cfgDir = (data && data.configDir) || "";
    // 只读时写文件名，本机数据时写「本机数据」：一个是「这份 YAML」，一个是
    // 「你自己那套」，指名的说法不一样。取文件名要按 / 切，清单路径一律是绝对路径。
    var cfgName = cfgReadOnly ? cfgPath.split("/").pop() : "本机数据";
    // 主目录写成 ~：数据目录就是 ~/.pier，完整的 /Users/xxx/.pier 在这一行里
    // 只会被截断成前几个目录。只读那份在哪儿就照实写——它不是数据目录。
    var cfgShort = cfgDir.replace(/^\/Users\/[^/]+/, "~");

    var manifest = html`<${React.Fragment}>
      <section className="dc-card">
        <div className="dc-card-head">
          <span className="dc-card-title">清单</span>
          <span className="dc-card-sub">这份清单在哪、怎么带走、怎么换一份</span>
        </div>
        <div className="dc-set-row">
          <span className="dc-set-key" style=${sm}>数据来源</span>
          <span className="dc-set-val">
            <span className="dc-src" title=${cfgPath ? cfgName + " · " + cfgPath : cfgName}>
              <span className="dc-src-name">${cfgName}</span>
              ${"" /* 来源只在不寻常时才标：平时就是「本机数据」，和名字重复。 */}
              ${data && data.configSource && data.configSource !== "本机数据" ? html`<span
                className="dc-src-tag" style=${{ background: token.colorFillTertiary }}
                >${data.configSource}</span>` : null}
              ${cfgShort ? html`<span className="dc-src-dir dc-mono" title=${cfgDir}>${cfgShort}</span>` : null}
            </span>
          </span>
          <${A.Button} icon=${e(IconFolder)} loading=${busy === "reveal"}
            onClick=${function () {
              runSrc("reveal", function () { return call("revealConfig"); });
            }}>${cfgReadOnly ? "在" + FILEMGR + "中显示清单" : "打开数据目录"}<//>
        </div>
        <${A.Space} className="dc-set-acts">
          <${A.Button} icon=${e(IconDoc)} loading=${busy === "yaml"}
            onClick=${function () {
              runSrc("yaml", async function () {
                var y = await call("configYAML");
                await call("copyText", y.text);
                props.message.success("已复制清单 YAML");
              });
            }}>复制成 YAML<//>
          <${A.Button} icon=${e(IconLayers)} loading=${busy === "export"}
            onClick=${function () {
              runSrc("export", async function () {
                var r = await call("exportConfig");
                props.message.success(r.canceled ? "已取消" : (r.msg || "已导出"));
              });
            }}>导出清单…<//>
          <${A.Button} icon=${e(IconFolder)} loading=${busy === "open"}
            onClick=${function () {
              runSrc("open", async function () {
                var r = await call("openConfig");
                props.message.success(r.canceled ? "已取消" : (r.msg || "已打开"));
              });
            }}>打开清单…<//>
          ${"" /* 选错了要能退回来：打开的如果是 YAML，整份就只读、编辑入口全收起，
                 而没有这一颗的话，双击启动的人只能重启一次 Pier。 */}
          ${cfgReadOnly ? html`<${A.Button} icon=${e(IconLayers)} loading=${busy === "local"}
            onClick=${function () {
              runSrc("local", async function () {
                var r = await call("useLocalConfig");
                props.message.success(r.msg || "已切回本机数据");
              });
            }}>回到本机数据<//>` : null}
        <//>
      </section>

      <section className="dc-card">
        <div className="dc-card-head">
          <span className="dc-card-title">维护</span>
          <span className="dc-card-sub">进程记录与实际对不上时的手工收尾</span>
        </div>
        <div className="dc-set-row">
          <span className="dc-set-key" style=${sm}>残留记录</span>
          <span className="dc-set-flex"/>
          <${A.Button} icon=${e(IconBroom)} loading=${busy === "prune"}
            onClick=${function () {
              runSrc("prune", async function () {
                var r = await call("prune");
                props.message.success(r.msg || "已清理");
              });
            }}>清理<//>
        </div>
        <div className="dc-set-hint" style=${sm}>
          服务崩溃、或被手工结束之后，状态文件里会留下一条指向已死进程的记录，
          界面上那条服务就会一直显成在跑。走「停止」消失的进程后端会顺手清掉，
          这里是没有走过「停止」时的那条路。
        </div>
      </section>
    </${React.Fragment}>`;

    var tabs = [["general", "通用"], ["appearance", "外观"], ["data", "数据"]];

    return html`<div className="dc-set">
      <div className="dc-set-tabs">
        ${tabs.map(function (it) {
          return html`<div key=${it[0]}
            className=${"dc-set-tab" + (tab === it[0] ? " is-on" : "")}
            onClick=${function () { setTab(it[0]); }}>${it[1]}</div>`;
        })}
      </div>
      ${"" /* 上一次替换的结果不在这儿再摆一份：主区顶上已经有一条（见 App 里的
             res），而这一页就在主区里——同一句话并排出现两次，读的人会以为
             出了两次事。换完文件重新起来落在的是服务列表，那条告警跟着走，
             在哪儿都看得见。 */}
      <div className="dc-set-body">
        ${tab === "appearance" ? appearance : tab === "data" ? manifest : general}
      </div>
    </div>`;
  }

  // ── 主界面 ───────────────────────────────────────────────────────────────

  function App(props) {
    // 主题状态由 Root 持有并传下来，这里绝不能再调一次 useTheme()。
    //
    // 踩过的坑：两边各调一次 useTheme()，就是两份互不相干的 useState。
    // 侧栏那个 Segmented 改的是 App 这份，而决定 antd 算法的 ConfigProvider
    // 在 Root 里读的是另一份——点「暗色」什么都不会发生，只有 localStorage
    // 被悄悄改了，下次启动才生效。表现是「切了没反应」，很难联想到状态分裂。
    var th = props.th;
    var st = React.useState(null), data = st[0], setData = st[1];
    var es = React.useState(""), stateErr = es[0], setStateErr = es[1];
    var bs = React.useState(""), banner = bs[0], setBanner = bs[1];
    var sel = React.useState("all"), selected = sel[0], setSelected = sel[1];
    var lg = React.useState({ open: false, name: "" }), log = lg[0], setLog = lg[1];
    var po = React.useState({ open: false, name: "" }), portOwner = po[0], setPortOwner = po[1];
    var fm = React.useState({ open: false, editing: null }), form = fm[0], setForm = fm[1];
    // 扫描本机端口：单独一个开关，不挂在 form 上——那一屏有自己的取数与重扫，
    // 和表单的开合没有关系，混成一个状态只会让「关掉表单」顺手把扫描也关掉。
    var sc = React.useState(false), scanOpen = sc[0], setScanOpen = sc[1];
    var del = React.useState({ open: false, svc: null }), delState = del[0], setDel = del[1];
    var gm = React.useState({ open: false, mode: "create", target: "" }), grp = gm[0], setGrp = gm[1];
    var nm = React.useState(""), navMenu = nm[0], setNavMenu = nm[1];
    // 搜索与快捷筛选（纯前端，见下面 matchSvc）。这两个值不随换页清空：
    // 筛着筛着点进一个分组接着看，是很自然的下一步。
    var sq = React.useState(""), search = sq[0], setSearch = sq[1];
    var qf = React.useState("all"), quick = qf[0], setQuick = qf[1];
    var searchRef = React.useRef(null);
    // 拖动排序：正在拖的名字（服务与分组各一份）和落在哪一行的哪半边。
    // 落点存成 {name, after} 而不是一个下标：行是会重排的，下标一会儿就对不上了。
    var dsv = React.useState(null), dragSvc = dsv[0], setDragSvc = dsv[1];
    var dpt = React.useState(null), dropOn = dpt[0], setDropOn = dpt[1];
    var dgr = React.useState(null), dragGrp = dgr[0], setDragGrp = dgr[1];
    var dgt = React.useState(null), grpDrop = dgt[0], setGrpDrop = dgt[1];

    var msg = A.App.useApp();

    // refresh 把最新状态拉回来。轮询循环和每个动作之后都走它这一条路。
    //
    // 不给「刚点过的动作」另开一条自己改本地状态的近路：那样界面上的 op
    // 就成了前端自己编的，后端真排队失败了也不会露出来。这里只做「重新读一遍」，
    // op 的文案和阶段仍然只有一个来源，就是后端。
    var busyRef = React.useRef(0);
    var refresh = async function () {
      if (document.hidden) return;
      try {
        var d = await call("state");
        setData(d);
        setStateErr("");
        // 下一跳的间隔按这个值分档，所以要存在 ref 里而不是 state 里：
        // 存 state 会触发重渲染，而重渲染又会重建 refresh，循环那边拿到的
        // 永远是旧的那份。
        busyRef.current = (d && d.busyCount) || 0;
      } catch (ex) {
        setStateErr(ex.message);
      }
    };
    // 轮询循环只在挂载时建一次，拿不到后续渲染新建的 refresh，所以用 ref 递进去。
    var refreshRef = React.useRef(refresh);
    refreshRef.current = refresh;

    // 轮询状态。窗口在后台时不再刷——那既没意义，又会让 Maven 构建期间
    // 每分钟多跑几十次健康探测。
    //
    // 循环用 setTimeout 自续而不是 setInterval：间隔要按「有没有动作在跑」
    // 分两档，而忙闲只有每次刷新回来才知道。自续的写法每跳都重算下一次的
    // 间隔，一个动作开始或结束时不必重建这个循环。
    React.useEffect(function () {
      var alive = true;
      var t = null;
      var tick = async function () {
        if (alive) await refreshRef.current();
        if (!alive) return;
        // 窗口不可见时按空闲档走：那会儿刷新本来就直接返回，
        // 按忙时档空转只是白唤醒。
        t = setTimeout(tick, busyRef.current > 0 && !document.hidden
          ? STATE_POLL_BUSY_MS : STATE_POLL_MS);
      };
      tick();
      // 窗口从后台回到前台时立刻刷一次：不可见期间刷新是停着的，
      // 等下一跳轮询的话，切回来会先看到最多 5 秒前的旧状态（刚启动时就是一片骨架屏）。
      var onVisible = function () { if (!document.hidden && alive) refreshRef.current(); };
      document.addEventListener("visibilitychange", onVisible);
      return function () {
        alive = false; clearTimeout(t);
        document.removeEventListener("visibilitychange", onVisible);
      };
    }, []);

    // 更新状态。与上面那条轮询并列，但两者说的不是一件事：那条读的是服务进程，
    // 这条读的是「有没有新版本」。分开两条而不是合并成一个绑定，是因为间隔不同——
    // 服务列表 5 秒一跳，而更新状态跳一次读一遍 settings.json，没必要陪着一起跑。
    //
    // 这一条不出网：真正去问 GitHub 的那次在 Go 的后台协程里，这里只是把结果取回来。
    // 所以间隔按本地调用的尺度给——平时一分钟一跳，只为让侧栏那颗圆点自己出现；
    // 正在检查或下载时一秒一跳，那条进度条得动起来。
    //
    // 状态只有这一份：侧栏页脚那颗圆点与偏好设置页读的是同一个 up，
    // 两处各轮一次迟早一前一后对不上，页面上说的和圆点亮的就不是同一件事。
    var us = React.useState(null), up = us[0], setUp = us[1];
    // 上一次替换留下的结果。变量名必须是 res：gui/app_test.go 按变量名比对字段，
    // 而 result 那一层是 update.ApplyResult，不在 StatusOut 里。
    //
    // 这是「上次更新成没成」唯一的去处：助手跑在 Pier 已经退出的空档里，
    // 没有窗口能报错，它只能把这句写进结果文件等下一次启动。所以主区顶上摆一条，
    // 偏好设置页里也有一份（见那一页）——换完文件重新起来落在的是服务列表。
    var res = (up && up.result) || null;
    var upBusyRef = React.useRef(false);
    var upRefresh = async function () {
      if (document.hidden) return;
      try {
        var d = await call("updateStatus");
        setUp(d);
        upBusyRef.current = !!(d && (d.checking || d.downloading));
      } catch (ex) {
        // 读不到更新状态不该在主界面上报错：它既不影响启停，也不影响日志，
        // 让人为此看到一条红字只会以为 Pier 坏了。偏好设置页那边会看到空白。
        console.error("Pier 界面：读取更新状态失败", ex);
      }
    };
    var upRefreshRef = React.useRef(upRefresh);
    upRefreshRef.current = upRefresh;

    // 与上面那条同一个写法：循环只在挂载时建一次，间隔按上一跳的结果重算。
    React.useEffect(function () {
      var alive = true;
      var t = null;
      var tick = async function () {
        if (alive) await upRefreshRef.current();
        if (!alive) return;
        t = setTimeout(tick, upBusyRef.current && !document.hidden
          ? STATE_POLL_BUSY_MS : UPDATE_POLL_MS);
      };
      tick();
      var onVisible = function () { if (!document.hidden && alive) upRefreshRef.current(); };
      document.addEventListener("visibilitychange", onVisible);
      return function () {
        alive = false; clearTimeout(t);
        document.removeEventListener("visibilitychange", onVisible);
      };
    }, []);

    var flash = function (kind, text) {
      setBanner(text);
      if (kind === "ok") msg.message.success(text);
      else if (kind === "err") msg.message.error(text);
    };

    // 所有动作都走后端队列。这里只负责发出去和显示结果，不在前端自己维护
    // 「我刚点了什么」——那份状态迟早会和真实情况对不上。
    //
    // 发完立刻 refresh 一次，而不是等下一跳轮询：后端在入队那一刻就把 op
    // 写好了（internal/panel 的 enqueue），这一下读回来就能让按钮当场转起来。
    var act = async function (kind, svc) {
      if (kind === "logs") { setLog({ open: true, name: svc.name }); return; }
      if (kind === "occupant") { setPortOwner({ open: true, name: svc.name }); return; }
      if (kind === "edit") { setForm({ open: true, editing: svc }); return; }
      // 复制出来的是另一条服务，直接把它开在表单里：端口已经换过、健康检查也
      // 清掉了，剩下的往往还要补两笔（目录指向另一个人、备注）。要的是刚建出来的
      // 那一条本身，所以复制完重新取一次状态再找它——不等下一跳轮询，
      // 也不从「已复制为 xxx」那句话里抠名字。
      if (kind === "duplicate") {
        try {
          var made = await call("duplicateService", svc.name);
          flash("ok", made.msg || "已复制");
          var d = await call("state");
          setData(d);
          setStateErr("");
          var hit = ((d && d.services) || []).filter(function (x) { return x.name === made.name; })[0];
          if (hit) setForm({ open: true, editing: hit });
        } catch (ex) { flash("err", ex.message); }
        return;
      }
      if (kind === "dir") { call("reveal", svc.name); return; }
      // 复制成 YAML：文字由后端按清单里的字段定义生成，这里只负责送进剪贴板。
      // 分两步是有意的——剪贴板只有宿主那条路（网页自己的剪贴板 API 在这个
      // 页面里会静默失败，见 window_darwin.go 的 pierCopyText）。
      if (kind === "yaml") {
        try {
          var y = await call("serviceYAML", svc.name);
          await call("copyText", y.text);
          flash("ok", "已复制 " + svc.name + " 的 YAML");
        } catch (ex) { flash("err", ex.message); }
        return;
      }
      if (kind === "health") { call("openHealth", svc.name).catch(function (ex) { flash("err", ex.message); }); return; }
      if (kind === "delete") {
        setDel({ open: true, svc: svc });
        return;
      }
      try {
        var r = await call(kind, svc.name);
        if (r.msg) flash("ok", r.msg);
      } catch (ex) {
        flash("err", ex.message);
      }
      refresh();
    };

    var services = (data && data.services) || [];
    var groups = (data && data.groups) || [];
    var ungrouped = (data && data.ungroupedName) || "未分组";
    var busyCount = (data && data.busyCount) || 0;

    // 命令行指定了 YAML 清单时整份只读：收起新增、编辑、分组操作、拖动。
    // 定义挪到这一处（原来在下面和标题挨着）：拖动那一套要用它，
    // 而 JS 的 var 声明会提前、赋值不会——写在后面读到的就是 undefined。
    var readOnly = !!(data && data.readOnly);

    // ── 出事时叫一声 ───────────────────────────────────────────────────────
    //
    // 数的是**全量**，不跟着当前这一页或搜索框走：Dock 角标说的是「这个应用里
    // 还有事」，切成「只看在跑」就把它清掉，等于把通知交给了筛选状态。
    // 口径与页头那一格是同一个 needsAttention。
    var attentionAll = services.filter(needsAttention).length;
    // null 表示第一轮还没算过，见下。
    var lastAttention = React.useRef(null);
    React.useEffect(function () {
      var prev = lastAttention.current;
      if (prev === attentionAll) return;
      lastAttention.current = attentionAll;
      // 失败就算了：这是锦上添花的提示，为它弹一条错误反而更吵。
      call("badge", attentionAll ? String(attentionAll) : "").catch(function () {});
      // 只在「本来没事、现在有事」时跳一下。第一轮（prev 是 null）也不跳——
      // 那是刚启动，窗口本来就在最前面，跳一下纯属白跳；要叫人的是
      // 「开着开着，有服务出事了」。
      if (prev === 0 && attentionAll > 0) call("bounce").catch(function () {});
    }, [attentionAll]);

    // ── 快捷键 ─────────────────────────────────────────────────────────────
    //
    // 四个键全走 JS，不往原生菜单里加：菜单项的 key equivalent 会先于网页拿到
    // 按键，而现有那三套菜单占的是 ⌘H/⌘Q/⌘Z/⌘X/⌘C/⌘V/⌘A/⌘M/⌘W，⌘K/⌘R/⌘N
    // 都是空的。为它们新造一个 ObjC target 回叫 Go（全仓没有先例）不值当。
    // ⌘F 不在这里，在抽屉自己身上——见 LogDrawer。
    //
    // 可发现性靠控件上的 title：搜索框写「⌘K」，「添加应用」写「⌘N」。
    React.useEffect(function () {
      var onKey = function (ev) {
        if (!(ev.metaKey || ev.ctrlKey)) return;
        var k = (ev.key || "").toLowerCase();
        if (k === "k") {
          var box = searchRef.current;
          if (!box) return;
          ev.preventDefault();
          // 已经在搜索框里了就当作「再按一次收回来」：这时人想要的是
          // 把名单放回全量，而不是把光标原地挪一下。
          if (document.activeElement === (box.input || box)) setSearch("");
          else if (box.focus) box.focus();
          return;
        }
        if (k === "r") {
          // 不拦的话 WebView 会去刷新整个页面——那是重新喂一次 HTML，
          // 界面整个重来一遍，看起来就是闪了一下。
          ev.preventDefault();
          refreshRef.current();
          return;
        }
        if (k === "n") {
          if (readOnly) return;
          ev.preventDefault();
          setForm({ open: true, editing: null });
        }
      };
      document.addEventListener("keydown", onKey);
      return function () { document.removeEventListener("keydown", onKey); };
    }, [readOnly]);

    // 侧栏选中某组时只显示该组；「全部」显示所有分组。
    var shown = selected === "all" ? groups
      : groups.filter(function (g) { return g.name === selected; });

    var inGroup = function (g) {
      return services.filter(function (s) { return s.group === g; });
    };

    var failedIn = function (g) {
      return inGroup(g).some(function (s) { return s.statusKey === "stale" || s.opErr; });
    };

    // ── 搜索与快捷筛选 ─────────────────────────────────────────────────────
    //
    // 纯前端过滤，只收窄眼前这份名单：后端不认识「筛选」，状态、概览、启停
    // 都还是照完整清单算的。收窄过这件事写在页头副标题里（3 / 12 个服务），
    // 不然「怎么只剩这几个」第一反应是数据丢了。
    //
    // 「异常」那一档用的是 needsAttention，与概览里「N 个需要关注」同一个口径：
    // 另写一份判据的话，两处的数字迟早对不上，而它们说的是同一件事。
    var needle = search.trim().toLowerCase();
    var filtering = !!needle || quick !== "all";
    var matchSvc = function (s) {
      if (quick === "running" && !(s.running || s.statusKey === "starting")) return false;
      if (quick === "attention" && !needsAttention(s)) return false;
      if (!needle) return true;
      // 名字、目录、端口都算命中：想找某个服务时，手里常常只有一个端口号，
      // 或者记得它在哪个目录下，偏偏记不全它叫什么。
      return (s.name + "\n" + s.dir + "\n" + (s.port > 0 ? String(s.port) : ""))
        .toLowerCase().indexOf(needle) >= 0;
    };
    var listIn = function (g) { return inGroup(g.name).filter(matchSvc); };

    // 把几个服务的占用加起来。只有在筛选之后才需要自己算：不筛时后端已经算好了
    // 整页的和每个分组的合计，界面上不必再算一遍。相加用的每一项仍然是后端报的。
    var sumUsage = function (list) {
      return list.reduce(function (acc, s) {
        if (!s.running || !s.usage) return acc;
        acc.cpu += s.usage.cpu || 0;
        acc.memBytes += s.usage.memBytes || 0;
        acc.procs += s.usage.procs || 0;
        return acc;
      }, { cpu: 0, memBytes: 0, procs: 0 });
    };

    // ── 拖动排序 ───────────────────────────────────────────────────────────
    //
    // 送给后端的是「屏幕上从上到下那一串名字」，由它把这些名字填回它们此刻
    // 占着的位置——分组页或筛选中拖动时，看不见的服务不会被挤走。
    // 语义写在 internal/config 的 ReorderServices 上，界面这边只管顺序从眼睛来。
    //
    // 只允许在同一组内换位置。跨组拖动没法用「换顺序」表达（那是改分组，
    // 走编辑表单），而且真按顺序存下去会立刻弹回来：主区是按分组分块渲染的，
    // 一组的两行之间隔着别的组，换个位置看不出来。
    var reorder = function (list, moving, target, after) {
      var names = list.slice();
      var from = names.indexOf(moving);
      if (from < 0) return null;
      names.splice(from, 1);
      var to = names.indexOf(target);
      if (to < 0) return null;
      names.splice(after ? to + 1 : to, 0, moving);
      return names;
    };
    var sameOrder = function (a, b) { return a.join("\u0000") === b.join("\u0000"); };

    // 主区从上到下的那一串服务名：分组的渲染顺序、组内顺序都要和这里一致。
    var flatRows = function () {
      var out = [];
      visible.forEach(function (g) {
        listIn(g).forEach(function (s) { out.push(s.name); });
      });
      return out;
    };

    var moveDone = function (p) {
      p.then(function () { refresh(); }).catch(function (ex) { flash("err", ex.message); });
    };

    // 落点只在真的换了一行时更新：dragover 每移动几像素就来一次，
    // 每次都 setState 会让整页跟着鼠标抖。
    var setDropOnIfMoved = function (name, after) {
      setDropOn(function (cur) {
        if (cur && cur.name === name && cur.after === after) return cur;
        return { name: name, after: after };
      });
    };
    var setGrpDropIfMoved = function (name, after) {
      setGrpDrop(function (cur) {
        if (cur && cur.name === name && cur.after === after) return cur;
        return { name: name, after: after };
      });
    };
    // 指针在某一行里偏向哪半边。取行自己的矩形，不取整段列表的。
    var halfOf = function (ev) {
      var r = ev.currentTarget.getBoundingClientRect();
      return ev.clientY > r.top + r.height / 2;
    };

    var endDrag = function () { setDragSvc(null); setDropOn(null); };

    // 服务行的拖动：readOnly（命令行指定的 YAML）下整段不开，
    // 那时拖了也没处存，draggable 挂上去只会让人以为能拖。
    var svcDnd = (data && !readOnly) ? {
      moving: dragSvc ? dragSvc.name : "",
      // 落点叫 target，不能叫 drop：这个对象里还有一个名字更该归它的成员——
      // 「放手时做什么」那个函数。两个都叫 drop 时后写的那个静静地赢，
      // dnd.drop 就成了函数，下面 `dnd.drop.name === s.name` 永远不成立，
      // 那条落点线一次也画不出来；而 node --check、go vet 和截图全都看不见
      // （线不画只是少了个提示，拖动本身照常工作）。见 app_test.go 里那条键名扫描。
      target: dropOn,
      start: function (s, ev) {
        setDragSvc(s);
        // setData 不是给接收方读的（同页面自己处理）：某些浏览器不设它就不肯开始拖。
        ev.dataTransfer.effectAllowed = "move";
        ev.dataTransfer.setData("text/plain", s.name);
      },
      over: function (s, ev) {
        if (!dragSvc || dragSvc.name === s.name) return;
        // 别的组：不给落点，光标回成「禁止」，也不画那条线。
        //
        // 还要把上一次那条线收掉：它是上一行留下的，此刻指针根本不在那儿，
        // 留着就是一条指向别处的线——光标已经说了「这儿不行」，线却指着另一个
        // 位置说「落这儿」。清成 null 重复调用不会重渲染（同一个值），
        // dragover 每几像素来一次也不怕。
        if (s.group !== dragSvc.group) { ev.dataTransfer.dropEffect = "none"; setDropOn(null); return; }
        ev.preventDefault();
        ev.dataTransfer.dropEffect = "move";
        setDropOnIfMoved(s.name, halfOf(ev));
      },
      drop: function (s, ev) {
        ev.preventDefault();
        // 先把这一份留下来再清状态：清完 dragSvc 就没了，而下面还要用它判组。
        var from = dragSvc;
        endDrag();
        if (!from || from.name === s.name || from.group !== s.group) return;
        var before = flatRows();
        var names = reorder(before, from.name, s.name, halfOf(ev));
        if (!names || sameOrder(names, before)) return;
        moveDone(call("moveServices", JSON.stringify(names)));
      },
      end: endDrag
    } : null;

    // 侧栏分组的拖动。「未分组」是内置的桶（后端也拒绝给它排序），
    // 它不参与拖动，也不是落点——让一个拖不动的行当落点，线画在那儿却什么都没发生。
    var draggableGroups = groups.filter(function (g) { return !g.builtin; })
      .map(function (g) { return g.name; });
    var grpDnd = (data && !readOnly) ? {
      moving: dragGrp || "",
      target: grpDrop,
      start: function (name, ev) {
        setDragGrp(name);
        ev.dataTransfer.effectAllowed = "move";
        ev.dataTransfer.setData("text/plain", name);
      },
      over: function (name, ev) {
        if (!dragGrp || dragGrp === name) return;
        ev.preventDefault();
        ev.dataTransfer.dropEffect = "move";
        setGrpDropIfMoved(name, halfOf(ev));
      },
      drop: function (name, ev) {
        ev.preventDefault();
        var moving = dragGrp;
        setDragGrp(null); setGrpDrop(null);
        if (!moving || moving === name) return;
        var names = reorder(draggableGroups, moving, name, halfOf(ev));
        if (!names || sameOrder(names, draggableGroups)) return;
        moveDone(call("moveGroups", JSON.stringify(names)));
      },
      end: function () { setDragGrp(null); setGrpDrop(null); }
    } : null;

    var cur = shown[0];

    // SDK 管理与日志是侧栏里的一页，不属于任何一个分组。做成一个保留的选中值
    // 而不是另开一套路由：侧栏本来就是「选一个东西看它」的那套模型，
    // 多一层路由会多出一份「现在到底在看哪」的状态。
    //
    // 两页都在「设置」那一节下，也就是都不看服务：页头不摆启停与「添加应用」，
    // 副标题也不说「N 个在跑」——那些说的都是服务，摆在讲 SDK / 讲日志的页头下，
    // 点下去和眼前这一页毫无关系。
    var SDK_KEY = "__sdk";
    var LOG_KEY = "__logs";
    var SETTINGS_KEY = "__settings";
    var onSDKPage = selected === SDK_KEY;
    var onLogPage = selected === LOG_KEY;
    // 偏好设置页不在「设置」那一节里（它在侧栏页脚），但页头的规矩一样：
    // 说的都是本机的东西，不是某一组服务，所以启停按钮与副标题的计数同样撤掉。
    var onSettingsPage = selected === SETTINGS_KEY;
    var onToolPage = onSDKPage || onLogPage || onSettingsPage;

    var title = onSDKPage ? "SDK 管理"
      : onLogPage ? "日志管理"
      : onSettingsPage ? "偏好设置"
      : (selected === "all" ? "全部服务" : (cur ? cur.name : selected));

    // 骨架的底色由这里出，不交给 antd 的 Layout/Sider 自己算。
    //
    // 起因是一个只在暗色下出现的故障：Sider 的基样式是 background: transparent，
    // 真正的颜色挂在 -light/-dark 两个变体类上；而开了 darkAlgorithm 之后
    // 那个 -dark 规则压根没被生成，于是整个页面露出浏览器默认的白底，
    // 白字写在白底上，什么都看不见。用 token 自己刷底色，
    // 深浅两套就都跟着算法走，也不会再被组件的类名策略牵着走。
    var token = A.theme.useToken().token;
    var shellBg = { background: token.colorBgLayout, color: token.colorText };
    // 侧栏不再自己铺底色，露出整页的灰——只有主区那一块是白的。
    // 这里仍然显式写死 transparent，不只在 token 里配：见上面那段关于
    // Sider 变体类在暗色下没被生成的说明，自己刷一遍最稳。
    var sideBg = { background: "transparent" };
    // 主区是一块浮在灰底上的白色面板，页头下面收一条发丝线。
    var mainBg = { background: token.colorBgContainer, boxShadow: token.boxShadowTertiary };
    var headLine = { borderBottom: "1px solid " + token.colorBorderSecondary };

    // body 也要刷：卡片之外的地方露出来的就是画布本身，不改它的话
    // 暗色下滚动到列表末尾会看到一条白边。
    //
    // 顺带把滚动条那两个颜色挂到根元素上。挂在根上而不是就近挂在各个容器上，
    // 是因为滚动条散落在主区、侧栏、弹窗体、日志区，还在运行期才出现的
    // 弹窗 DOM 里——逐个去贴 style 必然漏。写成自定义属性，app.css 那边
    // 一条 ::-webkit-scrollbar 规则就全覆盖了，切主题也自动跟着变。
    // 色值本身仍然只来自 PALETTE，不落到 CSS 里。
    React.useEffect(function () {
      document.body.style.background = token.colorBgLayout;
      document.body.style.color = token.colorText;
      var root = document.documentElement.style;
      root.setProperty("--dc-scroll-thumb", token.dcScrollThumb);
      root.setProperty("--dc-scroll-thumb-hover", token.dcScrollThumbHover);
      // 列表行的分隔线与悬停底色、概览格子的描边，同样只从 token 递过去。
      root.setProperty("--dc-line", token.colorBorderSecondary);
      root.setProperty("--dc-hover", token.colorFillQuaternary);
      root.setProperty("--dc-surface", token.colorBgContainer);
      root.setProperty("--dc-text2", token.colorTextSecondary);
      root.setProperty("--dc-text3", token.colorTextTertiary);
      root.setProperty("--dc-fill", token.colorFillTertiary);
      root.setProperty("--dc-primary", token.colorPrimary);
      root.setProperty("--dc-primary-bg", token.colorPrimaryBg);
      // 警示色：服务列表上「本机没有它要的版本」那一条、工具链预演里的失败行
      // 都用它。色值本身仍然只来自 PALETTE，不落到 CSS 里。
      root.setProperty("--dc-warning", token.colorWarning);
      // 标题栏是透明的，露出来的是窗口自己的底色：刷成和页面同一个灰，
      // 标题栏和侧栏才连成一片。演示模式下没有窗口，跳过。
      if (!DEMO) call("setChrome", token.colorBgLayout, !!th.dark).catch(function (ex) {
        console.error("Pier 界面：同步窗口底色失败", ex);
      });
    }, [token.colorBgLayout, token.colorText, token.dcScrollThumb, token.dcScrollThumbHover,
      token.colorBorderSecondary, token.colorFillQuaternary, token.colorBgContainer, token.colorTextSecondary,
      token.colorTextTertiary, token.colorFillTertiary, token.colorPrimary, token.colorPrimaryBg,
      token.colorWarning,
      th.dark]);

    // 演示模式下挂一个开弹窗的钩子，供截图核对用。
    //
    // 界面里有六个弹窗，全都只能靠点出来，而核对截图时没人去点。「弹窗里整排
    // 显示 undefined」正是这么漏过去的：主界面看着好好的，一开弹窗才发现字段
    // 名对不上。有了这个钩子就能把每个弹窗逐个拍下来看。
    //
    // 只在 ?demo=1 时挂载——真实窗口是用 SetHtml 喂进去的，不带查询串，
    // 这个钩子在真实运行里根本不存在。
    React.useEffect(function () {
      if (!DEMO) return;
      // 找不到就叫出来。这里原先「找不到就退回第一个服务」，于是名字写错、
      // 或者状态还没拉回来（首次渲染时 services 是空的）时，弹窗会打开成
      // 另一个服务，或者干脆什么都不显示——两种都不会报错，只会在核对截图时
      // 表现为「这个弹窗没打开」，很容易被当成弹窗本身坏了。
      var find = function (n) {
        for (var i = 0; i < services.length; i++) if (services[i].name === n) return services[i];
        console.error("演示钩子：找不到服务 " + n + "（当前有 " + services.length + " 个）");
        return null;
      };
      window.__pierDemo = {
        names: function () {
          return {
            services: services.map(function (s) { return s.name; }),
            groups: groups.map(function (g) { return g.name; })
          };
        },
        // 深浅两套要各拍一遍。主题平时只由侧栏的 Segmented 切，
        // 而从 file:// 打开的预览页没法预先写 localStorage，所以留个口子。
        theme: function (v) { th.set(v); },
        // 只读清单（命令行 --config，或者偏好设置里那个「打开清单…」）平时要看一眼
        // 得真去启动一次。翻的是上面那两个模块变量，也就是 demoState 的出题口，
        // 这样每 5 秒那一跳取回来的还是只读的那一份。
        // 第二个参数是来源，用来分别拍「命令行指定」和「打开清单…」那两种说法。
        // 直接摆一份新的上去，不走 refresh：那条路要等一次异步的 state 回来，
        // 后台标签页里 setTimeout 被节流到秒级，截图的人等不到它。
        readonly: function (on, src) {
          demoReadOnly = on !== false;
          demoReadOnlySrc = src || "";
          setData(demoState());
        },
        // 偏好设置页的那几种样子：有新版本、下载中、下载好了、跳过了、
        // 下载失败、查失败、上次换文件失败。真要去 GitHub 上查一次、
        // 再正好赶上一版新的才看得到，而它们正是这一页最需要核对排版的地方。
        //
        // 直接摆一份上去并当场重渲染，不等下一跳轮询：后台标签页里
        // setTimeout 被节流到秒级，截图的人等不到它。
        update: function (kind) {
          var u = demoUpdateState();
          // 每一种都从「有新版本、还没下」那个底子重来，免得上一回摆下的
          // done / error 还挂在上面，两张截图叠在一起分不清是谁的。
          u.skipped = false; u.downloading = false; u.done = false;
          u.error = ""; u.checkError = ""; u.result = null;
          u.received = 0; u.total = 0; u.progress = 0;
          u.receivedSize = "0 B"; u.totalSize = "0 B";
          u.hasUpdate = true; u.latest = "0.3.0"; u.autoCheck = true; u.canInstall = true;
          if (kind === "downloading") {
            u.downloading = true; u.total = 23173530;
            u.received = 12976128; u.progress = 56;
            u.receivedSize = "12.4 MB"; u.totalSize = "22.1 MB";
          } else if (kind === "done") {
            u.done = true; u.progress = 100;
            u.received = 23173530; u.total = 23173530;
            u.receivedSize = "22.1 MB"; u.totalSize = "22.1 MB";
          } else if (kind === "skipped") {
            u.skipped = true;
          } else if (kind === "failed") {
            u.error = "下载 0.3.0 失败：连接被对方重置（已重试 5 次）";
          } else if (kind === "checkfail") {
            u.checkError = "连不上 api.github.com：dial tcp 140.82.113.6:443: i/o timeout";
          } else if (kind === "nostall") {
            u.canInstall = false;
            u.installHint = "这一份 Pier 是从源码编译的，不会自己替换自己。";
          } else if (kind === "latest") {
            u.hasUpdate = false; u.latest = "0.2.0";
          } else if (kind === "result") {
            u.result = { ok: false, version: "0.3.0", rolledBack: true, when: "2026-10-01T14:40:12+08:00",
              message: "换文件的时候失败了：解开的新版本里没有 Pier.app 的 Contents/Info.plist" };
          } else if (kind === "resultOk") {
            u.result = { ok: true, version: "0.3.0", rolledBack: false, when: "2026-10-01T14:40:12+08:00",
              message: "换文件的过程写在 /Users/you/.pier/logs/update/2026-10-01.log 里。" };
          }
          setUp(Object.assign({}, u));
        },
        open: function (kind, arg) {
          if (kind === "log") setLog({ open: true, name: arg || "demo-admin" });
          else if (kind === "occupant") setPortOwner({ open: true, name: arg || "demo-web" });
          else if (kind === "add") setForm({ open: true, editing: null });
          else if (kind === "edit") setForm({ open: true, editing: find(arg) });
          else if (kind === "portPicker") setForm({ open: true, editing: null, openPort: true });
          // 从扫描结果里挑一条收进来：端口写死成演示数据里那个「能收进来的」，
          // 走的是和点「纳管」完全相同的两步（预演 → 填表）。
          else if (kind === "scan") setScanOpen(true);
          else if (kind === "adopt") {
            call("adoptPort", String(arg || 47840), "").then(function (info) {
              setScanOpen(false);
              setForm({ open: true, editing: null, adopt: { port: parseInt(arg, 10) || 47840, info: info } });
            }).catch(function () {});
          }
          else if (kind === "groupNew") setGrp({ open: true, mode: "create", target: "" });
          else if (kind === "groupRename") setGrp({ open: true, mode: "rename", target: arg || "示例后端" });
          else if (kind === "delete") {
            var s = find(arg);
            setDel({ open: true, svc: s });
          } else if (kind === "close") {
            setLog({ open: false, name: "" });
            setPortOwner({ open: false, name: "" });
            setForm({ open: false, editing: null });
            setGrp({ open: false, mode: "create", target: "" });
            setDel({ open: false, svc: null });
            setScanOpen(false);
          } else if (kind === "select") {
            setSelected(arg || "all");
          } else if (kind === "filter") {
            // 搜索框与快捷筛选是受控组件，照着 DOM 敲字既慢又要绕 React 的
            // value setter；截图核对要的只是「筛完之后长什么样」，直接置状态。
            setSearch(arg || "");
          } else if (kind === "quick") {
            setQuick(arg || "all");
          } else if (kind === "sdk") {
            setSelected("__sdk");
          } else if (kind === "logsPage") {
            setSelected("__logs");
          } else if (kind === "settings") {
            // 第二个参数顺带把更新那一块摆成指定的一种样子（见上面 update），
            // 截图时「点开这一页」和「这一页上是什么状态」是同一步。
            setSelected("__settings");
            if (arg) window.__pierDemo.update(arg);
          } else if (kind === "readonly") {
            window.__pierDemo.readonly(true, arg || "");
          }
        }
      };
      return function () { delete window.__pierDemo; };
    });

    // 侧栏每个分组前面那颗点：有异常标红，有在跑的标绿，其余灰。
    var groupStatus = function (name) {
      if (failedIn(name)) return "stale";
      return inGroup(name).some(function (x) { return x.statusKey === "running"; }) ? "running" : "stopped";
    };

    // 「全部」视图下不摆空着的「未分组」：它只是个兜底的桶，
    // 里面没东西时整块空状态只会把列表撑长。侧栏里仍然能点进去。
    var visible = shown.filter(function (g) { return selected !== "all" || !g.builtin || g.count > 0; });
    // 筛掉之后剩下的那一份。没筛时它就是整页的名单，筛了就是「这一页里符合的」。
    var baseList = selected === "all" ? services : inGroup(selected);
    var shownServices = baseList.filter(matchSvc);
    var tallyAll = tally(shownServices);
    // 概览那一排格子里的「M 在跑」跟着筛选走（格子里的数都来自这里），
    // 但它是「筛出来的这些里有多少在跑」——页头那个「N / M」说了收窄的范围。
    var headCount = filtering
      ? shownServices.length + " / " + baseList.length + " 个服务"
      : shownServices.length + " 个服务";
    // 一个都没筛出来：概览那块整个不摆。它这时只剩「0 / 0 在跑」「全部未启动」，
    // 而这两句话和「没有匹配的服务」并排摆着，读起来像所有服务都停了。
    var noMatch = filtering && !shownServices.length;
    // 「N 个正在操作」也按眼前这份名单数。后端给的 busyCount 是全局的，
    // 摆在「2 / 8 个服务」旁边会打架：屏幕上一个在动的都没有，却说有一个在操作。
    var busyShown = shownServices.filter(function (s) { return !!s.op; }).length;
    // 顶栏那对启停按钮作用的那一批：在「全部服务」页上是全部，在分组页上就只是这一组。
    // null 表示「全部」，交给后端的 startAll / stopAll。
    var headBatch = selected === "all" ? null : inGroup(selected);
    var headBatchLabel = selected === "all" ? "全部服务" : "「" + selected + "」的服务";
    // 概览里「服务占用」那一格的数：全部服务页取后端加好的合计，分组页取这个分组的。
    //
    // 筛选之后只能自己把筛出来的那几个加起来：后端的合计是整个页面的，
    // 摆在「2 / 8 个服务」底下、旁边那张榜只有两行，两个数对不上。
    // 这不是「在界面里推算后端该给的数」——后端没有也不该有「筛选后」这个口径，
    // 它连筛选都不知道，相加用的仍是后端报的每一项。
    var shownUsage = !filtering
      ? (selected === "all"
        ? (data && data.usage)
        : ((groups.filter(function (g) { return g.name === selected; })[0] || {}).usage))
      : sumUsage(shownServices);

    // 只读告警里要写出那份 YAML 的完整路径。名字、目录那几样的说法在偏好设置页里
    // （见 SettingsPage 的「数据」一栏）——主区这边不再展示「现在看的是哪份数据」。
    var cfgPath = (data && data.configPath) || "";

    // 一批服务一起启停。只有这一份实现：分组标题行那对按钮和顶栏那对按钮
    // 走的是同一个函数，两处各写一遍的话，「停止要不要确认」这类规矩
    // 迟早只在其中一处生效。
    //
    // list 为 null 表示「全部服务」：交给后端的 startAll / stopAll 一次做完，
    // 顺序（编译型的要排队）由后端定。给了列表就逐个入队。
    var runBatch = function (kind, list, label) {
      var fire = async function () {
        try {
          if (!list) {
            var r = await call(kind === "start" ? "startAll" : "stopAll");
            if (r.msg) flash("ok", r.msg);
          } else {
            for (var i = 0; i < list.length; i++) {
              // 单个失败不打断其余的：一个服务起不来，不该让后面几个也不动。
              try { await call(kind, list[i].name); } catch (ex) { /* 见上 */ }
            }
            flash("ok", "已把" + label + "排入队列");
          }
        } catch (ex) { flash("err", ex.message); }
        refresh();
      };

      // 启动不打断任何东西，点了直接排队。
      if (kind === "start") { fire(); return; }
      // 停止会打断正在编译或正在跑的服务，先问一句——与删除、清空同一个规格。
      // 这一句里的数字取当前状态，不另做一次后端查询。
      var live = (list || services).filter(function (s) {
        return s.running || s.statusKey === "starting";
      }).length;
      msg.modal.confirm({
        title: "停止" + label + "？",
        content: live
          ? "其中 " + live + " 个正在运行或正在启动，停止会打断它们。"
          : "这些服务现在都没有在跑。",
        okText: "停止", okButtonProps: { danger: true }, cancelText: "取消",
        onOk: fire
      });
    };

    var groupActs = function (g, list) {
      var label = "「" + g.name + "」的服务";
      return html`<div className="dc-group-acts">
        ${list.length ? html`<${React.Fragment}>
          <${A.Button} type="text" className="dc-quiet" icon=${e(IconPlay)}
            onClick=${function () { runBatch("start", list, label); }}>全部启动<//>
          <${A.Button} type="text" className="dc-quiet" icon=${e(IconStop)}
            onClick=${function () { runBatch("stop", list, label); }}>全部停止<//>
        <//>` : null}
      </div>`;
    };

    // 分组自己的操作（改名、删除）挂在侧栏那一行上，和分组名挨着：
    // 放在主区标题栏最右边的话，离它作用的那个名字隔着半个屏幕。
    var groupMenu = function (g) {
      return { items: [
        { key: "rename", label: "重命名分组", icon: e(IconEdit) },
        { key: "delete", label: "删除分组", icon: e(IconTrash), danger: true }
      ], onClick: function (mi) {
        if (mi.key === "rename") setGrp({ open: true, mode: "rename", target: g.name });
        if (mi.key === "delete") {
          // 用 msg.modal 而不是 A.Modal.confirm：静态方法拿不到 ConfigProvider
          // 的主题，暗色下会弹出一个亮色的确认框。
          msg.modal.confirm({
            title: "删除分组「" + g.name + "」",
            content: g.count > 0
              ? "分组里的 " + g.count + " 个应用不会被删掉，它们会移到「" + ungrouped + "」。"
              : "这是个空分组，删除不影响任何应用。",
            okText: "删除", okButtonProps: { danger: true }, cancelText: "取消",
            onOk: async function () {
              try {
                var r = await call("deleteGroup", g.name);
                msg.message.success(r.msg || "已删除");
                if (selected === g.name) setSelected("all");
              } catch (ex) { msg.message.error(ex.message); }
              refresh();
            }
          });
        }
      } };
    };

    // 「…」只在悬停这一行（或菜单开着）时出现在数量左边，数量始终原地不动：
    // 数量是这一栏里被扫读的信息，拿按钮去顶替它，鼠标一划过列表数字就一闪一闪。
    //
    // 外面那层 span 必须拦住点击冒泡：下拉菜单虽然渲染在 body 上，React 的事件
    // 仍会顺着组件树冒到侧栏的 Menu 项上，不拦的话点「重命名」会顺带切换选中的分组。
    // g 传 null 表示这一行既没有分组菜单、也不能拖：内置的「未分组」是固定位置
    // 的桶，只读清单整份不改。拖动的落点线画在名字这一行的上下边缘，
    // 与主区服务行同一套做法。
    var navLabel = function (name, count, g) {
      var menuOpen = navMenu === name;
      var dropCls = g && grpDnd && grpDnd.target && grpDnd.target.name === name
        ? (grpDnd.target.after ? " dc-nav-drop-after" : " dc-nav-drop-before") : "";
      return html`<span className=${"dc-nav-row" + (menuOpen ? " is-open" : "") + dropCls}
        draggable=${!!g}
        onDragStart=${g ? function (ev) { grpDnd.start(name, ev); } : null}
        onDragOver=${g ? function (ev) { grpDnd.over(name, ev); } : null}
        onDrop=${g ? function (ev) { grpDnd.drop(name, ev); } : null}
        onDragEnd=${g ? grpDnd.end : null}>
        <span className="dc-nav-name" title=${name}>${name}</span>
        ${g ? html`<span className="dc-nav-more" onClick=${function (ev) { ev.stopPropagation(); }}>
          <${A.Dropdown} trigger=${["click"]} placement="bottomLeft" menu=${groupMenu(g)}
            onOpenChange=${function (o) { setNavMenu(o ? name : ""); }}>
            <${A.Button} type="text" icon=${e(IconMore)} title="分组操作"/>
          <//>
        </span>` : null}
        <span className="dc-nav-count" style=${{ fontSize: token.fontSizeSM }}>${count}</span>
      </span>`;
    };

    var sm = { fontSize: token.fontSizeSM, color: token.colorTextTertiary };

    // 搜索与快捷筛选那一行。摆在页头标题下面、列表上面，而不是塞进右边那排按钮里：
    // 窗口窄下来时那一排（启停 / 从端口添加 / 添加应用）本来就占满了，再挤进一个输入框和
    // 三个选项，先被顶出窗口的会是「添加应用」。放在页头里也不跟着列表滚走，
    // 筛完往下翻还能看见自己筛了什么。
    //
    // 一个服务都没有时不摆：没有东西可筛，一行空控件只会把空状态往下推。
    var filterBar = (data && services.length && !onToolPage) ? html`<div className="dc-filter">
      <${A.Input} className="dc-search" allowClear value=${search} ref=${searchRef}
        title="⌘K 聚焦搜索" prefix=${e(IconSearch, { size: 13 })}
        onChange=${function (ev) { setSearch(ev.target.value); }}
        placeholder="搜索服务、目录或端口"/>
      <${A.Segmented} value=${quick} onChange=${setQuick} options=${[
        { value: "all", label: "全部" },
        { value: "running", label: "在跑" },
        { value: "attention", label: "异常" }]}/>
    </div>` : null;

    // 侧栏的第一节是「服务」：全部服务和各个分组都平铺在这里，都是把服务按某种口径看一遍。
    // 分组前面那颗点是灰/绿/红三态，形状本身就说明它是分组，不再单给它一个标题——
    // 原来是一个标题（分组）、一个孤零零的「全部服务」、再加一个没标题的「SDK 管理」，
    // 三种层级摞在一起，读的人得先猜这里有几层。
    //
    // 分组试过缩进一级，不行：232 的字宽里再让出 24px，名字列只剩 100px，
    // 悬停时「…」一出来就当场地省略成「demo-adm…」；而且这么一退，
    // 分组看着像是「全部服务」展开出来的子节点，可它并不是——它是另一条看服务的口径。
    var navServices = [
      { key: "all", icon: html`<span className="dc-nav-dot">${e(IconGrid)}</span>`, label: navLabel("全部服务", services.length) }
    ].concat(groups.map(function (g) {
      // 有服务异常的组标红点，有在跑的标绿点。
      // 分组本身的重命名/删除在这一行悬停出来的「…」里，见 navLabel。
      var gs = groupStatus(g.name);
      return {
        key: g.name,
        icon: html`<span className="dc-nav-dot"><${Dot} status=${gs} title=${groupStatusText(gs)}/></span>`,
        label: navLabel(g.name, g.count, g.builtin || readOnly ? null : g)
      };
    })).concat(readOnly ? [] : [{
      key: "__new",
      icon: html`<span className="dc-nav-dot" style=${{ color: token.colorTextTertiary }}>${e(IconPlus)}</span>`,
      label: html`<span style=${{ color: token.colorTextTertiary }}>新建分组</span>`
    }]);

    return html`<${A.Layout} className="dc-shell" style=${shellBg}>
      <${A.Layout.Sider} width=${232} className="dc-side" style=${sideBg}>
        <div className="dc-brand">
          <${Mark} size=${30}/>
          <div className="dc-brand-text">
            <div className="dc-brand-name" style=${{ fontSize: token.fontSizeLG }}>Pier</div>
            <div style=${sm}>本地服务控制台</div>
          </div>
        </div>

        <div className="dc-nav">
          <${A.Menu} mode="inline" selectedKeys=${[selected]}
            style=${{ borderInlineEnd: 0 }}
            onClick=${function (mi) {
              if (mi.key === "__new") { setGrp({ open: true, mode: "create", target: "" }); return; }
              setSelected(mi.key);
            }}
            items=${[
              { type: "group", label: "服务", children: navServices },
              // 「设置」这一节管的是全局的东西（本机装了哪些 SDK、默认用哪个），
              // 和「看某个分组的服务」不是一回事，所以另起一节，而不是拿一条分隔线了事：
              // 分隔线只说了「下面是别的东西」，说不清是别的什么。
              //
              // 不挂 navLabel：那一行的右边是服务数，而这一页没有数量可言，
              // 挂上去只会留下一个空的计数占位。
              { type: "group", label: "设置", children: [
                { key: SDK_KEY, icon: html`<span className="dc-nav-dot">${e(IconChip)}</span>`,
                  label: html`<span className="dc-nav-row">
                    <span className="dc-nav-name">SDK 管理</span></span>` },
                { key: LOG_KEY, icon: html`<span className="dc-nav-dot">${e(IconDoc)}</span>`,
                  label: html`<span className="dc-nav-row">
                    <span className="dc-nav-name">日志管理</span></span>` }
              ]}
            ]}/>
        </div>

        <div className="dc-side-foot" style=${{ borderTop: "1px solid " + token.colorSplit }}>
          ${"" /* 页脚只有这一行入口。「现在看的是哪份数据」那件事（以及跟着它的
                 复制成 YAML / 导出清单… / 打开清单… / 清理残留记录）在它打开的
                 那一页里（见 SettingsPage 的「数据」一栏），不再占着主区的顶栏。
                 仍然用一个 Menu 而不是自己画一行：选中态、悬停底色、图标对齐都和
                 上面那两节一模一样，而自绘的每一处都要各自对齐一次。

                 文案用「偏好设置」而不是「设置」：上面已经有一节叫「设置」
                 （SDK 管理 / 日志管理），同名并列会让人以为点进去是同一处。 */}
          <${A.Menu} mode="inline" selectedKeys=${[selected]} style=${{ borderInlineEnd: 0 }}
            onClick=${function (mi) { setSelected(mi.key); }}
            items=${[{
              key: SETTINGS_KEY,
              icon: html`<span className="dc-nav-dot">${e(IconGear)}</span>`,
              label: html`<span className="dc-nav-row">
                <span className="dc-nav-name">偏好设置</span>
                ${"" /* 右边这一格平时是当前版本；有新版本时换成主色的圆点加最新版本号——
                       自动更新最怕的就是「查过了，而没人知道」，不点进去也看得见。 */}
                ${up && up.hasUpdate && !up.skipped
                  ? html`<span className="dc-set-ver is-new" style=${{ color: token.colorPrimary }}
                      title=${"有新版本 " + up.latest + "，点开查看"}>
                      <span className="dc-set-dot" style=${{ background: token.colorPrimary }}/>
                      <span>${up.latest}</span>
                    </span>`
                  : html`<span className="dc-set-ver" title="当前版本">
                      ${up && hasVal(up.current) ? up.current : ""}
                    </span>`}
              </span>`
            }]}/>
        </div>
      <//>

      <${A.Layout} className="dc-main" style=${mainBg}>
        <div className="dc-head" style=${headLine}>
          <div className="dc-head-row">
            <div style=${{ minWidth: 0 }}>
              <div className="dc-head-title" style=${{ fontSize: token.fontSizeHeading4 }}>${title}</div>
              ${"" /* 副标题压到 12px 的弱化色：它只是计数之类的元信息，不该和标题抢同等分量。
                     它说的是「这一页现在怎么样」，不是「你看的是哪份清单」——后者在
                     偏好设置的「数据」一栏里。 */}
              <div className="dc-head-sub" style=${sm}>
                ${onSDKPage
                  ? "本机扫到的 SDK · 服务上单独指定的优先级更高"
                  : onLogPage
                  ? "一个服务一个目录、一天一个文件 · 启动时自动清理超期日志"
                  : onSettingsPage
                  ? "更新、外观与清单 · 都存在本机数据目录里"
                  : (data ? headCount + " · " + (tallyAll.running + tallyAll.starting) + " 个在跑" : "正在读取…")}
                ${!onToolPage && tallyAll.attention ? html`<span style=${{ color: token.colorError }}>
                  ${" · " + tallyAll.attention + " 个需要关注"}</span>` : null}
                ${!onToolPage && busyShown ? html`<span style=${{ color: token.colorPrimary }}>
                  ${" · " + busyShown + " 个正在操作"}</span>` : null}
              </div>
            </div>
            ${"" /* 设置那三页（SDK 管理、日志管理、偏好设置）上不摆启停与「添加应用」，
                   理由见上面 onToolPage 那段。

                   这一排原先还有一颗「⋯」：现在看的是哪份数据 / 复制成 YAML /
                   导出清单… / 打开清单… / 清理残留记录。那一组说的是同一件事
                   ——手上这份清单在哪、怎么把它带走、怎么换一份——而这件事跟
                   「对这一页的服务做什么」没有关系，却占着主区每时每刻最显眼的那一排，
                   点开还看得见一组跟屏幕上这一页无关的操作。整组挪进了偏好设置的
                   「数据」一栏（见 SettingsPage），顶栏只剩下面这些按页生效的动作。

                   代价是清楚的：打开只读清单之后，那一页上不再有一眼可见的「现在看的
                   是哪一份」。所以只读时主区顶上那条告警里写着去哪儿切回来（见下面
                   readOnly 那一段），而偏好设置的「数据」栏里那份是完整的。 */}
            ${onToolPage ? null : html`<div className="dc-head-acts">
              ${"" /* 这对按钮跟着当前这一页走：在「全部服务」页上是全部服务，
                     在某个分组页上就只作用于这一组。以前它们写死调 startAll / stopAll，
                     于是在分组页点「全部停止」会把屏幕上根本没显示、也不属于这个分组的
                     服务一起停掉；文案也一并跟着改，按钮上说的就是它真会做的事。 */}
              <${A.Space.Compact}>
                ${headBatch === null ? html`<${React.Fragment}>
                  <${A.Button} icon=${e(IconPlay)}
                    onClick=${function () { runBatch("start", null, headBatchLabel); }}>全部启动<//>
                  <${A.Button} icon=${e(IconStop)}
                    onClick=${function () { runBatch("stop", null, headBatchLabel); }}>全部停止<//>
                <//>` : html`<${React.Fragment}>
                  <${A.Button} icon=${e(IconPlay)} disabled=${!headBatch.length}
                    onClick=${function () { runBatch("start", headBatch, headBatchLabel); }}>启动本组<//>
                  <${A.Button} icon=${e(IconStop)} disabled=${!headBatch.length}
                    onClick=${function () { runBatch("stop", headBatch, headBatchLabel); }}>停止本组<//>
                <//>`}
              <//>
              ${"" /* 添加服务的两个入口并排：左手边那个是「它已经在跑了，照它现在的样子
                     记下来」——手边有一个清单里没有的服务时，这条路上要的四样东西
                     （目录、类型、启动命令、端口）那个进程身上都有，而「添加应用」得从
                     填目录开始。两个按钮分开摆而不是收进一个下拉里：藏在箭头后面的话，
                     只有已经知道有这条路的人找得到它。 */}
              ${readOnly ? null : html`<${React.Fragment}>
                <${A.Button} onClick=${function () { setScanOpen(true); }}>从端口添加<//>
                <${A.Button} type="primary" icon=${e(IconPlus)} title="⌘N"
                  onClick=${function () { setForm({ open: true, editing: null }); }}>添加应用<//>
              <//>`}
            </div>`}
          </div>
          ${filterBar}
        </div>

        <div className="dc-body">
          ${bridgeBroken ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
            message="界面与后端没有接上"
            description=${"这些入口没有被绑定：" + missingBindings.join("、") +
              "。按钮点了不会有任何反应，下面显示的可能不是真实状态。"}/>` : null}

          ${stateErr ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
            message="读取状态失败" description=${stateErr}/>` : null}

          ${data && data.error ? html`<${A.Alert} type="error" showIcon className="dc-hidden-bar"
            message="服务清单加载失败" description=${data.error}/>` : null}

          ${banner ? html`<${A.Alert} className="dc-hidden-bar" type="info" showIcon closable
            message=${banner} onClose=${function () { setBanner(""); }}/>` : null}

          ${"" /* 上一次替换的结果。点掉才清：读一次就删等于「更新失败了，
                 而界面上一句都不说」——助手那边没有第二个能说话的地方。
                 成功那条也给一句，否则换了文件之后什么都没发生，用户得自己去
                 偏好设置里看版本号才知道刚才那一下成没成。 */}
          ${res ? html`<${A.Alert} className="dc-hidden-bar" showIcon closable
            type=${res.ok ? "success" : "error"}
            message=${res.ok ? "已经更新到 " + res.version : "上次更新没有成功"}
            description=${res.ok ? (hasVal(res.message) ? res.message : "")
              : (res.message || "") + (res.rolledBack ? "（原来的文件已经放回去了）" : "")}
            onClose=${function () {
              call("updateClearResult").catch(function () {});
              upRefresh();
            }}/>` : null}

          ${onSDKPage ? html`<${SDKPage} message=${msg.message} modal=${msg.modal}/>`
            : onLogPage ? html`<${LogPage} message=${msg.message} modal=${msg.modal}
                onOpenLog=${function (n) { setLog({ open: true, name: n }); }}/>`
            : onSettingsPage ? html`<${SettingsPage} up=${up} th=${th} data=${data}
                message=${msg.message} reload=${upRefresh} refresh=${refresh}/>`
            : html`<${React.Fragment}>
          ${"" /* 分析面板摆在告警之后、服务列表之前：告警说的是「下面这些可能不是真的」，
                 排在它前面就成了「先看数字再看免责声明」。 */}
          ${data && services.length && !noMatch ? html`<${Overview} data=${data} services=${shownServices}
            scope=${selected} usage=${shownUsage}/>` : null}

          ${"" /* 只读的来源有两种：命令行 --config，或者偏好设置里的「打开清单…」。
                 退回去的办法不一样，所以这句话跟着来源走——只写「去掉 --config」
                 对第二种人是一句没有门的话，他们压根没给过这个参数。

                 「现在看的是哪份数据」已经从顶栏挪进偏好设置，这一条就是主区里
                 唯一还在说这件事的地方：只读本来就不寻常，该说的时候把路一起说清楚。 */}
          ${readOnly ? html`<${A.Alert} type="info" showIcon className="dc-hidden-bar"
            message=${(data.configSource === "命令行指定" ? "正在查看命令行指定的清单 " : "正在查看只读清单 ")
              + cfgPath}
            description=${"这份 YAML 文件 Pier 只读不写：可以启停，但不能在界面里增删改。"
              + (data.configSource === "命令行指定"
                ? "去掉 --config 启动就会回到本机数据。"
                : "偏好设置 · 数据 里的「回到本机数据」可以切回去。")}/>` : null}

          ${!data ? html`<${A.Skeleton} active paragraph=${{ rows: 6 }}/>` : null}

          ${data && data.ok !== false && services.length === 0 ? html`<${A.Empty} className="dc-empty"
            description="还没有任何服务。选一个项目目录，Pier 会认出类型、读出端口、推出启动命令。">
            <${A.Button} type="primary" onClick=${function () { setForm({ open: true, editing: null }); }}>
              添加第一个应用<//>
          <//>` : null}

          ${"" /* 筛完一个都不剩：整块列表收成一句。留着一块空标题配「这个分组还是空的」
                 会让人以为分组真的空了，而它只是被筛掉了。 */}
          ${filtering && !shownServices.length && services.length ? html`<div
            className="dc-filter-empty" style=${sm}>
            没有匹配的服务${needle ? "（关键词「" + search.trim() + "」）" : ""}。</div>` : null}

          ${visible.map(function (g) {
            var list = listIn(g);
            // 被筛空的组整块不摆：标题栏里的数量、在跑数、占用都跟着这一份走，
            // 摆一个「0 个」的空壳只是在列表里多几道横线。
            if (filtering && !list.length) return null;
            var gt = tally(list);
            // 只在「全部服务」页上挂：分组页上概览那一格就是这个数，再挂一遍是重复。
            // 筛过之后用筛出来的那几个重算，否则这一行说的是整组，下面摆的是半组。
            var gUsage = filtering ? sumUsage(list) : g.usage;
            var usageText = selected === "all" && !data.metricsError && gUsage && gUsage.procs > 0
              ? "CPU " + fmtCPU(gUsage.cpu) + " · " + fmtMem(gUsage.memBytes) + " · " + gUsage.procs + " 进程"
              : "";
            return html`<section className="dc-group-block" key=${g.name}>
              <div className="dc-group-head">
                <div className="dc-group-title">
                  <span className="dc-group-name" style=${{ fontSize: token.fontSizeLG }}>${g.name}</span>
                  <span className="dc-count" style=${{ fontSize: token.fontSizeSM,
                    color: token.colorTextSecondary, background: token.colorFillTertiary }}>${list.length}</span>
                  ${gt.running + gt.starting ? html`<span className="dc-group-live" style=${sm}>
                    <${Dot} status="running"/>${gt.running + gt.starting} 个在跑</span>` : null}
                  ${usageText ? html`<span style=${sm}>${usageText}</span>` : null}
                </div>
                ${groupActs(g, list)}
              </div>
              <div className="dc-list">
                ${list.length === 0
                  ? html`<div className="dc-list-empty" style=${sm}>
                      ${"这个分组还是空的。在「添加应用」里把分组选成「" + g.name + "」即可。"}
                    </div>`
                  : list.map(function (s) {
                      return html`<${ServiceRow} key=${s.name} svc=${s} act=${act}
                        dnd=${svcDnd} metricsErr=${data.metricsError}/>`;
                    })}
              </div>
            </section>`;
          })}
          <//>`}
        </div>
      <//>

      <${LogDrawer} open=${log.open} name=${log.name} message=${msg.message}
        onClose=${function () { setLog({ open: false, name: "" }); }}/>

      <${PortOwnerModal} open=${portOwner.open} name=${portOwner.name}
        message=${msg.message}
        onDone=${refresh}
        onStopService=${function (n) { setPortOwner({ open: false, name: "" }); act("stop", { name: n }); }}
        onClose=${function () { setPortOwner({ open: false, name: "" }); }}/>

      <${PortScanModal} open=${scanOpen} message=${msg.message}
        onAdopt=${function (info, sp) {
          // 扫描那一屏只是把那一行交过来，真正填表的是表单自己：从端口收进来和
          // 手填目录走的是同一次识别，预填逻辑放在一处才不会两边说法不一。
          setForm({ open: true, editing: null, adopt: { port: sp.port, info: info } });
        }}
        onClose=${function () { setScanOpen(false); }}/>

      <${ServiceFormModal} open=${form.open} editing=${form.editing} openPort=${form.openPort}
        adopt=${form.adopt}
        groups=${groups} defaultGroup=${selected === "all" ? "" : selected}
        knownNames=${services.map(function (s) { return s.name; })}
        message=${msg.message}
        onSaved=${refresh}
        onClose=${function () { setForm({ open: false, editing: null }); }}/>

      <${GroupModal} open=${grp.open} mode=${grp.mode} target=${grp.target}
        message=${msg.message} onSaved=${refresh} onRenamed=${setSelected}
        onClose=${function () { setGrp({ open: false, mode: "create", target: "" }); }}/>

      <${DeleteModal} open=${delState.open} svc=${delState.svc}
        message=${msg.message} onSaved=${refresh}
        onClose=${function () { setDel({ open: false, svc: null }); }}/>

      ${DEMO ? html`<${A.Tag} color="warning" className="dc-demo-mark">
        演示数据 · 没有连接后端<//>` : null}
    <//>`;
  }

  // ConfigProvider 与 App 必须包在最外层：theme 要在渲染前就定下来，
  // 而 App 提供 message 的上下文（脱离它的 message 会拿不到主题）。
  //
  // useTheme() 只在这里调一次，结果往下传：主题是一份全局状态，
  // 两处各持一份的话，切换只改到其中一份，ConfigProvider 读不到。
  function Root() {
    var th = useTheme();
    // autoInsertSpace 关掉：antd 会在两个汉字的按钮中间自动插空格（「停 止」「取 消」），
    // 同一排里「全部启动」不插、「启动」插，字距忽宽忽窄。
    return html`<${A.ConfigProvider} locale=${A.locales.zh_CN} theme=${themeConfig(th.dark)}
      button=${{ autoInsertSpace: false }}>
      <${A.App}>
        <${App} th=${th}/>
      <//>
    <//>`;
  }

  // 渲染期抛错时给一块看得见的红屏，而不是留一个白窗口。
  // 白窗口是最难排查的：看不出是没加载、还是崩了、还是后端没接上。
  //
  // 这块界面在 ConfigProvider 外面（它得能接住 Root 自己的错），
  // 所以拿不到 useToken()。颜色用 antd 的静态接口 getDesignToken() 取，
  // 不写死十六进制——写死的话就又绕开算法自己定了一套色。
  class Boundary extends React.Component {
    constructor(p) { super(p); this.state = { err: null }; }
    static getDerivedStateFromError(err) { return { err: err }; }
    componentDidCatch(err, info) { console.error("Pier 界面渲染失败", err, info); }
    render() {
      if (!this.state.err) return this.props.children;
      var tk = {};
      try { tk = A.theme.getDesignToken(); } catch (e2) { /* 取不到就用浏览器默认色，聊胜于无 */ }
      // 字号同样取自 token，不写死像素。取不到就交给浏览器默认值。
      return e("div", { style: { padding: 24, fontFamily: "-apple-system, sans-serif" } },
        e("h2", { style: { color: tk.colorError } }, "界面出错了"),
        e("pre", { style: { whiteSpace: "pre-wrap", fontSize: tk.fontSizeSM, opacity: .8 } },
          String(this.state.err && this.state.err.stack || this.state.err)),
        e("p", { style: { opacity: .7, fontSize: tk.fontSize } },
          "把上面这段报给开发者。控制台里通常还有更多信息。"));
    }
  }

  ReactDOM.createRoot(document.getElementById("root"))
    .render(e(Boundary, null, e(Root)));
})();
