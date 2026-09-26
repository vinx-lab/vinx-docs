package build

// 用 testdata/golden.json 里固定的期望值逐条比对边缘语义：
// exclude 匹配（整路径通配、逐段匹配、** 变体）、明文口令规则、标题提取、搜索摘要。
// 这些结果会写进已有用户的站点和增量缓存，改动任何一条都要确认是有意的。

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

type golden struct {
	Match []struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		File    any    `json:"file"`
		Dir     any    `json:"dir"`
	} `json:"match"`
	Suspect []struct {
		Text string `json:"text"`
		Hit  bool   `json:"hit"`
	} `json:"suspect"`
	Titles []struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	} `json:"titles"`
	Search []struct {
		Text  string `json:"text"`
		Terms string `json:"terms"`
	} `json:"search"`
}

func loadGolden(t *testing.T) golden {
	data, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func safeMatch(fn func(string, []string) bool, path, pattern string) (result any) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("panic:%v", r)
		}
	}()
	return fn(path, []string{pattern})
}

func TestGoldenExcludeMatching(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Match {
		if got := safeMatch(MatchesExclude, c.Path, c.Pattern); fmt.Sprint(got) != fmt.Sprint(c.File) {
			t.Errorf("MatchesExclude(%q, %q) = %v, want %v", c.Path, c.Pattern, got, c.File)
		}
		if got := safeMatch(MatchesExcludeDir, c.Path, c.Pattern); fmt.Sprint(got) != fmt.Sprint(c.Dir) {
			t.Errorf("MatchesExcludeDir(%q, %q) = %v, want %v", c.Path, c.Pattern, got, c.Dir)
		}
	}
}

func TestGoldenSuspectContent(t *testing.T) {
	for _, c := range loadGolden(t).Suspect {
		if got := suspectSearch(c.Text); got != c.Hit {
			t.Errorf("suspect(%q) = %v, want %v", c.Text, got, c.Hit)
		}
	}
}

func TestGoldenTitles(t *testing.T) {
	for _, c := range loadGolden(t).Titles {
		if got := Title([]byte(c.Text), "fallback", "markdown"); got != c.Title {
			t.Errorf("Title(%q) = %q, want %q", c.Text, got, c.Title)
		}
	}
}

func TestGoldenSearchTerms(t *testing.T) {
	for _, c := range loadGolden(t).Search {
		if got := SearchTerms([]byte(c.Text), "markdown"); got != c.Terms {
			t.Errorf("SearchTerms(%q)\n got %q\nwant %q", c.Text, got, c.Terms)
		}
	}
}
