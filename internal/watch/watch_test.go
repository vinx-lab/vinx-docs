package watch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func waitFor(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return pred()
}

func mkdirs(t *testing.T, paths ...string) {
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, text string) {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkSkipsCachesAndHiddenDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "docs")
	mkdirs(t, filepath.Join(root, "guide"), filepath.Join(root, "node_modules", "deep"), filepath.Join(root, ".git"))
	if err := os.Symlink(filepath.Join(root, "guide"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range WalkDirs([]string{root}, WatchBudget) {
		names[filepath.Base(p)] = true
	}
	if !names["guide"] || names["node_modules"] || names["deep"] || names[".git"] || names["link"] {
		t.Fatal(names)
	}
}

func TestWalkGivesUpOverBudget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "docs")
	for i := 0; i < 6; i++ {
		mkdirs(t, filepath.Join(root, "d"+itoa(i)))
	}
	if WalkDirs([]string{root}, 3) != nil {
		t.Fatal("超过预算应返回 nil")
	}
	if WalkDirs([]string{root}, 50) == nil {
		t.Fatal("预算内应返回目录")
	}
}

func TestWatcherCoalescesChangesAndSeesNewSubdirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "docs")
	mkdirs(t, filepath.Join(root, "guide"))
	var mu sync.Mutex
	hits := 0
	count := func() int { mu.Lock(); defer mu.Unlock(); return hits }
	w := New([]string{root}, func() { mu.Lock(); hits++; mu.Unlock() }, 0.6, 20)
	w.Start()
	defer w.Stop()
	if !waitFor(func() bool { return w.Mode() != "idle" }, 6*time.Second) {
		t.Fatal("监听未启动")
	}
	if w.Mode() != "inotify(2)" && w.Mode() != "poll" {
		t.Fatal(w.Mode())
	}
	if w.Mode() == "poll" {
		t.Skip("本机没有 inotify")
	}
	write(t, filepath.Join(root, "guide", "a.md"), "one")
	write(t, filepath.Join(root, "guide", "b.md"), "two")
	if !waitFor(func() bool { return count() >= 1 }, 6*time.Second) {
		t.Fatal("没有触发")
	}
	time.Sleep(300 * time.Millisecond)
	if count() != 1 {
		t.Fatalf("同一防抖窗口只应触发一次，实际 %d", count())
	}
	before := count()
	mkdirs(t, filepath.Join(root, "later"))
	if !waitFor(func() bool { return count() > before }, 6*time.Second) {
		t.Fatal("新建目录没有触发")
	}
	after := count()
	write(t, filepath.Join(root, "later", "c.md"), "three")
	if !waitFor(func() bool { return count() > after }, 6*time.Second) {
		t.Fatal("新目录里的文件没有触发")
	}
	// 隐藏文件和缓存目录的变化不触发。
	quiet := count()
	write(t, filepath.Join(root, ".hidden.md"), "x")
	time.Sleep(1500 * time.Millisecond)
	if count() != quiet {
		t.Fatal("隐藏文件不应触发同步")
	}
	w.Stop()
	if w.Mode() != "idle" {
		t.Fatal(w.Mode())
	}
}

func TestPollModeDetectsChanges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "docs")
	mkdirs(t, root)
	write(t, filepath.Join(root, "a.md"), "one")
	var mu sync.Mutex
	hits := 0
	w := New([]string{root}, func() { mu.Lock(); hits++; mu.Unlock() }, 0.5, 0.3)
	done := make(chan struct{})
	go func() { w.poll([]string{root}); close(done) }()
	if !waitFor(func() bool { return w.Mode() == "poll" }, 6*time.Second) {
		t.Fatal(w.Mode())
	}
	time.Sleep(400 * time.Millisecond)
	write(t, filepath.Join(root, "b.md"), "two")
	if !waitFor(func() bool { mu.Lock(); defer mu.Unlock(); return hits >= 1 }, 8*time.Second) {
		t.Fatal("轮询没有发现变化")
	}
	w.stopped.Store(true)
	close(w.stopCh)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("轮询没有退出")
	}
}

func TestReloadSwitchesRootsAndStopIsPrompt(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "one"), filepath.Join(base, "two")
	mkdirs(t, first, second)
	var mu sync.Mutex
	hits := 0
	w := New([]string{first}, func() { mu.Lock(); hits++; mu.Unlock() }, 0.5, 20)
	w.Start()
	if !waitFor(func() bool { return w.Mode() != "idle" }, 6*time.Second) {
		t.Fatal("监听未启动")
	}
	if w.Mode() == "poll" {
		w.Stop()
		t.Skip("本机没有 inotify")
	}
	w.Reload([]string{first, second})
	if !waitFor(func() bool { return w.Mode() == "inotify(2)" }, 6*time.Second) {
		t.Fatal(w.Mode())
	}
	write(t, filepath.Join(second, "x.md"), "x")
	if !waitFor(func() bool { mu.Lock(); defer mu.Unlock(); return hits >= 1 }, 6*time.Second) {
		t.Fatal("重载后的新根没有触发")
	}
	start := time.Now()
	w.Stop()
	if time.Since(start) > time.Second {
		t.Fatalf("停止用了 %v", time.Since(start))
	}
}

func TestNoRootsIsIdle(t *testing.T) {
	w := New([]string{filepath.Join(t.TempDir(), "missing")}, func() {}, 1, 0.2)
	w.Start()
	time.Sleep(300 * time.Millisecond)
	if w.Mode() != "idle" {
		t.Fatal(w.Mode())
	}
	w.Stop()
}

func TestSnapshotSkipsHiddenAndCacheDirectories(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, "node_modules"), filepath.Join(root, ".cache"), filepath.Join(root, "docs"))
	write(t, filepath.Join(root, "node_modules", "x.js"), "x")
	write(t, filepath.Join(root, ".cache", "y"), "y")
	write(t, filepath.Join(root, "docs", "a.md"), "a")
	write(t, filepath.Join(root, ".top.md"), "t")
	state := Snapshot([]string{root})
	if len(state) != 2 {
		t.Fatal(state) // docs/a.md 和 .top.md（隐藏文件本身仍在指纹里）
	}
}
