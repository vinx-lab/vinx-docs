package build

import (
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// patternVariants `**/` 表示零层或多层目录，
// 去掉开头的 `**/`、把中间的 `/**/` 压成 `/`，让顶层条目也能命中。
func patternVariants(pattern string) []string {
	variants := []string{pattern}
	for i := 0; i < len(variants); i++ {
		item := variants[i]
		var shorter string
		if strings.HasPrefix(item, "**/") {
			shorter = item[3:]
		} else {
			shorter = strings.Replace(item, "/**/", "/", 1)
		}
		if shorter != item && !contains(variants, shorter) {
			variants = append(variants, shorter)
		}
	}
	return variants
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// matchesPattern 用两种方式匹配：整条路径通配（* 可以跨目录），或从右往左逐段匹配。
func matchesPattern(rel, pattern string) bool {
	for _, item := range patternVariants(pattern) {
		if textutil.FnMatch(rel, item) || textutil.PathMatch(rel, item) {
			return true
		}
	}
	return false
}

// MatchesExclude 报告文件路径是否命中某条排除规则。
func MatchesExclude(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchesPattern(rel, pattern) {
			return true
		}
	}
	return false
}

// MatchesExcludeDir 目录还额外匹配去掉结尾 "/" 或 "/**" 的规则。
func MatchesExcludeDir(rel string, patterns []string) bool {
	trimmed := make([]string, len(patterns))
	for i, item := range patterns {
		if strings.HasSuffix(item, "/**") {
			trimmed[i] = item[:len(item)-3]
		} else {
			trimmed[i] = strings.TrimRight(item, "/")
		}
	}
	return MatchesExclude(rel, patterns) || MatchesExclude(rel, trimmed)
}

// unusedExcludes 找出没有排除任何条目的规则。
func unusedExcludes(patterns, skipped []string) []string {
	var files, dirs []string
	for _, item := range skipped {
		if !strings.HasSuffix(item, ":exclude") {
			continue
		}
		path := item[:len(item)-len(":exclude")]
		if strings.HasSuffix(path, "/") {
			dirs = append(dirs, strings.TrimRight(path, "/"))
		} else {
			files = append(files, path)
		}
	}
	out := []string{}
	for _, pattern := range patterns {
		used := false
		for _, item := range files {
			if MatchesExclude(item, []string{pattern}) {
				used = true
				break
			}
		}
		if !used {
			for _, item := range dirs {
				if MatchesExcludeDir(item, []string{pattern}) {
					used = true
					break
				}
			}
		}
		if !used {
			out = append(out, pattern)
		}
	}
	return out
}
