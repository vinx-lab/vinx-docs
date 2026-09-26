package cli

// 帮助和报错文案是固定的输出格式，这里逐字比对。

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vinx-lab/vinx-docs/internal/testutil"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	SetOutput(&out, &errOut)
	defer SetOutput(os.Stdout, os.Stderr)
	code := Main(args)
	return code, out.String(), errOut.String()
}

func TestHelpAndErrorsAreStable(t *testing.T) {
	home := filepath.Join(testutil.TempDir(t), "home")
	cases := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"publish", "--help"}, 0, `usage: vinx-docs publish [-h] [--root ROOT] [--file FILE] [--title TITLE]
                         [--json]
                         entry

positional arguments:
  entry          入口 .html 文件

options:
  -h, --help     show this help message and exit
  --root ROOT    文件清单的根目录，默认是入口页所在目录
  --file FILE    额外发布的文件（相对根目录），可重复
  --title TITLE
  --json
`, ""},
		{[]string{"register"}, 2, "", `usage: vinx-docs register [-h] [--config CONFIG] [--output OUTPUT] --id ID
                          --name NAME --docs DOCS [--home HOME]
                          [--exclude EXCLUDE]
vinx-docs register: error: the following arguments are required: --id, --name, --docs
`},
		{[]string{"comments", "--s", "x"}, 2, "", `usage: vinx-docs comments [-h] [--status STATUS] [--scope SCOPE] [--json]
vinx-docs comments: error: ambiguous option: --s could match --status, --scope
`},
		{[]string{"refresh", "extra"}, 2, "", `usage: vinx-docs [-h] [--config CONFIG] [--output OUTPUT]
                 {validate,refresh,register,unregister,settings} ...
vinx-docs: error: unrecognized arguments: extra
`},
		{[]string{"watch", "--agent", "x"}, 2, "", `usage: vinx-docs watch [-h] [--agent {claude,codex}] [--name NAME]
                       [--scope SCOPE] [--interval INTERVAL]
vinx-docs watch: error: argument --agent: invalid choice: 'x' (choose from claude, codex)
`},
		{[]string{"comment", "--json=1", "3"}, 2, "", `usage: vinx-docs comment [-h] [--json] id
vinx-docs comment: error: argument --json: ignored explicit argument '1'
`},
		{[]string{"start", "x"}, 2, "", `usage: vinx-docs [-h] {serve,start,stop,status,restart}
vinx-docs: error: unrecognized arguments: x
`},
		{[]string{"--version"}, 0, "0.1.0\n", ""},
	}
	for _, c := range cases {
		code, stdout, stderr := runCLI(t, append([]string{"--home", home}, c.args...)...)
		if code != c.code || stdout != c.stdout || stderr != c.stderr {
			t.Errorf("%v\ncode=%d\nstdout=%q\nstderr=%q", c.args, code, stdout, stderr)
		}
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	code, stdout, _ := runCLI(t)
	if code != 0 || !strings.HasPrefix(stdout, "vinx-docs 0.1.0 — 本地的 agent 产出审阅台\n") ||
		!strings.Contains(stdout, "  uninstall-service 移除开机自动启动\n") || !strings.Contains(stdout, "  start             后台启动服务（先构建站点）\n") {
		t.Fatal(stdout)
	}
	code, _, stderr := runCLI(t, "bogus")
	if code != 2 || !strings.HasPrefix(stderr, "未知命令：bogus\n\nvinx-docs 0.1.0") {
		t.Fatal(code, stderr)
	}
}

func TestIntAndFloatArgParsing(t *testing.T) {
	for text, want := range map[string]string{"5": "5", " +7 ": "7", "1_000": "1000", "-3": "-3"} {
		if got, ok := parseIntArg(text); !ok || string(got) != want {
			t.Errorf("int(%q) = %v %v", text, got, ok)
		}
	}
	for _, text := range []string{"1.5", "x", "_1", "1__0", ""} {
		if _, ok := parseIntArg(text); ok {
			t.Errorf("int(%q) 应失败", text)
		}
	}
	for text, want := range map[string]float64{"2": 2, "0.5": 0.5, "1e1": 10, " 3 ": 3} {
		if got, ok := parseFloatArg(text); !ok || got != want {
			t.Errorf("float(%q) = %v %v", text, got, ok)
		}
	}
	if _, ok := parseFloatArg("abc"); ok {
		t.Error("float('abc') 应失败")
	}
}

func TestInstallServiceDryRunWritesNothing(t *testing.T) {
	fakeHome := filepath.Join(testutil.TempDir(t), "user")
	t.Setenv("HOME", fakeHome)
	home := filepath.Join(testutil.TempDir(t), "data dir")
	code, stdout, _ := runCLI(t, "--home", home, "install-service", "--dry-run")
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(fakeHome, ".config")); err == nil {
		t.Fatal("dry-run 不能写文件")
	}
	// 每个平台写的启动项不同：Linux 是 systemd 用户单元，macOS 是 LaunchAgent，Windows 只打印计划任务的做法。
	switch runtime.GOOS {
	case "linux":
		if !strings.Contains(stdout, `Environment="VINX_DOCS_HOME=`+home+`"`) || !strings.Contains(stdout, " serve\nRestart=on-failure\n") {
			t.Fatal(stdout)
		}
	case "darwin":
		if !strings.Contains(stdout, filepath.Join(fakeHome, "Library", "LaunchAgents", "vinx-docs.plist")) ||
			!strings.Contains(stdout, "<key>VINX_DOCS_HOME</key><string>"+home+"</string>") {
			t.Fatal(stdout)
		}
	case "windows":
		if !strings.Contains(stdout, "schtasks /Create") || !strings.Contains(stdout, "VINX_DOCS_HOME="+filepath.ToSlash(home)+"\n") {
			t.Fatal(stdout)
		}
	}
	unit := SystemdUnit("/h", []string{"/bin/vinx-docs", "serve"})
	want := "[Unit]\nDescription=Vinx Docs\nAfter=network.target\n\n[Service]\nType=simple\nEnvironment=VINX_DOCS_HOME=/h\n" +
		"ExecStart=/bin/vinx-docs serve\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
	if unit != want {
		t.Fatalf("%q", unit)
	}
	if plist := LaunchdPlist("/h", []string{"/bin/vinx-docs", "serve"}); !strings.Contains(plist, "<array><string>/bin/vinx-docs</string><string>serve</string></array>") {
		t.Fatal(plist)
	}
}

// 每个命令加 --help 都只打印帮助、退出码 0，不执行任何动作。
// 为防止回归时真的动到本机，测试期间 HOME 指向临时目录、PATH 置空（找不到 systemctl 等外部命令）。
func TestEveryCommandHonorsHelp(t *testing.T) {
	tmp := testutil.TempDir(t)
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", filepath.Join(tmp, "empty-path"))
	t.Setenv("VINX_DOCS_HOME", filepath.Join(tmp, "home"))
	for _, line := range strings.Split(usage(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(line, "  ") || strings.HasPrefix(fields[0], "-") {
			continue
		}
		command := fields[0]
		code, out, errOut := runCLI(t, command, "--help")
		if code != 0 || !strings.HasPrefix(out, "usage: vinx-docs ") || errOut != "" {
			t.Errorf("%s --help: code=%d stdout=%q stderr=%q", command, code, out, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, ".config")); err == nil {
		t.Error("--help 不应写入任何文件")
	}
}

func TestServiceRejectsUnknownArguments(t *testing.T) {
	tmp := testutil.TempDir(t)
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", filepath.Join(tmp, "empty-path"))
	for _, command := range []string{"install-service", "uninstall-service"} {
		code, _, errOut := runCLI(t, command, "--dryrun")
		if code != 2 || !strings.Contains(errOut, "unrecognized arguments: --dryrun") {
			t.Errorf("%s --dryrun: code=%d stderr=%q", command, code, errOut)
		}
	}
}
