// Package pages 按短链接发布单个页面。登记入口页和显式文件清单，
// 由服务按请求实时读取源文件，不复制内容。
package pages

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vinx-lab/vinx-docs/internal/build"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

const (
	SchemaVersion = 1
	// ReservedDir：/a/<id>/__docsify_x/ 留给版本查询，发布的文件不能占用。
	ReservedDir = "__docsify_x"
	MaxFiles    = 500
	LiveScript  = "/assets/artifact-live.js"
	idAlphabet  = "abcdefghjkmnpqrstuvwxyz23456789"
)

// PagePolicy：页面在沙箱里运行，origin 变成 null，调不动 /api/ 写接口；只允许加载本站资源，离线可用。
const PagePolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; " +
	"default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
	"media-src 'self' data: blob:; connect-src 'self'; frame-src 'self'; worker-src 'self' blob:; " +
	"object-src 'none'; base-uri 'self'; form-action 'none'; frame-ancestors 'self'"

var pageSuffixes = map[string]bool{".html": true, ".htm": true}

// markdownSuffixes 是可以作为入口的 Markdown 文件；这类发布在站点的单文件阅读页里渲染。
var markdownSuffixes = map[string]bool{".md": true, ".markdown": true}

// IsMarkdown 报告文件是否按 Markdown 处理。
func IsMarkdown(rel string) bool { return markdownSuffixes[textutil.Lower(textutil.Suffix(rel))] }

// ReadURL 是 Markdown 发布的阅读页地址。
func ReadURL(id string) string { return "/read.html?a=" + id }

// MimeTypes 是短链接页面按扩展名返回的 Content-Type。
var MimeTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".mjs": "text/javascript; charset=utf-8", ".json": "application/json; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".xml": "application/xml; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".ico": "image/x-icon", ".bmp": "image/bmp",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	".pdf": "application/pdf", ".wasm": "application/wasm",
}

var idRE = regexp.MustCompile(`^[a-z0-9]{4,32}\n?$`)

// DefaultRegistry 返回家目录下的页面登记文件路径。
func DefaultRegistry(home string) string { return textutil.Join(home, "config", "artifacts.json") }

func validateRecord(item any) error {
	record, ok := item.(*ojson.Object)
	if !ok {
		return config.Errorf("artifacts[]必须是对象")
	}
	id, ok := record.Value("id").(string)
	if !ok || !idRE.MatchString(id) {
		return config.Errorf("artifacts[].id无效")
	}
	if root, ok := record.Value("root").(string); !ok || !textutil.IsAbs(root) {
		return config.Errorf("%s的root必须是绝对路径", id)
	}
	files, ok := record.Value("files").([]any)
	if !ok || len(files) == 0 {
		return config.Errorf("%s的files必须是非空字符串数组", id)
	}
	entry := record.Value("entry")
	inFiles := false
	for _, f := range files {
		s, ok := f.(string)
		if !ok {
			return config.Errorf("%s的files必须是非空字符串数组", id)
		}
		if e, ok := entry.(string); ok && e == s {
			inFiles = true
		}
	}
	if !inFiles {
		return config.Errorf("%s的entry必须在files中", id)
	}
	if _, ok := record.Value("title").(string); !ok {
		return config.Errorf("%s的title必须是字符串", id)
	}
	for _, key := range []string{"createdAt", "publishedAt"} {
		if _, ok := textutil.Int(record.Value(key)); !ok {
			return config.Errorf("%s的%s必须是整数", id, key)
		}
	}
	return nil
}

// LoadRegistry 读取页面登记文件；不存在时返回空登记。
func LoadRegistry(path string) (*ojson.Object, error) {
	if !textutil.Exists(path) {
		return ojson.NewObject("schemaVersion", SchemaVersion, "artifacts", []any{}), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, config.Errorf("无法读取发布登记: %s", textutil.Norm(path))
	}
	value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
	if err != nil {
		return nil, config.Errorf("无法读取发布登记: %s", textutil.Norm(path))
	}
	obj, ok := value.(*ojson.Object)
	if !ok || !textutil.EqualsInt(obj.Value("schemaVersion"), SchemaVersion) {
		return nil, config.Errorf("发布登记格式无效: %s", textutil.Norm(path))
	}
	list, ok := obj.Value("artifacts").([]any)
	if !ok {
		return nil, config.Errorf("发布登记格式无效: %s", textutil.Norm(path))
	}
	for _, item := range list {
		if err := validateRecord(item); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

func records(value *ojson.Object) []any {
	list, _ := value.Value("artifacts").([]any)
	return list
}

// Find 按短码找登记项，没有返回 nil。
func Find(value *ojson.Object, id string) *ojson.Object {
	for _, item := range records(value) {
		record := item.(*ojson.Object)
		if record.Value("id") == id {
			return record
		}
	}
	return nil
}

// Record 取值的小工具。
func Str(record *ojson.Object, key string) string {
	s, _ := record.Value(key).(string)
	return s
}

// Files 返回登记项的文件清单。
func Files(record *ojson.Object) []string {
	list, _ := record.Value("files").([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.(string))
	}
	return out
}

// CheckFile 在根目录内、不经过符号链接、不是隐藏或凭据文件、不含私钥。返回源文件路径。
func CheckFile(root, relText string) (string, error) {
	rel, err := config.RejectDotdot(relText, "发布文件")
	if err != nil {
		return "", err
	}
	parts := textutil.Parts(rel)
	if len(parts) == 0 {
		return "", &config.PanicError{Msg: "IndexError: tuple index out of range"}
	}
	if parts[0] == ReservedDir {
		return "", config.Errorf("%s是保留目录: %s", ReservedDir, relText)
	}
	path := textutil.Join(root, parts...)
	if reason := config.PolicyReason(rel, path); reason != "" {
		return "", config.Errorf("%s，不能发布: %s", reason, relText)
	}
	current := root
	for _, part := range parts {
		current = textutil.Join(current, part)
		if textutil.IsSymlink(current) {
			return "", config.Errorf("不能经过符号链接: %s", relText)
		}
	}
	if !textutil.IsFile(path) {
		return "", config.Errorf("文件不存在: %s", relText)
	}
	if blocked, _ := contentReason(path); blocked != "" {
		return "", config.Errorf("%s，不能发布: %s", blocked, relText)
	}
	return path, nil
}

func contentReason(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > 512*1024 {
		return "", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return build.ContentReason(int64(len(data)), data)
}

const (
	ws    = `\t\n\x0b\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	space = `[` + ws + `]`
)

var (
	htmlRefRE = regexp.MustCompile(`(?is)\A(src|href|poster|data|srcset)` + space + `*=` + space +
		`*(?:"([^"]*)"|'([^']*)'|([^` + ws + `>"']+))`)
	cssRefRE = regexp.MustCompile(`(?is)url\(` + space + `*(?:"([^"]*)"|'([^']*)'|([^)'"` + ws + `]+))` + space +
		`*\)|@import` + space + `+(?:"([^"]*)"|'([^']*)')`)
	jsImportRE = regexp.MustCompile(`(?s)\A(?:from` + space + `*|import` + space + `*\(` + space + `*|import` + space +
		`+)(?:"(\.{1,2}/[^"'\n]+)"|'(\.{1,2}/[^"'\n]+)')`)
	jsFetchRE = regexp.MustCompile(`(?s)\Afetch` + space + `*\(` + space + `*(?:"([^"'\n]+)"|'([^"'\n]+)')`)
	jsURLRE   = regexp.MustCompile(`(?s)\Anew` + space + `+URL` + space + `*\(` + space + `*(?:"([^"'\n]+)"|'([^"'\n]+)')`)
	schemeRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	titleRE   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	// Markdown 图片 ![说明](地址 "标题")，地址可以用 <> 包起来。
	mdImageRE   = regexp.MustCompile(`!\[[^\]]*\]\(\s*(?:<([^>]+)>|([^)\s]+))`)
	mdHeadingRE = regexp.MustCompile(`(?m)^#\s+(.+?)\s*#*\s*$`)
	mdCodeRE    = regexp.MustCompile("(?ms)^(```|~~~).*?^(```|~~~)[^\n]*$|`[^`\n]*`")
)

// boundaryAt 判断 text[pos] 处是否是词边界（按 Unicode 词字符）。
func boundaryAt(text string, pos int) bool {
	before, after := false, false
	if pos > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:pos])
		before = textutil.IsWord(r)
	}
	if pos < len(text) {
		r, _ := utf8.DecodeRuneInString(text[pos:])
		after = textutil.IsWord(r)
	}
	return before != after
}

// groupValue 返回第一个参与了匹配的分组。
func groupValue(text string, loc []int, from int) string {
	for g := from; 2*g+1 < len(loc); g++ {
		if loc[2*g] >= 0 {
			return text[loc[2*g]:loc[2*g+1]]
		}
	}
	return ""
}

type htmlTag struct{ name, attrs string }

// htmlTags 找出所有形如 <([A-Za-z][\w-]*)\b([^>]*)> 的标签。
func htmlTags(text string) []htmlTag {
	var tags []htmlTag
	for i := 0; i < len(text); {
		if text[i] != '<' || i+1 >= len(text) || !isASCIILetter(text[i+1]) {
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
			continue
		}
		j := i + 1
		for j < len(text) {
			r, size := utf8.DecodeRuneInString(text[j:])
			if !textutil.IsWord(r) && r != '-' {
				break
			}
			j += size
		}
		nameEnd := j
		for nameEnd > i+2 && text[nameEnd-1] == '-' { // \b 要求名字以词字符结尾
			nameEnd--
		}
		close := strings.IndexByte(text[nameEnd:], '>')
		if close < 0 {
			i++
			continue
		}
		tags = append(tags, htmlTag{text[i+1 : nameEnd], text[nameEnd : nameEnd+close]})
		i = nameEnd + close + 1
	}
	return tags
}

func isASCIILetter(c byte) bool { return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }

// scanAnchored 在每个位置尝试锚定正则（可要求该处是 \b），语义同 finditer。
func scanAnchored(text string, pattern *regexp.Regexp, needBoundary bool, visit func(loc []int, sub string)) {
	for pos := 0; pos < len(text); {
		if !needBoundary || boundaryAt(text, pos) {
			if loc := pattern.FindStringSubmatchIndex(text[pos:]); loc != nil && loc[1] > 0 {
				visit(loc, text[pos:])
				pos += loc[1]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		pos += size
	}
}

func references(path string) []string {
	suffix := textutil.Lower(textutil.Suffix(path))
	text, err := textutil.ReadTextReplace(path)
	if err != nil {
		return nil
	}
	var found []string
	if markdownSuffixes[suffix] {
		// 只收图片（Markdown 语法和内嵌的 <img>/<source>）；链接到的其他文件不自动收，避免扩大发布范围。
		// 代码块和行内代码里的内容不是引用，先去掉。
		text = mdCodeRE.ReplaceAllString(text, "")
		for _, m := range mdImageRE.FindAllStringSubmatch(text, -1) {
			found = append(found, m[1]+m[2])
		}
		for _, tag := range htmlTags(text) {
			if name := strings.ToLower(tag.name); name != "img" && name != "source" {
				continue
			}
			scanAnchored(tag.attrs, htmlRefRE, true, func(loc []int, sub string) {
				if attr := strings.ToLower(sub[loc[2]:loc[3]]); attr == "src" || attr == "srcset" {
					value := groupValue(sub, loc, 2)
					if attr == "srcset" {
						for _, part := range strings.Split(value, ",") {
							if fields := textutil.SplitWhitespace(textutil.Strip(part)); len(fields) > 0 {
								found = append(found, fields[0])
							}
						}
						return
					}
					found = append(found, value)
				}
			})
		}
		return found
	}
	if pageSuffixes[suffix] {
		for _, tag := range htmlTags(text) {
			scanAnchored(tag.attrs, htmlRefRE, true, func(loc []int, sub string) {
				value := groupValue(sub, loc, 2)
				attr := strings.ToLower(sub[loc[2]:loc[3]])
				switch {
				case attr == "srcset":
					for _, part := range strings.Split(value, ",") {
						if fields := textutil.SplitWhitespace(textutil.Strip(part)); len(fields) > 0 {
							found = append(found, fields[0])
						}
					}
				case strings.ToLower(tag.name) == "a" && attr == "href" && (classify(value) == "external" || classify(value) == "absolute"):
					// 超链接是跳转，不是要加载的资源：指向外部文档不算问题，不提示。
				default:
					found = append(found, value)
				}
			})
		}
	}
	if pageSuffixes[suffix] || suffix == ".css" {
		for _, loc := range cssRefRE.FindAllStringSubmatchIndex(text, -1) {
			found = append(found, groupValue(text, loc, 1))
		}
	}
	if pageSuffixes[suffix] || suffix == ".js" || suffix == ".mjs" {
		for pos := 0; pos < len(text); {
			matched := false
			for _, item := range []struct {
				re       *regexp.Regexp
				boundary bool
			}{{jsImportRE, true}, {jsFetchRE, true}, {jsURLRE, false}} {
				if item.boundary && !boundaryAt(text, pos) {
					continue
				}
				if loc := item.re.FindStringSubmatchIndex(text[pos:]); loc != nil {
					found = append(found, groupValue(text[pos:], loc, 1))
					pos += loc[1]
					matched = true
					break
				}
			}
			if !matched {
				_, size := utf8.DecodeRuneInString(text[pos:])
				pos += size
			}
		}
	}
	return found
}

// classify 返回需要提示的类型；"" 表示相对引用。
func classify(ref string) string {
	ref = textutil.Strip(ref)
	lower := strings.ToLower(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return "skip"
	}
	for _, prefix := range []string{"data:", "blob:", "javascript:", "mailto:", "tel:", "about:"} {
		if strings.HasPrefix(lower, prefix) {
			return "skip"
		}
	}
	if strings.HasPrefix(ref, "//") || schemeRE.MatchString(ref) {
		return "external"
	}
	if strings.HasPrefix(ref, "/") {
		return "absolute"
	}
	if strings.Contains(ref, "${") || strings.Contains(ref, "{{") {
		return "skip"
	}
	return ""
}

func headRunes(s string, n int) string { return textutil.Head(s, n) }

// Collect 从入口页出发，沿 HTML/CSS/JS 里的相对引用收集文件清单；只读，不改源文件。
func Collect(entry, root string, extra []string) ([]string, []string, error) {
	files := []string{}
	notes := []string{}
	seen := map[string]bool{}
	queue := []string{textutil.RelativeTo(entry, root)}
	for _, item := range extra {
		queue = append(queue, strings.Trim(item, "/"))
	}
	explicit := map[string]bool{}
	for _, item := range queue {
		explicit[item] = true
	}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		path := textutil.Join(root, textutil.Parts(rel)...)
		if !explicit[rel] && textutil.IsDir(path) {
			queue = append(queue, textutil.PosixJoin(rel, "index.html"))
			continue
		}
		checked, err := CheckFile(root, rel)
		if err != nil {
			if explicit[rel] || !config.IsConfigError(err) {
				return nil, nil, err
			}
			notes = append(notes, "已跳过引用："+err.Error())
			continue
		}
		files = append(files, rel)
		if len(files) > MaxFiles {
			return nil, nil, config.Errorf("文件超过%d个，请缩小发布范围", MaxFiles)
		}
		base := textutil.PosixDirname(rel)
		for _, ref := range references(checked) {
			switch classify(ref) {
			case "skip":
				continue
			case "external":
				notes = append(notes, rel+" 引用了外部地址，页面里会被拦截："+headRunes(ref, 120))
				continue
			case "absolute":
				notes = append(notes, rel+" 用了站点绝对路径，发布后找不到，请改成相对路径："+headRunes(ref, 120))
				continue
			}
			target, _, _ := strings.Cut(ref, "#")
			target, _, _ = strings.Cut(target, "?")
			target = textutil.Unquote(target)
			if target == "" {
				continue
			}
			joined := textutil.PosixNormpath(textutil.PosixJoin(base, target))
			if joined == ".." || strings.HasPrefix(joined, "../") {
				return nil, nil, config.Errorf("%s 引用了根目录之外的文件 %s；请用 --root 指定更上层的目录", rel, ref)
			}
			if !seen[joined] {
				queue = append(queue, joined)
			}
		}
	}
	return files, notes, nil
}

func pageTitle(entry string) string {
	text, err := textutil.ReadTextReplace(entry)
	if err != nil {
		text = ""
	}
	text = textutil.Head(text, 65536)
	title := ""
	re := titleRE
	if IsMarkdown(entry) {
		re = mdHeadingRE
	}
	if m := re.FindStringSubmatch(text); m != nil {
		title = textutil.Strip(textutil.CollapseSpace(m[1]))
	}
	title = textutil.Head(title, 120)
	if title == "" {
		return textutil.Stem(entry)
	}
	return title
}

func newID(taken map[string]bool) string {
	for {
		b := make([]byte, 6)
		for i := range b {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
			b[i] = idAlphabet[n.Int64()]
		}
		if !taken[string(b)] {
			return string(b)
		}
	}
}

// Now 可在测试里替换。
var Now = func() int64 { return time.Now().Unix() }

// Publish 登记或更新一个发布；同一个入口页重复发布沿用原短码。
// root 为空表示入口页所在目录；title 为空表示从页面 <title> 取。返回登记项副本和提示。
func Publish(registry, entry, root string, extra []string, title string) (*ojson.Object, []string, error) {
	expanded, err := textutil.ExpandUser(entry)
	if err != nil {
		return nil, nil, err
	}
	entryPath, err := config.AbsoluteNoSymlink(expanded, "入口页")
	if err != nil {
		return nil, nil, err
	}
	if suffix := textutil.Lower(textutil.Suffix(entryPath)); !(pageSuffixes[suffix] || markdownSuffixes[suffix]) || !textutil.IsFile(entryPath) {
		return nil, nil, config.Errorf("入口页必须是存在的.html或.md文件: %s", entryPath)
	}
	if root == "" {
		root = textutil.Parent(entryPath)
	}
	rootPath, err := config.CheckedDir(root, "发布根目录")
	if err != nil {
		return nil, nil, err
	}
	if !textutil.IsWithin(entryPath, rootPath) {
		return nil, nil, config.Errorf("入口页必须在发布根目录内")
	}
	files, notes, err := Collect(entryPath, rootPath, extra)
	if err != nil {
		return nil, nil, err
	}
	now := Now()
	unlock, err := config.Lock(registry)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	value, err := LoadRegistry(registry)
	if err != nil {
		return nil, nil, err
	}
	entryRel := textutil.RelativeTo(entryPath, rootPath)
	var record *ojson.Object
	for _, item := range records(value) {
		candidate := item.(*ojson.Object)
		if textutil.SamePath(textutil.Join(Str(candidate, "root"), textutil.Parts(Str(candidate, "entry"))...), entryPath) {
			record = candidate
			break
		}
	}
	if record == nil {
		taken := map[string]bool{}
		for _, item := range records(value) {
			taken[Str(item.(*ojson.Object), "id")] = true
		}
		record = ojson.NewObject("id", newID(taken), "createdAt", now)
		value.Set("artifacts", append(records(value), record))
	}
	finalTitle := textutil.Head(textutil.Strip(title), 120)
	if finalTitle == "" {
		finalTitle = pageTitle(entryPath)
	}
	fileList := make([]any, len(files))
	for i, f := range files {
		fileList[i] = f
	}
	record.Set("title", finalTitle)
	record.Set("root", rootPath)
	record.Set("entry", entryRel)
	record.Set("files", fileList)
	record.Set("publishedAt", now)
	if err := config.AtomicWriteJSON(registry, value); err != nil {
		return nil, nil, err
	}
	return record.Copy(), notes, nil
}

// Unpublish 取消一个短链接，不删源文件。
func Unpublish(registry, id string) (*ojson.Object, error) {
	unlock, err := config.Lock(registry)
	if err != nil {
		return nil, err
	}
	defer unlock()
	value, err := LoadRegistry(registry)
	if err != nil {
		return nil, err
	}
	record := Find(value, id)
	if record == nil {
		return nil, config.Errorf("没有这个短码: %s", id)
	}
	var remaining []any
	removed := false
	for _, item := range records(value) {
		if !removed && item == any(record) {
			removed = true
			continue
		}
		remaining = append(remaining, item)
	}
	if remaining == nil {
		remaining = []any{}
	}
	value.Set("artifacts", remaining)
	if err := config.AtomicWriteJSON(registry, value); err != nil {
		return nil, err
	}
	return record, nil
}

// Version 清单里任一文件的修改时间或大小变化，版本号就变；页面据此自动刷新。
func Version(record *ojson.Object) string {
	digest := sha1.New()
	digest.Write([]byte(textutil.Str(record.Value("publishedAt"))))
	root := Str(record, "root")
	for _, rel := range Files(record) {
		if st, err := textutil.Stat(textutil.Join(root, textutil.Parts(rel)...)); err == nil {
			digest.Write([]byte(rel + ":" + strconv.FormatInt(st.MtimeNS, 10) + ":" + strconv.FormatInt(st.Size, 10) + "\n"))
		} else {
			digest.Write([]byte(rel + ":missing\n"))
		}
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

// Summary 列表页用的摘要（含缺失文件和最后修改时间）。
func Summary(record *ojson.Object) *ojson.Object {
	root := Str(record, "root")
	missing := []any{}
	var modified int64
	for _, rel := range Files(record) {
		if st, err := textutil.Stat(textutil.Join(root, textutil.Parts(rel)...)); err == nil {
			if st.Mtime > modified {
				modified = st.Mtime
			}
		} else {
			missing = append(missing, rel)
		}
	}
	id := Str(record, "id")
	kind, readURL := "html", ""
	if IsMarkdown(Str(record, "entry")) {
		kind, readURL = "markdown", ReadURL(id)
	}
	return ojson.NewObject(
		"id", id, "title", record.Value("title"), "url", "/a/"+id+"/", "kind", kind, "readUrl", readURL,
		"root", record.Value("root"), "entry", record.Value("entry"), "fileCount", len(Files(record)),
		"files", record.Value("files"), "missing", missing, "createdAt", record.Value("createdAt"),
		"publishedAt", record.Value("publishedAt"), "modifiedAt", modified,
	)
}

// Listing 按发布时间从新到旧。
func Listing(registry string) ([]any, error) {
	value, err := LoadRegistry(registry)
	if err != nil {
		return nil, err
	}
	items := []*ojson.Object{}
	for _, item := range records(value) {
		items = append(items, Summary(item.(*ojson.Object)))
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, _ := textutil.Int(items[i].Value("publishedAt"))
		b, _ := textutil.Int(items[j].Value("publishedAt"))
		return a.Cmp(b) > 0
	})
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out, nil
}

// ReaderManifest 是单文件阅读页用的清单，格式与文档项目的 manifest.json 相同：
// Markdown 文件可在阅读页里切换，图片内嵌，HTML 在新窗口打开，其余文件下载。
func ReaderManifest(record *ojson.Object) *ojson.Object {
	id := Str(record, "id")
	base := "/a/" + id + "/"
	entries := []any{}
	for _, rel := range Files(record) {
		url := base + textutil.Quote(rel, "/")
		entry := ojson.NewObject("path", rel)
		switch mime := MimeFor(rel); {
		case IsMarkdown(rel):
			title := textutil.Stem(rel)
			if rel == Str(record, "entry") {
				title = Str(record, "title")
			}
			entry.Set("route", rel)
			entry.Set("title", title)
		case strings.HasPrefix(mime, "image/"):
			entry.Set("kind", "image")
			entry.Set("url", url)
		case strings.HasPrefix(mime, "text/html"):
			entry.Set("kind", "html")
			entry.Set("previewUrl", url)
		default:
			entry.Set("kind", "download")
			entry.Set("url", url)
		}
		entries = append(entries, entry)
	}
	return ojson.NewObject("id", "a-"+id, "name", record.Value("title"), "home", record.Value("entry"),
		"version", Version(record), "target", "a:"+id+"/", "content", base, "versionUrl", base+ReservedDir+"/version",
		"entries", entries)
}

// InjectLive 在最后一个 </body> 前插入自动刷新脚本；data-page 告诉页面自己是清单里的哪个文件。
func InjectLive(body []byte, current, page string) []byte {
	tag := []byte(`<script src="` + LiveScript + `" data-version="` + current + `" data-page="` + textutil.HTMLEscape(page) + `"></script>`)
	index := lastIndexFoldASCII(body, []byte("</body>"))
	if index < 0 {
		return append(append([]byte(nil), body...), tag...)
	}
	out := append([]byte(nil), body[:index]...)
	out = append(out, tag...)
	return append(out, body[index:]...)
}

func lastIndexFoldASCII(body, needle []byte) int {
	for i := len(body) - len(needle); i >= 0; i-- {
		match := true
		for k := range needle {
			c := body[i+k]
			if 'A' <= c && c <= 'Z' {
				c += 32
			}
			if c != needle[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// MimeFor 按扩展名返回 Content-Type，未知类型为 application/octet-stream。
func MimeFor(rel string) string {
	if t, ok := MimeTypes[textutil.Lower(textutil.Suffix(rel))]; ok {
		return t
	}
	return "application/octet-stream"
}

var (
	publicBaseRE = regexp.MustCompile(`^https?://[A-Za-z0-9.:\[\]-]+$`)
	dnsNameRE    = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
)

// TailscaleStatus 可在测试里替换；返回 `tailscale status --json` 的输出。
var TailscaleStatus = func() ([]byte, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
		return out, err
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return nil, os.ErrDeadlineExceeded
	}
}

// PublicBase 给人点的链接地址。
// 优先 config 的 server.publicBase；否则用本机 Tailscale MagicDNS 名；都没有才退回 localhost。
func PublicBase(home string) string {
	if cfg, err := config.Load(config.ConfigPath(home)); err == nil {
		if server, ok := cfg.Value("server").(*ojson.Object); ok {
			if configured, ok := server.Value("publicBase").(string); ok {
				trimmed := strings.TrimRight(configured, "/")
				if publicBaseRE.MatchString(trimmed) {
					return trimmed
				}
			}
		}
	}
	_, port := config.ServerAddress(home)
	if output, err := TailscaleStatus(); err == nil {
		if value, err := ojson.Decode(output); err == nil {
			if status, ok := value.(*ojson.Object); ok {
				if self, ok := status.Value("Self").(*ojson.Object); ok && self.Has("DNSName") {
					name := strings.TrimRight(textutil.Str(self.Value("DNSName")), ".")
					if dnsNameRE.MatchString(name) {
						return "http://" + name + ":" + strconv.Itoa(port)
					}
				}
			}
		}
	}
	return "http://localhost:" + strconv.Itoa(port)
}

// Probe 直接连本机服务取一次入口页，确认服务能读到；不经过代理。
func Probe(home, id string) (bool, string) {
	status, _ := config.LocalRequest(home, "/a/"+id+"/", 3*time.Second)
	switch {
	case status == 0:
		return false, "服务未运行，链接暂时打不开；执行 vinx-docs start 后即可访问"
	case status == 200 || status == 302:
		return true, "服务已可访问"
	default:
		return false, "服务返回HTTP " + strconv.Itoa(status) + "，请检查"
	}
}
