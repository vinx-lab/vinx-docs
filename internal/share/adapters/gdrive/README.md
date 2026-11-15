# Google Drive 适配器

内置在 vinx-docs 里，直接调用 Google Drive API，不需要安装其他程序。授权用 OAuth 设备码流程，在后台「外部发布」里完成，手机或电脑都可以。实现见 [spec 0008](../../../../docs/specs/0008-share-targets.md) 第 9 节；写新的适配器见 [适配器开发指南](../../../../docs/share-adapter-guide.md)，代码可以参照这个目录。

## 准备：创建 OAuth 客户端（一次，约 5 分钟）

调用 Google 接口需要一个「应用身份」。每个 Vinx Docs 用户自己创建一个：

1. 打开 Google Cloud 控制台，新建一个项目（名称随意，例如 `vinx-docs`）。
2. 「API 和服务 → 库」里搜索 **Google Drive API**，点「启用」。
3. 「API 和服务 → OAuth 同意屏幕」（新版控制台叫「Google Auth Platform」）：
   - 用户类型选「外部」；填写应用名称和你的邮箱。
   - 数据访问（范围）里添加 `.../auth/drive.file`、`email`、`openid`。`drive.file` 只能访问这个应用自己创建的文件，看不到网盘里原有的内容。
   - 发布状态：
     - 「测试中」：要把你要连接的 Google 账号加进「测试用户」（新版控制台在「Google Auth Platform → 目标对象（Audience）→ 测试用户 → 添加用户」）。没加的账号授权时会看到「尚未完成 Google 验证流程……仅供已获开发者批准的测试人员使用」和「错误 403：access_denied」。这个状态下 Google 签发的授权 **7 天后过期**，过期后要在后台「重新连接」。
     - 「正式版」（点「发布应用」）：没有 7 天的限制。`drive.file` 属于 Google 的「非敏感」范围，不需要敏感或受限范围的额外审核；授权时是否显示「未经验证的应用」提示，还没有实测确认。
4. 「API 和服务 → 凭据 → 创建凭据 → OAuth 客户端 ID」，应用类型选 **「电视和输入受限的设备」**（TVs and Limited Input devices），创建后记下客户端 ID 和客户端密钥。
5. 在 Vinx Docs 后台「外部发布 → Google Drive → 服务商设置」里填入这两项，保存。密钥保存后不再显示。

命令行也可以：`vinx-docs share-provider set google-drive --config clientId=… --secret clientSecret=…`。

## 连接账号

后台点「连接 Google Drive 账号」，页面显示一个网址、一个验证码和二维码。在任意设备上打开网址（手机扫码也行），输入验证码，登录 Google，**勾选「查看、修改、创建和删除您通过此应用使用的特定 Google 云端硬盘文件」**后点「继续」或「允许」，后台会自动显示「已连接 某某@gmail.com」。可以连接多个账号。

令牌保存在家目录的 `share-state/google-drive/<账号 id>/token.json`（权限 0600），不进配置文件。断开账号会删除这个目录。

命令行：`vinx-docs share-account connect google-drive`。

授权时没勾选上面那一项：Google 仍然算「允许」，但令牌用不了 Drive。新版会在连接时直接提示；之前已经连上的账号，保存目标时会提示，账号标为「需要重新连接」，点「重新连接」并勾选即可。

授权页显示「错误 403：access_denied」：这个 Google 账号不在测试用户里，按上面第 3 步加进去后，回后台重新点「连接 Google Drive 账号」（之前的验证码已作废）。

## 目标配置

| 字段 | 说明 |
| --- | --- |
| `dir` | 网盘里的目录，可以写多级，如 `Vinx/客户A`；不存在时自动创建；留空放在「我的云端硬盘」根目录 |

每个目标还要选：账号、是否公开链接、是否允许发布短链接页面、文件名按标题还是按原文件名。

## 行为

| 操作 | 效果 |
| --- | --- |
| 发布 | 可续传上传到目标目录；文件带 `appProperties.vinxKey`（发布记录 id），重试时按它找回原文件，不产生重复 |
| 非公开链接 | 文件的 `webViewLink`，只有这个 Google 账号本人能打开 |
| 公开链接 | 给文件加「知道链接的任何人都可查看」权限 |
| 更新 | 覆盖原文件内容，文件 ID 和链接不变 |
| 撤回 | 先取消公开权限，再把文件移到回收站，链接随之失效（回收站里的文件公开链接仍能打开，所以要先取消公开）；用户还能从回收站恢复，恢复后只有本人能看。文件已经不在也算成功 |

令牌过期时自动刷新；刷新被拒（`invalid_grant`）时报「需要重新授权」，账号标为「授权已失效」，到后台点「重新连接」。

错误码：401 → `auth_required`；403 `storageQuotaExceeded` → `quota_exceeded`；403 `rateLimitExceeded`、`userRateLimitExceeded` 和 429 → `rate_limited`；其他 403 → `permission_denied`；404 → `not_found`；5xx 和网络错误 → `network`。

## 已知限制

- 页面标题改了以后再更新，Drive 上仍沿用第一次发布时的文件名（为了保持链接不变）。
- 只支持 Linux 和 macOS。

## 测试

- 离线：`go test ./internal/share/adapters/gdrive/`。用 `gdrivetest` 里的假 Google 服务（设备码授权、令牌、Drive 文件接口），覆盖授权的允许和拒绝、两个账号、逐级建目录、公开和非公开、更新不产生新文件、令牌自动刷新、授权失效、撤回，以及一致性测试。接口地址通过环境变量 `VINX_GDRIVE_ENDPOINT` 指向假服务，只在测试里使用。
- 浏览器：`tests/browser/share.cjs`，用 `tests/browser/fakedrive` 启动同一个假服务，在页面上走完整个流程。
- 真实账号实测（2026-11-14，「测试中」状态，一个账号）：设备码授权、多级目录、单文件和 zip、非公开和公开链接、更新后链接不变、重复发布、撤回都已通过，过程中修复了「没勾选 Drive 权限」和「回收站里公开链接仍可访问」两个问题，详见仓库里的 `docs/share-targets-test.md`。还没测：第二个账号、授权被撤销后重新连接、「测试中」7 天过期、「正式版」授权页提示、「结果未知」后重试。
