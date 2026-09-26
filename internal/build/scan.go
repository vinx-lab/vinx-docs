package build

import (
	"regexp"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

const (
	contentScanLimit   = 512 * 1024
	searchExcerptLimit = 1200
)

// 高置信度内容规则直接拦截导出；低置信度只在同步结果里提示，避免静默藏文档。
var blockingContentRE = regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----`)

var suspectKeywords = []string{"password", "passwd", "secret", "token", "api?key", "access?key", "private?key"}

// ContentReason 返回（拦截原因，提示原因）。只看开头部分，不输出命中内容。
// size 是文件大小，data 是文件内容（size 超过上限时可以为 nil）。
func ContentReason(size int64, data []byte) (string, string) {
	if size > contentScanLimit {
		return "", ""
	}
	text, ok := textutil.DecodeText(data)
	if !ok {
		return "", ""
	}
	if blockingContentRE.MatchString(text) {
		return "文件内含私钥", ""
	}
	if suspectSearch(text) {
		return "", "疑似明文口令或密钥"
	}
	return "", ""
}

// suspectSearch 判断文本里是否有疑似明文口令，规则等价于正则
//
//	(?im)^[^\n#]{0,80}\b(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)\b
//	\s*[:=]\s*["']?(?!\s|$|\$\{|<|\{\{|changeme|example|your[_-]|xxx|\*{3})\S{6,}
//
// RE2 不支持否定前瞻，这里按回溯语义逐一穷举可能的匹配位置。
func suspectSearch(text string) bool {
	t := []rune(text)
	n := len(t)
	for s := 0; s <= n; s++ {
		if s > 0 && t[s-1] != '\n' {
			continue
		}
		run := 0
		for run < 80 && s+run < n && t[s+run] != '\n' && t[s+run] != '#' {
			run++
		}
		for pre := run; pre >= 0; pre-- {
			p := s + pre
			if p < n && textutil.IsWord(t[p]) && (p == 0 || !textutil.IsWord(t[p-1])) {
				for _, keyword := range suspectKeywords {
					for _, q := range keywordEnds(t, p, keyword) {
						if (q >= n || !textutil.IsWord(t[q])) && valueFollows(t, q) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// keywordEnds 返回关键字（忽略大小写，"?" 代表可选的 _ 或 -）在 p 处匹配后的所有结束位置。
func keywordEnds(t []rune, p int, keyword string) []int {
	ends := []int{p}
	for _, k := range keyword {
		var next []int
		for _, pos := range ends {
			if k == '?' {
				next = append(next, pos)
				if pos < len(t) && (t[pos] == '_' || t[pos] == '-') {
					next = append(next, pos+1)
				}
				continue
			}
			if pos < len(t) && foldEq(t[pos], k) {
				next = append(next, pos+1)
			}
		}
		ends = next
	}
	return ends
}

func foldEq(a, b rune) bool {
	return strings.EqualFold(string(a), string(b))
}

// valueFollows 检查关键字之后的 \s*[:=]\s*["']?(?!...)\S{6,}。
func valueFollows(t []rune, q int) bool {
	n := len(t)
	i := q
	for i < n && textutil.IsSpace(t[i]) {
		i++
	}
	if i >= n || (t[i] != ':' && t[i] != '=') {
		return false
	}
	i++
	for i < n && textutil.IsSpace(t[i]) {
		i++
	}
	candidates := []int{i}
	if i < n && (t[i] == '"' || t[i] == '\'') {
		candidates = append(candidates, i+1)
	}
	for _, r := range candidates {
		if !lookaheadBlocks(t, r) && nonSpaceRun(t, r) >= 6 {
			return true
		}
	}
	return false
}

func lookaheadBlocks(t []rune, r int) bool {
	n := len(t)
	if r >= n || t[r] == '\n' || textutil.IsSpace(t[r]) {
		return true
	}
	rest := t[r:]
	has := func(prefix string, fold bool) bool {
		pr := []rune(prefix)
		if len(rest) < len(pr) {
			return false
		}
		for i, c := range pr {
			if fold {
				if !foldEq(rest[i], c) {
					return false
				}
			} else if rest[i] != c {
				return false
			}
		}
		return true
	}
	for _, literal := range []string{"${", "<", "{{", "***"} {
		if has(literal, false) {
			return true
		}
	}
	for _, word := range []string{"changeme", "example", "your_", "your-", "xxx"} {
		if has(word, true) {
			return true
		}
	}
	return false
}

func nonSpaceRun(t []rune, r int) int {
	count := 0
	for r+count < len(t) && !textutil.IsSpace(t[r+count]) {
		count++
		if count >= 6 {
			break
		}
	}
	return count
}

var spaceClass = `[\t\n\x0b\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

var titleRE = regexp.MustCompile(`^` + spaceClass + `{0,3}#{1,6}` + spaceClass + `+(.+?)` + spaceClass + `*#*` + spaceClass + `*$`)

// Title 只有 Markdown 才用首个标题当标题。
func Title(data []byte, fallback, kind string) string {
	if kind != "markdown" {
		return fallback
	}
	text, ok := textutil.DecodeText(data)
	if !ok {
		return fallback
	}
	for _, line := range textutil.SplitLines(text) {
		if m := titleRE.FindStringSubmatch(line); m != nil {
			return textutil.Strip(m[1])
		}
	}
	return fallback
}

var (
	fenceRE       = regexp.MustCompile("(?s)```.*?```")
	punctuationRE = regexp.MustCompile("[#>*`_\\[\\]()|-]+")
)

// SearchTerms 抽取用于跨项目搜索的纯文本片段。
func SearchTerms(data []byte, kind string) string {
	if kind != "markdown" && kind != "text" {
		return ""
	}
	text, ok := textutil.DecodeText(data)
	if !ok {
		return ""
	}
	text = textutil.Head(text, searchExcerptLimit)
	text = fenceRE.ReplaceAllLiteralString(text, " ")
	text = punctuationRE.ReplaceAllLiteralString(text, " ")
	var b strings.Builder
	inSpace := false
	for _, r := range text {
		if textutil.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
			}
			inSpace = true
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return textutil.Head(textutil.Strip(b.String()), searchExcerptLimit)
}

// PreviewText 把文本文件包成 Markdown 代码块；不是 UTF-8 时返回 false。
func PreviewText(data []byte, name string) (string, bool) {
	text, ok := textutil.DecodeText(data)
	if !ok {
		return "", false
	}
	maxTicks, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			if run > maxTicks {
				maxTicks = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, maxTicks+1))
	return "# " + name + "\n\n" + fence + "text\n" + text + "\n" + fence + "\n", true
}
