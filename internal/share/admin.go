package share

import (
	"os"
	"strings"
	"sync"
)

// 后台管理：服务商设置、账号授权、目标（spec 0008 第 4 节）。页面只能管理内置适配器的目标，
// 外部适配器（命令）只能在命令行登记。

var storeMu sync.Mutex // 同一进程里串行读改写 share-targets.json

// UpdateStore 在锁内读改写配置文件。
func UpdateStore(home string, fn func(st *Store) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	st, err := LoadStore(home)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return SaveStore(home, st)
}

func (s *Service) markAccount(id, status string) {
	_ = UpdateStore(s.Home, func(st *Store) error {
		if a := st.Account(id); a != nil {
			a.Status = status
		}
		return nil
	})
}

// FieldView 是一个配置字段及其当前值（密钥只给是否已设置）。
type FieldView struct {
	Field
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
	Set    bool   `json:"set"`
}

// AdapterView 是后台里的一个服务商。
type AdapterView struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Docs         string       `json:"docs,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Provider     []FieldView  `json:"provider"`
	Config       []Field      `json:"config"`
	Ready        bool         `json:"ready"` // 服务商设置已填全
}

// AccountView 是后台里的一个账号。
type AccountView struct {
	*Account
	Targets int `json:"targets"`
}

// TargetAdminView 是后台里的一个目标（不含密钥值）。
type TargetAdminView struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Adapter      string            `json:"adapter"`
	AdapterName  string            `json:"adapterName"`
	Account      string            `json:"account,omitempty"`
	AccountLabel string            `json:"accountLabel,omitempty"`
	Config       map[string]string `json:"config"`
	Public       bool              `json:"public"`
	Sources      []string          `json:"sources"`
	Naming       string            `json:"naming"`
	External     bool              `json:"external"` // 外部适配器：只能在命令行修改
	Problems     []string          `json:"problems"`
	Live         int               `json:"live"` // 有效的发布记录数
}

// AdminData 是后台「外部发布」的全部数据。
type AdminData struct {
	Adapters []AdapterView     `json:"adapters"`
	Accounts []AccountView     `json:"accounts"`
	Targets  []TargetAdminView `json:"targets"`
}

func providerReady(m *Manifest, p *Settings) bool {
	for _, f := range m.Provider {
		if f.Required && p.Config[f.Name] == "" {
			return false
		}
	}
	for _, f := range m.ProviderSecrets {
		if f.Required && p.Secrets[f.Name] == "" {
			return false
		}
	}
	return true
}

// Admin 返回后台数据。
func (s *Service) Admin() (*AdminData, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	records, _ := s.AllShares()
	out := &AdminData{Adapters: []AdapterView{}, Accounts: []AccountView{}, Targets: []TargetAdminView{}}
	for _, m := range BuiltinManifests() {
		p := st.Provider(m.ID)
		view := AdapterView{ID: m.ID, Name: m.Name, Docs: m.Docs, Capabilities: m.Capabilities, Provider: []FieldView{},
			Config: append([]Field{}, m.Config...), Ready: providerReady(m, p)}
		for _, f := range m.Provider {
			view.Provider = append(view.Provider, FieldView{Field: f, Value: p.Config[f.Name], Set: p.Config[f.Name] != ""})
		}
		for _, f := range m.ProviderSecrets {
			view.Provider = append(view.Provider, FieldView{Field: f, Secret: true, Set: p.Secrets[f.Name] != ""})
		}
		out.Adapters = append(out.Adapters, view)
	}
	for _, a := range st.Accounts {
		n := 0
		for _, t := range st.Targets {
			if t.Account == a.ID {
				n++
			}
		}
		out.Accounts = append(out.Accounts, AccountView{a, n})
	}
	for _, t := range st.Targets {
		view := TargetAdminView{ID: t.ID, Label: t.Label, Adapter: t.Adapter, Account: t.Account, Config: t.Config,
			Public: t.Public, Sources: t.Sources, Naming: t.Naming, External: t.Adapter == "", Problems: []string{}}
		if view.Config == nil {
			view.Config = map[string]string{}
		}
		if view.Sources == nil {
			view.Sources = []string{}
		}
		if view.Naming == "" {
			view.Naming = "title"
		}
		if r, err := Resolve(st, t); err != nil {
			view.Problems = []string{err.Error()}
		} else {
			view.AdapterName = r.Manifest.Name
			view.Problems = append(view.Problems, Problems(r)...)
			if r.Account != nil {
				view.AccountLabel = r.Account.Label
			}
		}
		for _, rec := range records {
			if rec.Target == t.ID && rec.Live() {
				view.Live++
			}
		}
		out.Targets = append(out.Targets, view)
	}
	return out, nil
}

// SetProvider 保存内置适配器的服务商设置。密钥留空表示不修改。
func (s *Service) SetProvider(adapter string, config, secrets map[string]string) error {
	b := LookupBuiltin(adapter)
	if b == nil {
		return userErr(404, "没有内置适配器 %s", adapter)
	}
	known := map[string]bool{}
	for _, f := range b.Manifest.Provider {
		known[f.Name] = true
	}
	knownSecret := map[string]bool{}
	for _, f := range b.Manifest.ProviderSecrets {
		knownSecret[f.Name] = true
	}
	return UpdateStore(s.Home, func(st *Store) error {
		p := st.Providers[adapter]
		if p == nil {
			p = &Settings{Config: map[string]string{}, Secrets: map[string]string{}}
			st.Providers[adapter] = p
		}
		if p.Config == nil {
			p.Config = map[string]string{}
		}
		if p.Secrets == nil {
			p.Secrets = map[string]string{}
		}
		for k, v := range config {
			if !known[k] {
				return userErr(400, "服务商设置没有字段 %s", k)
			}
			p.Config[k] = strings.TrimSpace(v)
		}
		for k, v := range secrets {
			if !knownSecret[k] {
				return userErr(400, "服务商设置没有密钥字段 %s", k)
			}
			if v = strings.TrimSpace(v); v != "" {
				p.Secrets[k] = v
			}
		}
		return nil
	})
}

// AuthStart 是开始授权的结果：给用户看的网址和验证码。
type AuthStart struct {
	Account         string  `json:"account"`
	VerificationURL string  `json:"verificationUrl"`
	UserCode        string  `json:"userCode"`
	ExpiresIn       float64 `json:"expiresIn"`
	Interval        float64 `json:"interval"`
}

// AuthPoll 是一次轮询的结果。
type AuthPoll struct {
	Status  string     `json:"status"` // pending / done / failed
	Account *Account   `json:"account,omitempty"`
	Error   *ErrorInfo `json:"error,omitempty"`
}

// accountResolved 为授权调用组装一个只带账号的目标。
func (s *Service) accountResolved(st *Store, adapter string, account *Account) (*Resolved, error) {
	b := LookupBuiltin(adapter)
	if b == nil {
		return nil, userErr(404, "没有内置适配器 %s", adapter)
	}
	if b.Manifest.Capabilities.Auth != "device" {
		return nil, userErr(409, "%s 不需要在这里授权", b.Manifest.Name)
	}
	p := st.Provider(adapter)
	if !providerReady(b.Manifest, p) {
		return nil, userErr(409, "请先填写 %s 的服务商设置", b.Manifest.Name)
	}
	t := &Target{ID: account.ID, Adapter: adapter, Account: account.ID}
	return &Resolved{Target: t, Manifest: b.Manifest, Argv: SelfCommand(adapter), Account: account, Provider: p}, nil
}

func (s *Service) authCall(r *Resolved, method string) Outcome {
	state, err := s.stateDir(r)
	if err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: err.Error()}
	}
	work, err := s.workDir(newID("a_"))
	if err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: err.Error()}
	}
	defer os.RemoveAll(work)
	lock := s.accountLock(state)
	lock.Lock()
	defer lock.Unlock()
	return s.invoke(r, method, r.Account.ID, nil, work, state)
}

// ConnectAccount 开始连接一个新账号（accountID 为空）或重新连接已有账号。
func (s *Service) ConnectAccount(adapter, accountID string) (*AuthStart, error) {
	var account *Account
	var resolved *Resolved
	err := UpdateStore(s.Home, func(st *Store) error {
		if accountID != "" {
			if account = st.Account(accountID); account == nil {
				return userErr(404, "账号不存在")
			}
			adapter = account.Adapter
		} else {
			account = &Account{ID: newID("acct_"), Adapter: adapter, Label: "连接中…", Status: AccountConnecting}
		}
		var err error
		if resolved, err = s.accountResolved(st, adapter, account); err != nil {
			return err
		}
		if accountID == "" {
			st.Accounts = append(st.Accounts, account)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	o := s.authCall(resolved, "auth.begin")
	if o.Status != OK {
		if accountID == "" {
			_ = s.removeAccountRecord(account.ID)
		}
		info := s.errorInfo(resolved, o)
		return nil, userErr(502, "%s：%s", info.Text, info.Message)
	}
	str := func(k string) string { v, _ := o.Data[k].(string); return v }
	num := func(k string) float64 { v, _ := o.Data[k].(float64); return v }
	return &AuthStart{Account: account.ID, VerificationURL: str("verificationUrl"), UserCode: str("userCode"),
		ExpiresIn: num("expiresIn"), Interval: num("interval")}, nil
}

// PollAccount 轮询一次授权结果。
func (s *Service) PollAccount(accountID string) (*AuthPoll, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	account := st.Account(accountID)
	if account == nil {
		return nil, userErr(404, "账号不存在")
	}
	resolved, err := s.accountResolved(st, account.Adapter, account)
	if err != nil {
		return nil, err
	}
	o := s.authCall(resolved, "auth.poll")
	switch {
	case o.Status == OK && o.Data["status"] == "done":
		label, _ := o.Data["account"].(string)
		var updated Account
		err := UpdateStore(s.Home, func(st *Store) error {
			a := st.Account(accountID)
			if a == nil {
				return userErr(404, "账号不存在")
			}
			if label != "" {
				a.Label = label
			}
			a.Status, a.ConnectedAt = AccountConnected, now()
			updated = *a
			return nil
		})
		if err != nil {
			return nil, err
		}
		return &AuthPoll{Status: "done", Account: &updated}, nil
	case o.Status == OK:
		return &AuthPoll{Status: "pending"}, nil
	default:
		info := s.errorInfo(resolved, o)
		if account.Status == AccountConnecting {
			_ = s.removeAccountRecord(accountID) // 新账号授权失败：不留半截记录
		}
		return &AuthPoll{Status: "failed", Error: info}, nil
	}
}

func (s *Service) removeAccountRecord(id string) error {
	err := UpdateStore(s.Home, func(st *Store) error {
		kept := st.Accounts[:0]
		var adapter string
		for _, a := range st.Accounts {
			if a.ID == id {
				adapter = a.Adapter
				continue
			}
			kept = append(kept, a)
		}
		st.Accounts = kept
		if adapter != "" {
			_ = os.RemoveAll(StateDir(s.Home, adapter, id))
		}
		return nil
	})
	return err
}

// RemoveAccount 断开账号：删除本机令牌。还有目标在用时拒绝。
func (s *Service) RemoveAccount(id string) error {
	st, err := s.store()
	if err != nil {
		return err
	}
	if st.Account(id) == nil {
		return userErr(404, "账号不存在")
	}
	var users []string
	for _, t := range st.Targets {
		if t.Account == id {
			users = append(users, t.Label)
		}
	}
	if len(users) > 0 {
		return userErr(409, "还有目标在用这个账号：%s；请先删除这些目标", strings.Join(users, "、"))
	}
	return s.removeAccountRecord(id)
}

// TargetInput 是页面或命令行提交的目标（只限内置适配器）。
type TargetInput struct {
	ID      string            `json:"id"`
	Label   string            `json:"label"`
	Adapter string            `json:"adapter"`
	Account string            `json:"account"`
	Config  map[string]string `json:"config"`
	Public  bool              `json:"public"`
	Sources []string          `json:"sources"`
	Naming  string            `json:"naming"`
}

// SaveTarget 新增或修改内置适配器的目标；保存前调用一次 check。返回保存后的 id 和账号说明。
func (s *Service) SaveTarget(in TargetInput, skipCheck bool) (string, string, error) {
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" {
		return "", "", userErr(400, "请填写显示名称")
	}
	b := LookupBuiltin(in.Adapter)
	if b == nil {
		return "", "", userErr(400, "没有内置适配器 %s", in.Adapter)
	}
	known := map[string]bool{}
	for _, f := range b.Manifest.Config {
		known[f.Name] = true
	}
	config := map[string]string{}
	for k, v := range in.Config {
		if !known[k] {
			return "", "", userErr(400, "%s 没有配置字段 %s", b.Manifest.Name, k)
		}
		if v = strings.TrimSpace(v); v != "" {
			config[k] = v
		}
	}
	for _, src := range in.Sources {
		if src != "pages" {
			return "", "", userErr(400, "第一版只支持发布短链接页面（pages）")
		}
	}
	if in.Naming != "" && in.Naming != "title" && in.Naming != "file" {
		return "", "", userErr(400, "命名方式只能是 title 或 file")
	}
	if in.ID != "" && !ValidTargetID(in.ID) {
		return "", "", userErr(400, "目标 id 只能用小写字母、数字、点、下划线和连字符")
	}
	st, err := s.store()
	if err != nil {
		return "", "", err
	}
	existing := st.Target(in.ID)
	if existing != nil && existing.Adapter == "" {
		return "", "", userErr(409, "外部适配器的目标只能在命令行修改")
	}
	t := &Target{ID: in.ID, Label: in.Label, Adapter: in.Adapter, Account: in.Account, Config: config, Secrets: map[string]string{},
		Public: in.Public, Sources: append([]string{}, in.Sources...), Naming: in.Naming}
	if t.Sources == nil {
		t.Sources = []string{}
	}
	if existing != nil {
		t.Secrets, t.Timeout, t.MaxSize = existing.Secrets, existing.Timeout, existing.MaxSize
		live, _ := LiveRecords(s.Home, existing.ID)
		if live > 0 && (existing.Public != t.Public || existing.Account != t.Account || existing.Adapter != t.Adapter || existing.Config["dir"] != t.Config["dir"]) {
			return "", "", userErr(409, "这个目标还有 %d 条有效的发布记录，不能修改账号、目录或公开设置；请先撤回，或新建一个目标", live)
		}
	} else if t.ID == "" {
		t.ID = newID("t_")
	}
	r, err := Resolve(st, t)
	if err != nil {
		return "", "", userErr(400, "%v", err)
	}
	if problems := Problems(r); len(problems) > 0 {
		return "", "", userErr(400, "不能保存：%s", strings.Join(problems, "；"))
	}
	account := ""
	if !skipCheck {
		if account, err = s.Check(t); err != nil {
			return "", "", userErr(409, "检查没通过，没有保存：%v", err)
		}
	}
	err = UpdateStore(s.Home, func(st *Store) error {
		for i, old := range st.Targets {
			if old.ID == t.ID {
				st.Targets[i] = t
				return nil
			}
		}
		st.Targets = append(st.Targets, t)
		return nil
	})
	return t.ID, account, err
}

// RemoveTarget 删除目标。还有有效记录时要求 force；记录保留，但不能再更新或撤回。
func (s *Service) RemoveTarget(id string, force bool) error {
	live, err := LiveRecords(s.Home, id)
	if err != nil {
		return err
	}
	if live > 0 && !force {
		return userErr(409, "这个目标还有 %d 条有效的发布记录，删除后不能再更新或撤回；建议先撤回", live)
	}
	return UpdateStore(s.Home, func(st *Store) error {
		kept := st.Targets[:0]
		found := false
		for _, t := range st.Targets {
			if t.ID == id {
				found = true
				continue
			}
			kept = append(kept, t)
		}
		if !found {
			return userErr(404, "发布目标 %s 不存在", id)
		}
		st.Targets = kept
		return nil
	})
}

// TestTarget 对已保存的目标调用 check。
func (s *Service) TestTarget(id string) (string, error) {
	st, err := s.store()
	if err != nil {
		return "", err
	}
	t := st.Target(id)
	if t == nil {
		return "", userErr(404, "发布目标 %s 不存在", id)
	}
	account, err := s.Check(t)
	if err != nil {
		return "", userErr(409, "%v", err)
	}
	return account, nil
}
