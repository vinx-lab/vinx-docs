package config

import (
	"os"
	"path/filepath"
)

// Lock 对 <配置文件>.lock 加独占锁，返回解锁函数。
// 锁文件和锁方式（POSIX flock / Windows 锁第一个字节）是固定的，同时运行的多个进程靠它互斥。
func Lock(configPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o777); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(configPath+".lock", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	unlock, err := LockFile(handle)
	if err != nil {
		handle.Close()
		return nil, err
	}
	return func() {
		unlock()
		handle.Close()
	}, nil
}

// LockFile 对已打开的锁文件加独占锁（阻塞等待）。
func LockFile(handle *os.File) (func(), error) {
	if err := lockHandle(handle); err != nil {
		return nil, err
	}
	return func() { _ = unlockHandle(handle) }, nil
}
