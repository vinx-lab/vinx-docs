// Package assets 把前端资源（本目录的 web/、vendor/）内嵌进二进制，做到单文件分发。
// 前端没有构建步骤，改完这里的文件重新编译即可；调试时也可以在家目录放一份 web/，构建时优先用它。
package assets

import (
	"embed"
	"io/fs"

	"github.com/vinx-lab/vinx-docs/internal/build"
)

//go:embed all:web all:vendor
var files embed.FS

// FS 返回内嵌资源，根下是 web/ 和 vendor/。
func FS() fs.FS { return files }

// Install 把内嵌资源设为 build.PackageAssets。
// 家目录里自带 web/ 时 build.AssetsRoot 仍优先用家目录。
func Install() {
	build.PackageAssets = build.Assets{FS: files}
}
