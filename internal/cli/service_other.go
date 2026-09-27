//go:build !windows

package cli

import "errors"

var errNotWindows = errors.New("登录启动项只在 Windows 上使用")

func setRunValue(name, value string) error { return errNotWindows }

func deleteRunValue(name string) error { return errNotWindows }
