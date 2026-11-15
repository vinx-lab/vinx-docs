//go:build !windows

package gdrive_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/share"
	_ "github.com/vinx-lab/vinx-docs/internal/share/adapters/gdrive"
	"github.com/vinx-lab/vinx-docs/internal/share/adapters/gdrive/gdrivetest"
)

func TestMain(m *testing.M) {
	share.RunTestAdapterIfRequested()
	share.UseTestBinaryForBuiltins()
	os.Exit(m.Run())
}

type env struct {
	fake      *gdrivetest.Server
	svc       *share.Service
	home, src string
	page      string
}

func setup(t *testing.T) *env {
	t.Helper()
	fake := gdrivetest.New()
	t.Cleanup(fake.Close)
	t.Setenv("VINX_GDRIVE_ENDPOINT", fake.URL)
	home, src := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "plan.md"), []byte("# 方案\n![](a.png)\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "a.png"), []byte("png"), 0o644)
	registry := pages.DefaultRegistry(home)
	record, _, err := pages.Publish(registry, filepath.Join(src, "plan.md"), src, nil, "订单方案")
	if err != nil {
		t.Fatal(err)
	}
	svc := share.NewService(home, registry, os.Stderr)
	if err := svc.SetProvider("google-drive", map[string]string{"clientId": "cid"}, map[string]string{"clientSecret": "csecret"}); err != nil {
		t.Fatal(err)
	}
	return &env{fake, svc, home, src, pages.Str(record, "id")}
}

// connect 走一遍设备码授权，返回账号 id。
func (e *env) connect(t *testing.T, email string) string {
	t.Helper()
	start, err := e.svc.ConnectAccount("google-drive", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(start.VerificationURL, "/device") || start.UserCode == "" {
		t.Fatalf("授权开始：%+v", start)
	}
	if poll, err := e.svc.PollAccount(start.Account); err != nil || poll.Status != "pending" {
		t.Fatalf("没允许前应是 pending：%+v %v", poll, err)
	}
	if !e.fake.Approve(start.UserCode, email) {
		t.Fatal("假服务找不到验证码")
	}
	poll, err := e.svc.PollAccount(start.Account)
	if err != nil || poll.Status != "done" || poll.Account.Label != email || poll.Account.Status != share.AccountConnected {
		t.Fatalf("允许后应完成：%+v %v", poll, err)
	}
	return start.Account
}

func (e *env) publish(t *testing.T, target string) *share.Job {
	t.Helper()
	job, err := e.svc.Publish(e.page, target)
	if err != nil {
		t.Fatal(err)
	}
	return e.svc.Wait(job.ID)
}

func findFile(t *testing.T, fake *gdrivetest.Server, id string) gdrivetest.File {
	t.Helper()
	for _, f := range fake.Files() {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("假网盘里没有 %s", id)
	return gdrivetest.File{}
}

func TestDeviceAuthAndTwoAccounts(t *testing.T) {
	e := setup(t)
	alice := e.connect(t, "alice@example.com")
	bob := e.connect(t, "bob@example.com")
	info, err := os.Stat(filepath.Join(share.StateDir(e.home, "google-drive", alice), "token.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("令牌文件权限应为 0600：%v %v", info, err)
	}
	if data, _ := os.ReadFile(share.TargetsPath(e.home)); strings.Contains(string(data), "at0") || strings.Contains(string(data), "rt0") {
		t.Fatal("令牌不应写进 share-targets.json")
	}

	id1, account, err := e.svc.SaveTarget(share.TargetInput{Label: "工作 / 客户A", Adapter: "google-drive", Account: alice,
		Config: map[string]string{"dir": "Vinx/客户A"}, Sources: []string{"pages"}}, false)
	if err != nil || account != "alice@example.com · Vinx/客户A" {
		t.Fatalf("保存目标：%v %q", err, account)
	}
	id2, _, err := e.svc.SaveTarget(share.TargetInput{Label: "个人", Adapter: "google-drive", Account: bob, Public: true, Sources: []string{"pages"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	j1 := e.publish(t, id1)
	j2 := e.publish(t, id2)
	if j1.Status != "done" || j2.Status != "done" {
		t.Fatalf("发布：%+v %+v", j1, j2)
	}
	f1, f2 := findFile(t, e.fake, j1.Record.RemoteID), findFile(t, e.fake, j2.Record.RemoteID)
	if f1.Owner != "alice@example.com" || f1.Public || f1.Name != "订单方案.zip" || f1.AppProperties["vinxKey"] != j1.Record.ID {
		t.Fatalf("alice 的文件：%+v", f1)
	}
	if f2.Owner != "bob@example.com" || !f2.Public || len(f2.Parents) == 0 || f2.Parents[0] != "root" {
		t.Fatalf("bob 的文件：%+v", f2)
	}
	// 目录逐级创建：Vinx/客户A。
	folders := map[string]string{}
	for _, f := range e.fake.Files() {
		if f.MimeType == "application/vnd.google-apps.folder" {
			folders[f.Name] = f.Parents[0]
		}
	}
	if folders["Vinx"] != "root" || folders["客户A"] == "" || f1.Parents[0] == "root" {
		t.Fatalf("目录结构不对：%v %v", folders, f1.Parents)
	}
	if !strings.Contains(j1.Record.URL, j1.Record.RemoteID) {
		t.Fatalf("链接应指向文件：%s", j1.Record.URL)
	}

	// 更新：文件 id 和链接不变，内容变了；访问令牌过期时自动刷新。
	_ = os.WriteFile(filepath.Join(e.src, "plan.md"), []byte("# 方案 v2\n![](a.png)\n"), 0o644)
	e.fake.ExpireAccess()
	j3 := e.publish(t, id1)
	if j3.Status != "done" || j3.Record.RemoteID != j1.Record.RemoteID || j3.Record.URL != j1.Record.URL {
		t.Fatalf("更新：%+v %+v", j3, j3.Record)
	}
	if n := len(e.fake.Files()); n != 4 { // 两个文件夹 + 两个文件
		t.Fatalf("更新不应产生新文件：共 %d 个", n)
	}

	// 撤回：移到回收站；再撤回一次也算成功。
	rj, err := e.svc.Revoke(j1.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job := e.svc.Wait(rj.ID); job.Status != "done" || !findFile(t, e.fake, j1.Record.RemoteID).Trashed {
		t.Fatalf("撤回：%+v", job)
	}

	// 刷新令牌失效：auth_required，账号标为需要重新连接，目标仍列出。
	e.fake.RevokeRefresh()
	j4 := e.publish(t, id2)
	if j4.Status != "failed" || j4.Error.Code != "auth_required" {
		t.Fatalf("授权失效：%+v", j4)
	}
	admin, _ := e.svc.Admin()
	for _, a := range admin.Accounts {
		if a.ID == bob && a.Status != share.AccountAuthRequired {
			t.Fatalf("bob 应标为需要重新连接：%+v", a)
		}
	}
	views, _ := e.svc.Targets(e.page)
	found := false
	for _, v := range views {
		if v.ID == id2 && v.AccountStatus == share.AccountAuthRequired {
			found = true
		}
	}
	if !found {
		t.Fatalf("授权失效的目标应仍列出并带状态：%+v", views)
	}
	// 重新连接后恢复。
	start, err := e.svc.ConnectAccount("", bob)
	if err != nil {
		t.Fatal(err)
	}
	e.fake.Approve(start.UserCode, "bob@example.com")
	if poll, _ := e.svc.PollAccount(bob); poll.Status != "done" {
		t.Fatalf("重新连接：%+v", poll)
	}
	if job := e.publish(t, id2); job.Status != "done" {
		t.Fatalf("重新连接后发布：%+v", job)
	}

	// 断开账号：还有目标在用时拒绝。
	if err := e.svc.RemoveAccount(alice); err == nil {
		t.Fatal("还有目标在用时不应能断开账号")
	}
}

func TestDeviceAuthDenied(t *testing.T) {
	e := setup(t)
	start, err := e.svc.ConnectAccount("google-drive", "")
	if err != nil {
		t.Fatal(err)
	}
	e.fake.Deny(start.UserCode)
	poll, err := e.svc.PollAccount(start.Account)
	if err != nil || poll.Status != "failed" || poll.Error.Code != "auth_denied" {
		t.Fatalf("拒绝：%+v %v", poll, err)
	}
	admin, _ := e.svc.Admin()
	if len(admin.Accounts) != 0 {
		t.Fatalf("授权失败的新账号不应留下：%+v", admin.Accounts)
	}
}

func TestProviderRequiredAndSecretsHidden(t *testing.T) {
	e := setup(t)
	admin, _ := e.svc.Admin()
	for _, a := range admin.Adapters {
		for _, f := range a.Provider {
			if f.Secret && (f.Value != "" || !f.Set) {
				t.Fatalf("密钥不应回显：%+v", f)
			}
		}
	}
	// 密钥留空表示不修改。
	_ = e.svc.SetProvider("google-drive", map[string]string{"clientId": "cid2"}, map[string]string{"clientSecret": ""})
	st, _ := share.LoadStore(e.home)
	if st.Providers["google-drive"].Secrets["clientSecret"] != "csecret" || st.Providers["google-drive"].Config["clientId"] != "cid2" {
		t.Fatalf("服务商设置：%+v", st.Providers["google-drive"])
	}
	if err := e.svc.SetProvider("google-drive", map[string]string{"command": "x"}, nil); err == nil {
		t.Fatal("不认识的字段应拒绝")
	}
	if _, _, err := e.svc.SaveTarget(share.TargetInput{Label: "x", Adapter: "google-drive", Sources: []string{"pages"}}, true); err == nil ||
		!strings.Contains(err.Error(), "没有选择账号") {
		t.Fatalf("没有账号不能保存：%v", err)
	}
}

func TestConformance(t *testing.T) {
	e := setup(t)
	account := e.connect(t, "carol@example.com")
	b := share.LookupBuiltin("google-drive")
	var out strings.Builder
	err := share.RunConformance(share.ConformanceSetup{
		Manifest: b.Manifest, Argv: share.SelfCommand("google-drive"),
		Config: map[string]string{"dir": "一致性 测试"}, Provider: map[string]string{"clientId": "cid"},
		Secrets: map[string]string{"clientSecret": "csecret"}, Env: map[string]string{"VINX_GDRIVE_ENDPOINT": e.fake.URL},
		StateDir: share.StateDir(e.home, "google-drive", account),
	}, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log(out.String())
}

// Google 的分项授权：用户点了「允许」但没勾选 Drive。新连接直接失败并说明；已经存下的这种令牌，
// 保存目标时报「需要重新连接并勾选」而不是笼统的「没有权限」，账号标为需要重新连接。
func TestDriveScopeNotGranted(t *testing.T) {
	e := setup(t)
	start, err := e.svc.ConnectAccount("google-drive", "")
	if err != nil {
		t.Fatal(err)
	}
	e.fake.ApproveWithoutDrive(start.UserCode, "carol@example.com")
	poll, err := e.svc.PollAccount(start.Account)
	if err != nil || poll.Status != "failed" || poll.Error.Code != "auth_denied" || !strings.Contains(poll.Error.Message, "勾选") {
		t.Fatalf("没勾选 Drive 应失败并说明：%+v %v", poll, err)
	}
	if admin, _ := e.svc.Admin(); len(admin.Accounts) != 0 {
		t.Fatalf("失败的新账号不应留下：%+v", admin.Accounts)
	}

	carol := e.connect(t, "carol@example.com")
	e.fake.DropDriveScope()
	_, _, err = e.svc.SaveTarget(share.TargetInput{Label: "c", Adapter: "google-drive", Account: carol, Sources: []string{"pages"}}, false)
	if err == nil || !strings.Contains(err.Error(), "勾选") {
		t.Fatalf("保存目标应提示重新连接并勾选 Drive：%v", err)
	}
	admin, _ := e.svc.Admin()
	if len(admin.Accounts) != 1 || admin.Accounts[0].Status != share.AccountAuthRequired {
		t.Fatalf("账号应标为需要重新连接：%+v", admin.Accounts)
	}
}
