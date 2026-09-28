package build

import (
	"strings"
	"testing"
)

// 侧栏的文件条目带 title：悬停显示项目内相对路径，路径里的引号和反斜杠要转义；
// 地址含空格、括号、引号时用尖括号包起来，不让链接断掉。
func TestSidebarLinkTitleShowsPath(t *testing.T) {
	route := func(s string) *string { return &s }
	entries := []Entry{
		{Path: "README.md", Kind: "markdown", Route: route("README.md"), Title: "首页"},
		{Path: `guide/say "hi"\x.md`, Kind: "markdown", Route: route(`guide/say "hi"\x.md`), Title: "问候"},
		{Path: "guide/a (1).md", Kind: "markdown", Route: route("guide/a (1).md"), Title: "副本"},
		{Path: "guide/page.html", Kind: "html", URL: "files/guide/page.html", PreviewURL: "preview/guide/page.html", Title: "页面"},
	}
	got := sidebar(entries)
	want := strings.Join([]string{
		"- [本项目首页](#/)",
		"- [本项目收录范围](#/__scope.md)",
		`- [首页](#/README.md "README.md")`,
		"- guide",
		`  - [问候](<#/guide/say "hi"\\x.md> "guide/say \"hi\"\\x.md")`,
		`  - [副本](<#/guide/a (1).md> "guide/a (1).md")`,
		`  - [页面](preview/guide/page.html "guide/page.html")`,
		"",
	}, "\n")
	if got != want {
		t.Fatalf("sidebar mismatch:\n%s\nwant:\n%s", got, want)
	}
	if linkTitle("a\nb") != `"a b"` {
		t.Fatal(linkTitle("a\nb"))
	}
	if linkDest("#/a<b>.md") != `<#/a\<b\>.md>` || linkDest("#/plain.md") != "#/plain.md" {
		t.Fatal(linkDest("#/a<b>.md"))
	}
}
