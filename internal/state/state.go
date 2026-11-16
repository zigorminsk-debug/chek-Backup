// Package state stores the outcome of the last check so that the tray icon
// and the status window can show it after a restart.
package state

import (
	"encoding/json"
	"os"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/checker"
)

// Message is a persisted, human readable message summary.
type Message struct {
	Date    time.Time `json:"date"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Class   string    `json:"class"`
}

// State is the persisted view of the last check.
type State struct {
	LastCheck  time.Time `json:"lastCheck"`
	WindowFrom time.Time `json:"windowFrom"`
	NextCheck  time.Time `json:"nextCheck"`
	Status     string    `json:"status"`
	Title      string    `json:"title"`
	Summary    string    `json:"summary"`
	Messages   []Message `json:"messages,omitempty"`
}

// Load reads the state file. A missing or corrupt file yields an empty state
// and no error, because the state is only informational.
func Load(path string) *State {
	data, err := os.ReadFile(path)
	if err != nil {
		return &State{}
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{}
	}
	return &s
}

// Save writes the state file, ignoring failures as it is not critical.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// FromResult converts a checker result into a persisted state.
func FromResult(res checker.Result) *State {
	s := &State{
		LastCheck:  res.CheckedAt,
		WindowFrom: res.WindowFrom,
		Status:     string(res.Status),
		Title:      res.Title,
		Summary:    res.Summary,
	}
	for _, m := range res.Messages {
		s.Messages = append(s.Messages, Message{
			Date:    m.Date,
			From:    m.From,
			Subject: m.Subject,
			Class:   m.Class,
		})
	}
	return s
}
