//go:build !windows

package server_test

// 发布到外部服务的接口：后台管理（服务商、账号授权、目标）只限内网且不接受命令字段；
// 目标列表不含密钥；只有内网能发布、撤回；沙箱页面和跨域请求被拒绝。用的是假 Google 服务。

import (
	"strings"
	"testing"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/share/adapters/gdrive/gdrivetest"
)

type shareFixture struct {
	*accessFixture
	fake   *gdrivetest.Server
	target string
}

func setupShare(t *testing.T) *shareFixture {
	t.Helper()
	fake := gdrivetest.New()
	t.Cleanup(fake.Close)
	t.Setenv("VINX_GDRIVE_ENDPOINT", fake.URL)
	a := setupAccess(t)
	f := &shareFixture{accessFixture: a, fake: fake}
	if r := a.post("/api/share-providers/set", map[string]any{"adapter": "google-drive",
		"config": map[string]string{"clientId": "cid"}, "secrets": map[string]string{"clientSecret": "hidden-secret"}}, nil); r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	r := a.post("/api/share-accounts/connect", map[string]any{"adapter": "google-drive"}, nil)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	start := r.json(t)
	account := start["account"].(string)
	fake.Approve(start["userCode"].(string), "alice@example.com")
	if r := a.post("/api/share-accounts/poll", map[string]any{"account": account}, nil); r.status != 200 || r.json(t)["status"] != "done" {
		t.Fatal(r.status, string(r.body))
	}
	r = a.post("/api/share-targets/save", map[string]any{"label": "Drive · 工作", "adapter": "google-drive", "account": account,
		"config": map[string]string{"dir": "Vinx"}, "sources": []string{"pages"}}, nil)
	if r.status != 200 {
		t.Fatal(r.status, string(r.body))
	}
	f.target = r.json(t)["id"].(string)
	return f
}

func waitJob(t *testing.T, a *accessFixture, id string) map[string]any {
	t.Helper()
	for i := 0; i < 200; i++ {
		job := a.get("/api/shares/job?id="+id, nil).json(t)
		if job["status"] != "running" {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务没有结束")
	return nil
}

func TestShareAdminHidesSecretsAndRejectsCommands(t *testing.T) {
	f := setupShare(t)
	r := f.get("/api/share-admin", nil)
	if r.status != 200 || strings.Contains(string(r.body), "hidden-secret") || strings.Contains(string(r.body), "at0") {
		t.Fatalf("后台数据不应含密钥或令牌：%d %s", r.status, r.body)
	}
	if !strings.Contains(string(r.body), "alice@example.com") {
		t.Fatalf("后台应显示账号：%s", r.body)
	}
	// 页面接口不接受命令相关字段。
	for _, field := range []string{"command", "manifest", "passEnv"} {
		body := map[string]any{"label": "x", "adapter": "google-drive", field: []string{"/bin/sh"}}
		if r := f.post("/api/share-targets/save", body, nil); r.status != 400 {
			t.Errorf("字段 %s 应被拒绝：%d %s", field, r.status, r.body)
		}
	}
	// 目标列表不含配置和密钥。
	r = f.get("/api/share-targets?page="+f.fresh, nil)
	body := string(r.body)
	for _, bad := range []string{"hidden-secret", "secrets", "\"config\""} {
		if strings.Contains(body, bad) {
			t.Errorf("目标列表不应包含 %q：%s", bad, body)
		}
	}
	if !strings.Contains(body, "alice@example.com") {
		t.Errorf("目标列表应显示账号：%s", body)
	}
}

func TestShareAccessControl(t *testing.T) {
	f := setupShare(t)
	f.setPassword(t, "pw")
	cookie := sessionCookie(t, f.login(t, "203.0.113.5", "pw"))
	authed := externalPost("203.0.113.5", map[string]string{"Cookie": cookie})
	writes := map[string]map[string]any{
		"/api/shares":                 {"page": f.fresh, "target": f.target},
		"/api/share-providers/set":    {"adapter": "google-drive"},
		"/api/share-accounts/connect": {"adapter": "google-drive"},
		"/api/share-targets/save":     {"label": "x", "adapter": "google-drive"},
		"/api/share-targets/remove":   {"id": f.target},
	}
	for path, body := range writes {
		if r := f.post(path, body, authed); r.status != 403 {
			t.Errorf("外部已登录访问 %s 应返回 403：%d", path, r.status)
		}
		if r := f.post(path, body, externalPost("203.0.113.6", nil)); r.status != 401 {
			t.Errorf("匿名外部访问 %s 应返回 401：%d", path, r.status)
		}
		if r := f.post(path, body, map[string]string{"Origin": "null"}); r.status != 403 {
			t.Errorf("沙箱页面（Origin: null）访问 %s 应返回 403：%d", path, r.status)
		}
	}
	if r := f.get("/api/share-admin", external("203.0.113.5", map[string]string{"Cookie": cookie})); r.status != 403 {
		t.Errorf("外部访问后台数据应返回 403：%d", r.status)
	}
	if r := f.get("/api/share-targets?page="+f.fresh, external("203.0.113.5", map[string]string{"Cookie": cookie})); r.status != 200 ||
		len(r.json(t)["targets"].([]any)) != 0 {
		t.Errorf("外部访问不应列出目标：%d %s", r.status, r.body)
	}
}

func TestSharePublishFlow(t *testing.T) {
	f := setupShare(t)
	r := f.post("/api/shares", map[string]any{"page": f.fresh, "target": f.target}, nil)
	if r.status != 202 {
		t.Fatal(r.status, string(r.body))
	}
	job := waitJob(t, f.accessFixture, r.json(t)["job"].(map[string]any)["id"].(string))
	record := job["record"].(map[string]any)
	if job["status"] != "done" || record["status"] != "active" || !strings.Contains(record["url"].(string), "drive.google.com/file/d/") {
		t.Fatalf("发布结果不对：%v", job)
	}
	if files := f.fake.Files(); len(files) != 2 { // 文件夹 Vinx + 文件
		t.Fatalf("假网盘里应有 2 项：%+v", files)
	}
	listing := f.get("/api/artifacts", nil).json(t)["artifacts"].([]any)
	for _, item := range listing {
		if obj := item.(map[string]any); obj["id"] == f.fresh && obj["externalShares"] != float64(1) {
			t.Errorf("页面列表应显示 1 处外部发布：%v", obj)
		}
	}
	// 还有有效记录时删除目标要确认。
	if r := f.post("/api/share-targets/remove", map[string]any{"id": f.target}, nil); r.status != 409 {
		t.Errorf("有有效记录时删除目标应返回 409：%d", r.status)
	}
	shareID := record["id"].(string)
	r = f.post("/api/shares/revoke", map[string]any{"share": shareID}, nil)
	if r.status != 202 {
		t.Fatal(r.status, string(r.body))
	}
	if job = waitJob(t, f.accessFixture, r.json(t)["job"].(map[string]any)["id"].(string)); job["status"] != "done" {
		t.Fatalf("撤回失败：%v", job)
	}
	if r := f.post("/api/shares/forget", map[string]any{"share": shareID}, nil); r.status != 200 {
		t.Fatalf("forget：%d %s", r.status, r.body)
	}
	if r := f.post("/api/share-targets/remove", map[string]any{"id": f.target}, nil); r.status != 200 {
		t.Fatalf("撤回后删除目标：%d %s", r.status, r.body)
	}
}
