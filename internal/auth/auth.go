// Package auth 是外部访问（经反向代理进来的请求）的密码登录：密码摘要、会话和失败限流。
//
// 只有一个密码，没有用户名。密码摘要和外部访问设置存在 config/auth.json，会话存在 config/sessions.json，
// 两个文件都是 0600、原子写入，只由服务进程读写。内网直连的请求不经过这里。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vinx-lab/vinx-docs/internal/config"
)

const (
	CookieName = "vinx_session"
	// SessionDays 是登录后会话的有效期（固定，不随访问续期）。
	SessionDays = 30
	// DefaultLinkHours 是短链接发布后外部免登录只读的时长。
	DefaultLinkHours = 24
	MaxLinkHours     = 24 * 30
	iterations       = 600000
	// 同一 IP 连续失败 maxFailures 次锁定 lockFor；全部来源一分钟内失败超过 globalLimit 次时暂停一分钟。
	maxFailures = 5
	lockFor     = 15 * time.Minute
	globalLimit = 30
)

var (
	ErrNoPassword = errors.New("尚未设置访问密码，请在内网打开管理后台设置")
	ErrWrong      = errors.New("密码不正确")
	ErrLocked     = errors.New("失败次数过多，请稍后再试")
)

type digest struct {
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Hash       string `json:"hash"`
}

type settingsFile struct {
	Password      *digest  `json:"password,omitempty"`
	ExternalHosts []string `json:"externalHosts"`
	LinkHours     *int     `json:"linkHours,omitempty"`
}

type session struct {
	Hash      string `json:"hash"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

type sessionsFile struct {
	Sessions []session `json:"sessions"`
}

type failure struct {
	count       int
	lockedUntil time.Time
}

// Settings 是外部访问设置（不含密码）。
type Settings struct {
	ExternalHosts []string // 外部访问用的域名；Host 命中时按外部请求处理
	LinkHours     int      // 短链接发布后免登录的小时数；0 表示一律要登录
}

// Store 保存密码、会话和失败计数。内存里的内容是文件的副本，只有本进程写文件。
type Store struct {
	settingsPath string
	sessionsPath string

	mu       sync.Mutex
	file     settingsFile
	sessions []session
	failures map[string]*failure
	recent   []time.Time // 全部来源最近一分钟的失败时间

	Now func() time.Time
}

// Open 读取家目录下的认证文件；文件不存在时用默认值。文件损坏时返回错误，不当作「没有密码」。
func Open(home string) (*Store, error) {
	dir := filepath.Dir(config.ConfigPath(home))
	s := &Store{
		settingsPath: filepath.Join(dir, "auth.json"),
		sessionsPath: filepath.Join(dir, "sessions.json"),
		failures:     map[string]*failure{},
		Now:          time.Now,
	}
	if err := readJSON(s.settingsPath, &s.file); err != nil {
		return nil, err
	}
	var stored sessionsFile
	if err := readJSON(s.sessionsPath, &stored); err != nil {
		return nil, err
	}
	s.sessions = stored.Sessions
	return s, nil
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, into); err != nil {
		return config.Errorf("%s 格式无效", filepath.Base(path))
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(path, append(data, '\n'))
}

// Settings 返回外部访问设置。
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	hours := DefaultLinkHours
	if s.file.LinkHours != nil {
		hours = *s.file.LinkHours
	}
	return Settings{ExternalHosts: append([]string{}, s.file.ExternalHosts...), LinkHours: hours}
}

// SaveSettings 校验并保存外部访问设置。域名只保留主机名部分（小写、去掉端口）。
func (s *Store) SaveSettings(next Settings) (Settings, error) {
	if next.LinkHours < 0 || next.LinkHours > MaxLinkHours {
		return Settings{}, config.Errorf("短链接免登录时长必须是0到%d小时", MaxLinkHours)
	}
	if len(next.ExternalHosts) > 20 {
		return Settings{}, config.Errorf("外部域名最多20个")
	}
	hosts := []string{}
	seen := map[string]bool{}
	for _, raw := range next.ExternalHosts {
		host := HostName(raw)
		if host == "" {
			continue
		}
		if len(host) > 253 || strings.ContainsAny(host, " /\\@?#") {
			return Settings{}, config.Errorf("外部域名格式无效：%s", raw)
		}
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := s.file
	updated.ExternalHosts = hosts
	hours := next.LinkHours
	updated.LinkHours = &hours
	if err := writeJSON(s.settingsPath, updated); err != nil {
		return Settings{}, err
	}
	s.file = updated
	return Settings{ExternalHosts: append([]string{}, hosts...), LinkHours: hours}, nil
}

// HostName 取 Host 值里的主机名：小写，去掉端口和 IPv6 的方括号。
func HostName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end > 0 {
			return host[1:end]
		}
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[:i], ":") {
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

// IsExternalHost 报告 Host 是否是登记的外部域名。
func (s *Store) IsExternalHost(host string) bool {
	name := HostName(host)
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.file.ExternalHosts {
		if item == name {
			return true
		}
	}
	return false
}

// PasswordSet 报告是否已设置密码。
func (s *Store) PasswordSet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Password != nil
}

// SetPassword 设置新密码并注销全部会话。
func (s *Store) SetPassword(password string) error {
	if n := utf8.RuneCountInString(password); n < 8 || n > 200 {
		return config.Errorf("密码长度必须是8到200个字符")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := s.file
	updated.Password = &digest{
		Salt: base64.StdEncoding.EncodeToString(salt), Iterations: iterations,
		Hash: base64.StdEncoding.EncodeToString(hash),
	}
	if err := writeJSON(s.settingsPath, updated); err != nil {
		return err
	}
	s.file = updated
	return s.saveSessions(nil)
}

// Login 校验密码，成功时返回新会话的令牌。ip 用于失败限流。
func (s *Store) Login(password, ip string) (string, error) {
	s.mu.Lock()
	stored := s.file.Password
	if s.blockedLocked(ip) {
		s.mu.Unlock()
		return "", ErrLocked
	}
	s.mu.Unlock()
	if stored == nil {
		return "", ErrNoPassword
	}
	salt, err1 := base64.StdEncoding.DecodeString(stored.Salt)
	want, err2 := base64.StdEncoding.DecodeString(stored.Hash)
	if err1 != nil || err2 != nil || stored.Iterations < 1 {
		return "", config.Errorf("auth.json 格式无效")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, stored.Iterations, len(want))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if subtle.ConstantTimeCompare(got, want) != 1 {
		s.failLocked(ip)
		return "", ErrWrong
	}
	delete(s.failures, ip)
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	text := hex.EncodeToString(token)
	now := s.Now()
	next := append(s.liveLocked(now), session{
		Hash: tokenHash(text), CreatedAt: now.Unix(), ExpiresAt: now.Add(SessionDays * 24 * time.Hour).Unix(),
	})
	if err := s.saveSessions(next); err != nil {
		return "", err
	}
	return text, nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// liveLocked 返回未过期的会话（调用方持有锁）。
func (s *Store) liveLocked(now time.Time) []session {
	live := []session{}
	for _, item := range s.sessions {
		if item.ExpiresAt > now.Unix() {
			live = append(live, item)
		}
	}
	return live
}

func (s *Store) saveSessions(list []session) error {
	if list == nil {
		list = []session{}
	}
	if err := writeJSON(s.sessionsPath, sessionsFile{Sessions: list}); err != nil {
		return err
	}
	s.sessions = list
	return nil
}

// Valid 报告令牌是否对应一个有效会话。
func (s *Store) Valid(token string) bool {
	if len(token) != 32 {
		return false
	}
	hash := tokenHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().Unix()
	for _, item := range s.sessions {
		if item.ExpiresAt > now && subtle.ConstantTimeCompare([]byte(item.Hash), []byte(hash)) == 1 {
			return true
		}
	}
	return false
}

// Logout 注销一个会话。
func (s *Store) Logout(token string) error {
	hash := tokenHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	next := []session{}
	for _, item := range s.liveLocked(s.Now()) {
		if item.Hash != hash {
			next = append(next, item)
		}
	}
	return s.saveSessions(next)
}

// LogoutAll 注销全部会话。
func (s *Store) LogoutAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveSessions(nil)
}

// SessionCount 返回有效会话数。
func (s *Store) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.liveLocked(s.Now()))
}

// Fail 记一次失败（如外部未登录访问不存在的短码），用于限制暴力猜测。
func (s *Store) Fail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLocked(ip)
}

// Blocked 报告该来源当前是否被限流。
func (s *Store) Blocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockedLocked(ip)
}

func (s *Store) failLocked(ip string) {
	now := s.Now()
	s.recent = append(s.pruneLocked(now), now)
	entry := s.failures[ip]
	if entry == nil {
		if len(s.failures) > 10000 {
			s.failures = map[string]*failure{} // 防止大量来源撑满内存；只是限流状态，丢掉可以接受
		}
		entry = &failure{}
		s.failures[ip] = entry
	}
	entry.count++
	if entry.count >= maxFailures {
		entry.count = 0
		entry.lockedUntil = now.Add(lockFor)
	}
}

func (s *Store) pruneLocked(now time.Time) []time.Time {
	kept := s.recent[:0]
	for _, at := range s.recent {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	return kept
}

func (s *Store) blockedLocked(ip string) bool {
	now := s.Now()
	s.recent = s.pruneLocked(now)
	if len(s.recent) > globalLimit {
		return true
	}
	entry := s.failures[ip]
	return entry != nil && now.Before(entry.lockedUntil)
}
