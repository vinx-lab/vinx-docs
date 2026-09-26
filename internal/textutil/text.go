// Package textutil 是构建和服务共用的文本、路径、通配和 URL 小工具：空白判断、纯字面的路径规范化、
// 排除规则的通配匹配、百分号编码等。这些函数的边缘语义是固定的：结果会写进生成的站点、
// 增量缓存、接口响应和报错文案，改动会让已有数据与前端的约定失配。
package textutil

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

// IsSpace 判断单个字符是否算空白：Unicode 空白，再加 U+001C–U+001F 四个分隔符。
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// IsWord 判断是否是词字符：下划线、Unicode 字母或数字。
func IsWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// Strip 去掉首尾的空白（按 IsSpace）。
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// Len 返回字符串的码点数。
func Len(s string) int { return utf8.RuneCountInString(s) }

// Head 返回 s[:n]（按码点截取）。
func Head(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// UniversalNewlines 把 \r\n 和单独的 \r 统一成 \n。
func UniversalNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// DecodeText 严格按 UTF-8 解码（不合法返回 false），并统一换行。
func DecodeText(data []byte) (string, bool) {
	if !utf8.Valid(data) {
		return "", false
	}
	return UniversalNewlines(string(data)), true
}

// SplitLines 按所有 Unicode 行边界（\n、\r、\r\n、\v、\f、U+001C–U+001E、U+0085、U+2028、U+2029）切行，不保留行尾。
func SplitLines(s string) []string {
	var lines []string
	start := 0
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, string(runes[start:i]))
			start = i + 1
		case '\r':
			lines = append(lines, string(runes[start:i]))
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// Lower 转小写；U+0130（带点大写 I）展开成 "i" 加组合点 U+0307。
func Lower(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0x130 {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Quote 对 UTF-8 字节做百分号编码：字母、数字、_.-~ 和 safe 里的字符保留原样。
func Quote(s, safe string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 && (('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') ||
			c == '_' || c == '.' || c == '-' || c == '~' || strings.IndexByte(safe, c) >= 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Str 把 JSON 值写进报错文案：字符串原样，其他值用 Repr。
func Str(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return Repr(value)
}

// Repr 把 JSON 值写成报错文案里的字面量：字符串加单引号并转义，null/true/false 写成 None/True/False，
// 数组和对象用 [..]、{..}。已有的报错文案沿用这种写法。
func Repr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case string:
		quote := "'"
		if strings.Contains(v, "'") && !strings.Contains(v, `"`) {
			quote = `"`
		}
		var b strings.Builder
		b.WriteString(quote)
		for _, r := range v {
			switch {
			case r == '\\':
				b.WriteString(`\\`)
			case string(r) == quote:
				b.WriteString(`\` + quote)
			case r == '\n':
				b.WriteString(`\n`)
			case r == '\r':
				b.WriteString(`\r`)
			case r == '\t':
				b.WriteString(`\t`)
			case !unicode.IsPrint(r) && r != ' ':
				if r < 0x100 {
					fmt.Fprintf(&b, `\x%02x`, r)
				} else if r < 0x10000 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					fmt.Fprintf(&b, `\U%08x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
		b.WriteString(quote)
		return b.String()
	case ojson.Number:
		if n, ok := v.BigInt(); ok {
			return n.String()
		}
		return ojson.FloatRepr(v.Float())
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return ojson.FloatRepr(v)
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *ojson.Object:
		parts := make([]string, 0, v.Len())
		for _, k := range v.Keys() {
			parts = append(parts, Repr(k)+": "+Repr(v.Value(k)))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(value)
}

// Truthy 判断 JSON 值是否算“有值”：null、false、0、空串、空数组、空对象为假，其余为真。
func Truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case ojson.Number:
		if n, ok := v.BigInt(); ok {
			return n.Sign() != 0
		}
		return v.Float() != 0
	case []any:
		return len(v) > 0
	case *ojson.Object:
		return v.Len() > 0
	}
	return true
}

// Int 返回 JSON 整数字面量的值；小数和布尔值都不算整数。
func Int(value any) (*big.Int, bool) {
	if n, ok := value.(ojson.Number); ok {
		return n.BigInt()
	}
	return nil, false
}

// EqualsInt 判断 JSON 值是否在数值上等于 n：true 等于 1、false 等于 0、2.0 等于 2。
func EqualsInt(value any, n int64) bool {
	switch v := value.(type) {
	case bool:
		if v {
			return n == 1
		}
		return n == 0
	case ojson.Number:
		if i, ok := v.BigInt(); ok {
			return i.Cmp(big.NewInt(n)) == 0
		}
		return cmpFloatInt(v.Float(), n)
	}
	return false
}

func cmpFloatInt(f float64, n int64) bool {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return false
	}
	bf := new(big.Float).SetFloat64(f)
	return bf.Cmp(new(big.Float).SetInt64(n)) == 0
}
