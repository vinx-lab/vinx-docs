package textutil

import (
	"os"
	"strings"
	"unicode/utf8"
)

// PosixJoin 用 / 拼接路径：遇到以 / 开头的段就从它重新开始；不做规范化。
func PosixJoin(a string, parts ...string) string {
	path := a
	for _, b := range parts {
		switch {
		case strings.HasPrefix(b, "/"):
			path = b
		case path == "" || strings.HasSuffix(path, "/"):
			path += b
		default:
			path += "/" + b
		}
	}
	return path
}

// PosixNormpath 规范化 POSIX 路径：去掉空段和 "."，按字面折叠 ".."；开头恰好两个 / 时保留。
func PosixNormpath(path string) string {
	if path == "" {
		return "."
	}
	initial := 0
	if strings.HasPrefix(path, "/") {
		initial = 1
		if strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "///") {
			initial = 2
		}
	}
	var comps []string
	for _, comp := range strings.Split(path, "/") {
		if comp == "" || comp == "." {
			continue
		}
		if comp != ".." || (initial == 0 && len(comps) == 0) || (len(comps) > 0 && comps[len(comps)-1] == "..") {
			comps = append(comps, comp)
		} else if len(comps) > 0 {
			comps = comps[:len(comps)-1]
		}
	}
	out := strings.Repeat("/", initial) + strings.Join(comps, "/")
	if out == "" {
		return "."
	}
	return out
}

// PosixDirname 返回最后一个 / 之前的部分（去掉结尾多余的 /，根目录保留）。
func PosixDirname(p string) string {
	i := strings.LastIndex(p, "/") + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// DecodeReplace 按 UTF-8 解码，非法字节换成 U+FFFD。
// 按“最大合法前缀”替换：一段被截断的多字节序列只换成一个 U+FFFD。
func DecodeReplace(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || size > 1 {
			b.WriteRune(r)
			i += size
			continue
		}
		b.WriteRune(utf8.RuneError)
		i += maximalSubpart(data[i:])
	}
	return b.String()
}

// maximalSubpart 返回从非法位置开始、应当整体替换成一个 U+FFFD 的字节数（Unicode 标准推荐的做法）。
func maximalSubpart(p []byte) int {
	c := p[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		need, lo = 2, 0xA0
	case c >= 0xE1 && c <= 0xEC, c == 0xEE, c == 0xEF:
		need = 2
	case c == 0xED:
		need, hi = 2, 0x9F
	case c == 0xF0:
		need, lo = 3, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	case c == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(p); k++ {
		b := p[n]
		if k == 0 {
			if b < lo || b > hi {
				return n
			}
		} else if b < 0x80 || b > 0xBF {
			return n
		}
		n++
	}
	return n
}

// ReadTextReplace 读取文件并按 DecodeReplace 解码，再统一换行。
func ReadTextReplace(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return UniversalNewlines(DecodeReplace(data)), nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Unquote 解码百分号编码（按 UTF-8，非法字节换成 U+FFFD）；格式不对的 % 原样保留。
func Unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			h, ok1 := unhex(s[i+1])
			l, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				out = append(out, h<<4|l)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return DecodeReplace(out)
}

// UnquotePlus 先把 + 换成空格再 Unquote（查询串的写法）。
func UnquotePlus(s string) string { return Unquote(strings.ReplaceAll(s, "+", " ")) }

// ParseQS 解析查询串：只按 & 分隔，值为空的参数丢弃，同名参数按出现顺序收集。
func ParseQS(qs string) map[string][]string {
	out := map[string][]string{}
	for _, field := range strings.Split(qs, "&") {
		if field == "" {
			continue
		}
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if value == "" {
			continue
		}
		key := UnquotePlus(name)
		out[key] = append(out[key], UnquotePlus(value))
	}
	return out
}

// HTMLEscape 转义 &、<、>、" 和 '。
func HTMLEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;").Replace(s)
}

// Stem 返回去掉扩展名的文件名。
func Stem(p string) string {
	name, suffix := Name(p), Suffix(p)
	return name[:len(name)-len(suffix)]
}

// StatInfo 是文件状态里用到的字段。
type StatInfo struct {
	MtimeNS int64
	Size    int64
	Mtime   int64 // 整数秒，由 float64(sec + nsec*1e-9) 截断
	Mode    os.FileMode
}

// Stat 取文件状态（跟随符号链接）。
func Stat(path string) (StatInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return StatInfo{}, err
	}
	mod := info.ModTime()
	sec, nsec := mod.Unix(), int64(mod.Nanosecond())
	return StatInfo{
		MtimeNS: sec*1_000_000_000 + nsec,
		Size:    info.Size(),
		Mtime:   int64(float64(sec) + float64(nsec)*1e-9),
		Mode:    info.Mode(),
	}, nil
}

// SplitWhitespace 按连续空白（IsSpace）切分，丢弃空项。
func SplitWhitespace(s string) []string {
	return strings.FieldsFunc(s, IsSpace)
}

// CollapseSpace 把每段连续空白替换成一个空格。
func CollapseSpace(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if IsSpace(r) {
			if !in {
				b.WriteByte(' ')
			}
			in = true
			continue
		}
		in = false
		b.WriteRune(r)
	}
	return b.String()
}
