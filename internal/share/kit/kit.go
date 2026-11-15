// Package kit 是写适配器用的小工具（适配器一侧的协议 v1，见 spec 0008 第 2 节）：
// 读 stdin 的一行请求，往 stdout 写 NDJSON 事件。只用标准库。
package kit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Protocol 是本包实现的协议版本。
const Protocol = 1

// Request 是 stdin 上的一行请求。Params 按方法再解析。
type Request struct {
	Protocol int               `json:"protocol"`
	Method   string            `json:"method"`
	Key      string            `json:"key"`
	Target   Target            `json:"target"`
	Provider map[string]string `json:"provider"`
	Secrets  map[string]string `json:"secrets"`
	StateDir string            `json:"stateDir"`
	Params   json.RawMessage   `json:"params"`
}

// Target 是请求里的目标。
type Target struct {
	ID     string            `json:"id"`
	Public bool              `json:"public"`
	Config map[string]string `json:"config"`
}

// Previous 是上次发布的结果。
type Previous struct {
	RemoteID string `json:"remoteId"`
	URL      string `json:"url"`
}

// PublishParams 是 publish 的参数。
type PublishParams struct {
	File     string    `json:"file"`
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Previous *Previous `json:"previous"`
}

// RevokeParams 是 revoke 的参数。
type RevokeParams struct {
	Previous Previous `json:"previous"`
}

// Error 是要以 error 事件返回的错误。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf 生成一个带错误码的错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{code, fmt.Sprintf(format, args...)}
}

// Out 往 stdout 写事件。
type Out struct{ w io.Writer }

func (o *Out) emit(v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(o.w, "%s\n", data)
}

// Log 写一条日志事件。
func (o *Out) Log(format string, args ...any) {
	o.emit(map[string]any{"event": "log", "level": "info", "message": fmt.Sprintf(format, args...)})
}

// Progress 写一条进度事件。
func (o *Out) Progress(bytes, total int64) {
	o.emit(map[string]any{"event": "progress", "bytes": bytes, "total": total})
}

// Handler 处理一个方法，返回 result 的 data；返回 *Error 时以 error 事件结束。
type Handler func(req *Request, out *Out) (map[string]any, error)

// Serve 读一行请求、调用对应的处理函数、写终止事件。返回退出码（写了终止事件就是 0）。
func Serve(stdin io.Reader, stdout io.Writer, handlers map[string]Handler) int {
	out := &Out{stdout}
	fail := func(code, message string) int {
		out.emit(map[string]any{"event": "error", "code": code, "message": message})
		return 0
	}
	line, err := bufio.NewReaderSize(stdin, 1<<20).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return fail("internal", "读不到请求："+err.Error())
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return fail("internal", "请求不是 JSON："+err.Error())
	}
	if req.Protocol != Protocol {
		return fail("unsupported", fmt.Sprintf("不支持的协议版本：%d", req.Protocol))
	}
	handler := handlers[req.Method]
	if handler == nil {
		return fail("unsupported", "不支持的方法："+req.Method)
	}
	data, err := handler(&req, out)
	if err != nil {
		if e, ok := err.(*Error); ok {
			return fail(e.Code, e.Message)
		}
		return fail("internal", err.Error())
	}
	if data == nil {
		data = map[string]any{}
	}
	out.emit(map[string]any{"event": "result", "data": data})
	return 0
}

// ReadState 读状态目录里的 JSON 文件；不存在时返回 os.ErrNotExist。
func ReadState(dir, name string, v any) error {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteState 原子写入状态目录里的 JSON 文件，权限 0600。
func WriteState(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// RemoveState 删除状态目录里的文件（不存在不算错）。
func RemoveState(dir, name string) {
	_ = os.Remove(filepath.Join(dir, name))
}
