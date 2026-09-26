package server

import (
	"strconv"
	"sync"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/build"
	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
	"github.com/vinx-lab/vinx-docs/internal/watch"
)

// AutoSync 把注册根的文件变化合并成自动刷新；开关和防抖时间来自配置。
type AutoSync struct {
	server *Server

	mu        sync.Mutex
	watcher   *watch.Watcher
	lastError string
	lastAuto  int64
}

// State 返回 (mode, lastRun, lastError)；没有监听时 mode 是 "off"。
func (a *AutoSync) State() (string, int64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	mode := "off"
	if a.watcher != nil {
		mode = a.watcher.Mode()
	}
	return mode, a.lastAuto, a.lastError
}

// SetError 记录一条错误（启动时构建失败等）。
func (a *AutoSync) SetError(message string) {
	a.mu.Lock()
	a.lastError = message
	a.mu.Unlock()
}

// Apply 按当前配置启停或重载监听。配置不可读或某个项目的目录失效时返回 ConfigError。
func (a *AutoSync) Apply() error {
	cfg, err := config.Load(a.server.ConfigPath)
	if err != nil {
		return err
	}
	settings := config.DefaultSettings()
	if value, ok := cfg.Value("settings").(*ojson.Object); ok {
		settings = value
	}
	var roots []string
	for _, item := range config.Projects(cfg) {
		project, ok := item.(*ojson.Object)
		if !ok {
			return &config.PanicError{Msg: "AttributeError: project is not an object"}
		}
		projectRoots, err := config.ProjectRoots(project)
		if err != nil {
			return err
		}
		for _, root := range projectRoots {
			roots = append(roots, root.Path)
		}
	}
	if !textutil.Truthy(settings.Value("autoSync")) || len(roots) == 0 {
		// 不在持有 a.mu 时等待监听退出：正在进行的自动同步结束时要拿 a.mu 记录结果。
		a.Stop()
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	debounce := 3.0
	if value, ok := settings.Get("debounceSeconds"); ok {
		if f, ok := number(value); ok {
			debounce = f
		}
	}
	if a.watcher == nil {
		a.watcher = watch.New(roots, a.sync, debounce, 20)
		a.watcher.Start()
	} else {
		a.watcher.SetDebounce(debounce)
		a.watcher.Reload(roots)
	}
	return nil
}

// Stop 停止监听。
func (a *AutoSync) Stop() {
	a.mu.Lock()
	watcher := a.watcher
	a.watcher = nil
	a.mu.Unlock()
	if watcher != nil {
		watcher.Stop()
	}
}

// sync 手动操作优先，正在同步时跳过这一轮，下一次文件变化会再触发。
func (a *AutoSync) sync() {
	lock := a.server.Lock
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	_, err := build.Refresh(a.server.ConfigPath, a.server.Output, "", "")
	a.mu.Lock()
	if err != nil {
		a.lastError = err.Error()
	} else {
		a.lastError = ""
	}
	a.lastAuto = time.Now().Unix()
	a.mu.Unlock()
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case ojson.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		return f, err == nil
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}
