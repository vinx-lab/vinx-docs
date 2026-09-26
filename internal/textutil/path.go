package textutil

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// 这里的路径函数是纯字面的路径处理：规范化只去掉空段和 "."，保留 ".."
// （filepath.Clean 会按字面折叠 ".."，遇到符号链接时结果可能指向别处，所以不用它）。
//
// 分隔符一律输出 /。Windows 上另外把 \ 当作分隔符，并识别盘符（C:/）和 UNC 根（//server/share/），
// 规范化后形如 C:/Users/x/docs；Go 的文件接口在 Windows 上同样接受 /。项目内的相对路径（发布路径、
// 前缀、exclude）由调用方另行拒绝反斜杠和盘符，不经过这里的 Windows 规则放宽。

// windows 为真时按 Windows 规则解析根；测试里可以临时改成 true 验证纯字符串部分。
var windows = runtime.GOOS == "windows"

// Norm 规范化路径：去掉空段和 "."，保留 ".."；开头恰好两个 / 时保留，空路径返回 "."。
func Norm(p string) string {
	root, tail := splitRoot(p)
	if root == "" && len(tail) == 0 {
		return "."
	}
	return root + strings.Join(tail, "/")
}

func splitRoot(p string) (string, []string) {
	root, rest := "", p
	if windows {
		root, rest = splitWindowsRoot(p)
	} else {
		switch {
		case strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///"):
			root = "//"
		case strings.HasPrefix(p, "/"):
			root = "/"
		}
	}
	var tail []string
	for _, part := range strings.Split(rest, "/") {
		if part != "" && part != "." {
			tail = append(tail, part)
		}
	}
	return root, tail
}

// splitWindowsRoot 把 \ 换成 / 后拆出根：盘符（C: 或 C:/，盘符转成大写）、UNC（//server/share/）
// 或只有开头的 /（当前盘的根）。返回 (根, 其余部分)。
func splitWindowsRoot(p string) (string, string) {
	p = strings.ReplaceAll(p, `\`, "/")
	switch {
	case len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]):
		root, rest := strings.ToUpper(p[:1])+":", p[2:]
		if strings.HasPrefix(rest, "/") {
			root += "/"
		}
		return root, rest
	case strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///"):
		server, after, _ := strings.Cut(p[2:], "/")
		share, rest, _ := strings.Cut(after, "/")
		if server != "" && share != "" {
			return "//" + server + "/" + share + "/", rest
		}
		return "/", p
	case strings.HasPrefix(p, "/"):
		return "/", p
	}
	return "", p
}

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// Parts 返回路径的各段（绝对路径第一项是根）。
func Parts(p string) []string {
	root, tail := splitRoot(p)
	if root != "" {
		return append([]string{root}, tail...)
	}
	return tail
}

// Tail 返回去掉根后的各段。
func Tail(p string) []string {
	_, tail := splitRoot(p)
	return tail
}

// IsAbs 报告是否是绝对路径。Windows 上要求带盘符并以 / 开头（C:/x）或是 UNC 路径；
// 只有开头的 /（当前盘的根）和 C:x（C 盘的当前目录）都不算绝对路径。
func IsAbs(p string) bool {
	if !windows {
		return strings.HasPrefix(p, "/")
	}
	root, _ := splitWindowsRoot(p)
	return len(root) > 1 && strings.HasSuffix(root, "/")
}

// HasRoot 报告路径是否带根：绝对路径，以及 Windows 上的 /x、C:x 这类不完整的根。
// 校验项目内相对路径时用它，而不是 IsAbs。
func HasRoot(p string) bool {
	root, _ := splitRoot(p)
	return root != ""
}

// IsRoot 报告路径是否就是根目录本身（/、C:/、//server/share/）。
func IsRoot(p string) bool {
	root, tail := splitRoot(p)
	return root != "" && len(tail) == 0
}

// SamePath 按段比较两个路径是否相同；Windows 上不区分大小写。
func SamePath(a, b string) bool {
	return IsWithin(a, b) && IsWithin(b, a)
}

// Join 拼接路径并规范化：遇到绝对路径段就从它重新开始。
func Join(base string, parts ...string) string {
	p := base
	for _, part := range parts {
		switch {
		case IsAbs(part):
			p = part
		case p == "":
			p = part
		default:
			p = p + "/" + part
		}
	}
	return Norm(p)
}

// Name 返回最后一段。
func Name(p string) string {
	tail := Tail(p)
	if len(tail) == 0 {
		return ""
	}
	return tail[len(tail)-1]
}

// Parent 返回去掉最后一段后的路径（根目录和 "." 的父目录是自己）。
func Parent(p string) string {
	root, tail := splitRoot(p)
	if len(tail) == 0 {
		return Norm(p)
	}
	return Norm(root + strings.Join(tail[:len(tail)-1], "/"))
}

// Suffix 返回最后一段里最后一个 "." 起的扩展名；以 "." 开头的名字（如 .md）没有扩展名，"a." 的扩展名是 "."。
func Suffix(p string) string {
	name := Name(p)
	i := strings.LastIndex(name, ".")
	if i > 0 {
		return name[i:]
	}
	return ""
}

// Getwd 返回当前目录：直接问内核，不像 os.Getwd 那样优先采用 $PWD。Windows 上规范化成 / 分隔。
func Getwd() (string, error) {
	cwd, err := syscall.Getwd()
	if err == nil && windows {
		cwd = Norm(cwd)
	}
	return cwd, err
}

// Absolute 把相对路径接到当前目录后面并规范化；不解析符号链接。
func Absolute(p string) string {
	if IsAbs(p) {
		return Norm(p)
	}
	cwd, err := Getwd()
	if err != nil {
		cwd, _ = os.Getwd()
		if windows {
			cwd = Norm(cwd)
		}
	}
	if windows {
		switch root, tail := splitRoot(p); root {
		case "":
		case "/": // 当前盘的根
			cwdRoot, _ := splitRoot(cwd)
			return Norm(cwdRoot + "/" + strings.Join(tail, "/"))
		default: // C:x：那个盘上的当前目录
			base, err := filepath.Abs(root)
			if err != nil {
				base = root + "/"
			}
			return Norm(base + "/" + strings.Join(tail, "/"))
		}
	}
	tail := Tail(p)
	if len(tail) == 0 {
		return cwd
	}
	return Norm(cwd + "/" + strings.Join(tail, "/"))
}

// ErrNoHome 表示展开 ~ 时无法确定家目录。
var ErrNoHome = errors.New("Could not determine home directory.")

// ExpandUserString 展开开头的 ~ 或 ~user；无法确定时原样返回。
func ExpandUserString(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	i := strings.Index(path, "/")
	if i < 0 {
		i = len(path)
	}
	if windows {
		return expandUserWindows(path)
	}
	var home string
	if i == 1 {
		if h, ok := os.LookupEnv("HOME"); ok {
			home = h
		} else {
			u, err := user.Current()
			if err != nil {
				return path
			}
			home = u.HomeDir
		}
	} else {
		u, err := user.Lookup(path[1:i])
		if err != nil {
			return path
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + path[i:]; out != "" {
		return out
	}
	return "/"
}

// expandUserWindows 只展开 ~（USERPROFILE，其次 HOMEDRIVE+HOMEPATH、当前用户）；~user 原样返回。
// 与 Python 3.8 起一样不看 HOME。
func expandUserWindows(path string) string {
	i := strings.IndexAny(path, `/\`)
	if i < 0 {
		i = len(path)
	}
	if i != 1 {
		return path
	}
	home := os.Getenv("USERPROFILE")
	if home == "" {
		if drive, rest := os.Getenv("HOMEDRIVE"), os.Getenv("HOMEPATH"); rest != "" {
			home = drive + rest
		} else if u, err := user.Current(); err == nil {
			home = u.HomeDir
		} else {
			return path
		}
	}
	return Norm(home) + path[i:]
}

// ExpandUser 展开开头的 ~ 或 ~user 并规范化；无法确定家目录时返回 ErrNoHome。
func ExpandUser(p string) (string, error) {
	root, tail := splitRoot(p)
	if root == "" && len(tail) > 0 && strings.HasPrefix(tail[0], "~") {
		home := ExpandUserString(tail[0])
		if strings.HasPrefix(home, "~") {
			return "", ErrNoHome
		}
		return Join(home, tail[1:]...), nil
	}
	return Norm(p), nil
}

// Home 返回当前用户的家目录。
func Home() string {
	return Norm(ExpandUserString("~"))
}

// IsWithin 报告 p 是否等于 parent 或在它下面（按段做纯字面比较；Windows 上不区分大小写，
// 与它的文件系统默认行为一致）。
func IsWithin(p, parent string) bool {
	a, b := Parts(p), Parts(parent)
	if len(b) > len(a) {
		return false
	}
	for i := range b {
		if a[i] != b[i] && !(windows && strings.EqualFold(a[i], b[i])) {
			return false
		}
	}
	return true
}

// RelativeTo 返回 p 相对 parent 的 posix 路径（调用方保证 IsWithin）。
func RelativeTo(p, parent string) string {
	a, b := Parts(p), Parts(parent)
	return Norm(strings.Join(a[len(b):], "/"))
}

// IsSymlink 报告路径本身是否是符号链接（出错视为否）。Windows 上目录联接（junction）等其他
// 重解析点同样可以把路径引到别处，Go 把它们报成 ModeIrregular，这里一并算作符号链接。
func IsSymlink(p string) bool {
	info, err := os.Lstat(p)
	if err != nil {
		return false
	}
	mask := os.ModeSymlink
	if windows {
		mask |= os.ModeIrregular
	}
	return info.Mode()&mask != 0
}

// IsDir 报告路径是否是目录（跟随符号链接，出错视为否）。
func IsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// IsFile 报告路径是否是普通文件（跟随符号链接，出错视为否）。
func IsFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// Exists 报告路径是否存在（跟随符号链接）。
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Realpath 解析路径上的全部符号链接，得到绝对路径。不存在的部分按字面保留；遇到符号链接环时
// 停在环所在的路径上，不报错。
func Realpath(filename string) string {
	if windows {
		return realpathWindows(filename)
	}
	const sep = "/"
	var rest []*string
	pushParts := func(s string) int {
		parts := strings.Split(s, sep)
		for i := len(parts) - 1; i >= 0; i-- {
			part := parts[i]
			rest = append(rest, &part)
		}
		return len(parts)
	}
	partCount := pushParts(filename)
	path := sep
	if !strings.HasPrefix(filename, sep) {
		cwd, err := Getwd()
		if err != nil {
			cwd, _ = os.Getwd()
		}
		path = cwd
	}
	type seenValue struct {
		path     string
		resolved bool
	}
	seen := map[string]seenValue{}
	for partCount > 0 {
		top := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		if top == nil {
			link := rest[len(rest)-1]
			rest = rest[:len(rest)-1]
			seen[*link] = seenValue{path: path, resolved: true}
			continue
		}
		name := *top
		partCount--
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			i := strings.LastIndex(path, sep)
			path = path[:i]
			if path == "" {
				path = sep
			}
			continue
		}
		newpath := path + sep + name
		if path == sep {
			newpath = path + name
		}
		info, err := os.Lstat(newpath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if value, ok := seen[newpath]; ok {
			if value.resolved {
				path = value.path
				continue
			}
			path = newpath
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			path = newpath
			continue
		}
		if strings.HasPrefix(target, sep) {
			path = sep
		}
		seen[newpath] = seenValue{}
		link := newpath
		rest = append(rest, &link, nil)
		partCount += pushParts(target)
	}
	// 还有未弹出的 nil 标记时（循环提前结束不会发生），忽略即可。
	return path
}

// realpathWindows 逐段解析：已存在的部分交给 filepath.EvalSymlinks（它同时把 8.3 短名和大小写
// 规范成磁盘上的写法），解析失败（不存在、链接环）的段按字面保留，".." 在已解析的路径上回退一级。
func realpathWindows(filename string) string {
	root, tail := splitRoot(Absolute(filename))
	path := Norm(root)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		path = Norm(resolved)
	}
	for _, name := range tail {
		if name == ".." {
			path = Parent(path)
			continue
		}
		candidate := Join(path, name)
		if _, err := os.Lstat(candidate); err != nil {
			path = candidate
			continue
		}
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			path = Norm(resolved)
		} else {
			path = candidate
		}
	}
	return path
}
