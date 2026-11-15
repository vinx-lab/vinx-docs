package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/pages"
	"github.com/vinx-lab/vinx-docs/internal/server"
	"github.com/vinx-lab/vinx-docs/internal/share"
)

// 发布到外部服务（spec 0008）。内置适配器的服务商设置、账号和目标也可以在后台页面上管理；
// 外部适配器（命令）只能在这里登记。发布、撤回经本机服务执行（任务和记录由服务管理）。

const shareTargetUsage = `usage: vinx-docs share-target {add,list,test,remove} ...

  vinx-docs share-target add <id> --adapter <内置适配器> --label <名称> --allow pages [--account <账号 id>] [--public] [--yes]
        [--config KEY=VALUE ...] [--naming title|file] [--no-check]
  vinx-docs share-target add <id> --manifest <adapter.json> --label <名称> --allow pages [--public] [--yes]
        [--config KEY=VALUE ...] [--secret KEY=VALUE ...] [--pass-env NAME ...] [--timeout 5m] [--max-size 200MB] -- <命令> [参数...]
  vinx-docs share-target list [--json]        列出目标（不显示密钥的值）
  vinx-docs share-target test <id>            调用适配器的 check
  vinx-docs share-target remove <id> [--force]

内置适配器的目标也可以在后台「外部发布」里管理；外部适配器（命令）只能在这里登记（ADR 0006）。
--allow 默认不开启任何来源，第一版只有 pages（短链接页面）。--public 表示生成「知道链接的任何人都能打开」的链接。`

const shareAccountUsage = `usage: vinx-docs share-account {list,connect,remove} ...

  vinx-docs share-account list                       列出已连接的账号
  vinx-docs share-account connect <内置适配器>       连接新账号：显示网址和验证码，在任意设备上打开网址完成授权
  vinx-docs share-account connect --account <账号 id> 重新连接（授权失效时）
  vinx-docs share-account remove <账号 id>           断开账号，删除本机令牌（还有目标在用时拒绝）

也可以在后台「外部发布」里操作。`

const shareProviderUsage = `usage: vinx-docs share-provider set <内置适配器> [--config KEY=VALUE ...] [--secret KEY=VALUE ...]

保存服务商设置（例如 Google 的 OAuth 客户端 ID 和密钥），所有账号共用。也可以在后台「外部发布」里填写。`

const shareAdapterUsage = `usage: vinx-docs share-adapter {list,conformance} ...

  vinx-docs share-adapter list                显示内置适配器及其服务商设置是否已填写
  vinx-docs share-adapter conformance <目录>  对外部适配器跑离线一致性测试（贡献适配器时用）`

const shareUsage = `usage: vinx-docs {share,unshare,shares} ...

  vinx-docs share <短码> --to <目标>          发布或更新（经本机服务执行）
  vinx-docs unshare <记录ID>                  撤回
  vinx-docs shares [--page <短码>] [--json]   列出发布记录
  vinx-docs shares forget <记录ID>            删除一条「结果未知」或「已撤回」的本机记录，不动远端`

func shareMain(args []string) int {
	command, rest := args[0], args[1:]
	// 内置适配器的入口：主程序以 share-adapter run <id> 启动自己，按协议读 stdin、写 stdout。
	if command == "share-adapter" && len(rest) == 2 && rest[0] == "run" {
		b := share.LookupBuiltin(rest[1])
		if b == nil {
			errorf("error: 没有内置适配器 %s\n", rest[1])
			return 2
		}
		return b.Run(os.Stdin, stdout, stderr)
	}
	usages := map[string]string{"share-target": shareTargetUsage, "share-account": shareAccountUsage,
		"share-provider": shareProviderUsage, "share-adapter": shareAdapterUsage}
	usage := usages[command]
	if usage == "" {
		usage = shareUsage
	}
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	home, err := config.DataHome()
	if err != nil {
		return finishCommand(err)
	}
	switch command {
	case "share-target":
		err = shareTargetMain(home, rest)
	case "share-account":
		err = shareAccountMain(home, rest)
	case "share-provider":
		err = shareProviderMain(home, rest)
	case "share-adapter":
		err = shareAdapterMain(home, rest)
	case "share":
		err = shareRun(home, rest)
	case "unshare":
		err = unshareRun(home, rest)
	default:
		err = sharesRun(home, rest)
	}
	if err != nil {
		if usageErr, ok := err.(usageError); ok {
			errorf("error: %s\n\n%s\n", usageErr, usage)
			return 2
		}
		errorf("error: %s\n", err)
		return 1
	}
	return 0
}

type usageError string

func (e usageError) Error() string { return string(e) }

// flags 是一个简单的参数解析：--name value（可重复）、开关、-- 之后的命令。
type flags struct {
	values  map[string][]string
	bools   map[string]bool
	args    []string
	command []string
}

func parseFlags(args []string, valued, switches []string) (*flags, error) {
	f := &flags{values: map[string][]string{}, bools: map[string]bool{}}
	isValued := map[string]bool{}
	for _, name := range valued {
		isValued[name] = true
	}
	isSwitch := map[string]bool{}
	for _, name := range switches {
		isSwitch[name] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			f.command = args[i+1:]
			return f, nil
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(arg[2:], "=")
			if isSwitch[name] && !hasValue {
				f.bools[name] = true
				continue
			}
			if !isValued[name] {
				return nil, usageError("不认识的参数 --" + name)
			}
			if !hasValue {
				if i+1 >= len(args) {
					return nil, usageError("--" + name + " 需要一个值")
				}
				i++
				value = args[i]
			}
			f.values[name] = append(f.values[name], value)
		default:
			f.args = append(f.args, arg)
		}
	}
	return f, nil
}

func (f *flags) one(name string) string {
	if v := f.values[name]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func pairs(items []string, what string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range items {
		k, v, ok := strings.Cut(item, "=")
		if !ok || k == "" {
			return nil, usageError(what + " 应写成 KEY=VALUE：" + item)
		}
		out[k] = v
	}
	return out, nil
}

func newShareService(home string) *share.Service {
	return share.NewService(home, pages.DefaultRegistry(home), server.Stderr)
}

func confirmPublic(f *flags) error {
	if !f.bools["public"] || f.bools["yes"] {
		return nil
	}
	fmt.Fprint(stdout, "这个目标会生成「知道链接的任何人都能打开」的公开链接。确认？[y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return fmt.Errorf("已取消")
	}
	return nil
}

func shareTargetMain(home string, args []string) error {
	if len(args) == 0 {
		return usageError("缺少子命令")
	}
	sub, args := args[0], args[1:]
	svc := newShareService(home)
	switch sub {
	case "add":
		return shareTargetAdd(home, args)
	case "list":
		f, err := parseFlags(args, nil, []string{"json"})
		if err != nil {
			return err
		}
		return shareTargetList(svc, f.bools["json"])
	case "test":
		if len(args) != 1 {
			return usageError("用法：share-target test <id>")
		}
		account, err := svc.TestTarget(args[0])
		if err != nil {
			return fmt.Errorf("检查没通过：%v", err)
		}
		fmt.Fprintf(stdout, "检查通过：%s\n", account)
		return nil
	case "remove":
		f, err := parseFlags(args, nil, []string{"force"})
		if err != nil {
			return err
		}
		if len(f.args) != 1 {
			return usageError("用法：share-target remove <id> [--force]")
		}
		if err := svc.RemoveTarget(f.args[0], f.bools["force"]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "已删除发布目标 %s\n", f.args[0])
		return nil
	}
	return usageError("不认识的子命令 " + sub)
}

func shareTargetAdd(home string, args []string) error {
	f, err := parseFlags(args, []string{"adapter", "account", "manifest", "label", "allow", "config", "secret", "naming", "pass-env", "timeout", "max-size"},
		[]string{"public", "yes", "no-check"})
	if err != nil {
		return err
	}
	if len(f.args) != 1 {
		return usageError("需要一个目标 id")
	}
	id := f.args[0]
	if !share.ValidTargetID(id) {
		return usageError("目标 id 只能用小写字母、数字、点、下划线和连字符：" + id)
	}
	cfg, err := pairs(f.values["config"], "--config")
	if err != nil {
		return err
	}
	sources := f.values["allow"]
	if sources == nil {
		sources = []string{}
	}
	st, err := share.LoadStore(home)
	if err != nil {
		return err
	}
	if st.Target(id) != nil {
		return fmt.Errorf("发布目标 %s 已存在；要修改请先 share-target remove，或在后台修改", id)
	}
	if len(sources) == 0 {
		fmt.Fprintln(stdout, "提示：没有 --allow，这个目标暂时不能发布任何内容。")
	}
	svc := newShareService(home)
	public := "仅自己可见"
	if f.bools["public"] {
		public = "公开链接"
	}
	switch adapter, manifest := f.one("adapter"), f.one("manifest"); {
	case adapter != "" && manifest == "" && len(f.command) == 0:
		for _, name := range []string{"secret", "pass-env", "timeout", "max-size"} {
			if len(f.values[name]) > 0 {
				return usageError("内置适配器的目标不支持 --" + name)
			}
		}
		if err := confirmPublic(f); err != nil {
			return err
		}
		_, account, err := svc.SaveTarget(share.TargetInput{ID: id, Label: f.one("label"), Adapter: adapter, Account: f.one("account"),
			Config: cfg, Public: f.bools["public"], Sources: sources, Naming: f.one("naming")}, f.bools["no-check"])
		if err != nil {
			return err
		}
		if account != "" {
			fmt.Fprintf(stdout, "检查通过：%s\n", account)
		}
	case adapter == "" && manifest != "" && len(f.command) > 0:
		t := &share.Target{ID: id, Label: f.one("label"), Config: cfg, Public: f.bools["public"], Sources: sources,
			Naming: f.one("naming"), PassEnv: f.values["pass-env"], Timeout: f.one("timeout"), MaxSize: f.one("max-size")}
		if t.Label == "" {
			return usageError("缺少 --label")
		}
		if t.Naming != "" && t.Naming != "title" && t.Naming != "file" {
			return usageError("--naming 只能是 title 或 file")
		}
		if t.Timeout != "" {
			if d, err := time.ParseDuration(t.Timeout); err != nil || d <= 0 {
				return usageError("--timeout 格式无效，例如 5m")
			}
		}
		if t.MaxSize != "" {
			if _, err := share.ParseSize(t.MaxSize); err != nil {
				return usageError(err.Error())
			}
		}
		if t.Secrets, err = pairs(f.values["secret"], "--secret"); err != nil {
			return err
		}
		if t.Manifest, err = filepath.Abs(manifest); err != nil {
			return err
		}
		command := append([]string{}, f.command...)
		path, err := exec.LookPath(command[0])
		if err != nil {
			return fmt.Errorf("找不到适配器命令：%s", command[0])
		}
		if command[0], err = filepath.Abs(path); err != nil {
			return err
		}
		t.Command = command
		resolved, err := share.Resolve(st, t)
		if err != nil {
			return err
		}
		if problems := share.Problems(resolved); len(problems) > 0 {
			return fmt.Errorf("不能登记：%s", strings.Join(problems, "；"))
		}
		if err := confirmPublic(f); err != nil {
			return err
		}
		if !f.bools["no-check"] {
			account, err := svc.Check(t)
			if err != nil {
				return fmt.Errorf("检查没通过，没有登记：%v（确认无误可加 --no-check 跳过）", err)
			}
			fmt.Fprintf(stdout, "检查通过：%s\n", account)
		}
		err = share.UpdateStore(home, func(st *share.Store) error {
			if st.Target(id) != nil {
				return fmt.Errorf("发布目标 %s 已存在", id)
			}
			st.Targets = append(st.Targets, t)
			return nil
		})
		if err != nil {
			return err
		}
	default:
		return usageError("用 --adapter 指定内置适配器，或用 --manifest 加 -- <命令> 指定外部适配器")
	}
	fmt.Fprintf(stdout, "已登记发布目标 %s（%s，%s，允许：%s）\n", id, f.one("label"), public, strings.Join(sources, ","))
	return nil
}

func shareTargetList(svc *share.Service, asJSON bool) error {
	data, err := svc.Admin()
	if err != nil {
		return err
	}
	if asJSON {
		out, _ := json.MarshalIndent(data.Targets, "", "  ")
		fmt.Fprintln(stdout, string(out))
		return nil
	}
	if len(data.Targets) == 0 {
		fmt.Fprintln(stdout, "还没有发布目标（在后台「外部发布」里添加，或 vinx-docs share-target --help）")
	}
	for _, v := range data.Targets {
		public := "仅自己"
		if v.Public {
			public = "公开"
		}
		keys := make([]string, 0, len(v.Config))
		for k := range v.Config {
			keys = append(keys, k+"="+v.Config[k])
		}
		sort.Strings(keys)
		adapter := v.AdapterName
		if v.External {
			adapter += "（外部）"
		}
		line := fmt.Sprintf("%s  %s  %s  %s  %s  允许：%s  配置：%s", v.ID, v.Label, adapter, orDash(v.AccountLabel), public,
			strings.Join(v.Sources, ","), strings.Join(keys, " "))
		if len(v.Problems) > 0 {
			line += "  不可用：" + strings.Join(v.Problems, "；")
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shareAccountMain(home string, args []string) error {
	if len(args) == 0 {
		return usageError("缺少子命令")
	}
	svc := newShareService(home)
	switch args[0] {
	case "list":
		data, err := svc.Admin()
		if err != nil {
			return err
		}
		if len(data.Accounts) == 0 {
			fmt.Fprintln(stdout, "还没有连接账号（vinx-docs share-account connect <适配器>，或在后台「外部发布」里连接）")
		}
		for _, a := range data.Accounts {
			fmt.Fprintf(stdout, "%s  %s  %s  %s  目标 %d 个\n", a.ID, a.Adapter, a.Label, a.Status, a.Targets)
		}
		return nil
	case "connect":
		f, err := parseFlags(args[1:], []string{"account"}, nil)
		if err != nil {
			return err
		}
		adapter := ""
		if len(f.args) == 1 {
			adapter = f.args[0]
		}
		if (adapter == "") == (f.one("account") == "") {
			return usageError("用法：share-account connect <适配器>，或 share-account connect --account <账号 id>")
		}
		start, err := svc.ConnectAccount(adapter, f.one("account"))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "请在任意设备的浏览器里打开：%s\n输入验证码：%s\n（%d 分钟内有效，完成后这里会自动继续）\n",
			start.VerificationURL, start.UserCode, int(start.ExpiresIn)/60)
		interval := time.Duration(start.Interval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		for {
			time.Sleep(interval)
			poll, err := svc.PollAccount(start.Account)
			if err != nil {
				return err
			}
			switch poll.Status {
			case "done":
				fmt.Fprintf(stdout, "已连接：%s（账号 id %s）\n", poll.Account.Label, poll.Account.ID)
				return nil
			case "failed":
				return fmt.Errorf("%s：%s", poll.Error.Text, poll.Error.Message)
			}
		}
	case "remove":
		if len(args) != 2 {
			return usageError("用法：share-account remove <账号 id>")
		}
		if err := svc.RemoveAccount(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "已断开账号 %s，本机令牌已删除\n", args[1])
		return nil
	}
	return usageError("不认识的子命令 " + args[0])
}

func shareProviderMain(home string, args []string) error {
	if len(args) < 2 || args[0] != "set" {
		return usageError("用法：share-provider set <内置适配器> [--config KEY=VALUE ...] [--secret KEY=VALUE ...]")
	}
	f, err := parseFlags(args[2:], []string{"config", "secret"}, nil)
	if err != nil {
		return err
	}
	cfg, err := pairs(f.values["config"], "--config")
	if err != nil {
		return err
	}
	secrets, err := pairs(f.values["secret"], "--secret")
	if err != nil {
		return err
	}
	if err := newShareService(home).SetProvider(args[1], cfg, secrets); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已保存 %s 的服务商设置\n", args[1])
	return nil
}

func shareAdapterMain(home string, args []string) error {
	if len(args) == 0 {
		return usageError("缺少子命令")
	}
	switch args[0] {
	case "list":
		data, err := newShareService(home).Admin()
		if err != nil {
			return err
		}
		for _, a := range data.Adapters {
			ready := "服务商设置已填写"
			if !a.Ready {
				ready = "服务商设置未填写（后台「外部发布」或 share-provider set）"
			}
			fmt.Fprintf(stdout, "%s  %s  %s\n", a.ID, a.Name, ready)
		}
		return nil
	case "conformance":
		if len(args) != 2 {
			return usageError("用法：share-adapter conformance <适配器目录>")
		}
		return share.Conformance(args[1], stdout)
	}
	return usageError("不认识的子命令 " + args[0])
}

func orNone(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, "、")
}

// ---------------------------------------------------------------- 经本机服务发布、撤回

func serviceBase(home string) string {
	_, port := config.ServerAddress(home)
	if listen := os.Getenv(server.ListenOverride); listen != "" {
		if _, p, err := net.SplitHostPort(listen); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func callService(home, method, path string, body any) (int, map[string]any, error) {
	base := serviceBase(home)
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("连不上本机服务（%s）：发布和撤回由服务执行，请先 vinx-docs start", base)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		message, _ := out["error"].(string)
		return resp.StatusCode, out, fmt.Errorf("%s", message)
	}
	return resp.StatusCode, out, nil
}

func waitJob(home string, job map[string]any) (map[string]any, error) {
	id, _ := job["id"].(string)
	start := time.Now()
	for {
		status, _ := job["status"].(string)
		if status != "running" {
			return job, nil
		}
		time.Sleep(500 * time.Millisecond)
		if time.Since(start) > 10*time.Second && int(time.Since(start).Seconds())%10 == 0 {
			fmt.Fprintf(stdout, "发送中…已用 %.0f 秒\n", time.Since(start).Seconds())
		}
		_, out, err := callService(home, "GET", "/api/shares/job?id="+id, nil)
		if err != nil {
			return nil, err
		}
		job = out
	}
}

func reportJob(job map[string]any) error {
	record, _ := job["record"].(map[string]any)
	str := func(m map[string]any, key string) string { s, _ := m[key].(string); return s }
	switch job["status"] {
	case "done":
		if job["kind"] == "revoke" {
			fmt.Fprintf(stdout, "已撤回：%s\n", str(record, "name"))
			return nil
		}
		public := "仅自己可见"
		if record["public"] == true {
			public = "公开链接"
		}
		fmt.Fprintf(stdout, "已发布：%s → %s（%s）\n链接：%s\n记录：%s\n", str(record, "name"), str(record, "target"), public, str(record, "url"), str(record, "id"))
		return nil
	default:
		info, _ := job["error"].(map[string]any)
		text := str(info, "text")
		if msg := str(info, "message"); msg != "" {
			text += "：" + msg
		}
		if job["status"] == "unknown" {
			return fmt.Errorf("结果未知（%s）；可以重试 vinx-docs share，或撤回", text)
		}
		return fmt.Errorf("失败：%s", text)
	}
}

func shareRun(home string, args []string) error {
	f, err := parseFlags(args, []string{"to"}, nil)
	if err != nil {
		return err
	}
	if len(f.args) != 1 || f.one("to") == "" {
		return usageError("用法：share <短码> --to <目标>")
	}
	_, out, err := callService(home, "POST", "/api/shares", map[string]string{"page": f.args[0], "target": f.one("to")})
	if err != nil {
		return err
	}
	job, _ := out["job"].(map[string]any)
	if job, err = waitJob(home, job); err != nil {
		return err
	}
	return reportJob(job)
}

func unshareRun(home string, args []string) error {
	if len(args) != 1 {
		return usageError("用法：unshare <记录ID>")
	}
	_, out, err := callService(home, "POST", "/api/shares/revoke", map[string]string{"share": args[0]})
	if err != nil {
		return err
	}
	job, _ := out["job"].(map[string]any)
	if job, err = waitJob(home, job); err != nil {
		return err
	}
	return reportJob(job)
}

func sharesRun(home string, args []string) error {
	if len(args) > 0 && args[0] == "forget" {
		if len(args) != 2 {
			return usageError("用法：shares forget <记录ID>")
		}
		if _, _, err := callService(home, "POST", "/api/shares/forget", map[string]string{"share": args[1]}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "已删除本机记录 %s（远端未改动）\n", args[1])
		return nil
	}
	f, err := parseFlags(args, []string{"page"}, []string{"json"})
	if err != nil {
		return err
	}
	records, err := share.LoadRecords(home)
	if err != nil {
		return err
	}
	var shown []*share.Record
	for _, r := range records {
		if page := f.one("page"); page == "" || r.Source.ID == page {
			shown = append(shown, r)
		}
	}
	if f.bools["json"] {
		if shown == nil {
			shown = []*share.Record{}
		}
		data, _ := json.MarshalIndent(shown, "", "  ")
		fmt.Fprintln(stdout, string(data))
		return nil
	}
	if len(shown) == 0 {
		fmt.Fprintln(stdout, "没有发布记录")
	}
	statusText := map[string]string{share.StatusActive: "已发布", share.StatusPending: "进行中", share.StatusUnknown: "结果未知", share.StatusRevoked: "已撤回"}
	// 已发布的记录和页面当前内容比对，改过的标「源文件已修改」（与页面上一致）。
	registry, _ := pages.LoadRegistry(pages.DefaultRegistry(home))
	digests := map[string]string{}
	for _, r := range shown {
		status := statusText[r.Status]
		if r.Status == share.StatusActive && r.Digest != "" && registry != nil {
			digest, ok := digests[r.Source.ID]
			if !ok {
				if record := pages.Find(registry, r.Source.ID); record != nil {
					digest = pages.CurrentDigest(record)
				}
				digests[r.Source.ID] = digest
			}
			if digest != "" && digest != r.Digest {
				status += " · 源文件已修改"
			}
		}
		line := fmt.Sprintf("%s  %s → %s  %s  %s  %s", r.ID, r.Source.ID, r.Target, status, r.Name, r.URL)
		if r.LastError != nil {
			line += "  （" + r.LastError.Text + "）"
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}
