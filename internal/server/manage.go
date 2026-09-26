package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/build"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Stdout / Stderr 是命令输出的去向（测试里可以替换）。
var (
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
)

// Health 连本机服务的 /api/healthz；不是 Vinx Docs 在应答时返回 nil。
func Health(root string) *ojson.Object {
	status, body := config.LocalRequest(root, "/api/healthz", 3*time.Second)
	if status != 200 {
		return nil
	}
	value, err := ojson.Decode(body)
	if err != nil {
		return nil
	}
	info, ok := value.(*ojson.Object)
	if !ok || info.Value("service") != Service {
		return nil
	}
	return info
}

// pidOf 把 JSON 里的 pid 转成整数；不是整数时返回 (0, false)。
func pidOf(value any) (int, bool) {
	if value == nil {
		return 0, false
	}
	if b, ok := value.(bool); ok { // 布尔值不算整数
		if b {
			return 1, true
		}
		return 0, true
	}
	n, ok := textutil.Int(value)
	if !ok || !n.IsInt64() {
		return 0, false
	}
	return int(n.Int64()), true
}

// Executable 是后台启动时运行的程序（默认是当前二进制），ExtraEnv 是额外传给子进程的环境变量。
var (
	Executable = os.Executable
	ExtraEnv   []string
)

// spawn 在后台启动 serve，脱离当前终端，关掉终端也不会跟着退出。
func spawn(root string, log *os.File) (*exec.Cmd, <-chan struct{}, error) {
	program, err := Executable()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(program, "serve")
	env := append(os.Environ(), "VINX_DOCS_HOME="+root)
	cmd.Env = append(env, ExtraEnv...)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detachedAttr()
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return cmd, exited, nil
}

// Manage 实现 start/stop/status。进程身份一律用健康检查确认：只停自己启动、且正在应答的那个服务。
func Manage(root, action string) (int, error) {
	runtimeDir := textutil.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o777); err != nil {
		return 1, err
	}
	pidFile := textutil.Join(runtimeDir, "server.json")
	host, port := config.ServerAddress(root)
	unlock, err := config.Lock(textutil.Join(runtimeDir, "server"))
	if err != nil {
		return 1, err
	}
	defer unlock()
	savedPID, haveSaved := 0, false
	if data, err := os.ReadFile(pidFile); err == nil {
		if value, err := ojson.Decode(data); err == nil {
			if saved, ok := value.(*ojson.Object); ok {
				if raw := saved.Value("pid"); raw != nil {
					if _, isNumber := raw.(ojson.Number); isNumber || isBool(raw) {
						savedPID, haveSaved = pidOf(raw)
					}
				}
			}
		}
	}
	info := Health(root)
	running := info != nil && info.Value("home") == root
	infoPID := func() string {
		if info == nil {
			return "None"
		}
		return textutil.Str(info.Value("pid"))
	}
	switch action {
	case "status":
		if !running {
			fmt.Fprintf(Stdout, "Vinx Docs 未运行（家目录 %s）\n", root)
			return 1, nil
		}
		mode := "未知"
		lastError := ""
		if status, body := config.LocalRequest(root, "/api/status", 3*time.Second); status == 200 {
			if value, err := ojson.Decode(body); err == nil {
				if payload, ok := value.(*ojson.Object); ok {
					if auto, ok := payload.Value("autoSync").(*ojson.Object); ok {
						if m, ok := auto.Get("mode"); ok {
							mode = textutil.Str(m)
						}
						if textutil.Truthy(auto.Value("lastError")) {
							lastError = textutil.Str(auto.Value("lastError"))
						}
					}
				}
			}
		}
		fmt.Fprintf(Stdout, "Vinx Docs 运行中：http://%s:%d/（PID %s，家目录 %s）\n", host, port, infoPID(), root)
		line := "自动同步：" + mode
		if lastError != "" {
			line += "；上次错误：" + lastError
		}
		fmt.Fprintln(Stdout, line)
		return 0, nil
	case "stop":
		pid, ok := 0, false
		if info != nil {
			pid, ok = pidOf(info.Value("pid"))
		}
		if running && haveSaved && ok && pid == savedPID {
			if err := terminate(savedPID); err != nil {
				return 1, err
			}
			stopped := false
			for i := 0; i < 100; i++ {
				if Health(root) == nil {
					stopped = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !stopped {
				return 1, config.Errorf("服务仍在完成当前操作；未强制终止，请稍后重试")
			}
			fmt.Fprintln(Stdout, "Vinx Docs 已停止")
		} else if running {
			fmt.Fprintf(Stdout, "端口 %d 上的 Vinx Docs 不是由 start 启动的（PID %s），未停止它\n", port, infoPID())
			return 1, nil
		}
		if err := os.Remove(pidFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 1, err
		}
		return 0, nil
	}
	if running {
		fmt.Fprintf(Stdout, "Vinx Docs 已在运行：http://%s:%d/\n", host, port)
		return 0, nil
	}
	if status, _ := config.LocalRequest(root, "/", 3*time.Second); info != nil || status != 0 {
		return 1, config.Errorf("端口 %d 已被其他服务占用，可在配置的 server.port 里换一个", port)
	}
	logPath := textutil.Join(runtimeDir, "server.log")
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return 1, err
	}
	cmd, exited, err := spawn(root, log)
	log.Close()
	if err != nil {
		return 1, err
	}
	for i := 0; i < 100; i++ {
		select {
		case <-exited:
			return 1, config.Errorf("服务启动失败，详情见 %s", logPath)
		default:
		}
		if health := Health(root); health != nil {
			if pid, ok := pidOf(health.Value("pid")); ok && pid == cmd.Process.Pid {
				if err := config.AtomicWriteJSON(pidFile, ojson.NewObject("pid", cmd.Process.Pid, "port", port)); err != nil {
					return 1, err
				}
				fmt.Fprintf(Stdout, "Vinx Docs 已启动：http://%s:%d/\n", host, port)
				return 0, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = terminate(cmd.Process.Pid)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
	}
	return 1, config.Errorf("服务启动超时，已停止本次启动的进程；详情见 %s", logPath)
}

func isBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

// Serve 前台运行，给 systemd、launchd 或手动调试用；Ctrl+C 或 SIGTERM 退出。
// 先绑定端口（健康检查马上可用），站点在后台重建，完成前页面可能是上一次的版本。
func Serve(root string) error {
	var s *Server
	var err error
	if listen := os.Getenv(ListenOverride); listen != "" {
		host, portText, splitErr := splitHostPort(listen)
		port, convErr := strconv.Atoi(portText)
		if splitErr != nil || convErr != nil {
			return config.Errorf("%s 格式应为 host:port", ListenOverride)
		}
		s, err = Create(root, &host, &port)
	} else {
		s, err = Create(root, nil, nil)
	}
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		<-signals
		s.Shutdown(5 * time.Second)
	}()
	go s.startup()
	fmt.Fprintf(Stdout, "Vinx Docs 监听 http://%s:%d/（家目录 %s）\n", s.Host, s.Port, root)
	serveErr := s.Serve()
	s.Auto.Stop()
	return serveErr
}

// startup 是服务启动后的准备工作：持锁做首次刷新，再按配置启动自动同步。
func (s *Server) startup() {
	s.Lock.Lock()
	if _, err := build.Refresh(s.ConfigPath, s.Output, "", ""); err != nil {
		message := "启动时构建失败：" + err.Error()
		s.Auto.SetError(message)
		fmt.Fprintln(Stderr, message)
	}
	s.Lock.Unlock()
	if err := s.Auto.Apply(); err != nil {
		message := "自动同步未启动：" + err.Error()
		s.Auto.SetError(message)
		fmt.Fprintln(Stderr, message)
	}
}

func splitHostPort(value string) (string, string, error) {
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] == ':' {
			return value[:i], value[i+1:], nil
		}
	}
	return "", "", errors.New("missing port")
}
