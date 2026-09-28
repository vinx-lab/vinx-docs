package build

import (
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/config"
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
	got := sidebar(entries, nil)
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

func sidebarEntry(path, title string) Entry {
	route := path
	return Entry{Path: path, Kind: "markdown", Route: &route, Title: title}
}

// 只有一个文档目录（空前缀或带前缀）时，侧栏和以前一样：目录按完整路径平铺成一级。
func TestSidebarSingleRootUnchanged(t *testing.T) {
	entries := []Entry{
		sidebarEntry("docs/README.md", "首页"),
		sidebarEntry("docs/a/b/c.md", "深层"),
		sidebarEntry("docs/a/x.md", "X"),
	}
	want := strings.Join([]string{
		"- [本项目首页](#/)",
		"- [本项目收录范围](#/__scope.md)",
		"- docs",
		`  - [首页](#/docs/README.md "docs/README.md")`,
		"- docs/a",
		`  - [X](#/docs/a/x.md "docs/a/x.md")`,
		"- docs/a/b",
		`  - [深层](#/docs/a/b/c.md "docs/a/b/c.md")`,
		"",
	}, "\n")
	for _, roots := range [][]config.Root{nil, {{Prefix: "", Path: "/p"}}, {{Prefix: "docs", Path: "/p/docs"}}} {
		if got := sidebar(entries, roots); got != want {
			t.Fatalf("roots=%v sidebar mismatch:\n%s\nwant:\n%s", roots, got, want)
		}
	}
}

// 多个文档目录：项目根的文件和目录置顶，带前缀的目录按登记顺序各成一组，
// 组内先列顶层文件，子目录去掉前缀、按字母序平铺在二级，文件在三级；没有文件的组不显示。
func TestSidebarGroupsByRootOrder(t *testing.T) {
	roots := []config.Root{
		{Prefix: "zeta", Path: "/z"},
		{Prefix: "", Path: "/p"},
		{Prefix: "alpha", Path: "/a"},
		{Prefix: "empty", Path: "/e"},
		{Prefix: "mid/api", Path: "/m"},
	}
	// 输入按项目内路径排序，和 IterCandidates 的输出一致。
	entries := []Entry{
		sidebarEntry("README.md", "根首页"),
		sidebarEntry("alpha/architecture/proposals/p1.md", "提案"),
		sidebarEntry("alpha/architecture/x.md", "架构"),
		sidebarEntry("alpha/intro.md", "介绍"),
		sidebarEntry("guide/start.md", "上手"),
		sidebarEntry("mid/api/ref.md", "接口"),
		sidebarEntry("zeta/b/deep/z.md", "深"),
		sidebarEntry("zeta/top.md", "顶"),
	}
	want := strings.Join([]string{
		"- [本项目首页](#/)",
		"- [本项目收录范围](#/__scope.md)",
		`- [根首页](#/README.md "README.md")`,
		"- guide",
		`  - [上手](#/guide/start.md "guide/start.md")`,
		"- zeta",
		`  - [顶](#/zeta/top.md "zeta/top.md")`,
		"  - b/deep",
		`    - [深](#/zeta/b/deep/z.md "zeta/b/deep/z.md")`,
		"- alpha",
		`  - [介绍](#/alpha/intro.md "alpha/intro.md")`,
		"  - architecture",
		`    - [架构](#/alpha/architecture/x.md "alpha/architecture/x.md")`,
		"  - architecture/proposals",
		`    - [提案](#/alpha/architecture/proposals/p1.md "alpha/architecture/proposals/p1.md")`,
		"- mid/api",
		`  - [接口](#/mid/api/ref.md "mid/api/ref.md")`,
		"",
	}, "\n")
	if got := sidebar(entries, roots); got != want {
		t.Fatalf("sidebar mismatch:\n%s\nwant:\n%s", got, want)
	}
}
