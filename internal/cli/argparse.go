package cli

// 一个小型参数解析器，只覆盖 vinx-docs 各命令用到的写法：位置参数、选项、子命令、append、choices、
// 前缀缩写。用法、帮助和报错文案沿用常见的 argparse 风格（usage:/options:，宽度 80 列），
// 输出固定、不带颜色，方便脚本和 agent 解析。

import (
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Arg 是一个参数定义。
type Arg struct {
	Flag     string   // "--config"；位置参数为空
	Dest     string   // 结果里的键；位置参数同时是显示名
	Metavar  string   // 为空时由 dest 推出（选项用大写）
	Action   string   // "store"（默认）、"append"、"store_true"
	Type     string   // ""（字符串）、"int"、"float"、"path"
	Choices  []string // 非空时限制取值
	Required bool
	Help     string
	Suppress bool // 不出现在用法和帮助里
	Default  any  // 未给出时的值；nil 表示没有值
	NoDest   bool // 未给出时结果里没有这个键
}

// Parser 是一个命令（或子命令）的参数解析器。
type Parser struct {
	Prog     string
	Args     []*Arg
	Commands []*Command // 非空时有子命令（结果键为 command，必须给出）
}

// Command 是一个子命令。
type Command struct {
	Name   string
	Parser *Parser
}

// Values 是解析结果。
type Values map[string]any

func (v Values) Str(key string) string {
	s, _ := v[key].(string)
	return s
}

func (v Values) Bool(key string) bool {
	b, _ := v[key].(bool)
	return b
}

func (v Values) List(key string) []string {
	list, _ := v[key].([]string)
	return list
}

// Has 报告键是否存在且有值（不是 nil）。
func (v Values) Has(key string) bool { return v[key] != nil }

// exit 表示解析结束、应当以给定退出码返回（帮助或报错已经输出）。
type exit struct{ code int }

var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

const textWidth = 78 // 按 80 列终端排版，留 2 列边距

func (a *Arg) positional() bool { return a.Flag == "" }

func (a *Arg) metavar() string {
	if a.Metavar != "" {
		return a.Metavar
	}
	if len(a.Choices) > 0 {
		return "{" + strings.Join(a.Choices, ",") + "}"
	}
	if a.positional() {
		return a.Dest
	}
	return strings.ToUpper(a.Dest)
}

// name 是报错里用来指代参数的名字：选项取全部写法，位置参数取 metavar 或 dest。
func (a *Arg) name() string {
	if a.positional() {
		return a.metavar()
	}
	return a.Flag
}

func (a *Arg) takesValue() bool { return a.Action != "store_true" }

func (p *Parser) helpArg() *Arg {
	return &Arg{Flag: "-h, --help", Help: "show this help message and exit", Action: "store_true"}
}

// usageParts 返回 (可选参数部分, 位置参数部分)。
func (p *Parser) usageParts() ([]string, []string) {
	opts := []string{"[-h]"}
	var pos []string
	for _, a := range p.Args {
		if a.Suppress {
			continue
		}
		if a.positional() {
			pos = append(pos, a.metavar())
			continue
		}
		part := a.Flag
		if a.takesValue() {
			part += " " + a.metavar()
		}
		if !a.Required {
			part = "[" + part + "]"
		}
		opts = append(opts, part)
	}
	if len(p.Commands) > 0 {
		names := make([]string, len(p.Commands))
		for i, c := range p.Commands {
			names[i] = c.Name
		}
		pos = append(pos, "{"+strings.Join(names, ",")+"} ...")
	}
	return opts, pos
}

// usage 生成用法行（含开头的 "usage: " 和结尾换行）。
func (p *Parser) usage() string {
	const prefix = "usage: "
	opts, pos := p.usageParts()
	all := strings.Join(append(append([]string{}, opts...), pos...), " ")
	text := p.Prog + " " + all
	if len(prefix)+len([]rune(text)) > textWidth {
		getLines := func(parts []string, indent string, withPrefix bool) []string {
			var lines []string
			var line []string
			lineLen := len(indent) - 1
			if withPrefix {
				lineLen = len(prefix) - 1
			}
			for _, part := range parts {
				if lineLen+1+len(part) > textWidth && len(line) > 0 {
					lines = append(lines, indent+strings.Join(line, " "))
					line = nil
					lineLen = len(indent) - 1
				}
				line = append(line, part)
				lineLen += len(part) + 1
			}
			if len(line) > 0 {
				lines = append(lines, indent+strings.Join(line, " "))
			}
			if withPrefix && len(lines) > 0 {
				lines[0] = lines[0][len(indent):]
			}
			return lines
		}
		indent := strings.Repeat(" ", len(prefix)+len(p.Prog)+1)
		var lines []string
		if len(opts) > 0 {
			lines = getLines(append([]string{p.Prog}, opts...), indent, true)
			lines = append(lines, getLines(pos, indent, false)...)
		} else {
			lines = getLines(append([]string{p.Prog}, pos...), indent, true)
		}
		text = strings.Join(lines, "\n")
	}
	return prefix + text + "\n"
}

func wrapWords(text string, width int) []string {
	words := strings.FieldsFunc(text, unicode.IsSpace)
	var lines []string
	line := ""
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// help 生成 --help 的完整输出。
func (p *Parser) help() string {
	var b strings.Builder
	b.WriteString(p.usage())
	type item struct{ invocation, help string }
	var positionals, optionals []item
	optionals = append(optionals, item{"-h, --help", "show this help message and exit"})
	for _, a := range p.Args {
		if a.Suppress {
			continue
		}
		if a.positional() {
			positionals = append(positionals, item{a.metavar(), a.Help})
			continue
		}
		invocation := a.Flag
		if a.takesValue() {
			invocation += " " + a.metavar()
		}
		optionals = append(optionals, item{invocation, a.Help})
	}
	maxLen := 0
	for _, group := range [][]item{positionals, optionals} {
		for _, it := range group {
			if n := len([]rune(it.invocation)) + 2; n > maxLen {
				maxLen = n
			}
		}
	}
	helpPosition := maxLen + 2
	if helpPosition > 24 {
		helpPosition = 24
	}
	helpWidth := textWidth - helpPosition
	if helpWidth < 11 {
		helpWidth = 11
	}
	actionWidth := helpPosition - 2 - 2
	section := func(title string, items []item) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n" + title + ":\n")
		for _, it := range items {
			if it.help == "" {
				b.WriteString("  " + it.invocation + "\n")
				continue
			}
			lines := wrapWords(it.help, helpWidth)
			if len([]rune(it.invocation)) <= actionWidth {
				pad := actionWidth - len([]rune(it.invocation))
				b.WriteString("  " + it.invocation + strings.Repeat(" ", pad) + "  " + lines[0] + "\n")
				lines = lines[1:]
			} else {
				b.WriteString("  " + it.invocation + "\n")
			}
			for _, line := range lines {
				b.WriteString(strings.Repeat(" ", helpPosition) + line + "\n")
			}
		}
	}
	section("positional arguments", positionals)
	section("options", optionals)
	return b.String()
}

func (p *Parser) fail(message string) {
	io.WriteString(stderr, p.usage())
	fmt.Fprintf(stderr, "%s: error: %s\n", p.Prog, message)
	panic(exit{2})
}

var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// lookup 返回匹配到的参数、显式值（--x=v）、是否是选项。
func (p *Parser) lookup(arg string) (a *Arg, explicit *string, isOption bool) {
	if arg == "" || arg[0] != '-' {
		return nil, nil, false
	}
	byFlag := func(flag string) *Arg {
		if flag == "-h" || flag == "--help" {
			return p.helpArg()
		}
		for _, a := range p.Args {
			if !a.positional() && a.Flag == flag {
				return a
			}
		}
		return nil
	}
	if a := byFlag(arg); a != nil {
		return a, nil, true
	}
	if len(arg) == 1 {
		return nil, nil, false
	}
	if flag, value, ok := strings.Cut(arg, "="); ok {
		if a := byFlag(flag); a != nil {
			return a, &value, true
		}
	}
	if strings.HasPrefix(arg, "--") {
		prefix, value, hasValue := strings.Cut(arg, "=")
		var matches []string
		flags := []string{"--help"}
		for _, a := range p.Args {
			if !a.positional() {
				flags = append(flags, a.Flag)
			}
		}
		for _, flag := range flags {
			if strings.HasPrefix(flag, prefix) {
				matches = append(matches, flag)
			}
		}
		if len(matches) > 1 {
			p.fail(fmt.Sprintf("ambiguous option: %s could match %s", prefix, strings.Join(matches, ", ")))
		}
		if len(matches) == 1 {
			a := byFlag(matches[0])
			if hasValue {
				return a, &value, true
			}
			return a, nil, true
		}
	}
	if negativeNumber.MatchString(arg) {
		return nil, nil, false
	}
	if strings.Contains(arg, " ") {
		return nil, nil, false
	}
	return nil, nil, true
}

// parseIntArg 解析整数参数：允许首尾空白、正负号和数字之间的单个下划线。
func parseIntArg(text string) (ojson.Number, bool) {
	s := strings.TrimFunc(text, textutil.IsSpace)
	sign := ""
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		if s[0] == '-' {
			sign = "-"
		}
		s = s[1:]
	}
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return "", false
	}
	digits := strings.ReplaceAll(s, "_", "")
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	n, ok := new(big.Int).SetString(sign+digits, 10)
	if !ok {
		return "", false
	}
	return ojson.Number(n.String()), true
}

// parseFloatArg 解析浮点参数：允许首尾空白、inf/nan 和数字之间的单个下划线。
func parseFloatArg(text string) (float64, bool) {
	s := strings.TrimFunc(text, textutil.IsSpace)
	lower := strings.ToLower(strings.TrimLeft(s, "+-"))
	switch lower {
	case "inf", "infinity", "nan":
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	if strings.Contains(s, "__") || strings.HasPrefix(lower, "_") || strings.HasSuffix(s, "_") {
		return 0, false
	}
	cleaned := strings.ReplaceAll(s, "_", "")
	for _, r := range strings.ToLower(cleaned) {
		if !(r >= '0' && r <= '9') && !strings.ContainsRune("+-.e", r) {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

func (p *Parser) convert(a *Arg, value string) any {
	var result any = value
	switch a.Type {
	case "int":
		n, ok := parseIntArg(value)
		if !ok {
			p.fail(fmt.Sprintf("argument %s: invalid int value: %s", a.name(), textutil.Repr(value)))
		}
		result = n
	case "float":
		f, ok := parseFloatArg(value)
		if !ok {
			p.fail(fmt.Sprintf("argument %s: invalid float value: %s", a.name(), textutil.Repr(value)))
		}
		result = f
	case "path":
		result = textutil.Norm(value)
	}
	if len(a.Choices) > 0 {
		found := false
		for _, choice := range a.Choices {
			if choice == value {
				found = true
			}
		}
		if !found {
			p.fail(fmt.Sprintf("argument %s: invalid choice: %s (choose from %s)", a.name(), textutil.Repr(value), strings.Join(a.Choices, ", ")))
		}
	}
	return result
}

// parseKnown 返回结果和没认出来的参数。
func (p *Parser) parseKnown(args []string) (Values, []string) {
	values := Values{}
	for _, a := range p.Args {
		switch {
		case a.NoDest:
		case a.Action == "store_true":
			values[a.Dest] = false
		case a.Action == "append":
			values[a.Dest] = []string{}
		default:
			values[a.Dest] = a.Default
		}
	}
	seen := map[*Arg]bool{}
	var extras []string
	var positionals []*Arg
	for _, a := range p.Args {
		if a.positional() {
			positionals = append(positionals, a)
		}
	}
	next := 0
	rest := false
	var afterDashes []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !rest && arg == "--" {
			rest = true
			afterDashes = append(afterDashes, arg)
			continue
		}
		var a *Arg
		var explicit *string
		isOption := false
		if !rest {
			a, explicit, isOption = p.lookup(arg)
		}
		if !isOption {
			if len(p.Commands) > 0 && next == len(positionals) {
				p.dispatch(values, args[i:], &extras)
				return values, extras
			}
			if next < len(positionals) {
				target := positionals[next]
				values[target.Dest] = p.convert(target, arg)
				seen[target] = true
				next++
				afterDashes = nil
				continue
			}
			if rest && len(afterDashes) > 0 {
				extras = append(extras, afterDashes...)
				afterDashes = nil
			}
			extras = append(extras, arg)
			continue
		}
		if a == nil {
			extras = append(extras, arg)
			continue
		}
		if a.Flag == "-h, --help" {
			io.WriteString(stdout, p.help())
			panic(exit{0})
		}
		if !a.takesValue() {
			if explicit != nil {
				p.fail(fmt.Sprintf("argument %s: ignored explicit argument %s", a.Flag, textutil.Repr(*explicit)))
			}
			values[a.Dest] = true
			seen[a] = true
			continue
		}
		var value string
		if explicit != nil {
			value = *explicit
		} else {
			if i+1 >= len(args) {
				p.fail(fmt.Sprintf("argument %s: expected one argument", a.Flag))
			}
			if _, _, option := p.lookup(args[i+1]); option || args[i+1] == "--" {
				p.fail(fmt.Sprintf("argument %s: expected one argument", a.Flag))
			}
			i++
			value = args[i]
		}
		converted := p.convert(a, value)
		if a.Action == "append" {
			values[a.Dest] = append(values.List(a.Dest), value)
			if a.Type == "path" {
				list := values.List(a.Dest)
				list[len(list)-1] = converted.(string)
			}
		} else {
			values[a.Dest] = converted
		}
		seen[a] = true
	}
	if len(p.Commands) > 0 {
		p.fail("the following arguments are required: command")
	}
	var missing []string
	for _, a := range p.Args {
		if (a.Required || a.positional()) && !seen[a] {
			missing = append(missing, a.name())
		}
	}
	if len(missing) > 0 {
		p.fail("the following arguments are required: " + strings.Join(missing, ", "))
	}
	return values, extras
}

// dispatch 把子命令名之后的全部参数交给子命令的 parser。
func (p *Parser) dispatch(values Values, args []string, extras *[]string) {
	for _, c := range p.Commands {
		if c.Name == args[0] {
			values["command"] = c.Name
			sub, subExtras := c.Parser.parseKnown(args[1:])
			for key, value := range sub {
				values[key] = value
			}
			*extras = append(*extras, subExtras...)
			return
		}
	}
	names := make([]string, len(p.Commands))
	for i, c := range p.Commands {
		names[i] = c.Name
	}
	p.fail(fmt.Sprintf("argument command: invalid choice: %s (choose from %s)", textutil.Repr(args[0]), strings.Join(names, ", ")))
}

// Parse 出错或 --help 时返回 (nil, 退出码)。
func (p *Parser) Parse(args []string) (values Values, code int) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(exit)
			if !ok {
				panic(r)
			}
			values, code = nil, e.code
		}
	}()
	values, extras := p.parseKnown(args)
	if len(extras) > 0 {
		p.fail("unrecognized arguments: " + strings.Join(extras, " "))
	}
	return values, -1
}

// Sub 创建一个子命令 parser，prog 为 "<父 prog> <名字>"。
func (p *Parser) Sub(name string, args ...*Arg) *Parser {
	sub := &Parser{Prog: p.Prog + " " + name, Args: args}
	p.Commands = append(p.Commands, &Command{Name: name, Parser: sub})
	return sub
}
