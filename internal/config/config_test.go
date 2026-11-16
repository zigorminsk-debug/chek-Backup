package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultHost exists: the mailbox of csl.by is published as mail.csl.by in
// DNS, the name imap.csl.by does not exist.
func TestDefaultHost(t *testing.T) {
	c := Default()
	if c.IMAP.Host != "mail.csl.by" {
		t.Fatalf("ожидался сервер mail.csl.by, получен %q", c.IMAP.Host)
	}
	if c.IMAP.Port != 993 || c.IMAP.Mode != TLSImplicit {
		t.Fatalf("ожидался 993 + SSL/TLS, получено %d + %q", c.IMAP.Port, c.IMAP.Mode)
	}
	if c.IMAP.Username != "backup@csl.by" {
		t.Fatalf("ожидался ящик backup@csl.by, получен %q", c.IMAP.Username)
	}
}

// TestLoadMigratesLegacyHost makes sure that a configuration written by an
// earlier build (with the non existing imap.csl.by) is repaired on load.
func TestLoadMigratesLegacyHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"imap":{"host":"imap.csl.by","port":993,"mode":"implicit","username":"backup@csl.by"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, created, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if created {
		t.Fatal("файл существует, created должен быть false")
	}
	if cfg.IMAP.Host != DefaultHost {
		t.Fatalf("старый сервер не исправлен: %q", cfg.IMAP.Host)
	}
}

// TestNormalizeKeepsCustomHost checks that a user supplied server is untouched.
func TestNormalizeKeepsCustomHost(t *testing.T) {
	cfg := Default()
	cfg.IMAP.Host = "10.254.100.250"
	cfg.Normalize()
	if cfg.IMAP.Host != "10.254.100.250" {
		t.Fatalf("адрес изменён: %q", cfg.IMAP.Host)
	}
}

// TestValidate requires the connection fields and valid patterns.
func TestValidate(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("конфигурация по умолчанию не проходит проверку: %v", err)
	}
	cfg.IMAP.Host = " "
	if err := cfg.Validate(); err == nil {
		t.Fatal("пустой сервер должен считаться ошибкой")
	}
	cfg = Default()
	cfg.Rules.FailurePatterns = []string{"("}
	if err := cfg.Validate(); err == nil {
		t.Fatal("некорректное регулярное выражение должно считаться ошибкой")
	}
}

// TestScheduleTime checks parsing of the daily time.
func TestScheduleTime(t *testing.T) {
	cfg := Default()
	cfg.Schedule.DailyAt = "7:05"
	hour, minute, err := cfg.Schedule.Time()
	if err != nil || hour != 7 || minute != 5 {
		t.Fatalf("получено %d:%d, %v", hour, minute, err)
	}
	cfg.Schedule.DailyAt = "25:00"
	if _, _, err := cfg.Schedule.Time(); err == nil {
		t.Fatal("25:00 должно быть ошибкой")
	}
}

// TestLoadExReportsRepair checks that the caller learns about the automatic
// replacement of a legacy server name.
func TestLoadExReportsRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	legacy := `{"imap":{"host":"imap.csl.by","port":993,"mode":"implicit","username":"backup@csl.by"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, repairedFrom, err := LoadEx(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IMAP.Host != DefaultHost {
		t.Fatalf("сервер не исправлен: %q", cfg.IMAP.Host)
	}
	if repairedFrom != "imap.csl.by → mail.csl.by" {
		t.Fatalf("repair не описан: %q", repairedFrom)
	}

	// A normal configuration must not report anything.
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, _, again, err := LoadEx(path); err != nil || again != "" {
		t.Fatalf("повторная загрузка сообщила об исправлении: %q, %v", again, err)
	}
}
