package build

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

// tempDir 建一个随测试自动删除的临时目录（系统临时目录下，VINX_GO_TEST_TMP 可覆盖）。
func tempDir(t *testing.T) string {
	t.Helper()
	base := os.Getenv("VINX_GO_TEST_TMP")
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, strings.ReplaceAll(t.Name(), "/", "_")+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// makeTool 家目录自带 web/ 和带哈希清单的 vendor/。
func makeTool(t *testing.T, base string) (tool, cfg, output string) {
	t.Helper()
	tool = filepath.Join(base, "tool")
	cfg = filepath.Join(tool, "config", "projects.json")
	output = filepath.Join(tool, ".runtime", "site")
	mustWrite(t, filepath.Join(tool, "web", "index.html"), "<html>index</html>")
	mustWrite(t, filepath.Join(tool, "web", "project.html"), "<html>project</html>")
	os.MkdirAll(filepath.Join(tool, "web", "assets"), 0o755)
	files := map[string]string{"docsify.min.js": "docsify", "search.min.js": "search", "vue.css": "vue", "LICENSE": "license"}
	hashes := ojson.NewObject()
	for name, content := range files {
		mustWrite(t, filepath.Join(tool, "vendor", name), content)
		sum := sha256.Sum256([]byte(content))
		hashes.Set(name, hex.EncodeToString(sum[:]))
	}
	mustWrite(t, filepath.Join(tool, "vendor", "manifest.json"), ojson.Dumps(ojson.NewObject("files", hashes), -1))
	return
}

func writeConfig(t *testing.T, path string, projects []any, extra ...any) {
	t.Helper()
	if projects == nil {
		projects = []any{}
	}
	value := ojson.NewObject(
		"schemaVersion", 1,
		"server", ojson.NewObject("host", "0.0.0.0", "port", 8000),
		"protectedPorts", []any{8088},
		"projects", projects,
	)
	for i := 0; i+1 < len(extra); i += 2 {
		value.Set(extra[i].(string), extra[i+1])
	}
	mustWrite(t, path, ojson.Dumps(value, -1))
}

// project 生成一个项目登记项。
func project(source string, overrides ...any) *ojson.Object {
	value := ojson.NewObject("id", "demo", "name", "演示文档", "docsPath", source, "home", "README.md", "exclude", []any{})
	for i := 0; i+1 < len(overrides); i += 2 {
		value.Set(overrides[i].(string), overrides[i+1])
	}
	return value
}

func setupSource(t *testing.T, base string) string {
	t.Helper()
	source := filepath.Join(base, "docs")
	mustWrite(t, filepath.Join(source, "README.md"), "# 首页\n\n中文内容")
	mustWrite(t, filepath.Join(source, "guide", "使用 说明.md"), "# 使用")
	return source
}

func strs(items ...string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func loadJSON(t *testing.T, path string) *ojson.Object {
	t.Helper()
	value, err := ojson.Decode([]byte(readText(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return value.(*ojson.Object)
}

func expectConfigError(t *testing.T, err error, contains ...string) {
	t.Helper()
	if err == nil || !config.IsConfigError(err) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
	for _, item := range contains {
		if !strings.Contains(err.Error(), item) {
			t.Fatalf("error %q does not contain %q", err.Error(), item)
		}
	}
}

func hasItem(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

func siteSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			data, _ := os.ReadFile(p)
			out[rel] = string(data)
		}
		return nil
	})
	return out
}
