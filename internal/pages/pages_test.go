package pages

// 短链接发布的单元测试；HTTP 相关的行为（注入脚本、版本、MIME、CSP）按函数单独验证。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	tu "github.com/vinx-lab/vinx-docs/internal/testutil"
)

func sorted(items []string) string {
	c := append([]string(nil), items...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

func TestPublishCollectsReferencedFilesAndReportsUnusableRefs(t *testing.T) {
	base := tu.TempDir(t)
	page := tu.MakePage(t, filepath.Join(base, "proto"))
	before := tu.Digest(t, page)
	record, notes, err := Publish(filepath.Join(base, "reg.json"), filepath.Join(page, "index.html"), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Value("title") != "界面 原型" || record.Value("entry") != "index.html" {
		t.Fatal(ojson.Dumps(record, -1))
	}
	want := []string{"index.html", "css/app.css", "font.woff2", "img/bg.png", "img/a.png", "img/b.png", "img/c.png",
		"page2.html", "js/main.js", "js/util.js", "data.json", "more.json"}
	if sorted(Files(record)) != sorted(want) {
		t.Fatal(Files(record))
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "外部地址") || !strings.Contains(joined, "绝对路径") {
		t.Fatal(notes)
	}
	if strings.Contains(joined, "docs.example.com") || strings.Contains(joined, "root-link") {
		t.Fatal("超链接跳转不是资源，不该提示")
	}
	after := tu.Digest(t, page)
	if ojson.Dumps(mapObj(after), -1) != ojson.Dumps(mapObj(before), -1) {
		t.Fatal("发布不能改动源文件")
	}
}

func mapObj(m map[string]string) *ojson.Object {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	o := ojson.NewObject()
	for _, k := range keys {
		o.Set(k, m[k])
	}
	return o
}

func TestPublishMarkdownCollectsImagesOnly(t *testing.T) {
	base := tu.TempDir(t)
	doc := filepath.Join(base, "doc")
	tu.Write(t, filepath.Join(doc, "plan.md"), "前言\n\n# 方案 A\n\n![图](img/a.png) ![带标题](<img/b c.png> \"t\")\n"+
		"<img src=\"img/c.png\" srcset=\"img/d.png 2x\">\n[另一篇](other.md) [附件](data.xlsx) ![外部](https://x.example/e.png)\n"+
		"代码里的不算：`![x](img/no1.png)`\n\n```md\n![y](img/no2.png)\n```\n")
	for _, name := range []string{"img/a.png", "img/b c.png", "img/c.png", "img/d.png", "other.md", "data.xlsx"} {
		tu.Write(t, filepath.Join(doc, filepath.FromSlash(name)), "x")
	}
	registry := filepath.Join(base, "reg.json")
	record, notes, err := Publish(registry, filepath.Join(doc, "plan.md"), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Value("title") != "方案 A" || record.Value("entry") != "plan.md" {
		t.Fatal(ojson.Dumps(record, -1))
	}
	// 只收图片；链接到的其他 Markdown 和附件不自动收。
	if got := sorted(Files(record)); got != sorted([]string{"plan.md", "img/a.png", "img/b c.png", "img/c.png", "img/d.png"}) {
		t.Fatal(got)
	}
	if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "外部地址") || strings.Contains(joined, "no1") || strings.Contains(joined, "no2") {
		t.Fatal(notes)
	}
	withExtra, _, err := Publish(registry, filepath.Join(doc, "plan.md"), "", []string{"other.md", "data.xlsx"}, "")
	if err != nil || Str(withExtra, "id") != Str(record, "id") {
		t.Fatal(err, ojson.Dumps(withExtra, -1))
	}
	manifest := ReaderManifest(withExtra)
	id := Str(record, "id")
	if manifest.Value("home") != "plan.md" || manifest.Value("target") != "a:"+id+"/" || manifest.Value("content") != "/a/"+id+"/" {
		t.Fatal(ojson.Dumps(manifest, -1))
	}
	kinds := map[string]string{}
	for _, item := range manifest.Value("entries").([]any) {
		entry := item.(*ojson.Object)
		kind := Str(entry, "kind")
		if Str(entry, "route") != "" {
			kind = "route"
		}
		kinds[Str(entry, "path")] = kind
	}
	if kinds["plan.md"] != "route" || kinds["other.md"] != "route" || kinds["img/a.png"] != "image" || kinds["data.xlsx"] != "download" {
		t.Fatal(kinds)
	}
	if s := Summary(withExtra); s.Value("kind") != "markdown" || s.Value("readUrl") != "/read.html?a="+id {
		t.Fatal(ojson.Dumps(s, -1))
	}
	tu.Write(t, filepath.Join(doc, "notes.txt"), "x")
	if _, _, err := Publish(registry, filepath.Join(doc, "notes.txt"), "", nil, ""); err == nil || !strings.Contains(err.Error(), ".html或.md") {
		t.Fatal(err)
	}
}

func TestRepublishKeepsShortIDAndPicksUpNewFiles(t *testing.T) {
	base := tu.TempDir(t)
	page := tu.MakePage(t, filepath.Join(base, "proto"))
	registry := filepath.Join(base, "reg.json")
	first, _, err := Publish(registry, filepath.Join(page, "index.html"), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tu.Write(t, filepath.Join(page, "extra.txt"), "后补")
	second, _, err := Publish(registry, filepath.Join(page, "index.html"), "", []string{"extra.txt"}, "新标题")
	if err != nil {
		t.Fatal(err)
	}
	if second.Value("id") != first.Value("id") || second.Value("title") != "新标题" || !strings.Contains(sorted(Files(second)), "extra.txt") {
		t.Fatal(ojson.Dumps(second, -1))
	}
	value, _ := LoadRegistry(registry)
	if len(records(value)) != 1 {
		t.Fatal("republish duplicated record")
	}
	other, _, _ := Publish(registry, filepath.Join(page, "page2.html"), "", nil, "")
	if other.Value("id") == first.Value("id") {
		t.Fatal("different entry must get a new id")
	}
}

func expectError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !config.IsConfigError(err) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected ConfigError containing %q, got %v", contains, err)
	}
}

func TestPublishRejectsEscapesAndSensitiveFiles(t *testing.T) {
	base := tu.TempDir(t)
	page := tu.MakePage(t, filepath.Join(base, "proto"))
	registry := filepath.Join(base, "reg.json")
	entry := filepath.Join(page, "index.html")
	tu.Write(t, entry, `<img src="../outside.png">`)
	_, _, err := Publish(registry, entry, "", nil, "")
	expectError(t, err, "--root")
	tu.Write(t, entry, `<img src=".hidden/x.png"><link href="id_rsa">`)
	tu.Write(t, filepath.Join(page, ".hidden", "x.png"), "x")
	tu.Write(t, filepath.Join(page, "id_rsa"), "key")
	record, notes, err := Publish(registry, entry, "", nil, "")
	if err != nil || strings.Join(Files(record), ",") != "index.html" {
		t.Fatal(err, Files(record))
	}
	skipped := 0
	for _, note := range notes {
		if strings.Contains(note, "已跳过引用") {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatal(notes)
	}
	tu.Write(t, filepath.Join(page, "key.txt"), "-----BEGIN PRIVATE KEY-----\n")
	for _, bad := range []string{".hidden/x.png", "key.txt", "../proto/index.html", "__docsify_x/version"} {
		if _, _, err := Publish(registry, entry, "", []string{bad}, ""); err == nil || !config.IsConfigError(err) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	tu.Write(t, filepath.Join(base, "real", "p.png"), "x")
	os.Symlink(filepath.Join(base, "real"), filepath.Join(page, "link"))
	_, _, err = Publish(registry, entry, "", []string{"link/p.png"}, "")
	expectError(t, err, "符号链接")
	tu.Write(t, filepath.Join(page, "notes.txt"), "x")
	_, _, err = Publish(registry, filepath.Join(page, "notes.txt"), "", nil, "")
	expectError(t, err, ".html")
}

func TestListingNewestFirstAndReportsMissing(t *testing.T) {
	base := tu.TempDir(t)
	registry := filepath.Join(base, "reg.json")
	one, _, _ := Publish(registry, filepath.Join(tu.MakePage(t, filepath.Join(base, "one")), "index.html"), "", nil, "")
	two, _, _ := Publish(registry, filepath.Join(tu.MakePage(t, filepath.Join(base, "two")), "index.html"), "", nil, "")
	value, _ := LoadRegistry(registry)
	record := Find(value, Str(one, "id"))
	n, _ := record.Value("publishedAt").(ojson.Number).BigInt()
	record.Set("publishedAt", n.Int64()-100)
	tu.Write(t, registry, ojson.Dumps(value, -1))
	os.Remove(filepath.Join(base, "two", "img", "a.png"))
	items, err := Listing(registry)
	if err != nil {
		t.Fatal(err)
	}
	first := items[0].(*ojson.Object)
	if first.Value("id") != two.Value("id") || items[1].(*ojson.Object).Value("id") != one.Value("id") {
		t.Fatal("order")
	}
	if ojson.Dumps(first.Value("missing"), -1) != `["img/a.png"]` || first.Value("url") != "/a/"+Str(two, "id")+"/" {
		t.Fatal(ojson.Dumps(first, -1))
	}
	// 服务按请求重新校验：缺失的文件读不到。
	if _, err := CheckFile(Str(two, "root"), "img/a.png"); err == nil {
		t.Fatal("missing file passed CheckFile")
	}
}

func TestShortLinkHelpers(t *testing.T) {
	base := tu.TempDir(t)
	page := tu.MakePage(t, filepath.Join(base, "proto"))
	record, _, err := Publish(filepath.Join(base, "reg.json"), filepath.Join(page, "index.html"), "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	version := Version(record)
	body := InjectLive([]byte("<html><BODY>x</Body></body>"), version, `a"b.html`)
	re := regexp.MustCompile(`artifact-live\.js" data-version="([0-9a-f]+)" data-page="a&quot;b.html"></script></body>$`)
	if m := re.FindSubmatch(body); m == nil || string(m[1]) != version {
		t.Fatal(string(body))
	}
	if string(InjectLive([]byte("no body"), "v", "")) != `no body<script src="/assets/artifact-live.js" data-version="v" data-page=""></script>` {
		t.Fatal("append when no </body>")
	}
	tu.Write(t, filepath.Join(page, "img", "a.png"), "\x89PNGchanged-longer")
	if Version(record) == version {
		t.Fatal("version must change with file size/mtime")
	}
	if MimeFor("img/A.PNG") != "image/png" || MimeFor("x.bin") != "application/octet-stream" || !strings.HasPrefix(MimeFor("a.css"), "text/css") {
		t.Fatal("mime")
	}
	if !strings.HasPrefix(PagePolicy, "sandbox allow-scripts ") || strings.Contains(PagePolicy, "allow-same-origin") {
		t.Fatal("policy")
	}
	// 发布后才写进私钥的文件也不能读出。
	tu.Write(t, filepath.Join(page, "data.json"), "-----BEGIN PRIVATE KEY-----\n")
	if _, err := CheckFile(Str(record, "root"), "data.json"); err == nil {
		t.Fatal("private key served")
	}
	if _, err := Unpublish(filepath.Join(base, "reg.json"), Str(record, "id")); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpublish(filepath.Join(base, "reg.json"), "nope"); err == nil {
		t.Fatal("unpublish unknown id")
	}
	if _, ok := tu.Digest(t, page)["unused/secret.txt"]; !ok {
		t.Fatal("unpublish must not delete source files")
	}
}

func TestPublicBaseRules(t *testing.T) {
	home := tu.TempDir(t)
	tu.Write(t, filepath.Join(home, "config", "projects.json"),
		`{"schemaVersion": 2, "server": {"host": "0.0.0.0", "port": 18123, "publicBase": "http://box.tail:8000/"}, "projects": []}`)
	if got := PublicBase(home); got != "http://box.tail:8000" {
		t.Fatal(got)
	}
	old := TailscaleStatus
	defer func() { TailscaleStatus = old }()
	tu.Write(t, filepath.Join(home, "config", "projects.json"),
		`{"schemaVersion": 2, "server": {"host": "0.0.0.0", "port": 18123, "publicBase": "javascript:alert(1)"}, "projects": []}`)
	TailscaleStatus = func() ([]byte, error) { return []byte(`{"Self": {"DNSName": "myhost.tail1234.ts.net."}}`), nil }
	if got := PublicBase(home); got != "http://myhost.tail1234.ts.net:18123" {
		t.Fatal(got)
	}
	TailscaleStatus = func() ([]byte, error) { return nil, os.ErrNotExist }
	if got := PublicBase(home); got != "http://localhost:18123" {
		t.Fatal(got)
	}
}
