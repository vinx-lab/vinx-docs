// Package server 用一个进程同时提供生成的站点、管理接口、页面短链接和批注接口。
//
// 各路径的安全响应头和原来 Nginx 配置里的一致：站点页面用严格 CSP，项目里的 HTML 预览和原件下载放进
// sandbox，短链接页面由 pages 模块给出沙箱策略。HTTP/1.1 长连接；写操作（POST）响应后关闭连接。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/api"
	"github.com/vinx-lab/vinx-docs/internal/build"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Service 是健康检查里的服务名，manage 靠它确认进程身份。
const Service = "vinx-docs"

// Version 是程序版本号，由命令行入口设置，/api/status 返回给页面显示。
var Version = "dev"

// 三种安全头策略：站点自身、项目里的 HTML 隔离预览、原件下载。
const (
	SitePolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	PreviewPolicy = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	RawPolicy = "sandbox; default-src 'none'; base-uri 'none'"
	// ReaderPolicy 给单文件阅读页：同站点策略，只多允许被本站嵌入（页面列表里预览 Markdown）。
	ReaderPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
)

var projectArea = regexp.MustCompile(`^/projects/[a-z0-9-]+/(preview|raw|content)/`)

// StaticTypes 不用 mime 包：各平台的系统 MIME 表不一样（Windows 上 .js 可能被报成 text/plain）。
var StaticTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".js": "application/javascript; charset=utf-8", ".mjs": "application/javascript; charset=utf-8",
	".json": "application/json", ".map": "application/json", ".txt": "text/plain; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".ico": "image/x-icon", ".woff": "font/woff", ".woff2": "font/woff2",
}

// RawTypes 是原件下载区的类型表。
var RawTypes = map[string]string{
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".json": "application/json",
	".csv": "text/csv; charset=utf-8", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".webp": "image/webp", ".gif": "image/gif",
}

var readEndpoints = map[string]bool{"/api/healthz": true, "/api/status": true, "/api/config": true, "/api/artifacts": true}

// writeEndpoints：每个写接口声明必需键和可选键，多一个未知字段就拒绝。
var writeEndpoints = map[string]api.RouteSpec{
	"/api/refresh":        {Limit: 65536},
	"/api/projects":       {Required: []string{"docsPath"}, Limit: 65536},
	"/api/project/update": {Required: []string{"id"}, Optional: []string{"name", "exclude", "roots", "types"}, Limit: 65536},
	"/api/project/remove": {Required: []string{"id"}, Limit: 65536},
	"/api/settings":       {Optional: config.DefaultSettings().Keys(), Limit: 65536},
}

// OpLock 是同步、自动同步和保存后刷新共用的操作锁；可以查询是否被占用。
type OpLock struct {
	mu   sync.Mutex
	held atomic.Bool
}

func (l *OpLock) Lock()   { l.mu.Lock(); l.held.Store(true) }
func (l *OpLock) Unlock() { l.held.Store(false); l.mu.Unlock() }

// TryLock 尝试加锁，不等待；拿到返回 true。
func (l *OpLock) TryLock() bool {
	if l.mu.TryLock() {
		l.held.Store(true)
		return true
	}
	return false
}

// Locked 报告锁当前是否被占用。
func (l *OpLock) Locked() bool { return l.held.Load() }

// Server 是一个监听中的 Vinx Docs 服务。
type Server struct {
	Root         string
	ConfigPath   string
	Output       string
	RegistryPath string
	Lock         *OpLock
	Auto         *AutoSync
	Vinx         *api.API

	Host     string // 监听的地址（配置里的原值，如 0.0.0.0）
	Port     int    // 实际端口（传 0 时是系统分配的端口）
	listener net.Listener
	http     *http.Server
}

// 监听失败时报错用的系统错误文案（与 C 库 strerror 一致，不随平台变化）。
var strerrors = map[syscall.Errno]string{
	syscall.EADDRINUSE:    "Address already in use",
	syscall.EACCES:        "Permission denied",
	syscall.EADDRNOTAVAIL: "Cannot assign requested address",
}

func strerror(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if text, ok := strerrors[errno]; ok {
			return text
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "Name or service not known"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	return err.Error()
}

// ListenOverride 是开发用的监听地址覆盖（环境变量 VINX_DOCS_LISTEN=host:port）：
// 不改配置文件，让同一个家目录的服务临时换一个地址或端口运行，例如和正在用的服务并排调试。正常使用不设置。
const ListenOverride = "VINX_DOCS_LISTEN"

// Create 创建服务。host/port 为 nil 时读配置（配置坏了用默认值）；
// 测试传 port=0 拿一个空闲端口。
func Create(root string, host *string, port *int) (*Server, error) {
	configPath := config.ConfigPath(root)
	h, p := "", 0
	if host == nil || port == nil {
		configuredHost, configuredPort := "0.0.0.0", 8000
		if cfg, err := config.Load(configPath); err == nil {
			if server, ok := cfg.Value("server").(*ojson.Object); ok {
				if value, ok := server.Value("host").(string); ok {
					configuredHost = value
				}
				if n, ok := textutil.Int(server.Value("port")); ok && n.IsInt64() {
					configuredPort = int(n.Int64())
				}
			}
		}
		h, p = configuredHost, configuredPort
	}
	if host != nil {
		h = *host
	}
	if port != nil {
		p = *port
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(h, strconv.Itoa(p)))
	if err != nil {
		return nil, config.Errorf("无法监听 %s:%d：%s（端口可能已被占用，可在配置的 server.port 里换一个）", h, p, strerror(err))
	}
	s := &Server{
		Root:         root,
		ConfigPath:   configPath,
		Output:       config.SitePath(root),
		RegistryPath: pages.DefaultRegistry(root),
		Lock:         &OpLock{},
		Host:         h,
		Port:         listener.Addr().(*net.TCPAddr).Port,
		listener:     listener,
	}
	s.Auto = &AutoSync{server: s}
	s.Vinx = api.New(root, "", func() error {
		_, err := build.Refresh(s.ConfigPath, s.Output, "", "")
		return err
	}, s.Lock)
	s.Vinx.ErrorLog = Stderr
	s.http = &http.Server{
		Handler: s,
		// 保持连接：远程经 Tailscale 打开时，每个资源都新建 TCP 连接会明显变慢。空闲 10 秒断开。
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       10 * time.Second,
		ErrorLog:          nullLogger(),
	}
	return s, nil
}

// Serve 在已绑定的端口上处理请求，直到 Shutdown。
func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown 停止接受新连接，等正在处理的请求结束（最多 timeout），再关闭剩下的连接。
func (s *Server) Shutdown(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.http.Shutdown(ctx); err != nil {
		_ = s.http.Close()
	}
}

// Close 立即关闭监听和全部连接（测试用）。
func (s *Server) Close() error { return s.http.Close() }

// ---------------------------------------------------------------- 请求

type request struct {
	s      *Server
	w      http.ResponseWriter
	r      *http.Request
	full   string // 完整请求目标（含查询串）
	path   string // urlsplit(self.path).path
	query  string
	isHead bool
}

// splitTarget 拆分请求目标：开头的多个 / 合并成一个（避免被当成 //host 形式的网络路径），
// 再按 ? 和 # 切出路径和查询串。
func splitTarget(target string) (full, path, query string) {
	if strings.HasPrefix(target, "//") {
		target = "/" + strings.TrimLeft(target, "/")
	}
	full = target
	rest := target
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	path, query, _ = strings.Cut(rest, "?")
	return full, path, query
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	full, path, query := splitTarget(r.RequestURI)
	q := &request{s: s, w: w, r: r, full: full, path: path, query: query, isHead: r.Method == http.MethodHead}
	w.Header()["Server"] = []string{"VinxDocs"}
	switch r.Method {
	case http.MethodGet:
		q.doGet()
	case http.MethodHead:
		q.doHead()
	case http.MethodPost:
		q.doPost()
	case http.MethodOptions:
		q.doOptions()
	default:
		q.sendError(501, "Unsupported method ("+textutil.Repr(r.Method)+")", "Server does not support this operation")
	}
}

// abort 用于处理中遇到的意外错误：不写响应，直接断开连接。
func abort() { panic(http.ErrAbortHandler) }

func (q *request) setHeaders(pairs ...string) {
	h := q.w.Header()
	for i := 0; i+1 < len(pairs); i += 2 {
		h[pairs[i]] = append(h[pairs[i]], pairs[i+1]) // 保留原样的大小写（如 ETag）
	}
}

// reply JSON 响应；data 为 nil 时响应体为空。
func (q *request) reply(status int, data any) {
	var payload []byte
	if data != nil {
		payload = []byte(ojson.Dumps(data, -1))
	}
	q.setHeaders("Content-Type", "application/json; charset=utf-8", "Content-Length", strconv.Itoa(len(payload)),
		"Cache-Control", "no-store", "X-Content-Type-Options", "nosniff", "Referrer-Policy", "no-referrer",
		"Content-Security-Policy", SitePolicy)
	q.w.WriteHeader(status)
	_, _ = q.w.Write(payload)
}

func errorBody(message string) *ojson.Object { return ojson.NewObject("error", message) }

// sendPlain 发送纯文本响应。
func (q *request) sendPlain(status int, text string, extra ...string) {
	q.setHeaders("Content-Type", "text/plain; charset=utf-8", "Content-Length", strconv.Itoa(len(text)),
		"X-Content-Type-Options", "nosniff", "Referrer-Policy", "no-referrer", "Cache-Control", "no-cache")
	q.setHeaders(extra...)
	q.w.WriteHeader(status)
	if !q.isHead {
		_, _ = io.WriteString(q.w, text)
	}
}

const errorTemplate = `<!DOCTYPE HTML>
<html lang="en">
    <head>
        <meta charset="utf-8">
        <style type="text/css">
            :root {
                color-scheme: light dark;
            }
        </style>
        <title>Error response</title>
    </head>
    <body>
        <h1>Error response</h1>
        <p>Error code: %d</p>
        <p>Message: %s.</p>
        <p>Error code explanation: %d - %s.</p>
    </body>
</html>
`

// escapeText 转义 &、<、>（不转义引号），用于 HTML 正文。
func escapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// sendError 发送一个简单的 HTML 错误页（目前只用于不支持的方法）。
func (q *request) sendError(status int, message, explain string) {
	body := fmt.Sprintf(errorTemplate, status, escapeText(message), status, escapeText(explain))
	q.setHeaders("Connection", "close", "Content-Type", "text/html;charset=utf-8", "Content-Length", strconv.Itoa(len(body)))
	q.w.WriteHeader(status)
	if !q.isHead {
		_, _ = io.WriteString(q.w, body)
	}
}

// ---------------------------------------------------------------- 静态站点

// serveStatic 生成站点里的文件。点开头的路径段一律 404。
func (q *request) serveStatic(rawPath string) {
	path := textutil.Unquote(rawPath)
	if strings.ContainsAny(path, "\x00\\") {
		q.sendPlain(404, "Not Found")
		return
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			q.sendPlain(404, "Not Found")
			return
		}
	}
	site := textutil.Realpath(q.s.Output)
	target := textutil.Realpath(textutil.Join(site, strings.TrimLeft(path, "/")))
	if target != site && !textutil.IsWithin(target, site) {
		q.sendPlain(404, "Not Found")
		return
	}
	if textutil.IsDir(target) {
		if !strings.HasSuffix(path, "/") {
			location := rawPath + "/"
			if q.query != "" {
				location += "?" + q.query
			}
			q.sendPlain(301, "", "Location", location)
			return
		}
		target = textutil.Join(target, "index.html")
	}
	if !textutil.IsFile(target) {
		q.sendPlain(404, "Not Found")
		return
	}
	area := projectArea.FindStringSubmatch(path)
	suffix := textutil.Lower(textutil.Suffix(target))
	headers := []string{"X-Content-Type-Options", "nosniff", "Referrer-Policy", "no-referrer", "Cache-Control", "no-cache"}
	var contentType string
	switch {
	case area != nil && area[1] == "preview":
		contentType = "text/html; charset=utf-8"
		headers = append(headers, "Content-Security-Policy", PreviewPolicy)
	case area != nil && area[1] == "raw":
		contentType = RawTypes[suffix]
		if contentType == "" {
			contentType = "text/plain; charset=utf-8"
		}
		headers = append(headers, "Content-Disposition", "attachment", "Content-Security-Policy", RawPolicy)
	case area != nil:
		contentType = "text/plain; charset=utf-8"
		headers = append(headers, "Content-Security-Policy", SitePolicy)
	default:
		contentType = StaticTypes[suffix]
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		policy := SitePolicy
		if path == "/read.html" {
			policy = ReaderPolicy
		}
		headers = append(headers, "Content-Security-Policy", policy)
	}
	file, err := os.Open(target)
	if err != nil {
		abort()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		abort()
	}
	etag := fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size())
	headers = append(headers, "ETag", etag)
	if values := q.r.Header.Values("If-None-Match"); len(values) > 0 && values[0] == etag {
		q.setHeaders(headers...)
		q.setHeaders("Content-Length", "0")
		q.w.WriteHeader(304)
		return
	}
	q.setHeaders("Content-Type", contentType, "Content-Length", strconv.FormatInt(info.Size(), 10))
	q.setHeaders(headers...)
	q.w.WriteHeader(200)
	if !q.isHead {
		_, _ = io.CopyN(q.w, file, info.Size())
	}
}

func (q *request) doHead() {
	if strings.HasPrefix(q.path, "/api/") || q.path == "/a" || strings.HasPrefix(q.path, "/a/") {
		q.sendPlain(405, "Method Not Allowed")
		return
	}
	q.serveStatic(q.path)
}

// allowed Host 必须存在；要求 Origin 时（或请求带了 Origin 时）必须与 Host 同源。
func (q *request) allowed(requireOrigin bool) bool {
	host := q.r.Host
	origins := q.r.Header.Values("Origin")
	if host == "" || ((requireOrigin || len(origins) > 0) && (len(origins) == 0 || origins[0] != "http://"+host)) {
		q.reply(403, errorBody("请通过当前文档站点页面操作"))
		return false
	}
	return true
}

func (q *request) doOptions() {
	if !q.allowed(true) {
		return
	}
	_, known := writeEndpoints[q.path]
	_, vinxPost := api.PostRoutes[q.path]
	if !(readEndpoints[q.path] || known || api.GetRoutes[q.path] || vinxPost) {
		q.reply(404, errorBody("接口不存在"))
		return
	}
	q.reply(204, nil)
}

// ---------------------------------------------------------------- 短链接

// sendPage 发送短链接页面的响应（带页面专用的安全头）。
func (q *request) sendPage(status int, body []byte, contentType string, extra ...string) {
	q.setHeaders("Content-Type", contentType, "Content-Length", strconv.Itoa(len(body)), "Cache-Control", "no-cache",
		"X-Content-Type-Options", "nosniff", "Referrer-Policy", "no-referrer", "Content-Security-Policy", pages.PagePolicy)
	// 沙箱页面的 origin 是 null；只对它放开读取，普通站点跨域读不到。
	if values := q.r.Header.Values("Origin"); len(values) > 0 && values[0] == "null" {
		q.setHeaders("Access-Control-Allow-Origin", "null")
	}
	q.setHeaders(extra...)
	q.w.WriteHeader(status)
	_, _ = q.w.Write(body)
}

// serveArtifact 按短码读取登记的源文件；只放行文件清单里的条目，每次请求重新校验。
func (q *request) serveArtifact(path string) {
	const plain = "text/plain; charset=utf-8"
	missing := []byte("页面不存在或已取消发布")
	parts := strings.SplitN(path, "/", 4) // ["", "a", id, rest]
	id := ""
	if len(parts) > 2 {
		id = parts[2]
	}
	registry, err := pages.LoadRegistry(q.s.RegistryPath)
	if err != nil {
		if config.IsConfigError(err) {
			q.sendPage(500, []byte("发布登记不可读"), plain)
			return
		}
		abort()
	}
	record := pages.Find(registry, id)
	if record == nil {
		q.sendPage(404, missing, plain)
		return
	}
	base := "/a/" + id + "/"
	if len(parts) < 4 {
		q.sendPage(301, nil, plain, "Location", base)
		return
	}
	rel := textutil.Unquote(parts[3])
	if rel == "" && pages.IsMarkdown(pages.Str(record, "entry")) {
		// Markdown 在站点的单文件阅读页里渲染（站点 CSP，可以批注和编辑）；链接仍然是短链接。
		q.sendPage(302, nil, plain, "Location", pages.ReadURL(id))
		return
	}
	if rel == "" {
		entry := pages.Str(record, "entry")
		if strings.Contains(entry, "/") {
			q.sendPage(302, nil, plain, "Location", base+textutil.Quote(entry, "/"))
			return
		}
		rel = entry
	}
	if rel == pages.ReservedDir+"/manifest" {
		q.sendPage(200, []byte(ojson.DumpsASCII(pages.ReaderManifest(record), -1)), "application/json; charset=utf-8")
		return
	}
	if rel == pages.ReservedDir+"/version" {
		q.sendPage(200, []byte(ojson.DumpsASCII(ojson.NewObject("version", pages.Version(record)), -1)), "application/json; charset=utf-8")
		return
	}
	listed := false
	for _, name := range pages.Files(record) {
		if name == rel {
			listed = true
			break
		}
	}
	if !listed {
		q.sendPage(404, missing, plain)
		return
	}
	source, err := pages.CheckFile(pages.Str(record, "root"), rel)
	var body []byte
	if err == nil {
		body, err = os.ReadFile(source)
	}
	if err != nil {
		q.sendPage(404, []byte("源文件已不存在或已不允许发布"), plain)
		return
	}
	contentType := pages.MimeFor(rel)
	if strings.HasPrefix(contentType, "text/html") {
		body = pages.InjectLive(body, pages.Version(record), rel)
	}
	var extra []string
	if contentType == "application/octet-stream" {
		extra = []string{"Content-Disposition", "attachment"}
	}
	q.sendPage(200, body, contentType, extra...)
}

// ---------------------------------------------------------------- GET 接口

func (q *request) doGet() {
	path := q.path
	if path == "/a" || strings.HasPrefix(path, "/a/") {
		q.serveArtifact(path)
		return
	}
	if path == "/healthz" {
		q.sendPlain(200, "ok")
		return
	}
	if !strings.HasPrefix(path, "/api/") {
		q.serveStatic(path)
		return
	}
	if !q.allowed(false) {
		return
	}
	if api.GetRoutes[path] {
		q.reply(q.s.Vinx.ServeGet(path, textutil.ParseQS(q.query)))
		return
	}
	// 下面几个接口按完整请求目标比较（带查询串就不匹配）。
	switch q.full {
	case "/api/healthz":
		q.reply(200, ojson.NewObject("service", Service, "pid", os.Getpid(), "home", q.s.Root, "busy", q.s.Lock.Locked()))
	case "/api/status":
		q.reply(200, q.s.statusPayload())
	case "/api/artifacts":
		items, err := pages.Listing(q.s.RegistryPath)
		if err != nil {
			if config.IsConfigError(err) {
				q.reply(500, errorBody(err.Error()))
				return
			}
			abort()
		}
		q.reply(200, ojson.NewObject("artifacts", items))
	case "/api/config":
		q.reply(200, q.s.configPayload())
	default:
		q.reply(405, errorBody("管理操作必须使用POST"))
	}
}

func loadConfigOrAbort(path string) *ojson.Object {
	cfg, err := config.Load(path)
	if err != nil {
		abort()
	}
	return cfg
}

func settingsOf(cfg *ojson.Object) any {
	if value, ok := cfg.Get("settings"); ok {
		return value
	}
	return config.DefaultSettings()
}

func serverOf(cfg *ojson.Object) any {
	if value, ok := cfg.Get("server"); ok {
		return value
	}
	return ojson.NewObject()
}

func (s *Server) statusPayload() *ojson.Object {
	cfg := loadConfigOrAbort(s.ConfigPath)
	report := build.LoadReport(s.Output)
	mode, lastRun, lastError := s.Auto.State()
	return ojson.NewObject(
		"service", Service,
		"version", Version,
		"pid", os.Getpid(),
		"busy", s.Lock.Locked(),
		"settings", settingsOf(cfg),
		"server", serverOf(cfg),
		"autoSync", ojson.NewObject("mode", mode, "lastRun", lastRun, "lastError", lastError),
		"lastBuild", report,
	)
}

// projectRoots 单个项目的目录失效（被改名或删除）时仍原样返回登记值和错误，页面才能改路径或取消接入。
func projectRoots(item *ojson.Object) (any, string) {
	roots, err := config.ProjectRoots(item)
	if err == nil {
		list := []any{}
		for _, root := range roots {
			list = append(list, ojson.NewObject("prefix", root.Prefix, "path", root.Path))
		}
		return list, ""
	}
	if !config.IsConfigError(err) {
		abort()
	}
	raw, ok := item.Value("roots").([]any)
	if !ok {
		docs, present := item.Get("docsPath")
		if !present {
			docs = ""
		}
		raw = []any{ojson.NewObject("prefix", "", "path", docs)}
	}
	list := []any{}
	for _, entry := range raw {
		obj, ok := entry.(*ojson.Object)
		if !ok {
			continue
		}
		list = append(list, ojson.NewObject("prefix", strOr(obj.Value("prefix")), "path", strOr(obj.Value("path"))))
	}
	return list, err.Error()
}

// strOr 把值转成字符串；空值（nil、false、0、空串等）返回 ""。
func strOr(value any) string {
	if !textutil.Truthy(value) {
		return ""
	}
	return textutil.Str(value)
}

func (s *Server) configPayload() *ojson.Object {
	cfg := loadConfigOrAbort(s.ConfigPath)
	report := map[string]*ojson.Object{}
	if list, ok := build.LoadReport(s.Output).Value("projects").([]any); ok {
		for _, item := range list {
			if obj, ok := item.(*ojson.Object); ok {
				report[textutil.Str(obj.Value("id"))] = obj
			}
		}
	}
	projects := []any{}
	for _, raw := range config.Projects(cfg) {
		item := raw.(*ojson.Object)
		id := item.Value("id")
		roots, rootError := projectRoots(item)
		entry := ojson.NewObject("id", id, "name", item.Value("name"), "roots", roots, "error", rootError)
		home, ok := item.Get("home")
		if !ok {
			home = ""
		}
		exclude, ok := item.Get("exclude")
		if !ok {
			exclude = []any{}
		}
		types := []any{}
		for _, ext := range config.Types(item) {
			types = append(types, ext)
		}
		entry.Set("home", home)
		entry.Set("exclude", exclude)
		entry.Set("types", types)
		entry.Set("url", "/projects/"+textutil.Str(id)+"/")
		built := report[textutil.Str(id)]
		for _, pair := range []struct {
			key      string
			fallback any
		}{{"fileCount", 0}, {"updatedAt", 0}, {"skipped", []any{}}, {"warnings", []any{}}, {"unusedExclude", []any{}}} {
			value := pair.fallback
			if built != nil {
				if got, ok := built.Get(pair.key); ok {
					value = got
				}
			}
			entry.Set(pair.key, value)
		}
		projects = append(projects, entry)
	}
	mode, _, lastError := s.Auto.State()
	return ojson.NewObject(
		"settings", settingsOf(cfg),
		"server", serverOf(cfg),
		"autoSync", ojson.NewObject("mode", mode, "lastError", lastError),
		"projects", projects,
		"typeGroups", typeGroupsPayload(),
	)
}

// typeGroupsPayload 给后台的扩展名勾选框用，和 config.KindFor 能识别的扩展名一致。
func typeGroupsPayload() []any {
	out := []any{}
	for _, group := range config.TypeGroups() {
		exts := make([]any, len(group.Extensions))
		for i, ext := range group.Extensions {
			exts[i] = ext
		}
		out = append(out, ojson.NewObject("label", group.Label, "extensions", exts))
	}
	return out
}

// ---------------------------------------------------------------- POST 接口

// contentType 取 Content-Type 分号前的部分并转小写，格式不对时当作 text/plain。
func contentType(values []string) string {
	if len(values) == 0 {
		return "text/plain"
	}
	value, _, _ := strings.Cut(values[0], ";")
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.Count(value, "/") != 1 {
		return "text/plain"
	}
	return value
}

// parseBody 类型必须是 application/json；不接受分块传输；大小不超过上限；字段必须符合声明。
func (q *request) parseBody(spec api.RouteSpec) (*ojson.Object, bool) {
	if contentType(q.r.Header.Values("Content-Type")) != "application/json" {
		q.reply(415, errorBody("请求必须使用application/json"))
		return nil, false
	}
	invalid := func() (*ojson.Object, bool) {
		_, err := api.ValidateBody(api.RouteSpec{Required: spec.Required, Optional: spec.Optional, Limit: -1}, nil)
		q.reply(400, errorBody(err.Error()))
		return nil, false
	}
	size := q.r.ContentLength
	if len(q.r.TransferEncoding) > 0 || size < 0 || size > spec.Limit {
		return invalid()
	}
	raw := make([]byte, 0, size)
	control := http.NewResponseController(q.w)
	buf := make([]byte, 32*1024)
	for int64(len(raw)) < size {
		// 每次读取最多等 10 秒，超时当作请求格式无效。
		_ = control.SetReadDeadline(time.Now().Add(10 * time.Second))
		want := size - int64(len(raw))
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, err := q.r.Body.Read(buf[:want])
		raw = append(raw, buf[:n]...)
		if err != nil {
			if err == io.EOF && int64(len(raw)) == size {
				break
			}
			return invalid()
		}
	}
	_ = control.SetReadDeadline(time.Time{})
	body, err := api.ValidateBody(spec, raw)
	if err != nil {
		q.reply(400, errorBody(err.Error()))
		return nil, false
	}
	return body, true
}

func (q *request) doPost() {
	// 出错时可能没读完请求体，剩下的字节会被当成下一个请求；写操作不复用连接。
	q.w.Header().Set("Connection", "close")
	if !q.allowed(true) {
		return
	}
	if spec, ok := api.PostRoutes[q.full]; ok {
		if body, ok := q.parseBody(spec); ok {
			q.reply(q.s.Vinx.ServePost(q.full, body))
		}
		return
	}
	spec, ok := writeEndpoints[q.full]
	if !ok {
		q.reply(404, errorBody("接口不存在"))
		return
	}
	body, ok := q.parseBody(spec)
	if !ok {
		return
	}
	if !q.s.Lock.TryLock() {
		q.reply(409, errorBody("正在同步文档，请稍后重试"))
		return
	}
	defer q.s.Lock.Unlock()
	status, data := q.s.write(q.full, body)
	q.reply(status, data)
}

// noProject 是一个不可能通过 ID 校验的值：请求里的 id 不是字符串时，按「项目ID不存在」处理。
const noProject = "\x00"

func idOf(body *ojson.Object) string {
	if id, ok := body.Value("id").(string); ok {
		return id
	}
	return noProject
}

// write 执行管理写操作（调用方已持有操作锁），返回状态码和响应。
func (s *Server) write(path string, body *ojson.Object) (int, any) {
	var results []*build.ProjectResult
	var err error
	switch path {
	case "/api/settings":
		var settings *ojson.Object
		if settings, err = build.SaveSettings(s.ConfigPath, body); err == nil {
			if err = s.Auto.Apply(); err == nil {
				mode, _, _ := s.Auto.State()
				return 200, ojson.NewObject("settings", settings, "autoSync", ojson.NewObject("mode", mode))
			}
		}
		return writeError(err)
	case "/api/projects":
		docs, ok := body.Value("docsPath").(string)
		if !ok {
			return writeError(config.Errorf("请输入文档目录的绝对路径"))
		}
		results, err = build.RegisterPath(s.ConfigPath, s.Output, docs)
	case "/api/project/update":
		results, err = build.UpdateProject(s.ConfigPath, s.Output, idOf(body), build.ProjectUpdate{
			Name: body.Value("name"), Exclude: body.Value("exclude"), Roots: body.Value("roots"),
			Types: body.Value("types"),
		})
	case "/api/project/remove":
		if err = build.Unregister(s.ConfigPath, s.Output, idOf(body)); err == nil {
			results, err = build.Refresh(s.ConfigPath, s.Output, "", "")
		}
	default:
		results, err = build.Refresh(s.ConfigPath, s.Output, "", "")
	}
	if err == nil && (path == "/api/projects" || path == "/api/project/remove") {
		err = s.Auto.Apply()
	}
	if err != nil {
		return writeError(err)
	}
	return 200, buildPayload(results)
}

// writeError 把 POST 处理中的错误映射成响应：ConfigError 只回冒号前的部分（不回显主机路径）；文件错误回 500。
// 其余意外错误不写响应，直接断开连接。
func writeError(err error) (int, any) {
	if config.IsConfigError(err) {
		message, _, _ := strings.Cut(err.Error(), ":")
		return 400, errorBody(message)
	}
	var panicErr *config.PanicError
	if errors.As(err, &panicErr) || strings.HasPrefix(err.Error(), "ValueError: ") || strings.HasPrefix(err.Error(), "re.error: ") {
		abort()
	}
	return 500, errorBody("文件读取或写入失败，请检查目录权限、文档编码及磁盘空间后重试")
}

// isoNow 返回当前 UTC 时间的 ISO 8601 文本（微秒精度，带 +00:00）。
func isoNow() string {
	now := time.Now().UTC()
	if now.Nanosecond()/1000 == 0 {
		return now.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return now.Format("2006-01-02T15:04:05.000000") + "+00:00"
}

func buildPayload(results []*build.ProjectResult) *ojson.Object {
	notices := []any{}
	files, reused, skipped := 0, 0, 0
	projects := []any{}
	for _, item := range results {
		for _, entry := range item.Warnings {
			notices = append(notices, item.ID+"/"+entry)
		}
		files += item.FileCount
		reused += item.Reused
		skipped += len(item.Skipped)
		projects = append(projects, ojson.NewObject("id", item.ID, "name", item.Name, "fileCount", item.FileCount,
			"unusedExclude", item.UnusedExclude))
	}
	shown := notices
	if len(shown) > 10 {
		shown = shown[:10]
	}
	return ojson.NewObject(
		"projectCount", len(results),
		"fileCount", files,
		"reusedCount", reused,
		"skippedCount", skipped,
		"warningCount", len(notices),
		"warnings", shown,
		"completedAt", isoNow(),
		"projects", projects,
	)
}
