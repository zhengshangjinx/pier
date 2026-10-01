package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsName)

	s, err := LoadSettings(path)
	if err != nil || s.Theme != "system" {
		t.Fatalf("文件不存在时应当返回默认值 system：%+v %v", s, err)
	}
	if err := SaveSettings(path, Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := LoadSettings(path); s.Theme != "dark" {
		t.Errorf("存进去又读出来不一致：%+v", s)
	}
	if err := SaveSettings(path, Settings{Theme: "purple"}); err == nil {
		t.Error("不认识的主题应当被拒绝")
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadSettings(path); err == nil || s.Theme != "system" {
		t.Errorf("文件坏了应当报错并退回默认值：%+v %v", s, err)
	}
}
