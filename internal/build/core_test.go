package build

// 构建、登记、排除规则和安全边界的测试。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

func TestDefaultConfigHasSafeServerDefaults(t *testing.T) {
	_, cfg, _ := makeTool(t, tempDir(t))
	value, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := value.Value("server").(*ojson.Object)
	if server.Value("host") != "0.0.0.0" || server.Value("port") != ojson.Number("8000") {
		t.Fatal(ojson.Dumps(server, -1))
	}
	if len(config.ProtectedPorts(value)) != 0 || len(config.Projects(value)) != 0 {
		t.Fatal("expected empty lists")
	}
	if config.SettingsOf(value).DisplayName != "我" {
		t.Fatal("displayName default")
	}
}

func TestRefreshImportsAllProjectsAndReportsSkips(t *testing.T) {
	base := tempDir(t)
	a := setupSource(t, filepath.Join(base, "a"))
	b := setupSource(t, filepath.Join(base, "b"))
	mustWrite(t, filepath.Join(a, "guide", ".private.md"), "private")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(a), project(b, "id", "second")})

	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if results[0].ID != "demo" || results[1].ID != "second" {
		t.Fatal("ids")
	}
	found := false
	for _, item := range results[0].Skipped {
		if strings.HasPrefix(item, "guide/.private.md:") && strings.Contains(item, "隐藏") {
			found = true
		}
	}
	if !found {
		t.Fatal(results[0].Skipped)
	}
	for _, id := range []string{"demo", "second"} {
		if !exists(filepath.Join(output, "projects", id, "manifest.json")) {
			t.Fatal("manifest missing")
		}
	}
	catalog := loadJSON(t, filepath.Join(output, "catalog.json"))
	for _, item := range catalog.Value("projects").([]any) {
		obj := item.(*ojson.Object)
		if obj.Has("skipped") {
			t.Fatal("catalog leaks skipped")
		}
		recent := obj.Value("recent").([]any)
		if len(recent) == 0 {
			t.Fatal("recent empty")
		}
		keys := recent[0].(*ojson.Object).Keys()
		sort.Strings(keys)
		if strings.Join(keys, ",") != "path,route,title,updatedAt" {
			t.Fatal(keys)
		}
	}
}

func TestRefreshSkipsNonUTF8Text(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "guide", "invalid.json"), "{\xff")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasItem(results[0].Skipped, "guide/invalid.json:不是UTF-8文本") {
		t.Fatal(results[0].Skipped)
	}
	if exists(filepath.Join(output, "projects", "demo", "raw", "guide", "invalid.json")) {
		t.Fatal("non-utf8 text published")
	}
}

func TestValidateRejectsProtectedPortAndBadID(t *testing.T) {
	source := setupSource(t, tempDir(t))
	bad := ojson.NewObject("schemaVersion", 1, "server", ojson.NewObject("host", "0.0.0.0", "port", 8088),
		"protectedPorts", []any{8088}, "projects", []any{})
	bad, _ = decodeRoundTrip(bad)
	expectConfigError(t, config.ValidateShape(bad), "受保护")
	_, err := config.ValidateProject(project(source, "id", "../bad"), []any{ojson.Number("8088")})
	expectConfigError(t, err, "id")
}

// decodeRoundTrip 把 Go 构造的对象转成与读取配置相同的表示（数字为 Number）。
func decodeRoundTrip(value *ojson.Object) (*ojson.Object, error) {
	decoded, err := ojson.Decode([]byte(ojson.Dumps(value, -1)))
	if err != nil {
		return nil, err
	}
	return decoded.(*ojson.Object), nil
}

func TestRegisterRejectsOverlappingRootsAndExistingID(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	child := filepath.Join(source, "child")
	mustWrite(t, filepath.Join(child, "README.md"), "# child")
	_, cfg, output := makeTool(t, base)
	if err := RegisterProject(cfg, output, project(source)); err != nil {
		t.Fatal(err)
	}
	expectConfigError(t, RegisterProject(cfg, output, project(child, "id", "child")), "重叠")
	expectConfigError(t, RegisterProject(cfg, output, project(source, "id", "demo2")), "重叠")
	expectConfigError(t, RegisterProject(cfg, output, project(source)), "已存在")
}

func TestExportFiltersSymlinksHiddenAndCredentials(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, ".secret.md"), "secret")
	mustWrite(t, filepath.Join(source, "guide", "token.key"), "key")
	outside := filepath.Join(base, "outside.md")
	mustWrite(t, outside, "outside")
	os.Symlink(outside, filepath.Join(source, "guide", "escape.md"))
	_, cfg, output := makeTool(t, base)
	if err := RegisterProject(cfg, output, project(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(output, "projects", "demo", "raw")
	for _, rel := range []string{"guide/escape.md", "guide/token.key", ".secret.md"} {
		if exists(filepath.Join(raw, rel)) {
			t.Fatal(rel + " exported")
		}
	}
}

func TestExportGeneratesManifestsTypesPreviewsAndUnicode(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "guide", "data.json"), `{"name":"中文"}`)
	mustWrite(t, filepath.Join(source, "guide", "report.html"), "<h1>报告</h1>")
	mustWrite(t, filepath.Join(source, "guide", "sheet.xlsx"), "xlsx")
	mustWrite(t, filepath.Join(source, "guide", "photo.png"), "png")
	_, cfg, output := makeTool(t, base)
	RegisterProject(cfg, output, project(source))
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	manifest := loadJSON(t, filepath.Join(output, "projects", "demo", "manifest.json"))
	byPath := map[string]*ojson.Object{}
	for _, item := range manifest.Value("entries").([]any) {
		entry := item.(*ojson.Object)
		byPath[entry.Value("path").(string)] = entry
	}
	check := func(path, key string, want any) {
		if got := byPath[path].Value(key); got != want {
			t.Fatalf("%s.%s = %v, want %v", path, key, got, want)
		}
	}
	check("README.md", "kind", "markdown")
	if !strings.HasPrefix(byPath["guide/data.json"].Value("route").(string), "__previews/") {
		t.Fatal("preview route")
	}
	check("guide/report.html", "kind", "html")
	check("guide/report.html", "route", nil)
	check("guide/sheet.xlsx", "kind", "download")
	check("guide/photo.png", "kind", "image")
	preview := readText(t, filepath.Join(output, "projects", "demo", "content", "__previews", "guide", "data.json.md"))
	if !strings.Contains(preview, "中文") {
		t.Fatal(preview)
	}
	if readText(t, filepath.Join(output, "projects", "demo", "content", "guide", "使用 说明.md")) != "# 使用" {
		t.Fatal("content copy")
	}
}

func TestIncludeWhitelistIsRejected(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	expectConfigError(t, RegisterProject(cfg, output, project(source, "include", strs("README.md"))), "include")
}

func TestLegacyWhitelistConfigMigratesToFullScope(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "operations", "new-note.md"), "# 新文档")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "include", strs("README.md"))})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(output, "projects", "demo", "raw", "operations", "new-note.md")) || results[0].FileCount != 3 {
		t.Fatal("migration")
	}
}

func TestRefreshDoesNotModifySourceAndRemovesOldExport(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	RegisterProject(cfg, output, project(source))
	Refresh(cfg, output, "", "")
	original := readText(t, filepath.Join(source, "README.md"))
	mustWrite(t, filepath.Join(source, "guide", "old.md"), "old")
	Refresh(cfg, output, "", "")
	old := filepath.Join(output, "projects", "demo", "raw", "guide", "old.md")
	if !exists(old) {
		t.Fatal("new file not published")
	}
	os.Remove(filepath.Join(source, "guide", "old.md"))
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	if exists(old) {
		t.Fatal("stale file kept")
	}
	if readText(t, filepath.Join(source, "README.md")) != original {
		t.Fatal("source modified")
	}
}

func TestUnregisterOnlyRemovesExport(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	RegisterProject(cfg, output, project(source))
	Refresh(cfg, output, "", "")
	if err := Unregister(cfg, output, "demo"); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(source, "README.md")) || exists(filepath.Join(output, "projects", "demo")) {
		t.Fatal("unregister")
	}
	if len(config.Projects(loadJSON(t, cfg))) != 0 {
		t.Fatal("config not updated")
	}
}

func TestOutputCannotBeSourceOrDangerous(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, _ := makeTool(t, base)
	expectConfigError(t, RegisterProject(cfg, source, project(source)))
	_, err := config.ValidateOutput("/", []string{source}, "")
	expectConfigError(t, err)
}

func TestSourceRootAncestorSymlinkIsRejected(t *testing.T) {
	base := tempDir(t)
	realParent := filepath.Join(base, "real")
	setupSource(t, realParent)
	link := filepath.Join(base, "linked")
	os.Symlink(realParent, link)
	_, err := config.ValidateProject(project(filepath.Join(link, "docs")), nil)
	expectConfigError(t, err, "符号链接")
}

func TestManifestVersionChangesWithContent(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	RegisterProject(cfg, output, project(source))
	Refresh(cfg, output, "", "")
	manifest := filepath.Join(output, "projects", "demo", "manifest.json")
	first := loadJSON(t, manifest).Value("version")
	mustWrite(t, filepath.Join(source, "README.md"), "# 首页\n\nchanged")
	Refresh(cfg, output, "", "")
	if loadJSON(t, manifest).Value("version") == first {
		t.Fatal("version unchanged")
	}
}

func TestProjectShellIsCopiedWithoutInterpolation(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	tool, cfg, output := makeTool(t, base)
	shell := "<script>const id = location.pathname;</script>"
	mustWrite(t, filepath.Join(tool, "web", "project.html"), shell)
	RegisterProject(cfg, output, project(source))
	if _, err := Refresh(cfg, output, tool, ""); err != nil {
		t.Fatal(err)
	}
	if readText(t, filepath.Join(output, "projects", "demo", "index.html")) != shell {
		t.Fatal("shell changed")
	}
}

func TestRefreshRejectsTamperedVendor(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	vendor := filepath.Join(base, "vendor")
	mustWrite(t, filepath.Join(vendor, "x.js"), "good")
	writeConfig(t, cfg, []any{project(source)}, "vendor", ojson.NewObject("files", ojson.NewObject("x.js", "not-the-right-hash")))
	_, err := Refresh(cfg, output, base, vendor)
	expectConfigError(t, err, "vendor")
}

func TestExcludePrunesDirectoriesAndIsReported(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "operations", "keep.md"), "# 保留")
	mustWrite(t, filepath.Join(source, "operations", "secrets", "drop.md"), "# 排除")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "exclude", strs("operations/secrets/"))})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(output, "projects", "demo", "raw")
	if !exists(filepath.Join(raw, "operations", "keep.md")) || exists(filepath.Join(raw, "operations", "secrets")) {
		t.Fatal("prune")
	}
	if !hasItem(results[0].Skipped, "operations/secrets/:exclude") {
		t.Fatal(results[0].Skipped)
	}
}

func TestDoubleStarPatternsAlsoMatchTheTopLevel(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "top.json"), "{}")
	mustWrite(t, filepath.Join(source, "drafts", "wip.md"), "# 草稿")
	mustWrite(t, filepath.Join(source, "guide", "nested.json"), "{}")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "exclude", strs("**/*.json", "**/drafts/**"))})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(output, "projects", "demo", "raw")
	for _, rel := range []string{"top.json", "guide/nested.json", "drafts"} {
		if exists(filepath.Join(raw, rel)) {
			t.Fatal(rel)
		}
	}
	for _, item := range []string{"top.json:exclude", "guide/nested.json:exclude", "drafts/:exclude"} {
		if !hasItem(results[0].Skipped, item) {
			t.Fatal(item)
		}
	}
}

func TestExcludeRulesThatMatchNothingAreReported(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "tool.py"), "x = 1")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "exclude", strs(".py", "*.py", "missing/"))})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(results[0].UnusedExclude, ",") != ".py,missing/" {
		t.Fatal(results[0].UnusedExclude)
	}
	report := loadJSON(t, filepath.Join(filepath.Dir(output), "report.json"))
	unused := report.Value("projects").([]any)[0].(*ojson.Object).Value("unusedExclude")
	if ojson.Dumps(unused, -1) != `[".py", "missing/"]` {
		t.Fatal(ojson.Dumps(unused, -1))
	}
}

func TestUnchangedFilesAreReusedAndChangesStillPublish(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	first, _ := Refresh(cfg, output, "", "")
	second, _ := Refresh(cfg, output, "", "")
	if first[0].Reused != 0 || second[0].Reused != second[0].FileCount {
		t.Fatal("reuse counts")
	}
	mustWrite(t, filepath.Join(source, "guide", "changed.md"), "# 新增")
	third, _ := Refresh(cfg, output, "", "")
	if third[0].Reused != third[0].FileCount-1 || !exists(filepath.Join(output, "projects", "demo", "content", "guide", "changed.md")) {
		t.Fatal("incremental")
	}
}

func TestReuseRebuildsWhenThePublishedCopyDisappears(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	Refresh(cfg, output, "", "")
	published := filepath.Join(output, "projects", "demo", "raw", "README.md")
	os.Remove(published)
	Refresh(cfg, output, "", "")
	if !exists(published) {
		t.Fatal("copy not restored")
	}
}

func TestScopePageAndSearchIndexAreGenerated(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "guide", "skipme.md"), "# 排除")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "exclude", strs("guide/skipme.md"))})
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	scope := readText(t, filepath.Join(output, "projects", "demo", "content", "__scope.md"))
	if !strings.Contains(scope, "guide/skipme.md") || !strings.Contains(scope, "exclude") {
		t.Fatal(scope)
	}
	index := loadJSON(t, filepath.Join(output, "search-index.json"))
	var paths []string
	for _, item := range index.Value("documents").([]any) {
		doc := item.(*ojson.Object)
		paths = append(paths, doc.Value("path").(string))
		if doc.Value("project") != "demo" {
			t.Fatal("project")
		}
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "README.md,guide/使用 说明.md" {
		t.Fatal(paths)
	}
}

func TestSettingsRoundTripAndRejectBadValues(t *testing.T) {
	_, cfg, _ := makeTool(t, tempDir(t))
	writeConfig(t, cfg, nil)
	saved, err := SaveSettings(cfg, ojson.NewObject("autoSync", false, "debounceSeconds", ojson.Number("12")))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Value("autoSync") != false || saved.Value("debounceSeconds") != ojson.Number("12") {
		t.Fatal(ojson.Dumps(saved, -1))
	}
	if loadJSON(t, cfg).Value("settings").(*ojson.Object).Value("debounceSeconds") != ojson.Number("12") {
		t.Fatal("not persisted")
	}
	_, err = SaveSettings(cfg, ojson.NewObject("debounceSeconds", ojson.Number("0")))
	expectConfigError(t, err)
	_, err = SaveSettings(cfg, ojson.NewObject("autoSync", "yes"))
	expectConfigError(t, err)
}

func TestUpdateProjectChangesNameAndExclude(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	Refresh(cfg, output, "", "")
	results, err := UpdateProject(cfg, output, "demo", ProjectUpdate{Name: "改过的名字", Exclude: strs("guide/")})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Name != "改过的名字" || exists(filepath.Join(output, "projects", "demo", "raw", "guide")) {
		t.Fatal("update")
	}
	if !exists(filepath.Join(source, "guide", "使用 说明.md")) {
		t.Fatal("source touched")
	}
	saved := config.Projects(loadJSON(t, cfg))[0].(*ojson.Object)
	if saved.Value("name") != "改过的名字" || ojson.Dumps(saved.Value("exclude"), -1) != `["guide/"]` {
		t.Fatal(ojson.Dumps(saved, -1))
	}
}

func TestPrivateKeyContentIsNeverExportedAndSecretsAreFlagged(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "guide", "deploy.sh"), "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")
	mustWrite(t, filepath.Join(source, "guide", "app.yaml"), "spring:\n  datasource:\n    password: Real-Secret-9x\n")
	mustWrite(t, filepath.Join(source, "guide", "sample.yaml"), "password: ${DB_PASSWORD}\n# password: changeme\n")
	mustWrite(t, filepath.Join(source, "guide", "ssh_config"), "Host demo\n")
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(output, "projects", "demo", "raw", "guide")
	if exists(filepath.Join(raw, "deploy.sh")) || exists(filepath.Join(raw, "ssh_config")) || !exists(filepath.Join(raw, "app.yaml")) {
		t.Fatal("policy")
	}
	if !hasItem(results[0].Skipped, "guide/deploy.sh:文件内含私钥") {
		t.Fatal(results[0].Skipped)
	}
	if strings.Join(results[0].Warnings, ",") != "guide/app.yaml:疑似明文口令或密钥" {
		t.Fatal(results[0].Warnings)
	}
}

func multiSource(t *testing.T, base string) (top, mid, iot string) {
	mid = filepath.Join(base, "repo", "mid-system", "docs")
	iot = filepath.Join(base, "repo", "iot-system", "docs")
	top = filepath.Join(base, "repo", "docs")
	mustWrite(t, filepath.Join(top, "README.md"), "# 项目索引")
	mustWrite(t, filepath.Join(mid, "README.md"), "# 子系统文档")
	mustWrite(t, filepath.Join(mid, "guide.md"), "# 子系统指南")
	mustWrite(t, filepath.Join(iot, "README.md"), "# 物联网文档")
	return
}

func rootsOf(pairs ...string) []any {
	var out []any
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ojson.NewObject("prefix", pairs[i], "path", pairs[i+1]))
	}
	return out
}

func multiProject(top, mid, iot string, overrides ...any) *ojson.Object {
	value := ojson.NewObject("id", "demo", "name", "多目录项目",
		"roots", rootsOf("", top, "mid-system", mid, "iot-system", iot),
		"home", "README.md", "exclude", []any{})
	for i := 0; i+1 < len(overrides); i += 2 {
		value.Set(overrides[i].(string), overrides[i+1])
	}
	return value
}

func TestOneProjectCanPublishSeveralDirectories(t *testing.T) {
	base := tempDir(t)
	top, mid, iot := multiSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{multiProject(top, mid, iot)})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(output, "projects", "demo", "raw")
	if results[0].FileCount != 4 || readText(t, filepath.Join(raw, "README.md")) != "# 项目索引" ||
		readText(t, filepath.Join(raw, "mid-system", "README.md")) != "# 子系统文档" ||
		readText(t, filepath.Join(raw, "iot-system", "README.md")) != "# 物联网文档" {
		t.Fatal("multi roots")
	}
	sidebarText := readText(t, filepath.Join(output, "projects", "demo", "content", "_sidebar.md"))
	if !strings.Contains(sidebarText, "- mid-system") || !strings.Contains(sidebarText, "- iot-system") {
		t.Fatal(sidebarText)
	}
	scope := readText(t, filepath.Join(output, "projects", "demo", "content", "__scope.md"))
	if !strings.Contains(scope, filepath.ToSlash(mid)) || !strings.Contains(scope, filepath.ToSlash(iot)) {
		t.Fatal(scope)
	}
}

func TestExcludeAndHomeWorkOnTheCombinedPaths(t *testing.T) {
	base := tempDir(t)
	top, mid, iot := multiSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{multiProject(top, mid, iot, "home", "mid-system/README.md", "exclude", strs("iot-system/"))})
	results, err := Refresh(cfg, output, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(output, "projects", "demo", "raw", "iot-system")) || !hasItem(results[0].Skipped, "iot-system/:exclude") {
		t.Fatal("prefix exclude")
	}
	if loadJSON(t, filepath.Join(output, "projects", "demo", "manifest.json")).Value("home") != "mid-system/README.md" {
		t.Fatal("home")
	}
}

func TestMultiDirectoryConfigsAreValidated(t *testing.T) {
	base := tempDir(t)
	top, mid, iot := multiSource(t, base)
	_, cfg, output := makeTool(t, base)
	fails := func(p *ojson.Object, match string) {
		t.Helper()
		writeConfig(t, cfg, []any{p})
		_, err := Refresh(cfg, output, "", "")
		expectConfigError(t, err, match)
	}
	fails(multiProject(top, mid, iot, "roots", rootsOf("a", mid, "a", iot)), "重复")
	fails(multiProject(top, mid, iot, "roots", rootsOf("a", mid, "a/b", iot)), "嵌套")
	fails(multiProject(top, mid, iot, "roots", rootsOf("", filepath.Dir(top), "m", mid)), "嵌套")
	fails(multiProject(top, mid, iot, "roots", rootsOf("../逃逸", mid)), "prefix")
	fails(multiProject(top, mid, iot, "home", "iot-system/missing.md"), "home")
	p := multiProject(top, mid, iot)
	p.Set("docsPath", top)
	fails(p, "同时使用")
}

func TestSingleDirectoryConfigStillWorks(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	results, err := Refresh(cfg, output, "", "")
	if err != nil || results[0].FileCount != 2 || !exists(filepath.Join(output, "projects", "demo", "raw", "README.md")) {
		t.Fatal(err)
	}
}

func TestUpdateProjectCanAddDirectories(t *testing.T) {
	base := tempDir(t)
	top, mid, iot := multiSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{multiProject(top, mid, iot, "roots", rootsOf("", top))})
	Refresh(cfg, output, "", "")
	if exists(filepath.Join(output, "projects", "demo", "raw", "mid-system")) {
		t.Fatal("unexpected")
	}
	if _, err := UpdateProject(cfg, output, "demo", ProjectUpdate{Roots: rootsOf("", top, "mid-system", mid)}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(output, "projects", "demo", "raw", "mid-system", "guide.md")) {
		t.Fatal("new root not published")
	}
	saved := config.Projects(loadJSON(t, cfg))[0].(*ojson.Object)
	if saved.Has("docsPath") || ojson.Dumps(saved.Value("roots"), -1) != ojson.Dumps(rootsOf("", filepath.ToSlash(top), "mid-system", filepath.ToSlash(mid)), -1) {
		t.Fatal(ojson.Dumps(saved, -1))
	}
}

func TestRegisterRejectsRootContainingGeneratedSite(t *testing.T) {
	base := tempDir(t)
	_, cfg, output := makeTool(t, base)
	tool := filepath.Join(base, "tool")
	mustWrite(t, filepath.Join(tool, "README.md"), "# tool")
	err := RegisterProject(cfg, output, project(tool, "id", "tool"))
	expectConfigError(t, err, filepath.ToSlash(tool), "docs/")
}

// ---- 按目录接入（页面上的「接入项目」）

func TestPathRegistrationExportsRootAndPreservesSource(t *testing.T) {
	base := tempDir(t)
	_, cfg, output := makeTool(t, base)
	source := setupSource(t, filepath.Join(base, "source"))
	mustWrite(t, filepath.Join(source, "extra.md"), "# Extra")
	mustWrite(t, filepath.Join(source, ".private.md"), "private")
	before := readText(t, filepath.Join(source, "README.md"))
	results, err := RegisterPath(cfg, output, source)
	if err != nil {
		t.Fatal(err)
	}
	saved := config.Projects(loadJSON(t, cfg))[0].(*ojson.Object)
	id := saved.Value("id").(string)
	if saved.Has("include") || saved.Value("home") != "README.md" || !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(id) {
		t.Fatal(ojson.Dumps(saved, -1))
	}
	if results[0].FileCount != 3 || !exists(filepath.Join(output, "projects", id, "content", "extra.md")) ||
		exists(filepath.Join(output, "projects", id, "raw", ".private.md")) {
		t.Fatal("register path export")
	}
	if readText(t, filepath.Join(source, "README.md")) != before {
		t.Fatal("source changed")
	}
}

func TestTwoDocsDirectoriesHaveDistinctStableIDs(t *testing.T) {
	base := tempDir(t)
	_, cfg, output := makeTool(t, base)
	a := setupSource(t, filepath.Join(base, "a"))
	b := setupSource(t, filepath.Join(base, "b"))
	if _, err := RegisterPath(cfg, output, a); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterPath(cfg, output, b); err != nil {
		t.Fatal(err)
	}
	projects := config.Projects(loadJSON(t, cfg))
	if len(projects) != 2 || projects[0].(*ojson.Object).Value("id") == projects[1].(*ojson.Object).Value("id") {
		t.Fatal("ids")
	}
	before := readText(t, cfg)
	_, err := RegisterPath(cfg, output, a)
	expectConfigError(t, err)
	if readText(t, cfg) != before {
		t.Fatal("config changed")
	}
}

func TestUnicodeDirectoryAndFallbackHome(t *testing.T) {
	base := tempDir(t)
	_, cfg, output := makeTool(t, base)
	source := filepath.Join(base, "中文文档")
	mustWrite(t, filepath.Join(source, "指南.md"), "# 指南")
	if _, err := RegisterPath(cfg, output, source); err != nil {
		t.Fatal(err)
	}
	saved := config.Projects(loadJSON(t, cfg))[0].(*ojson.Object)
	if saved.Value("name") != "中文文档" || saved.Value("home") != "指南.md" ||
		!regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(saved.Value("id").(string)) {
		t.Fatal(ojson.Dumps(saved, -1))
	}
}

func TestFailedBuildDoesNotRegisterOrChangeExistingSite(t *testing.T) {
	base := tempDir(t)
	tool, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, nil)
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	beforeConfig := readText(t, cfg)
	beforeSite := siteSnapshot(t, output)
	mustWrite(t, filepath.Join(tool, "vendor", "docsify.min.js"), "tampered")
	source := setupSource(t, filepath.Join(base, "source"))
	_, err := RegisterPath(cfg, output, source)
	expectConfigError(t, err)
	if readText(t, cfg) != beforeConfig {
		t.Fatal("config changed")
	}
	after, _ := json.Marshal(siteSnapshot(t, output))
	want, _ := json.Marshal(beforeSite)
	if string(after) != string(want) {
		t.Fatal("site changed")
	}
}

func TestPathRegistrationRejectsUnsafePaths(t *testing.T) {
	for _, value := range []string{"relative/docs", "/", "https://example.com/docs", "/tmp/../", ""} {
		base := tempDir(t)
		_, cfg, output := makeTool(t, base)
		_, err := RegisterPath(cfg, output, value)
		expectConfigError(t, err)
		if exists(cfg) {
			t.Fatal("config created for " + value)
		}
	}
}

func TestNoMarkdownOrSymlinkRootIsRejected(t *testing.T) {
	base := tempDir(t)
	_, cfg, output := makeTool(t, base)
	empty := filepath.Join(base, "empty")
	mustWrite(t, filepath.Join(empty, ".hidden.md"), "# Hidden")
	_, err := RegisterPath(cfg, output, empty)
	expectConfigError(t, err)
	real := setupSource(t, filepath.Join(base, "real"))
	link := filepath.Join(base, "linked")
	os.Symlink(real, link)
	_, err = RegisterPath(cfg, output, link)
	expectConfigError(t, err)
}

// ---- 额外：托管标记与原子替换

func TestRefusesToManageUnmarkedOutput(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	mustWrite(t, filepath.Join(output, "someone-elses.txt"), "keep me")
	_, err := Refresh(cfg, output, "", "")
	expectConfigError(t, err, "标记")
	if readText(t, filepath.Join(output, "someone-elses.txt")) != "keep me" {
		t.Fatal("foreign file touched")
	}
	entries, _ := os.ReadDir(filepath.Dir(output))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".site-stage-") {
			t.Fatal("stage dir left behind")
		}
	}
}

func typedSource(t *testing.T, base string) string {
	t.Helper()
	source := setupSource(t, base)
	mustWrite(t, filepath.Join(source, "page.html"), "<h1>页面</h1>")
	mustWrite(t, filepath.Join(source, "img", "a.png"), "png")
	mustWrite(t, filepath.Join(source, "img", "b.PNG"), "png")
	mustWrite(t, filepath.Join(source, "data.json"), "{}")
	mustWrite(t, filepath.Join(source, "Makefile"), "all:\n")
	mustWrite(t, filepath.Join(source, "drafts", "draft.md"), "# 草稿")
	mustWrite(t, filepath.Join(source, "old.html"), "<p>old</p>")
	mustWrite(t, filepath.Join(source, ".hidden", "x.md"), "# 隐藏")
	mustWrite(t, filepath.Join(source, "deploy.env"), "A=1")
	mustWrite(t, filepath.Join(source, "key.md"), "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")
	return source
}

func TestTypesLimitCandidatesAfterExcludeAndPolicy(t *testing.T) {
	base := tempDir(t)
	source := typedSource(t, base)
	p := project(source, "exclude", strs("drafts/", "old.html", "nothing/"), "types", strs(".md", ".HTML"))
	candidates, skipped, _, typeSkipped := IterCandidates(p, []config.Root{{Prefix: "", Path: source}})
	var rels []string
	for _, c := range candidates {
		rels = append(rels, c.Rel)
	}
	if strings.Join(rels, ",") != "README.md,guide/使用 说明.md,page.html" {
		t.Fatal(rels)
	}
	if typeSkipped[".png"] != 2 || typeSkipped[".json"] != 1 || typeSkipped["(无扩展名)"] != 1 || len(typeSkipped) != 3 {
		t.Fatal(typeSkipped)
	}
	for _, want := range []string{"drafts/:exclude", "old.html:exclude", ".hidden/:隐藏路径", "deploy.env:凭据或环境文件", "key.md:文件内含私钥"} {
		if !hasItem(skipped, want) {
			t.Fatalf("missing %s in %v", want, skipped)
		}
	}
	for _, item := range skipped {
		if strings.HasSuffix(item, ".png") || strings.Contains(item, "data.json") {
			t.Fatalf("类型跳过的文件不应逐条列出: %v", skipped)
		}
	}
	if got := unusedExcludes(config.Excludes(p), skipped); strings.Join(got, ",") != "nothing/" {
		t.Fatal(got)
	}
	// 不设 types 时照旧全部收录，也没有类型计数。
	all, _, _, none := IterCandidates(project(source), []config.Root{{Prefix: "", Path: source}})
	if len(none) != 0 || len(all) <= len(candidates) {
		t.Fatal(len(all), none)
	}
}

func TestTypesRefreshAndScopePage(t *testing.T) {
	base := tempDir(t)
	source := typedSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source, "types", strs(".md", ".html"))})
	if _, err := Refresh(cfg, output, "", ""); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(output, "projects", "demo")
	sidebar := readText(t, filepath.Join(site, "content", "_sidebar.md"))
	if strings.Contains(sidebar, "png") || strings.Contains(sidebar, "json") || !strings.Contains(sidebar, "page.html") {
		t.Fatal(sidebar)
	}
	if exists(filepath.Join(site, "raw", "img", "a.png")) || exists(filepath.Join(site, "raw", "data.json")) {
		t.Fatal("类型之外的文件不应导出")
	}
	scope := readText(t, filepath.Join(site, "content", "__scope.md"))
	for _, want := range []string{"只收录 .html、.md", "## 因类型未收录", "(无扩展名) 1、.json 1、.png 2"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope page missing %q:\n%s", want, scope)
		}
	}
	// 去掉 types 后恢复全部收录，范围页不再出现类型一节。
	if _, err := UpdateProject(cfg, output, "demo", ProjectUpdate{Types: []any{}}); err != nil {
		t.Fatal(err)
	}
	if config.Projects(loadJSON(t, cfg))[0].(*ojson.Object).Has("types") {
		t.Fatal("空数组应去掉 types 字段")
	}
	if !exists(filepath.Join(site, "raw", "img", "a.png")) {
		t.Fatal("去掉 types 后应恢复收录")
	}
	if scope := readText(t, filepath.Join(site, "content", "__scope.md")); strings.Contains(scope, "因类型未收录") || strings.Contains(scope, "只收录") {
		t.Fatal(scope)
	}
}

func TestUpdateProjectTypesNormalizesAndRejects(t *testing.T) {
	base := tempDir(t)
	source := setupSource(t, base)
	_, cfg, output := makeTool(t, base)
	writeConfig(t, cfg, []any{project(source)})
	Refresh(cfg, output, "", "")
	if _, err := UpdateProject(cfg, output, "demo", ProjectUpdate{Types: strs("HTML", ".MD", ".md")}); err != nil {
		t.Fatal(err)
	}
	saved := config.Projects(loadJSON(t, cfg))[0].(*ojson.Object)
	if ojson.Dumps(saved.Value("types"), -1) != `[".html", ".md"]` {
		t.Fatal(ojson.Dumps(saved, -1))
	}
	_, err := UpdateProject(cfg, output, "demo", ProjectUpdate{Types: strs(".md", ".exe")})
	expectConfigError(t, err, "types只能包含支持的扩展名")
	_, err = UpdateProject(cfg, output, "demo", ProjectUpdate{Types: strs(".html")})
	expectConfigError(t, err, "types必须包含首页的扩展名")
	_, err = UpdateProject(cfg, output, "demo", ProjectUpdate{Types: ".md"})
	expectConfigError(t, err, "types必须是扩展名数组")
	if ojson.Dumps(config.Projects(loadJSON(t, cfg))[0].(*ojson.Object).Value("types"), -1) != `[".html", ".md"]` {
		t.Fatal("被拒绝的修改不应落盘")
	}
}
