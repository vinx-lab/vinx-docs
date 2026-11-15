# 更新记录

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循语义化版本。

## [未发布]

### 新增

- 经反向代理对外访问时要登录：带转发头或 `Host` 是登记的外部域名的请求按外部处理；未登录只能只读查看标为「公开阅读」的项目和发布后 24 小时内（可调）的短链接，其余跳转登录页。密码、外部域名和免登录时长在管理后台「外部访问」里设置，只能在内网修改。内网直连行为不变，登记了外部域名后，`vinx-docs publish` 同时给出外网链接（`--json` 里是 `externalUrl`）。见 [spec 0006](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0006-password-for-reverse-proxy.md) 与 [ADR 0005](https://github.com/vinx-lab/vinx-docs/blob/main/docs/architecture-decisions/0005-external-access-login.md)。
- 短链接支持图片、视频和音频：可直接发布 `.png` `.jpg` `.gif` `.webp` `.svg` `.mp4` `.webm` `.mp3` `.wav`，打开的阅读页与 Markdown 一致（顶栏、返回文档中心、批注），媒体按屏幕高度等比缩小、一屏看完，图片点击放大，视频可全屏（页面列表的预览框里也可以）；Markdown 里 `![](demo.mp4)` 内嵌播放；非 HTML 文件流式发送并支持 `Range`，可拖动进度（Safari / iOS 播放需要 `Range`）；Markdown 里直接写的 `<img>` `<video>` `<audio>` 相对地址在阅读页里也能加载，见 [spec 0005](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0005-video-short-links.md)。
- 短链接可以下载源文件：阅读页顶栏（Markdown、图片、音视频）、页面列表顶栏和单独打开的 HTML 页面右下角都有「下载源文件」，可选按标题命名或按原文件名。只有一个文件时原样下载，有多个文件时按原目录结构打成 zip，HTML 不含注入的脚本，见 [spec 0007](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0007-short-link-download-and-toc.md)。
- 短链接阅读页的本页目录移到左侧：固定在屏幕中部，滚动时高亮当前小节，可以收起和展开（宽屏记住选择，手机默认收起）；收二到四级标题，按层级折叠，默认只展开当前小节所在的分支。
- 外部访问密码不再限制长度和字符，只要求不为空。
- 短链接可以发布到外部服务：「下载源文件」旁边的「发布到 ▾」列出登记的目标（同一个服务商可以有多个账号、多个目录），发布后显示链接；源文件改了提示「源文件已修改」，点「更新」覆盖原文件、链接不变；可以撤回。内置 Google Drive（直接调用 Drive API，不用另装程序）：在后台「外部发布」填写 OAuth 客户端，用验证码在任意设备上授权，可以连接多个账号、每个账号多个目录。目标默认不允许发布任何来源，公开链接要再确认；内网和已登录的外部访问能发布和管理。服务商由适配器接入（协议 v1：一次性进程 + NDJSON），外部适配器只能在命令行登记；命令行 `share` `unshare` `shares` `share-target` `share-account` `share-provider`，见 [spec 0008](https://github.com/vinx-lab/vinx-docs/blob/main/docs/specs/0008-share-targets.md)、[ADR 0006](https://github.com/vinx-lab/vinx-docs/blob/main/docs/architecture-decisions/0006-share-adapters-via-local-commands.md) 与 [发布到外部服务](https://github.com/vinx-lab/vinx-docs/blob/main/docs/share-targets.md)。
- 批注抽屉新增「全部交给 agent（N）」：先逐条「保存批注」，写完后把本页所有待处理批注按编号顺序一次交给 agent。

### 修复

- 点「本页目录」里的标题显示 404：现在滚到对应标题，标题不再被顶栏挡住（短链接阅读页和项目阅读页都有这个问题）。
- 写了批注没点保存就去选下一段，或刷新页面，写的内容会丢失：现在选别处时先自动存为待处理；关上批注抽屉不丢草稿；有未保存内容时刷新或关闭页面会提示。「保存批注」改为主按钮，Ctrl+Enter 只保存不交给 agent。

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
