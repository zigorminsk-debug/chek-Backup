//go:build !windows

package platform

import "errors"

// Severity of a system beep.
type Severity int

// Beep severities.
const (
	BeepInfo    Severity = 0
	BeepWarning Severity = 1
	BeepError   Severity = 2
)

// IsWindows reports whether the platform is Windows.
func IsWindows() bool { return false }

// SingleInstance is a no-op outside of Windows.
func SingleInstance(name string) (bool, func(), error) {
	return false, func() {}, nil
}

// SetAutostart is a no-op outside of Windows.
func SetAutostart(enabled bool) error { return nil }

// AutostartEnabled always reports false outside of Windows.
func AutostartEnabled() bool { return false }

// Beep is a no-op outside of Windows.
func Beep(severity Severity) {}

// RevealInExplorer is not supported outside of Windows.
func RevealInExplorer(path string) error {
	return errors.New("не поддерживается на этой платформе")
}

// OpenFile is not supported outside of Windows.
func OpenFile(path string) error {
	return errors.New("не поддерживается на этой платформе")
}

// HideConsole is a no-op outside of Windows.
func HideConsole() {}
