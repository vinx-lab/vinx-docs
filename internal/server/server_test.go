package server_test

// HTTP 层测试：管理接口、短链接、静态头、不支持的方法、长连接和进程管理。

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vinx-lab/vinx-docs/assets"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/server"
	"github.com/vinx-lab/vinx-docs/internal/testutil"
)

const childEnv = "VINX_SERVER_TEST_CHILD"

// TestMain：manage 的 start 会以 "<程序> serve" 启动子进程；测试里程序就是测试二进制本身，
// 带上 childEnv 时直接作为服务运行。
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		assets.Install()
		if err := server.Serve(os.Getenv("VINX_DOCS_HOME")); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeTool 家目录自带最小的 web/ 和 vendor/。
func makeTool(t *testing.T) string {
	t.Helper()
	tool := filepath.Join(testutil.TempDir(t), "tool")
	writeFile(t, filepath.Join(tool, "web", "index.html"), "<html>index</html>")
	writeFile(t, filepath.Join(tool, "web", "project.html"), "<html>project</html>")
	if err := os.MkdirAll(filepath.Join(tool, "web", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	vendor := map[string]string{"docsify.min.js": "docsify", "search.min.js": "search", "vue.css": "vue", "LICENSE": "license"}
	hashes := map[string]string{}
	for name, content := range vendor {
		writeFile(t, filepath.Join(tool, "vendor", name), content)
		sum := sha256.Sum256([]byte(content))
		hashes[name] = hex.EncodeToString(sum[:])
	}
	data, _ := json.Marshal(map[string]any{"files": hashes})
	writeFile(t, filepath.Join(tool, "vendor", "manifest.json"), string(data))
	return tool
}

func setupSource(t *testing.T, dir string) string {
	t.Helper()
	source := filepath.Join(dir, "docs")
	writeFile(t, filepath.Join(source, "README.md"), "# 首页\n\n中文内容")
	writeFile(t, filepath.Join(source, "guide", "使用 说明.md"), "# 使用")
	return source
}

type fixture struct {
	t    *testing.T
	s    *server.Server
	tool string
	addr string
}

func startServer(t *testing.T, tool string) *fixture {
	t.Helper()
	// 测试只用 8765–8799 之间的空闲端口，只绑定 127.0.0.1。
	host := "127.0.0.1"
	var s *server.Server
	var err error
	for port := firstPort; port <= lastPort; port++ {
		if s, err = server.Create(tool, &host, &port); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() {
		s.Auto.Stop()
		s.Close()
	})
	return &fixture{t: t, s: s, tool: tool, addr: "127.0.0.1:" + strconv.Itoa(s.Port)}
}

type response struct {
	status  int
	headers http.Header
	body    []byte
	close   bool
}

func (r response) json(t *testing.T) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(r.body, &value); err != nil {
		t.Fatalf("不是 JSON：%s", r.body)
	}
	return value
}

// raw 原样发一个请求（Host、Origin 由调用方决定），读回响应。
func (f *fixture) raw(method, target string, headers map[string]string, body []byte) response {
	f.t.Helper()
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	defer conn.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", method, target)
	if _, ok := headers["Host"]; !ok {
		b.WriteString("Host: localhost:8000\r\n")
	}
	for key, value := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	if body != nil {
		if _, ok := headers["Content-Length"]; !ok {
			fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
		}
	}
	b.WriteString("Connection: close\r\n\r\n")
	conn.Write(append([]byte(b.String()), body...))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, data, resp.Close}
}

// post 默认 Content-Type JSON、Host localhost:8000、Origin 同源。
func (f *fixture) post(path string, body any, headers map[string]string) response {
	f.t.Helper()
	merged := map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:8000"}
	for key, value := range headers {
		merged[key] = value
	}
	for key, value := range merged {
		if value == "-" {
			delete(merged, key)
		}
	}
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	if data == nil {
		data = []byte{}
	}
	return f.raw("POST", path, merged, data)
}

func (f *fixture) get(path string, headers map[string]string) response {
	f.t.Helper()
	return f.raw("GET", path, headers, nil)
}

func TestSameOriginHostCanRegisterAndRefresh(t *testing.T) {
	for _, host := range []string{"192.168.1.2:8000", "docs.local:8000", "127.0.0.1:8000"} {
		f := startServer(t, makeTool(t))
		source := setupSource(t, testutil.TempDir(t))
		r := f.post("/api/projects", map[string]any{"docsPath": source}, map[string]string{"Origin": "http://" + host, "Host": host})
		if r.status != 200 || r.json(t)["projectCount"] != float64(1) || r.headers.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal(host, r.status, string(r.body))
		}
		if r := f.post("/api/refresh", nil, map[string]string{"Origin": "http://" + host, "Host": host}); r.status != 200 {
			t.Fatal(r.status, string(r.body))
		}
		if r := f.get("/api/healthz", map[string]string{"Host": host}); r.status != 200 {
			t.Fatal(r.status)
		}
	}
}

func TestRegisterAndRefreshRealDocuments(t *testing.T) {
	f := startServer(t, makeTool(t))
	source := setupSource(t, testutil.TempDir(t))
	r := f.post("/api/projects", map[string]any{"docsPath": source}, nil)
	result := r.json(t)
	if r.status != 200 || result["fileCount"] != float64(2) || strings.Contains(string(r.body), filepath.ToSlash(source)) {
		t.Fatal(r.status, string(r.body))
	}
	if !r.close {
		t.Fatal("POST 响应后应关闭连接")
	}
	cfg, err := config.Load(config.ConfigPath(f.tool))
	if err != nil {
		t.Fatal(err)
	}
	id := config.Projects(cfg)[0].(interface{ Value(string) any }).Value("id").(string)
	writeFile(t, filepath.Join(source, "README.md"), "# Changed")
	writeFile(t, filepath.Join(source, "new.md"), "# New")
	r = f.post("/api/refresh", nil, nil)
	if r.status != 200 || r.json(t)["fileCount"] != float64(3) {
		t.Fatal(string(r.body))
	}
	data, _ := os.ReadFile(filepath.Join(f.tool, ".runtime/site/projects", id, "content/README.md"))
	if string(data) != "# Changed" {
		t.Fatal(string(data))
	}
	os.Remove(filepath.Join(source, "new.md"))
	if r := f.post("/api/refresh", nil, nil); r.status != 200 {
		t.Fatal(r.status)
	}
	if _, err := os.Stat(filepath.Join(f.tool, ".runtime/site/projects", id, "content/new.md")); err == nil {
		t.Fatal("删除的文档应从站点移除")
	}
}

func TestRejectUntrustedOriginsWithoutMutation(t *testing.T) {
	f := startServer(t, makeTool(t))
	for _, origin := range []string{"-", "null", "http://evil.example", "http://192.168.1.2:8000"} {
		r := f.post("/api/refresh", nil, map[string]string{"Origin": origin})
		if r.status != 403 || r.headers.Get("Access-Control-Allow-Origin") != "" || r.json(t)["error"] != "请通过当前文档站点页面操作" {
			t.Fatal(origin, r.status, string(r.body))
		}
	}
	if _, err := os.Stat(filepath.Join(f.tool, ".runtime/site")); err == nil {
		t.Fatal("被拒绝的请求不应构建站点")
	}
}

func TestRejectRebindingHostAndFormRequests(t *testing.T) {
	f := startServer(t, makeTool(t))
	cases := []struct {
		status int
		r      response
	}{
		{403, f.post("/api/refresh", nil, map[string]string{"Host": "evil.example"})},
		{415, f.post("/api/refresh", nil, map[string]string{"Content-Type": "text/plain"})},
		{405, f.get("/api/refresh", map[string]string{"Origin": "http://localhost:8000"})},
		{400, f.post("/api/projects", map[string]any{"docsPath": "/tmp", "command": "ls"}, nil)},
		{400, f.post("/api/refresh", map[string]any{"docsPath": "/tmp"}, nil)},
		{400, f.raw("POST", "/api/refresh", map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:8000",
			"Transfer-Encoding": "chunked"}, []byte("2\r\n{}\r\n0\r\n\r\n"))},
		{400, f.raw("POST", "/api/refresh", map[string]string{"Content-Type": "application/json", "Origin": "http://localhost:8000",
			"Content-Length": "70000"}, []byte("{}"))},
	}
	for i, c := range cases {
		if c.r.status != c.status {
			t.Fatal(i, c.r.status, string(c.r.body))
		}
	}
	if msg := cases[3].r.json(t)["error"]; msg != "请求格式无效；本接口接受的字段：docsPath" {
		t.Fatal(msg)
	}
	if msg := cases[4].r.json(t)["error"]; msg != "请求格式无效；本接口接受的字段：无" {
		t.Fatal(msg)
	}
}

func TestOptionsAndBusyState(t *testing.T) {
	f := startServer(t, makeTool(t))
	r := f.raw("OPTIONS", "/api/refresh", map[string]string{"Origin": "http://localhost:8000",
		"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type"}, nil)
	if r.status != 204 || r.headers.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal(r.status)
	}
	if r := f.raw("OPTIONS", "/api/nope", map[string]string{"Origin": "http://localhost:8000"}, nil); r.status != 404 {
		t.Fatal(r.status)
	}
	f.s.Lock.Lock()
	busy := f.get("/api/healthz", nil).json(t)["busy"]
	status := f.post("/api/refresh", nil, nil).status
	f.s.Lock.Unlock()
	if status != 409 || busy != true {
		t.Fatal(status, busy)
	}
	if f.get("/api/healthz", nil).json(t)["busy"] != false {
		t.Fatal("释放后不应忙")
	}
}

func TestBadPathAndFailedRefreshAreReported(t *testing.T) {
	f := startServer(t, makeTool(t))
	if r := f.post("/api/projects", map[string]any{"docsPath": "/no-such-docsify-directory"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	writeFile(t, filepath.Join(f.tool, "config/projects.json"), "broken")
	r := f.post("/api/refresh", nil, nil)
	if r.status != 400 || strings.Contains(r.json(t)["error"].(string), f.tool) {
		t.Fatal(r.status, string(r.body))
	}
	// 配置坏了时 /api/status 不回响应、直接断开连接。
	conn, _ := net.Dial("tcp", f.addr)
	defer conn.Close()
	conn.Write([]byte("GET /api/status HTTP/1.1\r\nHost: localhost:8000\r\n\r\n"))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if data, _ := io.ReadAll(conn); len(data) != 0 {
		t.Fatalf("应断开连接，实际收到 %q", data)
	}
}

func TestConfigAndSettingsEndpointsDriveTheAdminPage(t *testing.T) {
	f := startServer(t, makeTool(t))
	source := setupSource(t, testutil.TempDir(t))
	if r := f.post("/api/projects", map[string]any{"docsPath": source}, nil); r.status != 200 {
		t.Fatal(string(r.body))
	}
	cfg := f.get("/api/config", nil).json(t)
	project := cfg["projects"].([]any)[0].(map[string]any)
	roots := project["roots"].([]any)
	if roots[0].(map[string]any)["path"] != filepath.ToSlash(source) || project["fileCount"].(float64) < 1 || cfg["settings"].(map[string]any)["autoSync"] != true {
		t.Fatal(cfg)
	}
	// 接入后自动同步已经启动。
	if mode := cfg["autoSync"].(map[string]any)["mode"].(string); !strings.HasPrefix(mode, "inotify(") && mode != "poll" && mode != "idle" {
		t.Fatal(mode)
	}
	if status := f.get("/api/status", nil).json(t); status["version"] != server.Version {
		t.Fatal(status)
	}
	saved := f.post("/api/settings", map[string]any{"autoSync": false, "debounceSeconds": 9}, nil).json(t)
	if saved["settings"].(map[string]any)["debounceSeconds"] != float64(9) || saved["autoSync"].(map[string]any)["mode"] != "off" {
		t.Fatal(saved)
	}
	id := project["id"].(string)
	updated := f.post("/api/project/update", map[string]any{"id": id, "name": "改名", "exclude": []string{"guide/", ".py"}}, nil).json(t)
	first := updated["projects"].([]any)[0].(map[string]any)
	if first["name"] != "改名" || fmt.Sprint(first["unusedExclude"]) != "[.py]" {
		t.Fatal(updated)
	}
	if _, err := os.Stat(filepath.Join(f.tool, ".runtime/site/projects", id, "raw/guide")); err == nil {
		t.Fatal("排除的目录不应发布")
	}
	removed := f.post("/api/project/remove", map[string]any{"id": id}, nil)
	if removed.status != 200 || removed.json(t)["projectCount"] != float64(0) {
		t.Fatal(string(removed.body))
	}
	if _, err := os.Stat(filepath.Join(source, "README.md")); err != nil {
		t.Fatal("取消接入不能动原文")
	}
}

func TestConfigSurvivesAVanishedRoot(t *testing.T) {
	f := startServer(t, makeTool(t))
	base := testutil.TempDir(t)
	kept := setupSource(t, filepath.Join(base, "kept"))
	gone := setupSource(t, filepath.Join(base, "gone"))
	for _, source := range []string{kept, gone} {
		if r := f.post("/api/projects", map[string]any{"docsPath": source}, nil); r.status != 200 {
			t.Fatal(string(r.body))
		}
	}
	renamed := filepath.Join(base, "renamed")
	testutil.Rename(t, gone, renamed)
	cfg := f.get("/api/config", nil).json(t)
	// 接口返回的路径统一用 / 分隔（Windows 上是 C:/...），这里也换成 / 再查。
	kept, gone = filepath.ToSlash(kept), filepath.ToSlash(gone)
	byPath := map[string]map[string]any{}
	for _, item := range cfg["projects"].([]any) {
		project := item.(map[string]any)
		byPath[project["roots"].([]any)[0].(map[string]any)["path"].(string)] = project
	}
	broken := byPath[gone]
	if byPath[kept]["error"] != "" || !strings.Contains(broken["error"].(string), "存在的目录") {
		t.Fatal(cfg)
	}
	r := f.post("/api/project/update", map[string]any{"id": byPath[kept]["id"], "name": "改名"}, nil)
	msg := r.json(t)["error"].(string)
	if r.status != 400 || !strings.HasPrefix(msg, "项目「"+broken["name"].(string)+"」：") || strings.Contains(msg, gone) {
		t.Fatal(r.status, msg)
	}
	fixed := f.post("/api/project/update", map[string]any{"id": broken["id"], "roots": []any{map[string]any{"prefix": "", "path": renamed}}}, nil)
	if fixed.status != 200 || fixed.json(t)["projectCount"] != float64(2) {
		t.Fatal(string(fixed.body))
	}
	if r := f.post("/api/project/remove", map[string]any{"id": broken["id"]}, nil); r.status != 200 || r.json(t)["projectCount"] != float64(1) {
		t.Fatal(string(r.body))
	}
}

func TestWriteEndpointsRejectUnknownFieldsAndBadOrigin(t *testing.T) {
	f := startServer(t, makeTool(t))
	r := f.post("/api/settings", map[string]any{"autoSync": true, "somethingElse": 1}, nil)
	if r.status != 400 || !strings.Contains(r.json(t)["error"].(string), "字段") {
		t.Fatal(string(r.body))
	}
	if r := f.post("/api/project/update", map[string]any{"name": "缺少id"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	if r := f.get("/api/config", map[string]string{"Origin": "http://evil.example"}); r.status != 403 {
		t.Fatal(r.status)
	}
	if r := f.post("/api/settings", map[string]any{"autoSync": true}, map[string]string{"Origin": "http://evil.example"}); r.status != 403 {
		t.Fatal(r.status)
	}
	if r := f.post("/api/project/update", map[string]any{"id": 5}, nil); r.status != 400 || r.json(t)["error"] != "项目ID不存在" {
		t.Fatal(string(r.body))
	}
}

func TestStaticSiteHeadersMatchTheOldNginxRules(t *testing.T) {
	f := startServer(t, makeTool(t))
	source := setupSource(t, testutil.TempDir(t))
	writeFile(t, filepath.Join(source, "guide", "page.html"), "<script>alert(1)</script>")
	writeFile(t, filepath.Join(source, "guide", "data.json"), "{}")
	if r := f.post("/api/projects", map[string]any{"docsPath": source}, nil); r.status != 200 {
		t.Fatal(string(r.body))
	}
	id := f.get("/api/config", nil).json(t)["projects"].([]any)[0].(map[string]any)["id"].(string)
	base := "/projects/" + id + "/"

	r := f.get("/", nil)
	if r.status != 200 || !strings.HasPrefix(r.headers.Get("Content-Type"), "text/html") ||
		!strings.Contains(r.headers.Get("Content-Security-Policy"), "default-src 'self'") || r.headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(r.status, r.headers)
	}
	if r := f.get(base+"preview/guide/page.html", nil); r.status != 200 || !strings.HasPrefix(r.headers.Get("Content-Security-Policy"), "sandbox;") {
		t.Fatal("项目里的 HTML 预览必须禁止脚本", r.headers)
	}
	if r := f.get(base+"raw/guide/data.json", nil); r.status != 200 || r.headers.Get("Content-Disposition") != "attachment" ||
		r.headers.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.headers.Get("Content-Security-Policy"), "sandbox;") {
		t.Fatal(r.headers)
	}
	if r := f.get(base+"content/README.md", nil); r.status != 200 || !strings.HasPrefix(r.headers.Get("Content-Type"), "text/plain") {
		t.Fatal(r.headers)
	}
	if r := f.get(strings.TrimSuffix(base, "/")+"?a=1", nil); r.status != 301 || r.headers.Get("Location") != base+"?a=1" {
		t.Fatal(r.status, r.headers)
	}
	for _, path := range []string{"/.runtime/report.json", base + ".hidden", "/../config/projects.json", "/%2e%2e/config/projects.json",
		"/nope.html", "/a%00b", "/x%5cy"} {
		if r := f.get(path, nil); r.status != 404 || string(r.body) != "Not Found" {
			t.Fatal(path, r.status)
		}
	}
	r = f.get("/index.html", nil)
	etag := r.headers.Get("ETag")
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("HEAD /index.html HTTP/1.1\r\nHost: localhost:8000\r\nConnection: close\r\n\r\n"))
	rawHead, _ := io.ReadAll(conn)
	conn.Close()
	if !bytes.Contains(rawHead, []byte("\r\nETag: "+etag+"\r\n")) {
		t.Fatalf("ETag 头名应保持原样大小写：%q", rawHead)
	}
	if r := f.get("/index.html", map[string]string{"If-None-Match": etag}); r.status != 304 || len(r.body) != 0 {
		t.Fatal(r.status)
	}
	head := f.raw("HEAD", "/index.html", nil, nil)
	if head.status != 200 || len(head.body) != 0 || head.headers.Get("Content-Length") != strconv.Itoa(len("<html>index</html>")) {
		t.Fatal(head.status, head.headers)
	}
	if r := f.raw("HEAD", "/api/status", nil, nil); r.status != 405 {
		t.Fatal(r.status)
	}
	if r := f.get("/healthz", nil); r.status != 200 || string(r.body) != "ok" {
		t.Fatal(r.status)
	}
	if r := f.raw("PUT", "/", nil, nil); r.status != 501 || !bytes.Contains(r.body, []byte("Unsupported method ('PUT')")) {
		t.Fatal(r.status, string(r.body))
	}
}

func TestEmbeddedAssetsAreServedWhenHomeHasNoWeb(t *testing.T) {
	assets.Install()
	home := filepath.Join(testutil.TempDir(t), "home")
	f := startServer(t, home)
	if r := f.post("/api/refresh", nil, nil); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	r := f.get("/assets/app.css", nil)
	want, _ := os.ReadFile(filepath.Join("..", "..", "assets", "web", "assets", "app.css"))
	if r.status != 200 || r.headers.Get("Content-Type") != "text/css; charset=utf-8" || (len(want) > 0 && !bytes.Equal(r.body, want)) {
		t.Fatal(r.status, r.headers)
	}
	if r := f.get("/vendor/docsify.min.js", nil); r.status != 200 || r.headers.Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatal(r.status)
	}
}

func TestArtifactShortLinks(t *testing.T) {
	f := startServer(t, makeTool(t))
	page := filepath.Join(testutil.TempDir(t), "page")
	writeFile(t, filepath.Join(page, "index.html"), `<html><head><title>原型</title><link rel="stylesheet" href="app.css"></head><body>hi</body></html>`)
	writeFile(t, filepath.Join(page, "app.css"), "body{}")
	writeFile(t, filepath.Join(page, "data.bin"), "\x00")
	writeFile(t, filepath.Join(page, "secret.txt"), "不在清单")
	writeFile(t, filepath.Join(page, "sub", "entry.html"), "<html><body>子目录</body></html>")
	registry := pages.DefaultRegistry(f.tool)
	record, _, err := pages.Publish(registry, filepath.Join(page, "index.html"), "", []string{"data.bin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	id := pages.Str(record, "id")
	nested, _, err := pages.Publish(registry, filepath.Join(page, "sub", "entry.html"), page, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := f.get("/a/"+id+"/", nil)
	if r.status != 200 || !bytes.Contains(r.body, []byte(`<script src="/assets/artifact-live.js"`)) ||
		r.headers.Get("Content-Security-Policy") != pages.PagePolicy || r.headers.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal(r.status, r.headers, string(r.body))
	}
	if r := f.get("/a/"+id+"/app.css", map[string]string{"Origin": "null"}); r.headers.Get("Access-Control-Allow-Origin") != "null" {
		t.Fatal(r.headers)
	}
	if r := f.get("/a/"+id, nil); r.status != 301 || r.headers.Get("Location") != "/a/"+id+"/" {
		t.Fatal(r.status)
	}
	if r := f.get("/a/"+pages.Str(nested, "id")+"/", nil); r.status != 302 || r.headers.Get("Location") != "/a/"+pages.Str(nested, "id")+"/sub/entry.html" {
		t.Fatal(r.status, r.headers)
	}
	if r := f.get("/a/"+id+"/data.bin", nil); r.headers.Get("Content-Disposition") != "attachment" {
		t.Fatal(r.headers)
	}
	if r := f.get("/a/"+id+"/"+pages.ReservedDir+"/version", nil); r.status != 200 || !bytes.HasPrefix(r.body, []byte(`{"version": "`)) {
		t.Fatal(string(r.body))
	}
	for _, path := range []string{"/a/" + id + "/secret.txt", "/a/zzzz/", "/a", "/a/"} {
		if r := f.get(path, nil); r.status != 404 || string(r.body) != "页面不存在或已取消发布" {
			t.Fatal(path, r.status, string(r.body))
		}
	}
	os.Remove(filepath.Join(page, "data.bin"))
	if r := f.get("/a/"+id+"/data.bin", nil); r.status != 404 || string(r.body) != "源文件已不存在或已不允许发布" {
		t.Fatal(r.status, string(r.body))
	}
	list := f.get("/api/artifacts", nil).json(t)["artifacts"].([]any)
	if len(list) != 2 {
		t.Fatal(list)
	}
	writeFile(t, registry, "broken")
	if r := f.get("/a/"+id+"/", nil); r.status != 500 || string(r.body) != "发布登记不可读" {
		t.Fatal(r.status, string(r.body))
	}
	if r := f.get("/api/artifacts", nil); r.status != 500 {
		t.Fatal(r.status)
	}
}

func TestKeepAliveAndPostCloses(t *testing.T) {
	f := startServer(t, makeTool(t))
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: localhost:8000\r\n\r\n"))
		resp, err := http.ReadResponse(reader, nil)
		if err != nil || resp.StatusCode != 200 || resp.Close {
			t.Fatal(i, err)
		}
		io.ReadAll(resp.Body)
	}
	conn.Write([]byte("POST /api/nope HTTP/1.1\r\nHost: localhost:8000\r\nOrigin: http://localhost:8000\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"))
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 404 {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := reader.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatal("POST 之后应关闭连接", n, err)
	}
}

func TestCreateReportsBusyPort(t *testing.T) {
	listener := listenInRange(t)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	host := "127.0.0.1"
	_, err := server.Create(testutil.TempDir(t), &host, &port)
	want := fmt.Sprintf("无法监听 127.0.0.1:%d：Address already in use（端口可能已被占用，可在配置的 server.port 里换一个）", port)
	if err == nil || err.Error() != want || !config.IsConfigError(err) {
		t.Fatal(err)
	}
}

const firstPort, lastPort = 8765, 8799

// listenInRange 在 8765–8799 里找一个空闲端口并占住。
func listenInRange(t *testing.T) net.Listener {
	for port := firstPort; port <= lastPort; port++ {
		if listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			return listener
		}
	}
	t.Fatal("8765–8799 没有空闲端口")
	return nil
}

func freePort(t *testing.T) int {
	listener := listenInRange(t)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// start/stop 可重复执行，且不会停掉别的进程（PID 文件指向的不是 Vinx Docs 时）。
func TestManageLifecycleIsIdempotentAndDoesNotKillForeignPID(t *testing.T) {
	tool := makeTool(t)
	port := freePort(t)
	writeFile(t, config.ConfigPath(tool), fmt.Sprintf(`{"schemaVersion": 1, "server": {"host": "127.0.0.1", "port": %d}, "protectedPorts": [], "projects": []}`, port))
	var out bytes.Buffer
	server.Stdout = &out
	defer func() { server.Stdout = os.Stdout }()
	server.ExtraEnv = []string{childEnv + "=1"}
	defer func() { server.ExtraEnv = nil }()
	pidFile := filepath.Join(tool, ".runtime", "server.json")
	readPID := func() float64 {
		var saved map[string]any
		data, _ := os.ReadFile(pidFile)
		json.Unmarshal(data, &saved)
		pid, _ := saved["pid"].(float64)
		return pid
	}
	manage := func(action string) int {
		code, err := server.Manage(tool, action)
		if err != nil {
			t.Fatal(action, err)
		}
		return code
	}
	defer manage("stop")
	if manage("start") != 0 {
		t.Fatal(out.String())
	}
	first := readPID()
	if manage("start") != 0 || readPID() != first {
		t.Fatal("重复 start 不能再起一个进程")
	}
	if manage("status") != 0 || !strings.Contains(out.String(), "Vinx Docs 运行中") {
		t.Fatal(out.String())
	}
	writeFile(t, pidFile, fmt.Sprintf(`{"pid": %d}`, os.Getpid()))
	if manage("stop") == 0 || !strings.Contains(out.String(), "不是由 start 启动的") {
		t.Fatal("pid 文件指向别的进程时不能停服务", out.String())
	}
	writeFile(t, pidFile, fmt.Sprintf(`{"pid": %d}`, int(first)))
	if manage("stop") != 0 || manage("status") == 0 {
		t.Fatal(out.String())
	}
	writeFile(t, pidFile, fmt.Sprintf(`{"pid": %d}`, os.Getpid()))
	if manage("stop") != 0 {
		t.Fatal("没有服务在跑时 stop 只清理 pid 文件")
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("pid 文件应已删除")
	}
}
