// vinx-docs 是 Vinx Docs 的命令入口：一个二进制提供服务、构建、页面发布和批注处理的全部子命令。
// 前端资源内嵌在二进制里，单文件即可运行；家目录里自带 web/ 时优先用它。
package main

import (
	"os"

	"github.com/vinx-lab/vinx-docs/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
