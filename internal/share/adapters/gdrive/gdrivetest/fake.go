// Package gdrivetest 是离线测试用的假 Google 服务：设备码授权、令牌、Drive 文件接口的最小实现。
// 适配器通过环境变量 VINX_GDRIVE_ENDPOINT 指向它。Go 测试和浏览器测试（tests/browser/fakedrive）共用。
package gdrivetest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
)

// File 是假网盘里的一个文件或文件夹。
type File struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	MimeType      string            `json:"mimeType"`
	Parents       []string          `json:"parents"`
	AppProperties map[string]string `json:"appProperties"`
	Content       []byte            `json:"-"`
	Trashed       bool              `json:"trashed"`
	Public        bool              `json:"public"`
	Owner         string            `json:"owner"`
}

type device struct {
	code, userCode, email string
	approved, denied      bool
	noDrive               bool // 授权页上没勾选 Drive 权限
}

const fullScope = "https://www.googleapis.com/auth/drive.file https://www.googleapis.com/auth/userinfo.email openid"
const noDriveScope = "https://www.googleapis.com/auth/userinfo.email openid"

// Server 是假服务。
type Server struct {
	URL string

	mu       sync.Mutex
	srv      *httptest.Server
	seq      int
	devices  map[string]*device // device_code → 授权
	access   map[string]string  // access token → 邮箱
	refresh  map[string]string  // refresh token → 邮箱
	noDrive  map[string]bool    // 没有 Drive 权限的 access / refresh token
	files    map[string]*File
	sessions map[string]string // 上传会话 → 文件 id（新建时为空，见 pending）
	pending  map[string]*File  // 上传会话 → 待创建的文件
}

// NewHandler 创建假服务但不监听（浏览器测试的独立进程自己监听）。
func NewHandler() *Server {
	return &Server{devices: map[string]*device{}, access: map[string]string{}, refresh: map[string]string{}, noDrive: map[string]bool{},
		files: map[string]*File{}, sessions: map[string]string{}, pending: map[string]*File{}}
}

// New 启动假服务（Close 关闭）。
func New() *Server {
	s := NewHandler()
	s.srv = httptest.NewServer(s)
	s.URL = s.srv.URL
	return s
}

// Close 关闭假服务。
func (s *Server) Close() {
	if s.srv != nil {
		s.srv.Close()
	}
}

func (s *Server) next(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s%04d", prefix, s.seq)
}

// Approve 模拟用户在 Google 页面上输入验证码并允许。
func (s *Server) Approve(userCode, email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.approved, d.email = true, email
			return true
		}
	}
	return false
}

// ApproveWithoutDrive 模拟用户允许了，但没有勾选 Drive 那一项（Google 的分项授权）。
func (s *Server) ApproveWithoutDrive(userCode, email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.approved, d.email, d.noDrive = true, email, true
			return true
		}
	}
	return false
}

// DropDriveScope 让现有的所有令牌失去 Drive 权限（模拟早先没勾选就保存下来的令牌）。
func (s *Server) DropDriveScope() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.access {
		s.noDrive[token] = true
	}
	for token := range s.refresh {
		s.noDrive[token] = true
	}
}

// Deny 模拟用户拒绝。
func (s *Server) Deny(userCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.denied = true
		}
	}
}

// ExpireAccess 让所有访问令牌失效（下一次调用会 401，适配器应刷新）。
func (s *Server) ExpireAccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]string{}
}

// RevokeRefresh 让所有刷新令牌失效（刷新返回 invalid_grant）。
func (s *Server) RevokeRefresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = map[string]string{}
	s.refresh = map[string]string{}
}

// Files 返回全部文件的副本。
func (s *Server) Files() []File {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []File{}
	for _, f := range s.files {
		out = append(out, *f)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func driveError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": reason, "errors": []map[string]string{{"reason": reason}}}})
}

func idToken(email string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]string{"email": email})
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/oauth/device/code" && r.Method == "POST":
		_ = r.ParseForm()
		if r.Form.Get("client_id") == "" || !strings.Contains(r.Form.Get("scope"), "drive.file") {
			writeJSON(w, 400, map[string]string{"error": "invalid_client"})
			return
		}
		d := &device{code: s.next("dc"), userCode: fmt.Sprintf("FAKE-%04d", s.seq)}
		s.devices[d.code] = d
		writeJSON(w, 200, map[string]any{"device_code": d.code, "user_code": d.userCode, "verification_url": s.base(r) + "/device",
			"expires_in": 600, "interval": 1})
	case path == "/oauth/token" && r.Method == "POST":
		_ = r.ParseForm()
		if r.Form.Get("client_secret") == "" {
			writeJSON(w, 401, map[string]string{"error": "invalid_client"})
			return
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			d := s.devices[r.Form.Get("device_code")]
			switch {
			case d == nil:
				writeJSON(w, 400, map[string]string{"error": "expired_token"})
			case d.denied:
				writeJSON(w, 403, map[string]string{"error": "access_denied"})
			case !d.approved:
				writeJSON(w, 428, map[string]string{"error": "authorization_pending"})
			default:
				delete(s.devices, d.code)
				at, rt := s.next("at"), s.next("rt")
				s.access[at], s.refresh[rt] = d.email, d.email
				scope := fullScope
				if d.noDrive {
					s.noDrive[at], s.noDrive[rt], scope = true, true, noDriveScope
				}
				writeJSON(w, 200, map[string]any{"access_token": at, "refresh_token": rt, "expires_in": 3600, "id_token": idToken(d.email), "scope": scope})
			}
		case "refresh_token":
			email, ok := s.refresh[r.Form.Get("refresh_token")]
			if !ok {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			at := s.next("at")
			s.access[at] = email
			scope := fullScope
			if s.noDrive[r.Form.Get("refresh_token")] {
				s.noDrive[at], scope = true, noDriveScope
			}
			writeJSON(w, 200, map[string]any{"access_token": at, "expires_in": 3600, "scope": scope})
		default:
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		}
	case path == "/device":
		s.devicePage(w, r)
	default:
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		email, ok := s.access[bearer]
		if !ok && !strings.HasPrefix(path, "/upload/session/") {
			driveError(w, 401, "authError")
			return
		}
		if ok && s.noDrive[bearer] {
			driveError(w, 403, "insufficientPermissions")
			return
		}
		s.drive(w, r, email)
	}
}

func (s *Server) base(r *http.Request) string { return "http://" + r.Host }

// devicePage 是假的 Google 授权页：输入验证码和邮箱，点允许或拒绝。
func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("user_code"))
		message := "验证码不对"
		for _, d := range s.devices {
			if d.userCode == code {
				if r.Form.Get("action") == "deny" {
					d.denied, message = true, "已拒绝"
				} else {
					d.approved, d.email, message = true, r.Form.Get("email"), "已允许，可以回到 Vinx Docs"
					d.noDrive = r.Form.Get("drive") == ""
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>假 Google 授权</title><p id=result>%s</p>", html.EscapeString(message))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>假 Google 授权</title>
<form method=post><label>验证码 <input name=user_code></label><label>邮箱 <input name=email value="tester@example.com"></label>
<label><input type=checkbox name=drive value=1 checked> 查看、修改、创建和删除您通过此应用使用的特定 Google 云端硬盘文件</label>
<button name=action value=allow>允许</button><button name=action value=deny>拒绝</button></form>`)
}

var (
	reFolder = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)' and mimeType = '([^']*)' and '((?:[^'\\]|\\.)*)' in parents and trashed = false$`)
	reKey    = regexp.MustCompile(`^appProperties has \{ key='vinxKey' and value='((?:[^'\\]|\\.)*)' \} and trashed = false$`)
	unquote  = strings.NewReplacer(`\'`, `'`, `\\`, `\`)
)

func (s *Server) view(f *File) map[string]any {
	return map[string]any{"id": f.ID, "trashed": f.Trashed, "webViewLink": "https://drive.google.com/file/d/" + f.ID + "/view?usp=drivesdk"}
}

func (s *Server) drive(w http.ResponseWriter, r *http.Request, email string) {
	path := r.URL.Path
	readJSON := func() map[string]any {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body
	}
	switch {
	case path == "/drive/v3/about" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"user": map[string]string{"emailAddress": email}})
	case path == "/drive/v3/files" && r.Method == "GET":
		q := r.URL.Query().Get("q")
		var match func(f *File) bool
		if m := reFolder.FindStringSubmatch(q); m != nil {
			name, mime, parent := unquote.Replace(m[1]), m[2], unquote.Replace(m[3])
			match = func(f *File) bool {
				return f.Name == name && f.MimeType == mime && !f.Trashed && len(f.Parents) > 0 && f.Parents[0] == parent
			}
		} else if m := reKey.FindStringSubmatch(q); m != nil {
			key := unquote.Replace(m[1])
			match = func(f *File) bool { return f.AppProperties["vinxKey"] == key && !f.Trashed }
		} else {
			driveError(w, 400, "invalidQuery")
			return
		}
		files := []map[string]string{}
		for _, f := range s.files {
			if f.Owner == email && match(f) {
				files = append(files, map[string]string{"id": f.ID})
			}
		}
		writeJSON(w, 200, map[string]any{"files": files})
	case path == "/drive/v3/files" && r.Method == "POST":
		body := readJSON()
		f := &File{ID: s.next("folder"), Owner: email, Name: fmt.Sprint(body["name"]), MimeType: fmt.Sprint(body["mimeType"])}
		if parents, ok := body["parents"].([]any); ok && len(parents) > 0 {
			f.Parents = []string{fmt.Sprint(parents[0])}
		}
		s.files[f.ID] = f
		writeJSON(w, 200, map[string]string{"id": f.ID})
	case strings.HasPrefix(path, "/drive/v3/files/"):
		rest := strings.TrimPrefix(path, "/drive/v3/files/")
		id, sub, _ := strings.Cut(rest, "/")
		f := s.files[id]
		if f == nil || f.Owner != email {
			driveError(w, 404, "notFound")
			return
		}
		switch {
		case sub == "permissions" && r.Method == "POST":
			body := readJSON()
			if body["type"] == "anyone" && body["role"] == "reader" {
				f.Public = true
			}
			writeJSON(w, 200, map[string]string{"id": "anyoneWithLink"})
		case sub == "" && r.Method == "GET":
			writeJSON(w, 200, s.view(f))
		case sub == "" && r.Method == "PATCH":
			if body := readJSON(); body["trashed"] == true {
				f.Trashed = true
			}
			writeJSON(w, 200, s.view(f))
		default:
			driveError(w, 400, "badRequest")
		}
	case path == "/upload/drive/v3/files" && r.Method == "POST":
		body := readJSON()
		f := &File{Owner: email, Name: fmt.Sprint(body["name"]), MimeType: r.Header.Get("X-Upload-Content-Type"), AppProperties: map[string]string{}}
		if parents, ok := body["parents"].([]any); ok && len(parents) > 0 {
			f.Parents = []string{fmt.Sprint(parents[0])}
		}
		if props, ok := body["appProperties"].(map[string]any); ok {
			for k, v := range props {
				f.AppProperties[k] = fmt.Sprint(v)
			}
		}
		session := s.next("up")
		s.pending[session] = f
		w.Header().Set("Location", s.base(r)+"/upload/session/"+session)
		w.WriteHeader(200)
	case strings.HasPrefix(path, "/upload/drive/v3/files/") && r.Method == "PATCH":
		id := strings.TrimPrefix(path, "/upload/drive/v3/files/")
		if f := s.files[id]; f == nil || f.Owner != email {
			driveError(w, 404, "notFound")
			return
		}
		session := s.next("up")
		s.sessions[session] = id
		w.Header().Set("Location", s.base(r)+"/upload/session/"+session)
		w.WriteHeader(200)
	case strings.HasPrefix(path, "/upload/session/") && r.Method == "PUT":
		session := strings.TrimPrefix(path, "/upload/session/")
		content, _ := io.ReadAll(r.Body)
		if f := s.pending[session]; f != nil {
			delete(s.pending, session)
			f.ID, f.Content = s.next("file"), content
			s.files[f.ID] = f
			writeJSON(w, 200, map[string]string{"id": f.ID})
			return
		}
		if id, ok := s.sessions[session]; ok {
			delete(s.sessions, session)
			s.files[id].Content = content
			writeJSON(w, 200, map[string]string{"id": id})
			return
		}
		driveError(w, 404, "notFound")
	default:
		driveError(w, 404, "notFound")
	}
}
