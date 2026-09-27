# 部署、安全导出与验证规则

更新日期：2026-09-27；状态：单个 Go 程序（前端内嵌，不依赖 Nginx / Docker）已实现并通过测试。

## 运行方式

Vinx Docs 是一个 Go 编写的可执行文件，前端资源和 SQLite 驱动都编译在里面，不需要额外的运行时。它同时提供生成的站点、管理接口、页面短链接和批注接口，直接监听配置里的 `server.host:server.port`（默认 `0.0.0.0:8000`）。

```bash
vinx-docs start     # 后台启动：先监听端口，再在后台重建站点、开启自动同步
vinx-docs status    # 家目录、地址、PID、自动同步状态
vinx-docs stop      # 只停止由 start 启动、且正在应答的那个服务
vinx-docs serve     # 前台运行，给 systemd / launchd 用
vinx-docs install-service   # 登记开机自启，见下文；--dry-run 只预览
```

- 进程身份一律通过 `/api/healthz` 确认：返回的服务名、PID 和家目录都要对上，才会被 `stop` 停止。pid 文件被改成别的进程时拒绝操作，不会误杀。
- 端口被占用时报告冲突，不杀进程、不自动换端口。配置里的 `protectedPorts` 同样拒绝。
- `0.0.0.0` 是监听地址，不是浏览地址。本机用 `http://localhost:8000/`，其他设备用主机 IP 或 Tailscale 名。跨设备可达性要实际验证，不因此自动修改防火墙或端口转发。

## 开机自启

`install-service` 只登记启动项，不替你启动，避免和已经用 `start` 跑着的服务抢端口；`uninstall-service` 移除启动项并停止它管理的服务。两者都支持 `--dry-run`。

| 平台 | 启动项 | 执行的命令 | 说明 |
| --- | --- | --- | --- |
| Linux | systemd 用户单元 `~/.config/systemd/user/vinx-docs.service` | `vinx-docs serve`，家目录写在 `Environment=` | `Restart=on-failure`；注销后仍运行需要 linger |
| macOS | `~/Library/LaunchAgents/vinx-docs.plist` | `vinx-docs serve`，家目录写在 `EnvironmentVariables` | 用 `launchctl load -w` 登记 |
| Windows | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 的 `vinx-docs` 值 | `"<程序>" --home "<家目录>" start` | 当前用户登录时运行，不需要管理员权限 |

Windows 执行的是 `start` 而不是 `serve`：`start` 在后台拉起不带窗口的服务后自己退出，登录后不会留下控制台窗口，之后可以照常用 `vinx-docs status/stop/restart` 管理。已知限制：

- `vinx-docs.exe` 是控制台程序，登录时可能闪一下窗口。
- 没有崩溃自动重启，服务意外退出后要手动 `vinx-docs start` 或重新登录。
- 用户登录后才运行；开机但没人登录时不运行。
- 启动项里记的是登记时的程序路径，移动或改名程序后要重新执行 `install-service`。

检查是否已登记：`reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v vinx-docs`，也可以在「任务管理器 → 启动应用」里看到。设计取舍见 [specs/0001-windows-autostart.md](specs/0001-windows-autostart.md)。

## 家目录

运行数据全部放在家目录，和代码分开，也不会落进任何被收录的文档目录：

| 平台 | 默认位置 |
| --- | --- |
| Linux | `$XDG_DATA_HOME/vinx-docs`，默认 `~/.local/share/vinx-docs` |
| macOS | `~/Library/Application Support/vinx-docs` |
| Windows | `%LOCALAPPDATA%\vinx-docs` |

可以用 `--home` 或环境变量 `VINX_DOCS_HOME` 指定。目录内容：

```text
config/projects.json     项目登记与设置
config/artifacts.json    页面短链接登记表
vinx.db                  批注库（SQLite）
.runtime/site/           生成的站点（可重建）
.runtime/history/        页面内编辑的保存历史
.runtime/server.json     start 记录的 PID
.runtime/server.log      后台运行日志
```

前端资源（`assets/web/`、`assets/vendor/`）在编译时内嵌进程序。家目录里如果自带一份 `web/`，构建时优先用它，方便定制界面；开发时也可以用 `--assets <目录>`（或环境变量 `VINX_DOCS_ASSETS`）直接读取源码里的 `assets/`，改前端不用重新编译。

## 前端依赖

浏览器端资源固定版本放在 `assets/vendor/`，`manifest.json` 记录来源 URL、版本、完整性值、许可证和逐文件 SHA-256；每次构建都会重新校验，校验失败直接拒绝发布。阅读阶段不请求外部 CDN。

| 包 | 版本 | 许可证 | 来源 | 用途 |
| --- | --- | --- | --- | --- |
| docsify | 4.13.1 | MIT | registry.npmjs.org | 阅读框架与项目内搜索 |
| mermaid | 12.0.0 | MIT | registry.npmjs.org | Mermaid 图渲染，按需懒加载 |
| xlsx (SheetJS CE) | 0.20.3 | Apache-2.0 | cdn.sheetjs.com | XLSX 只读表格预览 |
| CodeMirror | 5.65.21 | MIT | registry.npmmirror.com | 页面内编辑器 |
| qrcode-generator | 1.4.4 | MIT | registry.npmjs.org | 首页访问地址二维码 |

SheetJS 在 npm 上停留在 0.18.5 且有已知 CVE，官方发布已迁至 cdn.sheetjs.com，这里固定官方来源的 0.20.3。mermaid 与 SheetJS 均不含 `eval`/`new Function`，站点 CSP 的 `script-src 'self'` 无需放宽。

更新资源时从明确来源获取所选版本，核验确为目标项目而非错误页。下载或校验失败就停下，不轮流尝试未知镜像源，不关闭 TLS 校验。

## 文件收录边界

1. 源目录按项目显式登记，读取前验证真实路径。拒绝整个 `/`、整个用户目录等过宽的根目录；登记目录不能包含家目录里的生成站点。
2. 登记目录和路径组件不允许通过符号链接跳到别处，候选文件同样拒绝符号链接。拒绝绝对路径注入、`..`、编码后的路径穿越和登记 ID 冲突。
3. 收录范围是整个登记目录加排除规则，不能只按扩展名决定公开内容；排除规则与强制安全规则优先。
4. 默认不收录隐藏文件、`.env`、私钥和证书凭据、运行日志、依赖缓存。
5. 环境清单、部署配置等要看内容：文件名是 Markdown 不代表没有凭据，但也不能只因出现「password」一词就排除需求文档，要看是否真的含敏感值。
6. 构建代码预览时转义内容，并处理内容自身的反引号围栏，避免配置或脚本文本变成可执行页面。
7. 生成的 manifest 只包含必要的展示路径与类型，不把主机绝对路径和凭据发到浏览器。

## 响应头与同主机 cookie

不同端口仍可能共享按主机设置的 cookie，所以「单独一个端口」本身不足以保证 HTML 报告无法影响本机其他应用。服务按路径给出以下响应头（由 `internal/server` 实现，`internal/server/server_test.go` 守住）：

| 路径 | 策略 |
| --- | --- |
| 站点页面、脚本、`/api/` | `default-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'` 等，禁止内联脚本 |
| `/projects/<id>/preview/` | 项目里的 HTML 报告：`sandbox`，不允许脚本 |
| `/projects/<id>/raw/` | 原件下载：`Content-Disposition: attachment` + `sandbox` |
| `/projects/<id>/content/` | 一律 `text/plain` |
| `/a/<短码>/` | 页面短链接：只有一条沙箱策略（允许脚本、origin 为 null），不叠加站点策略 |

所有响应都带 `X-Content-Type-Options: nosniff` 和 `Referrer-Policy: no-referrer`。任何以 `.` 开头的路径段、目录穿越和不存在的文件都返回 404，不兜底成首页；没有目录列表。静态文件带 ETag，重复访问返回 304。

## 刷新一致性

站点是可重建的只读副本：服务启动时构建一次，之后由自动同步触发，也可以点首页「立即同步」或执行 `vinx-docs refresh`。

自动同步在 Linux 上用 inotify 监听登记目录（跳过隐藏目录和 `node_modules` 这类缓存目录），文件停止变化 `settings.debounceSeconds` 秒后重建一次；目录数超过预算，或者平台没有 inotify（macOS、Windows）时，改用定时轮询。

刷新是增量的：`.runtime/build-cache.json` 记录每个文件的 mtime、大小和摘要，未变更的文件不重新读取或复制，只重建清单、侧栏、范围页和搜索索引。先在临时目录生成并验证全部文件，再原子替换到站点目录；只在本工具拥有（带标记文件）的站点目录里删除旧文件，永远不删源文档。

## 管理接口

浏览器只请求同一站点的 `/api/`。写接口要求 POST、`application/json`、同源 `Origin`，并逐个声明允许的字段，多一个未知字段就拒绝。不提供任意命令执行、目录浏览或通用文件读取接口。按单人自用场景，不设账号，也不区分本机和远程。

| 动作 | 预期语义 | 不允许的附带动作 |
| --- | --- | --- |
| 接入项目 | 验证明确路径，保存登记信息并构建 | 扫描其他目录、修改源目录 |
| 同步 | 生成/更新收录文档与索引 | 改写原文 |
| 取消接入 | 移除导航、索引和生成副本 | 删除原项目文件或 Git 记录 |
| 保存（页面内编辑） | 版本检查、原子写入、保存前留历史 | 批量改写、自动提交 Git |

## 验收清单

- 路径单测：中文/空格、相对链接/锚点、重复 ID、越界路径、符号链接、隐藏文件和凭据排除、删除后刷新。
- 服务：受保护端口拒绝；各路径的 MIME、CSP、404 与上表一致；生命周期不误杀别的进程。
- 真实浏览器：首页、侧栏、章节、前进后退、搜索、HTML 受限预览、附件下载、手机布局；阅读过程中没有外部请求。
- 源文件保护：生成前后原文和工作簿的校验值不变。
- 跨设备访问是否实际验证，要单独记录。
