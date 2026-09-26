package build

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"

	"github.com/vinx-lab/vinx-docs/internal/config"
	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

// Assets 是前端资源（web/、vendor/）的来源。
//
// Dir 非空时资源在真实目录里（家目录自带的 web/，或用 --assets 指定的开发目录），
// 可以做符号链接检查，复制时保留权限位和修改时间。
// Dir 为空时只用 FS（go:embed 内嵌资源），内嵌文件没有符号链接，复制时权限按 0666 &^ umask、不设修改时间。
type Assets struct {
	FS  fs.FS
	Dir string
}

// DirAssets 从真实目录创建资源来源。
func DirAssets(dir string) Assets {
	return Assets{FS: os.DirFS(dir), Dir: dir}
}

// Valid 报告是否配置了资源来源。
func (a Assets) Valid() bool { return a.FS != nil }

// PackageAssets 是随程序分发的前端资源：家目录里没有 web/ 时使用。
// 默认是内嵌资源（assets.Install 设置）；开发时可用 --assets 换成真实目录。
var PackageAssets Assets

// AssetsRoot 家目录里自带一份 web/ 时用它，否则用安装包里的。
func AssetsRoot(home string) Assets {
	if textutil.IsDir(textutil.Join(home, "web")) {
		return DirAssets(home)
	}
	return PackageAssets
}

func (a Assets) isFile(name string) bool {
	if !a.Valid() {
		return false
	}
	info, err := fs.Stat(a.FS, name)
	return err == nil && info.Mode().IsRegular()
}

func (a Assets) isDir(name string) bool {
	if !a.Valid() {
		return false
	}
	info, err := fs.Stat(a.FS, name)
	return err == nil && info.IsDir()
}

// copyFile 只复制内容，目标权限按默认（0666 &^ umask）。
func (a Assets) copyFile(name, dst string) error {
	src, err := a.FS.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()
	return writeFrom(src, dst, 0o666)
}

func writeFrom(src io.Reader, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyTree 递归复制目录，跟随符号链接；来源是真实目录时，文件保留权限和修改时间。
func (a Assets) copyTree(name, dst string) error {
	entries, err := fs.ReadDir(a.FS, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	for _, entry := range entries {
		child := path.Join(name, entry.Name())
		target := filepath.Join(dst, entry.Name())
		info, err := fs.Stat(a.FS, child) // 跟随符号链接
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := a.copyTree(child, target); err != nil {
				return err
			}
			continue
		}
		if err := a.copyWithMeta(child, target, info); err != nil {
			return err
		}
	}
	return nil
}

func (a Assets) copyWithMeta(name, dst string, info fs.FileInfo) error {
	if err := a.copyFile(name, dst); err != nil {
		return err
	}
	if a.Dir == "" {
		return nil
	}
	mode := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	if err := os.Chmod(dst, mode); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

var hexDigestRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// verifyVendor 按配置里的 vendor 或 vendor/manifest.json 校验每个文件的 SHA-256。
func verifyVendor(vendor Assets, root string, cfg *ojson.Object) error {
	configured := cfg.Value("vendor")
	manifestPath := path.Join(root, "manifest.json")
	var expected *ojson.Object
	haveExpected := false
	if obj, ok := configured.(*ojson.Object); ok {
		if files, ok := obj.Value("files").(*ojson.Object); ok {
			expected = files
		} else {
			expected = obj
		}
		haveExpected = true
	} else if vendor.isFile(manifestPath) {
		data, err := fs.ReadFile(vendor.FS, manifestPath)
		if err != nil {
			return config.Errorf("vendor清单无效")
		}
		value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
		if err != nil {
			return config.Errorf("vendor清单无效")
		}
		if manifest, ok := value.(*ojson.Object); ok {
			if files, present := manifest.Get("files"); present && files != nil {
				obj, ok := files.(*ojson.Object)
				if !ok {
					return &config.PanicError{Msg: "AttributeError: vendor manifest files is not an object"}
				}
				expected, haveExpected = obj, true
			}
		}
	}
	if !haveExpected {
		if vendor.isDir(root) && vendor.hasAnyFile(root) {
			return config.Errorf("vendor缺少哈希清单")
		}
		return nil
	}
	if !vendor.isDir(root) {
		return config.Errorf("vendor目录不存在")
	}
	for _, rel := range expected.Keys() {
		digest, ok := expected.Value(rel).(string)
		if !ok || !hexDigestRE.MatchString(digest) {
			return config.Errorf("vendor哈希清单无效")
		}
		if textutil.IsAbs(rel) || contains(textutil.Parts(rel), "..") {
			return config.Errorf("vendor清单路径越界")
		}
		name := path.Join(root, textutil.Norm(rel))
		if textutil.Norm(rel) == "." {
			name = root
		}
		if !vendor.isFile(name) || vendor.isSymlinkOrOutside(root, name) {
			return config.Errorf("vendor文件缺失: %s", rel)
		}
		data, err := fs.ReadFile(vendor.FS, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if !equalFoldASCII(hex.EncodeToString(sum[:]), digest) {
			return config.Errorf("vendor哈希校验失败: %s", rel)
		}
	}
	return nil
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 32
		}
		if 'A' <= y && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}

// isSymlinkOrOutside 报告路径是符号链接，或解析后不在 vendor 目录内。
func (a Assets) isSymlinkOrOutside(root, name string) bool {
	if a.Dir == "" {
		return false
	}
	real := textutil.Join(a.Dir, name)
	if textutil.IsSymlink(real) {
		return true
	}
	return !textutil.IsWithin(textutil.Realpath(real), textutil.Realpath(textutil.Join(a.Dir, root)))
}

func (a Assets) hasAnyFile(root string) bool {
	found := false
	_ = fs.WalkDir(a.FS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if p != root {
			if info, err := fs.Stat(a.FS, p); err == nil && info.Mode().IsRegular() {
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	return found
}
