package share

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// 协议 v1 的上限与等待时间。
const (
	maxLine       = 1 << 20          // stdout 单行上限
	exitGrace     = 10 * time.Second // 终止事件之后等待进程退出
	termGrace     = 5 * time.Second  // SIGTERM 之后等待，再结束整个进程组
	messageLimit  = 300              // 界面上显示的 message 长度
	stderrTailCap = 64 << 10
)

// Params 是 publish 的参数。
type Params struct {
	File     string    `json:"file"`
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Source   Source    `json:"source"`
	Previous *Previous `json:"previous,omitempty"`
}

// Previous 是上次发布的结果，更新和撤回时回传给适配器。
type Previous struct {
	RemoteID string `json:"remoteId"`
	URL      string `json:"url,omitempty"`
}

// Source 是发布来源；第一版只有短链接页面（kind=page）。
type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type requestTarget struct {
	ID     string            `json:"id"`
	Public bool              `json:"public"`
	Config map[string]string `json:"config"`
}

// Request 是写到适配器 stdin 的一行请求。
type Request struct {
	Protocol int               `json:"protocol"`
	Method   string            `json:"method"`
	Key      string            `json:"key"`
	Target   requestTarget     `json:"target"`
	Provider map[string]string `json:"provider"`
	Secrets  map[string]string `json:"secrets"`
	StateDir string            `json:"stateDir"`
	Params   any               `json:"params,omitempty"`
}

// 调用结果。
const (
	OK      = "ok"      // 有 result，退出码 0
	Failed  = "failed"  // 有 error 事件：远端没有变化
	Unknown = "unknown" // 其他情况：远端可能已经生效
)

// Outcome 是一次调用的结果。
type Outcome struct {
	Status  string
	Data    map[string]any
	Code    string
	Message string
}

// ErrorCodes 是协议定义的错误码及界面上的说明；不认识的按 internal。
var ErrorCodes = map[string]string{
	"auth_required":     "需要重新授权",
	"permission_denied": "没有权限",
	"quota_exceeded":    "空间或配额不足",
	"not_found":         "远端文件不存在",
	"conflict":          "远端有冲突",
	"rate_limited":      "请求过于频繁，请稍后重试",
	"network":           "网络或服务暂时不可用",
	"invalid_config":    "目标配置有误",
	"unsupported":       "适配器不支持这个操作",
	"internal":          "适配器内部错误",
	"auth_denied":       "授权被拒绝",
	"auth_expired":      "验证码已过期，请重新连接",
	"timeout":           "适配器超时，结果未知",
	"crashed":           "适配器异常退出，结果未知",
}

// Call 是一次适配器调用的全部输入。
type Call struct {
	Argv    []string
	Dir     string   // 工作目录（临时目录）
	Env     []string // 完整环境，不继承主程序
	Request Request
	Timeout time.Duration
	Log     io.Writer // 适配器的 stderr、非协议输出写到这里（本机日志）
}

// Run 按协议 v1 调用一次适配器。
func Run(ctx context.Context, c Call) Outcome {
	logf := func(format string, args ...any) {
		if c.Log != nil {
			fmt.Fprintf(c.Log, "[share %s %s] "+format+"\n", append([]any{c.Request.Target.ID, c.Request.Method}, args...)...)
		}
	}
	payload, err := json.Marshal(c.Request)
	if err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: err.Error()}
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir, cmd.Env = c.Dir, c.Env
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	setGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: err.Error()}
	}
	stderr := &lineLogger{log: logf}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return Outcome{Status: Failed, Code: "internal", Message: "无法启动适配器：" + err.Error()}
	}

	var mu sync.Mutex
	var terminal *Outcome
	terminated := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, tooLong, err := readLine(reader)
			if tooLong {
				logf("stdout 行超过 1 MiB，已忽略")
			} else if len(line) > 0 {
				if ev := parseEvent(line, logf); ev != nil {
					mu.Lock()
					if terminal == nil {
						terminal = ev
						close(terminated)
					} else {
						logf("多余的终止事件已忽略")
					}
					mu.Unlock()
				}
			}
			if err != nil {
				return
			}
		}
	}()

	waitDone := make(chan error, 1)
	go func() {
		<-readDone
		waitDone <- cmd.Wait()
	}()

	timer := time.NewTimer(c.Timeout)
	defer timer.Stop()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-waitDone:
	case <-terminated:
		select {
		case waitErr = <-waitDone:
		case <-time.After(exitGrace):
			logf("终止事件后 %s 仍未退出，结束进程", exitGrace)
			stop(cmd, termGrace, waitDone)
			waitErr = fmt.Errorf("killed")
		}
	case <-timer.C:
		timedOut = true
		logf("超时（%s），结束进程", c.Timeout)
		waitErr = stop(cmd, termGrace, waitDone)
	case <-ctx.Done():
		timedOut = true
		logf("服务停止，结束进程")
		waitErr = stop(cmd, termGrace, waitDone)
	}
	stderr.flush()

	mu.Lock()
	defer mu.Unlock()
	switch {
	case terminal != nil && terminal.Status == Failed:
		return *terminal
	case terminal != nil && waitErr == nil && !timedOut:
		return *terminal
	case timedOut:
		return Outcome{Status: Unknown, Code: "timeout", Message: ErrorCodes["timeout"]}
	default:
		return Outcome{Status: Unknown, Code: "crashed", Message: ErrorCodes["crashed"]}
	}
}

// stop 发 SIGTERM，等 grace 后结束整个进程组，返回 Wait 的结果。
func stop(cmd *exec.Cmd, grace time.Duration, waitDone chan error) error {
	terminate(cmd)
	select {
	case err := <-waitDone:
		killGroup(cmd)
		return err
	case <-time.After(grace):
		killGroup(cmd)
		return <-waitDone
	}
}

// readLine 读一行（不含换行）；超过上限时丢弃剩余部分并返回 tooLong。
func readLine(r *bufio.Reader) ([]byte, bool, error) {
	var line []byte
	tooLong := false
	for {
		chunk, isPrefix, err := r.ReadLine()
		if !tooLong {
			line = append(line, chunk...)
			if len(line) > maxLine {
				tooLong, line = true, nil
			}
		}
		if err != nil || !isPrefix {
			return line, tooLong, err
		}
	}
}

// parseEvent 解析一行事件；返回终止事件（result 或 error），其他事件写日志后返回 nil。
func parseEvent(line []byte, logf func(string, ...any)) *Outcome {
	var ev struct {
		Event   string          `json:"event"`
		Data    json.RawMessage `json:"data"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Level   string          `json:"level"`
		Bytes   int64           `json:"bytes"`
		Total   int64           `json:"total"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Event == "" {
		logf("stdout: %s", truncate(string(line), 500))
		return nil
	}
	switch ev.Event {
	case "log":
		logf("%s: %s", orStr(ev.Level, "info"), ev.Message)
	case "progress":
		logf("progress %d/%d", ev.Bytes, ev.Total)
	case "result":
		data := map[string]any{}
		if len(ev.Data) > 0 && json.Unmarshal(ev.Data, &data) != nil {
			logf("result 的 data 不是对象，已忽略：%s", truncate(string(line), 500))
			return nil
		}
		return &Outcome{Status: OK, Data: data}
	case "error":
		code := ev.Code
		if _, ok := ErrorCodes[code]; !ok || code == "timeout" || code == "crashed" {
			code = "internal"
		}
		return &Outcome{Status: Failed, Code: code, Message: ev.Message}
	default:
		logf("不认识的事件 %q，已忽略", ev.Event)
	}
	return nil
}

func orStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// lineLogger 把适配器的 stderr 按行写进本机日志。
type lineLogger struct {
	mu  sync.Mutex
	buf []byte
	log func(string, ...any)
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.log("stderr: %s", truncate(string(l.buf[:i]), 2000))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > stderrTailCap {
		l.log("stderr: %s", truncate(string(l.buf), 2000))
		l.buf = nil
	}
	return len(p), nil
}

func (l *lineLogger) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.log("stderr: %s", truncate(string(l.buf), 2000))
		l.buf = nil
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Redact 把文本里出现的密钥值替换成 ***，并截到界面显示的长度。
func Redact(message string, secrets map[string]string) string {
	values := make([]string, 0, len(secrets))
	for _, v := range secrets {
		if v != "" {
			values = append(values, v)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		message = strings.ReplaceAll(message, v, "***")
	}
	return truncate(message, messageLimit)
}

// envName 把配置项名字转成环境变量后缀：大写，非字母数字换成下划线。
func envName(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return unicode.ToUpper(r)
		}
		return '_'
	}, name)
}

// BuildEnv 生成适配器的环境：最小集合 + passEnv（目标的，以及内置适配器清单里的）+ 便捷变量（不含密钥）。
func BuildEnv(t *Target, req Request, workDir string, extraPass ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + workDir}
	lang := os.Getenv("LANG")
	if lang == "" {
		lang = "C.UTF-8"
	}
	env = append(env, "LANG="+lang)
	for _, name := range append(append([]string{}, t.PassEnv...), extraPass...) {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	public := "0"
	if req.Target.Public {
		public = "1"
	}
	env = append(env,
		fmt.Sprintf("VINX_SHARE_PROTOCOL=%d", req.Protocol),
		"VINX_SHARE_METHOD="+req.Method,
		"VINX_SHARE_KEY="+req.Key,
		"VINX_SHARE_TARGET="+req.Target.ID,
		"VINX_SHARE_PUBLIC="+public,
		"VINX_SHARE_STATE_DIR="+req.StateDir,
	)
	file, name, title, remote := "", "", "", ""
	if p, ok := req.Params.(*Params); ok && p != nil {
		file, name, title = p.File, p.Name, p.Title
		if p.Previous != nil {
			remote = p.Previous.RemoteID
		}
	}
	if rp, ok := req.Params.(*RevokeParams); ok && rp != nil {
		remote = rp.Previous.RemoteID
	}
	env = append(env, "VINX_SHARE_FILE="+file, "VINX_SHARE_NAME="+name, "VINX_SHARE_TITLE="+title, "VINX_SHARE_REMOTE_ID="+remote)
	keys := make([]string, 0, len(req.Target.Config))
	for k := range req.Target.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, "VINX_SHARE_CONFIG_"+envName(k)+"="+req.Target.Config[k])
	}
	return env
}

// RevokeParams 是 revoke 的参数。
type RevokeParams struct {
	Source   Source   `json:"source"`
	Previous Previous `json:"previous"`
}
