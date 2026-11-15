package pages

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

// SourceFile 是短链接清单里一个通过发布检查的文件。
type SourceFile struct {
	Rel  string // 清单里的相对路径
	Path string // 绝对路径
}

// SourceSum 是写出的一个文件的摘要。
type SourceSum struct {
	Rel    string
	Size   int64
	SHA256 string
}

// CheckSources 把清单里的文件分成通过发布检查的和没通过的（相对路径）。
func CheckSources(record *ojson.Object) (ok []SourceFile, rejected []string) {
	root := Str(record, "root")
	for _, rel := range Files(record) {
		if path, err := CheckFile(root, rel); err == nil {
			ok = append(ok, SourceFile{rel, path})
		} else {
			rejected = append(rejected, rel)
		}
	}
	return ok, rejected
}

// Bundled 报告下载或发布时是否打成 zip（清单有多个文件）。
func Bundled(record *ojson.Object) bool { return len(Files(record)) > 1 }

// WriteSource 写出短链接的源文件内容，与「下载源文件」相同：清单只有一个文件时原样写出，
// 多个文件时按原目录结构打成 zip，包内顶层目录是 name 去掉 .zip。返回每个写出文件的摘要。
// 打开或读取失败的文件跳过（在调用前一刻被删掉的情况）；写入失败返回错误。
func WriteSource(record *ojson.Object, items []SourceFile, name string, w io.Writer) ([]SourceSum, error) {
	var sums []SourceSum
	if !Bundled(record) {
		for _, it := range items {
			sum, err := copySum(it, w)
			if err != nil {
				return nil, err
			}
			sums = append(sums, sum)
		}
		return sums, nil
	}
	dir := strings.TrimSuffix(name, ".zip")
	archive := zip.NewWriter(w)
	for _, it := range items {
		file, err := os.Open(it.Path)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		var header *zip.FileHeader
		if err == nil {
			header, err = zip.FileInfoHeader(info)
		}
		if err != nil {
			file.Close()
			continue
		}
		header.Name = dir + "/" + it.Rel
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err != nil {
			file.Close()
			return nil, err
		}
		hash := sha256.New()
		size, err := io.Copy(io.MultiWriter(writer, hash), file)
		file.Close()
		if err != nil {
			return nil, err
		}
		sums = append(sums, SourceSum{it.Rel, size, hex.EncodeToString(hash.Sum(nil))})
	}
	return sums, archive.Close()
}

func copySum(it SourceFile, w io.Writer) (SourceSum, error) {
	file, err := os.Open(it.Path)
	if err != nil {
		return SourceSum{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(w, hash), file)
	return SourceSum{it.Rel, size, hex.EncodeToString(hash.Sum(nil))}, err
}

// SourceDigest 由每个文件的相对路径和 sha256 计算，不受 zip 打包时间影响。
func SourceDigest(sums []SourceSum) string {
	lines := make([]string, 0, len(sums))
	for _, s := range sums {
		lines = append(lines, s.Rel+"\x00"+s.SHA256)
	}
	sort.Strings(lines)
	hash := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(hash[:])
}

// CurrentDigest 读取当前源文件计算摘要；有文件读不到时返回空字符串。
func CurrentDigest(record *ojson.Object) string {
	items, rejected := CheckSources(record)
	if len(rejected) > 0 {
		return ""
	}
	var sums []SourceSum
	for _, it := range items {
		sum, err := copySum(it, io.Discard)
		if err != nil {
			return ""
		}
		sums = append(sums, sum)
	}
	return SourceDigest(sums)
}
