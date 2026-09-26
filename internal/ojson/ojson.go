// Package ojson 提供保留键顺序的 JSON 读写，以及固定的输出格式。
//
// 配置、页面登记、构建报告和接口响应都用这里的格式输出：键按插入顺序、不转义 <>&、
// 紧凑模式分隔符是 ", " 和 ": "、浮点数用最短往返表示（1e+16、1e-05、1000000000000000.0 这类写法）。
// 已有的配置文件、增量缓存和前端都依赖这个格式；配置文件里还可能有本工具不认识的字段，
// 读写一遍必须原样保留，所以这里用有序对象而不是 map，也不直接用 encoding/json 输出。
package ojson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Number 是 JSON 数字的原文，整数/浮点的区分和大整数都不丢失。
type Number string

// IsInt 报告数字原文是否是整数字面量（不含小数点和指数）。
func (n Number) IsInt() bool {
	return !strings.ContainsAny(string(n), ".eE")
}

// BigInt 返回整数字面量的值。
func (n Number) BigInt() (*big.Int, bool) {
	if !n.IsInt() {
		return nil, false
	}
	v, ok := new(big.Int).SetString(string(n), 10)
	return v, ok
}

// Float 返回数值的 float64（越界为 ±Inf）。
func (n Number) Float() float64 {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && errors.Is(ne.Err, strconv.ErrRange) {
			return f
		}
		return math.NaN()
	}
	return f
}

// Object 是保留键顺序的 JSON 对象：对已有键重新赋值时保留原位置。
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject 按给定的键值对顺序创建对象：NewObject("a", 1, "b", 2)。
func NewObject(pairs ...any) *Object {
	o := &Object{vals: map[string]any{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}

func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Value 返回键的值，不存在时为 nil。
func (o *Object) Value(key string) any {
	v, _ := o.Get(key)
	return v
}

func (o *Object) Has(key string) bool {
	_, ok := o.Get(key)
	return ok
}

func (o *Object) Set(key string, value any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func (o *Object) Delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Copy 是浅拷贝。
func (o *Object) Copy() *Object {
	c := &Object{keys: append([]string(nil), o.keys...), vals: make(map[string]any, len(o.vals))}
	for k, v := range o.vals {
		c.vals[k] = v
	}
	return c
}

// DeepCopy 递归复制对象和数组。
func DeepCopy(v any) any {
	switch x := v.(type) {
	case *Object:
		c := &Object{keys: append([]string(nil), x.keys...), vals: make(map[string]any, len(x.vals))}
		for k, item := range x.vals {
			c.vals[k] = DeepCopy(item)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, item := range x {
			c[i] = DeepCopy(item)
		}
		return c
	default:
		return v
	}
}

// Decode 解析 JSON 文本，对象保持键顺序，数字保留为 Number。
func Decode(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("extra data")
	}
	return value, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, errors.New("object key must be string")
				}
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			list := []any{}
			for dec.More() {
				value, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	case json.Number:
		return Number(t), nil
	default:
		return t, nil // string, bool, nil
	}
}

// Dumps 按本包的固定格式输出 JSON，非 ASCII 字符原样保留；indent<0 表示紧凑单行。
func Dumps(value any, indent int) string {
	var b strings.Builder
	encode(&b, value, indent, 0)
	return b.String()
}

// DumpsASCII 同 Dumps，但非 ASCII 字符写成 \uXXXX（BMP 以外写成代理对）。
func DumpsASCII(value any, indent int) string {
	var b strings.Builder
	for _, r := range Dumps(value, indent) {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return b.String()
}

func encode(b *strings.Builder, value any, indent, level int) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, v)
	case Number:
		b.WriteString(formatNumber(v))
	case int:
		b.WriteString(strconv.Itoa(v))
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case float64:
		b.WriteString(FloatRepr(v))
	case []string:
		items := make([]any, len(v))
		for i, s := range v {
			items[i] = s
		}
		encode(b, items, indent, level)
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
				if indent < 0 {
					b.WriteByte(' ')
				}
			}
			newline(b, indent, level+1)
			encode(b, item, indent, level+1)
		}
		newline(b, indent, level)
		b.WriteByte(']')
	case *Object:
		if v.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, key := range v.keys {
			if i > 0 {
				b.WriteByte(',')
				if indent < 0 {
					b.WriteByte(' ')
				}
			}
			newline(b, indent, level+1)
			writeString(b, key)
			b.WriteString(": ")
			encode(b, v.vals[key], indent, level+1)
		}
		newline(b, indent, level)
		b.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := NewObject()
		for _, k := range keys {
			obj.Set(k, v[k])
		}
		encode(b, obj, indent, level)
	default:
		panic(fmt.Sprintf("ojson: unsupported type %T", value))
	}
}

func newline(b *strings.Builder, indent, level int) {
	if indent < 0 {
		return
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(" ", indent*level))
}

// writeString 输出 JSON 字符串：只转义引号、反斜杠和控制字符。
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func formatNumber(n Number) string {
	if v, ok := n.BigInt(); ok {
		return v.String()
	}
	return FloatRepr(n.Float())
}

// FloatRepr 把浮点数写成最短往返文本：指数在 [-4, 16) 内用定点写法（整数值带 .0），否则用 1e+16 形式；
// inf/nan 输出 Infinity/NaN。
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mantissa, expText, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expText)
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	decpt := exp + 1
	if -4 < decpt && decpt <= 16 {
		switch {
		case decpt <= 0:
			return sign + "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			return sign + digits[:decpt] + "." + digits[decpt:]
		}
	}
	out := digits[:1]
	if len(digits) > 1 {
		out += "." + digits[1:]
	}
	e := decpt - 1
	if e < 0 {
		return fmt.Sprintf("%s%se-%02d", sign, out, -e)
	}
	return fmt.Sprintf("%s%se+%02d", sign, out, e)
}
