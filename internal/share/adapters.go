package share

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
)

// Builtin 是编进主程序的官方适配器：清单和入口。入口按协议 v1 读 stdin、写 stdout，返回退出码。
// 主程序以 vinx-docs share-adapter run <id> 启动自己来运行它，和外部适配器走同一套进程协议。
type Builtin struct {
	Manifest *Manifest
	Run      func(stdin io.Reader, stdout, stderr io.Writer) int
}

var (
	builtinMu sync.Mutex
	builtins  = map[string]*Builtin{}
)

// RegisterBuiltin 登记一个内置适配器（各适配器包的 init 调用）。清单不合法时 panic。
func RegisterBuiltin(manifest []byte, run func(io.Reader, io.Writer, io.Writer) int) {
	m, err := ParseManifest(manifest)
	if err != nil {
		panic(err)
	}
	if !m.Builtin {
		panic("内置适配器的 adapter.json 必须写 builtin: true：" + m.ID)
	}
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtins[m.ID] = &Builtin{m, run}
}

// LookupBuiltin 按 id 找内置适配器。
func LookupBuiltin(id string) *Builtin {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	return builtins[id]
}

// BuiltinManifests 列出内置适配器的清单，按 id 排序。
func BuiltinManifests() []*Manifest {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	out := make([]*Manifest, 0, len(builtins))
	for _, b := range builtins {
		out = append(out, b.Manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SelfCommand 返回启动内置适配器的命令：当前程序 + share-adapter run <id>。测试里会换成测试程序自己。
var SelfCommand = func(id string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return []string{exe, "share-adapter", "run", id}
}

// Resolved 是可以执行的目标：清单、命令、账号和服务商设置。
type Resolved struct {
	Target   *Target
	Manifest *Manifest
	Argv     []string
	Account  *Account  // 需要授权的适配器才有
	Provider *Settings // 服务商设置
}

// StateKey 是状态目录的名字：有账号时按账号，否则按目标。
func (r *Resolved) StateKey() string {
	if r.Account != nil {
		return r.Account.ID
	}
	return r.Target.ID
}

// Resolve 找到目标对应的适配器、账号和服务商设置。
func Resolve(st *Store, t *Target) (*Resolved, error) {
	r := &Resolved{Target: t}
	if t.Adapter != "" {
		b := LookupBuiltin(t.Adapter)
		if b == nil {
			return nil, fmt.Errorf("没有内置适配器 %s", t.Adapter)
		}
		r.Manifest, r.Argv = b.Manifest, SelfCommand(b.Manifest.ID)
	} else {
		if len(t.Command) == 0 || t.Manifest == "" {
			return nil, fmt.Errorf("目标 %s 既没有 adapter，也没有 command 和 manifest", t.ID)
		}
		data, err := os.ReadFile(t.Manifest)
		if err != nil {
			return nil, fmt.Errorf("读不到适配器清单 %s", t.Manifest)
		}
		if r.Manifest, err = ParseManifest(data); err != nil {
			return nil, err
		}
		if r.Manifest.Builtin {
			return nil, fmt.Errorf("外部适配器的清单不能写 builtin")
		}
		r.Argv = append([]string{}, t.Command...)
	}
	r.Provider = st.Provider(r.Manifest.ID)
	if t.Account != "" {
		if r.Account = st.Account(t.Account); r.Account == nil {
			return nil, fmt.Errorf("目标 %s 连接的账号已经断开，请重新选择账号", t.ID)
		}
	}
	return r, nil
}

// Problems 列出目标不能用的原因：缺必填项、缺服务商设置、账号未连接、缺依赖工具、能力不支持。
func Problems(r *Resolved) []string {
	var out []string
	missing := func(fields []Field, values map[string]string, what string) {
		for _, f := range fields {
			if f.Required && values[f.Name] == "" {
				out = append(out, what+" "+f.Name+"（"+f.Description+"）")
			}
		}
	}
	missing(r.Manifest.Config, r.Target.Config, "缺少配置")
	missing(r.Manifest.Secrets, r.Target.Secrets, "缺少密钥")
	missing(r.Manifest.Provider, r.Provider.Config, "服务商设置缺少")
	missing(r.Manifest.ProviderSecrets, r.Provider.Secrets, "服务商设置缺少")
	if r.Manifest.Capabilities.Auth != "" {
		switch {
		case r.Account == nil:
			out = append(out, "没有选择账号")
		case r.Account.Status == AccountAuthRequired:
			out = append(out, "账号 "+r.Account.Label+" 的授权已失效，请重新连接")
		case r.Account.Status != AccountConnected:
			out = append(out, "账号 "+r.Account.Label+" 还没有完成授权")
		}
	}
	for _, tool := range r.Manifest.Requires {
		if _, err := exec.LookPath(tool); err != nil {
			out = append(out, "找不到依赖的工具 "+tool+"，请先安装（见适配器说明）")
		}
	}
	if r.Target.Public && !r.Manifest.Capabilities.PublicLink {
		out = append(out, "适配器不支持公开链接")
	}
	if !r.Target.Public && !r.Manifest.Capabilities.PrivateLink {
		out = append(out, "适配器不支持非公开链接，需要选择公开")
	}
	if !r.Manifest.Builtin {
		if info, err := os.Stat(r.Argv[0]); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			out = append(out, "适配器命令不存在或不可执行："+r.Argv[0])
		}
	}
	sort.Strings(out)
	return out
}

// mergeSecrets 合并服务商密钥和目标密钥（目标的优先）。
func mergeSecrets(r *Resolved) map[string]string {
	out := map[string]string{}
	for k, v := range r.Provider.Secrets {
		out[k] = v
	}
	for k, v := range r.Target.Secrets {
		out[k] = v
	}
	return out
}

const testAdapterArg = "__vinx_share_adapter__"

// RunTestAdapterIfRequested 给测试程序用：在 TestMain 开头调用。测试里内置适配器由测试程序自己扮演
// （UseTestBinaryForBuiltins 把 SelfCommand 换成「测试程序 + 标记参数」），收到标记参数时运行适配器后退出。
func RunTestAdapterIfRequested() {
	if len(os.Args) >= 3 && os.Args[1] == testAdapterArg {
		b := LookupBuiltin(os.Args[2])
		if b == nil {
			os.Exit(3)
		}
		os.Exit(b.Run(os.Stdin, os.Stdout, os.Stderr))
	}
}

// UseTestBinaryForBuiltins 让内置适配器由当前测试程序运行（配合 RunTestAdapterIfRequested）。
func UseTestBinaryForBuiltins() {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	SelfCommand = func(id string) []string { return []string{exe, testAdapterArg, id} }
}
