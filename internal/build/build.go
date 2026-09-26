// Package build 负责项目登记与站点构建：把登记的文档目录只读导出成静态站点。
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Entry 是 manifest.json 里的一条文件记录。
type Entry struct {
	Path       string
	Kind       string
	Route      *string // nil 输出为 null
	URL        string
	Title      string
	UpdatedAt  int64
	PreviewURL string // 空表示没有该字段
}

// Object 按固定的键顺序输出（前端和已有的 report.json 依赖这个形状）。
func (e Entry) Object() *ojson.Object {
	var route any
	if e.Route != nil {
		route = *e.Route
	}
	o := ojson.NewObject("path", e.Path, "kind", e.Kind, "route", route, "url", e.URL,
		"title", e.Title, "updatedAt", e.UpdatedAt)
	if e.PreviewURL != "" {
		o.Set("previewUrl", e.PreviewURL)
	}
	return o
}

// ProjectResult 是一次构建里单个项目的报告。
type ProjectResult struct {
	ID            string
	Name          string
	URL           string
	FileCount     int
	UpdatedAt     int64
	Reused        int
	Skipped       []string
	Warnings      []string
	UnusedExclude []string

	entries []Entry
	kept    []string
}

// Object 输出 refresh 返回值里的一项；withURL=false 时对应 report.json 里的一项。
func (r *ProjectResult) Object(withURL bool) *ojson.Object {
	o := ojson.NewObject("id", r.ID, "name", r.Name)
	if withURL {
		o.Set("url", r.URL)
	}
	o.Set("fileCount", r.FileCount)
	o.Set("updatedAt", r.UpdatedAt)
	o.Set("reused", r.Reused)
	o.Set("skipped", r.Skipped)
	o.Set("warnings", r.Warnings)
	o.Set("unusedExclude", r.UnusedExclude)
	return o
}

// ResultsJSON 把结果列表转成 refresh 命令和同步接口输出的 JSON 形状。
func ResultsJSON(results []*ProjectResult) []any {
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r.Object(true)
	}
	return out
}

func safeRel(rel string) string {
	parts := textutil.Parts(rel)
	if len(parts) > 0 && (config.ReservedFirstComponents[parts[0]] || config.ReservedNames[textutil.Name(rel)] || config.ReservedNames[rel]) {
		return textutil.Join("__source__", rel)
	}
	return rel
}

func encoded(rel string) string { return textutil.Quote(rel, "/") }

func writeText(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o666)
}

// fileMeta 取文件的纳秒修改时间、大小和整数秒修改时间（增量缓存用）。
type fileMeta struct {
	mtimeNS int64
	size    int64
	mtime   int64
}

func statMeta(path string) (fileMeta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileMeta{}, err
	}
	mod := info.ModTime()
	sec, nsec := mod.Unix(), int64(mod.Nanosecond())
	// 整数秒按 float64(sec + nsec*1e-9) 截断得到，和增量缓存里已有的值用同一种算法，避免临界值不一致。
	floatMtime := float64(sec) + float64(nsec)*1e-9
	return fileMeta{mtimeNS: sec*1_000_000_000 + nsec, size: info.Size(), mtime: int64(floatMtime)}, nil
}

func numberEquals(value any, n int64) bool { return textutil.EqualsInt(value, n) }

// writeProject 把一个项目的收录文件写进站点目录，能复用的文件直接沿用上次的副本。
func writeProject(stage string, project *ojson.Object, roots []config.Root, cache *ojson.Object, output string) (*ProjectResult, error) {
	projectID := project.Value("id").(string)
	name := project.Value("name").(string)
	base := filepath.Join(stage, "projects", projectID)
	for _, dir := range []string{"content", "raw"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o777); err != nil {
			return nil, err
		}
	}
	var entries []Entry
	var versionParts, kept []string
	reused := 0
	known, _ := cache.Value(projectID).(*ojson.Object)
	fresh := ojson.NewObject()
	candidates, skipped, warnings := IterCandidates(project, roots)
	for _, candidate := range candidates {
		rel, src := candidate.Rel, candidate.Src
		kind := config.KindFor(src)
		safe := safeRel(rel)
		text := rel
		meta, err := statMeta(src)
		if err != nil {
			return nil, err
		}
		produced := []string{"projects/" + projectID + "/raw/" + text}
		previewRel := ""
		switch kind {
		case "markdown":
			produced = append(produced, "projects/"+projectID+"/content/"+safe)
		case "text":
			parts := textutil.Parts(rel)
			if len(parts) > 0 && config.ReservedFirstComponents[parts[0]] {
				previewRel = textutil.Join("__previews", "__source__", rel)
			} else {
				previewRel = textutil.Join("__previews", rel)
			}
			produced = append(produced, "projects/"+projectID+"/content/"+previewRel+".md")
		case "html":
			produced = append(produced, "projects/"+projectID+"/preview/"+text)
		}
		cached, _ := known.Value(text).(*ojson.Object)
		usable := cached != nil &&
			numberEquals(cached.Value("mtime"), meta.mtimeNS) &&
			numberEquals(cached.Value("size"), meta.size) &&
			cached.Value("kind") == kind
		if usable {
			for _, item := range produced {
				if !textutil.IsFile(filepath.Join(output, item)) {
					usable = false
					break
				}
			}
		}
		var digest, title, terms string
		if usable {
			d, ok1 := cached.Value("sha256").(string)
			t, ok2 := cached.Value("title").(string)
			if !ok1 || !ok2 {
				usable = false
			} else {
				digest, title = d, t
				terms, _ = cached.Value("terms").(string)
			}
		}
		if usable {
			kept = append(kept, produced...)
			reused++
		} else {
			data, err := os.ReadFile(src)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
			title = Title(data, textutil.Name(rel), kind)
			terms = SearchTerms(data, kind)
			rawDst := filepath.Join(base, "raw", filepath.FromSlash(text))
			if err := writeBytes(rawDst, data); err != nil {
				return nil, err
			}
			switch kind {
			case "markdown":
				if err := writeBytes(filepath.Join(base, "content", filepath.FromSlash(safe)), data); err != nil {
					return nil, err
				}
			case "text":
				preview, ok := PreviewText(data, textutil.Name(rel))
				if !ok {
					skipped = append(skipped, rel+":不是UTF-8文本")
					_ = os.Remove(rawDst)
					continue
				}
				if err := writeText(filepath.Join(base, "content", filepath.FromSlash(previewRel+".md")), preview); err != nil {
					return nil, err
				}
			case "html":
				if err := writeBytes(filepath.Join(base, "preview", filepath.FromSlash(text)), data); err != nil {
					return nil, err
				}
			}
		}
		versionParts = append(versionParts, text+":"+digest)
		fresh.Set(text, ojson.NewObject(
			"mtime", meta.mtimeNS, "size", meta.size, "kind", kind,
			"sha256", digest, "title", title, "terms", terms,
		))
		entry := Entry{
			Path:      text,
			Kind:      kind,
			URL:       "/projects/" + projectID + "/raw/" + encoded(rel),
			Title:     title,
			UpdatedAt: meta.mtime,
		}
		switch {
		case kind == "markdown":
			route := safe
			entry.Route = &route
		case kind == "text":
			route := previewRel + ".md"
			entry.Route = &route
		case kind == "html":
			entry.PreviewURL = "/projects/" + projectID + "/preview/" + encoded(rel)
		case kind == "download" && textutil.Lower(textutil.Suffix(rel)) == ".xlsx":
			entry.PreviewURL = "/projects/" + projectID + "/sheet.html?f=" + textutil.Quote(text, "")
		}
		entries = append(entries, entry)
	}

	home, _ := project.Value("home").(string)
	found := false
	for _, entry := range entries {
		if entry.Path == home {
			found = true
			break
		}
	}
	if !found {
		return nil, config.Errorf("home未被允许或被安全策略排除")
	}
	cache.Set(projectID, fresh)
	versionSum := sha256.Sum256([]byte(strings.Join(versionParts, "\n")))
	version := hex.EncodeToString(versionSum[:])[:16]
	var updatedAt int64
	for i, entry := range entries {
		if i == 0 || entry.UpdatedAt > updatedAt {
			updatedAt = entry.UpdatedAt
		}
	}
	entryObjects := make([]any, len(entries))
	for i, entry := range entries {
		entryObjects[i] = entry.Object()
	}
	manifest := ojson.NewObject(
		"id", projectID,
		"name", name,
		"home", project.Value("home"),
		"version", version,
		"updatedAt", updatedAt,
		"scopeRoute", "__scope.md",
		"entries", entryObjects,
	)
	if err := writeText(filepath.Join(base, "manifest.json"), ojson.Dumps(manifest, 2)+"\n"); err != nil {
		return nil, err
	}
	if err := writeText(filepath.Join(base, "content", "__scope.md"), scopePage(project, roots, len(entries), skipped, warnings)); err != nil {
		return nil, err
	}
	if err := writeText(filepath.Join(base, "content", "_sidebar.md"), sidebar(entries)); err != nil {
		return nil, err
	}
	if skipped == nil {
		skipped = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return &ProjectResult{
		ID:            projectID,
		Name:          name,
		URL:           "/projects/" + projectID + "/",
		FileCount:     len(entries),
		UpdatedAt:     updatedAt,
		Reused:        reused,
		Skipped:       skipped,
		Warnings:      warnings,
		UnusedExclude: unusedExcludes(config.Excludes(project), skipped),
		entries:       entries,
		kept:          kept,
	}, nil
}

func writeBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o666)
}

func sidebar(entries []Entry) string {
	lines := []string{"- [本项目首页](#/)", "- [本项目收录范围](#/__scope.md)"}
	groups := map[string][]Entry{}
	for _, entry := range entries {
		parent := textutil.Parent(entry.Path)
		if parent == "." {
			parent = ""
		}
		groups[parent] = append(groups[parent], entry)
	}
	target := func(entry Entry) string {
		if entry.Kind == "markdown" || entry.Kind == "text" {
			return "#/" + *entry.Route
		}
		if entry.PreviewURL != "" {
			return entry.PreviewURL
		}
		return entry.URL
	}
	for _, entry := range groups[""] {
		lines = append(lines, fmt.Sprintf("- [%s](%s)", entry.Title, target(entry)))
	}
	var dirs []string
	for key := range groups {
		if key != "" {
			dirs = append(dirs, key)
		}
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		lines = append(lines, "- "+dir)
		for _, entry := range groups[dir] {
			lines = append(lines, fmt.Sprintf("  - [%s](%s)", entry.Title, target(entry)))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func rsplitColon(item string) (string, string) {
	i := strings.LastIndex(item, ":")
	if i < 0 {
		return item, ""
	}
	return item[:i], item[i+1:]
}

// scopePage 生成“本项目收录了什么”的说明页。
func scopePage(project *ojson.Object, roots []config.Root, entryCount int, skipped, warnings []string) string {
	lines := []string{
		fmt.Sprintf("# %s 的收录范围", project.Value("name").(string)),
		"",
		"- 收录方式：登记的每个文档目录整体收录，逐项排除（黑名单）",
		fmt.Sprintf("- 已收录：%d 个文件，来自 %d 个目录", entryCount, len(roots)),
		fmt.Sprintf("- 已排除：%d 个条目", len(skipped)),
		"",
		"## 文档目录",
		"",
		"| 项目内位置 | 主机上的目录 |",
		"| --- | --- |",
	}
	for _, root := range roots {
		prefix := root.Prefix
		if prefix == "" {
			prefix = "(项目根)"
		}
		lines = append(lines, fmt.Sprintf("| `%s` | `%s` |", prefix, root.Path))
	}
	lines = append(lines, "", "## 排除规则", "")
	excludes := config.Excludes(project)
	if len(excludes) > 0 {
		for _, item := range excludes {
			lines = append(lines, "- `"+item+"`")
		}
	} else {
		lines = append(lines, "本项目没有自定义排除规则，全部排除都来自强制安全规则。")
	}
	lines = append(lines, "", "## 被排除的条目", "")
	if len(skipped) > 0 {
		lines = append(lines, "| 路径 | 原因 |", "| --- | --- |")
		for _, item := range skipped {
			path, reason := rsplitColon(item)
			lines = append(lines, fmt.Sprintf("| `%s` | %s |", path, reason))
		}
	} else {
		lines = append(lines, "没有条目被排除。")
	}
	if len(warnings) > 0 {
		lines = append(lines,
			"", "## 疑似含明文口令的已收录文件", "",
			"这些文件仍然可以被任何能访问本站的设备读取。确认后可以把它们加入排除规则。", "",
			"| 路径 | 判断 |", "| --- | --- |",
		)
		for _, item := range warnings {
			path, reason := rsplitColon(item)
			lines = append(lines, fmt.Sprintf("| `%s` | %s |", path, reason))
		}
	}
	lines = append(lines,
		"", "---", "",
		"强制安全规则不受排除规则影响，也无法在后台关闭：隐藏路径、缓存与仓库目录、符号链接、凭据与SSH文件名、",
		"不支持的文件类型、以及内容中含私钥的文本文件一律不收录。完整说明见工具文档的“收录范围与隐私规则”。", "",
	)
	return strings.Join(lines, "\n") + "\n"
}

// copyStatic 复制首页、后台等外壳页面、assets 和校验过的 vendor。
func copyStatic(stage, toolRoot string, cfg *ojson.Object, vendorRoot string) error {
	assets := AssetsRoot(toolRoot)
	for _, name := range []string{"index.html", "admin.html", "published.html", "comments.html"} {
		if assets.isFile("web/" + name) {
			if err := assets.copyFile("web/"+name, filepath.Join(stage, name)); err != nil {
				return err
			}
		}
	}
	if assets.isDir("web/assets") {
		if err := assets.copyTree("web/assets", filepath.Join(stage, "assets")); err != nil {
			return err
		}
	}
	vendor, root := assets, "vendor"
	if vendorRoot != "" {
		vendor, root = DirAssets(vendorRoot), "."
	}
	if vendor.isDir(root) {
		if err := verifyVendor(vendor, root, cfg); err != nil {
			return err
		}
		if err := vendor.copyTree(root, filepath.Join(stage, "vendor")); err != nil {
			return err
		}
	}
	return nil
}

// preparedSite 在输出目录旁的临时目录里生成完整站点。
type preparedSite struct {
	stage   string
	results []*ProjectResult
}

func prepareSite(cfg *ojson.Object, site, toolRoot, vendorRoot string, cache *ojson.Object) (*preparedSite, func(), error) {
	roots, err := config.ValidateAll(cfg)
	if err != nil {
		return nil, nil, err
	}
	if _, err := config.ValidateOutput(site, roots, toolRoot); err != nil {
		return nil, nil, err
	}
	parent := textutil.Parent(site)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return nil, nil, err
	}
	stage, err := os.MkdirTemp(parent, ".site-stage-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	fail := func(err error) (*preparedSite, func(), error) {
		cleanup()
		return nil, nil, err
	}
	if err := copyStatic(stage, toolRoot, cfg, vendorRoot); err != nil {
		return fail(err)
	}
	var results []*ProjectResult
	shells := AssetsRoot(toolRoot)
	for _, item := range config.Projects(cfg) {
		project := item.(*ojson.Object)
		projectRoots, err := config.ProjectRoots(project)
		if err != nil {
			return fail(err)
		}
		result, err := writeProject(stage, project, projectRoots, cache, site)
		if err != nil {
			return fail(err)
		}
		results = append(results, result)
		id := project.Value("id").(string)
		for _, pair := range [][2]string{{"project.html", "index.html"}, {"sheet.html", "sheet.html"}} {
			if shells.isFile("web/" + pair[0]) {
				if err := shells.copyFile("web/"+pair[0], filepath.Join(stage, "projects", id, pair[1])); err != nil {
					return fail(err)
				}
			}
		}
	}
	settings := config.SettingsOf(cfg)
	now := time.Now()
	type recentItem struct {
		entry  Entry
		result *ProjectResult
	}
	var recent []recentItem
	for _, result := range results {
		for _, entry := range result.entries {
			if entry.Kind == "markdown" || entry.Kind == "text" {
				recent = append(recent, recentItem{entry, result})
			}
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].entry.UpdatedAt > recent[j].entry.UpdatedAt })
	cutoff := float64(now.UnixNano())/1e9 - float64(settings.RecentDays)*86400
	limit := settings.RecentLimit
	var catalog []any
	for _, result := range results {
		// 首页按项目展示最近更新，每个项目各取自己的，更新少的项目不会被别的项目挤掉。
		items := []any{}
		for _, item := range recent {
			if len(items) >= limit {
				break
			}
			if item.result.ID == result.ID && float64(item.entry.UpdatedAt) >= cutoff {
				var route any
				if item.entry.Route != nil {
					route = *item.entry.Route
				}
				items = append(items, ojson.NewObject("path", item.entry.Path, "route", route,
					"title", item.entry.Title, "updatedAt", item.entry.UpdatedAt))
			}
		}
		catalog = append(catalog, ojson.NewObject(
			"id", result.ID, "name", result.Name, "url", result.URL, "fileCount", result.FileCount,
			"updatedAt", result.UpdatedAt, "reused", result.Reused, "recent", items,
		))
	}
	topRecent := []any{}
	for _, item := range recent {
		if len(topRecent) >= limit {
			break
		}
		if float64(item.entry.UpdatedAt) >= cutoff {
			o := item.entry.Object()
			o.Set("project", item.result.ID)
			o.Set("projectName", item.result.Name)
			o.Set("projectUrl", item.result.URL)
			topRecent = append(topRecent, o)
		}
	}
	if catalog == nil {
		catalog = []any{}
	}
	catalogValue := ojson.NewObject(
		"projects", catalog,
		"settings", ojson.NewObject("autoSync", settings.AutoSync),
		"generatedAt", now.Unix(),
		"recent", topRecent,
	)
	if err := writeText(filepath.Join(stage, "catalog.json"), ojson.Dumps(catalogValue, 2)+"\n"); err != nil {
		return fail(err)
	}
	if err := writeText(filepath.Join(stage, "search-index.json"), ojson.Dumps(searchIndex(results, cache), -1)+"\n"); err != nil {
		return fail(err)
	}
	return &preparedSite{stage: stage, results: results}, cleanup, nil
}

func searchIndex(results []*ProjectResult, cache *ojson.Object) *ojson.Object {
	documents := []any{}
	for _, result := range results {
		known, _ := cache.Value(result.ID).(*ojson.Object)
		for _, entry := range result.entries {
			if entry.Kind != "markdown" && entry.Kind != "text" {
				continue
			}
			terms := ""
			if item, ok := known.Value(entry.Path).(*ojson.Object); ok {
				terms, _ = item.Value("terms").(string)
			}
			documents = append(documents, ojson.NewObject(
				"project", result.ID,
				"projectName", result.Name,
				"title", entry.Title,
				"path", entry.Path,
				"url", result.URL+"#/"+*entry.Route,
				"updatedAt", entry.UpdatedAt,
				"text", terms,
			))
		}
	}
	return ojson.NewObject("documents", documents)
}

const markerName = ".docsify-x-managed"

// syncOwned 只删除本工具拥有的陈旧文件，再把新文件原子地换进去。
func syncOwned(stage, output string, kept map[string]bool) error {
	output, err := config.ValidateOutput(output, nil, "")
	if err != nil {
		return err
	}
	if textutil.Exists(output) && !textutil.IsDir(output) {
		return config.Errorf("输出路径不是目录")
	}
	if textutil.IsDir(output) && !textutil.IsSymlink(output) {
		entries, err := os.ReadDir(output)
		if err != nil {
			return err
		}
		marker := filepath.Join(output, markerName)
		if len(entries) > 0 {
			content, err := os.ReadFile(marker)
			if !textutil.IsFile(marker) || err != nil || string(content) != config.Marker {
				return config.Errorf("拒绝管理没有 Vinx Docs 标记的输出目录")
			}
		}
		hasLink := false
		_ = filepath.WalkDir(output, func(p string, d fs.DirEntry, err error) error {
			if err == nil && p != output && d.Type()&fs.ModeSymlink != 0 {
				hasLink = true
				return fs.SkipAll
			}
			return nil
		})
		if hasLink {
			return config.Errorf("输出目录内部不能包含符号链接")
		}
	}
	if err := os.MkdirAll(output, 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, markerName), []byte(config.Marker), 0o666); err != nil {
		return err
	}
	desired := map[string]bool{markerName: true}
	var stageFiles []string
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(stage, p)
			desired[filepath.ToSlash(rel)] = true
			stageFiles = append(stageFiles, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// kept 是本轮确认未变更、直接留在输出目录里的文件；它们不在 stage 里，但不能被当成陈旧文件删掉。
	for item := range kept {
		desired[item] = true
	}
	type oldEntry struct {
		path  string
		depth int
	}
	var olds []oldEntry
	_ = filepath.WalkDir(output, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == output {
			return nil
		}
		rel, _ := filepath.Rel(output, p)
		olds = append(olds, oldEntry{p, strings.Count(filepath.ToSlash(rel), "/") + 1})
		return nil
	})
	sort.SliceStable(olds, func(i, j int) bool { return olds[i].depth > olds[j].depth })
	for _, old := range olds {
		rel, _ := filepath.Rel(output, old.path)
		if textutil.IsFile(old.path) && !desired[filepath.ToSlash(rel)] {
			if err := os.Remove(old.path); err != nil {
				return err
			}
		} else if textutil.IsDir(old.path) && !textutil.IsSymlink(old.path) {
			if entries, err := os.ReadDir(old.path); err == nil && len(entries) == 0 {
				if err := os.Remove(old.path); err != nil {
					return err
				}
			}
		}
	}
	for _, src := range stageFiles {
		rel, _ := filepath.Rel(stage, src)
		dst := filepath.Join(output, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func cachePath(site string) string { return textutil.Join(textutil.Parent(site), "build-cache.json") }

// loadCache 只认 version 2 的缓存。
func loadCache(site string) *ojson.Object {
	data, err := os.ReadFile(cachePath(site))
	if err != nil {
		return ojson.NewObject()
	}
	value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
	if err != nil {
		return ojson.NewObject()
	}
	obj, ok := value.(*ojson.Object)
	if !ok || !numberEquals(obj.Value("version"), 2) {
		return ojson.NewObject()
	}
	projects, present := obj.Get("projects")
	if !present {
		return ojson.NewObject()
	}
	if p, ok := projects.(*ojson.Object); ok {
		return p
	}
	return ojson.NewObject()
}

// finish 写增量缓存和构建报告。
func finish(site string, cache *ojson.Object, results []*ProjectResult) ([]*ProjectResult, error) {
	if err := config.AtomicWriteJSON(cachePath(site), ojson.NewObject("version", 2, "projects", cache)); err != nil {
		return nil, err
	}
	projects := make([]any, len(results))
	for i, result := range results {
		projects[i] = result.Object(false)
	}
	report := ojson.NewObject("completedAt", time.Now().Unix(), "projects", projects)
	if err := config.AtomicWriteJSON(textutil.Join(textutil.Parent(site), "report.json"), report); err != nil {
		return nil, err
	}
	return results, nil
}

func keptSet(results []*ProjectResult) map[string]bool {
	kept := map[string]bool{}
	for _, result := range results {
		for _, item := range result.kept {
			kept[item] = true
		}
	}
	return kept
}

// buildSite 构建并原子发布站点；复用未变更文件，返回每个项目的报告。
func buildSite(cfg *ojson.Object, site, toolRoot, vendorRoot string) ([]*ProjectResult, error) {
	cache := loadCache(site)
	prepared, cleanup, err := prepareSite(cfg, site, toolRoot, vendorRoot, cache)
	if err != nil {
		return nil, err
	}
	err = syncOwned(prepared.stage, site, keptSet(prepared.results))
	cleanup()
	if err != nil {
		return nil, err
	}
	return finish(site, cache, prepared.results)
}

// LoadReport 读取上一次构建报告。
func LoadReport(site string) *ojson.Object {
	empty := ojson.NewObject("completedAt", 0, "projects", []any{})
	data, err := os.ReadFile(textutil.Join(textutil.Parent(site), "report.json"))
	if err != nil {
		return empty
	}
	value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
	if err != nil {
		return empty
	}
	if obj, ok := value.(*ojson.Object); ok {
		return obj
	}
	return empty
}

// guard 把通配匹配里的 panic（空模式、坏字符类）转成普通错误。
func guard(err *error) {
	if r := recover(); r != nil {
		switch v := r.(type) {
		case textutil.EmptyPatternError:
			*err = fmt.Errorf("ValueError: %w", v)
		case textutil.BadPatternError:
			*err = fmt.Errorf("re.error: %w", v)
		default:
			panic(r)
		}
	}
}
