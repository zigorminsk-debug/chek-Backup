package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/checker"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	res := checker.Result{
		Status:     checker.StatusError,
		Title:      "Бэкап не выполнен",
		Summary:    "Ошибка",
		CheckedAt:  time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC),
		WindowFrom: time.Date(2026, 11, 15, 7, 30, 0, 0, time.UTC),
		Messages: []checker.Message{
			{UID: 1, Date: time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC), From: "backup@csl.by", Subject: "Ошибка", Class: "failure"},
		},
	}
	st := FromResult(res)
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded := Load(path)
	if loaded.Status != string(checker.StatusError) {
		t.Fatalf("статус = %q", loaded.Status)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Subject != "Ошибка" {
		t.Fatalf("сообщения: %+v", loaded.Messages)
	}
	if !loaded.LastCheck.Equal(res.CheckedAt) {
		t.Fatalf("время проверки = %s", loaded.LastCheck)
	}
}

func TestLoadMissingFile(t *testing.T) {
	st := Load(filepath.Join(t.TempDir(), "nope.json"))
	if st == nil || !st.LastCheck.IsZero() {
		t.Fatalf("ожидали пустое состояние, получили %+v", st)
	}
}
