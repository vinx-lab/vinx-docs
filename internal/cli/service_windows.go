//go:build windows

package cli

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// setRunValue 在当前用户的登录启动项里写入（或覆盖）一个值。
func setRunValue(name, value string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, WindowsRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(name, value)
}

// deleteRunValue 删除当前用户登录启动项里的一个值；本来就没有时不算错。
func deleteRunValue(name string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, WindowsRunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
