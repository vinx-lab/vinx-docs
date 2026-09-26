package textutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormIsLiteral(t *testing.T) {
	cases := map[string]string{
		"": ".", ".": ".", "a//b/./c/": "a/b/c", "/a/../b": "/a/../b", "///a": "/a", "a/..": "a/..",
	}
	if !windows {
		// POSIX 保留开头恰好两个斜杠；Windows 上 //a 是 UNC 前缀，另见 TestWindowsPathRules。
		cases["//a"] = "//a"
	}
	for in, want := range cases {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
	if Parent("a") != "." || Parent("/") != "/" || Name("a/b.md") != "b.md" || Suffix("a.") != "." || Suffix(".md") != "" {
		t.Error("Parent/Name/Suffix")
	}
}

func TestRealpathResolvesLinksAndMissingTails(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.MkdirAll(filepath.Join(real, "sub"), 0o755)
	os.Symlink(real, filepath.Join(dir, "link"))
	os.Symlink("loop", filepath.Join(dir, "loop"))
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	cases := map[string]string{
		filepath.Join(dir, "link", "sub"):                filepath.Join(resolvedDir, "real", "sub"),
		filepath.Join(dir, "link", "missing", "..", "x"): filepath.Join(resolvedDir, "real", "x"),
		filepath.Join(dir, "loop", "x"):                  filepath.Join(resolvedDir, "loop", "x"),
	}
	for in, want := range cases {
		// Windows 上结果统一用 / 分隔（C:/Users/...），期望值由 filepath 拼出，比较前换成 /。
		if got := Realpath(in); got != filepath.ToSlash(want) {
			t.Errorf("Realpath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuotePercentEncodesUTF8(t *testing.T) {
	if got := Quote("guide/使用 说明%#?.md", "/"); got != "guide/%E4%BD%BF%E7%94%A8%20%E8%AF%B4%E6%98%8E%25%23%3F.md" {
		t.Error(got)
	}
	if got := Quote("data/a b.xlsx", ""); got != "data%2Fa%20b.xlsx" {
		t.Error(got)
	}
}

// Windows 的盘符、UNC 和反斜杠规则只涉及字符串处理，临时打开开关就能在任何平台上验证。
func TestWindowsPathRules(t *testing.T) {
	saved := windows
	windows = true
	defer func() { windows = saved }()
	norms := map[string]string{
		`C:\Users\x\docs\`:        "C:/Users/x/docs",
		`c:/Users//x/./docs`:      "C:/Users/x/docs",
		`C:`:                      "C:",
		`C:rel\a`:                 "C:rel/a",
		`\\server\share\a\b`:      "//server/share/a/b",
		`\rooted\a`:               "/rooted/a",
		`a\b/c`:                   "a/b/c",
		`C:\a\..\b`:               "C:/a/../b",
		`C:\`:                     "C:/",
		`//server/share`:          "//server/share/",
		`\\server`:                "/server",
		`C:\Users\RUNNER~1\x.txt`: "C:/Users/RUNNER~1/x.txt",
	}
	for in, want := range norms {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{`C:\a`: true, `C:/`: true, `\\s\share\x`: true, `\a`: false, `C:a`: false, `a`: false, `/a`: false} {
		if IsAbs(in) != want {
			t.Errorf("IsAbs(%q) != %v", in, want)
		}
	}
	for in, want := range map[string]bool{`C:a`: true, `\a`: true, `/a`: true, `a/b`: false, `a:b`: true, `ab:c`: false} {
		if HasRoot(in) != want {
			t.Errorf("HasRoot(%q) != %v", in, want)
		}
	}
	if !IsRoot(`C:\`) || !IsRoot(`\\s\share`) || IsRoot(`C:\a`) || IsRoot("a") {
		t.Error("IsRoot")
	}
	if got := Join(`C:\base`, "a/b", `C:\other`, "c"); got != "C:/other/c" {
		t.Error("Join resets on absolute part:", got)
	}
	if got := Join("C:/base", "/x", "C:y"); got != "C:/base/x/C:y" {
		t.Error("Join keeps rooted and drive-relative parts inside:", got)
	}
	if Parent("C:/") != "C:/" || Parent("C:/a") != "C:/" || Name(`C:\a\b.md`) != "b.md" {
		t.Error("Parent/Name")
	}
	if !IsWithin(`c:\users\X\docs\a.md`, "C:/Users/x/docs") || IsWithin("C:/Users/x/docs2", "C:/Users/x/docs") ||
		IsWithin("D:/Users/x/docs/a", "C:/Users/x/docs") {
		t.Error("IsWithin")
	}
	if got := RelativeTo(`C:\docs\guide\a.md`, "C:/docs"); got != "guide/a.md" {
		t.Error("RelativeTo keeps / for published paths:", got)
	}
	if !SamePath(`C:\Users\X`, "c:/users/x") || SamePath("C:/a", "C:/a/b") {
		t.Error("SamePath")
	}
}
