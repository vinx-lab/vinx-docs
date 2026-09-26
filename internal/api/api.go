// Package api 是编辑与批注的接口逻辑，与 HTTP 框架无关。
//
// HTTP 层负责：Origin/Host 检查、Content-Type 与请求体大小、按 PostRoutes 校验字段（ValidateBody），
// 然后调用 HandleGet / HandlePost，再用 ErrorResponse 把错误映射成状态码和 {"error": ...}。
package api

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/comments"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/files"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// GetRoutes 是本包处理的 GET 接口。
var GetRoutes = map[string]bool{
	"/api/file": true, "/api/file/history": true, "/api/file/history/item": true, "/api/comments": true, "/api/agents": true,
}

// RouteSpec 是 POST 接口的请求体约束：必需字段、可选字段、请求体上限（字节）。
type RouteSpec struct {
	Required []string
	Optional []string
	Limit    int64
}

// PostRoutes 是本包处理的 POST 接口，以及各自的必填字段和请求体上限。
var PostRoutes = map[string]RouteSpec{
	"/api/file/save":      {Required: []string{"target", "content", "baseVersion"}, Limit: 3 * 1024 * 1024},
	"/api/comments":       {Required: []string{"target", "anchor", "body"}, Limit: 65536},
	"/api/comment/update": {Required: []string{"id", "action"}, Limit: 4096},
	"/api/comment/reply":  {Required: []string{"id", "body"}, Limit: 65536},
}

// BadRequest 是请求体格式错误（返回 400）。
type BadRequest struct{ Msg string }

func (e *BadRequest) Error() string { return e.Msg }

// ValidateBody 检查请求体字段：body 必须是 JSON 对象（*ojson.Object），
// 包含全部必需字段、不含多余字段。raw 是已按 Limit 读取的请求体原文。
func ValidateBody(spec RouteSpec, raw []byte) (*ojson.Object, error) {
	fail := func() error {
		allowed := append(append([]string(nil), spec.Required...), spec.Optional...)
		sort.Strings(allowed)
		text := strings.Join(allowed, "、")
		if text == "" {
			text = "无"
		}
		return &BadRequest{Msg: "请求格式无效；本接口接受的字段：" + text}
	}
	if int64(len(raw)) > spec.Limit {
		return nil, fail()
	}
	body := ojson.NewObject()
	if len(raw) > 0 {
		value, err := ojson.Decode(raw)
		if err != nil {
			return nil, fail()
		}
		obj, ok := value.(*ojson.Object)
		if !ok {
			return nil, fail()
		}
		body = obj
	}
	allowed := map[string]bool{}
	for _, key := range spec.Required {
		if !body.Has(key) {
			return nil, fail()
		}
		allowed[key] = true
	}
	for _, key := range spec.Optional {
		allowed[key] = true
	}
	for _, key := range body.Keys() {
		if !allowed[key] {
			return nil, fail()
		}
	}
	return body, nil
}

// API 持有编辑与批注接口需要的路径、批注库和刷新回调。
type API struct {
	Paths files.Paths
	// DB 为空时：设置了 VINX_DOCS_DB 就用它，否则用 <家目录>/vinx.db。
	DB string
	// Refresh 保存 doc: 目标后调用，做一次增量刷新（通常是 build.Refresh）；为 nil 表示不刷新。
	Refresh func() error
	// OperationLock 与同步、自动同步共用的操作锁；保存后刷新前最多等 20 秒。
	OperationLock TryLocker
}

// New 创建接口对象：家目录、批注库路径、保存后的刷新函数和操作锁。
// TryLocker 是操作锁需要的最小接口；*sync.Mutex 和 HTTP 服务的操作锁（能报告是否被占用）都满足。
type TryLocker interface {
	TryLock() bool
	Unlock()
}

func New(toolRoot, db string, refresh func() error, lock TryLocker) *API {
	if db == "" && os.Getenv("VINX_DOCS_DB") == "" {
		db = textutil.Join(toolRoot, "vinx.db")
	}
	a := &API{Paths: files.NewPaths(toolRoot), DB: db, Refresh: refresh}
	if lock != nil && !isNilMutex(lock) {
		a.OperationLock = lock
	}
	return a
}

// isNilMutex 让 New(..., (*sync.Mutex)(nil)) 与不传锁等价，避免接口里装着 nil 指针。
func isNilMutex(lock TryLocker) bool {
	m, ok := lock.(*sync.Mutex)
	return ok && m == nil
}

// Author 人在页面上的署名，来自设置里的 displayName。
func (a *API) Author() string {
	cfg, err := config.Load(a.Paths.Config)
	if err != nil {
		return "我"
	}
	if settings, ok := cfg.Value("settings").(*ojson.Object); ok {
		if name := settings.Value("displayName"); textutil.Truthy(name) {
			if s, ok := name.(string); ok {
				return s
			}
		}
	}
	return "我"
}

func (a *API) conn() (*comments.Conn, error) { return comments.Connect(a.DB) }

// HandleGet 处理 GET 接口。query 是 textutil.ParseQS 的结果。
// 返回 (状态码, 响应对象, 错误)；错误交给 ErrorResponse 映射。
func (a *API) HandleGet(path string, query map[string][]string) (int, any, error) {
	one := func(key string) string {
		if values := query[key]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	switch path {
	case "/api/file":
		item, err := files.Read(a.Paths, one("target"))
		return 200, item, err
	case "/api/file/history":
		items, err := files.History(a.Paths, one("target"))
		if err != nil {
			return 0, nil, err
		}
		return 200, ojson.NewObject("history", items), nil
	case "/api/file/history/item":
		text, err := files.HistoryItem(a.Paths, one("target"), one("id"))
		if err != nil {
			return 0, nil, err
		}
		return 200, ojson.NewObject("content", text), nil
	}
	conn, err := a.conn()
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	matches := comments.ScopeMatcher(a.Paths)
	if path == "/api/agents" {
		online, err := conn.Online()
		if err != nil {
			return 0, nil, err
		}
		agents := []any{}
		for _, item := range online {
			agents = append(agents, ojson.NewObject("agent", item.Value("agent"), "name", item.Value("name"),
				"scopes", item.Value("scopes"), "heartbeat_at", item.Value("heartbeat_at")))
		}
		counts, err := conn.Counts()
		if err != nil {
			return 0, nil, err
		}
		return 200, ojson.NewObject("agents", agents, "counts", counts), nil
	}
	target := one("target")
	var statuses []string
	for _, item := range strings.Split(one("status"), ",") {
		if item != "" {
			statuses = append(statuses, item)
		}
	}
	items, err := conn.Listing(target, statuses, one("prefix"))
	if err != nil {
		return 0, nil, err
	}
	list := []any{}
	for _, item := range items {
		delivery, err := conn.DeliveryState(item, matches)
		if err != nil {
			return 0, nil, err
		}
		item.Set("delivery", delivery)
		if target == "" {
			if _, where, err := files.Resolve(a.Paths, item.Value("target")); err == nil {
				where.Delete("source")
				item.Set("where", where)
			} else {
				item.Set("where", nil)
			}
		}
		list = append(list, item)
	}
	counts, err := conn.Counts()
	if err != nil {
		return 0, nil, err
	}
	return 200, ojson.NewObject("comments", list, "counts", counts, "me", a.Author()), nil
}

// HandlePost 处理 POST 接口。body 已经过 ValidateBody。
// 保存冲突直接返回 409 和 {"error","content","version"}，不作为错误返回。
func (a *API) HandlePost(path string, body *ojson.Object) (int, any, error) {
	if path == "/api/file/save" {
		result, err := files.Save(a.Paths, body.Value("target"), body.Value("content"), body.Value("baseVersion"))
		var conflict *files.Conflict
		if errors.As(err, &conflict) {
			return 409, ojson.NewObject("error", "文件在你编辑期间已被修改", "content", conflict.Content, "version", conflict.Version), nil
		}
		if err != nil {
			return 0, nil, err
		}
		if result.Value("changed") == true && result.Value("kind") == "doc" {
			result.Set("refreshed", a.refreshAfterSave())
		}
		result.Delete("source")
		return 200, result, nil
	}
	conn, err := a.conn()
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	switch path {
	case "/api/comments":
		item, err := conn.Create(body.Value("target"), body.Value("anchor"), body.Value("body"))
		return 200, item, err
	case "/api/comment/reply":
		item, err := conn.AddReply(body.Value("id"), a.Author(), body.Value("body"))
		return 200, item, err
	}
	id, action := body.Value("id"), body.Value("action")
	item, err := conn.SetStatus(id, action)
	if err != nil {
		return 0, nil, err
	}
	if action == "send" {
		// Codex 会话不能自己挂着监听，由这里推送；Claude Code 的会话靠 watch 自己来取。
		if _, err := conn.PushCodex(item, comments.ScopeMatcher(a.Paths), nil); err != nil {
			return 0, nil, err
		}
		if item, err = conn.Get(id); err != nil {
			return 0, nil, err
		}
		delivery, err := conn.DeliveryState(item, comments.ScopeMatcher(a.Paths))
		if err != nil {
			return 0, nil, err
		}
		item.Set("delivery", delivery)
	}
	return 200, item, nil
}

// refreshAfterSave 保存后立即增量刷新阅读副本；自动同步正在跑就等它，最多等 20 秒。
func (a *API) refreshAfterSave() bool {
	if a.Refresh == nil || a.OperationLock == nil {
		return false
	}
	deadline := time.Now().Add(20 * time.Second)
	for !a.OperationLock.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer a.OperationLock.Unlock()
	return a.Refresh() == nil
}

// ErrorResponse 把接口错误映射成状态码和响应体：
// ConfigError / BadRequest → 400 {"error": 原文}；UnicodeError → 400 {"error": "内容不是有效的UTF-8文本"}；
// 其他（文件系统、数据库）→ 500 {"error": "文件读取或写入失败，请检查权限和磁盘空间"}。
func ErrorResponse(err error) (int, *ojson.Object) {
	var configErr *config.Error
	var bad *BadRequest
	var unicodeErr *files.UnicodeError
	switch {
	case errors.As(err, &configErr):
		return 400, ojson.NewObject("error", configErr.Msg)
	case errors.As(err, &bad):
		return 400, ojson.NewObject("error", bad.Msg)
	case errors.As(err, &unicodeErr):
		return 400, ojson.NewObject("error", "内容不是有效的UTF-8文本")
	}
	return 500, ojson.NewObject("error", "文件读取或写入失败，请检查权限和磁盘空间")
}

// ServeGet / ServePost 把处理和错误映射合在一起，HTTP 层直接写出 (状态码, JSON)。
func (a *API) ServeGet(path string, query map[string][]string) (int, any) {
	status, data, err := a.HandleGet(path, query)
	if err != nil {
		return ErrorResponse(err)
	}
	return status, data
}

func (a *API) ServePost(path string, body *ojson.Object) (int, any) {
	status, data, err := a.HandlePost(path, body)
	if err != nil {
		return ErrorResponse(err)
	}
	return status, data
}
