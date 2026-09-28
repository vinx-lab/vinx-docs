# 更新记录

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循语义化版本。

## [0.4.0] - 2026-09-28

### 新增

- 项目可按文件类型限定收录：配置字段 `types`（如只收录 `.md`、`.html`），管理后台按组勾选；不在名单里的文件不收录，收录范围页按扩展名汇总计数。按路径的白名单仍不提供，见 [spec 0003](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0003-type-whitelist.md) 与 [ADR 0004](https://github.com/vinx-lab/vinx-docs/blob/main/docs/architecture-decisions/0004-type-whitelist.md)（#3）。
- 阅读页侧栏目录可折叠：默认只展开当前页所在目录，切页自动展开，提供「全部展开 / 全部折叠」；鼠标悬停文件显示项目内路径（#4）。
- 多目录项目的侧栏按文档目录分组，顺序与管理后台登记顺序一致，子目录名去掉前缀。

### 修复

- 侧栏「本项目收录范围」不再显示为「未收录」。
- 手机宽度下顶栏换行后，不再挡住侧栏搜索框和正文标题；手机上打开侧栏也能正常显示。
- 深色模式下侧栏底部按钮条跟随主题。
- 文件名含空格、括号或引号时，侧栏链接不再断成原文。
- 批注库首次被并发打开时偶发 500：遇到 `SQLITE_BUSY` 时重试；接口返回 500 时写服务日志。

## [0.3.0] - 2026-09-27

### 新增

- `publish` 支持 Markdown 入口：短链接打开单文件阅读页，渲染同文档阅读页，批注和编辑写回源文件；自动收引用的图片。见 [spec 0002](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0002-publish-markdown.md)。
- AGENTS.md 增加 issue 与 agent 的协作约定（`agent:plan` / `agent:ready` / `agent:done` 标签）。
- vinx-docs skill：补清边界（没覆盖的情况先试再问，改配置的命令只在用户要求时执行），补充挂批注监听的时机，监听时限改为 30 分钟。

### 修复

- 阅读页把开头的 front matter 显示为代码块，不再渲染成分隔线和标题。
- 选词后点「批注」，输入框自动获得焦点（#2）。

## [0.2.1] - 2026-09-27

- 首页和管理后台的顶栏显示版本号；`/api/status` 增加 `version` 字段。

## [0.2.0] - 2026-09-27

### 新增

- Windows 开机自启：`install-service` 写入当前用户的登录启动项（`HKCU\...\Run`），登录时用 `start` 在后台启动服务，不需要管理员权限；`uninstall-service` 删除启动项并停止服务。以前只打印一条 `schtasks` 命令。见 [specs/0001](docs/specs/0001-windows-autostart.md)。
- 推送 `v*` 标签时自动编译 5 个平台并发布 Release；README 增加从 Releases 下载安装的方式。

### 修复

- macOS：登记目录允许经过系统自带的 `/tmp`、`/var`、`/etc` 链接（必须指向 `/private` 下的对应目录），其他符号链接照旧拒绝。
- Windows：排除 `node_modules` 等缓存目录时不区分大小写，`Node_Modules` 也会被排除。
- 测试：修正 macOS 临时目录符号链接、Windows 上改名被占用目录导致的偶发失败。

## [0.1.0] - 2026-09-26

首个公开版本。

- 多项目文档阅读：一个项目可登记多个文档目录，整目录收录、用 exclude 排除，文件变化自动同步。
- 页面短链接：agent 用 `vinx-docs publish` 发布本地 HTML 页面，得到 `/a/<短码>/` 链接，源文件修改后页面自动刷新。
- 页面内编辑：带版本检查、原子写入和历史备份。
- 批注：在文档或页面上写批注，agent 用 `vinx-docs` 命令领取、回复和解决，支持监听自动投递。
- 首页、管理后台、批注总览。
- 单个 Go 可执行文件，前端资源内嵌，批注库用纯 Go 的 SQLite；支持 Linux，macOS 和 Windows 为实验性支持。
