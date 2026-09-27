# 更新记录

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循语义化版本。

## [Unreleased]

- Windows 开机自启：`install-service` 写入当前用户的登录启动项（`HKCU\...\Run`），登录时用 `start` 在后台启动服务，不需要管理员权限；`uninstall-service` 删除启动项并停止服务。以前只打印一条 `schtasks` 命令。

## [0.1.0] - 2026-09-26

首个公开版本。

- 多项目文档阅读：一个项目可登记多个文档目录，整目录收录、用 exclude 排除，文件变化自动同步。
- 页面短链接：agent 用 `vinx-docs publish` 发布本地 HTML 页面，得到 `/a/<短码>/` 链接，源文件修改后页面自动刷新。
- 页面内编辑：带版本检查、原子写入和历史备份。
- 批注：在文档或页面上写批注，agent 用 `vinx-docs` 命令领取、回复和解决，支持监听自动投递。
- 首页、管理后台、批注总览。
- 单个 Go 可执行文件，前端资源内嵌，批注库用纯 Go 的 SQLite；支持 Linux，macOS 和 Windows 为实验性支持。
