package server

import (
	"mime"
	"net/http"
	"os"

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
	items, _ := pages.CheckSources(record)
	if len(items) == 0 {
		q.sendPage(404, gone, plain)
		return
	}
	if !pages.Bundled(record) {
		file, err := os.Open(items[0].Path)
		var info os.FileInfo
		if err == nil {
			defer file.Close()
			info, err = file.Stat()
		}
		if err != nil {
			q.sendPage(404, gone, plain)
			return
		}
		q.setPageHeaders(pages.MimeFor(items[0].Rel))
		q.setHeaders("Content-Disposition", attachment(name))
		http.ServeContent(q.w, q.r, "", info.ModTime(), file)
		return
	}
	q.setPageHeaders("application/zip")
	q.setHeaders("Content-Disposition", attachment(name))
	q.w.WriteHeader(200)
	if q.isHead {
		return
	}
	if _, err := pages.WriteSource(record, items, name, q.w); err != nil {
		abort() // 响应已经开始，只能断开连接，浏览器会显示下载失败
	}
}
