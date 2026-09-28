package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// 在 Linux 上构造临时的「假系统链接」，验证 macOS 的 /tmp、/var、/etc 放行条件。
func TestDarwinSystemLinkAncestors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	// 先解析掉临时目录自身的符号链接（macOS 的 /var → /private/var），测试里只保留下面造的假链接。
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "private", "tmp")
	if err := os.MkdirAll(filepath.Join(real, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "elsewhere", "docs"), 0o755)
	link := filepath.Join(dir, "tmp")          // 链接内容正确
	tampered := filepath.Join(dir, "tmp2")     // 在表里，但指向别处
	unlisted := filepath.Join(dir, "unlisted") // 不在表里
	for name, target := range map[string]string{link: real, tampered: filepath.Join(dir, "elsewhere"), unlisted: real} {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	savedHost, savedLinks := darwinHost, systemLinks
	defer func() { darwinHost, systemLinks = savedHost, savedLinks }()
	key := func(p string) string { return textutil.Absolute(p) }
	systemLinks = map[string][]string{
		key(link):     {real, "private/tmp"},
		key(tampered): {real, "private/tmp"},
	}
	check := func(p string) error {
		_, err := AbsoluteNoSymlink(filepath.Join(p, "docs"), "文档目录")
		return err
	}

	darwinHost = true
	if err := check(link); err != nil {
		t.Errorf("目标正确的系统链接应放行: %v", err)
	}
	if err := check(tampered); err == nil {
		t.Error("目标被篡改的系统链接应拒绝")
	}
	if err := check(unlisted); err == nil {
		t.Error("不在表内的符号链接应拒绝")
	}
	// 表内链接之下再出现的符号链接仍然拒绝。
	os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(real, "inner"))
	if err := check(filepath.Join(link, "inner")); err == nil {
		t.Error("系统链接之下的其他符号链接应拒绝")
	}

	darwinHost = false
	if err := check(link); err == nil {
		t.Error("非 macOS 上不应放行任何符号链接")
	}
}

func TestForbiddenDirCase(t *testing.T) {
	saved := caseInsensitiveNames
	defer func() { caseInsensitiveNames = saved }()

	caseInsensitiveNames = false
	if !IsForbiddenDir("node_modules") || IsForbiddenDir("Node_Modules") {
		t.Error("区分大小写平台：只拦小写原名")
	}
	if PolicyReason("Node_Modules/a.md", "/r/Node_Modules/a.md") != "" {
		t.Error("区分大小写平台上 Node_Modules 行为应保持不变")
	}

	caseInsensitiveNames = true
	for _, name := range []string{"node_modules", "Node_Modules", "NODE_MODULES", "Target", "LOGS", "__PyCache__", ".GIT"} {
		if !IsForbiddenDir(name) {
			t.Errorf("Windows 上 %q 应被拦住", name)
		}
	}
	if IsForbiddenDir("docs") {
		t.Error("普通目录不应被拦")
	}
	if PolicyReason("Node_Modules/a.md", "C:/r/Node_Modules/a.md") != "运行缓存或仓库目录" {
		t.Error("Windows 上 PolicyReason 应拦住 Node_Modules")
	}
	// 凭据文件名本来就不区分大小写。
	for _, name := range []string{"ID_RSA", "Credentials.JSON", "Server.PEM", "Prod.ENV"} {
		if PolicyReason(name, "C:/r/"+name) == "" {
			t.Errorf("%q 应被拦住", name)
		}
	}
}

func TestValidateProjectTypes(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# 首页"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(types any) *ojson.Object {
		p := ojson.NewObject("id", "demo", "name", "演示", "docsPath", dir, "home", "README.md")
		if types != nil {
			p.Set("types", types)
		}
		return p
	}
	for _, ok := range []any{nil, []any{}, []any{".md", ".html"}, []any{"MD", ".HTML", ".md"}} {
		if _, err := ValidateProject(mk(ok), nil); err != nil {
			t.Errorf("types %v 应通过: %v", ok, err)
		}
	}
	for _, bad := range []any{".md", []any{".md", ".exe"}, []any{".md", 3}, []any{".html"}, []any{".md", ".tar.md"}} {
		if _, err := ValidateProject(mk(bad), nil); err == nil || !IsConfigError(err) {
			t.Errorf("types %v 应被拒绝, got %v", bad, err)
		}
	}
	if got := Types(mk([]any{"MD", ".HTML", ".md", " png "})); strings.Join(got, ",") != ".html,.md,.png" {
		t.Errorf("规范化结果不对: %v", got)
	}
	if got := Types(mk(nil)); len(got) != 0 {
		t.Errorf("缺省应表示不限类型: %v", got)
	}
	// include 的报错保持原样。
	p := mk(nil)
	p.Set("include", []any{"*.md"})
	if _, err := ValidateProject(p, nil); err == nil || !strings.Contains(err.Error(), "include白名单已移除") {
		t.Errorf("include 报错变了: %v", err)
	}
}

func TestTypeGroupsCoverAllKinds(t *testing.T) {
	seen := map[string]bool{}
	for _, group := range TypeGroups() {
		for _, ext := range group.Extensions {
			if KindFor("x"+ext) == "" || seen[ext] {
				t.Errorf("分组里的扩展名无效或重复: %s", ext)
			}
			seen[ext] = true
		}
	}
	for _, m := range []map[string]bool{TextExtensions, MarkdownExtensions, HTMLExtensions, ImageExtensions, DownloadExtensions} {
		for ext := range m {
			if !seen[ext] {
				t.Errorf("分组漏了 %s", ext)
			}
		}
	}
}
