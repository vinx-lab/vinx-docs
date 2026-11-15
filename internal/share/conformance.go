package share

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// conformanceConfig 是外部适配器的 test/conformance.json：离线测试用的目标配置和额外环境变量。
// 值里的 {work} 换成本次测试的临时目录。
type conformanceConfig struct {
	Config   map[string]string `json:"config"`
	Provider map[string]string `json:"provider"`
	Secrets  map[string]string `json:"secrets"`
	Env      map[string]string `json:"env"`
}

// ConformanceSetup 是一次一致性测试的准备：适配器、命令、配置和额外环境。
// 内置适配器在自己的测试里准备好假服务端和已授权的状态目录后调用 RunConformance。
type ConformanceSetup struct {
	Manifest   *Manifest
	Argv       []string
	Config     map[string]string
	Provider   map[string]string
	Secrets    map[string]string
	Env        map[string]string // 追加到适配器环境里
	PathPrefix string            // 放在 PATH 最前面的目录（桩程序）
	StateDir   string            // 已授权的状态目录；空时用临时目录
}

// Conformance 对一个外部适配器目录跑离线一致性测试（spec 0008 第 10 节）：把 test/ 放到 PATH 最前面，
// 配置取自 test/conformance.json。每一步的结果写到 out，全部通过返回 nil。
func Conformance(dir string, out io.Writer) error {
	dir, _ = filepath.Abs(dir)
	data, err := os.ReadFile(filepath.Join(dir, "adapter.json"))
	if err != nil {
		return fmt.Errorf("读不到 %s/adapter.json", dir)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return err
	}
	if m.Builtin {
		return fmt.Errorf("内置适配器的一致性测试在它自己的 Go 测试里跑（go test ./internal/share/...）")
	}
	work, err := os.MkdirTemp("", "vinx-share-conformance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var cc conformanceConfig
	if raw, err := os.ReadFile(filepath.Join(dir, "test", "conformance.json")); err == nil {
		raw = bytes.ReplaceAll(raw, []byte("{work}"), []byte(work))
		if err := json.Unmarshal(raw, &cc); err != nil {
			return fmt.Errorf("test/conformance.json 格式错误：%v", err)
		}
	}
	argv := append([]string{}, m.Command...)
	if strings.HasPrefix(argv[0], "./") {
		argv[0] = filepath.Join(dir, argv[0][2:])
	}
	return RunConformance(ConformanceSetup{Manifest: m, Argv: argv, Config: cc.Config, Provider: cc.Provider, Secrets: cc.Secrets,
		Env: cc.Env, PathPrefix: filepath.Join(dir, "test")}, out)
}

// RunConformance 依次调用 check、publish（首次、重复、更新、公开）、revoke（两次）、授权（支持时）
// 以及不支持的协议和方法，检查事件格式和语义。
func RunConformance(cs ConformanceSetup, out io.Writer) error {
	m := cs.Manifest
	work, err := os.MkdirTemp("", "vinx-share-conformance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	state := cs.StateDir
	if state == "" {
		state = filepath.Join(work, "state")
		_ = os.MkdirAll(state, 0o700)
	}
	file := filepath.Join(work, "测试 文件.md")
	if err := os.WriteFile(file, []byte("# 一致性测试\n"), 0o600); err != nil {
		return err
	}

	failures := 0
	step := func(name string, ok bool, detail string) {
		mark := "通过"
		if !ok {
			mark, failures = "失败", failures+1
		}
		fmt.Fprintf(out, "[%s] %s", mark, name)
		if detail != "" && !ok {
			fmt.Fprintf(out, "：%s", detail)
		}
		fmt.Fprintln(out)
	}
	callIn := func(stateDir, method string, protocol int, public bool, params any) (Outcome, string) {
		var logBuf bytes.Buffer
		target := &Target{ID: "conformance", Public: public, Config: cs.Config, Secrets: cs.Secrets}
		req := Request{Protocol: protocol, Method: method, Key: "s_conformance", Provider: orEmpty(cs.Provider), Secrets: orEmpty(cs.Secrets),
			StateDir: stateDir, Params: params, Target: requestTarget{ID: target.ID, Public: public, Config: orEmpty(cs.Config)}}
		env := BuildEnv(target, req, work)
		if cs.PathPrefix != "" {
			for i, kv := range env {
				if strings.HasPrefix(kv, "PATH=") {
					env[i] = "PATH=" + cs.PathPrefix + string(os.PathListSeparator) + kv[len("PATH="):]
				}
			}
		}
		for k, v := range cs.Env {
			env = append(env, k+"="+v)
		}
		o := Run(context.Background(), Call{Argv: cs.Argv, Dir: work, Env: env, Request: req, Timeout: time.Minute, Log: &logBuf})
		return o, logBuf.String()
	}
	call := func(method string, protocol int, public bool, params any) (Outcome, string) {
		return callIn(state, method, protocol, public, params)
	}
	pure := func(log string) (bool, string) {
		for _, line := range strings.Split(log, "\n") {
			if strings.Contains(line, "] stdout: ") {
				return false, "stdout 里有不是事件的输出：" + line
			}
		}
		return true, ""
	}
	describe := func(o Outcome) string {
		data, _ := json.Marshal(o.Data)
		return fmt.Sprintf("status=%s code=%s message=%s data=%s", o.Status, o.Code, o.Message, data)
	}
	str := func(o Outcome, key string) string { s, _ := o.Data[key].(string); return s }
	params := func(prev *Previous) *Params {
		size := int64(0)
		if info, err := os.Stat(file); err == nil {
			size = info.Size()
		}
		return &Params{File: file, Name: filepath.Base(file), Title: "一致性测试", Size: size, Source: Source{"page", "test"}, Previous: prev}
	}

	o, log := call("check", Protocol, false, nil)
	step("check 返回 result", o.Status == OK, describe(o))
	ok, detail := pure(log)
	step("check 的 stdout 只有事件行", ok, detail)

	if m.Capabilities.PrivateLink {
		o, log = call("publish", Protocol, false, params(nil))
		first := o
		step("首次 publish 返回 url 和 remoteId", o.Status == OK && str(o, "url") != "" && str(o, "remoteId") != "", describe(o))
		step("首次 publish 的 public 为 false", o.Data["public"] == false, describe(o))
		ok, detail = pure(log)
		step("publish 的 stdout 只有事件行", ok, detail)

		o, _ = call("publish", Protocol, false, params(nil))
		step("同一个 key 重复 publish 不产生新文件（remoteId 不变）", o.Status == OK && str(o, "remoteId") == str(first, "remoteId"), describe(o))

		if m.Capabilities.Update {
			o, _ = call("publish", Protocol, false, params(&Previous{RemoteID: str(first, "remoteId"), URL: str(first, "url")}))
			step("带 previous 的 publish 覆盖原位置", o.Status == OK && str(o, "remoteId") == str(first, "remoteId"), describe(o))
			if m.Capabilities.StableLinkOnUpdate {
				step("更新后链接不变", str(o, "url") == str(first, "url"), describe(o))
			}
		}
		if m.Capabilities.Revoke {
			prev := Previous{RemoteID: str(first, "remoteId"), URL: str(first, "url")}
			o, _ = call("revoke", Protocol, false, &RevokeParams{Source: Source{"page", "test"}, Previous: prev})
			step("revoke 成功且 alreadyGone 为 false", o.Status == OK && o.Data["alreadyGone"] == false, describe(o))
			o, _ = call("revoke", Protocol, false, &RevokeParams{Source: Source{"page", "test"}, Previous: prev})
			step("再次 revoke 仍然成功且 alreadyGone 为 true", o.Status == OK && o.Data["alreadyGone"] == true, describe(o))
		}
	}
	if m.Capabilities.PublicLink {
		o, _ = call("publish", Protocol, true, params(nil))
		step("public 为 true 时返回公开链接", o.Status == OK && o.Data["public"] == true, describe(o))
	}
	if m.Capabilities.Auth == "device" {
		authState := filepath.Join(work, "auth-state")
		_ = os.MkdirAll(authState, 0o700)
		o, _ = callIn(authState, "auth.begin", Protocol, false, nil)
		step("auth.begin 返回网址和验证码", o.Status == OK && str(o, "verificationUrl") != "" && str(o, "userCode") != "", describe(o))
		o, _ = callIn(authState, "auth.poll", Protocol, false, nil)
		step("auth.poll 返回 pending 或 done", o.Status == OK && (o.Data["status"] == "pending" || o.Data["status"] == "done"), describe(o))
	}
	o, _ = call("check", Protocol+1, false, nil)
	step("不支持的 protocol 返回 unsupported", o.Status == Failed && o.Code == "unsupported", describe(o))
	o, _ = call("bogus", Protocol, false, nil)
	step("不认识的 method 返回 unsupported", o.Status == Failed && o.Code == "unsupported", describe(o))

	if failures > 0 {
		return fmt.Errorf("%d 项没有通过", failures)
	}
	fmt.Fprintln(out, "全部通过")
	return nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
