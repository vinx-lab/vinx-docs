package textutil

import "strings"

// 排除规则用的两种通配：FnMatch 对整条路径匹配（区分大小写，* 可以跨 /）；PathMatch 从右往左逐段匹配
// （** 当作普通 *）。支持 *、?、[...]、[!...]；字符类里的反斜杠按字面处理，空区间被去掉，
// 整个字符类都无效时报 BadPatternError。

type tokenKind int

const (
	tokLiteral tokenKind = iota
	tokAny
	tokStar
	tokClass
)

type classRange struct{ lo, hi rune }

type token struct {
	kind   tokenKind
	lit    rune
	neg    bool
	never  bool
	anyRun bool
	ranges []classRange
}

// EmptyPatternError 表示 PathMatch 收到了空模式。
type EmptyPatternError struct{}

func (EmptyPatternError) Error() string { return "empty pattern" }

// BadPatternError 表示模式里的字符类区间无效。
type BadPatternError struct{ Pattern string }

func (e BadPatternError) Error() string { return "bad character range in pattern: " + e.Pattern }

func translate(pat string) []token {
	p := []rune(pat)
	n := len(p)
	var out []token
	i := 0
	for i < n {
		c := p[i]
		i++
		switch c {
		case '*':
			out = append(out, token{kind: tokStar})
			for i < n && p[i] == '*' {
				i++
			}
		case '?':
			out = append(out, token{kind: tokAny})
		case '[':
			j := i
			if j < n && p[j] == '!' {
				j++
			}
			if j < n && p[j] == ']' {
				j++
			}
			for j < n && p[j] != ']' {
				j++
			}
			if j >= n {
				out = append(out, token{kind: tokLiteral, lit: '['})
				continue
			}
			stuff := classStuff(p, i, j)
			i = j + 1
			out = append(out, parseClass(stuff, pat))
		default:
			out = append(out, token{kind: tokLiteral, lit: c})
		}
	}
	return out
}

// classStuff 整理 [..] 的内容：转义反斜杠和连字符、去掉空区间。
func classStuff(p []rune, i, j int) []rune {
	stuff := p[i:j]
	hasDash := false
	for _, r := range stuff {
		if r == '-' {
			hasDash = true
		}
	}
	escape := func(s []rune, dash bool) []rune {
		var out []rune
		for _, r := range s {
			if r == '\\' || (dash && r == '-') {
				out = append(out, '\\')
			}
			out = append(out, r)
		}
		return out
	}
	if !hasDash {
		return escape(stuff, false)
	}
	var chunks [][]rune
	k := i + 1
	if p[i] == '!' {
		k = i + 2
	}
	find := func(from int) int {
		for x := from; x < j; x++ {
			if p[x] == '-' {
				return x
			}
		}
		return -1
	}
	for {
		k = find(k)
		if k < 0 {
			break
		}
		chunks = append(chunks, append([]rune(nil), p[i:k]...))
		i = k + 1
		k = k + 3
	}
	chunk := append([]rune(nil), p[i:j]...)
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
	} else {
		chunks[len(chunks)-1] = append(chunks[len(chunks)-1], '-')
	}
	for k := len(chunks) - 1; k > 0; k-- {
		prev, cur := chunks[k-1], chunks[k]
		if len(prev) > 0 && len(cur) > 0 && prev[len(prev)-1] > cur[0] {
			merged := append(append([]rune(nil), prev[:len(prev)-1]...), cur[1:]...)
			chunks[k-1] = merged
			chunks = append(chunks[:k], chunks[k+1:]...)
		}
	}
	var out []rune
	for idx, ch := range chunks {
		if idx > 0 {
			out = append(out, '-')
		}
		out = append(out, escape(ch, true)...)
	}
	return out
}

// parseClass 把整理后的字符类内容解释成区间集合。
func parseClass(stuff []rune, pat string) token {
	if len(stuff) == 0 {
		return token{kind: tokClass, never: true}
	}
	if len(stuff) == 1 && stuff[0] == '!' {
		return token{kind: tokClass, anyRun: true}
	}
	t := token{kind: tokClass}
	body := stuff
	if stuff[0] == '!' {
		t.neg = true
		body = stuff[1:]
	}
	// 读出一个元素：反斜杠转义或普通字符。
	type elem struct {
		r       rune
		escaped bool
	}
	var elems []elem
	for x := 0; x < len(body); x++ {
		if body[x] == '\\' && x+1 < len(body) {
			elems = append(elems, elem{body[x+1], true})
			x++
			continue
		}
		elems = append(elems, elem{body[x], false})
	}
	for x := 0; x < len(elems); x++ {
		e := elems[x]
		if x+2 < len(elems) && elems[x+1].r == '-' && !elems[x+1].escaped {
			lo, hi := e.r, elems[x+2].r
			if lo > hi {
				panic(BadPatternError{pat})
			}
			t.ranges = append(t.ranges, classRange{lo, hi})
			x += 2
			continue
		}
		t.ranges = append(t.ranges, classRange{e.r, e.r})
	}
	return t
}

func (t token) matches(r rune) bool {
	switch t.kind {
	case tokLiteral:
		return r == t.lit
	case tokAny:
		return true
	case tokClass:
		if t.never {
			return false
		}
		if t.anyRun {
			return true
		}
		in := false
		for _, rg := range t.ranges {
			if rg.lo <= r && r <= rg.hi {
				in = true
				break
			}
		}
		return in != t.neg
	}
	return false
}

func matchTokens(tokens []token, name []rune) bool {
	ti, ni := 0, 0
	starT, starN := -1, 0
	for ni < len(name) {
		if ti < len(tokens) && tokens[ti].kind != tokStar && tokens[ti].matches(name[ni]) {
			ti++
			ni++
			continue
		}
		if ti < len(tokens) && tokens[ti].kind == tokStar {
			starT, starN = ti, ni
			ti++
			continue
		}
		if starT >= 0 {
			starN++
			ni = starN
			ti = starT + 1
			continue
		}
		return false
	}
	for ti < len(tokens) && tokens[ti].kind == tokStar {
		ti++
	}
	return ti == len(tokens)
}

// FnMatch 对整条路径做通配匹配，区分大小写，* 可以跨 /。
func FnMatch(name, pat string) bool {
	return matchTokens(translate(pat), []rune(name))
}

// PathMatch 从右往左逐段匹配：模式以 / 开头时必须整条路径匹配，否则只需匹配路径的末尾几段；
// 空模式会 panic(EmptyPatternError)。
func PathMatch(path, pat string) bool {
	patParts := Parts(pat)
	if len(patParts) == 0 {
		panic(EmptyPatternError{})
	}
	pathParts := Parts(path)
	if len(pathParts) < len(patParts) {
		return false
	}
	anchored := IsAbs(pat)
	if len(pathParts) > len(patParts) && anchored {
		return false
	}
	for i := 1; i <= len(patParts); i++ {
		part := pathParts[len(pathParts)-i]
		pp := patParts[len(patParts)-i]
		if strings.HasPrefix(pp, "/") { // 根本身：相对路径的段不可能等于它
			if part != pp {
				return false
			}
			continue
		}
		if !matchTokens(translate(pp), []rune(part)) {
			return false
		}
	}
	return true
}
