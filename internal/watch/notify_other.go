//go:build !linux

package watch

import "errors"

var errWatchLimit = errors.New("inotify watch 数量不足")

// 非 Linux 平台没有 inotify，直接用轮询。
func newNotifier() (notifier, error) { return nil, errors.New("inotify 不可用") }
