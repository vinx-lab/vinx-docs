// Package files 实现页面内编辑：把「目标」映射回源文件，带版本检查地读写，保存前留历史。
//
// 目标写法：
//
//	doc:<项目ID>/<收录路径>   已注册项目里已收录的文件（必须出现在 manifest 里）
//	a:<短码>/<清单路径>        短链接清单里的文件
package files

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

const (
	MaxBytes    = 2 * 1024 * 1024
	HistoryKeep = 20
)

// EditableSuffixes 是允许在页面上编辑的文件扩展名。
var EditableSuffixes = func() map[string]bool {
	out := map[string]bool{".css": true, ".svg": true, ".txt": true}
	for _, set := range []map[string]bool{config.TextExtensions, config.MarkdownExtensions, config.HTMLExtensions} {
		for k := range set {
			out[k] = true
		}
	}
	return out
}()

// Modes 是各扩展名对应的编辑器语法模式。
var Modes = map[string]string{
	".md": "markdown", ".markdown": "markdown", ".html": "htmlmixed", ".htm": "htmlmixed", ".svg": "xml", ".xml": "xml",
	".css": "css", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".json": "application/json",
}

var targetRE = regexp.MustCompile(`^(doc|a):([A-Za-z0-9_-]+)/(.+)\n?$`)

// Conflict 保存时文件已被别人改过；带上当前内容和版本，让前端做对比。
type Conflict struct {
	Content string
	Version string
}

func (c *Conflict) Error() string { return "文件已被修改" }

// UnicodeError 表示内容不是有效的 UTF-8（服务层映射成 400「内容不是有效的UTF-8文本」）。
type UnicodeError struct{ Msg string }

func (e *UnicodeError) Error() string { return e.Msg }

// Paths 是编辑涉及的几个位置，测试时可以整体换成临时目录。
type Paths struct {
	ToolRoot string
	Config   string
	Site     string
	Registry string
	History  string
}

// NewPaths 根据家目录推导配置、站点和编辑历史的位置。
func NewPaths(toolRoot string) Paths {
	return Paths{
		ToolRoot: toolRoot,
		Config:   config.ConfigPath(toolRoot),
		Site:     config.SitePath(toolRoot),
		Registry: pages.DefaultRegistry(toolRoot),
		History:  textutil.Join(toolRoot, ".runtime", "history"),
	}
}

// ParseTarget 返回 (kind, owner, rel)。
func ParseTarget(target any) (string, string, string, error) {
	text, ok := target.(string)
	if !ok || textutil.Len(text) > 1024 {
		return "", "", "", config.Errorf("目标无效")
	}
	m := targetRE.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", config.Errorf("目标格式应为 doc:<项目>/<路径> 或 a:<短码>/<路径>")
	}
	if _, err := config.RejectDotdot(m[3], "目标路径"); err != nil {
		return "", "", "", err
	}
	return m[1], m[2], m[3], nil
}

// Resolve 返回源文件路径和给人看的位置信息；只认已收录的文件，每次都重新做安全检查。
func Resolve(paths Paths, target any) (string, *ojson.Object, error) {
	kind, owner, rel, err := ParseTarget(target)
	if err != nil {
		return "", nil, err
	}
	targetText := target.(string)
	if kind == "a" {
		registry, err := pages.LoadRegistry(paths.Registry)
		if err != nil {
			return "", nil, err
		}
		record := pages.Find(registry, owner)
		if record == nil || !contains(pages.Files(record), rel) {
			return "", nil, config.Errorf("短链接或文件不存在")
		}
		source, err := pages.CheckFile(pages.Str(record, "root"), rel)
		if err != nil {
			return "", nil, err
		}
		return source, ojson.NewObject("target", targetText, "kind", "artifact", "owner", owner, "path", rel,
			"title", record.Value("title"), "url", "/a/"+owner+"/"+rel, "source", source), nil
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return "", nil, err
	}
	var project *ojson.Object
	for _, item := range config.Projects(cfg) {
		obj, ok := item.(*ojson.Object)
		if !ok {
			return "", nil, &config.PanicError{Msg: "TypeError: project is not an object"}
		}
		if obj.Value("id") == owner {
			project = obj
			break
		}
	}
	if project == nil {
		return "", nil, config.Errorf("项目不存在")
	}
	data, err := os.ReadFile(textutil.Join(paths.Site, "projects", owner, "manifest.json"))
	if err != nil {
		return "", nil, config.Errorf("项目还没有同步过")
	}
	value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
	if err != nil {
		return "", nil, config.Errorf("项目还没有同步过")
	}
	manifest, ok := value.(*ojson.Object)
	if !ok {
		return "", nil, &config.PanicError{Msg: "AttributeError: manifest is not an object"}
	}
	var entry *ojson.Object
	entries, _ := manifest.Value("entries").([]any)
	for _, item := range entries {
		if obj, ok := item.(*ojson.Object); ok && obj.Value("path") == rel {
			entry = obj
			break
		}
	}
	if entry == nil {
		return "", nil, config.Errorf("文件不在收录范围内")
	}
	roots, err := config.ProjectRoots(project)
	if err != nil {
		return "", nil, err
	}
	root, inner, found := config.Locate(roots, rel)
	if !found {
		return "", nil, config.Errorf("文件不在收录范围内")
	}
	source, err := pages.CheckFile(root.Path, inner)
	if err != nil {
		return "", nil, err
	}
	var url any
	if route := entry.Value("route"); textutil.Truthy(route) {
		url = "/projects/" + owner + "/#/" + textutil.Str(route)
	} else if preview := entry.Value("previewUrl"); textutil.Truthy(preview) {
		url = preview
	} else {
		url = "/projects/" + owner + "/"
	}
	title := entry.Value("title")
	if !textutil.Truthy(title) {
		title = rel
	}
	return source, ojson.NewObject("target", targetText, "kind", "doc", "owner", owner, "path", rel, "title", title,
		"projectName", project.Value("name"), "url", url, "source", source), nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func editable(source string) error {
	if !EditableSuffixes[textutil.Lower(textutil.Suffix(source))] {
		return config.Errorf("这种文件不能在页面上编辑")
	}
	st, err := textutil.Stat(source)
	if err != nil {
		return err
	}
	if st.Size > MaxBytes {
		return config.Errorf("文件超过2MB，不能在页面上编辑")
	}
	return nil
}

// VersionOf <mtime_ns>-<sha256 前 16 位>。
func VersionOf(data []byte, mtimeNS int64) string {
	sum := sha256.Sum256(data)
	return strconv.FormatInt(mtimeNS, 10) + "-" + hex.EncodeToString(sum[:])[:16]
}

var bom = []byte("\xef\xbb\xbf")

// decode 返回（去掉 BOM、换行统一成 \n 的文本, 有没有 BOM, 是不是 CRLF）。
func decode(data []byte) (string, bool, bool, error) {
	hasBOM := bytes.HasPrefix(data, bom)
	if hasBOM {
		data = data[3:]
	}
	if !utf8.Valid(data) {
		return "", false, false, &UnicodeError{Msg: "'utf-8' codec can't decode"}
	}
	text := string(data)
	crlf := strings.Contains(text, "\r\n")
	return strings.ReplaceAll(text, "\r\n", "\n"), hasBOM, crlf, nil
}

func merge(info *ojson.Object, pairs ...any) *ojson.Object {
	out := info.Copy()
	for i := 0; i+1 < len(pairs); i += 2 {
		out.Set(pairs[i].(string), pairs[i+1])
	}
	return out
}

// Read 返回位置信息加 content、version、crlf、mode。
func Read(paths Paths, target any) (*ojson.Object, error) {
	source, info, err := Resolve(paths, target)
	if err != nil {
		return nil, err
	}
	if err := editable(source); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	text, _, crlf, err := decode(data)
	if err != nil {
		return nil, config.Errorf("不是UTF-8文本，不能在页面上编辑")
	}
	st, err := textutil.Stat(source)
	if err != nil {
		return nil, err
	}
	mode, ok := Modes[textutil.Lower(textutil.Suffix(source))]
	if !ok {
		mode = "text/plain"
	}
	return merge(info, "content", text, "version", VersionOf(data, st.MtimeNS), "crlf", crlf, "mode", mode), nil
}

func historyDir(paths Paths, source string) string {
	sum := sha1.Sum([]byte(source))
	return textutil.Join(paths.History, hex.EncodeToString(sum[:])[:16])
}

// lockSource 按源文件在历史目录里加锁，同一文件的保存互斥（跨进程有效）。
func lockSource(paths Paths, source string) (func(), error) {
	folder := historyDir(paths, source)
	if err := os.MkdirAll(folder, 0o777); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(filepath.Join(folder, ".lock"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	unlock, err := config.LockFile(handle)
	if err != nil {
		handle.Close()
		return nil, err
	}
	return func() { unlock(); handle.Close() }, nil
}

func snapshots(folder string) []string {
	matches, _ := filepath.Glob(filepath.Join(folder, "*.bak"))
	sort.Strings(matches)
	return matches
}

func remember(paths Paths, source string, data []byte) error {
	folder := historyDir(paths, source)
	if err := os.WriteFile(filepath.Join(folder, "source.txt"), []byte(source), 0o666); err != nil {
		return err
	}
	name := strconv.FormatInt(time.Now().UnixNano(), 10) + ".bak"
	if err := os.WriteFile(filepath.Join(folder, name), data, 0o666); err != nil {
		return err
	}
	list := snapshots(folder)
	if len(list) > HistoryKeep {
		for _, old := range list[:len(list)-HistoryKeep] {
			_ = os.Remove(old)
		}
	}
	return nil
}

// Save 版本对得上才写；先留历史，再原子替换，保留原文件权限、BOM 和换行风格。
// 版本不一致返回 *Conflict；源文件不是 UTF-8 返回 *UnicodeError。
func Save(paths Paths, target, content, baseVersion any) (*ojson.Object, error) {
	text, ok1 := content.(string)
	base, ok2 := baseVersion.(string)
	if !ok1 || !ok2 {
		return nil, config.Errorf("保存请求缺少内容或基准版本")
	}
	source, info, err := Resolve(paths, target)
	if err != nil {
		return nil, err
	}
	if err := editable(source); err != nil {
		return nil, err
	}
	unlock, err := lockSource(paths, source)
	if err != nil {
		return nil, err
	}
	defer unlock()
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	st, err := textutil.Stat(source)
	if err != nil {
		return nil, err
	}
	currentText, hasBOM, crlf, err := decode(data)
	if err != nil {
		return nil, err
	}
	current := VersionOf(data, st.MtimeNS)
	if current != base {
		return nil, &Conflict{Content: currentText, Version: current}
	}
	body := strings.ReplaceAll(text, "\r\n", "\n")
	if crlf {
		body = strings.ReplaceAll(body, "\n", "\r\n")
	}
	var encoded []byte
	if hasBOM {
		encoded = append(encoded, bom...)
	}
	encoded = append(encoded, body...)
	if len(encoded) > MaxBytes {
		return nil, config.Errorf("内容超过2MB")
	}
	if bytes.Equal(encoded, data) {
		return merge(info, "version", current, "changed", false), nil
	}
	if err := remember(paths, source, data); err != nil {
		return nil, err
	}
	// 临时文件放在同一目录，保证替换是原子的；隐藏文件名，自动同步不会把它当成文档。
	temp, err := os.CreateTemp(filepath.Dir(source), ".vinx-*.tmp")
	if err != nil {
		return nil, err
	}
	tempName := temp.Name()
	fail := func(err error) (*ojson.Object, error) {
		temp.Close()
		os.Remove(tempName)
		return nil, err
	}
	if _, err := temp.Write(encoded); err != nil {
		return fail(err)
	}
	if err := temp.Sync(); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return nil, err
	}
	if err := os.Chmod(tempName, st.Mode&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
		os.Remove(tempName)
		return nil, err
	}
	if err := os.Rename(tempName, source); err != nil {
		os.Remove(tempName)
		return nil, err
	}
	after, err := textutil.Stat(source)
	if err != nil {
		return nil, err
	}
	return merge(info, "version", VersionOf(encoded, after.MtimeNS), "changed", true), nil
}

// History 从新到旧列出保存前的快照。
func History(paths Paths, target any) ([]any, error) {
	source, _, err := Resolve(paths, target)
	if err != nil {
		return nil, err
	}
	list := snapshots(historyDir(paths, source))
	items := []any{}
	for i := len(list) - 1; i >= 0; i-- {
		stem := strings.TrimSuffix(filepath.Base(list[i]), ".bak")
		n, err := strconv.ParseInt(stem, 10, 64)
		if err != nil {
			return nil, &config.PanicError{Msg: "ValueError: invalid literal for int(): " + stem}
		}
		st, err := textutil.Stat(list[i])
		if err != nil {
			return nil, err
		}
		items = append(items, ojson.NewObject("id", stem, "savedAt", floorDiv(n, 1_000_000_000), "size", st.Size))
	}
	return items, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// HistoryItem 取一份历史快照的文本。
func HistoryItem(paths Paths, target, item any) (string, error) {
	id, ok := item.(string)
	if !ok || !isDigit(id) {
		return "", config.Errorf("历史版本无效")
	}
	source, _, err := Resolve(paths, target)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(historyDir(paths, source), id+".bak"))
	if err != nil {
		return "", config.Errorf("历史版本不存在")
	}
	text, _, _, err := decode(data)
	if err != nil {
		return "", config.Errorf("历史版本不存在")
	}
	return text, nil
}

// isDigit 非空且每个字符都是数字（含上标等 Unicode 数字）。
func isDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !unicode.Is(unicode.No, r) {
			return false
		}
	}
	return true
}
