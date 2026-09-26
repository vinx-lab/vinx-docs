//go:build !windows

package server

import "syscall"

// detachedAttr 新会话，关掉终端也不会跟着退出。
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// terminate 给进程发 SIGTERM。
func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
