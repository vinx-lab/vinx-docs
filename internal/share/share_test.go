//go:build !windows

package share

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/pages"
)

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adapter.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runScript(t *testing.T, body string, timeout time.Duration) (Outcome, string) {
	t.Helper()
	work := t.TempDir()
	target := &Target{ID: "t1", Config: map[string]string{"remote": "x:"}, Secrets: map[string]string{"token": "s3cret-value"}}
	req := Request{Protocol: 1, Method: "publish", Key: "k1", Secrets: target.Secrets, StateDir: work,
		Target: requestTarget{ID: "t1", Config: target.Config}, Params: &Params{File: "/tmp/f", Name: "f.md"}}
	var log bytes.Buffer
	o := Run(context.Background(), Call{Argv: []string{script(t, body)}, Dir: work, Env: BuildEnv(target, req, work),
		Request: req, Timeout: timeout, Log: &log})
	return o, log.String()
}

func TestRunJudgesOutcome(t *testing.T) {
	cases := []struct {
		name, body, status, code string
	}{
		{"结果加退出码 0", `echo noise; echo '{"event":"log","message":"hi"}'; echo '{"event":"result","data":{"url":"u","remoteId":"r","public":false}}'`, OK, ""},
		{"error 事件", `echo '{"event":"error","code":"auth_required","message":"m"}'; exit 3`, Failed, "auth_required"},
		{"不认识的错误码按 internal", `echo '{"event":"error","code":"weird","message":"m"}'`, Failed, "internal"},
		{"没有终止事件", `echo '{"event":"log","message":"x"}'; exit 1`, Unknown, "crashed"},
		{"有 result 但退出码非 0", `echo '{"event":"result","data":{}}'; exit 1`, Unknown, "crashed"},
		{"stdout 是空的", `exit 0`, Unknown, "crashed"},
	}
	for _, c := range cases {
		o, _ := runScript(t, c.body, 10*time.Second)
		if o.Status != c.status || (c.code != "" && o.Code != c.code) {
			t.Errorf("%s：得到 %+v", c.name, o)
		}
	}
}

func TestRunLogsNoiseAndStderr(t *testing.T) {
	o, log := runScript(t, `echo noise; echo oops >&2; echo '{"event":"result","data":{"url":"u","remoteId":"r","public":false}}'`, 10*time.Second)
	if o.Status != OK || !strings.Contains(log, "stdout: noise") || !strings.Contains(log, "stderr: oops") {
		t.Fatalf("outcome=%+v log=%s", o, log)
	}
}

func TestRunIgnoresOverlongLine(t *testing.T) {
	o, log := runScript(t, `head -c 1100000 /dev/zero | tr '\0' a; echo; echo '{"event":"result","data":{}}'`, 10*time.Second)
	if o.Status != OK || !strings.Contains(log, "超过 1 MiB") {
		t.Fatalf("outcome=%+v log=%.200s", o, log)
	}
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-alive")
	start := time.Now()
	o, _ := runScript(t, `(sleep 3; touch `+marker+`) & sleep 30`, 300*time.Millisecond)
	if o.Status != Unknown || o.Code != "timeout" {
		t.Fatalf("得到 %+v", o)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatalf("超时后没有及时结束：%s", time.Since(start))
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("子进程没有随进程组一起结束")
	}
}

func TestRequestAndEnv(t *testing.T) {
	t.Setenv("VINX_TEST_LEAK", "should-not-pass")
	dump := filepath.Join(t.TempDir(), "dump")
	o, _ := runScript(t, `env > `+dump+`.env; cat > `+dump+`.json; echo '{"event":"result","data":{}}'`, 10*time.Second)
	if o.Status != OK {
		t.Fatalf("%+v", o)
	}
	env, _ := os.ReadFile(dump + ".env")
	for _, want := range []string{"VINX_SHARE_PROTOCOL=1", "VINX_SHARE_METHOD=publish", "VINX_SHARE_KEY=k1", "VINX_SHARE_FILE=/tmp/f",
		"VINX_SHARE_NAME=f.md", "VINX_SHARE_PUBLIC=0", "VINX_SHARE_CONFIG_REMOTE=x:"} {
		if !strings.Contains(string(env), want+"\n") {
			t.Errorf("环境变量里缺少 %s", want)
		}
	}
	for _, bad := range []string{"should-not-pass", "s3cret-value"} {
		if strings.Contains(string(env), bad) {
			t.Errorf("环境变量里不应出现 %s", bad)
		}
	}
	raw, _ := os.ReadFile(dump + ".json")
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("stdin 不是一行 JSON：%s", raw)
	}
	if req["secrets"].(map[string]any)["token"] != "s3cret-value" || req["key"] != "k1" {
		t.Fatalf("stdin 请求不对：%s", raw)
	}
}

func TestRedact(t *testing.T) {
	got := Redact("token abc123456 and abc", map[string]string{"t": "abc123456", "u": ""})
	if got != "token *** and abc" {
		t.Fatal(got)
	}
}

// ---------------------------------------------------------------- 发布流程（示例外部适配器 testdata/local-folder）

type fixture struct {
	home, registry, page, src, remote string
	svc                               *Service
}

func newFixture(t *testing.T, files map[string]string, entry string) *fixture {
	t.Helper()
	home := t.TempDir()
	src := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(src, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := pages.DefaultRegistry(home)
	record, _, err := pages.Publish(registry, filepath.Join(src, entry), src, nil, "订单 方案")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{home: home, registry: registry, page: pages.Str(record, "id"), src: src, remote: t.TempDir()}
	f.addTarget(t, "lf", false, []string{"pages"})
	f.svc = NewService(home, registry, os.Stderr)
	return f
}

func (f *fixture) addTarget(t *testing.T, id string, public bool, sources []string) {
	t.Helper()
	dir, _ := filepath.Abs("testdata/local-folder")
	err := UpdateStore(f.home, func(st *Store) error {
		st.Targets = append(st.Targets, &Target{ID: id, Label: "本机 " + id, Command: []string{filepath.Join(dir, "adapter.sh")},
			Manifest: filepath.Join(dir, "adapter.json"), Config: map[string]string{"root": filepath.Join(f.remote, id)},
			Public: public, Sources: sources, PassEnv: []string{"FAIL_ON", "HANG_ON"}, Timeout: "2s"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) publish(t *testing.T, target string) *Job {
	t.Helper()
	job, err := f.svc.Publish(f.page, target)
	if err != nil {
		t.Fatal(err)
	}
	return f.svc.Wait(job.ID)
}

func TestExampleAdapterConformance(t *testing.T) {
	var out strings.Builder
	if err := Conformance("testdata/local-folder", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestPublishUpdateRevoke(t *testing.T) {
	f := newFixture(t, map[string]string{"plan.md": "# 方案\n"}, "plan.md")
	job := f.publish(t, "lf")
	if job.Status != "done" || job.Record.Status != StatusActive || job.Record.Name != "订单 方案.md" || job.Record.Public {
		t.Fatalf("首次发布：%+v %+v", job, job.Record)
	}
	first := *job.Record
	if name, _ := os.ReadFile(filepath.Join(f.remote, "lf", first.RemoteID+".name")); string(name) != "订单 方案.md" {
		t.Fatalf("远端文件名：%q", name)
	}
	views, _ := f.svc.Shares(f.page)
	if len(views) != 1 || views[0].Modified {
		t.Fatalf("刚发布不应显示已修改：%+v", views)
	}
	_ = os.WriteFile(filepath.Join(f.src, "plan.md"), []byte("# 方案 v2\n"), 0o644)
	views, _ = f.svc.Shares(f.page)
	if !views[0].Modified {
		t.Fatal("改动源文件后应显示已修改")
	}
	job = f.publish(t, "lf")
	if job.Status != "done" || job.Record.RemoteID != first.RemoteID || job.Record.URL != first.URL || job.Record.ID != first.ID {
		t.Fatalf("更新后链接应不变：%+v", job.Record)
	}
	if content, _ := os.ReadFile(filepath.Join(f.remote, "lf", first.RemoteID)); string(content) != "# 方案 v2\n" {
		t.Fatalf("远端内容没更新：%q", content)
	}
	views, _ = f.svc.Shares(f.page)
	if views[0].Modified {
		t.Fatal("更新后不应再显示已修改")
	}
	rj, err := f.svc.Revoke(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job = f.svc.Wait(rj.ID); job.Status != "done" || job.Record.Status != StatusRevoked {
		t.Fatalf("撤回：%+v", job)
	}
	if err := f.svc.Forget(first.ID); err != nil {
		t.Fatal(err)
	}
	if records, _ := LoadRecords(f.home); len(records) != 0 {
		t.Fatalf("forget 后应没有记录：%+v", records)
	}
}

func TestPublishZipAndNames(t *testing.T) {
	f := newFixture(t, map[string]string{"index.html": `<img src="a.png">`, "a.png": "png"}, "index.html")
	job := f.publish(t, "lf")
	if job.Status != "done" || job.Record.Name != "订单 方案.zip" {
		t.Fatalf("%+v %+v", job, job.Record)
	}
	// 另一个同标题的页面发到同一个目标：文件名加短码。
	_ = os.WriteFile(filepath.Join(f.src, "other.html"), []byte(`<img src="a.png"><p>另一个</p>`), 0o644)
	record, _, err := pages.Publish(f.registry, filepath.Join(f.src, "other.html"), f.src, nil, "订单 方案")
	if err != nil {
		t.Fatal(err)
	}
	other := pages.Str(record, "id")
	j2, err := f.svc.Publish(other, "lf")
	if err != nil {
		t.Fatal(err)
	}
	if job = f.svc.Wait(j2.ID); job.Record.Name != "订单 方案 ("+other+").zip" {
		t.Fatalf("重名应加短码：%s", job.Record.Name)
	}
}

func TestPublishRefusals(t *testing.T) {
	f := newFixture(t, map[string]string{"plan.md": "# 方案\n![](a.png)\n", "a.png": "png"}, "plan.md")
	f.addTarget(t, "closed", false, nil)
	if _, err := f.svc.Publish(f.page, "closed"); err == nil || err.(*UserError).Status != 403 {
		t.Fatalf("没开启 pages 的目标应拒绝：%v", err)
	}
	if _, err := f.svc.Publish(f.page, "nope"); err == nil || err.(*UserError).Status != 404 {
		t.Fatalf("不存在的目标：%v", err)
	}
	_ = os.Remove(filepath.Join(f.src, "a.png"))
	_, err := f.svc.Publish(f.page, "lf")
	if err == nil || !strings.Contains(err.Error(), "a.png") {
		t.Fatalf("有文件没通过检查时应拒绝并列出：%v", err)
	}
	if views, _ := f.svc.Targets(f.page); len(views) != 1 || views[0].ID != "lf" {
		t.Fatalf("页面可用目标只应有 lf：%+v", views)
	}
}

func TestPublishFailureAndUnknown(t *testing.T) {
	f := newFixture(t, map[string]string{"plan.md": "# 方案\n"}, "plan.md")
	t.Setenv("FAIL_ON", "publish")
	job := f.publish(t, "lf")
	if job.Status != "failed" || job.Error.Code != "network" {
		t.Fatalf("%+v", job)
	}
	if records, _ := LoadRecords(f.home); len(records) != 0 {
		t.Fatal("首次发布明确失败应删除记录")
	}
	t.Setenv("FAIL_ON", "")
	first := f.publish(t, "lf").Record
	t.Setenv("FAIL_ON", "publish")
	job = f.publish(t, "lf")
	if job.Status != "failed" || job.Record.Status != StatusActive || job.Record.LastError == nil || job.Record.URL != first.URL {
		t.Fatalf("更新失败应恢复成 active：%+v %+v", job, job.Record)
	}
	t.Setenv("FAIL_ON", "")
	// 超时：结果未知；重试覆盖原位置。
	t.Setenv("HANG_ON", "publish")
	job = f.publish(t, "lf")
	if job.Status != "unknown" || job.Record.Status != StatusUnknown {
		t.Fatalf("超时应是结果未知：%+v %+v", job, job.Record)
	}
	t.Setenv("HANG_ON", "")
	// 服务在任务中途重启：记录停在 pending。
	records, _ := LoadRecords(f.home)
	records[0].Status = StatusPending
	_ = SaveRecords(f.home, records)
	NewService(f.home, f.registry, nil).Recover()
	records, _ = LoadRecords(f.home)
	if records[0].Status != StatusUnknown {
		t.Fatalf("重启后 pending 应变成 unknown：%+v", records[0])
	}
	job = f.publish(t, "lf")
	if job.Status != "done" || job.Record.RemoteID != first.RemoteID {
		t.Fatalf("unknown 重试应覆盖原位置：%+v", job.Record)
	}
}

func TestPublicMismatch(t *testing.T) {
	f := newFixture(t, map[string]string{"plan.md": "# 方案\n"}, "plan.md")
	// 适配器返回 public=true，但目标是非公开。
	dir := t.TempDir()
	manifest := filepath.Join(dir, "adapter.json")
	_ = os.WriteFile(manifest, []byte(`{"id":"liar","name":"Liar","version":"1","protocol":[1],"command":["x"],
		"capabilities":{"publicLink":true,"privateLink":true,"update":true,"revoke":true}}`), 0o644)
	cmd := script(t, `echo '{"event":"result","data":{"url":"https://x","remoteId":"r","public":true}}'`)
	_ = UpdateStore(f.home, func(st *Store) error {
		st.Targets = append(st.Targets, &Target{ID: "liar", Label: "Liar", Command: []string{cmd}, Manifest: manifest, Sources: []string{"pages"}})
		return nil
	})
	job := f.publish(t, "liar")
	if job.Status != "failed" || job.Error.Code != "public_mismatch" || !job.Record.PublicMismatch {
		t.Fatalf("%+v", job)
	}
}

func TestStoreFilePermissions(t *testing.T) {
	f := newFixture(t, map[string]string{"plan.md": "# 方案\n"}, "plan.md")
	if job := f.publish(t, "lf"); job.Status != "done" {
		t.Fatalf("%+v", job)
	}
	if info, _ := os.Stat(TargetsPath(f.home)); info.Mode().Perm() != 0o600 {
		t.Fatalf("share-targets.json 权限应为 0600：%v", info.Mode())
	}
	state, _ := os.Stat(StateDir(f.home, "local-folder", "lf"))
	if state == nil || state.Mode().Perm() != 0o700 {
		t.Fatalf("状态目录权限应为 0700：%v", state)
	}
}
