---
status: done
---

# Windows 开机自启

## 问题

`vinx-docs install-service` 在 Linux 写 systemd 用户单元、在 macOS 写 LaunchAgent，在 Windows 上只打印一条 `schtasks` 命令，让用户自己登记。这条命令有两个问题：

- 登录时触发的计划任务（`/SC ONLOGON`）通常要管理员权限，普通用户直接执行会失败。
- 登记的是前台的 `serve`，登录后会一直挂着一个控制台窗口；家目录还要另外设置系统环境变量。

## 方案

`install-service` 在 Windows 上写入当前用户的登录启动项：

```text
HKCU\Software\Microsoft\Windows\CurrentVersion\Run
  vinx-docs = "<程序路径>" --home "<家目录>" start
```

- 只影响当前用户，不需要管理员权限，和 Linux 的 systemd 用户单元、macOS 的 LaunchAgent 同一层级。
- 执行 `start` 而不是 `serve`：`start` 用 `DETACHED_PROCESS | CREATE_NO_WINDOW` 在后台拉起服务后自己退出，不留控制台窗口。
- 家目录用 `--home` 写在命令行里，不需要系统环境变量；用 `--assets` 指定了资源目录时一起写入。命令行按 Windows 的参数规则加引号，路径里有空格也能正确解析。
- 注册表用 `golang.org/x/sys/windows/registry` 读写（该模块原本就是间接依赖，改为直接依赖，不引入新模块）。

`uninstall-service` 删除这个注册表值，再停止由 `start` 启动的服务（相当于 Linux 的 `disable --now`）。两个命令都支持 `--dry-run`：只显示要写入或删除的注册表项和命令，不做改动。

影响的模块：`internal/cli`（`service.go`，新增 `service_windows.go`、`service_other.go`）。不改变服务本身的运行方式，也不影响 Linux 和 macOS 的行为。

## 取舍

- **任务计划程序**：可以做到崩溃后重启、登录前运行，但登录触发的任务通常要管理员权限，和其他平台「只动当前用户」的做法不一致。
- **Windows 服务**：要实现服务控制接口，默认以系统账户运行，家目录、文档目录的权限和当前用户对不上。
- **启动文件夹里放快捷方式**：效果和 Run 键相同，但要生成 `.lnk`（需要 COM 或额外代码），不如一个注册表值简单、好检查。

已知限制，写进文档：

- 登录时可能闪一下控制台窗口：`vinx-docs.exe` 是控制台程序，`start` 执行完才退出。
- 没有崩溃自动重启（systemd 有 `Restart=on-failure`）；服务退出后要手动 `vinx-docs start` 或重新登录。
- 用户登录后才启动；开机但没人登录时不运行。

以后如果做 Windows 托盘程序或安装包，可以换成 GUI 子系统的启动器，去掉闪窗。

## 验证

- 单元测试：命令行的引号规则（空格、结尾反斜杠、内嵌引号）；Run 值的内容（`--home`、`--assets`、`start`）；`--dry-run` 在各平台都不写入。
- 交叉编译 `GOOS=windows`；在 Windows 上运行编好的程序的 `install-service --dry-run` / `uninstall-service --dry-run`，确认输出。
- CI 的 windows-latest 跑单元测试。
- 真实写入注册表、注销重新登录后服务自动启动：需要在 Windows 机器上手动验证。

## 结果

- 实现了上述方案：`install-service` 写入 Run 值，`uninstall-service` 删除 Run 值并停止服务，`--dry-run` 只显示改动。
- 已验证：Linux 上 `go vet`、`go test`；交叉编译 Windows 版本；在 Windows 上运行 `install-service --dry-run` 和 `uninstall-service --dry-run`，输出的注册表项和命令行正确，没有写入注册表。
- 未验证：真实写入注册表、重新登录后自动启动，以及 `uninstall-service` 实际删除注册表值和停止服务。
