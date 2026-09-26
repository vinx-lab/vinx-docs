package config

import (
	"os"
	"path/filepath"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
)

// AtomicWriteJSON 写临时文件（0600，同 mkstemp）、fsync 后原子替换。
// 输出为两格缩进、非 ASCII 字符原样保留的 JSON，末尾加换行（格式见 ojson.Dumps）。
func AtomicWriteJSON(path string, value any) error {
	return AtomicWrite(path, []byte(ojson.Dumps(value, 2)+"\n"))
}

// AtomicWrite 原子写任意内容。
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
