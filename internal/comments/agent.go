package comments

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/files"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

func isMarkup(r rune) bool { return r == '*' || r == '_' || r == '`' || r == '~' }

// normalized 去掉常见 Markdown 标记、压缩空白，同时记下每个字符在原文里的位置。
func normalized(text []rune) ([]rune, []int) {
	var chars []rune
	var index []int
	space := false
	for pos, char := range text {
		if isMarkup(char) {
			continue
		}
		if textutil.IsSpace(char) {
			if space || len(chars) == 0 {
				continue
			}
			space = true
			chars = append(chars, ' ')
		} else {
			space = false
			chars = append(chars, char)
		}
		index = append(index, pos)
	}
	return chars, index
}

func findAll(text, needle []rune) []int {
	var out []int
	if len(needle) == 0 {
		return out
	}
	for start := 0; start+len(needle) <= len(text); start++ {
		match := true
		for k := range needle {
			if text[start+k] != needle[k] {
				match = false
				break
			}
		}
		if match {
			out = append(out, start)
		}
	}
	return out
}

func countNewlines(text []rune, end int) int {
	n := 0
	for _, r := range text[:end] {
		if r == '\n' {
			n++
		}
	}
	return n
}

func tailMatch(text, prefix []rune) int {
	count := 0
	for count < min(len(text), len(prefix)) && text[len(text)-1-count] == prefix[len(prefix)-1-count] {
		count++
	}
	return count
}

func headMatch(text, suffix []rune) int {
	count := 0
	for count < min(len(text), len(suffix)) && text[count] == suffix[count] {
		count++
	}
	return count
}

func anchorText(anchor *ojson.Object, key string) string {
	s, _ := anchor.Value(key).(string)
	return s
}

// LocateText 在源文件里重新找选中的原文，返回行号（从 1 开始）；找不到返回 0。
func LocateText(sourceText string, anchor *ojson.Object) int {
	quote := anchorText(anchor, "quote")
	if textutil.Strip(quote) == "" {
		return 0
	}
	source := []rune(sourceText)
	if candidates := findAll(source, []rune(quote)); len(candidates) == 1 {
		return countNewlines(source, candidates[0]) + 1
	}
	normal, index := normalized(source)
	target, _ := normalized([]rune(quote))
	prefix, _ := normalized([]rune(anchorText(anchor, "prefix")))
	suffix, _ := normalized([]rune(anchorText(anchor, "suffix")))
	var hits []int
	if len(target) > 0 {
		hits = findAll(normal, target)
	}
	if len(hits) > 1 && (len(prefix) > 0 || len(suffix) > 0) {
		best, bestScore := hits[0], -1
		for _, at := range hits {
			score := tailMatch(normal[:at], prefix) + headMatch(normal[min(at+len(target), len(normal)):], suffix)
			if score > bestScore {
				best, bestScore = at, score
			}
		}
		hits = []int{best}
	}
	if len(hits) == 0 && (len(prefix) > 0 || len(suffix) > 0) {
		tail := prefix[max(0, len(prefix)-24):]
		head := suffix[:min(24, len(suffix))]
		for _, probe := range []struct {
			text   []rune
			offset int
		}{{tail, len(tail)}, {head, 0}} {
			var found []int
			if textutil.Strip(string(probe.text)) != "" {
				found = findAll(normal, probe.text)
			}
			if len(found) == 1 {
				shift := 0
				if probe.offset == 0 {
					shift = len(target)
				}
				hits = []int{max(0, found[0]+probe.offset-shift)}
				break
			}
		}
	}
	if len(hits) == 0 {
		return 0
	}
	original := 0
	if len(index) > 0 {
		original = index[min(hits[0], len(index)-1)]
	}
	return countNewlines(source, original) + 1
}

// Describe 汇总 agent 处理一条批注需要的全部信息：源文件、行号、定位、批注和回复。
func Describe(paths files.Paths, comment *ojson.Object) *ojson.Object {
	info := ojson.NewObject("id", comment.Value("id"), "status", comment.Value("status"),
		"statusLabel", comment.Value("statusLabel"), "target", comment.Value("target"), "anchor", comment.Value("anchor"),
		"body", comment.Value("body"), "replies", comment.Value("replies"), "claimedBy", comment.Value("claimed_by"))
	source, where, err := files.Resolve(paths, comment.Value("target"))
	if err != nil {
		info.Set("source", nil)
		info.Set("targetError", err.Error())
		return info
	}
	info.Set("source", source)
	info.Set("url", where.Value("url"))
	info.Set("title", where.Value("title"))
	anchor, _ := comment.Value("anchor").(*ojson.Object)
	if anchor.Value("type") == "text" {
		text := ""
		if data, err := os.ReadFile(source); err == nil {
			if decoded, ok := textutil.DecodeText(data); ok {
				text = decoded
			}
		}
		if line := LocateText(text, anchor); line > 0 {
			info.Set("line", int64(line))
			info.Set("anchorLost", false)
		} else {
			info.Set("line", nil)
			info.Set("anchorLost", true)
		}
	}
	return info
}

func show(v any) string { return textutil.Str(v) }

// Format 给 agent 看的文本输出，用户写的内容明确标成数据。
func Format(info *ojson.Object, base string) string {
	anchor, _ := info.Value("anchor").(*ojson.Object)
	head := fmt.Sprintf("批注 #%s  [%s]", show(info.Value("id")), show(info.Value("statusLabel")))
	if textutil.Truthy(info.Value("claimedBy")) {
		head += "  领取：" + show(info.Value("claimedBy"))
	}
	lines := []string{head}
	if textutil.Truthy(info.Value("source")) {
		where := show(info.Value("source"))
		if textutil.Truthy(info.Value("line")) {
			where += ":" + show(info.Value("line"))
		}
		lines = append(lines, "源文件："+where, "页面："+base+show(info.Value("url")))
	} else {
		lines = append(lines, "目标已不存在："+show(info.Value("target"))+"（"+anchorText(info, "targetError")+"）")
	}
	switch kind := anchor.Value("type"); kind {
	case "text":
		if textutil.Truthy(info.Value("anchorLost")) {
			lines = append(lines, "位置已失效：源文件里找不到选中的原文，下面是批注时的原文，请自行判断。")
		}
		if h := anchorText(anchor, "heading"); h != "" {
			lines = append(lines, "小节："+h)
		}
		lines = append(lines, "选中原文："+anchorText(anchor, "quote"))
		if anchorText(anchor, "prefix") != "" || anchorText(anchor, "suffix") != "" {
			lines = append(lines, "上下文：…"+anchorText(anchor, "prefix")+"【"+anchorText(anchor, "quote")+"】"+anchorText(anchor, "suffix")+"…")
		}
	case "element", "region":
		if p := anchorText(anchor, "page"); p != "" {
			lines = append(lines, "页面文件："+p)
		}
		if s := anchorText(anchor, "selector"); s != "" {
			lines = append(lines, "元素选择器："+s)
		}
		rect, _ := anchor.Value("rect").(*ojson.Object)
		view, _ := anchor.Value("viewport").(*ojson.Object)
		if rect.Len() > 0 {
			label := "元素位置"
			if kind == "region" {
				label = "框选区域"
			}
			line := fmt.Sprintf("%s：x=%s y=%s 宽%s 高%s", label, show(rect.Value("x")), show(rect.Value("y")),
				show(rect.Value("w")), show(rect.Value("h")))
			if view.Len() > 0 {
				line += fmt.Sprintf("（窗口 %s×%s）", show(view.Value("w")), show(view.Value("h")))
			}
			lines = append(lines, line)
		}
		if t := anchorText(anchor, "text"); t != "" {
			lines = append(lines, "元素文字："+t)
		}
		if s := anchorText(anchor, "snippet"); s != "" {
			lines = append(lines, "HTML片段："+s)
		}
	}
	lines = append(lines, DataNotice, "批注："+show(info.Value("body")))
	replies, _ := info.Value("replies").([]any)
	for _, item := range replies {
		reply := item.(*ojson.Object)
		when := time.Unix(intOf(reply.Value("created_at")), 0).Format("01-02 15:04")
		lines = append(lines, "回复（"+show(reply.Value("author"))+"，"+when+"）："+show(reply.Value("body")))
	}
	return strings.Join(lines, "\n")
}

// AgentName VINX_AGENT_NAME 优先，否则 <agent>@<当前目录名>。
func AgentName(agent string) string {
	if name := os.Getenv("VINX_AGENT_NAME"); name != "" {
		return name
	}
	cwd, _ := textutil.Getwd()
	return agent + "@" + textutil.Name(cwd)
}

// WatchOptions 是 Watch 的参数；Stop 关闭时退出（例如收到 SIGTERM/SIGINT）。
type WatchOptions struct {
	Agent    string
	Name     string
	Scopes   []string
	Interval time.Duration
	Once     bool
	Out      io.Writer // 每条投递一行
	Log      io.Writer // 启动提示（命令行里写到 stderr）
	Stop     <-chan struct{}
}

// Watch 每有一条批注交给本会话，就输出一行；Claude Code 用 Monitor 挂着它，每行唤醒一次会话。
// 信号处理由调用方负责：收到 SIGTERM/SIGINT 时关闭 opts.Stop。
func (c *Conn) Watch(paths files.Paths, opts WatchOptions) error {
	subID, err := c.Subscribe(opts.Agent, opts.Name, opts.Scopes, "")
	if err != nil {
		return err
	}
	defer c.Unsubscribe(subID)
	matches := ScopeMatcher(paths)
	if opts.Log != nil {
		fmt.Fprintf(opts.Log, "vinx-docs watch：%s 开始监听 %s\n", opts.Name, strings.Join(opts.Scopes, " "))
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	for {
		select {
		case <-opts.Stop:
			return nil
		default:
		}
		if err := c.Heartbeat(subID); err != nil {
			return err
		}
		taken, err := c.TakeDeliveries(subID, opts.Scopes, matches)
		if err != nil {
			return err
		}
		for _, comment := range taken {
			summary := textutil.Head(textutil.CollapseSpace(strOf(comment.Value("body"))), 80)
			fmt.Fprintf(opts.Out, "Vinx Docs 批注 #%s 交给你处理：%s「%s」→ 运行 vinx-docs comment %s 查看详情\n",
				show(comment.Value("id")), show(comment.Value("target")), summary, show(comment.Value("id")))
		}
		if opts.Once {
			return nil
		}
		select {
		case <-opts.Stop:
			return nil
		case <-time.After(opts.Interval):
		}
	}
}
