# 发布到外部服务

把短链接页面的源文件发布到网盘等外部服务，拿到链接。发出去的内容和「下载源文件」相同：只有一个文件时原样发送，有多个文件时打成 zip。设计见 [spec 0008](specs/0008-share-targets.md) 和 [ADR 0006](architecture-decisions/0006-share-adapters-via-local-commands.md)。

目前内置的服务商是 Google Drive，不需要安装其他程序。

## 概念

- **服务商设置**：适配器级别的设置，所有账号共用，例如 Google 的 OAuth 客户端。
- **账号**：在服务商上授权过的身份，例如一个 Google 账号。可以连接多个。
- **发布目标**：账号加上目录、是否公开、允许发布哪些来源。一个账号可以有多个目标，在「发布到 ▾」菜单里各占一项。
- **发布记录**：某个页面发到某个目标的结果，用来打开链接、更新和撤回。

## 设置（后台「外部发布」，内网或外部访问登录后操作）

1. **服务商设置**：按 [Google Drive 适配器说明](../internal/share/adapters/gdrive/README.md#准备创建-oauth-客户端一次约-5-分钟) 创建 OAuth 客户端，把客户端 ID 和密钥填进来。
2. **连接账号**：点「连接 Google Drive 账号」，在任意设备上打开显示的网址，输入验证码，登录后点「允许」。
3. **新增目标**：填显示名称，选账号，填目录（如 `Vinx/客户A`），勾选「允许发布短链接页面」，选择是否公开链接（选公开时会再确认一次）。保存前会检查一次账号和目录。

目标默认不能发布任何内容，必须勾选允许的来源。还有有效发布记录的目标，不能修改账号、目录和公开设置（先撤回，或新建一个目标）；删除时会提示删除后不能再更新或撤回。

## 发布、更新、撤回

- 短链接阅读页顶栏（Markdown、图片、音视频）和页面列表顶栏（HTML）的「发布到 ▾」，选一个目标，在发布框里点「发布」，完成后显示链接和「复制」。
- 源文件改过以后，菜单和发布框里显示「源文件已修改」，点「更新」覆盖上次的文件，链接不变。
- 「撤回」：Google Drive 是先取消公开、再移到回收站，链接随之失效。
- 「结果未知」：适配器超时、异常退出，或服务在任务中途重启时，远端可能已经生效。可以「重试」（覆盖同一个位置，不会产生重复文件），或「撤回」。
- 账号授权失效时，菜单里显示「账号授权已失效」，到后台点「重新连接」。
- 单独打开的 HTML 页面右下角有「发布到…」，会跳到页面列表并展开菜单。
- 取消短链接不会自动撤回已经发出去的文件，页面列表和 `unpublish` 的输出会提示还有几处。
- 经反向代理的外部访问要先登录，登录后和内网一样；未登录（包括短链接免登录期内）看不到「发布到」，也不能管理目标。

## 命令行

```text
vinx-docs share-provider set google-drive --config clientId=… --secret clientSecret=…
vinx-docs share-account connect google-drive        # 显示网址和验证码，授权后自动继续
vinx-docs share-account list | remove <账号 id>
vinx-docs share-target add gd-work --adapter google-drive --account <账号 id> --label "Drive · 工作" --allow pages --config "dir=Vinx 分享"
vinx-docs share-target list | test <id> | remove <id> [--force]
vinx-docs share <短码> --to <目标>                  # 经本机服务执行
vinx-docs shares [--page <短码>] | unshare <记录ID> | shares forget <记录ID>
```

配置在家目录的 `config/share-targets.json`（权限 0600，含服务商密钥）；令牌在 `share-state/<适配器>/<账号 id>/`（权限 0700）；发布记录在 `config/shares.json`。

## 写一个新的适配器

接入其他服务商（网盘、文档平台等）见 [适配器开发指南](share-adapter-guide.md)：协议、清单、内置 Go 适配器和外部适配器两种写法、测试方法、提交前的检查清单，以及接 Google Drive 时踩过的坑。
