//go:build windows

package server

import (
	"os"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// detachedAttr 用 DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW 启动：脱离控制台、不弹窗口。
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup | createNoWindow, HideWindow: true}
}

// terminate 在 Windows 上用 TerminateProcess 结束进程。
func terminate(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

// Winsock 的错误码和 Unix errno 数值不同，这里映射到同样的文案。
func init() {
	strerrors[syscall.Errno(10048)] = "Address already in use"          // WSAEADDRINUSE
	strerrors[syscall.Errno(10013)] = "Permission denied"               // WSAEACCES
	strerrors[syscall.Errno(10049)] = "Cannot assign requested address" // WSAEADDRNOTAVAIL
}
