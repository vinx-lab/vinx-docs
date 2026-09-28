package config

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// 收录的文件类型与强制安全规则。
var (
	TextExtensions = set(".json", ".csv", ".yaml", ".yml", ".xml", ".sh", ".py", ".js", ".mjs",
		".cjs", ".conf", ".txt", ".example", ".dockerfile")
	MarkdownExtensions = set(".md", ".markdown")
	HTMLExtensions     = set(".html", ".htm")
	ImageExtensions    = set(".png", ".jpg", ".jpeg", ".webp", ".gif")
	DownloadExtensions = set(".xlsx")
	ForbiddenDirs      = set("__pycache__", "node_modules", "target", "logs", ".git")
	ForbiddenSuffixes  = set(".env", ".key", ".pem", ".p12", ".pfx", ".pub", ".ppk", ".jks", ".keystore", ".kdbx")
	ForbiddenNames     = set("credentials", "credentials.json", "secret.json", "secrets.json", "id_rsa", "id_dsa",
		"id_ecdsa", "id_ed25519", "known_hosts", "authorized_keys", "ssh_config", ".netrc", ".htpasswd")
	ReservedFirstComponents = set("__previews", "__source__")
	ReservedNames           = set("_sidebar.md", "index.html", "index.htm", "__scope.md")
)

// TypeGroup 是后台勾选框的一组扩展名；各组合起来正好是 KindFor 能识别的全部扩展名。
type TypeGroup struct {
	Label      string
	Extensions []string
}

// TypeGroups 按后台显示顺序分组；表格组取 .csv（文本预览）和 .xlsx（下载），其余文本归到文本与代码。
func TypeGroups() []TypeGroup {
	var text []string
	for ext := range TextExtensions {
		if ext != ".csv" {
			text = append(text, ext)
		}
	}
	return []TypeGroup{
		{"Markdown", sortedKeys(MarkdownExtensions)},
		{"HTML", sortedKeys(HTMLExtensions)},
		{"文本与代码", sortedList(text)},
		{"图片", sortedKeys(ImageExtensions)},
		{"表格", sortedList(append([]string{".csv"}, sortedKeys(DownloadExtensions)...))},
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for key := range m {
		out = append(out, key)
	}
	return sortedList(out)
}

func sortedList(items []string) []string {
	sort.Strings(items)
	return items
}

// NormalizeType 把扩展名规范成小写带点；不是 KindFor 能识别的扩展名时返回空串。
func NormalizeType(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	ext := textutil.Lower(textutil.Strip(text))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) < 2 || strings.ContainsAny(ext[1:], "./\\") || KindFor("x"+ext) == "" {
		return ""
	}
	return ext
}

// Types 返回项目允许收录的扩展名（规范化、去重、排序）；空表示不限类型。
func Types(project *ojson.Object) []string {
	list, _ := project.Value("types").([]any)
	seen := map[string]bool{}
	var out []string
	for _, item := range list {
		if ext := NormalizeType(item); ext != "" && !seen[ext] {
			seen[ext] = true
			out = append(out, ext)
		}
	}
	return sortedList(out)
}

// validateTypes 校验 types 字段：扩展名数组，每项都要能识别；缺省或空数组表示不限类型。
func validateTypes(project *ojson.Object) ([]string, error) {
	raw, present := project.Get("types")
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, Errorf("types必须是扩展名数组")
	}
	for _, item := range list {
		if NormalizeType(item) == "" {
			return nil, Errorf("types只能包含支持的扩展名: %s", textutil.Str(item))
		}
	}
	return Types(project), nil
}

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, item := range items {
		m[item] = true
	}
	return m
}

// Root 是项目登记的一个文档目录：Prefix 是项目内挂载前缀（空表示项目根），Path 是规范化的绝对路径。
type Root struct {
	Prefix string
	Path   string
}

// AbsoluteNoSymlink 路径上任何一级是符号链接都拒绝，唯一例外见 systemLink。
func AbsoluteNoSymlink(path, label string) (string, error) {
	path = textutil.Absolute(path)
	parts := textutil.Parts(path)
	current := parts[0]
	for _, component := range parts[1:] {
		current = textutil.Join(current, component)
		if textutil.IsSymlink(current) && !systemLink(current) {
			return "", Errorf("%s不能经过符号链接: %s", label, path)
		}
	}
	return path, nil
}

// darwinHost 与 systemLinks 是包级变量，测试里可以替换成临时的「假系统链接」。
var (
	darwinHost = runtime.GOOS == "darwin"
	// systemLinks 是 macOS 自带、位于根目录第一段的符号链接及其唯一允许的链接内容。
	systemLinks = map[string][]string{
		"/tmp": {"/private/tmp", "private/tmp"},
		"/var": {"/private/var", "private/var"},
		"/etc": {"/private/etc", "private/etc"},
	}
)

// systemLink 只在 macOS 上、且 path 恰好是 systemLinks 的键、链接内容恰好是表中的目标时为真；
// 其他符号链接（包括目标被改过的 /tmp）一律为假。
func systemLink(path string) bool {
	if !darwinHost {
		return false
	}
	// 调用方逐级拼出的路径以 //tmp 开头，先规范成 /tmp 再查表。
	targets, ok := systemLinks[filepath.Clean(path)]
	if !ok {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	for _, want := range targets {
		if target == want {
			return true
		}
	}
	return false
}

// RejectDotdot 返回规范化后的相对 posix 路径。
func RejectDotdot(value any, label string) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" || strings.Contains(text, "\\") {
		return "", Errorf("%s必须是非空相对路径", label)
	}
	// HasRoot 比 IsAbs 严：Windows 上 C:x、/x 不算绝对路径，但拼到目录后面会离开该目录。
	if textutil.IsAbs(text) || textutil.HasRoot(text) {
		return "", Errorf("%s不能包含绝对路径或越界组件", label)
	}
	for _, part := range textutil.Parts(text) {
		if part == ".." || windowsAlias(part) {
			return "", Errorf("%s不能包含绝对路径或越界组件", label)
		}
	}
	return textutil.Norm(text), nil
}

// windowsAlias 报告 Windows 上会被改写成别的名字的路径段：含 :（备用数据流、盘符），
// 或以 . 或空格结尾（Win32 会去掉，id_rsa. 实际打开 id_rsa，绕过文件名规则）。其他平台总是 false。
func windowsAlias(part string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return strings.Contains(part, ":") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ")
}

// CheckedDir 校验一个文档目录：必须是绝对路径、存在、是目录，且路径上没有符号链接。
func CheckedDir(raw any, label string) (string, error) {
	text, ok := raw.(string)
	if !ok || text == "" {
		return "", Errorf("%s必须是路径字符串", label)
	}
	expanded, err := textutil.ExpandUser(text)
	if err != nil {
		return "", err
	}
	root, err := AbsoluteNoSymlink(expanded, label)
	if err != nil {
		return "", err
	}
	if textutil.IsRoot(root) || textutil.SamePath(root, textutil.Home()) {
		return "", Errorf("%s不能是根目录或home目录", label)
	}
	if !textutil.IsDir(root) {
		return "", Errorf("%s必须是存在的目录: %s", label, root)
	}
	return root, nil
}

// PanicError 表示校验中遇到的非配置类错误（例如越界的边缘输入），调用方按普通错误处理。
type PanicError struct{ Msg string }

func (e *PanicError) Error() string { return e.Msg }

// ValidatePrefix 校验文档目录在项目内的前缀：相对路径、不含 .. 和隐藏段。
func ValidatePrefix(value any) (string, error) {
	if value == nil || value == "" {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", Errorf("roots[].prefix必须是字符串")
	}
	rel, err := RejectDotdot(strings.Trim(text, "/"), "roots[].prefix")
	if err != nil {
		return "", err
	}
	parts := textutil.Parts(rel)
	if len(parts) == 0 {
		return "", &PanicError{Msg: "IndexError: tuple index out of range"}
	}
	if ReservedFirstComponents[parts[0]] || ReservedNames[textutil.Name(rel)] {
		return "", Errorf("roots[].prefix不能使用保留名称")
	}
	return rel, nil
}

// ProjectRoots 返回项目的全部文档目录。
func ProjectRoots(project *ojson.Object) ([]Root, error) {
	raw := project.Value("roots")
	if raw == nil {
		path, err := CheckedDir(project.Value("docsPath"), "docsPath")
		if err != nil {
			return nil, err
		}
		return []Root{{"", path}}, nil
	}
	if project.Has("docsPath") {
		return nil, Errorf("roots与docsPath不能同时使用")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, Errorf("roots必须是非空数组")
	}
	var result []Root
	for _, item := range list {
		obj, ok := item.(*ojson.Object)
		if !ok {
			return nil, Errorf("roots[]只接受prefix和path字段")
		}
		for _, key := range obj.Keys() {
			if key != "prefix" && key != "path" {
				return nil, Errorf("roots[]只接受prefix和path字段")
			}
		}
		prefix, err := ValidatePrefix(obj.Value("prefix"))
		if err != nil {
			return nil, err
		}
		path, err := CheckedDir(obj.Value("path"), "roots[].path")
		if err != nil {
			return nil, err
		}
		for _, other := range result {
			label := prefix
			if label == "" {
				label = "(根)"
			}
			if prefix == other.Prefix {
				return nil, Errorf("同一项目内的prefix重复: %s", label)
			}
			if prefixCovers(prefix, other.Prefix) || prefixCovers(other.Prefix, prefix) {
				return nil, Errorf("同一项目内的prefix互相嵌套: %s", label)
			}
			if textutil.IsWithin(path, other.Path) || textutil.IsWithin(other.Path, path) {
				return nil, Errorf("同一项目内的文档目录互相嵌套: %s", path)
			}
		}
		result = append(result, Root{prefix, path})
	}
	return result, nil
}

// 空前缀挂在项目根下，与带前缀的目录并存是正常布局；真正的冲突由同名文件检查兜住。
func prefixCovers(outer, inner string) bool {
	if outer == "" || inner == "" {
		return false
	}
	return strings.HasPrefix(inner, outer+"/")
}

// Published 把目录内相对路径加上前缀。
func Published(prefix, rel string) string {
	if prefix == "" {
		return textutil.Norm(rel)
	}
	return textutil.Join(prefix, rel)
}

// Locate 把项目内的发布路径映射回具体目录；前缀长的优先，空前缀兜底。
// 返回 (root, 目录内相对路径, 是否找到)；inner 为 "." 表示正好是前缀本身。
func Locate(roots []Root, rel string) (Root, string, bool) {
	sorted := append([]Root(nil), roots...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Prefix) > len(sorted[j].Prefix) })
	text := textutil.Norm(rel)
	for _, root := range sorted {
		if root.Prefix == "" {
			return root, text, true
		}
		if text == root.Prefix {
			return root, ".", true
		}
		if strings.HasPrefix(text, root.Prefix+"/") {
			return root, textutil.Norm(text[len(root.Prefix)+1:]), true
		}
	}
	return Root{}, "", false
}

// caseInsensitiveNames 为真时禁止目录名不区分大小写（NTFS 默认不区分，Node_Modules 也要拦住）。
// 测试里可以临时改成 true。凭据文件名在各平台都先转小写再比较，不需要这个开关。
var caseInsensitiveNames = runtime.GOOS == "windows"

// IsForbiddenDir 报告目录名是否属于 ForbiddenDirs。
func IsForbiddenDir(name string) bool {
	if caseInsensitiveNames {
		name = textutil.Lower(name)
	}
	return ForbiddenDirs[name]
}

// PolicyReason 强制安全规则（隐藏路径、缓存目录、凭据文件名）。
func PolicyReason(rel, path string) string {
	parts := textutil.Parts(rel)
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return "隐藏路径"
		}
	}
	for _, part := range parts {
		if IsForbiddenDir(part) {
			return "运行缓存或仓库目录"
		}
	}
	name := textutil.Lower(textutil.Name(path))
	if name == ".env" || strings.HasSuffix(name, ".env") || ForbiddenSuffixes[textutil.Lower(textutil.Suffix(path))] {
		return "凭据或环境文件"
	}
	if ForbiddenNames[name] {
		return "凭据或SSH配置文件"
	}
	return ""
}

// KindFor 不支持的类型返回空串。
func KindFor(path string) string {
	suffix := textutil.Lower(textutil.Suffix(path))
	name := textutil.Lower(textutil.Name(path))
	switch {
	case MarkdownExtensions[suffix]:
		return "markdown"
	case HTMLExtensions[suffix]:
		return "html"
	case TextExtensions[suffix] || name == "dockerfile" || name == "makefile":
		return "text"
	case DownloadExtensions[suffix]:
		return "download"
	case ImageExtensions[suffix]:
		return "image"
	}
	return ""
}

// ValidateProject 校验单个注册项并返回规范化的文档目录列表。
func ValidateProject(value any, protectedPorts []any) ([]Root, error) {
	project, ok := value.(*ojson.Object)
	if !ok {
		return nil, Errorf("项目项必须是对象")
	}
	id, ok := project.Value("id").(string)
	if !ok || !ValidID(id) {
		return nil, Errorf("项目id只能包含字母、数字、下划线和连字符")
	}
	name, ok := project.Value("name").(string)
	if !ok || textutil.Strip(name) == "" {
		return nil, Errorf("项目name不能为空")
	}
	if project.Has("include") {
		return nil, Errorf("include白名单已移除；收录范围是整个注册根，请改用exclude")
	}
	roots, err := ProjectRoots(project)
	if err != nil {
		return nil, err
	}
	types, err := validateTypes(project)
	if err != nil {
		return nil, err
	}
	var excludes []any
	if raw, present := project.Get("exclude"); present {
		list, ok := raw.([]any)
		if !ok {
			return nil, Errorf("exclude必须是相对路径模式数组")
		}
		excludes = list
	}
	for _, item := range excludes {
		text, ok := item.(string)
		if !ok || strings.HasPrefix(text, "/") {
			return nil, Errorf("exclude必须是相对路径模式数组")
		}
	}
	for _, item := range excludes {
		if _, err := RejectDotdot(strings.TrimRight(item.(string), "/"), "exclude"); err != nil {
			return nil, err
		}
	}
	for _, port := range protectedPorts {
		if _, ok := textutil.Int(port); !ok {
			return nil, Errorf("protectedPorts必须是整数数组")
		}
	}
	homeRel, err := RejectDotdot(project.Value("home"), "home")
	if err != nil {
		return nil, err
	}
	root, inner, found := Locate(roots, homeRel)
	if !found {
		return nil, Errorf("home不在任何一个已登记的文档目录内")
	}
	homePath := root.Path
	if inner != "." {
		homePath = textutil.Join(root.Path, inner)
	}
	if !textutil.IsFile(homePath) || textutil.IsSymlink(homePath) || !textutil.IsWithin(homePath, root.Path) {
		return nil, Errorf("home必须是文档目录内的允许文件")
	}
	if _, err := AbsoluteNoSymlink(homePath, "home"); err != nil {
		return nil, err
	}
	if PolicyReason(homeRel, homePath) != "" || !MarkdownExtensions[textutil.Lower(textutil.Suffix(homePath))] {
		return nil, Errorf("home必须是安全的Markdown文件")
	}
	if homeExt := textutil.Lower(textutil.Suffix(homePath)); len(types) > 0 && !contains(types, homeExt) {
		return nil, Errorf("types必须包含首页的扩展名 %s", homeExt)
	}
	return roots, nil
}

func contains(list []string, item string) bool {
	for _, value := range list {
		if value == item {
			return true
		}
	}
	return false
}

// Excludes 返回项目的 exclude 列表（已校验的项目）。
func Excludes(project *ojson.Object) []string {
	list, _ := project.Value("exclude").([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ValidateAll 校验整个配置，返回所有项目的文档目录。
func ValidateAll(cfg *ojson.Object) ([]string, error) {
	if err := ValidateShape(cfg); err != nil {
		return nil, err
	}
	type seenRoot struct{ id, path string }
	var seen []seenRoot
	ids := map[string]bool{}
	for _, item := range Projects(cfg) {
		project, isObj := item.(*ojson.Object)
		roots, err := ValidateProject(item, ProtectedPorts(cfg))
		if err != nil {
			if !isObj {
				return nil, &PanicError{Msg: "AttributeError: project is not an object"}
			}
			if IsConfigError(err) {
				label := project.Value("name")
				if !textutil.Truthy(label) {
					label = project.Value("id")
				}
				return nil, Errorf("项目「%s」：%s", textutil.Str(label), err.Error())
			}
			return nil, err
		}
		id := project.Value("id").(string)
		if ids[id] {
			return nil, Errorf("项目id不能重复")
		}
		ids[id] = true
		for _, root := range roots {
			for _, other := range seen {
				if textutil.IsWithin(root.Path, other.path) || textutil.IsWithin(other.path, root.Path) {
					return nil, Errorf("项目文档目录与已有项目重叠: %s/%s", id, other.id)
				}
			}
			seen = append(seen, seenRoot{id, root.Path})
		}
	}
	out := make([]string, len(seen))
	for i, item := range seen {
		out[i] = item.path
	}
	return out, nil
}

// ValidateOutput 输出目录不能是危险位置，也不能与文档目录互相包含。
// toolRoot 为空表示不检查工具根目录。返回未解析的绝对路径。
func ValidateOutput(output string, sourceRoots []string, toolRoot string) (string, error) {
	path, err := textutil.ExpandUser(output)
	if err != nil {
		return "", err
	}
	path = textutil.Absolute(path)
	if _, err := AbsoluteNoSymlink(path, "输出目录"); err != nil {
		return "", err
	}
	if textutil.IsSymlink(path) {
		return "", Errorf("输出目录不能是符号链接")
	}
	resolved := textutil.Realpath(path)
	if textutil.IsRoot(resolved) || textutil.SamePath(resolved, textutil.Home()) {
		return "", Errorf("输出目录不能是根目录或home目录")
	}
	if toolRoot != "" {
		if textutil.SamePath(resolved, textutil.Realpath(toolRoot)) {
			return "", Errorf("输出目录不能是工具根目录")
		}
	}
	for _, root := range sourceRoots {
		source := textutil.Realpath(root)
		// 生成的站点落进被收录的目录，会被下一次同步当成文档再收录一遍；反过来会被构建覆盖。
		if textutil.IsWithin(resolved, source) {
			return "", Errorf("文档目录 %s 包含了 Vinx Docs 生成站点的位置 %s，不能收录。"+
				"请改填更具体的文档子目录（例如其中的 docs/）。", source, resolved)
		}
		if textutil.IsWithin(source, resolved) {
			return "", Errorf("文档目录 %s 位于 Vinx Docs 生成站点的目录 %s 里，不能收录。", source, resolved)
		}
	}
	return path, nil
}
