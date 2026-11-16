// Package platform wraps the few Windows specific bits the watcher needs:
// autostart registration, a single instance guard and small shell helpers.
// On other operating systems the functions degrade to no-ops so that the
// logic packages stay testable everywhere.
package platform

import (
	"os"
	"path/filepath"
)

// AppName is used for the data directory, the mutex and the autostart value.
const AppName = "ChekBackup"

// DataDir returns the per-user directory that holds the configuration, the
// log and the state file. On Windows this is %APPDATA%\ChekBackup.
func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, AppName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ConfigPath is the location of config.json inside the data directory.
func ConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LogPath is the location of the log file inside the data directory.
func LogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chek-backup.log"), nil
}

// StatePath is the location of the state file inside the data directory.
func StatePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}
