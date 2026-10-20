package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vinx-lab/vinx-docs/internal/testutil"
)

func TestHostName(t *testing.T) {
	for in, want := range map[string]string{"Docs.Example.com:443": "docs.example.com", "docs.example.com.": "docs.example.com",
		"[::1]:8000": "::1", "127.0.0.1": "127.0.0.1", " ": ""} {
		if got := HostName(in); got != want {
			t.Fatal(in, got, want)
		}
	}
}

func TestSessionsSurviveReopenAndFilesArePrivate(t *testing.T) {
	home := testutil.TempDir(t)
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	token, err := s.Login("correct horse", "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Valid(token) || reopened.Valid("0123456789abcdef0123456789abcdef") {
		t.Fatal("重启后会话应仍有效，伪造令牌无效")
	}
	reopened.Now = func() time.Time { return time.Now().Add((SessionDays*24 + 1) * time.Hour) }
	if reopened.Valid(token) {
		t.Fatal("会话过期后应失效")
	}
	for _, name := range []string{"auth.json", "sessions.json"} {
		path := filepath.Join(filepath.Dir(s.settingsPath), name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal(name, info, err)
		}
		if data, _ := os.ReadFile(path); len(data) == 0 || string(data) == token {
			t.Fatal(name)
		}
	}
	if data, _ := os.ReadFile(s.sessionsPath); strings.Contains(string(data), token) || strings.Contains(string(data), "correct horse") {
		t.Fatal("会话文件不应保存令牌原文")
	}
	if data, _ := os.ReadFile(s.settingsPath); strings.Contains(string(data), "correct horse") {
		t.Fatal("不应保存密码原文")
	}
}
