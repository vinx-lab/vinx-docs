package build

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// toolRootOf 由配置文件路径（<家目录>/config/projects.json）得到家目录。
func toolRootOf(configPath string) string { return textutil.Parent(textutil.Parent(configPath)) }

// Refresh 加配置锁、读取配置并重建站点。
// output 为空时用 <工具根>/.runtime/site；toolRoot 为空时用配置文件的上两级；vendorRoot 为空时用资源根的 vendor/。
func Refresh(configPath, output, toolRoot, vendorRoot string) (results []*ProjectResult, err error) {
	defer guard(&err)
	inferred := toolRootOf(configPath)
	site := output
	if site == "" {
		site = config.SitePath(inferred)
	}
	root := toolRoot
	if root == "" {
		root = inferred
	}
	unlock, err := config.Lock(configPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return buildSite(cfg, site, root, vendorRoot)
}

// publishCandidate 先完整构建，成功后才写配置；发布失败时回滚。
func publishCandidate(configPath, output string, previous, candidate *ojson.Object) ([]*ProjectResult, error) {
	if _, err := config.ValidateAll(candidate); err != nil {
		return nil, err
	}
	cache := loadCache(output)
	prepared, cleanup, err := prepareSite(candidate, output, toolRootOf(configPath), "", cache)
	if err != nil {
		return nil, err
	}
	existed := textutil.Exists(configPath)
	if err := config.AtomicWriteJSON(configPath, candidate); err != nil {
		cleanup()
		return nil, err
	}
	if err := syncOwned(prepared.stage, output, keptSet(prepared.results)); err != nil {
		if existed {
			_ = config.AtomicWriteJSON(configPath, previous)
		} else {
			_ = os.Remove(configPath)
		}
		cleanup()
		return nil, err
	}
	cleanup()
	return finish(output, cache, prepared.results)
}

func withProjects(cfg *ojson.Object, projects []any) *ojson.Object {
	candidate := cfg.Copy()
	candidate.Set("projects", projects)
	return candidate
}

// RegisterProject 校验并原子加入项目（不构建）；已有 ID 和重叠根都拒绝。
func RegisterProject(configPath, output string, project *ojson.Object) (err error) {
	defer guard(&err)
	unlock, err := config.Lock(configPath)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	newRoots, err := config.ValidateProject(project, config.ProtectedPorts(cfg))
	if err != nil {
		return err
	}
	for _, item := range config.Projects(cfg) {
		if obj, ok := item.(*ojson.Object); ok && obj.Value("id") == project.Value("id") {
			return config.Errorf("项目ID已存在，拒绝覆盖")
		}
	}
	var roots []string
	for _, item := range config.Projects(cfg) {
		itemRoots, err := config.ValidateProject(item, config.ProtectedPorts(cfg))
		if err != nil {
			return err
		}
		for _, root := range itemRoots {
			roots = append(roots, root.Path)
		}
	}
	var fresh []string
	for _, root := range newRoots {
		fresh = append(fresh, root.Path)
	}
	for _, a := range fresh {
		for _, b := range roots {
			if textutil.IsWithin(a, b) || textutil.IsWithin(b, a) {
				return config.Errorf("项目文档目录重叠，拒绝注册")
			}
		}
	}
	if _, err := config.ValidateOutput(output, append(roots, fresh...), toolRootOf(configPath)); err != nil {
		return err
	}
	cfg.Set("projects", append(config.Projects(cfg), project))
	return config.AtomicWriteJSON(configPath, cfg)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// RegisterPath 页面上「接入项目」输入一个目录，自动生成 id/名称/首页并构建。
func RegisterPath(configPath, output, docsPath string) (results []*ProjectResult, err error) {
	defer guard(&err)
	if textutil.Strip(docsPath) == "" || hasControl(docsPath) {
		return nil, config.Errorf("请输入文档目录的绝对路径")
	}
	path, err := textutil.ExpandUser(textutil.Strip(docsPath))
	if err != nil {
		return nil, err
	}
	if !textutil.IsAbs(path) || contains(textutil.Parts(path), "..") {
		return nil, config.Errorf("文档目录必须是绝对路径，不能包含..")
	}
	root, err := config.CheckedDir(path, "docsPath")
	if err != nil {
		return nil, err
	}
	slug := strings.Trim(slugRE.ReplaceAllString(textutil.Lower(textutil.Name(root)), "-"), "-")
	slug = textutil.Head(slug, 48)
	if slug == "" {
		slug = "project"
	}
	sum := sha256.Sum256([]byte(root))
	project := ojson.NewObject(
		"id", slug+"-"+hex.EncodeToString(sum[:])[:10],
		"name", textutil.Name(root),
		"docsPath", root,
		"exclude", []any{},
	)
	unlock, err := config.Lock(configPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	roots, err := config.ValidateAll(cfg)
	if err != nil {
		return nil, err
	}
	for _, old := range roots {
		if textutil.IsWithin(root, old) || textutil.IsWithin(old, root) {
			return nil, config.Errorf("该目录已接入或与已接入目录重叠")
		}
	}
	for _, item := range config.Projects(cfg) {
		if item.(*ojson.Object).Value("id") == project.Value("id") {
			return nil, config.Errorf("项目ID冲突，未覆盖已有项目")
		}
	}
	if _, err := config.ValidateOutput(output, append(roots, root), toolRootOf(configPath)); err != nil {
		return nil, err
	}
	candidates, _, _ := IterCandidates(project, []config.Root{{Prefix: "", Path: root}})
	var markdown []string
	for _, candidate := range candidates {
		if config.KindFor(candidate.Src) == "markdown" {
			markdown = append(markdown, candidate.Rel)
		}
	}
	if len(markdown) == 0 {
		return nil, config.Errorf("目录中没有可收录的Markdown文档")
	}
	home := markdown[0]
	if contains(markdown, "README.md") {
		home = "README.md"
	}
	project.Set("home", home)
	projects := append(append([]any(nil), config.Projects(cfg)...), project)
	return publishCandidate(configPath, output, cfg, withProjects(cfg, projects))
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 32 {
			return true
		}
	}
	return false
}

// ProjectUpdate 是后台对单个项目的修改；字段为 nil 表示不修改。
// 字段类型用 any，因为 HTTP 层直接传入请求 JSON 里的值，类型错误由这里统一给出报错文案。
type ProjectUpdate struct {
	Name    any
	Exclude any
	Home    any
	Roots   any
}

// UpdateProject 修改显示名、文档目录、排除规则或首页；源文档不受影响。
func UpdateProject(configPath, output, projectID string, update ProjectUpdate) (results []*ProjectResult, err error) {
	defer guard(&err)
	unlock, err := config.Lock(configPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	var projects []any
	var target *ojson.Object
	for _, item := range config.Projects(cfg) {
		obj, ok := item.(*ojson.Object)
		if !ok {
			return nil, &config.PanicError{Msg: "TypeError: project is not an object"}
		}
		copied := obj.Copy()
		projects = append(projects, copied)
		if target == nil && copied.Value("id") == projectID {
			target = copied
		}
	}
	if target == nil {
		return nil, config.Errorf("项目ID不存在")
	}
	if update.Name != nil {
		name, ok := update.Name.(string)
		if !ok || textutil.Strip(name) == "" || textutil.Len(name) > 120 {
			return nil, config.Errorf("项目名称不能为空且不超过120个字符")
		}
		target.Set("name", textutil.Strip(name))
	}
	if update.Exclude != nil {
		list, ok := update.Exclude.([]any)
		if !ok || len(list) > 200 {
			return nil, config.Errorf("排除规则必须是不超过200条的数组")
		}
		cleaned := []any{}
		for _, item := range list {
			if text, ok := item.(string); ok && textutil.Strip(text) != "" {
				cleaned = append(cleaned, textutil.Strip(text))
			}
		}
		for _, item := range cleaned {
			text := item.(string)
			if strings.HasPrefix(text, "/") || textutil.Len(text) > 300 {
				return nil, config.Errorf("排除规则必须是文档根内的相对模式")
			}
			if _, err := config.RejectDotdot(strings.ReplaceAll(strings.TrimRight(text, "/"), "*", "x"), "exclude"); err != nil {
				return nil, err
			}
		}
		target.Set("exclude", cleaned)
	}
	if update.Roots != nil {
		list, ok := update.Roots.([]any)
		if !ok || len(list) == 0 || len(list) > 50 {
			return nil, config.Errorf("文档目录必须是1到50条的数组")
		}
		target.Delete("docsPath")
		roots := []any{}
		for _, item := range list {
			obj, ok := item.(*ojson.Object)
			if !ok {
				continue
			}
			prefix, err := config.ValidatePrefix(obj.Value("prefix"))
			if err != nil {
				return nil, err
			}
			path, err := config.CheckedDir(obj.Value("path"), "roots[].path")
			if err != nil {
				return nil, err
			}
			roots = append(roots, ojson.NewObject("prefix", prefix, "path", path))
		}
		target.Set("roots", roots)
	}
	if update.Home != nil {
		target.Set("home", update.Home)
	}
	return publishCandidate(configPath, output, cfg, withProjects(cfg, projects))
}

// SaveSettings 保存自动同步等运行设置；不触发重建。
func SaveSettings(configPath string, settings *ojson.Object) (*ojson.Object, error) {
	unlock, err := config.Lock(configPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	merged := ojson.NewObject()
	if current, ok := cfg.Value("settings").(*ojson.Object); ok {
		merged = current.Copy()
	}
	for _, key := range settings.Keys() {
		merged.Set(key, settings.Value(key))
	}
	validated, err := config.ValidateSettings(merged)
	if err != nil {
		return nil, err
	}
	out := cfg.Copy()
	out.Set("settings", validated)
	if err := config.AtomicWriteJSON(configPath, out); err != nil {
		return nil, err
	}
	return validated, nil
}

// Unregister 取消注册只删除本工具生成的副本，原项目文档不动。
func Unregister(configPath, output, projectID string) (err error) {
	defer guard(&err)
	unlock, err := config.Lock(configPath)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	var remaining []any
	all := config.Projects(cfg)
	for _, item := range all {
		obj, ok := item.(*ojson.Object)
		if !ok {
			return &config.PanicError{Msg: "AttributeError: project is not an object"}
		}
		if obj.Value("id") != projectID {
			remaining = append(remaining, item)
		}
	}
	if len(remaining) == len(all) {
		return config.Errorf("项目ID不存在")
	}
	if remaining == nil {
		remaining = []any{}
	}
	cache := loadCache(output)
	cache.Delete(projectID)
	if err := config.AtomicWriteJSON(cachePath(output), ojson.NewObject("version", 2, "projects", cache)); err != nil {
		return err
	}
	_, err = publishCandidate(configPath, output, cfg, withProjects(cfg, remaining))
	return err
}

// Validate 是 validate 命令的实现：校验配置和输出目录，不写任何文件。
func Validate(configPath, output string) (err error) {
	defer guard(&err)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if _, err := config.ValidateAll(cfg); err != nil {
		return err
	}
	var roots []string
	for _, item := range config.Projects(cfg) {
		itemRoots, err := config.ProjectRoots(item.(*ojson.Object))
		if err != nil {
			return err
		}
		for _, root := range itemRoots {
			roots = append(roots, root.Path)
		}
	}
	_, err = config.ValidateOutput(output, roots, toolRootOf(configPath))
	return err
}
