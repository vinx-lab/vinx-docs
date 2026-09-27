// Package testutil 是各包测试共用的夹具：临时家目录、文档源目录、已发布页面等。
// 临时目录建在系统临时目录（遵循 TMPDIR）下；设置 VINX_GO_TEST_TMP 可以换到别处。
package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

// TempDir 建一个随测试自动删除的临时目录。
func TempDir(t testing.TB) string {
	t.Helper()
	base := os.Getenv("VINX_GO_TEST_TMP")
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// macOS 的临时目录在 /var/folders 下，/var 是指向 /private/var 的符号链接；Windows 的临时目录
	// 可能带 8.3 短名（RUNNER~1）。登记目录不允许经过符号链接，所以先解析成真实路径再交给测试。
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// Write 写文本文件（自动建目录）。
func Write(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Rename 改名文件或目录。Windows 上目录里有文件被打开时不能改名，而服务在后台构建、轮询时
// 会短暂打开登记目录里的文件，所以失败后重试一段时间再报错。
func Rename(t testing.TB, from, to string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := os.Rename(from, to)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Read 读文本文件。
func Read(t testing.TB, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// MakeTool 家目录自带 web/ 和带哈希清单的 vendor/。返回 (家目录, 配置路径, 站点路径)。
func MakeTool(t testing.TB, base string) (string, string, string) {
	t.Helper()
	tool := filepath.Join(base, "tool")
	Write(t, filepath.Join(tool, "web", "index.html"), "<html>index</html>")
	Write(t, filepath.Join(tool, "web", "project.html"), "<html>project</html>")
	os.MkdirAll(filepath.Join(tool, "web", "assets"), 0o755)
	hashes := ojson.NewObject()
	for name, content := range map[string]string{"docsify.min.js": "docsify", "search.min.js": "search", "vue.css": "vue", "LICENSE": "license"} {
		Write(t, filepath.Join(tool, "vendor", name), content)
		sum := sha256.Sum256([]byte(content))
		hashes.Set(name, hex.EncodeToString(sum[:]))
	}
	Write(t, filepath.Join(tool, "vendor", "manifest.json"), ojson.Dumps(ojson.NewObject("files", hashes), -1))
	return tool, filepath.Join(tool, "config", "projects.json"), filepath.Join(tool, ".runtime", "site")
}

// SetupSource 建一个带首页、子目录文档和隐藏文件的示例文档目录。
func SetupSource(t testing.TB, base string) string {
	t.Helper()
	source := filepath.Join(base, "docs")
	Write(t, filepath.Join(source, "README.md"), "# 首页\n\n中文内容")
	Write(t, filepath.Join(source, "guide", "使用 说明.md"), "# 使用")
	return source
}

// Project 生成一个项目登记项。
func Project(source string) *ojson.Object {
	return ojson.NewObject("id", "demo", "name", "演示文档", "docsPath", source, "home", "README.md", "exclude", []any{})
}

// WriteConfig 写一份包含给定项目的配置文件。
func WriteConfig(t testing.TB, path string, projects ...any) {
	t.Helper()
	if projects == nil {
		projects = []any{}
	}
	Write(t, path, ojson.Dumps(ojson.NewObject(
		"schemaVersion", 1,
		"server", ojson.NewObject("host", "0.0.0.0", "port", 8000),
		"protectedPorts", []any{8088},
		"projects", projects,
	), -1))
}

// MakePage 一个多文件原型。
func MakePage(t testing.TB, base string) string {
	t.Helper()
	Write(t, filepath.Join(base, "index.html"),
		`<!doctype html><title> 界面 原型 </title><link rel="stylesheet" href="css/app.css">`+
			`<img src="img/a.png" srcset="img/a.png 1x, img/b.png 2x"><a href="page2.html#top">下一页</a>`+
			`<script type="module" src="js/main.js"></script><script src="https://cdn.example.com/x.js"></script>`+
			`<img src="/abs.png"><a href="#local">锚点</a><img src="data:image/png;base64,AA==">`+
			`<a href="https://docs.example.com/x">外部文档</a><a class="x" href="/root-link">站内</a></body>`)
	Write(t, filepath.Join(base, "css", "app.css"), `@font-face{src:url("../font.woff2")} body{background:url(../img/bg.png)}`)
	Write(t, filepath.Join(base, "font.woff2"), "font")
	Write(t, filepath.Join(base, "js", "main.js"), "import {x} from './util.js';\nimport y from 'lib';\nfetch('../data.json');\nfetch(\"../more.json\");\n")
	Write(t, filepath.Join(base, "js", "util.js"), "export const x = 1;\n")
	Write(t, filepath.Join(base, "data.json"), `{"ok": true}`)
	Write(t, filepath.Join(base, "more.json"), "{}")
	Write(t, filepath.Join(base, "page2.html"), `<img src="img/c.png">`)
	for _, name := range []string{"a", "b", "c", "bg"} {
		Write(t, filepath.Join(base, "img", name+".png"), "\x89PNG"+name)
	}
	Write(t, filepath.Join(base, "unused", "secret.txt"), "不在清单里")
	return base
}

// Digest 目录下每个文件的 SHA-256。
func Digest(t testing.TB, base string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			data, _ := os.ReadFile(p)
			sum := sha256.Sum256(data)
			rel, _ := filepath.Rel(base, p)
			out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	return out
}
