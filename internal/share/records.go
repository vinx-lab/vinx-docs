package share

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// 记录状态。
const (
	StatusPending = "pending" // 已经开始调用适配器
	StatusActive  = "active"  // 已发布
	StatusUnknown = "unknown" // 结果未知：超时、异常退出、服务中途重启
	StatusRevoked = "revoked" // 已撤回，记录保留
)

// ErrorInfo 是记录或任务上的错误。
type ErrorInfo struct {
	Code    string `json:"code"`
	Text    string `json:"text"`    // 错误码对应的中文说明
	Message string `json:"message"` // 适配器给的说明（已去掉密钥、截短）
	At      string `json:"at,omitempty"`
}

// Record 是一条发布记录（config/shares.json）。
type Record struct {
	ID             string     `json:"id"`
	Target         string     `json:"target"`
	Source         Source     `json:"source"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	Operation      string     `json:"operation,omitempty"` // pending 时：publish 或 revoke
	URL            string     `json:"url,omitempty"`
	RemoteID       string     `json:"remoteId,omitempty"`
	Public         bool       `json:"public"`
	PublicMismatch bool       `json:"publicMismatch,omitempty"`
	Digest         string     `json:"digest,omitempty"`
	LastError      *ErrorInfo `json:"lastError"`
	CreatedAt      string     `json:"createdAt"`
	UpdatedAt      string     `json:"updatedAt"`
}

type recordsFile struct {
	Shares []*Record `json:"shares"`
}

// RecordsPath 是发布记录文件。
func RecordsPath(home string) string { return filepath.Join(home, "config", "shares.json") }

// LoadRecords 读取发布记录；文件不存在时返回空列表。
func LoadRecords(home string) ([]*Record, error) {
	data, err := os.ReadFile(RecordsPath(home))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file recordsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("shares.json 格式错误：%v", err)
	}
	return file.Shares, nil
}

// SaveRecords 原子写入发布记录。
func SaveRecords(home string, records []*Record) error {
	if records == nil {
		records = []*Record{}
	}
	data, err := json.MarshalIndent(recordsFile{records}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(RecordsPath(home), append(data, '\n'), 0o600)
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Live 报告记录是否占用远端位置（用于重名检查和删除目标前的检查）。
func (r *Record) Live() bool { return r.Status != StatusRevoked }
