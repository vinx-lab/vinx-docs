# 适配器开发指南：接入新的外部服务

「发布到外部服务」把短链接页面的源文件发到网盘等服务商，拿回链接。每个服务商由一个**适配器**接入：Vinx Docs 负责界面、权限、发布记录和任务调度，适配器只负责「把这个文件放到服务商那里，返回链接」。本文写给要接入新服务商的开发者。

- 设计和取舍：[spec 0008](specs/0008-share-targets.md)、[ADR 0006](architecture-decisions/0006-share-adapters-via-local-commands.md)
- 用户怎么用：[发布到外部服务](share-targets.md)
- 现成的例子：
  - [`internal/share/adapters/gdrive/`](../internal/share/adapters/gdrive/)：内置的 Google Drive 适配器，带设备码授权和假服务端，写内置适配器照着它来；
  - [`internal/share/testdata/local-folder/`](../internal/share/testdata/local-folder/)：最小的外部适配器（`sh` 脚本，把文件「发布」到本机目录）；
  - [`internal/share/kit/`](../internal/share/kit/kit.go)：Go 适配器用的小工具包。

## 1. 适配器要做什么，不用做什么

| Vinx Docs 负责 | 适配器负责 |
| --- | --- |
| 「发布到」菜单、发布框、后台的服务商设置 / 账号 / 目标表单（按清单自动生成） | 调用服务商的接口：上传、覆盖、设置公开、撤回 |
| 打包源文件（单文件原样，多文件打 zip）、检查文件、处理重名，给适配器一个临时副本 | 按幂等键找回已发布的文件，避免重复 |
| 发布记录、「源文件已修改」、「结果未知」后的重试 | 授权：生成验证码、换取和刷新令牌，令牌存在状态目录 |
| 访问控制、超时、结束进程、日志里的密钥脱敏 | 把错误映射成协议的错误码，给出用户看得懂的说明 |
| 同一个账号的任务串行执行，全局最多同时 2 个 | |

接入新服务商**不需要改主程序的任何流程代码**，也不需要改前端：内置适配器只要在 `internal/cli/cli.go` 里加一行导入。

## 2. 选一种写法

| | 内置 Go 适配器 | 外部适配器 |
| --- | --- | --- |
| 语言 | Go，只用标准库 | 任何语言（脚本或二进制） |
| 用户要装什么 | 什么都不用装，随 `vinx-docs` 升级 | 适配器本身和它依赖的工具 |
| 页面授权（连接账号） | 支持（实现 `auth.begin` / `auth.poll`） | 不支持，授权在别处完成（例如工具自带登录） |
| 怎么登记 | 后台「外部发布」或命令行 | **只能**在本机命令行登记（ADR 0006） |
| 仓库收录 | 首选 | 只收 `sh` 脚本 + 成熟命令行工具，用于 Go 里不好实现、又有可靠工具的服务 |

想提交到仓库的，优先写内置 Go 适配器。自己用的，写什么都行。

## 3. 一次调用是怎么进行的

```text
用户点「发布」
  → Vinx Docs 把源文件写到临时目录（算好 sha256），找出上次的记录
  → 启动适配器进程（内置的是 vinx-docs share-adapter run <id>，外部的是登记的命令）
  → stdin 写一行请求 JSON，然后关闭
  ← 适配器在 stdout 逐行输出事件：log / progress …，最后恰好一个 result 或 error
  → Vinx Docs 判定结果，更新发布记录，删除临时目录
```

每次调用一个进程，处理完就退出。没有常驻进程、没有握手，内置和外部适配器走完全相同的协议。

## 4. 协议 v1 速查

完整定义和设计理由见 spec 0008 第 2 节。

### 4.1 请求（stdin，一行 JSON）

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

| 字段 | 说明 |
| --- | --- |
| `protocol` | 协议版本，现在是 `1`。不支持的版本返回 `unsupported` |
| `method` | `check`、`publish`、`revoke`；支持页面授权的再加 `auth.begin`、`auth.poll`。不认识的返回 `unsupported` |
| `key` | 幂等键，等于发布记录的 id。同一个页面发到同一个目标时始终不变 |
| `target.public` | 这次要不要公开链接，由目标决定 |
| `target.config` | 目标的配置（清单 `config` 声明的字段） |
| `provider` | 适配器级别的非密钥设置（清单 `provider` 声明的字段），所有账号共用 |
| `secrets` | 适配器级别和目标级别的密钥合并后传入。**只在 stdin 里出现** |
| `stateDir` | 这个账号的状态目录（0700），令牌等写在这里。外部适配器没有账号时按目标划分 |
| `params` | 按方法不同：`publish` 见上；`revoke` 只有 `source` 和 `previous`；`check`、`auth.*` 没有 |
| `params.file` / `name` | 要上传的临时副本和建议的文件名（已经处理过重名）。适配器拿不到源目录 |
| `params.previous` | 更新和撤回时才有：上次返回的 `remoteId` 和 `url` |

请求里多出来的字段一律忽略：以后协议只加字段，不改已有字段的含义。

### 4.2 便捷环境变量

不想解析 JSON 的脚本可以直接读这些变量（都不含密钥）：

| 变量 | 内容 |
| --- | --- |
| `VINX_SHARE_PROTOCOL` / `VINX_SHARE_METHOD` / `VINX_SHARE_KEY` | 同请求 |
| `VINX_SHARE_FILE` / `VINX_SHARE_NAME` / `VINX_SHARE_TITLE` | `params` 里的对应字段 |
| `VINX_SHARE_REMOTE_ID` | `params.previous.remoteId`，没有时为空 |
| `VINX_SHARE_PUBLIC` | `1` 或 `0` |
| `VINX_SHARE_STATE_DIR` | 同 `stateDir` |
| `VINX_SHARE_CONFIG_<NAME>` | `target.config` 的每一项，名字转大写，如 `VINX_SHARE_CONFIG_DIR` |

进程**不继承** Vinx Docs 的环境，只有 `PATH`、`HOME`、`LANG`、`TMPDIR`（指向临时目录）、上面的变量，以及清单和目标 `passEnv` 里显式列出的变量（例如代理设置）。

### 4.3 事件（stdout，每行一个 JSON）

```text
{"event":"log","level":"info","message":"上传中"}
{"event":"progress","bytes":1048576,"total":18234567}
{"event":"result","data":{"url":"https://…","remoteId":"…","public":false}}
{"event":"error","code":"auth_required","message":"授权已失效，请在后台重新连接这个账号"}
```

- `log`、`progress` 可以有任意多条，只进本机日志（`progress` 以后给进度条用）。
- **恰好一个终止事件**：`result` 或 `error`。发出后 10 秒内要退出。
- 单行不超过 1 MiB，UTF-8。不是 JSON 的行会被忽略并记进日志，不会让任务失败；但别依赖这一点，**调用的其他工具的输出要转到 stderr**。
- stderr 只进本机日志，用户看不到。

### 4.4 各方法的 `result.data`

| 方法 | 返回 | 要求 |
| --- | --- | --- |
| `check` | `{"account": "alice@gmail.com · Vinx/客户A"}` | 确认授权有效、目录可写，**不上传文件**。保存目标前调用。一定要真的请求一次服务商（见第 9 节） |
| `publish` | `{"url", "remoteId", "public"}` | `remoteId` 由你定义，Vinx Docs 只负责保存和回传。`public` 返回**实际**状态 |
| `revoke` | `{"alreadyGone": true/false}` | 远端已经不在也算成功 |
| `auth.begin` | `{"verificationUrl", "userCode", "expiresIn", "interval"}` | 开始一次设备码授权 |
| `auth.poll` | `{"status": "pending"}` 或 `{"status": "done", "account": "alice@gmail.com"}` | 拿到令牌后写进 `stateDir`，返回显示用的账号名 |

### 4.5 错误码

| 错误码 | 什么时候用 | 用户看到的效果 |
| --- | --- | --- |
| `auth_required` | 令牌失效、刷新失败、令牌权限不够 | 账号标为「需要重新连接」，提示去后台重新连接 |
| `auth_denied` | 用户在授权页拒绝，或授权结果不能用（例如没勾选必需的权限） | 连接失败 |
| `auth_expired` | 验证码过期、没有进行中的授权 | 连接失败，提示重新开始 |
| `permission_denied` | 目录没有写权限等 | 失败 |
| `quota_exceeded` | 空间或配额不够 | 失败 |
| `not_found` | 目录或文件不存在 | 失败 |
| `conflict` | 远端状态冲突 | 失败 |
| `rate_limited` | 被限流 | 失败，可以稍后重试 |
| `network` | 连不上、超时 | 失败，可以重试 |
| `invalid_config` | 缺少或填错设置（例如没填客户端 ID） | 失败，提示去改设置 |
| `unsupported` | 不支持的协议版本或方法 | 失败 |
| `internal` | 其他 | 失败 |

`message` 写给用户看：中文、一句话、说清楚下一步做什么，最多显示 300 字。不认识的错误码按 `internal` 处理。

### 4.6 结果判定

| 情况 | 结果 |
| --- | --- |
| 有 `result`，退出码 0 | 成功 |
| 有 `error` | 失败，Vinx Docs 认为**远端没有变化** |
| 没有终止事件、退出码非 0、超时、有 `result` 但退出码非 0 | **结果未知**：远端可能已经生效，用户可以重试或撤回 |

所以：只在确定远端没有变化时才返回 `error`；上传到一半出错、不确定有没有生效时，宁可让它成为「结果未知」，靠幂等重试收拾。

超时默认 5 分钟（目标可以配置）。超时或服务停止时，先发 SIGTERM，5 秒后结束整个进程组。

## 5. 清单 `adapter.json`

```json
{
  "id": "google-drive",
  "name": "Google Drive",
  "version": "1.0.0",
  "protocol": [1],
  "builtin": true,
  "passEnv": ["VINX_GDRIVE_ENDPOINT"],
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
    { "name": "dir", "required": false, "description": "网盘里的目标目录，如「Vinx 分享」；可以写多级，如「Vinx/客户A」；留空放在根目录" }
  ],
  "secrets": [],
  "docs": "https://github.com/vinx-lab/vinx-docs/blob/main/internal/share/adapters/gdrive/README.md"
}
```

| 字段 | 说明 |
| --- | --- |
| `id` | 小写字母或数字开头，只含小写字母、数字、`.`、`_`、`-`，最长 64 个字符，全局唯一。出现在目标配置和状态目录路径里，发布后不要改 |
| `name` | 界面上显示的服务商名称 |
| `version` | 适配器自己的版本 |
| `protocol` | 支持的协议版本列表，现在写 `[1]` |
| `builtin` | `true` 表示编进主程序，不写 `command` |
| `command` / `requires` | 外部适配器的启动命令（相对路径相对于清单所在目录）和依赖的命令行工具 |
| `passEnv` | 额外放进适配器环境的变量名。内置适配器只用来放测试用的接口地址 |
| `capabilities.publicLink` / `privateLink` | 能否生成公开 / 非公开链接。不支持公开链接时，选了「公开」的目标保存不了 |
| `capabilities.update` | 能否覆盖已发布的文件。不支持时界面不显示「更新」 |
| `capabilities.stableLinkOnUpdate` | 更新后链接是否保持不变 |
| `capabilities.revoke` | 能否撤回 |
| `capabilities.auth` | `"device"` 表示支持页面上的设备码授权；不写表示不需要页面授权 |
| `provider` / `providerSecrets` | 适配器级别的设置和密钥（所有账号共用），后台「服务商设置」按它生成表单 |
| `config` / `secrets` | 每个目标的设置和密钥，后台「新增目标」按它生成表单 |
| `docs` | 准备步骤的链接，后台服务商卡片上显示为「设置说明」 |

字段列表的格式是 `{"name", "required", "description"}`。`description` 直接显示在表单上，写成用户看得懂的一句话。密钥字段在任何地方都不回显。

## 6. 适配器必须保证的语义

这些是一致性测试检查的内容，也是用户不丢数据、不重复、不泄露的底线。

1. **`publish` 幂等**：有 `previous` 时覆盖 `previous.remoteId`；没有 `previous` 时，先按 `key` 在远端找有没有这次发布留下的文件，有就覆盖，没有才新建。「结果未知」后重试不能产生第二份文件。所以 `key` 要存在服务商那边，比如 Google Drive 用文件的 `appProperties.vinxKey`。
2. **按请求设置公开**：`target.public` 为 `true` 才公开，返回的 `public` 是实际状态。和目标不一致时 Vinx Docs 按失败处理，并提示立即撤回。
3. **更新后链接不变**（声明了 `stableLinkOnUpdate` 时）：覆盖原文件的内容，不要删掉重建。
4. **`revoke` 幂等，且真的让链接失效**：文件已经不在也返回成功（`alreadyGone: true`）。如果服务商的「删除」只是移到回收站，要确认回收站里的文件不能再通过链接访问，否则先取消分享（见第 9 节）。
5. **令牌自己管**：过期时自己刷新，把新令牌写回 `stateDir`；刷新失败返回 `auth_required`。令牌文件权限 0600，不写进 stdout、日志和 `message`。
6. **stdout 只写事件**：调用其他工具时把它们的输出转到 stderr。
7. **不碰别的东西**：只读 `params.file`，只写 `stateDir` 和临时目录；不要读用户目录、不要改全局配置。

## 7. 页面授权：设备码流程

服务商支持 OAuth 设备码流程（RFC 8628）时，实现 `auth.begin` 和 `auth.poll`，清单写 `"auth": "device"`，用户就能在后台点「连接账号」，在手机或任意电脑上输入验证码完成授权。不需要回调地址，Vinx Docs 也不用对外开放端口。

过程：

1. 用户点「连接」，Vinx Docs 先生成账号 id 和状态目录，再调用 `auth.begin`。适配器向服务商申请设备码，把 `device_code` 等写进 `stateDir`（例如 `auth.json`），返回网址和验证码。
2. 页面按 `interval` 反复触发 `auth.poll`。适配器读出 `device_code` 去换令牌：
   - 用户还没操作：返回 `{"status": "pending"}`；服务商要求放慢时，增大 `interval` 后同样返回 `pending`；
   - 用户拒绝：返回 `auth_denied` 错误；过期：返回 `auth_expired` 错误；
   - 拿到令牌：**先检查实际授予的权限范围**，再写进 `stateDir`（例如 `token.json`），删掉 `auth.json`，返回 `{"status": "done", "account": "<邮箱或用户名>"}`。
3. 之后的 `check` / `publish` / `revoke` 都从 `stateDir` 读令牌。用户「重新连接」时，Vinx Docs 用同一个账号 id 和状态目录再走一遍。

一个账号的任务由 Vinx Docs 串行执行，不会有两个进程同时刷新同一份令牌。

服务商不支持设备码流程时，可以先不声明 `auth`，让用户在别处拿到长期凭据，填到 `providerSecrets` 或目标的 `secrets` 里。

## 8. 写一个内置 Go 适配器

### 8.1 目录

```text
internal/share/adapters/<id>/
  adapter.json        # 清单（第 5 节）
  <id>.go             # 实现
  <id>_test.go        # Go 测试：授权、发布、更新、撤回、错误映射、一致性测试
  <id>test/fake.go    # 假的服务端（httptest），离线测试用；只在 Go 测试里用时也可以写成 fake_test.go
  README.md           # 用户的准备步骤和实测记录（第 10.3 节）
```

### 8.2 骨架

```go
package example

import (
	_ "embed"
	"encoding/json"
	"io"

	"github.com/vinx-lab/vinx-docs/internal/share"
	"github.com/vinx-lab/vinx-docs/internal/share/kit"
)

//go:embed adapter.json
var manifest []byte

func init() { share.RegisterBuiltin(manifest, Run) }

// Run 是适配器进程的入口：vinx-docs share-adapter run <id> 会调用它。
func Run(stdin io.Reader, stdout, stderr io.Writer) int {
	return kit.Serve(stdin, stdout, map[string]kit.Handler{
		"check":   check,
		"publish": publish,
		"revoke":  revoke,
		// 支持页面授权时再加 "auth.begin" 和 "auth.poll"
	})
}

func check(req *kit.Request, out *kit.Out) (map[string]any, error) {
	// 读 stateDir 里的令牌，真的请求一次服务商，确认授权有效、目录可写。
	return map[string]any{"account": "alice@example.com · " + req.Target.Config["dir"]}, nil
}

func publish(req *kit.Request, out *kit.Out) (map[string]any, error) {
	var p kit.PublishParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.File == "" {
		return nil, kit.Errorf("internal", "publish 参数无效")
	}
	// 1. 有 p.Previous 就覆盖 p.Previous.RemoteID；否则按 req.Key 找远端已有的文件，有就覆盖，没有才新建。
	// 2. 按 req.Target.Public 设置是否公开。
	// 3. 大文件上传时可以调用 out.Progress(已发送, 总大小)。
	var link, remoteID string
	return map[string]any{"url": link, "remoteId": remoteID, "public": req.Target.Public}, nil
}

func revoke(req *kit.Request, out *kit.Out) (map[string]any, error) {
	var p kit.RevokeParams
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Previous.RemoteID == "" {
		return nil, kit.Errorf("internal", "revoke 参数无效")
	}
	// 文件已经不在：返回 alreadyGone=true。否则先取消公开分享，再删除或移到回收站。
	return map[string]any{"alreadyGone": false}, nil
}
```

`kit` 提供的东西：

| 名称 | 用途 |
| --- | --- |
| `kit.Serve(stdin, stdout, handlers)` | 读请求、检查协议版本、分发方法、写终止事件；处理函数返回 `*kit.Error` 时写成 `error` 事件 |
| `kit.Request` / `kit.Target` | 请求和目标 |
| `kit.PublishParams` / `kit.RevokeParams` / `kit.Previous` | 按方法解析 `req.Params` |
| `kit.Errorf(code, format, ...)` | 生成带错误码的错误 |
| `out.Log(format, ...)` / `out.Progress(bytes, total)` | 写 `log` / `progress` 事件 |
| `kit.ReadState` / `kit.WriteState` / `kit.RemoveState` | 读写 `stateDir` 里的 JSON 文件（原子写入，权限 0600） |

### 8.3 接进主程序

在 `internal/cli/cli.go` 的导入里加一行：

```go
_ "github.com/vinx-lab/vinx-docs/internal/share/adapters/<id>" // 内置的 XXX 适配器
```

编译后，`vinx-docs share-adapter list` 能看到它，后台「外部发布」会自动出现这个服务商的卡片。

### 8.4 要求

- 只用 Go 标准库，不加第三方依赖（ADR 0001）。
- 服务商的接口地址做成可以用环境变量覆盖（例如 `VINX_GDRIVE_ENDPOINT`），并写进清单的 `passEnv`，只给测试指向假服务端用。
- 只支持 Linux 和 macOS。

## 9. 写一个外部适配器

一个可执行程序加一份 `adapter.json`，放在同一个目录。最小的例子是 [`local-folder`](../internal/share/testdata/local-folder/)：

```sh
#!/bin/sh
set -u
emit_error() { printf '{"event":"error","code":"%s","message":"%s"}\n' "$1" "$2"; exit 0; }
[ "${VINX_SHARE_PROTOCOL:-}" = 1 ] || emit_error unsupported "不支持的协议版本"

case "${VINX_SHARE_METHOD:-}" in
check)   … printf '{"event":"result","data":{"account":"%s"}}\n' "$who" ;;
publish) … printf '{"event":"result","data":{"url":"%s","remoteId":"%s","public":%s}}\n' "$url" "$id" "$public" ;;
revoke)  … printf '{"event":"result","data":{"alreadyGone":%s}}\n' "$gone" ;;
*)       emit_error unsupported "不支持的方法：${VINX_SHARE_METHOD:-}" ;;
esac
```

注意：

- 需要密钥时从 stdin 的请求里取（`secrets`），它们不在环境变量里。
- 调用的工具输出要转到 stderr：`tool upload … >&2`。
- 拼 JSON 时注意转义引号和反斜杠；字段复杂时用 `jq` 之类的工具生成。

登记（只能在本机命令行）：

```text
vinx-docs share-adapter conformance <目录>      # 先跑离线一致性测试
vinx-docs share-target add my-target --manifest <目录>/adapter.json --label "我的服务" --allow pages \
  --config root=/tmp/out -- <目录>/adapter.sh
```

仓库收录的外部适配器放在 `share-adapters/<id>/`：`adapter.json`、入口脚本、`README.md`，以及 `test/`（桩程序和 `conformance.json`）。

## 10. 测试

### 10.1 离线一致性测试（必须通过）

一致性测试按顺序调用适配器，检查第 6 节的语义：

1. `check` 返回 `result`；stdout 只有事件行
2. 首次 `publish` 返回 `url` 和 `remoteId`，`public` 为 false；stdout 只有事件行
3. 用同一个 `key` 再 `publish`，`remoteId` 不变（不产生新文件）
4. 带 `previous` 的 `publish` 覆盖原位置；更新后链接不变
5. `revoke` 成功且 `alreadyGone` 为 false；再次 `revoke` 仍然成功且 `alreadyGone` 为 true
6. `public` 为 true 时返回公开链接
7. 支持授权的：`auth.begin` 返回网址和验证码，`auth.poll` 返回 pending 或 done
8. 不支持的 `protocol` 和不认识的 `method` 都返回 `unsupported`

怎么跑：

- **内置 Go 适配器**：在自己的 Go 测试里调用 `share.RunConformance`，指向假服务端。参照 `gdrive_test.go` 的 `TestConformance`：

  ```go
  b := share.LookupBuiltin("<id>")
  err := share.RunConformance(share.ConformanceSetup{
  	Manifest: b.Manifest, Argv: share.SelfCommand("<id>"),
  	Config:   map[string]string{"dir": "一致性 测试"}, // 带空格和中文
  	Provider: …, Secrets: …,
  	Env:      map[string]string{"VINX_<ID>_ENDPOINT": fake.URL},
  	StateDir: …, // 先用假服务端走一遍授权，令牌写在这里
  }, &out)
  ```

  测试的 `TestMain` 里要调用 `share.RunTestAdapterIfRequested()` 和 `share.UseTestBinaryForBuiltins()`，让测试二进制自己充当适配器进程（见 `gdrive_test.go`）。
- **外部适配器**：`vinx-docs share-adapter conformance <目录>`，或者 `tests/share-adapters/run.sh <目录>`。目录里的 `test/conformance.json` 提供测试用的设置：

  ```json
  {
    "config":   { "root": "{work}/remote" },
    "provider": {},
    "secrets":  {},
    "env":      {}
  }
  ```

  `{work}` 会换成测试的临时目录。`test/` 目录会放在 `PATH` 最前面，可以在里面放桩程序冒充依赖的工具，不碰真实服务。

### 10.2 假服务端

内置适配器要带一个假服务端（`net/http/httptest`），实现适配器用到的接口：授权（设备码、令牌、刷新）、上传、查找、设置公开、撤回。参照 [`gdrivetest/fake.go`](../internal/share/adapters/gdrive/gdrivetest/fake.go)，它还提供了制造异常的方法，用来测试错误路径：

- `Approve` / `Deny`：用户允许或拒绝授权；
- `ApproveWithoutDrive`：用户允许了，但没勾选必需的权限；
- `ExpireAccess` / `RevokeRefresh`：访问令牌过期（适配器应自动刷新）/ 授权被撤销（应返回 `auth_required`）；
- `Files`：查看假网盘里的文件，断言目录、公开状态、是否进了回收站。

浏览器测试也要用假服务端时，把它做成单独的包（如 `gdrivetest`），再写一个小程序启动它（如 `tests/browser/fakedrive`）。

### 10.3 真实账号测试（手动，结果写进 README）

离线测试过了，还要用真实账号走一遍。以下检查项都要做，结果写进适配器 README 的「测试」一节，注明日期：

| 检查项 | 预期 |
| --- | --- |
| 按 README 准备（建应用、拿凭据） | 步骤能照着走通 |
| 连接账号 | 后台显示「已连接 某某」 |
| 第二个账号 | 两个账号都能用，文件分别在各自账号下 |
| 多级目录 | 自动创建，`check` 通过 |
| 单文件、多文件（zip） | 服务商那边的内容完整 |
| 非公开链接 | 不登录打不开 |
| 公开链接 | 不登录能打开 |
| 更新 | 链接不变，内容是新的，没有多出文件 |
| 重复发布 | 不产生重复文件 |
| 撤回 | **公开链接**和非公开链接都打不开了 |
| 授权失效 | 在服务商那边移除授权后，提示「需要重新连接」；重新连接后恢复 |

公开和撤回要用匿名请求核对（例如 `curl` 不带任何登录信息），不能只看服务商网页上的状态。

## 11. 提交前的检查清单

- [ ] `adapter.json` 字段齐全，`description` 是用户看得懂的中文
- [ ] 实现了 `check`、`publish`、`revoke`；需要授权的实现了 `auth.begin`、`auth.poll`
- [ ] `check` 真的请求了服务商（不只是读本地令牌）
- [ ] 幂等：按 `key` 找回已发布的文件
- [ ] 撤回后公开链接确实打不开（匿名验证过）
- [ ] 授权后检查了实际授予的权限范围
- [ ] 错误码映射完整，`message` 是中文、说清楚下一步
- [ ] 密钥和令牌不出现在 stdout、stderr、日志和 `message` 里
- [ ] 只用 Go 标准库（内置适配器）
- [ ] 假服务端 + Go 测试 + 一致性测试，`go test ./...` 通过
- [ ] README：准备步骤、所需权限范围、每个字段的含义、公开 / 非公开 / 撤回的实际效果、已知限制、真实账号测试的日期和结果
- [ ] 在 `internal/cli/cli.go` 里导入了适配器
- [ ] CHANGELOG 加一行

## 12. 实测踩过的坑（Google Drive）

这些都是用真实账号测出来的，离线测试发现不了。接其他服务商时，先去确认对应的情况。

1. **授权页上的权限可以只勾一部分**。Google 的分项授权允许用户不勾 Drive 权限就点「继续」，拿到的令牌照样能刷新，但用不了 Drive。对策：`auth.poll` 拿到令牌后检查返回的 `scope`，缺少必需的权限就返回 `auth_denied` 并说明要勾选哪一项；服务商返回「权限不足」时映射成 `auth_required`，让用户重新连接。
2. **回收站里的文件，公开链接仍然能打开**。只把文件移到回收站时，「知道链接的任何人」仍然能查看和下载。对策：撤回时先删除公开权限，再移到回收站。
3. **`check` 必须真的请求一次服务商**。目录留空时，原来的实现不发任何请求，权限问题要到第一次发布才暴露。对策：`check` 至少调用一次轻量接口（Drive 用 `about`）。
4. **「测试中」的 OAuth 应用只允许测试用户授权**。别的账号会看到「尚未完成验证」和 `403 access_denied`。README 里要写清楚测试用户在控制台的哪里加，以及这个报错是什么意思。
5. **「测试中」状态的授权 7 天后过期**。README 里要说明，并告诉用户长期使用要改成正式版。

## 13. 协议怎么演进

- 加字段、加方法、加错误码都不算不兼容：适配器忽略不认识的字段，Vinx Docs 把不认识的错误码当作 `internal`。
- 不兼容的改动才提升 `protocol`。适配器在清单的 `protocol` 里声明支持哪些版本。
- 已经预留的扩展（spec 0008 第 11 节）：本机回调授权（`auth: "loopback"`）、多种输入形态（`params.kind` 和清单的 `inputs`）、进度条、一次发布到多个目标、配置字段升级成 JSON Schema。

## 14. 用 agent 开发适配器

仓库根目录的 `AGENTS.md`（Claude Code 通过 `CLAUDE.md` 引用它，Codex 直接读取）已经要求 agent 开发适配器时以本文为准。人和 agent 的分工：

| 步骤 | 谁做 | 内容 |
| --- | --- | --- |
| 1. 准备 | 人 | 服务商的 API 文档链接、授权方式；如果要做真实账号测试，自己准备测试账号和凭据，**不交给 agent** |
| 2. 方案 | agent 写，人确认 | 接口映射、幂等键存在哪、公开和撤回的实际效果、权限范围、错误码映射、清单字段、需要真实账号确认的点 |
| 3. 实现 | agent | 适配器、假服务端、Go 测试和一致性测试、README，按第 11 节自检 |
| 4. 真实账号测试 | 人（agent 可以陪着记录） | 按第 10.3 节的检查表，在自己的 Vinx Docs 后台填凭据、连接账号、发布 |
| 5. 提交 | agent 整理，人提 PR | 把真实测试结果写进 README，CHANGELOG 加一行 |

### 提示词模板

把尖括号里的内容换掉，整段发给 agent：

```text
在 vinx-docs 仓库里，为 <服务商名称> 写一个内置的「发布到外部服务」适配器，id 用 <id>。

先读：
- AGENTS.md
- docs/share-adapter-guide.md（适配器开发指南，按它做）
- internal/share/adapters/gdrive/（参照实现）
服务商资料：<API 文档链接>
授权方式：<OAuth 设备码 / 应用密钥 / 账号密码 / …>

第一步只写方案，不写代码。方案包括：
1. check、publish、revoke（需要页面授权的再加 auth.begin、auth.poll）分别调用哪些接口；
2. 幂等键 key 存在服务商的什么地方，怎么按 key 找回已发布的文件；
3. 公开链接和非公开链接怎么设置；撤回后链接是否真的失效（注意回收站、缓存）；
4. 授权需要哪些权限范围，用户能不能只授权一部分，拿到令牌后怎么检查；
5. 服务商的错误怎么映射到协议的错误码；
6. adapter.json 的 provider、providerSecrets、config、secrets 各有哪些字段；
7. 已知限制，以及哪些点只能用真实账号确认。
方案写完停下，等我确认后再实现。

实现要求：
- 只用 Go 标准库；不改主程序的发布流程和前端，只在 internal/cli/cli.go 加一行导入；
- 带假服务端（httptest）和 Go 测试，包括 share.RunConformance 一致性测试，以及授权拒绝、令牌刷新、授权失效、权限不足、撤回公开链接这些错误路径；
- go vet、gofmt、go test ./... 都通过；
- README 写清楚准备步骤、每个字段的含义、公开 / 非公开 / 撤回的实际效果和已知限制，真实账号测试一节先写「未测」；
- 最后按指南第 11 节的清单逐项自检，在汇报里列出每一项的结果和没做到的地方。

不要做的事：不要向我索要或处理真实的客户端密钥、令牌；不要请求真实服务商的接口；真实账号测试由我来做。
```

写外部适配器时，把第一句换成「写一个外部适配器（`sh` 脚本 + <工具名>），放在 `share-adapters/<id>/`」，实现要求换成「带 `test/conformance.json` 和桩程序，`tests/share-adapters/run.sh share-adapters/<id>` 通过」。

### 审 agent 的产出时重点看

- 方案里「撤回后链接是否真的失效」「用户能不能只授权一部分」有没有答案，没有的话要求它查文档，查不到就列为真实账号测试项；
- 测试有没有覆盖错误路径，而不只是正常发布；
- README 的准备步骤能不能照着走通（自己走一遍最可靠）；
- 有没有改到 `internal/share/adapters/<id>/` 和 `internal/cli/cli.go` 以外的代码，改了要说明理由。
