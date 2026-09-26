# 0001 单个 Go 可执行文件，前端资源内嵌

状态：已采纳

## 背景

工具要给别人用，安装越简单越好，而且要能在 Linux、macOS、Windows 上跑。

- 最早的形态是「宿主机上的管理进程 + Docker 里的 Nginx」：使用者要同时装运行时和 Docker；Docker Desktop 的文件共享不支持传递 unix socket，macOS 和原生 Windows 跑不起来。
- 之后改成单个 Python 进程、只用标准库，去掉了 Docker 和 Nginx，但使用者仍需要合适版本的 Python，前端资源要随包安装，分发时还要处理打包。

## 决定

用 Go 写成一个可执行文件：

- 同一个进程提供静态站点、控制 API、页面短链接和批注，按路径给出 CSP、附件头、点开头路径段 404、目录重定向和 ETag。
- 前端资源（`assets/web`、`assets/vendor`）用 `go:embed` 编进程序。
- 批注库用纯 Go 的 SQLite（modernc.org/sqlite），`CGO_ENABLED=0` 就能交叉编译各平台版本。
- 唯一的第三方 Go 依赖是这个 SQLite 驱动及其间接依赖。

## 影响

- 使用者下载或编译出一个文件就能运行，不需要安装运行时。
- 程序约 19 MB（去掉调试信息后），其中前端资源约 7 MB。
- 改前端后要重新编译；开发时可以用 `--assets` 直接读取源码目录，免去重复编译。
- 并发能力足够单人或小范围使用，定位仍是本机工具。
- 运行数据放在平台对应的用户数据目录，可用 `VINX_DOCS_HOME` 覆盖。
