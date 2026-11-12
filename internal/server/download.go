package server

import (
	"archive/zip"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// attachment 生成 Content-Disposition；非 ASCII 文件名按 RFC 2231 编码（filename*）。
func attachment(name string) string {
	if value := mime.FormatMediaType("attachment", map[string]string{"filename": name}); value != "" {
		return value
	}
	return "attachment"
}

// serveDownload 下载短链接的源文件：清单只有一个文件时原样发送（HTML 不注入自动刷新脚本）；
// 有多个文件时按原目录结构打成 zip。只包含清单里、且仍通过发布检查的文件。
// 默认按页面标题命名，?name=file 按原文件名命名（见 pages.DownloadNames）。
func (q *request) serveDownload(record *ojson.Object) {
	name, byFile := pages.DownloadNames(record)
	if values := textutil.ParseQS(q.query)["name"]; len(values) > 0 && values[0] == "file" {
		name = byFile
	}
	const plain = "text/plain; charset=utf-8"
	gone := []byte("源文件已不存在或已不允许发布")
	root := pages.Str(record, "root")
	type item struct{ rel, path string }
	var items []item
	for _, rel := range pages.Files(record) {
		if path, err := pages.CheckFile(root, rel); err == nil {
			items = append(items, item{rel, path})
		}
	}
	if len(items) == 0 {
		q.sendPage(404, gone, plain)
		return
	}
	if len(pages.Files(record)) == 1 {
		file, err := os.Open(items[0].path)
		var info os.FileInfo
		if err == nil {
			defer file.Close()
			info, err = file.Stat()
		}
		if err != nil {
			q.sendPage(404, gone, plain)
			return
		}
		q.setPageHeaders(pages.MimeFor(items[0].rel))
		q.setHeaders("Content-Disposition", attachment(name))
		http.ServeContent(q.w, q.r, "", info.ModTime(), file)
		return
	}
	q.setPageHeaders("application/zip")
	q.setHeaders("Content-Disposition", attachment(name))
	dir := strings.TrimSuffix(name, ".zip")
	q.w.WriteHeader(200)
	if q.isHead {
		return
	}
	archive := zip.NewWriter(q.w)
	for _, it := range items {
		file, err := os.Open(it.path)
		if err != nil {
			continue // 打开前一刻被删掉的文件跳过；其余文件照常打包
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			continue
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			file.Close()
			continue
		}
		header.Name = dir + "/" + it.rel
		header.Method = zip.Deflate
		writer, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(writer, file)
		}
		file.Close()
		if err != nil {
			abort() // 响应已经开始，只能断开连接，浏览器会显示下载失败
		}
	}
	if err := archive.Close(); err != nil {
		abort()
	}
}
