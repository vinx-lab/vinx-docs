// Package config 负责家目录、配置读写与校验、文件锁和原子写。
package config

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/vinx-lab/vinx-docs/internal/ojson"
	"github.com/vinx-lab/vinx-docs/internal/textutil"
)

const SchemaVersion = 2

// Marker 是生成站点目录里托管标记文件的内容。
const Marker = "Vinx Docs managed site; do not edit.\n"

// Error 表示用户配置或导出边界不合法（命令行退出码 2，接口返回 400）。
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Errorf 构造 ConfigError。
func Errorf(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// IsConfigError 报告 err 是否是 ConfigError。
func IsConfigError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// settingDefault 保持 DEFAULT_SETTINGS 的键顺序。
type settingDefault struct {
	key   string
	value any // bool / int / string
}

var defaultSettings = []settingDefault{
	{"autoSync", true},
	{"debounceSeconds", 3},
	{"recentDays", 14},
	{"recentLimit", 12},
	// displayName 是批注和回复的署名，也用来在页面上区分「我的回复」。
	{"displayName", "我"},
}

// DefaultSettings 返回 DEFAULT_SETTINGS 的副本。
func DefaultSettings() *ojson.Object {
	o := ojson.NewObject()
	for _, item := range defaultSettings {
		o.Set(item.key, jsonScalar(item.value))
	}
	return o
}

func jsonScalar(v any) any {
	if i, ok := v.(int); ok {
		return ojson.Number(fmt.Sprint(i))
	}
	return v
}

// DefaultConfig 返回 DEFAULT_CONFIG 的深拷贝。
func DefaultConfig() *ojson.Object {
	return ojson.NewObject(
		"schemaVersion", ojson.Number("2"),
		"server", ojson.NewObject("host", "0.0.0.0", "port", ojson.Number("8000")),
		"protectedPorts", []any{},
		"settings", DefaultSettings(),
		"projects", []any{},
	)
}

// Settings 是校验后的运行设置。
type Settings struct {
	AutoSync        bool
	DebounceSeconds int
	RecentDays      int
	RecentLimit     int
	DisplayName     string
}

// SettingsOf 从（已校验的）配置读出设置；缺失时用默认值。
func SettingsOf(cfg *ojson.Object) Settings {
	s := Settings{AutoSync: true, DebounceSeconds: 3, RecentDays: 14, RecentLimit: 12, DisplayName: "我"}
	obj, _ := cfg.Value("settings").(*ojson.Object)
	if obj == nil {
		return s
	}
	if v, ok := obj.Value("autoSync").(bool); ok {
		s.AutoSync = v
	}
	intOf := func(key string, dst *int) {
		if n, ok := textutil.Int(obj.Value(key)); ok && n.IsInt64() {
			*dst = int(n.Int64())
		}
	}
	intOf("debounceSeconds", &s.DebounceSeconds)
	intOf("recentDays", &s.RecentDays)
	intOf("recentLimit", &s.RecentLimit)
	if v, ok := obj.Value("displayName").(string); ok {
		s.DisplayName = v
	}
	return s
}

// DataHome 配置、站点、编辑历史、页面登记和批注库所在的家目录。
func DataHome() (string, error) {
	if configured := os.Getenv("VINX_DOCS_HOME"); configured != "" {
		expanded, err := textutil.ExpandUser(configured)
		if err != nil {
			return "", err
		}
		return textutil.Absolute(expanded), nil
	}
	home := textutil.Home()
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "vinx-docs"), nil
	case "darwin":
		base = textutil.Join(home, "Library", "Application Support")
	default:
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = textutil.Join(home, ".local", "share")
		}
	}
	return textutil.Join(base, "vinx-docs"), nil
}

// ConfigPath 返回家目录下的配置文件路径。
func ConfigPath(home string) string { return textutil.Join(home, "config", "projects.json") }

// SitePath 返回家目录下的站点输出目录。
func SitePath(home string) string { return textutil.Join(home, ".runtime", "site") }

// ServerAddress 监听全部网卡时连回环地址。
func ServerAddress(home string) (string, int) {
	host, port := "0.0.0.0", 8000
	if cfg, err := Load(ConfigPath(home)); err == nil {
		if server, ok := cfg.Value("server").(*ojson.Object); ok {
			if h, ok := server.Value("host").(string); ok {
				if n, ok := textutil.Int(server.Value("port")); ok && n.IsInt64() {
					host, port = h, int(n.Int64())
				}
			}
		}
	}
	if host == "0.0.0.0" || host == "" || host == "::" {
		host = "127.0.0.1"
	}
	return host, port
}

// Load 不存在时返回安全的空配置，否则校验形状并迁移。
func Load(path string) (*ojson.Object, error) {
	if !textutil.Exists(path) {
		return DefaultConfig(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Errorf("无法读取配置: %s", textutil.Norm(path))
	}
	value, err := ojson.Decode([]byte(textutil.UniversalNewlines(string(data))))
	if err != nil {
		return nil, Errorf("无法读取配置: %s", textutil.Norm(path))
	}
	if err := ValidateShape(value); err != nil {
		return nil, err
	}
	return Migrate(value.(*ojson.Object))
}

// Migrate 把 v1 的 include 白名单迁移为 v2 黑名单，并把 settings 规范化。
func Migrate(value *ojson.Object) (*ojson.Object, error) {
	settings, err := ValidateSettings(value.Value("settings"))
	if err != nil {
		return nil, err
	}
	value.Set("settings", settings)
	if textutil.EqualsInt(value.Value("schemaVersion"), SchemaVersion) {
		return value, nil
	}
	if projects, ok := value.Value("projects").([]any); ok {
		for _, item := range projects {
			if project, ok := item.(*ojson.Object); ok {
				project.Delete("include")
			}
		}
	}
	value.Set("schemaVersion", ojson.Number("2"))
	return value, nil
}

// ValidateSettings 返回按默认键顺序合并后的设置。
func ValidateSettings(value any) (*ojson.Object, error) {
	if value == nil {
		return DefaultSettings(), nil
	}
	obj, ok := value.(*ojson.Object)
	if !ok {
		return nil, Errorf("settings字段无效")
	}
	known := map[string]bool{}
	for _, item := range defaultSettings {
		known[item.key] = true
	}
	for _, key := range obj.Keys() {
		if !known[key] {
			return nil, Errorf("settings字段无效")
		}
	}
	merged := DefaultSettings()
	for _, def := range defaultSettings {
		item, present := obj.Get(def.key)
		if !present {
			continue
		}
		switch def.value.(type) {
		case bool:
			if _, ok := item.(bool); !ok {
				return nil, Errorf("settings.%s必须是布尔值", def.key)
			}
		case string:
			text, ok := item.(string)
			if !ok || textutil.Strip(text) == "" || textutil.Len(text) > 40 || hasControl(text) {
				return nil, Errorf("settings.%s必须是1到40个字符的文本", def.key)
			}
			item = textutil.Strip(text)
		default:
			n, ok := textutil.Int(item)
			if !ok || n.Cmp(big.NewInt(1)) < 0 || n.Cmp(big.NewInt(3600)) > 0 {
				return nil, Errorf("settings.%s必须是1到3600的整数", def.key)
			}
		}
		merged.Set(def.key, item)
	}
	return merged, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 32 {
			return true
		}
	}
	return false
}

// ValidateShape 校验配置的整体结构（版本、server、protectedPorts、projects 等字段的类型）。
func ValidateShape(value any) error {
	obj, ok := value.(*ojson.Object)
	if !ok || !(textutil.EqualsInt(obj.Value("schemaVersion"), 1) || textutil.EqualsInt(obj.Value("schemaVersion"), SchemaVersion)) {
		return Errorf("schemaVersion必须为1或%d", SchemaVersion)
	}
	if _, err := ValidateSettings(obj.Value("settings")); err != nil {
		return err
	}
	server, ok := obj.Value("server").(*ojson.Object)
	if !ok {
		return Errorf("server配置无效")
	}
	if _, ok := server.Value("host").(string); !ok {
		return Errorf("server配置无效")
	}
	port, ok := textutil.Int(server.Value("port"))
	if !ok || port.Cmp(big.NewInt(1)) < 0 || port.Cmp(big.NewInt(65535)) > 0 {
		return Errorf("server.port必须是1到65535的整数")
	}
	protected := []any{}
	if raw, present := obj.Get("protectedPorts"); present {
		list, ok := raw.([]any)
		if !ok {
			return Errorf("protectedPorts必须是整数数组")
		}
		protected = list
	}
	for _, item := range protected {
		if _, ok := textutil.Int(item); !ok {
			return Errorf("protectedPorts必须是整数数组")
		}
	}
	for _, item := range protected {
		if p, _ := textutil.Int(item); p.Cmp(port) == 0 {
			return Errorf("server.port不能使用受保护端口")
		}
	}
	if _, ok := obj.Value("projects").([]any); !ok {
		return Errorf("projects必须是数组")
	}
	return nil
}

// ProtectedPorts 返回配置里的受保护端口（已校验的配置）。
func ProtectedPorts(cfg *ojson.Object) []any {
	list, _ := cfg.Value("protectedPorts").([]any)
	return list
}

// Projects 返回配置里的项目数组。
func Projects(cfg *ojson.Object) []any {
	list, _ := cfg.Value("projects").([]any)
	return list
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidID 报告字符串是否是合法的项目 ID。
func ValidID(s string) bool { return idRE.MatchString(s) && !strings.Contains(s, "\n") }
