// Package cli 是 vinx-docs 的命令行入口：分发到服务、构建、页面和批注各命令。
// 输出文案和退出码是给脚本和 agent 用的接口（例如 publish 的退出码 3），改动要同步文档。
package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vinx-lab/vinx-docs/assets"
	"github.com/vinx-lab/vinx-docs/internal/build"
	"github.com/vinx-lab/vinx-docs/internal/comments"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/files"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/server"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Version 是程序版本号。scripts/build.sh 用 -ldflags "-X github.com/vinx-lab/vinx-docs/internal/cli.Version=…" 注入，
// 所以这里是 var；改版本号时改这一行（构建脚本从这里读取默认值）。
var Version = "0.2.0"

type group struct {
	title, module string
	names         [][2]string
}

var groups = []group{
	{"服务", "server", [][2]string{
		{"start", "后台启动服务（先构建站点）"},
		{"stop", "停止由 start 启动的服务"},
		{"restart", "重启服务"},
		{"status", "查看服务状态"},
		{"serve", "前台运行（给 systemd / launchd 或调试用）"},
	}},
	{"文档项目", "core", [][2]string{
		{"refresh", "重新构建站点"},
		{"validate", "校验配置"},
		{"register", "用参数登记一个项目（日常用页面上的「接入项目」即可）"},
		{"unregister", "取消登记一个项目，不动原文"},
		{"settings", "修改自动同步等设置"},
	}},
	{"页面", "pages", [][2]string{
		{"publish", "发布一个本地 HTML 页面，得到短链接"},
		{"unpublish", "取消一个短链接，不删源文件"},
		{"artifacts", "列出已发布的页面"},
	}},
	{"批注", "comments", [][2]string{
		{"comments", "列出批注"},
		{"comment", "查看一条批注"},
		{"claim", "领取一条批注"},
		{"reply", "回复"},
		{"resolve", "标记解决（必须附说明）"},
		{"reopen", "重新打开"},
		{"watch", "监听交给本会话的批注，每条输出一行"},
		{"subscribe", "Codex 会话登记订阅"},
		{"unsubscribe", "取消订阅"},
		{"agents", "列出在线 agent"},
	}},
	{"其他", "self", [][2]string{
		{"install-service", "登记开机自动启动（Linux systemd / macOS launchd / Windows 登录启动项）"},
		{"uninstall-service", "移除开机自动启动"},
		{"home", "显示家目录（配置、站点、批注库所在位置）"},
	}},
}

func moduleOf(command string) string {
	for _, g := range groups {
		for _, item := range g.names {
			if item[0] == command {
				return g.module
			}
		}
	}
	return ""
}

func usage() string {
	lines := []string{"vinx-docs " + Version + " — 本地的 agent 产出审阅台", "",
		"用法：vinx-docs [--home 目录] <命令> [参数]", ""}
	for _, g := range groups {
		lines = append(lines, g.title+"：")
		for _, item := range g.names {
			name := item[0]
			if pad := 18 - len([]rune(name)); pad > 0 {
				name += strings.Repeat(" ", pad)
			}
			lines = append(lines, "  "+name+item[1])
		}
		lines = append(lines, "")
	}
	lines = append(lines, "每个命令加 --help 查看参数。家目录默认在用户数据目录，可用 --home 或环境变量 VINX_DOCS_HOME 指定。")
	return strings.Join(lines, "\n")
}

func errorf(format string, args ...any) { fmt.Fprintf(stderr, format, args...) }

// Main 解析全局参数并执行命令，返回退出码。
//
// 开发用：--assets 目录（或环境变量 VINX_DOCS_ASSETS）让程序直接读取该目录下的 web/、vendor/，
// 代替编译进二进制的前端资源，改前端文件后不用重新编译。默认用内嵌资源；家目录里自带 web/ 时仍优先用家目录。
func Main(args []string) int {
	assets.Install()
	server.Version = Version
	assetsDir := os.Getenv("VINX_DOCS_ASSETS")
	for len(args) >= 2 && (args[0] == "--home" || args[0] == "--assets") {
		if args[0] == "--home" {
			expanded, err := textutil.ExpandUser(args[1])
			if err != nil {
				errorf("error: %s\n", err)
				return 1
			}
			os.Setenv("VINX_DOCS_HOME", textutil.Absolute(expanded))
		} else {
			assetsDir = args[1]
		}
		args = args[2:]
	}
	if assetsDir != "" {
		absolute := textutil.Absolute(assetsDir)
		build.PackageAssets = build.DirAssets(absolute)
		server.ExtraEnv = append(server.ExtraEnv, "VINX_DOCS_ASSETS="+absolute)
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, usage())
		return 0
	}
	if args[0] == "-V" || args[0] == "--version" {
		fmt.Fprintln(stdout, Version)
		return 0
	}
	command := args[0]
	switch moduleOf(command) {
	case "server":
		return serverMain(args)
	case "core":
		return coreMain(args)
	case "pages":
		return pagesMain(args)
	case "comments":
		return commentsMain(args)
	case "self":
		home, err := config.DataHome()
		if err != nil {
			errorf("error: %s\n", err)
			return 1
		}
		if command == "home" {
			if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
				fmt.Fprintln(stdout, "usage: vinx-docs home [-h]\n\n显示家目录（配置、站点、批注库所在位置）。")
				return 0
			}
			fmt.Fprintln(stdout, home)
			return 0
		}
		return service(command, home, args[1:])
	}
	errorf("未知命令：%s\n\n%s\n", command, usage())
	return 2
}

// ---------------------------------------------------------------- 服务

func serverMain(args []string) int {
	parser := &Parser{Prog: "vinx-docs", Args: []*Arg{
		{Dest: "action", Choices: []string{"serve", "start", "stop", "status", "restart"}},
	}}
	values, code := parser.Parse(args)
	if values == nil {
		return code
	}
	root, err := config.DataHome()
	if err == nil {
		switch action := values.Str("action"); action {
		case "serve":
			if err = server.Serve(root); err == nil {
				return 0
			}
		case "restart":
			if _, err = server.Manage(root, "stop"); err == nil {
				code, err = server.Manage(root, "start")
			}
		default:
			code, err = server.Manage(root, action)
		}
	}
	if err != nil {
		errorf("error: %s\n", err)
		return 1
	}
	return code
}

// ---------------------------------------------------------------- 文档项目

func ioArgs() []*Arg {
	// 选项也可以写在子命令后面（用户常写 refresh --config ...）。
	return []*Arg{{Flag: "--config", Dest: "config", Type: "path", NoDest: true},
		{Flag: "--output", Dest: "output", Type: "path", NoDest: true}}
}

func coreParser() *Parser {
	p := &Parser{Prog: "vinx-docs", Args: []*Arg{
		{Flag: "--config", Dest: "config", Type: "path"}, {Flag: "--output", Dest: "output", Type: "path"}}}
	p.Sub("validate", ioArgs()...)
	p.Sub("refresh", ioArgs()...)
	p.Sub("register", append(ioArgs(),
		&Arg{Flag: "--id", Dest: "id", Required: true},
		&Arg{Flag: "--name", Dest: "name", Required: true},
		&Arg{Flag: "--docs", Dest: "docs", Required: true, Type: "path"},
		&Arg{Flag: "--home", Dest: "home", Default: "README.md"},
		&Arg{Flag: "--exclude", Dest: "exclude", Action: "append"},
	)...)
	p.Sub("unregister", append(ioArgs(), &Arg{Flag: "--id", Dest: "id", Required: true})...)
	p.Sub("settings", append(ioArgs(),
		&Arg{Flag: "--auto-sync", Dest: "auto_sync", Choices: []string{"on", "off"}},
		&Arg{Flag: "--debounce", Dest: "debounce", Type: "int"},
	)...)
	return p
}

// corePaths 推导配置文件和站点输出目录：命令行参数优先，否则取家目录下的默认位置。
func corePaths(values Values) (string, string, error) {
	configPath := values.Str("config")
	if configPath == "" {
		home, err := config.DataHome()
		if err != nil {
			return "", "", err
		}
		configPath = config.ConfigPath(home)
	}
	output := values.Str("output")
	if output == "" {
		output = config.SitePath(textutil.Parent(textutil.Parent(configPath)))
	}
	return configPath, output, nil
}

// finishCore 把命令的错误转成退出码：配置错误回 2，其他错误回 1。
func finishCore(err error) int {
	if err == nil {
		return 0
	}
	if config.IsConfigError(err) {
		errorf("error: %s\n", err)
		return 2
	}
	errorf("error: %s\n", err)
	return 1
}

func coreMain(args []string) int {
	values, code := coreParser().Parse(args)
	if values == nil {
		return code
	}
	configPath, output, err := corePaths(values)
	if err != nil {
		return finishCore(err)
	}
	switch values.Str("command") {
	case "validate":
		err = build.Validate(configPath, output)
	case "refresh":
		var results []*build.ProjectResult
		if results, err = build.Refresh(configPath, output, "", ""); err == nil {
			fmt.Fprintln(stdout, ojson.Dumps(build.ResultsJSON(results), 2))
		}
	case "register":
		excludes := []any{}
		for _, item := range values.List("exclude") {
			excludes = append(excludes, item)
		}
		project := ojson.NewObject("id", values.Str("id"), "name", values.Str("name"), "docsPath", values.Str("docs"),
			"home", values.Str("home"), "exclude", excludes)
		err = build.RegisterProject(configPath, output, project)
	case "unregister":
		err = build.Unregister(configPath, output, values.Str("id"))
	case "settings":
		changes := ojson.NewObject()
		if values.Has("auto_sync") {
			changes.Set("autoSync", values.Str("auto_sync") == "on")
		}
		if values.Has("debounce") {
			changes.Set("debounceSeconds", values["debounce"])
		}
		var saved *ojson.Object
		if saved, err = build.SaveSettings(configPath, changes); err == nil {
			fmt.Fprintln(stdout, ojson.Dumps(saved, 2))
		}
	}
	return finishCore(err)
}

// ---------------------------------------------------------------- 页面

func pagesParser() *Parser {
	p := &Parser{Prog: "vinx-docs", Args: []*Arg{{Flag: "--registry", Dest: "registry", Type: "path"}}}
	p.Sub("publish",
		&Arg{Dest: "entry", Type: "path", Help: "入口 .html 文件"},
		&Arg{Flag: "--root", Dest: "root", Type: "path", Help: "文件清单的根目录，默认是入口页所在目录"},
		&Arg{Flag: "--file", Dest: "file", Action: "append", Help: "额外发布的文件（相对根目录），可重复"},
		&Arg{Flag: "--title", Dest: "title"},
		&Arg{Flag: "--json", Dest: "json", Action: "store_true"},
	)
	p.Sub("unpublish", &Arg{Dest: "id"})
	p.Sub("artifacts", &Arg{Flag: "--json", Dest: "json", Action: "store_true"})
	return p
}

// finishCommand 是页面和批注命令的错误处理：配置错误回 2，其他回 1。
func finishCommand(err error) int { return finishCore(err) }

func pagesMain(args []string) int {
	values, code := pagesParser().Parse(args)
	if values == nil {
		return code
	}
	home, err := config.DataHome()
	if err != nil {
		return finishCommand(err)
	}
	custom := values.Has("registry")
	registry := values.Str("registry")
	var base string
	if custom {
		_, port := config.ServerAddress(home)
		base = fmt.Sprintf("http://localhost:%d", port)
	} else {
		registry = pages.DefaultRegistry(home)
		base = pages.PublicBase(home)
	}
	switch values.Str("command") {
	case "publish":
		record, notes, err := pages.Publish(registry, values.Str("entry"), values.Str("root"), values.List("file"), values.Str("title"))
		if err != nil {
			return finishCommand(err)
		}
		id := pages.Str(record, "id")
		url := base + "/a/" + id + "/"
		reachable, state := true, "未检查（自定义登记文件）"
		if !custom {
			reachable, state = pages.Probe(home, id)
		}
		fileList := pages.Files(record)
		if values.Bool("json") {
			noteList := []any{}
			for _, note := range notes {
				noteList = append(noteList, note)
			}
			fmt.Fprintln(stdout, ojson.Dumps(ojson.NewObject("id", id, "url", url, "list", base+"/published.html",
				"title", record.Value("title"), "root", record.Value("root"), "files", record.Value("files"),
				"notes", noteList, "reachable", reachable, "service", state), 2))
		} else {
			fmt.Fprintf(stdout, "已发布：%s\n", textutil.Str(record.Value("title")))
			fmt.Fprintf(stdout, "短链接：%s\n", url)
			fmt.Fprintf(stdout, "页面列表：%s/published.html\n", base)
			fmt.Fprintf(stdout, "根目录：%s\n", textutil.Str(record.Value("root")))
			shown := fileList
			more := ""
			if len(shown) > 20 {
				shown, more = shown[:20], " 等"
			}
			fmt.Fprintf(stdout, "文件（%d）：%s%s\n", len(fileList), strings.Join(shown, "、"), more)
			fmt.Fprintf(stdout, "状态：%s\n", state)
			for _, note := range notes {
				fmt.Fprintf(stdout, "提示：%s\n", note)
			}
		}
		// 登记已写入但服务读不到：用单独的退出码，agent 不能当成发布成功。
		if reachable {
			return 0
		}
		return 3
	case "unpublish":
		record, err := pages.Unpublish(registry, values.Str("id"))
		if err != nil {
			return finishCommand(err)
		}
		fmt.Fprintf(stdout, "已取消：%s（%s）；源文件未改动\n", textutil.Str(record.Value("id")), textutil.Str(record.Value("title")))
	default:
		items, err := pages.Listing(registry)
		if err != nil {
			return finishCommand(err)
		}
		if values.Bool("json") {
			fmt.Fprintln(stdout, ojson.Dumps(items, 2))
			return 0
		}
		for _, raw := range items {
			item := raw.(*ojson.Object)
			published, _ := textutil.Int(item.Value("publishedAt"))
			when := time.Unix(published.Int64(), 0).Format("2006-01-02 15:04")
			lost := ""
			if missing, _ := item.Value("missing").([]any); len(missing) > 0 {
				lost = fmt.Sprintf("  缺失%d个文件", len(missing))
			}
			fmt.Fprintf(stdout, "%s  %s  %s  %s%s%s\n", textutil.Str(item.Value("id")), when, textutil.Str(item.Value("title")),
				base, textutil.Str(item.Value("url")), lost)
		}
	}
	return 0
}

// ---------------------------------------------------------------- 批注

func commentsParser() *Parser {
	p := &Parser{Prog: "vinx-docs", Args: []*Arg{{Flag: "--db", Dest: "db", Type: "path"}}}
	p.Sub("comments",
		&Arg{Flag: "--status", Dest: "status", Default: "open,sent", Help: "open,sent,resolved 或 all；默认 open,sent"},
		&Arg{Flag: "--scope", Dest: "scope", Action: "append", Help: "只看范围内的：path:/目录、project:<ID>、a:<短码>"},
		&Arg{Flag: "--json", Dest: "json", Action: "store_true"},
	)
	p.Sub("comment", &Arg{Dest: "id", Type: "int"}, &Arg{Flag: "--json", Dest: "json", Action: "store_true"})
	for _, name := range []string{"claim", "reply", "resolve"} {
		args := []*Arg{{Dest: "id", Type: "int"}}
		if name != "claim" {
			args = append(args, &Arg{Dest: "text"})
		}
		args = append(args, &Arg{Flag: "--as", Dest: "who", Help: "署名，默认 agent@<当前目录名>"})
		p.Sub(name, args...)
	}
	p.Sub("reopen", &Arg{Dest: "id", Type: "int"})
	p.Sub("watch",
		&Arg{Flag: "--agent", Dest: "agent", Default: "claude", Choices: []string{"claude", "codex"}},
		&Arg{Flag: "--name", Dest: "name"},
		&Arg{Flag: "--scope", Dest: "scope", Action: "append", Help: "默认 path:<当前目录>"},
		&Arg{Flag: "--interval", Dest: "interval", Type: "float", Default: 2.0},
		&Arg{Flag: "--once", Dest: "once", Action: "store_true", Suppress: true},
	)
	p.Sub("subscribe",
		&Arg{Flag: "--agent", Dest: "agent", Default: "codex", Choices: []string{"codex"}},
		&Arg{Flag: "--thread", Dest: "thread", Required: true},
		&Arg{Flag: "--name", Dest: "name"},
		&Arg{Flag: "--scope", Dest: "scope", Action: "append"},
	)
	p.Sub("unsubscribe", &Arg{Dest: "sub_id"})
	p.Sub("agents")
	return p
}

func defaultScopes(scopes []string) ([]string, error) {
	if len(scopes) > 0 {
		return scopes, nil
	}
	cwd, err := textutil.Getwd()
	if err != nil {
		return nil, err
	}
	return []string{"path:" + cwd}, nil
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func commentsMain(args []string) int {
	values, code := commentsParser().Parse(args)
	if values == nil {
		return code
	}
	home, err := config.DataHome()
	if err != nil {
		return finishCommand(err)
	}
	paths := files.NewPaths(home)
	conn, err := comments.Connect(values.Str("db"))
	if err != nil {
		return finishCommand(err)
	}
	defer conn.Close()
	code, err = runComments(conn, paths, home, values)
	if err != nil {
		return finishCommand(err)
	}
	return code
}

func runComments(conn *comments.Conn, paths files.Paths, home string, values Values) (int, error) {
	id := values["id"]
	switch values.Str("command") {
	case "comments":
		var statuses []string
		if values.Str("status") == "all" {
			for _, item := range comments.Statuses {
				statuses = append(statuses, item.Key)
			}
		} else {
			for _, item := range strings.Split(values.Str("status"), ",") {
				statuses = append(statuses, textutil.Strip(item))
			}
		}
		items, err := conn.Listing("", statuses, "")
		if err != nil {
			return 1, err
		}
		if scopes := values.List("scope"); len(scopes) > 0 {
			matches := comments.ScopeMatcher(paths)
			kept := items[:0]
			for _, item := range items {
				if matches(textutil.Str(item.Value("target")), scopes) {
					kept = append(kept, item)
				}
			}
			items = kept
		}
		switch {
		case values.Bool("json"):
			list := []any{}
			for _, item := range items {
				list = append(list, comments.Describe(paths, item))
			}
			fmt.Fprintln(stdout, ojson.Dumps(list, 2))
		case len(items) == 0:
			fmt.Fprintln(stdout, "没有符合条件的批注。")
		default:
			for _, item := range items {
				body := textutil.Head(textutil.CollapseSpace(textutil.Str(item.Value("body"))), 60)
				fmt.Fprintf(stdout, "#%s  [%s]  %s  「%s」\n", textutil.Str(item.Value("id")), textutil.Str(item.Value("statusLabel")),
					textutil.Str(item.Value("target")), body)
			}
		}
	case "comment":
		item, err := conn.Get(id)
		if err != nil {
			return 1, err
		}
		info := comments.Describe(paths, item)
		if values.Bool("json") {
			fmt.Fprintln(stdout, ojson.Dumps(info, 2))
		} else {
			fmt.Fprintln(stdout, comments.Format(info, pages.PublicBase(home)))
		}
	case "claim":
		who := orDefault(values.Str("who"), comments.AgentName("agent"))
		ok, err := conn.Claim(id, who)
		if err != nil {
			return 1, err
		}
		if !ok {
			errorf("批注 #%s 已被其他 agent 领取或已解决，跳过。\n", textutil.Str(id))
			return 4, nil
		}
		fmt.Fprintf(stdout, "已领取批注 #%s（%s）\n", textutil.Str(id), who)
	case "reply":
		if _, err := conn.AddReply(id, orDefault(values.Str("who"), comments.AgentName("agent")), values.Str("text")); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "已回复批注 #%s\n", textutil.Str(id))
	case "resolve":
		if _, err := conn.Resolve(id, orDefault(values.Str("who"), comments.AgentName("agent")), values.Str("text")); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "已解决批注 #%s\n", textutil.Str(id))
	case "reopen":
		if _, err := conn.SetStatus(id, "reopen"); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "已重新打开批注 #%s\n", textutil.Str(id))
	case "watch":
		scopes, err := defaultScopes(values.List("scope"))
		if err != nil {
			return 1, err
		}
		agent := values.Str("agent")
		stop := make(chan struct{})
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
		defer signal.Stop(signals)
		go func() {
			if _, ok := <-signals; ok {
				close(stop)
			}
		}()
		interval, _ := values["interval"].(float64)
		err = conn.Watch(paths, comments.WatchOptions{
			Agent: agent, Name: orDefault(values.Str("name"), comments.AgentName(agent)), Scopes: scopes,
			Interval: time.Duration(interval * float64(time.Second)), Once: values.Bool("once"),
			Out: stdout, Log: stderr, Stop: stop,
		})
		if err != nil {
			return 1, err
		}
	case "subscribe":
		scopes, err := defaultScopes(values.List("scope"))
		if err != nil {
			return 1, err
		}
		subID, err := conn.Subscribe("codex", orDefault(values.Str("name"), comments.AgentName("codex")), scopes, values.Str("thread"))
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "已订阅：%s（%s），12 小时内有效，重新执行即可续期\n", subID, strings.Join(scopes, " "))
	case "unsubscribe":
		if err := conn.Unsubscribe(values.Str("sub_id")); err != nil {
			return 1, err
		}
		fmt.Fprintln(stdout, "已取消订阅")
	default:
		items, err := conn.Online()
		if err != nil {
			return 1, err
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, "当前没有在线 agent。")
		}
		for _, item := range items {
			fmt.Fprintf(stdout, "%s  %s  %s  %s\n", textutil.Str(item.Value("id")), textutil.Str(item.Value("agent")),
				textutil.Str(item.Value("name")), strings.Join(comments.Scopes(item), " "))
		}
	}
	return 0, nil
}

// SetOutput 替换命令输出（测试用）。
func SetOutput(out, errOut io.Writer) {
	stdout, stderr = out, errOut
	server.Stdout, server.Stderr = out, errOut
}
