package textutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormIsLiteral(t *testing.T) {
	cases := map[string]string{
		"": ".", ".": ".", "a//b/./c/": "a/b/c", "/a/../b": "/a/../b", "//a": "//a", "///a": "/a", "a/..": "a/..",
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
		if got := Realpath(in); got != want {
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
