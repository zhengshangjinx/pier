package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhengshangjinx/pier/internal/config"
	"github.com/zhengshangjinx/pier/internal/ideaconf"
)

// cmdImport 读取 IDEA 的 .idea/workspace.xml，把它里面已有的运行配置
// 转成 Pier 的服务定义。
//
// 为什么值得做：开发者已经在 IDEA 里把工作目录、模块、启动项名称调好了，
// 手工照抄成 YAML 极易抄错，而且 IDEA 一改就失效。这里直接沿用那份事实。
//
// 默认只打印到标准输出，不写文件——生成结果需要人过一眼，
// 直接覆盖已有配置是不可接受的。
func cmdImport(args []string) int {
	opt, err := parseImportArgs(args)
	if err != nil {
		return fail("%v", err)
	}

	// 相对路径的基准：写到文件时以该文件所在目录为准（和 pier.yaml 的语义一致），
	// 只打印时以当前目录为准。
	baseDir := ""
	if opt.out != "" {
		abs, err := filepath.Abs(opt.out)
		if err != nil {
			return fail("解析输出路径失败：%v", err)
		}
		baseDir = filepath.Dir(abs)
	} else if wd, err := os.Getwd(); err == nil {
		baseDir = wd
	}

	imp := &importer{baseDir: baseDir}
	for _, d := range opt.dirs {
		// 路径本身写错要立刻报错，避免"以为导入了其实没有"。
		projDir, err := normalizeProjectDir(d)
		if err != nil {
			return fail("%v", err)
		}
		// 但"这个工程没有运行配置"只是它自己的事，不该连累其它工程。
		// 前端工程就常常一个运行配置都没有（脚本只写在 package.json 里），
		// 这属于正常情况。
		if err := imp.addProject(projDir, opt.prefix); err != nil {
			fmt.Fprintf(os.Stderr, "跳过 %s：%v\n", projDir, err)
		}
	}
	imp.dedupeGoTools()
	if len(imp.services) == 0 {
		fmt.Fprintln(os.Stderr, "没有导入任何服务。")
		for _, s := range imp.skipped {
			fmt.Fprintf(os.Stderr, "  跳过 %s：%s\n", s.name, s.reason)
		}
		return 1
	}

	text := imp.render(opt)

	if opt.out == "" {
		fmt.Print(text)
		return 0
	}
	abs, _ := filepath.Abs(opt.out)
	if _, err := os.Stat(abs); err == nil && !opt.force {
		return fail("%s 已存在。确认要覆盖请加 --force（或改用 -o 指定别的文件）", abs)
	}
	if err := os.WriteFile(abs, []byte(text), 0o644); err != nil {
		return fail("写入 %s 失败：%v", abs, err)
	}
	fmt.Printf("已写入 %s（%d 个服务）\n", abs, len(imp.services))
	return 0
}

// importOptions 是 import 的命令行参数。
type importOptions struct {
	dirs   []string
	prefix string
	out    string
	force  bool
}

func parseImportArgs(args []string) (*importOptions, error) {
	// 先把 --key=value 拆成两个参数，后面就只剩「选项 值」一种形式要处理
	flat := make([]string, 0, len(args)*2)
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if k, v, ok := strings.Cut(a, "="); ok {
				flat = append(flat, k, v)
				continue
			}
		}
		flat = append(flat, a)
	}

	opt := &importOptions{}
	for i := 0; i < len(flat); i++ {
		switch a := flat[i]; a {
		case "--force":
			opt.force = true
		case "--prefix", "-p":
			if i+1 >= len(flat) {
				return nil, fmt.Errorf("%s 后面缺少值", a)
			}
			i++
			opt.prefix = flat[i]
		case "-o", "--out":
			if i+1 >= len(flat) {
				return nil, fmt.Errorf("%s 后面缺少值", a)
			}
			i++
			opt.out = flat[i]
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("未知参数：%s", a)
			}
			opt.dirs = append(opt.dirs, a)
		}
	}
	if len(opt.dirs) == 0 {
		return nil, fmt.Errorf("请给出至少一个含 .idea 的项目目录，例如：pier import demo-server/backend")
	}
	if opt.prefix != "" && len(opt.dirs) > 1 {
		return nil, fmt.Errorf("--prefix 只能用于单个项目目录；多个目录请分次导入（各自的前缀会自动推导）")
	}
	return opt, nil
}

// renderedSvc 是一条待输出的服务定义。
// 字段用零值表示「没探测到」，渲染时补 TODO 注释，
// 而不是编一个看起来合理的默认值——那会把错误固化进配置里。
type renderedSvc struct {
	name     string
	dir      string
	kind     string
	module   string
	script   string
	port     int
	health   string
	env      map[string]string
	notes    []string
	javaWant string // 仅 java：pom 里要求的 Java 版本
	javaCand string // 仅 java：本机 sdkman 中匹配到的候选目录名
}

// skipItem 是被判定为「非常驻服务」而跳过的运行配置。
type skipItem struct {
	name   string
	reason string
}

// importer 累积一次导入的结果。
type importer struct {
	baseDir  string
	services []*renderedSvc
	skipped  []skipItem
}

// addProject 处理一个 IDEA 项目目录。projDir 必须是已确认存在的项目根目录。
func (im *importer) addProject(projDir, prefixOverride string) error {
	rcs, err := ideaconf.Load(projDir)
	if err != nil {
		return err
	}

	prefix := prefixOverride
	if prefix == "" {
		prefix = defaultPrefix(projDir)
	}
	// Java 的 Spring Boot 配置不写 module，只能靠主类源码反推。
	modules := mavenModules(projDir)

	for _, rc := range rcs {
		if !rc.IsService() {
			im.skipped = append(im.skipped, skipItem{
				name:   rc.Name,
				reason: fmt.Sprintf("类型 %s 不是常驻服务（多为一次性工具类）", rc.Type),
			})
			continue
		}
		svc, note := im.convert(rc, projDir, prefix, modules)
		if svc == nil {
			im.skipped = append(im.skipped, skipItem{name: rc.Name, reason: note})
			continue
		}
		im.services = append(im.services, svc)
	}
	return nil
}

// convert 把一条 IDEA 运行配置转成服务定义。返回 nil 表示不该导入，第二项是原因。
func (im *importer) convert(rc ideaconf.RunConfig, projDir, prefix string, modules []string) (*renderedSvc, string) {
	if rc.Dir == "" {
		return nil, "运行配置里没有工作目录，无法确定启动位置"
	}
	if !dirExists(rc.Dir) {
		return nil, fmt.Sprintf("工作目录不存在：%s", rc.Dir)
	}

	// Spring Boot 的 module 必须在起名之前定下来：服务短名正是从模块名推出来的，
	// 而 IDEA 的 Spring Boot 配置不写 module，只能靠主类源码位置反推。
	if rc.Type == ideaconf.TypeSpringBoot && rc.Module == "" {
		rc.Module = moduleForMainClass(projDir, modules, rc.MainClass)
		if rc.Module == "" {
			return nil, fmt.Sprintf("无法从主类 %s 定位 Maven 模块，请手工确认后填写 module", rc.MainClass)
		}
	}

	svc := &renderedSvc{
		name: prefix + "-" + shortName(rc, projDir),
		dir:  im.rel(rc.Dir),
		env:  rc.Env,
	}
	// IDEA 里的配置名注在注释里，方便与原 IDE 对照
	svc.notes = append(svc.notes, fmt.Sprintf("IDEA 运行配置「%s」", rc.Name))

	switch rc.Type {
	case ideaconf.TypeGoApplication:
		svc.kind = config.KindGo
		if strings.Contains(strings.Trim(rc.GoPackage, "/"), "/") {
			// 同一个工作目录下带子路径的包基本是子命令工具（如 admin/cmd/sqlexec），
			// 不是常驻服务。dedupeGoTools 会在同名目录的服务确定后再定夺。
			svc.notes = append(svc.notes, "package 含子路径，疑似子命令工具而非常驻服务")
		}
		svc.port = config.ReadGoPort(rc.Dir)
		if svc.port > 0 {
			pathPrefix := config.ReadGoPath(rc.Dir)
			svc.health = fmt.Sprintf("http://localhost:%d%s/health", svc.port, pathPrefix)
			svc.notes = append(svc.notes, fmt.Sprintf(
				"端口 %d ← config.yaml 的 server.port；健康检查 %s ← server.path + /health",
				svc.port, svc.health))
		}
		if len(svc.env) == 0 && fileExists(filepath.Join(rc.Dir, "config-dev.yaml")) {
			// 本仓库按 config-<env>.yaml 区分环境，不指定环境变量可能加载到非预期配置。
			// 这里只提示，不替开发者决定该填什么值。
			svc.notes = append(svc.notes,
				"TODO 该工程用 config-<env>.yaml 区分环境，IDEA 配置里未声明环境变量，请确认是否需要补 env")
		}

	case ideaconf.TypeSpringBoot:
		svc.kind = config.KindJava
		svc.module = rc.Module // 已在上面反推完成
		moduleDir := filepath.Join(projDir, svc.module)
		svc.notes = append(svc.notes,
			fmt.Sprintf("模块 %s ← 由主类 %s 的源码位置反推（IDEA 配置里不写 module）", svc.module, rc.MainClass))

		svc.port = config.ReadSpringPort(moduleDir)
		// 探针要打管理端口：Spring Boot 常把 Actuator 与业务端口分开，
		// 打业务端口会一直探不通。
		mgmtPort := config.ReadSpringManagementPort(moduleDir)
		if mgmtPort == 0 {
			mgmtPort = svc.port
		}
		if svc.port > 0 {
			svc.notes = append(svc.notes, fmt.Sprintf("端口 %d ← application.yml 的 server.port", svc.port))
		}
		if mgmtPort > 0 {
			svc.health = fmt.Sprintf("http://localhost:%d/actuator/health", mgmtPort)
			if mgmtPort != svc.port {
				svc.notes = append(svc.notes,
					fmt.Sprintf("健康检查 %s ← management.server.port（与业务端口不同）", svc.health))
			}
		}
		// Java 版本必须钉住：本机 sdkman 装了多个 JDK，选错不会当场报错，
		// 而是编译到一半才失败（实测用 17 编 21 的工程会报「不支持发行版本 21」）。
		svc.javaWant = javaVersionRequirement(moduleDir, projDir)
		if svc.javaWant != "" {
			svc.javaCand = sdkmanJavaCandidate(svc.javaWant)
			if svc.javaCand != "" {
				svc.notes = append(svc.notes, fmt.Sprintf(
					"pom 要求 Java %s，本机 sdkman 对应 %s，故按服务钉住版本", svc.javaWant, svc.javaCand))
			} else {
				svc.notes = append(svc.notes,
					fmt.Sprintf("TODO pom 要求 Java %s，但本机 sdkman 里没有匹配的 JDK，请安装或手工指定路径", svc.javaWant))
			}
		}

	case ideaconf.TypeNpm:
		svc.kind = config.KindNode
		svc.script = rc.Script
		if svc.script == "" {
			svc.script = firstNodeScript(rc.Dir)
		}
		svc.port = config.ReadNodePort(rc.Dir)
		if svc.port > 0 {
			svc.health = fmt.Sprintf("http://localhost:%d/", svc.port)
			svc.notes = append(svc.notes, fmt.Sprintf("端口 %d ← .env", svc.port))
		}

	default:
		return nil, fmt.Sprintf("暂不支持的类型 %s", rc.Type)
	}

	if svc.port == 0 {
		svc.notes = append(svc.notes, "TODO 未能探测到端口，请手工确认")
	}
	return svc, ""
}

// dedupeGoTools 处理「同一工作目录下有多条 Go 运行配置」的情况。
//
// 真实例子里 services/admin 下同时有 `go build admin`（服务）和
// `go build admin/cmd/sqlexec`（子命令工具），两者工作目录相同。
// 保留 package 不含子路径的那条作为服务，其余移入跳过列表——
// 但只有在该目录确实存在另一条服务时才这么做，避免误杀。
func (im *importer) dedupeGoTools() {
	byDir := map[string][]*renderedSvc{}
	for _, s := range im.services {
		if s.kind == config.KindGo {
			byDir[s.dir] = append(byDir[s.dir], s)
		}
	}
	drop := map[*renderedSvc]bool{}
	for _, group := range byDir {
		if len(group) < 2 {
			continue
		}
		var primary *renderedSvc
		for _, s := range group {
			if !hasToolNote(s) {
				primary = s
				break
			}
		}
		if primary == nil {
			continue // 全是子路径，没有更可信的候选，保持原样交给使用者判断
		}
		for _, s := range group {
			if s != primary {
				drop[s] = true
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	kept := im.services[:0]
	for _, s := range im.services {
		if drop[s] {
			im.skipped = append(im.skipped, skipItem{
				name:   s.name,
				reason: "与同目录下的服务共用工作目录，且 package 含子路径，判定为子命令工具",
			})
			continue
		}
		kept = append(kept, s)
	}
	im.services = kept
}

const toolNote = "package 含子路径，疑似子命令工具而非常驻服务"

func hasToolNote(s *renderedSvc) bool {
	for _, n := range s.notes {
		if n == toolNote {
			return true
		}
	}
	return false
}

// render 生成最终的 YAML 文本。
// 这里手工拼接而不是用 yaml.Marshal：配置的价值有一半在注释里
// （每个值是从哪个文件读出来的、哪些地方需要人工确认），
// 序列化器会把注释全部丢掉。
func (im *importer) render(opt *importOptions) string {
	var b strings.Builder
	b.WriteString("# 本文件由 pier import 生成，生成时间 " + time.Now().Format("2006-01-02 15:04") + "。\n")
	b.WriteString("# 重新生成：\n#   pier import " + strings.Join(opt.dirs, " ") + "\n#\n")
	b.WriteString("# 服务名沿用「工作空间前缀-服务短名」；工作目录、模块都取自 IDEA 已有的运行配置，\n")
	b.WriteString("# 端口与健康检查路径是从各工程自己的配置文件里读出来的（来源逐条注在下面）。\n")
	b.WriteString("# 标了 TODO 的地方需要人工确认：这里不会替你编一个看起来合理的默认值。\n")
	b.WriteString("\n")

	globalToolchain := "{}"
	b.WriteString("# 工具链覆盖：留空则自动解析（sdkman / nvm / Homebrew / JetBrains 内置 JBR）。\n")
	b.WriteString("# 各工程要求的版本不同时，按服务声明，见下面 java 服务的 toolchain。\n")
	b.WriteString("toolchain: " + globalToolchain + "\n\n")

	b.WriteString("services:\n")
	for i, s := range im.services {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("  # " + strings.Join(s.notes, "\n  # ") + "\n")
		if s.kind == config.KindJava && s.javaWant != "" && s.javaCand == "" {
			b.WriteString("  # TODO Java 版本未能自动匹配，确认后自行补 toolchain\n")
		}
		b.WriteString("  - name: " + yamlScalar(s.name) + "\n")
		b.WriteString("    dir: " + yamlScalar(s.dir) + "\n")
		b.WriteString("    kind: " + s.kind + "\n")
		if s.module != "" {
			b.WriteString("    module: " + yamlScalar(s.module) + "\n")
		}
		if s.script != "" {
			b.WriteString("    script: " + yamlScalar(s.script) + "\n")
		}
		if s.port > 0 {
			b.WriteString(fmt.Sprintf("    port: %d\n", s.port))
		}
		if s.health != "" {
			b.WriteString("    health: " + yamlScalar(s.health) + "\n")
		}
		if len(s.env) > 0 {
			b.WriteString("    env:\n")
			keys := make([]string, 0, len(s.env))
			for k := range s.env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString("      " + k + ": " + yamlScalar(s.env[k]) + "\n")
			}
		}
		if s.javaCand != "" {
			b.WriteString("    toolchain:\n")
			b.WriteString("      java: " + yamlScalar(s.javaCand) + "\n")
		}
	}

	if len(im.skipped) > 0 {
		b.WriteString("\n# ---------- 以下运行配置未导入（判定为非常驻服务） ----------\n")
		for _, s := range im.skipped {
			b.WriteString("# " + s.name + "：" + s.reason + "\n")
		}
	}
	return b.String()
}

// rel 把绝对路径转成相对 baseDir 的路径（用 / 分隔，YAML 里可读性更好）。
// 不在同一棵树里就只能写绝对路径。
func (im *importer) rel(target string) string {
	if im.baseDir == "" {
		return filepath.ToSlash(target)
	}
	r, err := filepath.Rel(im.baseDir, target)
	if err != nil || strings.HasPrefix(r, "..") {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(r)
}

// ---- 名称推导 ----

// normalizeProjectDir 接受「含 .idea 的项目目录」或「.idea 目录本身」。
func normalizeProjectDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("解析路径 %s 失败：%w", dir, err)
	}
	if filepath.Base(abs) == ".idea" {
		abs = filepath.Dir(abs)
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("目录不存在：%s", abs)
	}
	return abs, nil
}

// defaultPrefix 推导服务名前缀：取「工作空间根目录名」的第一段。
// 例如 acme/acme-server → 工作空间根是 acme → 前缀 acme。
// 这样两个工作空间里都叫 admin 的服务不会撞名（acme-admin / demo-admin）。
func defaultPrefix(projDir string) string {
	ws := filepath.Base(filepath.Dir(projDir))
	if ws == "" || ws == "." || ws == string(filepath.Separator) {
		ws = filepath.Base(projDir)
	}
	if i := strings.IndexByte(ws, '-'); i > 0 {
		return ws[:i]
	}
	return ws
}

// shortName 推导服务的短名，尽量贴近开发者在 IDEA 里习惯的叫法。
func shortName(rc ideaconf.RunConfig, projDir string) string {
	switch rc.Type {
	case ideaconf.TypeGoApplication:
		if p := strings.Trim(rc.GoPackage, "/"); p != "" {
			return path.Base(p)
		}
		return filepath.Base(rc.Dir)
	case ideaconf.TypeSpringBoot:
		if rc.Module != "" {
			return stripModulePrefix(rc.Module)
		}
		base := path.Base(strings.ReplaceAll(rc.MainClass, ".", "/"))
		return strings.TrimSuffix(base, "Application")
	case ideaconf.TypeNpm:
		b := filepath.Base(projDir)
		// 前端工程名形如 acme-web-admin，去掉工作空间名这段前缀更易读。
		ws := filepath.Base(filepath.Dir(projDir))
		if ws != "" && strings.HasPrefix(b, ws+"-") {
			return b[len(ws)+1:]
		}
		return b
	}
	return rc.Name
}

// stripModulePrefix 去掉 Maven 模块名里的角色前缀（shop-admin → admin）。
func stripModulePrefix(module string) string {
	for _, p := range []string{"server-", "service-", "module-"} {
		if rest, ok := strings.CutPrefix(module, p); ok {
			return rest
		}
	}
	return module
}

// ---- 工程内容探测 ----

// moduleForMainClass 在反应堆的各子模块里找主类源码，反推它属于哪个模块。
// IDEA 的 Spring Boot 运行配置只写主类不写 module，所以只能这样定位。
func moduleForMainClass(projDir string, modules []string, mainClass string) string {
	if mainClass == "" {
		return ""
	}
	rel := filepath.Join("src", "main", "java",
		filepath.FromSlash(strings.ReplaceAll(mainClass, ".", "/"))+".java")
	for _, m := range modules {
		if fileExists(filepath.Join(projDir, m, rel)) {
			return m
		}
	}
	return ""
}

// javaVersionRequirement 从 pom 里读工程要求的 Java 版本。
// 先看子模块自己的 pom，再退回反应堆根 pom（版本常定义在根 pom 的 properties 里）。
func javaVersionRequirement(moduleDir, projDir string) string {
	for _, p := range []string{
		filepath.Join(moduleDir, "pom.xml"),
		filepath.Join(projDir, "pom.xml"),
	} {
		if v := xmlTagValue(p, "java.version"); v != "" {
			return v
		}
		if v := xmlTagValue(p, "maven.compiler.release"); v != "" {
			return v
		}
	}
	return ""
}

// xmlTagValue 用最朴素的方式取 <tag>值</tag>。
// 只为读一个版本号，不值得引入完整的 XML 解析；
// 而且 pom 里的值可能带 ${...} 占位符，取不到就返回空让上层提示人工确认。
func xmlTagValue(file, tag string) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	s := string(raw)
	open, closeTag := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		return ""
	}
	v := strings.TrimSpace(rest[:j])
	if v == "" || strings.ContainsAny(v, "${}") {
		return "" // 占位符需要人工确认，不要猜
	}
	return v
}

// sdkmanJavaCandidate 在本机 sdkman 里找主版本匹配的 JDK，返回候选目录名。
//
// 必须返回真实存在的目录名：配置里的 toolchain 值要么是绝对路径，
// 要么是 sdkman 候选目录名，"21" 这种纯版本号是解析不出来的。
func sdkmanJavaCandidate(want string) string {
	wantMajor := javaMajor(want)
	if wantMajor == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	base := filepath.Join(home, ".sdkman", "candidates", "java")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	matches := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "current" {
			continue
		}
		if !fileExists(filepath.Join(base, e.Name(), "bin", "java")) {
			continue
		}
		if leadingMajor(e.Name()) == wantMajor {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 0 {
		return ""
	}
	// 同主版本有多个时取版本号最大的
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches[0]
}

// javaMajor 归一化 Java 版本号：21 → 21，1.8.0_472 → 8。
func javaMajor(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 && parts[0] == "1" {
		return parts[1]
	}
	return parts[0]
}

// leadingMajor 取候选目录名开头数字段的主版本号：21.0.9-oracle → 21，8.0.472-zulu → 8。
func leadingMajor(name string) string {
	i := 0
	for i < len(name) && (name[i] >= '0' && name[i] <= '9' || name[i] == '.') {
		i++
	}
	return javaMajor(name[:i])
}

// firstNodeScript 在 package.json 里挑一个约定俗成的启动脚本。
func firstNodeScript(dir string) string {
	scripts := nodeScripts(dir)
	for _, c := range nodeScriptCandidates {
		if scripts[c] {
			return c
		}
	}
	return ""
}

// yamlScalar 在必要时给 YAML 标量加引号。
// 目录名里可能有空格（本仓库就有「历史代码」这样的目录），不加引号会解析错。
// 注意连字符不是特殊字符：demo-admin 这类名字加引号只会更难读。
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	needQuote := strings.ContainsAny(s, ":#{}[],&*?|<>=!%@`\"'\n\t ")
	// 以特殊字符开头也必须加引号（YAML 里 "-x" 会被当成序列项）
	if strings.ContainsAny(s[:1], "-?*&!|>%@`\"'") {
		needQuote = true
	}
	if !needQuote {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
