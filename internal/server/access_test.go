package server_test

// 外部访问（经反向代理）的登录与免登录规则。

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/testutil"
)

const externalHost = "docs.example.com"

// external 模拟反代转发来的请求：来源仍是本机回环地址（等同经 tailnet 进来），但带转发头。
func external(ip string, extra map[string]string) map[string]string {
	headers := map[string]string{"Host": externalHost, "X-Forwarded-For": ip, "X-Forwarded-Proto": "https"}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

func externalPost(ip string, extra map[string]string) map[string]string {
	return external(ip, mergeHeaders(map[string]string{"Origin": "https://" + externalHost}, extra))
}

func mergeHeaders(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range a {
		out[key] = value
	}
	for key, value := range b {
		out[key] = value
	}
	return out
}

// accessFixture 接入两个项目（一个公开），发布一个新页面和一个两天前的页面。
type accessFixture struct {
	*fixture
	public, private string
	fresh, old      string
}

func setupAccess(t *testing.T) *accessFixture {
	tool := makeTool(t)
	writeFile(t, filepath.Join(tool, "web", "admin.html"), "<html>admin</html>")
	f := startServer(t, tool)
	base := testutil.TempDir(t)
	ids := []string{}
	for _, name := range []string{"pub", "priv"} {
		source := filepath.Join(base, name, "docs")
		writeFile(t, filepath.Join(source, "README.md"), "# "+name+"\n\n正文 "+name)
		r := f.post("/api/projects", map[string]any{"docsPath": source}, nil)
		if r.status != 200 {
			t.Fatal(r.status, string(r.body))
		}
		projects := r.json(t)["projects"].([]any)
		ids = append(ids, projects[len(projects)-1].(map[string]any)["id"].(string))
	}
	if r := f.post("/api/project/update", map[string]any{"id": ids[0], "public": true}, nil); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	page := filepath.Join(base, "page")
	writeFile(t, filepath.Join(page, "index.html"), "<html><body>新页面</body></html>")
	writeFile(t, filepath.Join(page, "old.html"), "<html><body>旧页面</body></html>")
	registry := pages.DefaultRegistry(f.tool)
	original := pages.Now
	now := original()
	pages.Now = func() int64 { return now - 2*86400 }
	old, _, err := pages.Publish(registry, filepath.Join(page, "old.html"), "", nil, "")
	pages.Now = original
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := pages.Publish(registry, filepath.Join(page, "index.html"), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return &accessFixture{fixture: f, public: ids[0], private: ids[1], fresh: pages.Str(fresh, "id"), old: pages.Str(old, "id")}
}

func (a *accessFixture) setPassword(t *testing.T, password string) {
	t.Helper()
	if r := a.post("/api/auth/password", map[string]any{"password": password}, nil); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
}

func (a *accessFixture) login(t *testing.T, ip, password string) response {
	t.Helper()
	return a.post("/api/login", map[string]any{"password": password}, externalPost(ip, nil))
}

func sessionCookie(t *testing.T, r response) string {
	t.Helper()
	for _, cookie := range (&http.Response{Header: r.headers}).Cookies() {
		if cookie.Name == "vinx_session" {
			return "vinx_session=" + cookie.Value
		}
	}
	t.Fatal("没有会话 Cookie", r.headers)
	return ""
}

func TestInternalRequestsAreUnchanged(t *testing.T) {
	a := setupAccess(t)
	a.setPassword(t, "correct horse")
	for _, path := range []string{"/api/config", "/api/status", "/a/" + a.old + "/", "/projects/" + a.private + "/index.html", "/admin.html"} {
		if r := a.get(path, nil); r.status != 200 {
			t.Fatal(path, r.status)
		}
	}
	if r := a.get("/", nil); bytes.Contains(r.body, []byte("data-vinx-auth")) {
		t.Fatal("内网页面不应带登录标记", string(r.body))
	}
	catalog := string(a.get("/catalog.json", nil).body)
	if !strings.Contains(catalog, a.private) {
		t.Fatal(catalog)
	}
}

func TestGuestSeesOnlyPublicContent(t *testing.T) {
	a := setupAccess(t)
	guest := external("203.0.113.9", nil)
	for _, path := range []string{"/api/config", "/api/status", "/api/artifacts", "/api/comments", "/api/file?target=doc:" + a.private + "/README.md",
		"/projects/" + a.private + "/index.html", "/projects/" + a.private + "/content/README.md", "/projects//" + a.private + "/index.html",
		"/projects/" + a.public + "%2F..%2F" + a.private + "/index.html", "/admin.html", "/comments.html", "/published.html"} {
		r := a.get(path, guest)
		if r.status != 401 && r.status != 404 {
			t.Fatal(path, r.status, string(r.body))
		}
	}
	if r := a.post("/api/refresh", nil, externalPost("203.0.113.9", nil)); r.status != 401 {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.post("/api/comments", map[string]any{"target": "a:" + a.fresh + "/index.html", "anchor": map[string]any{"type": "file"}, "body": "x"},
		externalPost("203.0.113.9", nil)); r.status != 401 {
		t.Fatal("未登录不能写批注", r.status)
	}
	r := a.get("/admin.html", external("203.0.113.9", map[string]string{"Accept": "text/html"}))
	if r.status != 302 || r.headers.Get("Location") != "/login.html?next=%2Fadmin.html" {
		t.Fatal(r.status, r.headers)
	}
	r = a.get("/projects/"+a.public+"/index.html", guest)
	if r.status != 200 || !bytes.Contains(r.body, []byte(`data-vinx-auth="guest"`)) || r.headers.Get("ETag") != "" {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.get("/projects/"+a.public+"/content/README.md", guest); r.status != 200 {
		t.Fatal(r.status)
	}
	catalog := string(a.get("/catalog.json", guest).body)
	if !strings.Contains(catalog, a.public) || strings.Contains(catalog, a.private) || !strings.Contains(catalog, `"recent"`) {
		t.Fatal(catalog)
	}
	index := string(a.get("/search-index.json", guest).body)
	if !strings.Contains(index, "正文 pub") || strings.Contains(index, "正文 priv") {
		t.Fatal(index)
	}
	if r := a.get("/", guest); r.status != 200 || !bytes.Contains(r.body, []byte(`data-vinx-auth="guest"`)) {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.get("/healthz", guest); r.status != 200 {
		t.Fatal(r.status)
	}
	info := a.get("/api/auth", guest).json(t)
	if info["external"] != true || info["authenticated"] != false || info["passwordSet"] != false {
		t.Fatal(info)
	}
}

func TestShortLinkWindow(t *testing.T) {
	a := setupAccess(t)
	guest := external("203.0.113.9", nil)
	if r := a.get("/a/"+a.fresh+"/index.html", guest); r.status != 200 || !bytes.Contains(r.body, []byte("新页面")) {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.get("/a/"+a.fresh+"/"+pages.ReservedDir+"/version", guest); r.status != 200 {
		t.Fatal(r.status)
	}
	if r := a.get("/a/"+a.old+"/old.html", guest); r.status != 401 {
		t.Fatal("过期的短链接要登录", r.status)
	}
	if r := a.get("/a/"+a.old+"/old.html", external("203.0.113.9", map[string]string{"Accept": "text/html"})); r.status != 302 ||
		!strings.HasPrefix(r.headers.Get("Location"), "/login.html?next=") {
		t.Fatal(r.status, r.headers)
	}
	list := a.get("/api/artifacts", nil).json(t)["artifacts"].([]any)
	for _, item := range list {
		entry := item.(map[string]any)
		open := entry["publicUntil"].(float64) > entry["publishedAt"].(float64)
		if !open {
			t.Fatal("publicUntil 应在发布时间之后", entry)
		}
	}
	// 免登录时长设为 0：短链接一律要登录。
	if r := a.post("/api/auth/settings", map[string]any{"linkHours": 0}, nil); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.get("/a/"+a.fresh+"/index.html", guest); r.status != 401 {
		t.Fatal(r.status)
	}
}

func TestUnknownShortCodesAreThrottled(t *testing.T) {
	a := setupAccess(t)
	for i := 0; i < 5; i++ {
		if r := a.get("/a/zzzzz"+string(rune('0'+i))+"/", external("198.51.100.7", nil)); r.status != 404 {
			t.Fatal(r.status)
		}
	}
	if r := a.get("/a/"+a.fresh+"/index.html", external("198.51.100.7", nil)); r.status != 429 {
		t.Fatal("连续猜错后应限流", r.status)
	}
	if r := a.get("/a/"+a.fresh+"/index.html", external("203.0.113.9", nil)); r.status != 200 {
		t.Fatal("别的来源不受影响", r.status)
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	a := setupAccess(t)
	if r := a.login(t, "203.0.113.9", "anything"); r.status != 401 || !strings.Contains(r.json(t)["error"].(string), "尚未设置") {
		t.Fatal("没设密码时不能登录", r.status, string(r.body))
	}
	if r := a.post("/api/auth/password", map[string]any{"password": "short"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	a.setPassword(t, "correct horse")
	if r := a.login(t, "203.0.113.9", "wrong horse"); r.status != 401 {
		t.Fatal(r.status)
	}
	r := a.login(t, "203.0.113.9", "correct horse")
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	set := r.headers.Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=None", "Path=/"} {
		if !strings.Contains(set, want) {
			t.Fatal(want, set)
		}
	}
	cookie := sessionCookie(t, r)
	user := external("203.0.113.9", map[string]string{"Cookie": cookie})
	for _, path := range []string{"/api/config", "/a/" + a.old + "/old.html", "/projects/" + a.private + "/index.html", "/admin.html"} {
		if r := a.get(path, user); r.status != 200 {
			t.Fatal(path, r.status)
		}
	}
	if r := a.get("/", user); !bytes.Contains(r.body, []byte(`data-vinx-auth="user"`)) {
		t.Fatal(string(r.body))
	}
	if catalog := string(a.get("/catalog.json", user).body); !strings.Contains(catalog, a.private) {
		t.Fatal(catalog)
	}
	// 改动接口按转发头比对 Origin：https://<外部域名> 才放行。
	if r := a.post("/api/refresh", nil, externalPost("203.0.113.9", map[string]string{"Cookie": cookie})); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	if r := a.post("/api/refresh", nil, externalPost("203.0.113.9", map[string]string{"Cookie": cookie, "Origin": "http://" + externalHost})); r.status != 403 {
		t.Fatal(r.status)
	}
	// 外部登录后也不能改密码和外部访问设置。
	if r := a.post("/api/auth/password", map[string]any{"password": "another horse"}, externalPost("203.0.113.9", map[string]string{"Cookie": cookie})); r.status != 403 {
		t.Fatal(r.status)
	}
	if r := a.post("/api/logout", nil, externalPost("203.0.113.9", map[string]string{"Cookie": cookie})); r.status != 200 {
		t.Fatal(r.status)
	}
	if r := a.get("/api/config", user); r.status != 401 {
		t.Fatal("退出后会话失效", r.status)
	}
	// 改密码后旧会话全部失效。
	cookie = sessionCookie(t, a.login(t, "203.0.113.9", "correct horse"))
	a.setPassword(t, "another horse")
	if r := a.get("/api/config", external("203.0.113.9", map[string]string{"Cookie": cookie})); r.status != 401 {
		t.Fatal(r.status)
	}
	if config := a.get("/api/config", nil).json(t)["auth"].(map[string]any); config["passwordSet"] != true || config["sessionCount"] != float64(0) {
		t.Fatal(config)
	}
}

func TestLoginLockout(t *testing.T) {
	a := setupAccess(t)
	a.setPassword(t, "correct horse")
	for i := 0; i < 5; i++ {
		a.login(t, "198.51.100.7", "wrong")
	}
	if r := a.login(t, "198.51.100.7", "correct horse"); r.status != 429 {
		t.Fatal("锁定期间正确密码也不放行", r.status)
	}
	if r := a.login(t, "203.0.113.9", "correct horse"); r.status != 200 {
		t.Fatal(r.status)
	}
}

func TestExternalHostWithoutForwardHeaders(t *testing.T) {
	a := setupAccess(t)
	if r := a.post("/api/auth/settings", map[string]any{"externalHosts": []any{"Docs.Example.com:443"}}, nil); r.status != 200 ||
		!strings.Contains(string(r.body), `"docs.example.com"`) {
		t.Fatal(r.status, string(r.body))
	}
	// 反代改写了 Host 但没加转发头时，靠登记的域名识别。
	if r := a.get("/api/config", map[string]string{"Host": externalHost}); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := a.get("/api/config", map[string]string{"Host": "localhost:8000"}); r.status != 200 {
		t.Fatal(r.status)
	}
	// 只有转发头、Host 是本机地址，也按外部处理。
	if r := a.get("/api/config", map[string]string{"Host": "127.0.0.1:8000", "X-Forwarded-For": "203.0.113.9"}); r.status != 401 {
		t.Fatal(r.status)
	}
}
