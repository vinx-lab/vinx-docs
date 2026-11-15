---
status: done
---

# 发布到外部服务：适配器框架（以 Google Drive 为例）

## 问题

短链接页面已经能「下载源文件」（spec 0007），但想把源文件放到网盘或其他平台给别人看，仍然要先下载，再到网盘里手动上传、手动开分享链接；源文件改过以后，又得重走一遍。

各家服务的接口和授权方式差别很大（OAuth、应用密钥、WebDAV 账号），以后还会不断有新服务要接。每接一家都改主程序的话，主程序会越来越重。

目标：

- 在短链接页面「下载源文件」旁边加一个「发布到 ▾」。选一个目标，就把源文件发上去，拿回链接。
- 可以配置多个服务商；同一个服务商可以连接多个账号，每个账号可以有多个目录。每种组合在菜单里是单独一项。
- 账号连接（授权）和目标管理都在页面上完成，不需要命令行。
- 做成框架：主程序定义标准，具体服务商由「适配器」实现，任何人都能按标准贡献新的适配器。第一个适配器是 Google Drive，同时作为样板。
- 用户不需要另外安装任何程序。

## 概念

- **适配器**：按本文的协议接收文件、上传到某个服务商、返回链接的程序。一种服务商对应一个适配器。官方适配器用 Go 写，编进主程序；也可以是外部的可执行程序（脚本或二进制）。
- **账号**：在某个服务商上授权过的一个身份，例如一个 Google 账号。一个适配器可以连接多个账号。授权得到的令牌只保存在本机，由适配器管理。
- **发布目标**：账号加上一组参数（目录、是否公开链接、允许发布哪些来源），有自己的 `id` 和显示名称。同一个账号可以有多个目标，例如「Google Drive · 工作」「Google Drive · 工作 / 客户A」「Google Drive · 个人」。
- **发布记录**：某个页面发布到某个目标的结果，包括外部链接和服务商那边的标识，用来更新和撤回。

## 方案

### 1. 发什么

第一版只支持短链接页面，内容与「下载源文件」相同：

- 清单只有一个文件时发原文件；有多个文件时打成 zip，规则同 0007。
- 文件名按目标的 `naming` 决定：`title`（默认，按页面标题）或 `file`（按原文件名）。
- 清单里的每个文件都要过 `CheckFile`。**有文件没通过检查时，拒绝发布并列出这些文件**，不像下载那样静默跳过（AGENTS.md：范围没覆盖到的情况必须显式报告）。
- 主程序先把文件写进本次任务的临时工作目录，算出 sha256，再把这个副本交给适配器。适配器拿不到源目录的路径。
- 大小超过目标的 `maxSize`（默认 200 MB）时拒绝发布。

下载和发布共用同一个内容生成函数（写入 `io.Writer`），同时返回清单里每个文件的 sha256，用来计算摘要。

同一个目标下，有效记录（含进行中和结果未知的）不能重名。重名时，主程序在文件名后面加上 ` (<短码>)`。

### 2. 适配器协议 v1：一次性进程 + NDJSON

每次调用启动一个进程，处理一个请求后退出。官方的 Go 适配器也是这样运行：主程序以 `vinx-docs share-adapter run <id>` 启动自己，走同一套 stdin/stdout 协议，所以内置和外部适配器的隔离、超时、日志规则完全相同。

**启动**

- 主程序直接执行命令（参数数组），不经过 shell。工作目录是本次任务的临时目录，结束后删除。
- 环境变量只给最小集合，不继承主程序的环境：`PATH`、`HOME`、`LANG`、`TMPDIR`（指向临时目录），加上目标 `passEnv` 里显式列出的变量（例如代理设置），以及下面的便捷变量。

**请求（stdin）**

stdin 只有一行 JSON，写完就关闭：

```json
{
  "protocol": 1,
  "method": "publish",
  "key": "s_7f3a…",
  "target": { "id": "gd-work", "public": false, "config": { "dir": "Vinx 分享" } },
  "provider": { "clientId": "…" },
  "secrets": { "clientSecret": "…" },
  "stateDir": "<家目录>/share-state/google-drive/acct_9c1e…",
  "params": {
    "file": "<临时目录>/订单看板方案.md",
    "name": "订单看板方案.md",
    "title": "订单看板方案",
    "size": 18234,
    "sha256": "…",
    "source": { "kind": "page", "id": "<短码>" },
    "previous": { "remoteId": "…", "url": "…" }
  }
}
```

- `method`：`check`、`publish`、`revoke`，以及授权用的 `auth.begin`、`auth.poll`（第 4 节）。适配器遇到不认识的 `method` 或不支持的 `protocol`，返回 `unsupported` 错误。
- `key`：幂等键，等于发布记录的 `id`，同一个页面发布到同一个目标时始终不变。
- `provider`：适配器级别的非密钥设置（例如 OAuth 客户端 ID），所有账号共用。
- `secrets`：适配器级别和目标级别的密钥合并后传入，只在 stdin 里传，不放进环境变量，也不写进日志。
- `stateDir`：这个账号的状态目录（第 4 节），适配器把令牌等写在这里。
- `previous`：更新时才有。
- 请求里没出现过的字段，适配器一律忽略；以后协议只增加字段，不改变已有字段的含义。不兼容的改动才提升 `protocol`。

不想解析 JSON 的适配器（例如 `sh` 脚本），可以直接读便捷环境变量，它们只包含非密钥字段：

| 变量 | 内容 |
| --- | --- |
| `VINX_SHARE_PROTOCOL` / `VINX_SHARE_METHOD` / `VINX_SHARE_KEY` | 同请求 |
| `VINX_SHARE_FILE` / `VINX_SHARE_NAME` / `VINX_SHARE_TITLE` | `params` 里的对应字段 |
| `VINX_SHARE_REMOTE_ID` | `params.previous.remoteId`，没有时为空 |
| `VINX_SHARE_PUBLIC` | `1` 或 `0` |
| `VINX_SHARE_STATE_DIR` | 同 `stateDir` |
| `VINX_SHARE_CONFIG_<NAME>` | `target.config` 里的每一项，名字转成大写，例如 `VINX_SHARE_CONFIG_DIR` |

**事件（stdout）**

stdout 每行一个 JSON 对象（NDJSON），UTF-8 编码，单行不超过 1 MiB：

```text
{"event":"log","level":"info","message":"上传中"}
{"event":"progress","bytes":1048576,"total":18234567}
{"event":"result","data":{"url":"https://…","remoteId":"…","public":false}}
{"event":"error","code":"auth_required","message":"授权已失效，请在后台重新连接这个账号"}
```

- `log` 和 `progress` 可以出现任意多次。第一版界面只显示「发送中」和耗时，`progress` 先只记录下来。
- 每次调用必须恰好有一个终止事件，`result` 或 `error`。发出终止事件后，进程要在 10 秒内退出，否则会被结束。
- 不是 JSON 的行、不认识的事件，都当作适配器的输出写进本机日志并忽略，不会让任务失败。
- stderr 只写进本机日志（`.runtime/server.log`），不会出现在界面上。

**各方法的 `result.data`**

| 方法 | 内容 |
| --- | --- |
| `check` | `{"account": "显示用的账号说明"}`：确认授权有效、目录可写，不上传任何文件 |
| `publish` | `{"url", "remoteId", "public"}`：`remoteId` 由适配器自己定义，主程序只负责保存和回传 |
| `revoke` | `{"alreadyGone": true/false}`：远端已经不存在也算成功 |
| `auth.begin` | `{"verificationUrl", "userCode", "expiresIn", "interval"}`：开始一次授权（第 4 节） |
| `auth.poll` | `{"status": "pending"}`，或 `{"status": "done", "account": "alice@gmail.com"}` |

**错误码**

`auth_required`（需要重新授权）、`auth_denied`（用户拒绝了授权）、`auth_expired`（验证码过期）、`permission_denied`、`quota_exceeded`、`not_found`、`conflict`、`rate_limited`、`network`、`invalid_config`、`unsupported`、`internal`。不认识的错误码按 `internal` 处理。

界面上只显示错误码对应的中文说明，以及适配器给的 `message`（截到 300 字）。`message` 里出现的密钥值会先替换成 `***`。

**判定结果**

| 情况 | 结果 |
| --- | --- |
| 有 `result`，退出码 0 | 成功 |
| 有 `error` | 失败，远端没有变化 |
| 没有终止事件、退出码非 0、超时，或有 `result` 但退出码非 0 | **结果未知**（`publish` 和 `revoke` 都可能已经在远端生效） |

超时（默认 5 分钟，可以按目标配置）或服务停止时，主程序先发 SIGTERM，等 5 秒后再结束整个进程组。

**适配器必须保证的语义**

- `publish` 对同一个 `key` 是幂等的。有 `previous` 时覆盖原位置；没有 `previous` 时，先按 `key` 找远端是否已经有这个文件，有就覆盖，没有才新建。重试不会产生重复文件。
- `publish` 必须按请求里的 `public` 设置链接是否公开。
- `revoke` 是幂等的。
- 令牌过期时，适配器自己刷新，并把新令牌写回 `stateDir`；刷新失败时返回 `auth_required`。

### 3. 适配器清单 `adapter.json`

每个适配器带一份清单，静态声明它的能力，不在运行时握手：

```json
{
  "id": "google-drive",
  "name": "Google Drive",
  "version": "1.0.0",
  "protocol": [1],
  "builtin": true,
  "capabilities": {
    "publicLink": true,
    "privateLink": true,
    "update": true,
    "stableLinkOnUpdate": true,
    "revoke": true,
    "auth": "device"
  },
  "provider": [
    { "name": "clientId", "required": true, "description": "Google OAuth 客户端 ID（类型：电视和输入受限的设备）" }
  ],
  "providerSecrets": [
    { "name": "clientSecret", "required": true, "description": "Google OAuth 客户端密钥" }
  ],
  "config": [
    { "name": "dir", "required": false, "description": "网盘里的目标目录，如 Vinx 分享；可以写多级，如 Vinx/客户A" }
  ],
  "secrets": []
}
```

- `builtin: true`：编进主程序的 Go 适配器，没有 `command`，主程序用 `vinx-docs share-adapter run <id>` 启动。外部适配器写 `command`（相对路径相对于适配器目录）和 `requires`（依赖的命令行工具）。
- `capabilities.auth`：`device` 表示支持第 4 节的页面授权流程；不写表示适配器不需要授权，或授权在别处完成（例如 `sh` 脚本调用的工具自带登录）。
- `provider` 和 `providerSecrets`：适配器级别的设置，所有账号共用，例如 OAuth 客户端。`config` 和 `secrets`：每个目标自己的设置。字段列表都是简单格式（名称、是否必填、说明），密钥字段在任何地方都不显示值。
- 主程序会按能力检查目标：要求公开链接但适配器不支持 `publicLink`，就不能保存；适配器不支持 `update` 时，界面不显示「更新」。

### 4. 账号、目标与授权

**数据**

家目录下的 `config/share-targets.json`，权限 0600：

```json
{
  "providers": {
    "google-drive": { "config": { "clientId": "…" }, "secrets": { "clientSecret": "…" } }
  },
  "accounts": [
    { "id": "acct_9c1e…", "adapter": "google-drive", "label": "alice@gmail.com", "status": "connected", "connectedAt": "…" },
    { "id": "acct_51b0…", "adapter": "google-drive", "label": "bob@example.com", "status": "auth_required", "connectedAt": "…" }
  ],
  "targets": [
    {
      "id": "gd-work",
      "label": "Google Drive · 工作",
      "adapter": "google-drive",
      "account": "acct_9c1e…",
      "config": { "dir": "Vinx 分享" },
      "secrets": {},
      "public": false,
      "sources": ["pages"],
      "naming": "title",
      "passEnv": [],
      "timeout": "5m",
      "maxSize": "200MB"
    }
  ]
}
```

- 每个账号有一个状态目录 `share-state/<适配器>/<账号 id>/`，权限 0700。令牌由适配器写在里面，主程序不读内容。同一个账号的任务串行执行，避免两个进程同时刷新令牌。
- 账号 `id` 由主程序在授权开始前生成，所以状态目录在授权前就确定，不需要等拿到邮箱后再改名。
- `public`：链接是否公开，保存目标时决定。适配器返回的 `public` 和目标不一致时，这次发布按失败处理，记录标为「公开状态不符」，并提示立即撤回。
- `sources`：允许发布哪些来源，**默认为空，也就是不能发布任何内容**，必须显式勾选。第一版只有 `pages`（短链接页面）；以后加项目文档时用 `project:<名称>`，取代 0004 第 3 节设计的项目 `share` 字段。
- 外部适配器的目标用 `command`（绝对路径加参数）和 `manifest`（`adapter.json` 的路径）代替 `adapter`，账号可以省略（状态目录按目标划分）。

**在页面上管理（只限内网）**

管理后台新增「外部发布」一节：

- **服务商设置**：每个内置适配器一张卡片，按 `provider` 和 `providerSecrets` 填写，例如 Google 的 OAuth 客户端 ID 和密钥，附上创建步骤的链接。保存后密钥只显示「已设置」和「替换」，不回显。
- **账号**：「连接 Google 账号」→ 页面显示一个网址和验证码（附二维码）→ 在任意设备（电脑或手机）打开网址、输入验证码、登录并点「允许」→ 页面自动显示「已连接 alice@gmail.com」。想连多个账号就重复这个操作。每个账号有「重新连接」（授权失效时用）和「断开」（删除本机令牌；还有目标在用时先提示）。
- **目标**：「新增目标」选账号，填写显示名称、目录，勾选允许的来源，选择链接是否公开（选公开时要求再确认一次），保存前会调用一次 `check`。目标可以修改和删除；还有有效发布记录的目标，删除前提示先撤回，确认后才删除，记录保留，但不能再更新或撤回。

页面授权用的是 OAuth 设备码流程（RFC 8628），原因：

- 不需要回调地址。浏览器在手机或别的电脑上也能完成授权，Vinx Docs 不需要对外开放端口，也不需要 HTTPS。
- Google 的设备码流程支持 `drive.file` 权限（官方文档的允许范围里列了它）。

授权过程：

1. 页面调用 `POST /api/share-accounts/connect`，主程序生成账号 `id` 和状态目录，用 `auth.begin` 调用适配器，把网址和验证码返回给页面。
2. 页面按 `interval` 调用 `POST /api/share-accounts/poll`，主程序每次用 `auth.poll` 调用适配器。适配器拿到令牌后写进状态目录，返回邮箱。
3. 用户拒绝、验证码过期或页面关闭超过有效期时，账号记录标为失败；页面可以重新开始。

**命令行**

命令行仍然可以完成同样的操作（脚本和 agent 用），而且是**登记外部适配器的唯一方式**：

```text
vinx-docs share-target add <id> --adapter google-drive --account <账号 id> --label <名称> --allow pages [--public] \
    [--config dir=…]
vinx-docs share-target add <id> --manifest <adapter.json> --label <名称> --allow pages ... -- <命令> [参数...]
vinx-docs share-target list | test <id> | remove <id> [--force]
vinx-docs share-account list | connect <适配器> | remove <账号 id>     # connect 在终端里显示网址和验证码
vinx-docs share-provider set google-drive --config clientId=… --secret clientSecret=…
```

### 5. 内置适配器

- 官方适配器用 Go 写，放在 `internal/share/adapters/<id>/`，清单 `adapter.json` 也在这个目录，随程序编译。主程序以 `vinx-docs share-adapter run <id>` 启动自己来运行适配器。
- 不用释放文件、不用校验，跟随程序版本一起升级。
- 只用 Go 标准库，不引入第三方依赖（ADR 0001）。

外部适配器（用户自己写的，或以后仓库收录的 `sh` 适配器）按第 10 节的规范实现，只能用命令行登记。

### 6. 发布记录与状态

记录保存在家目录的 `config/shares.json`，取代 0004 第 1 节里同名文件的结构：

```json
{
  "id": "s_7f3a…",
  "target": "gd-work",
  "source": { "kind": "page", "id": "<短码>" },
  "name": "订单看板方案.md",
  "status": "active",
  "url": "https://…",
  "remoteId": "…",
  "public": false,
  "digest": "…",
  "lastError": null,
  "createdAt": "…",
  "updatedAt": "…"
}
```

**状态**

| 状态 | 含义 | 界面上可以做什么 |
| --- | --- | --- |
| `pending` | 已经开始调用适配器 | 等待 |
| `active` | 已发布 | 打开、复制链接、更新、撤回 |
| `unknown` | 结果未知：超时、适配器异常退出，或服务在任务中途重启 | 重试（`publish` 幂等）、撤回（有 `remoteId` 时） |
| `revoked` | 已撤回，记录保留 | 重新发布 |

- 调用适配器**之前**，先把记录写成 `pending` 并落盘。服务启动时，所有 `pending` 改为 `unknown`。
- 首次发布明确失败（收到 `error` 事件）时，删除这条记录；更新或撤回明确失败时，恢复成 `active`，并写入 `lastError`。
- 收到 `auth_required` 时，同时把对应账号标为 `auth_required`，后台和发布框都提示「重新连接」。
- `digest` 由清单里每个文件的相对路径和 sha256 计算。当前源文件的摘要和它不同时，界面显示「源文件已修改」，**不自动重新发布**。
- 短链接取消发布时，已经发出去的文件不自动撤回；页面列表提示「还有 N 处外部发布」。
- 「结果未知」和「已撤回」的记录可以删除，只删本机记录，不碰远端。

### 7. 接口与访问控制

| 接口 | 说明 |
| --- | --- |
| `GET /api/share-targets?page=<短码>` | 这个页面能用的目标（`id`、`label`、适配器名称、`public`、能力、将用的文件名）和内容概况 |
| `GET /api/shares?page=<短码>` | 这个页面的发布记录，以及是否「源文件已修改」 |
| `POST /api/shares` `{page, target}` | 发布或更新，返回 `202 {job}` |
| `POST /api/shares/revoke` `{share}` | 撤回，同样返回任务 |
| `POST /api/shares/forget` `{share}` | 删除一条「结果未知」或「已撤回」的本机记录 |
| `GET /api/shares/job?id=<job>` | 任务状态 |
| `GET /api/share-admin` | 后台用：适配器、服务商设置（密钥只给「已设置」）、账号、目标（不含密钥值） |
| `POST /api/share-providers/set` | 保存服务商设置 |
| `POST /api/share-accounts/connect` / `poll` / `remove` | 账号授权和断开 |
| `POST /api/share-targets/save` / `remove` / `test` | 新增或修改、删除、检查目标（只限内置适配器） |

- **所有写接口只接受内网请求**。经反向代理的外部访问即使已登录（ADR 0005），也只能看到发布记录，写接口和 `/api/share-admin` 返回 403；匿名外部访问一律返回 401。
- 写接口沿用现有的 `Origin` 同源检查。短链接页面在沙箱里运行（origin 为 `null`），不能调用。
- 页面上的接口**不能登记命令**：只能为内置适配器保存目标，`command`、`manifest`、`passEnv` 字段一律拒绝。外部适配器只能在命令行登记（ADR 0006）。
- 不在目标 `sources` 范围内的页面，接口返回 403，界面上也不列出这个目标。
- 任务状态只存在内存里，记录的状态以 `shares.json` 为准。同一个页面、同一个目标同时只能有一个任务；全局最多同时运行 2 个。

已知风险：内网里任何设备都能打开后台连接账号、新增目标，也能伪造 `Origin` 用 curl 触发发布。这与 README「安全模型」里「内网没有权限控制」一致：这些设备本来就能读到全部文档。新增的风险是，内网设备可以把内容发到公开链接，或连接到别人的账号上，所以选择「公开链接」时要再确认一次，后台和目标列表也会明确显示每个目标连接的是哪个账号、链接是否公开。

### 8. 界面

- **发布到**：阅读页顶栏（Markdown、图片、音视频）和页面列表顶栏（HTML）的「下载源文件 ▾」旁边，加一个「发布到 ▾」。下拉菜单列出这个页面能用的目标，标明账号、「公开链接」或「仅自己」；已经发布过的目标后面标「已发布」「源文件已修改」或「结果未知」。没有可用目标，或者是外部访问时，菜单里只显示「去后台添加目标」（内网），或者整个按钮不显示（外部访问）。
- 单独打开的 HTML 页面右下角加一项「发布到…」。它不能直接调用接口，点击后打开页面列表并展开菜单，由用户自己选目标。
- **发布框**：显示目标名称、账号、是否公开、文件名、文件数和总大小。按钮是「发布」，已经发布过时是「更新」「撤回」，结果未知时是「重试」「撤回」。发布过程中显示「发送中」和耗时，完成后显示链接和「复制」按钮。失败时显示错误说明；`auth_required` 会显示「重新连接」，点击后跳到后台的账号。
- **后台「外部发布」**：服务商设置、账号、目标三块，见第 4 节。

### 9. 内置适配器：Google Drive（Go，直接调用 Drive API）

**准备（一次）**：在 Google Cloud 控制台创建一个项目，启用 Google Drive API，配置 OAuth 同意屏幕，创建「电视和输入受限的设备」类型的 OAuth 客户端，把客户端 ID 和密钥填进后台的服务商设置。步骤写进适配器 README，附截图位置说明。

- 同意屏幕的发布状态如果是「测试中」，Google 签发的刷新令牌 **7 天后过期**（官方文档的令牌过期说明），过期后要在后台重新连接。所以 README 提示把应用发布为「正式版」。
- 已查官方文档：`drive.file` 在 Drive API 的授权说明里列为「非敏感」范围，不需要敏感或受限范围的额外审核。发布为「正式版」后授权页面是否显示「未经验证的应用」，文档没有说清楚，留到真实账号实测时确认。
- 同一个 OAuth 客户端，每个 Google 账号最多保留 100 个刷新令牌，超出后最早的失效。这对正常使用没有影响。

**授权**：设备码流程。`auth.begin` 向 `https://oauth2.googleapis.com/device/code` 申请 `drive.file` 和 `email` 权限；`auth.poll` 用 `urn:ietf:params:oauth:grant-type:device_code` 轮询 `https://oauth2.googleapis.com/token`，处理 `authorization_pending`、`slow_down`、`access_denied`、`expired_token`。拿到令牌后，从 ID 令牌或用户信息接口取出邮箱，令牌写进状态目录的 `token.json`（权限 0600，原子写入）。

**发布**

- 目录：按 `dir` 逐级查找或创建文件夹，从「我的云端硬盘」根目录开始，只认这个应用自己创建的文件夹（`drive.file` 也只看得到这些）。文件夹 ID 缓存在状态目录里。
- 首次发布：先按 `appProperties` 里的 `vinxKey=<key>` 查找文件；找到就覆盖，找不到就用可续传上传（`uploadType=resumable`）新建，并写入 `vinxKey`。
- 更新：`files.update` 加可续传上传覆盖内容，文件 ID 和链接不变。
- 链接：公开时用 `permissions.create` 加 `{type: anyone, role: reader}`；非公开时不加权限。两种情况都返回文件的 `webViewLink`。
- 撤回：把文件移到回收站（`trashed=true`）。链接随之失效，用户还能从回收站恢复。文件已经不存在时算成功。

**错误映射**：401 先刷新令牌再重试一次，刷新返回 `invalid_grant` 时报 `auth_required`；403 `storageQuotaExceeded` 报 `quota_exceeded`；403 `rateLimitExceeded` / `userRateLimitExceeded` 和 429 报 `rate_limited`；404 报 `not_found`；5xx 和网络错误报 `network`。

**测试**：适配器的接口地址可以用一个只在测试里传入的环境变量改成本地的假 Drive 服务（`httptest`），离线一致性测试和浏览器测试都用这个假服务，不连 Google。

**已知限制**

- 非公开时，返回的链接只有这个 Google 账号本人能打开，适合自己留存，或之后在 Drive 里手动添加协作者。
- 页面标题改了以后再更新，Drive 上仍然沿用第一次发布时的文件名，这样链接才能保持不变。
- 只支持 Linux 和 macOS。

### 10. 贡献适配器的规范

协议对实现语言没有要求。仓库收录官方适配器时有以下要求；用户自己在本机登记的外部适配器不受这些限制。

**写法**：只收两种写法，避免用户还得另外安装运行时。

- 编进主程序的 Go 适配器（第 5 节），只用标准库；需要授权的服务实现 `auth.begin` / `auth.poll`。
- 外部 `sh` 脚本加一个成熟的命令行工具，脚本里只做参数组装。这种写法需要用户另外安装那个工具，只用于 Go 里不好实现、又已经有可靠工具的服务。

**目录结构（Go 适配器）**

```text
internal/share/adapters/<id>/
  adapter.json   # 清单（第 3 节）
  adapter.go     # 实现
  adapter_test.go
  fake_test.go   # 假的服务端（httptest），离线测试用
  README.md
```

外部 `sh` 适配器放在 `share-adapters/<id>/`，包括 `adapter.json`、入口脚本、`README.md`，以及 `test/` 目录（桩程序和 `conformance.json`）。

**README 必须写清楚**

- 怎么准备（例如创建 OAuth 客户端），需要什么权限范围；外部适配器还要写依赖哪些工具、怎么安装；
- 每个配置和密钥字段的含义；
- 公开和非公开链接分别是什么效果，撤回是删除文件还是只取消分享；
- 已知限制；
- 最近一次用真实账号实测的日期，以及通过了哪些检查项。

**测试**

- **离线一致性测试（CI 必跑）**：`vinx-docs share-adapter conformance <id 或目录>`（`tests/share-adapters/run.sh` 调用它）。对适配器依次检查：`check`、首次 `publish`、重复 `publish`（同一个 `key` 不产生重复文件）、带 `previous` 的 `publish`、公开链接、`revoke`、对已删除文件再 `revoke`、不支持的 `protocol` 和 `method`；支持授权的适配器还要检查 `auth.begin`，以及 `auth.poll` 的 pending、done、拒绝三种结果；stdout 里只有事件行；文件名带空格和中文时正常。
- **真实账号测试（手动执行）**：走一遍「连接账号 → 上传 → 打开链接 → 更新 → 撤回」，结果写进适配器的 README。

### 11. 预留到 v2 的扩展点

| 扩展 | 预留方式 |
| --- | --- |
| 本机回调授权（浏览器和服务在同一台机器时少输一次验证码） | `capabilities.auth` 增加取值，例如 `loopback` |
| 多种输入形态（目录、Markdown 原文、0004 的离线快照） | `params` 里加 `kind` 字段，清单里声明 `inputs`；不声明就视为只接收单个文件 |
| 进度条 | `progress` 事件已经定义好，界面以后再用 |
| 一次发布到多个目标 | 界面在发布框里多选，接口逐个创建任务，协议不变 |
| 配置的 JSON Schema | 字段列表以后可以升级成 schema，旧格式继续兼容 |

## 取舍

- **用 rclone 对接 Google Drive**（本 spec 的上一版，已经实现过一遍）：适配器只是一个 `sh` 脚本，但用户要另外安装约 60 MB 的 rclone，授权也要在终端里完成。改成 Go 直接调用 Drive API 后，不需要安装任何东西，程序只增加很少的体积，授权可以在页面上完成。代价是 OAuth、令牌刷新、可续传上传要自己实现和维护。
- **项目统一提供 OAuth 客户端**：用户最省事，但需要维护者名下的 Google Cloud 项目，可能还要通过 Google 审核，客户端的配额由所有用户共享。第一版由用户自己创建；以后有需要再提供。
- **本机回调授权**：浏览器必须和 Vinx Docs 在同一台机器上，从手机或另一台电脑打开后台时用不了。设备码流程在任何设备上都能完成。
- **stdio 上的 JSON-RPC 2.0（类似 LSP、MCP），带握手**：版本协商、取消请求都很完整，但只有长驻进程才用得上。这里每次任务一个进程，NDJSON 事件流已经能提供进度、结构化错误和版本控制，而且 `sh` 也能实现。
- **hashicorp/go-plugin（gRPC）**：接口有类型约束，但贡献者实际上只能用 Go，还要引入 gRPC、protobuf 等十几个依赖，而它擅长的长连接和流式调用这里用不上。
- **Go 原生 plugin（`.so`）**：需要开 cgo（违背 ADR 0001），插件必须和主程序用完全相同的 Go 版本和依赖编译，跑在同一个进程里，崩溃会拖垮服务，也不能卸载。
- **Go 适配器在主进程里直接调用，不另起进程**：省一次进程启动，但内置和外部适配器的超时、隔离、日志规则就不一样了；一次发布只启动一次进程，这点开销可以忽略。
- **MCP**：面向模型调用工具，没有文件传输和授权的约定；自己实现客户端工作量大，用官方 SDK 又要引入依赖。
- **WASM（wazero）**：沙箱隔离最好，但要引入新依赖，guest 里做网络请求和 OAuth 很别扭。
- **凭据放环境变量**：同一用户的其他进程能读到，还会被子进程继承，所以密钥只在 stdin 里传。
- **账号和目标只能在命令行管理**（上一版的做法）：安全边界最严，但用起来麻烦。内置适配器不涉及执行任意命令，在内网页面上管理不会扩大现有的风险面；登记任意命令仍然只能在命令行做。
- **每次发布都回终端确认**：页面上的按钮就没有意义了。改为保存目标时确认：默认不能发布任何来源，选择公开链接时再确认一次。
- **自动同步**：发到外部的内容应该由人确认，所以只提示「源文件已修改」。

## 与 0004 的关系

0004 的快照、按来源限定出口、凭据只存在家目录这几条原则不变。下面几处以本文为准：

- 出口的扩展方式；
- `config/shares.json` 的结构；
- 按项目限定出口改用目标的 `sources`；
- 凭据的管理方式：内置适配器在内网页面上管理，外部适配器只能在命令行登记。

0004 里的 P2 Cloudflare 和 P3 飞书，以后各写成一个适配器。

## 验证

- **Go 测试**：全部使用临时的 `VINX_DOCS_HOME`。
  - 协议：stdin 的 JSON 和便捷环境变量正确；不继承主程序的环境；密钥不出现在环境变量里；6 种结果判定；非 JSON 行和 stderr 只进日志；超过 1 MiB 的行；超时会结束整个进程组；`message` 里的密钥被替换成 `***`。
  - 输入：单文件原样、多文件打成 zip、两种命名、同名时加短码；有文件没通过 `CheckFile` 时拒绝并列出；超过大小上限时拒绝。
  - 状态：调用前已经落盘 `pending`；启动时 `pending` 变成 `unknown`；首次发布失败会删除记录；更新失败恢复 `active`；`unknown` 时重试会带上同一个 `key`；`public` 不一致按失败处理；`auth_required` 会把账号标为需要重新连接。
  - Google Drive 适配器（假 Drive 服务）：设备码授权的 pending、slow_down、done、拒绝、过期；令牌写入状态目录、权限 0600；令牌过期时自动刷新，`invalid_grant` 报 `auth_required`；逐级创建目录；按 `vinxKey` 找回文件，不产生重复；更新后文件 ID 不变；公开链接加 `anyone` 权限，非公开不加；撤回移到回收站，已经删除的也算成功；各类错误码的映射。
  - 访问控制：外部访问（已登录或匿名）调用写接口和 `/api/share-admin` 返回 403 或 401；`Origin: null` 和跨域请求被拒绝；页面接口提交 `command`、`manifest`、`passEnv` 时被拒绝；目标列表和后台数据里不含密钥值；不在 `sources` 范围内返回 403。
  - 命令行：`share-target`、`share-account`、`share-provider` 的各个子命令；配置文件权限 0600；状态目录权限 0700。
- **离线一致性测试**：Google Drive 适配器使用假 Drive 服务跑通，并加入 CI。
- **真实浏览器测试**（`tests/browser`，假 Drive 服务）：
  - 后台：填写服务商设置后，密钥不回显；连接账号时显示网址和验证码，假服务端「授权」后显示邮箱；连接第二个账号；为两个账号各建一个目标，其中一个公开（有二次确认）；删除仍有记录的目标时有提示。
  - 阅读页：菜单列出两个目标并标明账号和是否公开；发布后显示链接和复制按钮；修改源文件后显示「源文件已修改」，更新成功且链接不变；撤回成功；令牌失效时显示「重新连接」；超时显示「结果未知」，重试成功。
  - 单独打开的 HTML 页面能跳到页面列表并展开菜单。
- **Google Drive 实测**（需要 V大 创建 OAuth 客户端并用测试账号授权，完成前列为未验证项）：
  - 在手机上完成设备码授权；连接两个账号；
  - 单文件和 zip 能上传；非公开链接只有本人能打开，公开链接在未登录时也能打开；
  - 更新后链接不变；重复发布不会产生重复文件；
  - 撤回后链接打不开，文件在回收站里；两个账号、两个目录互不影响；
  - 令牌在「测试中」状态下 7 天过期后，能在后台重新连接。

## 已确认

1. 第一版只做短链接页面，与「下载源文件」一致。项目文档的发布要先给项目阅读页加上下载入口，放到下一轮。
2. 短链接取消发布时，已经发出去的文件只提示、不自动撤回：删除外部文件不可逆，应该由人决定。
3. 不考虑 Windows。
4. 扩展方式：适配器协议是「一次性进程 + NDJSON」，配合仓库的贡献规范；不用 JSON-RPC 握手，不用 go-plugin，也不用 Go 原生 plugin。
5. 不在每次发布时回终端确认，改为保存目标时确认：默认不能发布任何来源，选择公开链接时再确认一次。
6. 只有内网能发布、撤回和管理账号与目标；经反向代理的外部访问只能看到记录。
7. Google Drive 用 Go 直接调用 Drive API，编进主程序，不依赖 rclone；授权和目标管理在页面上完成，使用设备码流程；支持多个账号。
8. OAuth 客户端第一版由用户自己在 Google Cloud 创建。

## 结果

已实现，Google Drive 的真实账号实测未做（见下）。

**改动**

- `internal/share`：服务商设置、账号、目标（`config.go`）；内置适配器登记与目标解析（`adapters.go`）；协议 v1 执行（`runner.go`）；发布记录、任务与状态（`records.go`、`service.go`）；后台管理与设备码授权（`admin.go`）；一致性测试（`conformance.go`）。
- `internal/share/kit`：写适配器用的小工具（读请求、写事件、状态文件）。
- `internal/share/adapters/gdrive`：Google Drive 适配器（设备码授权、令牌刷新、逐级建目录、可续传上传、`vinxKey` 幂等、公开权限、移到回收站），只用标准库；`gdrivetest` 是假 Google 服务。
- `internal/share/testdata/local-folder`：外部 `sh` 适配器的最小示例，框架测试用。
- `internal/pages/source.go`：下载和发布共用的内容生成与摘要；`internal/server/download.go` 改用它。
- `internal/server/share.go`：第 7 节的接口。`internal/cli/share.go`：`share-target`、`share-account`、`share-provider`、`share-adapter`（含 `run`、`conformance`）、`share`、`unshare`、`shares`；`unpublish` 提示还有几处外部发布。
- 前端：`assets/share-menu.js`（菜单和发布框）、`assets/share-admin.js`（后台「外部发布」），阅读页、页面列表、管理后台、`artifact-live.js`、`app.css`。
- 测试：`tests/browser/share.cjs` 和 `tests/browser/fakedrive`（假 Google 服务的独立进程）。
- 文档：[发布到外部服务](../share-targets.md)、[Google Drive 适配器说明](../../internal/share/adapters/gdrive/README.md)、README、架构、更新记录，ADR 0006 改为已采纳。

**与方案的出入**

- 文件夹 ID 没有缓存在状态目录里：每次按名称逐级查找，`drive.file` 下只看得到本应用建的文件夹，查找很快。
- 上传大小以实际文件为准，不信任请求里的 `size`（一致性测试发现过两者不一致）。
- 「发布到」菜单不列出不可用的目标；账号授权失效的目标仍然列出，并提示去后台重新连接。
- 还有有效发布记录的目标，不能修改账号、目录和公开设置（第 4 节只写了删除时提示）。
- 外部适配器的一致性测试用 `vinx-docs share-adapter conformance <目录>`；内置 Go 适配器在自己的 Go 测试里调用 `share.RunConformance`，配合假服务端。

**验证**

- Go：`internal/share`（协议的 6 种结果判定、非 JSON 行和 stderr 只进日志、超过 1 MiB 的行、超时结束进程组、环境不继承且不含密钥、密钥替换；发布、更新链接不变、源文件已修改、撤回、forget；zip 与同名加短码；未开启来源 403、目标不存在 404、有文件没通过检查时拒绝并列出；首次失败删除记录、更新失败恢复 active、超时结果未知、pending 重启后变 unknown、unknown 重试覆盖原位置；公开状态不符；文件权限）；`internal/share/adapters/gdrive`（设备码授权的允许和拒绝、两个账号、令牌 0600 且不进配置文件、逐级建目录、公开与非公开、更新不产生新文件且链接不变、访问令牌过期自动刷新、刷新被拒报 auth_required 并标记账号、重新连接后恢复、有目标时不能断开账号、服务商密钥不回显、一致性测试 15 项）；`internal/server`（后台数据不含密钥和令牌、页面接口拒绝 `command`/`manifest`/`passEnv`、外部已登录 403、匿名外部 401、`Origin: null` 403、目标列表不含配置和密钥、发布撤回删除目标流程）；`internal/cli` 全部命令的 `--help`。
- 真实浏览器：`tests/browser` 五项全部通过（本机内核限制非特权 user namespace，在临时副本里关掉 `chromiumSandbox` 跑的，仓库里的脚本没改）。`share.cjs` 在页面上走完：填写服务商设置（密钥不回显）→ 另开页面输入验证码连接两个账号 → 添加两个目标（公开的有二次确认）→ 菜单标明账号和是否公开 → 发布到 `Vinx/客户A` → 改源文件后更新、链接不变 → 撤回进回收站 → 公开目标的文件是公开的 → 令牌失效后提示去后台重新连接 → 外部适配器超时后重试成功 → 单独打开的 HTML 页面跳到页面列表展开菜单。
- 手动：临时家目录加假 Google 服务，在页面上完成同样的流程，看过后台和菜单的截图；手机宽度（390px）下后台布局正常，没有横向滚动。

**未验证**

- Google Drive 真实账号：需要按适配器说明创建 OAuth 客户端，并用测试账号授权。验证项见 `internal/share/adapters/gdrive/README.md`「测试」一节。
- macOS 上的实际运行（只在 Linux 上跑过；CI 的 macOS 任务会跑 Go 测试）。

**后续**

- 项目文档的发布（先给项目阅读页加下载入口，`sources` 用 `project:<名称>`）。
- v2 扩展点见第 11 节。
