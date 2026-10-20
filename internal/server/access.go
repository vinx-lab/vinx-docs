package server

// 外部访问控制：经反向代理进来的请求（带转发头，或 Host 是登记的外部域名）要登录；
// 未登录时只放行发布后一段时间内的短链接、标为公开的项目和页面外壳。内网直连的请求不经过这里，行为不变。

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/api"
	"github.com/vinx-lab/vinx-docs/internal/auth"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// forwardHeaders 任一出现就按外部请求处理：本机直连不会带这些头，带了只会让请求受到更多限制。
var forwardHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-Ip"}

// guestPages 是未登录时可以打开的页面外壳；内容由各自读取的数据决定（项目清单、搜索索引会按公开项目过滤）。
var guestPages = map[string]bool{"/": true, "/index.html": true, "/read.html": true, "/login.html": true}

// authEndpoints 不论是否登录都可以调用。
var authEndpoints = map[string]bool{"/api/auth": true, "/api/login": true, "/api/logout": true}

// isExternal 判断请求是否来自外部（反向代理）。
func (s *Server) isExternal(r *http.Request) bool {
	for _, name := range forwardHeaders {
		if len(r.Header.Values(name)) > 0 {
			return true
		}
	}
	return s.Auth.IsExternalHost(r.Host)
}

// scheme 和 originHost 是浏览器看到的协议和主机：外部请求按转发头，内网按连接本身。
func (q *request) scheme() string {
	if q.external && strings.EqualFold(firstValue(q.r.Header.Values("X-Forwarded-Proto")), "https") {
		return "https"
	}
	return "http"
}

func (q *request) originHost() string {
	if q.external {
		if host := firstValue(q.r.Header.Values("X-Forwarded-Host")); host != "" {
			return host
		}
	}
	return q.r.Host
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	first, _, _ := strings.Cut(values[0], ",")
	return strings.TrimSpace(first)
}

// clientIP 用于失败限流：外部请求取 X-Forwarded-For 的最后一项（反代追加的、它看到的来源），否则取连接地址。
func (q *request) clientIP() string {
	if values := q.r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		parts := strings.Split(values[len(values)-1], ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(q.r.RemoteAddr)
	if err != nil {
		return q.r.RemoteAddr
	}
	return host
}

func (q *request) sessionToken() string {
	cookie, err := q.r.Cookie(auth.CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// guest 报告是否是未登录的外部访问者。
func (q *request) guest() bool { return q.external && !q.authed }

// gate 对外部请求做访问检查；不放行时写好响应并返回 false。内网请求总是放行。
func (q *request) gate() bool {
	if !q.external || q.authed || authEndpoints[q.path] || q.path == "/healthz" {
		return true
	}
	path := textutil.Unquote(q.path)
	segments := strings.Split(path, "/")
	for _, part := range segments {
		if part == ".." {
			q.sendPlain(404, "Not Found")
			return false
		}
	}
	switch {
	case guestPages[path], strings.HasPrefix(path, "/assets/"), strings.HasPrefix(path, "/vendor/"),
		path == "/catalog.json", path == "/search-index.json":
		return true
	case path == "/a" || strings.HasPrefix(path, "/a/"):
		return q.gateArtifact()
	case strings.HasPrefix(path, "/projects/") && len(segments) > 2:
		if segments[2] != "" && q.s.publicProjects()[segments[2]] {
			return true
		}
	}
	q.needLogin()
	return false
}

// needLogin 页面跳转到登录页（登录后回到原地址），接口和资源返回 401。
func (q *request) needLogin() {
	if strings.HasPrefix(q.path, "/api/") || q.r.Method != http.MethodGet ||
		!strings.Contains(q.r.Header.Get("Accept"), "text/html") {
		if strings.HasPrefix(q.path, "/api/") {
			q.reply(401, errorBody("需要登录"))
		} else {
			q.sendPlain(401, "需要登录")
		}
		return
	}
	q.sendPlain(302, "", "Location", "/login.html?next="+url.QueryEscape(q.full))
}

// gateArtifact 短链接在发布后 linkHours 小时内可以免登录只读；不存在的短码计入失败限流，防止枚举。
func (q *request) gateArtifact() bool {
	ip := q.clientIP()
	if q.s.Auth.Blocked(ip) {
		q.sendPlain(429, "请求过多，请稍后再试")
		return false
	}
	parts := strings.SplitN(q.path, "/", 4)
	if len(parts) < 3 {
		q.sendPlain(404, "页面不存在或已取消发布")
		return false
	}
	registry, err := pages.LoadRegistry(q.s.RegistryPath)
	if err != nil {
		q.sendPlain(500, "发布登记不可读")
		return false
	}
	record := pages.Find(registry, parts[2])
	if record == nil {
		q.s.Auth.Fail(ip)
		q.sendPlain(404, "页面不存在或已取消发布")
		return false
	}
	if q.s.linkOpen(record) {
		return true
	}
	q.needLogin()
	return false
}

// publicUntil 返回短链接免登录截止时间（Unix 秒）；0 表示不免登录。
func (s *Server) publicUntil(record *ojson.Object) int64 {
	hours := s.Auth.Settings().LinkHours
	published, ok := textutil.Int(record.Value("publishedAt"))
	if hours <= 0 || !ok || !published.IsInt64() {
		return 0
	}
	return published.Int64() + int64(hours)*3600
}

func (s *Server) linkOpen(record *ojson.Object) bool {
	return time.Now().Unix() < s.publicUntil(record)
}

// publicProjects 返回标为公开阅读的项目 ID。
func (s *Server) publicProjects() map[string]bool {
	out := map[string]bool{}
	cfg, err := config.Load(s.ConfigPath)
	if err != nil {
		return out
	}
	for _, item := range config.Projects(cfg) {
		if obj, ok := item.(*ojson.Object); ok && obj.Value("public") == true {
			out[textutil.Str(obj.Value("id"))] = true
		}
	}
	return out
}

// serveGuestData 给未登录的外部访问者发送过滤后的项目清单和搜索索引：只保留公开项目。
func (q *request) serveGuestData(target, path string) bool {
	if !q.guest() || (path != "/catalog.json" && path != "/search-index.json") {
		return false
	}
	data, err := os.ReadFile(target)
	if err != nil {
		abort()
	}
	value, err := ojson.Decode(data)
	obj, ok := value.(*ojson.Object)
	if err != nil || !ok {
		abort()
	}
	// 列表键 → 列表项里标明所属项目的字段。
	lists := map[string]string{"projects": "id", "recent": "project"}
	if path == "/search-index.json" {
		lists = map[string]string{"documents": "project"}
	}
	public := q.s.publicProjects()
	out := obj.Copy()
	for key, field := range lists {
		kept := []any{}
		list, _ := obj.Value(key).([]any)
		for _, item := range list {
			if entry, ok := item.(*ojson.Object); ok && public[textutil.Str(entry.Value(field))] {
				kept = append(kept, entry)
			}
		}
		if out.Has(key) {
			out.Set(key, kept)
		}
	}
	body := []byte(ojson.Dumps(out, -1))
	q.setHeaders("Content-Type", "application/json", "Content-Length", strconv.Itoa(len(body)),
		"X-Content-Type-Options", "nosniff", "Referrer-Policy", "no-referrer", "Cache-Control", "no-store",
		"Content-Security-Policy", SitePolicy)
	q.w.WriteHeader(200)
	if !q.isHead {
		_, _ = q.w.Write(body)
	}
	return true
}

// markAccess 在外部访问的页面根元素上标明登录状态（data-vinx-auth="guest" / "user"），
// 页面据此隐藏编辑、批注和管理入口，并显示登录或退出按钮。内网访问不加标记。
func (q *request) markAccess(body []byte) []byte {
	if !q.external {
		return body
	}
	state := "user"
	if !q.authed {
		state = "guest"
	}
	text := string(body)
	at := strings.Index(text, "<html")
	if at < 0 {
		return body
	}
	at += len("<html")
	return []byte(text[:at] + ` data-vinx-auth="` + state + `"` + text[at:])
}

// ---------------------------------------------------------------- 登录接口

func (q *request) authStatus() *ojson.Object {
	return ojson.NewObject("external", q.external, "authenticated", !q.guest(), "passwordSet", q.s.Auth.PasswordSet())
}

func (q *request) setSessionCookie(value string, maxAge int) {
	cookie := &http.Cookie{Name: auth.CookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if q.scheme() == "https" {
		// 短链接页面在 CSP 沙箱里，origin 是 null，它加载图片等资源属于跨站请求；Lax 不带 Cookie，
		// 过了免登录时间后已登录的人也会看到资源缺失。改动接口另有 Origin 检查防跨站请求。
		cookie.Secure = true
		cookie.SameSite = http.SameSiteNoneMode
	}
	q.w.Header().Add("Set-Cookie", cookie.String())
}

// login 处理 POST /api/login。
func (q *request) login(body *ojson.Object) {
	password, _ := body.Value("password").(string)
	token, err := q.s.Auth.Login(password, q.clientIP())
	switch err {
	case nil:
		q.authed = true
		q.setSessionCookie(token, auth.SessionDays*24*3600)
		q.reply(200, q.authStatus())
	case auth.ErrLocked:
		q.reply(429, errorBody(err.Error()))
	case auth.ErrWrong, auth.ErrNoPassword:
		q.reply(401, errorBody(err.Error()))
	default:
		q.reply(500, errorBody("登录失败，请查看服务日志"))
	}
}

func (q *request) logout() {
	if token := q.sessionToken(); token != "" {
		if err := q.s.Auth.Logout(token); err != nil {
			q.reply(500, errorBody("退出失败，请重试"))
			return
		}
	}
	q.authed = false
	q.setSessionCookie("", -1)
	q.reply(200, q.authStatus())
}

// authAdmin 处理密码和外部访问设置；只允许内网操作，外部登录后也不能改。
func (q *request) authAdmin(body *ojson.Object) {
	if q.external {
		q.reply(403, errorBody("访问密码和外部访问设置只能在内网修改"))
		return
	}
	var err error
	switch q.full {
	case "/api/auth/password":
		password, _ := body.Value("password").(string)
		err = q.s.Auth.SetPassword(password)
	case "/api/auth/settings":
		next := q.s.Auth.Settings()
		if value, present := body.Get("externalHosts"); present {
			list, ok := value.([]any)
			if !ok {
				q.reply(400, errorBody("externalHosts必须是域名数组"))
				return
			}
			next.ExternalHosts = []string{}
			for _, item := range list {
				text, ok := item.(string)
				if !ok {
					q.reply(400, errorBody("externalHosts必须是域名数组"))
					return
				}
				next.ExternalHosts = append(next.ExternalHosts, text)
			}
		}
		if value, present := body.Get("linkHours"); present {
			n, ok := textutil.Int(value)
			if !ok || !n.IsInt64() {
				q.reply(400, errorBody("linkHours必须是整数"))
				return
			}
			next.LinkHours = int(n.Int64())
		}
		_, err = q.s.Auth.SaveSettings(next)
	case "/api/auth/logout-all":
		err = q.s.Auth.LogoutAll()
	}
	if err != nil {
		if config.IsConfigError(err) {
			q.reply(400, errorBody(err.Error()))
			return
		}
		q.reply(500, errorBody("保存失败，请检查家目录权限后重试"))
		return
	}
	q.reply(200, q.s.authPayload())
}

// authPayload 是管理后台看到的外部访问状态；不含密码摘要和会话令牌。
func (s *Server) authPayload() *ojson.Object {
	settings := s.Auth.Settings()
	hosts := []any{}
	for _, host := range settings.ExternalHosts {
		hosts = append(hosts, host)
	}
	return ojson.NewObject("passwordSet", s.Auth.PasswordSet(), "externalHosts", hosts,
		"linkHours", settings.LinkHours, "sessionCount", s.Auth.SessionCount())
}

// authPosts 是认证相关的 POST 接口及其请求体约束。
var authPosts = map[string]api.RouteSpec{
	"/api/login":           {Required: []string{"password"}, Limit: 4096},
	"/api/logout":          {Limit: 4096},
	"/api/auth/password":   {Required: []string{"password"}, Limit: 4096},
	"/api/auth/settings":   {Optional: []string{"externalHosts", "linkHours"}, Limit: 65536},
	"/api/auth/logout-all": {Limit: 4096},
}

// servePostAuth 处理认证相关的 POST 接口。
func (q *request) servePostAuth(body *ojson.Object) {
	switch q.full {
	case "/api/login":
		q.login(body)
	case "/api/logout":
		q.logout()
	default:
		q.authAdmin(body)
	}
}
