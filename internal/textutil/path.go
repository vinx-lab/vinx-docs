package textutil

import (
	"errors"
	"os"
	"os/user"
	"strings"
	"syscall"
)

// 这里的路径函数是纯字面的 POSIX 路径处理：规范化只去掉空段和 "."，保留 ".."
// （filepath.Clean 会按字面折叠 ".."，遇到符号链接时结果可能指向别处，所以不用它）。
// 目前只实现 POSIX 语义，Windows 的盘符与反斜杠规则尚未处理。

// Norm 规范化路径：去掉空段和 "."，保留 ".."；开头恰好两个 / 时保留，空路径返回 "."。
func Norm(p string) string {
	root, tail := splitRoot(p)
	if root == "" && len(tail) == 0 {
		return "."
	}
	return root + strings.Join(tail, "/")
}

func splitRoot(p string) (string, []string) {
	root := ""
	switch {
	case strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///"):
		root = "//"
	case strings.HasPrefix(p, "/"):
		root = "/"
	}
	var tail []string
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			tail = append(tail, part)
		}
	}
	return root, tail
}

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

func IsAbs(p string) bool { return strings.HasPrefix(p, "/") }

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

// Getwd 返回当前目录：直接问内核，不像 os.Getwd 那样优先采用 $PWD。
func Getwd() (string, error) {
	return syscall.Getwd()
}

// Absolute 把相对路径接到当前目录后面并规范化；不解析符号链接。
func Absolute(p string) string {
	if IsAbs(p) {
		return Norm(p)
	}
	cwd, err := Getwd()
	if err != nil {
		cwd, _ = os.Getwd()
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

// IsWithin 报告 p 是否等于 parent 或在它下面（按段做纯字面比较）。
func IsWithin(p, parent string) bool {
	a, b := Parts(p), Parts(parent)
	if len(b) > len(a) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
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

// IsSymlink 报告路径本身是否是符号链接（出错视为否）。
func IsSymlink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&os.ModeSymlink != 0
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
