package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/server"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// ServiceName 是 systemd 单元 / launchd 标签名，也是 Windows 登录启动项的值名。
const ServiceName = "vinx-docs"

// WindowsRunKey 是 Windows 当前用户的登录启动项（相对 HKEY_CURRENT_USER）。
const WindowsRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

var serviceHelp = map[string]string{
	"install-service":   "登记开机自动启动（Linux systemd 用户单元 / macOS launchd / Windows 当前用户的登录启动项）。",
	"uninstall-service": "移除开机自动启动，并停止由它管理的服务。",
}

// serviceCommand 返回开机启动项要执行的命令：当前二进制的 serve。
func serviceCommand() ([]string, error) {
	program, err := server.Executable()
	if err != nil {
		return nil, err
	}
	return []string{program, "serve"}, nil
}

func quoteSpace(value string) string {
	if strings.Contains(value, " ") {
		return `"` + value + `"`
	}
	return value
}

// serviceEnv 是写进启动项的环境变量：家目录；用 --assets 指定了资源目录时一并写入。
func serviceEnv(home string) [][2]string {
	env := [][2]string{{"VINX_DOCS_HOME", home}}
	for _, item := range server.ExtraEnv {
		if key, value, ok := strings.Cut(item, "="); ok {
			env = append(env, [2]string{key, value})
		}
	}
	return env
}

// SystemdUnit 生成 systemd 用户单元的内容。
func SystemdUnit(home string, command []string) string {
	lines := []string{"[Unit]", "Description=Vinx Docs", "After=network.target", "", "[Service]", "Type=simple"}
	for _, pair := range serviceEnv(home) {
		lines = append(lines, "Environment="+quoteSpace(pair[0]+"="+pair[1]))
	}
	parts := make([]string, len(command))
	for i, part := range command {
		parts[i] = quoteSpace(part)
	}
	lines = append(lines, "ExecStart="+strings.Join(parts, " "), "Restart=on-failure", "", "[Install]", "WantedBy=default.target", "")
	return strings.Join(lines, "\n")
}

// windowsQuote 按 Windows 命令行的解析规则（CommandLineToArgvW）给参数加引号，与 syscall.EscapeArg 相同。
func windowsQuote(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\"") {
		return value
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range value {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// WindowsRunCommand 生成写进 Windows 登录启动项的命令行：用 start 在后台拉起服务后退出，不留控制台窗口；
// 家目录（以及 --assets 指定的资源目录）用全局参数写在命令行里，不依赖系统环境变量。
func WindowsRunCommand(home, program string) string {
	parts := []string{program, "--home", home}
	for _, pair := range serviceEnv(home) {
		if pair[0] == "VINX_DOCS_ASSETS" {
			parts = append(parts, "--assets", pair[1])
		}
	}
	parts = append(parts, "start")
	for i, part := range parts {
		parts[i] = windowsQuote(part)
	}
	return strings.Join(parts, " ")
}

func xmlEscape(value string) string {
	// saxutils.escape：只转义 & < >。
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// LaunchdPlist 生成 macOS launchd 的 plist 内容。
func LaunchdPlist(home string, command []string) string {
	var args strings.Builder
	for _, part := range command {
		args.WriteString("<string>" + xmlEscape(part) + "</string>")
	}
	var env strings.Builder
	for _, pair := range serviceEnv(home) {
		fmt.Fprintf(&env, "\n    <key>%s</key><string>%s</string>", pair[0], xmlEscape(pair[1]))
	}
	logPath := xmlEscape(textutil.Join(home, ".runtime", "server.log"))
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + ServiceName + `</string>
  <key>ProgramArguments</key><array>` + args.String() + `</array>
  <key>EnvironmentVariables</key><dict>` + env.String() + `
  </dict>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>` + logPath + `</string>
  <key>StandardErrorPath</key><string>` + logPath + `</string>
</dict></plist>
`
}

func run(check bool, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if check && err != nil {
		return fmt.Errorf("命令 %s %s 失败：%w", name, strings.Join(args, " "), err)
	}
	return nil
}

// service 只写启动项文件并登记，不替你启动，避免和已经用 start 跑着的服务抢端口。
//
// --dry-run 只打印将写入的文件路径和内容，不写文件、不调用 systemctl / launchctl。
func service(action, home string, extra []string) int {
	dryRun := false
	for _, arg := range extra {
		switch arg {
		case "--dry-run":
			dryRun = true
		case "-h", "--help":
			fmt.Fprintf(stdout, "usage: vinx-docs %s [-h] [--dry-run]\n\n%s\n\noptions:\n  -h, --help  show this help message and exit\n  --dry-run   只显示要写入或删除的文件和要执行的命令，不做改动\n", action, serviceHelp[action])
			return 0
		default:
			// 不认识的参数一律拒绝：这两个命令会改开机启动项，参数写错时不能照常执行。
			errorf("usage: vinx-docs %s [-h] [--dry-run]\nvinx-docs %s: error: unrecognized arguments: %s\n", action, action, arg)
			return 2
		}
	}
	command, err := serviceCommand()
	if err != nil {
		errorf("error: %s\n", err)
		return 1
	}
	userHome := textutil.Home()
	fail := func(err error) int {
		errorf("error: %s\n", err)
		return 1
	}
	switch runtime.GOOS {
	case "linux":
		unit := textutil.Join(userHome, ".config", "systemd", "user", ServiceName+".service")
		if dryRun {
			if action == "uninstall-service" {
				fmt.Fprintf(stdout, "将执行：systemctl --user disable --now %s.service；删除 %s；systemctl --user daemon-reload\n", ServiceName, unit)
				return 0
			}
			fmt.Fprintf(stdout, "将写入 %s：\n%s", unit, SystemdUnit(home, command))
			fmt.Fprintf(stdout, "并执行：systemctl --user daemon-reload；systemctl --user enable %s.service\n", ServiceName)
			return 0
		}
		if action == "uninstall-service" {
			_ = run(false, "systemctl", "--user", "disable", "--now", ServiceName+".service")
			if err := os.Remove(unit); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fail(err)
			}
			_ = run(false, "systemctl", "--user", "daemon-reload")
			fmt.Fprintf(stdout, "已移除 %s\n", unit)
			return 0
		}
		if err := os.MkdirAll(textutil.Parent(unit), 0o777); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(unit, []byte(SystemdUnit(home, command)), 0o666); err != nil {
			return fail(err)
		}
		if err := run(true, "systemctl", "--user", "daemon-reload"); err != nil {
			return fail(err)
		}
		if err := run(true, "systemctl", "--user", "enable", ServiceName+".service"); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "已写入并启用 %s，下次登录自动启动。\n", unit)
		fmt.Fprintln(stdout, "现在就切换到 systemd 管理：vinx-docs stop && systemctl --user start vinx-docs")
		fmt.Fprintln(stdout, "注销后仍要运行，需要开启 linger：sudo loginctl enable-linger $USER")
		return 0
	case "darwin":
		plist := textutil.Join(userHome, "Library", "LaunchAgents", ServiceName+".plist")
		if dryRun {
			if action == "uninstall-service" {
				fmt.Fprintf(stdout, "将执行：launchctl unload -w %s；删除 %s\n", plist, plist)
				return 0
			}
			fmt.Fprintf(stdout, "将写入 %s：\n%s", plist, LaunchdPlist(home, command))
			return 0
		}
		if action == "uninstall-service" {
			_ = run(false, "launchctl", "unload", "-w", plist)
			if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fail(err)
			}
			fmt.Fprintf(stdout, "已移除 %s\n", plist)
			return 0
		}
		if err := os.MkdirAll(textutil.Parent(plist), 0o777); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(plist, []byte(LaunchdPlist(home, command)), 0o666); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "已写入 %s。登记并启动：vinx-docs stop && launchctl load -w %s\n", plist, plist)
		return 0
	}
	// Windows：写当前用户的 Run 值，不需要管理员权限。
	runKey := `HKEY_CURRENT_USER\` + WindowsRunKey
	value := WindowsRunCommand(home, command[0])
	if dryRun {
		if action == "uninstall-service" {
			fmt.Fprintf(stdout, "将删除注册表值 %s\\%s，并停止由 start 启动的服务\n", runKey, ServiceName)
			return 0
		}
		fmt.Fprintf(stdout, "将写入注册表值 %s\\%s：\n%s\n", runKey, ServiceName, value)
		return 0
	}
	if action == "uninstall-service" {
		if err := deleteRunValue(ServiceName); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "已移除登录启动项 %s\\%s\n", runKey, ServiceName)
		code, err := server.Manage(home, "stop")
		if err != nil {
			return fail(err)
		}
		return code
	}
	if err := setRunValue(ServiceName, value); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "已写入登录启动项 %s\\%s，下次登录自动启动：\n%s\n", runKey, ServiceName, value)
	fmt.Fprintln(stdout, "现在就启动：vinx-docs start")
	return 0
}
