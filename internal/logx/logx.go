// Package logx implements a small, dependency free file logger with size
// based rotation. It is safe for concurrent use and every method is a no-op
// on a nil *Logger, which keeps call sites free of nil checks.
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level is a log severity.
type Level int

// Log levels ordered from the most to the least verbose.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the human readable level name.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "LOG"
	}
}

// ParseLevel converts a textual level into a Level. Unknown values map to
// LevelInfo.
func ParseLevel(s string) Level {
	switch s {
	case "debug", "DEBUG", "Debug":
		return LevelDebug
	case "warn", "warning", "WARN", "WARNING":
		return LevelWarn
	case "error", "ERROR", "Error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger writes timestamped lines to a file. The zero value is usable and
// discards everything.
type Logger struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	min     Level
	file    *os.File
	size    int64
	alsoOut bool
}

// Open creates (or appends to) the log file at path. maxSize is the size in
// bytes at which the file is rotated to "<name>.1".
func Open(path string, maxSize int64) (*Logger, error) {
	if maxSize <= 0 {
		maxSize = 5 << 20
	}
	l := &Logger{path: path, maxSize: maxSize, min: LevelInfo}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

// Nop returns a logger that discards every record but can be used safely.
func Nop() *Logger { return &Logger{min: LevelDebug} }

// SetStdout makes the logger mirror its records to stdout, which is handy when
// the binary is started from a console.
func (l *Logger) SetStdout(on bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.alsoOut = on
	l.mu.Unlock()
}

// SetLevel sets the minimum level that is written.
func (l *Logger) SetLevel(level Level) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.min = level
	l.mu.Unlock()
}

// SetMaxSize changes the rotation threshold without reopening the file.
func (l *Logger) SetMaxSize(size int64) {
	if l == nil || size <= 0 {
		return
	}
	l.mu.Lock()
	l.maxSize = size
	l.mu.Unlock()
}

// Path returns the log file location.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Logger) openLocked() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.file = f
	l.size = st.Size()
	return nil
}

func (l *Logger) rotateLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	_ = os.Rename(l.path, l.path+".1")
	_ = l.openLocked()
}

// Debugf logs at debug level.
func (l *Logger) Debugf(format string, args ...any) { l.logf(LevelDebug, format, args...) }

// Infof logs at info level.
func (l *Logger) Infof(format string, args ...any) { l.logf(LevelInfo, format, args...) }

// Warnf logs at warning level.
func (l *Logger) Warnf(format string, args ...any) { l.logf(LevelWarn, format, args...) }

// Errorf logs at error level.
func (l *Logger) Errorf(format string, args ...any) { l.logf(LevelError, format, args...) }

func (l *Logger) logf(level Level, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if level < l.min {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), level, msg)
	if l.alsoOut {
		_, _ = os.Stdout.WriteString(line)
	}
	if l.file == nil {
		if err := l.openLocked(); err != nil {
			return
		}
	}
	if l.maxSize > 0 && l.size+int64(len(line)) > l.maxSize {
		l.rotateLocked()
	}
	n, err := l.file.WriteString(line)
	if err != nil {
		return
	}
	l.size += int64(n)
}

// Close flushes and closes the underlying file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
