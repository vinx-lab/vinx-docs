package share

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/pages"
)

// UserError 是可以直接显示给用户的错误（接口返回 400/403/404/409）。
type UserError struct {
	Status  int
	Message string
}

func (e *UserError) Error() string { return e.Message }

func userErr(status int, format string, args ...any) error {
	return &UserError{status, fmt.Sprintf(format, args...)}
}

// Job 是一次发布或撤回任务（只在内存里）。
type Job struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"` // publish / revoke
	Share   string     `json:"share"`
	Status  string     `json:"status"` // running / done / failed / unknown
	Error   *ErrorInfo `json:"error,omitempty"`
	Record  *Record    `json:"record,omitempty"`
	Started string     `json:"started"`
	Elapsed float64    `json:"elapsed"`

	started time.Time
	done    chan struct{}
}

// Service 管理发布记录和任务。同一个页面同一个目标同时只有一个任务；全局最多同时 2 个；
// 同一个状态目录（账号）的任务串行执行。
type Service struct {
	Home     string
	Registry string
	Log      io.Writer

	mu       sync.Mutex
	jobs     map[string]*Job
	busy     map[string]string // 页面|目标 → 任务 ID
	sem      chan struct{}
	accounts map[string]*sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewService 创建服务。服务进程启动时还要调用 Recover。
func NewService(home, registry string, log io.Writer) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{Home: home, Registry: registry, Log: log, jobs: map[string]*Job{}, busy: map[string]string{},
		sem: make(chan struct{}, 2), accounts: map[string]*sync.Mutex{}, ctx: ctx, cancel: cancel}
	return s
}

// Close 结束进行中的适配器（服务停止时）。
func (s *Service) Close() { s.cancel() }

// Recover 把上次服务停止时没结束的 pending 记录改成 unknown（只在服务进程启动时调用）。
func (s *Service) Recover() {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := LoadRecords(s.Home)
	if err != nil {
		return
	}
	changed := false
	for _, r := range records {
		if r.Status == StatusPending {
			r.Status, r.Operation, r.UpdatedAt = StatusUnknown, "", now()
			r.LastError = &ErrorInfo{Code: "crashed", Text: "服务在任务中途重启，结果未知", At: now()}
			changed = true
		}
	}
	if changed {
		_ = SaveRecords(s.Home, records)
	}
}

// TargetView 是给页面的目标信息，不含命令、配置和密钥。
type TargetView struct {
	ID            string       `json:"id"`
	Label         string       `json:"label"`
	Adapter       string       `json:"adapter"`
	Public        bool         `json:"public"`
	Capabilities  Capabilities `json:"capabilities"`
	Name          string       `json:"name"`                    // 发布用的文件名（已发布过时是记录里的名字）
	Account       string       `json:"account,omitempty"`       // 账号显示名，如邮箱
	AccountStatus string       `json:"accountStatus,omitempty"` // connected / auth_required
}

// SourceInfo 是要发布的内容概况：文件数、总大小、是否打包成 zip、没通过检查的文件。
type SourceInfo struct {
	Files    int      `json:"files"`
	Size     int64    `json:"size"`
	Bundled  bool     `json:"bundled"`
	Rejected []string `json:"rejected"`
}

// Source 返回页面要发布的内容概况。
func (s *Service) Source(pageID string) (SourceInfo, error) {
	page, err := s.page(pageID)
	if err != nil {
		return SourceInfo{}, err
	}
	items, rejected := pages.CheckSources(page)
	info := SourceInfo{Files: len(pages.Files(page)), Bundled: pages.Bundled(page), Rejected: rejected}
	if info.Rejected == nil {
		info.Rejected = []string{}
	}
	for _, it := range items {
		if st, err := os.Stat(it.Path); err == nil {
			info.Size += st.Size()
		}
	}
	return info, nil
}

// RecordView 是给页面的记录，附带「源文件已修改」。
type RecordView struct {
	*Record
	Modified bool   `json:"modified"`
	Running  string `json:"job,omitempty"`
}

func (s *Service) page(id string) (*ojson.Object, error) {
	registry, err := pages.LoadRegistry(s.Registry)
	if err != nil {
		return nil, err
	}
	record := pages.Find(registry, id)
	if record == nil {
		return nil, userErr(404, "页面不存在或已取消发布")
	}
	return record, nil
}

// Targets 返回这个页面能用的目标（允许 pages 来源、适配器可用）。
func (s *Service) Targets(pageID string) ([]TargetView, error) {
	page, err := s.page(pageID)
	if err != nil {
		return nil, err
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	records, _ := s.AllShares()
	byTitle, byFile := pages.DownloadNames(page)
	out := []TargetView{}
	for _, t := range st.Targets {
		if !t.Allows("pages") {
			continue
		}
		r, err := Resolve(st, t)
		if err != nil {
			continue
		}
		problems := Problems(r)
		if len(problems) > 0 && (r.Account == nil || r.Account.Status != AccountAuthRequired) {
			continue // 授权失效的仍列出（发布框里提示重新连接），其他不可用的不列出
		}
		name := byTitle
		if t.Naming == "file" {
			name = byFile
		}
		if rec := findPageRecord(records, pageID, t.ID); rec != nil && rec.RemoteID != "" && rec.Status != StatusRevoked {
			name = rec.Name
		}
		view := TargetView{ID: t.ID, Label: t.Label, Adapter: r.Manifest.Name, Public: t.Public, Capabilities: r.Manifest.Capabilities, Name: name}
		if r.Account != nil {
			view.Account, view.AccountStatus = r.Account.Label, r.Account.Status
		}
		out = append(out, view)
	}
	return out, nil
}

// Shares 返回这个页面的发布记录。
func (s *Service) Shares(pageID string) ([]RecordView, error) {
	record, err := s.page(pageID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	records, err := LoadRecords(s.Home)
	busy := map[string]string{}
	for k, v := range s.busy {
		busy[k] = v
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	digest := ""
	out := []RecordView{}
	for _, r := range records {
		if r.Source.Kind != "page" || r.Source.ID != pageID {
			continue
		}
		view := RecordView{Record: r, Running: busy[pageID+"|"+r.Target]}
		if r.Status == StatusActive && r.Digest != "" {
			if digest == "" {
				digest = pages.CurrentDigest(record)
			}
			view.Modified = digest != r.Digest
		}
		out = append(out, view)
	}
	return out, nil
}

// AllShares 返回全部记录（命令行列表用）。
func (s *Service) AllShares() ([]*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return LoadRecords(s.Home)
}

// JobByID 返回任务的快照。
func (s *Service) JobByID(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[id]
	if job == nil {
		return nil
	}
	copy := *job
	copy.Elapsed = time.Since(job.started).Seconds()
	return &copy
}

// Wait 等任务结束（命令行用）。
func (s *Service) Wait(id string) *Job {
	s.mu.Lock()
	job := s.jobs[id]
	s.mu.Unlock()
	if job == nil {
		return nil
	}
	<-job.done
	return s.JobByID(id)
}

// update 在锁内读改写记录文件。
func (s *Service) update(fn func(records []*Record) ([]*Record, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := LoadRecords(s.Home)
	if err != nil {
		return err
	}
	records, err = fn(records)
	if err != nil {
		return err
	}
	return SaveRecords(s.Home, records)
}

func findRecord(records []*Record, id string) *Record {
	for _, r := range records {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func findPageRecord(records []*Record, pageID, target string) *Record {
	for _, r := range records {
		if r.Source.Kind == "page" && r.Source.ID == pageID && r.Target == target {
			return r
		}
	}
	return nil
}

// uniqueName 在同一个目标的有效记录里避开重名：重名时在扩展名前加 (<短码>)。
func uniqueName(records []*Record, target, pageID, name string) string {
	for _, r := range records {
		if r.Target == target && r.Live() && r.Source.ID != pageID && r.Name == name {
			ext := ""
			if dot := strings.LastIndex(name, "."); dot > 0 {
				ext = name[dot:]
			}
			return strings.TrimSuffix(name, ext) + " (" + pageID + ")" + ext
		}
	}
	return name
}

// store 读取服务商设置、账号和目标。
func (s *Service) store() (*Store, error) { return LoadStore(s.Home) }

// StateDir 是账号（或没有账号的目标）的状态目录：share-state/<适配器>/<账号 id 或目标 id>/，权限 0700。
func StateDir(home, adapter, key string) string {
	return filepath.Join(home, "share-state", adapter, key)
}

func (s *Service) stateDir(r *Resolved) (string, error) {
	dir := StateDir(s.Home, r.Manifest.ID, r.StateKey())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

func (s *Service) accountLock(dir string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts[dir] == nil {
		s.accounts[dir] = &sync.Mutex{}
	}
	return s.accounts[dir]
}

func (s *Service) resolveTarget(id string) (*Resolved, error) {
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	t := st.Target(id)
	if t == nil {
		return nil, userErr(404, "发布目标 %s 不存在", id)
	}
	r, err := Resolve(st, t)
	if err != nil {
		return nil, userErr(409, "%v", err)
	}
	return r, nil
}

func (s *Service) workDir(job string) (string, error) {
	dir := filepath.Join(s.Home, ".runtime", "share-work", job)
	return dir, os.MkdirAll(dir, 0o700)
}

// Publish 校验后启动发布任务（首次发布或更新）。
func (s *Service) Publish(pageID, targetID string) (*Job, error) {
	page, err := s.page(pageID)
	if err != nil {
		return nil, err
	}
	r, err := s.resolveTarget(targetID)
	if err != nil {
		return nil, err
	}
	if !r.Target.Allows("pages") {
		return nil, userErr(403, "目标 %s 没有开启短链接页面的发布（登记时加 --allow pages）", targetID)
	}
	if problems := Problems(r); len(problems) > 0 {
		return nil, userErr(409, "目标 %s 不可用：%s", targetID, strings.Join(problems, "；"))
	}
	items, rejected := pages.CheckSources(page)
	if len(rejected) > 0 {
		return nil, userErr(409, "有文件没通过发布检查，已拒绝发布：%s", strings.Join(rejected, "、"))
	}
	byTitle, byFile := pages.DownloadNames(page)
	name := byTitle
	if r.Target.Naming == "file" {
		name = byFile
	}

	job := &Job{ID: newID("j_"), Kind: "publish", Status: "running", started: time.Now(), done: make(chan struct{})}
	job.Started = job.started.UTC().Format(time.RFC3339)
	key := pageID + "|" + targetID
	var rec *Record
	var prior Record
	isNew := false
	err = func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if running := s.busy[key]; running != "" {
			return userErr(409, "这个页面正在发布到 %s，请等它结束", targetID)
		}
		records, err := LoadRecords(s.Home)
		if err != nil {
			return err
		}
		rec = findPageRecord(records, pageID, targetID)
		if rec != nil && rec.Status == StatusActive && !r.Manifest.Capabilities.Update {
			return userErr(409, "适配器不支持更新，请先撤回再重新发布")
		}
		if rec == nil {
			isNew = true
			rec = &Record{ID: newID("s_"), Target: targetID, Source: Source{"page", pageID}, CreatedAt: now()}
			records = append(records, rec)
		}
		prior = *rec
		if rec.Status != StatusActive && rec.Status != StatusUnknown {
			rec.RemoteID, rec.URL = "", "" // 撤回后重新发布：不带 previous
		}
		if rec.RemoteID == "" || isNew {
			rec.Name = uniqueName(records, targetID, pageID, name)
		}
		rec.Status, rec.Operation, rec.UpdatedAt = StatusPending, "publish", now()
		if err := SaveRecords(s.Home, records); err != nil {
			return err
		}
		s.busy[key] = job.ID
		job.Share = rec.ID
		s.jobs[job.ID] = job
		return nil
	}()
	if err != nil {
		return nil, err
	}
	snapshot := *rec
	go s.runPublish(job, r, page, items, &snapshot, prior, isNew, key)
	return s.JobByID(job.ID), nil
}

func (s *Service) runPublish(job *Job, r *Resolved, page *ojson.Object, items []pages.SourceFile, rec *Record, prior Record, isNew bool, key string) {
	var outcome Outcome
	var digest string
	func() {
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		work, err := s.workDir(job.ID)
		if err != nil {
			outcome = Outcome{Status: Failed, Code: "internal", Message: err.Error()}
			return
		}
		defer os.RemoveAll(work)
		path := filepath.Join(work, rec.Name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			outcome = Outcome{Status: Failed, Code: "internal", Message: err.Error()}
			return
		}
		hash := sha256.New()
		sums, err := pages.WriteSource(page, items, rec.Name, io.MultiWriter(file, hash))
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			outcome = Outcome{Status: Failed, Code: "internal", Message: "准备文件失败：" + err.Error()}
			return
		}
		if len(sums) != len(items) {
			outcome = Outcome{Status: Failed, Code: "internal", Message: "准备文件时有源文件被删除，请重试"}
			return
		}
		info, _ := os.Stat(path)
		if info.Size() > r.Target.MaxBytes() {
			outcome = Outcome{Status: Failed, Code: "invalid_config", Message: fmt.Sprintf("文件大小 %d 字节，超过目标的上限 %s", info.Size(), orStr(r.Target.MaxSize, "200MB"))}
			return
		}
		digest = pages.SourceDigest(sums)
		params := &Params{File: path, Name: rec.Name, Title: pages.Str(page, "title"), Size: info.Size(),
			SHA256: hex.EncodeToString(hash.Sum(nil)), Source: rec.Source}
		if rec.RemoteID != "" {
			params.Previous = &Previous{RemoteID: rec.RemoteID, URL: rec.URL}
		}
		outcome = s.call(r, "publish", rec.ID, params, work)
	}()

	_ = s.update(func(records []*Record) ([]*Record, error) {
		cur := findRecord(records, rec.ID)
		if cur == nil {
			return records, nil
		}
		cur.Operation, cur.UpdatedAt = "", now()
		switch outcome.Status {
		case OK:
			url, _ := outcome.Data["url"].(string)
			remote, _ := outcome.Data["remoteId"].(string)
			public, _ := outcome.Data["public"].(bool)
			if url == "" || remote == "" {
				cur.Status = StatusUnknown
				cur.LastError = s.errorInfo(r, Outcome{Code: "internal", Message: "适配器的结果缺少 url 或 remoteId"})
				break
			}
			cur.Status, cur.URL, cur.RemoteID, cur.Public, cur.Digest = StatusActive, url, remote, public, digest
			cur.PublicMismatch = public != r.Target.Public
			cur.LastError = nil
			if cur.PublicMismatch {
				cur.LastError = &ErrorInfo{Code: "public_mismatch", Text: "公开状态与登记不符，请立即撤回",
					Message: fmt.Sprintf("登记为%s，适配器返回%s", publicText(r.Target.Public), publicText(public)), At: now()}
			}
		case Failed:
			if isNew {
				return removeRecord(records, cur.ID), nil
			}
			*cur = prior
			cur.LastError = s.errorInfo(r, outcome)
		default:
			cur.Status = StatusUnknown
			cur.LastError = s.errorInfo(r, outcome)
		}
		return records, nil
	})
	s.finish(job, key, outcome)
}

func publicText(public bool) string {
	if public {
		return "公开链接"
	}
	return "仅自己可见"
}

func removeRecord(records []*Record, id string) []*Record {
	out := records[:0]
	for _, r := range records {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

func (s *Service) errorInfo(r *Resolved, o Outcome) *ErrorInfo {
	text := ErrorCodes[o.Code]
	if text == "" {
		text = ErrorCodes["internal"]
	}
	if o.Code == "auth_required" && r.Account != nil {
		s.markAccount(r.Account.ID, AccountAuthRequired)
	}
	return &ErrorInfo{Code: o.Code, Text: text, Message: Redact(o.Message, mergeSecrets(r)), At: now()}
}

func (s *Service) finish(job *Job, key string, outcome Outcome) {
	records, _ := LoadRecords(s.Home)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, key)
	if rec := findRecord(records, job.Share); rec != nil {
		copy := *rec
		job.Record = &copy
		if rec.PublicMismatch && outcome.Status == OK {
			job.Status, job.Error = "failed", rec.LastError
			close(job.done)
			return
		}
	}
	switch outcome.Status {
	case OK:
		job.Status = "done"
	case Failed:
		job.Status = "failed"
		if job.Record != nil && job.Record.LastError != nil {
			job.Error = job.Record.LastError
		} else {
			text := ErrorCodes[outcome.Code]
			job.Error = &ErrorInfo{Code: outcome.Code, Text: text, Message: truncate(outcome.Message, messageLimit), At: now()}
		}
	default:
		job.Status = "unknown"
		if job.Record != nil {
			job.Error = job.Record.LastError
		}
	}
	close(job.done)
}

// call 调用一次适配器（同一个账号串行）。
func (s *Service) call(r *Resolved, method, key string, params any, work string) Outcome {
	state, err := s.stateDir(r)
	if err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: err.Error()}
	}
	lock := s.accountLock(state)
	lock.Lock()
	defer lock.Unlock()
	return s.invoke(r, method, key, params, work, state)
}

// invoke 组装请求并执行适配器（调用方负责账号锁）。
func (s *Service) invoke(r *Resolved, method, key string, params any, work, state string) Outcome {
	config := r.Target.Config
	if config == nil {
		config = map[string]string{}
	}
	provider := r.Provider.Config
	if provider == nil {
		provider = map[string]string{}
	}
	req := Request{Protocol: Protocol, Method: method, Key: key, Provider: provider, Secrets: mergeSecrets(r), StateDir: state, Params: params,
		Target: requestTarget{ID: r.Target.ID, Public: r.Target.Public, Config: config}}
	var extra []string
	if r.Manifest.Builtin {
		extra = r.Manifest.PassEnv
	}
	return Run(s.ctx, Call{Argv: r.Argv, Dir: work, Env: BuildEnv(r.Target, req, work, extra...), Request: req,
		Timeout: r.Target.TimeoutDuration(), Log: s.Log})
}

// Revoke 启动撤回任务。
func (s *Service) Revoke(shareID string) (*Job, error) {
	records, err := s.AllShares()
	if err != nil {
		return nil, err
	}
	rec := findRecord(records, shareID)
	if rec == nil {
		return nil, userErr(404, "发布记录不存在")
	}
	if rec.RemoteID == "" || (rec.Status != StatusActive && rec.Status != StatusUnknown) {
		return nil, userErr(409, "这条记录没有可以撤回的远端文件")
	}
	r, err := s.resolveTarget(rec.Target)
	if err != nil {
		return nil, err
	}
	if !r.Manifest.Capabilities.Revoke {
		return nil, userErr(409, "适配器不支持撤回，请到服务商那边手动删除")
	}
	job := &Job{ID: newID("j_"), Kind: "revoke", Share: shareID, Status: "running", started: time.Now(), done: make(chan struct{})}
	job.Started = job.started.UTC().Format(time.RFC3339)
	key := rec.Source.ID + "|" + rec.Target
	var prior Record
	err = s.update(func(records []*Record) ([]*Record, error) {
		if s.busy[key] != "" {
			return nil, userErr(409, "这条记录正在处理，请等它结束")
		}
		cur := findRecord(records, shareID)
		prior = *cur
		cur.Status, cur.Operation, cur.UpdatedAt = StatusPending, "revoke", now()
		s.busy[key] = job.ID
		s.jobs[job.ID] = job
		return records, nil
	})
	if err != nil {
		return nil, err
	}
	go func() {
		var outcome Outcome
		func() {
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
			work, err := s.workDir(job.ID)
			if err != nil {
				outcome = Outcome{Status: Failed, Code: "internal", Message: err.Error()}
				return
			}
			defer os.RemoveAll(work)
			outcome = s.call(r, "revoke", shareID, &RevokeParams{Source: prior.Source, Previous: Previous{prior.RemoteID, prior.URL}}, work)
		}()
		_ = s.update(func(records []*Record) ([]*Record, error) {
			cur := findRecord(records, shareID)
			if cur == nil {
				return records, nil
			}
			cur.Operation, cur.UpdatedAt = "", now()
			switch outcome.Status {
			case OK:
				cur.Status, cur.LastError, cur.PublicMismatch = StatusRevoked, nil, false
			case Failed:
				*cur = prior
				cur.LastError = s.errorInfo(r, outcome)
			default:
				cur.Status = StatusUnknown
				cur.LastError = s.errorInfo(r, outcome)
			}
			return records, nil
		})
		s.finish(job, key, outcome)
	}()
	return s.JobByID(job.ID), nil
}

// Forget 删除一条 unknown 或 revoked 记录，只删本机记录，不碰远端。
func (s *Service) Forget(shareID string) error {
	return s.update(func(records []*Record) ([]*Record, error) {
		rec := findRecord(records, shareID)
		if rec == nil {
			return nil, userErr(404, "发布记录不存在")
		}
		if rec.Status != StatusUnknown && rec.Status != StatusRevoked {
			return nil, userErr(409, "只能删除「结果未知」或「已撤回」的记录；已发布的请先撤回")
		}
		if s.busy[rec.Source.ID+"|"+rec.Target] != "" {
			return nil, userErr(409, "这条记录正在处理")
		}
		return removeRecord(records, shareID), nil
	})
}

// Check 调用目标的 check（登记和 share-target test 用），返回账号说明。
func (s *Service) Check(t *Target) (string, error) {
	st, err := s.store()
	if err != nil {
		return "", err
	}
	r, err := Resolve(st, t)
	if err != nil {
		return "", err
	}
	if problems := Problems(r); len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "；"))
	}
	work, err := s.workDir(newID("c_"))
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	o := s.call(r, "check", "check", nil, work)
	switch o.Status {
	case OK:
		account, _ := o.Data["account"].(string)
		return account, nil
	default:
		info := s.errorInfo(r, o)
		return "", fmt.Errorf("%s：%s", info.Text, info.Message)
	}
}

// LiveRecords 返回某个目标仍占用远端位置的记录数（删除目标前检查）。
func LiveRecords(home, target string) (int, error) {
	records, err := LoadRecords(home)
	n := 0
	for _, r := range records {
		if r.Target == target && r.Live() {
			n++
		}
	}
	return n, err
}
