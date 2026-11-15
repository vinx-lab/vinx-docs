// Package share 把短链接的源文件发布到外部服务（spec 0008）：发布目标的登记、内置适配器的释放与校验、
// 适配器协议 v1（一次性进程 + NDJSON）、发布记录与任务。具体服务商由外部适配器实现。
package share

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Protocol 是本程序实现的适配器协议版本。
const Protocol = 1

// Field 是适配器清单里的一个配置或密钥字段。
type Field struct {
	Name        string `json:"name"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// Capabilities 是适配器静态声明的能力。
type Capabilities struct {
	PublicLink         bool   `json:"publicLink"`
	PrivateLink        bool   `json:"privateLink"`
	Update             bool   `json:"update"`
	StableLinkOnUpdate bool   `json:"stableLinkOnUpdate"`
	Revoke             bool   `json:"revoke"`
	Auth               string `json:"auth,omitempty"` // device：支持页面上的设备码授权
}

// Manifest 是 adapter.json。
type Manifest struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Protocol        []int        `json:"protocol"`
	Builtin         bool         `json:"builtin,omitempty"`
	Command         []string     `json:"command,omitempty"`
	Requires        []string     `json:"requires,omitempty"`
	PassEnv         []string     `json:"passEnv,omitempty"` // 只对内置适配器有效：从主程序环境传过去的变量（测试用的接口地址）
	Capabilities    Capabilities `json:"capabilities"`
	Provider        []Field      `json:"provider,omitempty"`
	ProviderSecrets []Field      `json:"providerSecrets,omitempty"`
	Config          []Field      `json:"config,omitempty"`
	Secrets         []Field      `json:"secrets,omitempty"`
	Docs            string       `json:"docs,omitempty"` // 说明文档地址（准备步骤）
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseManifest 解析并检查 adapter.json。
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("adapter.json 格式错误：%v", err)
	}
	switch {
	case !idPattern.MatchString(m.ID):
		return nil, fmt.Errorf("adapter.json 的 id 无效：%q", m.ID)
	case m.Version == "":
		return nil, fmt.Errorf("adapter.json 缺少 version")
	case !m.Builtin && len(m.Command) == 0:
		return nil, fmt.Errorf("adapter.json 缺少 command")
	}
	for _, v := range m.Protocol {
		if v == Protocol {
			return &m, nil
		}
	}
	return nil, fmt.Errorf("适配器 %s 不支持协议 v%d", m.ID, Protocol)
}

// Settings 是服务商（适配器级别）的设置，所有账号共用，例如 OAuth 客户端。
type Settings struct {
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// 账号状态。
const (
	AccountConnecting   = "connecting"
	AccountConnected    = "connected"
	AccountAuthRequired = "auth_required"
	AccountFailed       = "failed"
)

// Account 是在某个服务商上授权过的一个身份；令牌在它的状态目录里，由适配器管理。
type Account struct {
	ID          string `json:"id"`
	Adapter     string `json:"adapter"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	ConnectedAt string `json:"connectedAt,omitempty"`
}

// Target 是一个发布目标。
type Target struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Adapter  string            `json:"adapter,omitempty"`  // 内置适配器 id
	Account  string            `json:"account,omitempty"`  // 账号 id（需要授权的适配器）
	Command  []string          `json:"command,omitempty"`  // 外部适配器：绝对路径加参数（只能命令行登记）
	Manifest string            `json:"manifest,omitempty"` // 外部适配器的 adapter.json 路径
	Config   map[string]string `json:"config"`
	Secrets  map[string]string `json:"secrets"`
	Public   bool              `json:"public"`
	Sources  []string          `json:"sources"`
	Naming   string            `json:"naming,omitempty"`
	PassEnv  []string          `json:"passEnv,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	MaxSize  string            `json:"maxSize,omitempty"`
}

// Allows 报告目标是否允许发布这类来源（第一版只有 pages）。
func (t *Target) Allows(source string) bool {
	for _, s := range t.Sources {
		if s == source {
			return true
		}
	}
	return false
}

// TimeoutDuration 返回调用适配器的超时，默认 5 分钟。
func (t *Target) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(t.Timeout); err == nil && d > 0 {
		return d
	}
	return 5 * time.Minute
}

// MaxBytes 返回大小上限，默认 200 MB。
func (t *Target) MaxBytes() int64 {
	if n, err := ParseSize(t.MaxSize); err == nil && n > 0 {
		return n
	}
	return 200 << 20
}

// ParseSize 解析 200MB、1GB、512KB 或纯字节数。
func ParseSize(text string) (int64, error) {
	text = strings.ToUpper(strings.TrimSpace(text))
	mult := int64(1)
	for _, unit := range []struct {
		suffix string
		mult   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(text, unit.suffix) {
			text, mult = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix)), unit.mult
			break
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("大小格式无效：%q（例如 200MB）", text)
	}
	return n * mult, nil
}

// Store 是 config/share-targets.json：服务商设置、账号、目标。含密钥，权限 0600。
type Store struct {
	Providers map[string]*Settings `json:"providers"`
	Accounts  []*Account           `json:"accounts"`
	Targets   []*Target            `json:"targets"`
}

// TargetsPath 是发布目标的配置文件。
func TargetsPath(home string) string { return filepath.Join(home, "config", "share-targets.json") }

// LoadStore 读取配置；文件不存在时返回空配置。
func LoadStore(home string) (*Store, error) {
	st := &Store{}
	data, err := os.ReadFile(TargetsPath(home))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, st); err != nil {
			return nil, fmt.Errorf("share-targets.json 格式错误：%v", err)
		}
	}
	if st.Providers == nil {
		st.Providers = map[string]*Settings{}
	}
	return st, nil
}

// SaveStore 按 id 排序后原子写入，权限 0600。
func SaveStore(home string, st *Store) error {
	sort.Slice(st.Targets, func(i, j int) bool { return st.Targets[i].ID < st.Targets[j].ID })
	if st.Targets == nil {
		st.Targets = []*Target{}
	}
	if st.Accounts == nil {
		st.Accounts = []*Account{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(TargetsPath(home), append(data, '\n'), 0o600)
}

// Target 按 id 查找目标。
func (st *Store) Target(id string) *Target {
	for _, t := range st.Targets {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Account 按 id 查找账号。
func (st *Store) Account(id string) *Account {
	for _, a := range st.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// Provider 返回服务商设置（没有时返回空设置，不写回）。
func (st *Store) Provider(adapter string) *Settings {
	if p := st.Providers[adapter]; p != nil {
		return p
	}
	return &Settings{Config: map[string]string{}, Secrets: map[string]string{}}
}

// ValidTargetID 检查目标 id：小写字母、数字、点、下划线和连字符。
func ValidTargetID(id string) bool { return idPattern.MatchString(id) }

// writeAtomic 写临时文件后改名，避免写到一半的文件被读到。
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
