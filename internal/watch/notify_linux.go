//go:build linux

package watch

import (
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// inMask 是监听的 inotify 事件：修改、写完关闭、移入移出、创建、删除、自身删除/移动。
const inMask = syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO | syscall.IN_MOVED_FROM |
	syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF

var errWatchLimit = errors.New("inotify watch 数量不足")

type inotify struct {
	fd      int
	file    *os.File // 非阻塞 fd 交给 Go 运行时的 poller，读取可以设截止时间
	mu      sync.Mutex
	watches map[int32]string
	woken   atomic.Bool
	buf     []byte
}

func newNotifier() (notifier, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	return &inotify{fd: fd, file: os.NewFile(uintptr(fd), "inotify"), watches: map[int32]string{}, buf: make([]byte, 65536)}, nil
}

func (n *inotify) add(path string) error {
	wd, err := syscall.InotifyAddWatch(n.fd, path, inMask)
	if err != nil {
		if err == syscall.ENOSPC || err == syscall.EMFILE {
			return errWatchLimit
		}
		return err
	}
	n.mu.Lock()
	n.watches[int32(wd)] = path
	n.mu.Unlock()
	return nil
}

func (n *inotify) read(timeout time.Duration) ([]event, error) {
	if err := n.file.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if n.woken.Swap(false) {
		return nil, nil
	}
	count, err := n.file.Read(n.buf)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	data := n.buf[:count]
	events := []event{}
	const header = 16 // struct inotify_event: int wd; uint32 mask, cookie, len
	for offset := 0; offset+header <= len(data); {
		wd := int32(binary.NativeEndian.Uint32(data[offset:]))
		mask := binary.NativeEndian.Uint32(data[offset+4:])
		length := int(binary.NativeEndian.Uint32(data[offset+12:]))
		offset += header
		end := offset + length
		if end > len(data) {
			end = len(data)
		}
		raw := data[offset:end]
		for i, b := range raw {
			if b == 0 {
				raw = raw[:i]
				break
			}
		}
		offset += length
		n.mu.Lock()
		dir := n.watches[wd]
		n.mu.Unlock()
		events = append(events, event{dir: dir, name: string(raw), isDir: mask&syscall.IN_ISDIR != 0})
	}
	return events, nil
}

func (n *inotify) interrupt() {
	n.woken.Store(true)
	_ = n.file.SetReadDeadline(time.Now())
}

func (n *inotify) close() { _ = n.file.Close() }
