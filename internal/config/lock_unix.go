//go:build unix

package config

import (
	"errors"
	"os"
	"syscall"
)

func lockHandle(handle *os.File) error {
	for {
		err := syscall.Flock(int(handle.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func unlockHandle(handle *os.File) error {
	return syscall.Flock(int(handle.Fd()), syscall.LOCK_UN)
}
