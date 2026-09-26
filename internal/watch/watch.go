// Package watch 监听登记目录的变化。Linux 上优先用 inotify，拿不到 watch 时降级为轮询。
//
// 只监听已注册的文档根，跳过隐藏目录和缓存/仓库目录，因此 watch 数量与文档目录数同级，
// 不会因为某个项目里有 node_modules 就把内核 watch 耗光。只用标准库（syscall），不依赖 fsnotify。
package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SkipDirs 与生成器一致的剪枝规则；监听范围不应该大于发布范围。
var SkipDirs = map[string]bool{"__pycache__": true, "node_modules": true, "target": true, "logs": true, ".git": true}

// WatchBudget 是 inotify 模式最多监听的目录数，超过就改用轮询。
const WatchBudget = 4096

func skipped(name string) bool { return strings.HasPrefix(name, ".") || SkipDirs[name] }

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// WalkDirs 列出需要监听的目录；超过预算返回 nil，让调用方改用轮询。
func WalkDirs(roots []string, budget int) []string {
	found := []string{}
	var walk func(dir string) bool
	walk = func(dir string) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true // 读不了的目录直接跳过
		}
		var children []string
		for _, entry := range entries {
			// 符号链接一律不跟随（is_symlink 的目录被剔除），只收真正的子目录。
			if entry.IsDir() && !skipped(entry.Name()) {
				children = append(children, filepath.Join(dir, entry.Name()))
			}
		}
		found = append(found, children...)
		if len(found) > budget {
			return false
		}
		for _, child := range children {
			if !walk(child) {
				return false
			}
		}
		return true
	}
	for _, root := range roots {
		if !isDir(root) {
			continue
		}
		found = append(found, root)
		if !walk(root) {
			return nil
		}
	}
	return found
}

// FileState 是轮询模式下单个文件的指纹。
type FileState struct {
	MtimeNS int64
	Size    int64
}

// Snapshot 轮询模式使用的目录树指纹，路径到 (mtime_ns, size)。
func Snapshot(roots []string) map[string]FileState {
	state := map[string]FileState{}
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if !skipped(entry.Name()) {
					walk(path)
				}
				continue
			}
			info, err := os.Stat(path) // 跟随符号链接；指向目录的链接不进入，也不算文件
			if err != nil || info.IsDir() {
				continue
			}
			state[path] = FileState{info.ModTime().UnixNano(), info.Size()}
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return state
}

func sameState(a, b map[string]FileState) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// Watcher 把一串文件事件合并成一次回调；静默 debounce 后才真正触发同步。
type Watcher struct {
	onChange     func()
	pollInterval time.Duration

	mu       sync.Mutex
	roots    []string
	debounce time.Duration
	mode     string
	started  bool
	wake     func() // 打断正在等待的 inotify 读取（Stop / Reload 时调用）

	stopped  atomic.Bool
	reloaded atomic.Bool
	stopCh   chan struct{}
	reloadCh chan struct{}
	done     chan struct{}
}

// New 创建监听器；防抖和轮询间隔以秒为单位（可带小数）。
func New(roots []string, onChange func(), debounce, pollInterval float64) *Watcher {
	w := &Watcher{
		onChange:     onChange,
		pollInterval: seconds(pollInterval),
		roots:        append([]string(nil), roots...),
		mode:         "idle",
		stopCh:       make(chan struct{}),
		reloadCh:     make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	w.SetDebounce(debounce)
	return w
}

func seconds(value float64) time.Duration { return time.Duration(value * float64(time.Second)) }

// SetDebounce 修改防抖时间（秒），最小 0.5 秒。
func (w *Watcher) SetDebounce(value float64) {
	if value < 0.5 {
		value = 0.5
	}
	w.mu.Lock()
	w.debounce = seconds(value)
	w.mu.Unlock()
}

func (w *Watcher) currentDebounce() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.debounce
}

// Mode 返回 "idle"、"inotify(N)" 或 "poll"。
func (w *Watcher) Mode() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mode
}

func (w *Watcher) setMode(mode string) {
	w.mu.Lock()
	w.mode = mode
	w.mu.Unlock()
}

func (w *Watcher) setWake(fn func()) {
	w.mu.Lock()
	w.wake = fn
	w.mu.Unlock()
}

func (w *Watcher) interrupt() {
	w.mu.Lock()
	fn := w.wake
	w.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Start 启动后台监听；重复调用无效。
func (w *Watcher) Start() {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.run()
}

// Stop 停止监听，最多等 5 秒让后台退出；之后 Mode() 为 "idle"。
func (w *Watcher) Stop() {
	if w.stopped.CompareAndSwap(false, true) {
		close(w.stopCh)
	}
	w.interrupt()
	w.mu.Lock()
	started := w.started
	w.mu.Unlock()
	if started {
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
		}
	}
	w.setMode("idle")
}

// Reload 换一组根目录，后台重新列目录。
func (w *Watcher) Reload(roots []string) {
	w.mu.Lock()
	w.roots = append([]string(nil), roots...)
	w.mu.Unlock()
	w.reloaded.Store(true)
	select {
	case w.reloadCh <- struct{}{}:
	default:
	}
	w.interrupt()
}

func (w *Watcher) active() bool { return !w.stopped.Load() && !w.reloaded.Load() }

// sleep 等待 d；被 Stop 打断返回 false。被 Reload 打断时也提前返回 true（由外层循环重新读根目录）。
func (w *Watcher) sleep(d time.Duration, reloadWakes bool) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	if reloadWakes {
		select {
		case <-w.stopCh:
			return false
		case <-w.reloadCh:
			return true
		case <-timer.C:
			return true
		}
	}
	select {
	case <-w.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (w *Watcher) run() {
	defer close(w.done)
	for !w.stopped.Load() {
		w.reloaded.Store(false)
		select { // 清掉已处理的重载通知
		case <-w.reloadCh:
		default:
		}
		w.mu.Lock()
		candidates := append([]string(nil), w.roots...)
		w.mu.Unlock()
		var roots []string
		for _, item := range candidates {
			if isDir(item) {
				roots = append(roots, item)
			}
		}
		if len(roots) == 0 {
			w.setMode("idle")
			w.sleep(w.pollInterval, true)
			continue
		}
		directories := WalkDirs(roots, WatchBudget)
		if directories == nil {
			w.poll(roots)
		} else {
			w.watchInotify(roots, directories)
		}
	}
}

// fire 调用回调；自动同步失败（包括 panic）不应该让监听退出。
func (w *Watcher) fire() {
	defer func() { _ = recover() }()
	w.onChange()
}

// notifier 是平台相关的事件源（Linux 上是 inotify）。
type notifier interface {
	add(path string) error // 返回 errWatchLimit 表示 watch 数量不足
	// read 等待最多 timeout；返回事件（没有事件时为空）。
	read(timeout time.Duration) ([]event, error)
	interrupt()
	close()
}

type event struct {
	dir   string // 事件所在的监听目录；未知时为空
	name  string
	isDir bool
}

func (w *Watcher) watchInotify(roots, directories []string) {
	notify, err := newNotifier()
	if err != nil {
		w.poll(roots)
		return
	}
	defer notify.close()
	for _, directory := range directories {
		if err := notify.add(directory); err == errWatchLimit {
			notify.close()
			w.poll(roots)
			return
		}
	}
	w.setWake(notify.interrupt)
	defer w.setWake(nil)
	if !w.active() {
		return
	}
	w.setMode("inotify(" + itoa(len(directories)) + ")")
	pending := false
	for w.active() {
		timeout := time.Second
		if pending {
			timeout = w.currentDebounce()
		}
		events, err := notify.read(timeout)
		if err != nil {
			// 读失败时退回轮询，比让监听静默失效好。
			notify.close()
			w.poll(roots)
			return
		}
		if events == nil {
			if pending && w.active() {
				pending = false
				w.fire()
			}
			continue
		}
		fresh := false
		for _, item := range events {
			if skipped(item.name) {
				continue
			}
			if item.isDir && item.dir != "" {
				// 新建子目录要立刻纳入监听，否则里面的新文档不会触发同步。
				_ = notify.add(filepath.Join(item.dir, item.name))
			}
			fresh = true
		}
		if fresh {
			pending = true
		}
	}
}

func (w *Watcher) poll(roots []string) {
	w.setMode("poll")
	state := Snapshot(roots)
	for w.active() {
		if !w.sleep(w.pollInterval, true) || !w.active() {
			return
		}
		fresh := Snapshot(roots)
		if !sameState(fresh, state) {
			if !w.sleep(w.currentDebounce(), false) {
				return
			}
			state = Snapshot(roots)
			w.fire()
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
