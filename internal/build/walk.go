package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Candidate 是一个将被收录的文件：Rel 是项目内的发布路径（含前缀），Src 是源文件绝对路径。
type Candidate struct {
	Rel string
	Src string
}

// IterCandidates 遍历项目的每个文档目录；只有强制规则和 exclude 能把文件挡在外面。
// 返回按路径排序的候选、排序后的排除条目（"路径:原因"）和提示条目。
func IterCandidates(project *ojson.Object, roots []config.Root) ([]Candidate, []string, []string) {
	excludes := config.Excludes(project)
	st := &scanState{seen: map[string]bool{}}
	for _, root := range roots {
		// 前缀本身也要过一遍排除规则，否则整目录级别的排除挡不住带前缀的目录。
		if root.Prefix != "" && MatchesExcludeDir(root.Prefix, excludes) {
			st.skipped = append(st.skipped, root.Prefix+"/:exclude")
			continue
		}
		walkRoot(root.Prefix, root.Path, excludes, st)
	}
	sort.SliceStable(st.result, func(i, j int) bool { return st.result[i].Rel < st.result[j].Rel })
	sort.Strings(st.skipped)
	sort.Strings(st.warnings)
	return st.result, st.skipped, st.warnings
}

type scanState struct {
	result   []Candidate
	skipped  []string
	warnings []string
	seen     map[string]bool
}

// walkRoot 自顶向下遍历一个文档目录：不跟随符号链接，出错的目录静默跳过。
func walkRoot(prefix, root string, excludes []string, st *scanState) {
	var visit func(dir string)
	visit = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var dirnames, filenames []string
		for _, entry := range entries {
			isDir := entry.IsDir()
			if entry.Type()&fs.ModeSymlink != 0 {
				info, err := os.Stat(filepath.Join(dir, entry.Name()))
				isDir = err == nil && info.IsDir()
			}
			if isDir {
				dirnames = append(dirnames, entry.Name())
			} else {
				filenames = append(filenames, entry.Name())
			}
		}
		sort.Strings(dirnames)
		sort.Strings(filenames)
		var allowed []string
		for _, name := range dirnames {
			path := textutil.Join(dir, name)
			rel := config.Published(prefix, textutil.RelativeTo(path, root))
			switch {
			case textutil.IsSymlink(path):
				st.skipped = append(st.skipped, rel+"/:符号链接")
			case strings.HasPrefix(name, "."):
				st.skipped = append(st.skipped, rel+"/:隐藏路径")
			case config.IsForbiddenDir(name):
				st.skipped = append(st.skipped, rel+"/:运行缓存或仓库目录")
			case MatchesExcludeDir(rel, excludes):
				st.skipped = append(st.skipped, rel+"/:exclude")
			default:
				allowed = append(allowed, name)
			}
		}
		for _, name := range filenames {
			path := textutil.Join(dir, name)
			rel := config.Published(prefix, textutil.RelativeTo(path, root))
			if st.seen[rel] {
				st.skipped = append(st.skipped, rel+":与其他文档目录同名")
				continue
			}
			st.seen[rel] = true
			if MatchesExclude(rel, excludes) {
				st.skipped = append(st.skipped, rel+":exclude")
				continue
			}
			if reason := config.PolicyReason(rel, path); reason != "" {
				st.skipped = append(st.skipped, rel+":"+reason)
				continue
			}
			if textutil.IsSymlink(path) {
				st.skipped = append(st.skipped, rel+":符号链接")
				continue
			}
			kind := config.KindFor(path)
			if kind == "" {
				st.skipped = append(st.skipped, rel+":未知类型")
				continue
			}
			if kind == "markdown" || kind == "text" || kind == "html" {
				blocked, suspect := contentReasonOf(path)
				if blocked != "" {
					st.skipped = append(st.skipped, rel+":"+blocked)
					continue
				}
				if suspect != "" {
					st.warnings = append(st.warnings, rel+":"+suspect)
				}
			}
			st.result = append(st.result, Candidate{rel, path})
		}
		// 直接剪枝，既是发布边界也避免走进 node_modules 这类大目录。
		for _, name := range allowed {
			path := textutil.Join(dir, name)
			if !textutil.IsSymlink(path) {
				visit(path)
			}
		}
	}
	visit(root)
}

// contentReasonOf 读取文件后调用 ContentReason；读不了就当没有命中。
func contentReasonOf(path string) (string, string) {
	info, err := os.Stat(path)
	if err != nil {
		return "", ""
	}
	if info.Size() > contentScanLimit {
		return "", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return ContentReason(int64(len(data)), data)
}
