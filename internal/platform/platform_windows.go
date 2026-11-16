//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Severity of a system beep.
type Severity int

// Beep severities.
const (
	BeepInfo    Severity = 0x40 // MB_ICONASTERISK
	BeepWarning Severity = 0x30 // MB_ICONEXCLAMATION
	BeepError   Severity = 0x10 // MB_ICONHAND
)

const (
	hkeyCurrentUser    = 0x80000001
	keyQueryValue      = 0x0001
	keySetValue        = 0x0002
	regSZ              = 1
	errorAlreadyExists = 183
	runKeyPath         = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName = AppName
)

var (
	advapi32 = syscall.NewLazyDLL("advapi32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegCreateKeyEx  = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKeyEx    = advapi32.NewProc("RegOpenKeyExW")
	procRegSetValueEx   = advapi32.NewProc("RegSetValueExW")
	procRegQueryValueEx = advapi32.NewProc("RegQueryValueExW")
	procRegDeleteValue  = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey     = advapi32.NewProc("RegCloseKey")
	procMessageBeep     = user32.NewProc("MessageBeep")
	procCreateMutex     = kernel32.NewProc("CreateMutexW")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
)

// IsWindows reports whether the platform is Windows.
func IsWindows() bool { return true }

// SingleInstance makes sure that only one watcher runs per user session. The
// returned bool is true when another instance already owns the mutex.
func SingleInstance(name string) (bool, func(), error) {
	ptr, err := syscall.UTF16PtrFromString(`Local\` + name)
	if err != nil {
		return false, nil, err
	}
	handle, _, callErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if handle == 0 {
		return false, nil, callErr
	}
	release := func() { _, _, _ = procCloseHandle.Call(handle) }
	var errno syscall.Errno
	if errors.As(callErr, &errno) && errno == errorAlreadyExists {
		release()
		return true, func() {}, nil
	}
	return false, release, nil
}

func autostartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, linkErr := filepath.EvalSymlinks(exe); linkErr == nil {
		exe = resolved
	}
	return fmt.Sprintf(`"%s" --autostart`, exe), nil
}

// SetAutostart adds or removes the "start with Windows" registry value.
func SetAutostart(enabled bool) error {
	if !enabled {
		return clearAutostart()
	}
	command, err := autostartCommand()
	if err != nil {
		return err
	}
	pathPtr, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return err
	}
	var key syscall.Handle
	ret, _, _ := procRegCreateKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pathPtr)),
		0, 0, 0,
		uintptr(keyQueryValue|keySetValue),
		0,
		uintptr(unsafe.Pointer(&key)),
		0,
	)
	if ret != 0 {
		return fmt.Errorf("не удалось открыть раздел автозапуска (код %d)", ret)
	}
	defer func() { _, _, _ = procRegCloseKey.Call(uintptr(key)) }()

	namePtr, err := syscall.UTF16PtrFromString(autostartValueName)
	if err != nil {
		return err
	}
	data, err := syscall.UTF16FromString(command)
	if err != nil {
		return err
	}
	ret, _, _ = procRegSetValueEx.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0,
		uintptr(regSZ),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)*2),
	)
	if ret != 0 {
		return fmt.Errorf("не удалось записать автозапуск (код %d)", ret)
	}
	return nil
}

func clearAutostart() error {
	pathPtr, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return err
	}
	var key syscall.Handle
	ret, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(keyQueryValue|keySetValue),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		return nil // nothing to delete
	}
	defer func() { _, _, _ = procRegCloseKey.Call(uintptr(key)) }()
	namePtr, err := syscall.UTF16PtrFromString(autostartValueName)
	if err != nil {
		return err
	}
	_, _, _ = procRegDeleteValue.Call(uintptr(key), uintptr(unsafe.Pointer(namePtr)))
	return nil
}

// AutostartEnabled reports whether the registry value points at this binary.
func AutostartEnabled() bool {
	command, err := autostartCommand()
	if err != nil {
		return false
	}
	value, err := readAutostartValue()
	if err != nil || value == "" {
		return false
	}
	return value == command
}

func readAutostartValue() (string, error) {
	pathPtr, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return "", err
	}
	var key syscall.Handle
	ret, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(keyQueryValue),
		uintptr(unsafe.Pointer(&key)),
	)
	if ret != 0 {
		return "", fmt.Errorf("раздел автозапуска недоступен (код %d)", ret)
	}
	defer func() { _, _, _ = procRegCloseKey.Call(uintptr(key)) }()
	namePtr, err := syscall.UTF16PtrFromString(autostartValueName)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 1024)
	size := uint32(len(buf) * 2)
	ret, _, _ = procRegQueryValueEx.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0, 0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret != 0 {
		return "", fmt.Errorf("значение автозапуска не найдено (код %d)", ret)
	}
	return syscall.UTF16ToString(buf), nil
}

// Beep plays a system sound.
func Beep(severity Severity) {
	_, _, _ = procMessageBeep.Call(uintptr(severity))
}

// RevealInExplorer opens the Explorer with the given file selected.
func RevealInExplorer(path string) error {
	if path == "" {
		return errors.New("пустой путь")
	}
	cmd := exec.Command("explorer.exe", "/select,"+path)
	return cmd.Start()
}

// OpenFile opens the file with the application that is registered for it.
func OpenFile(path string) error {
	if path == "" {
		return errors.New("пустой путь")
	}
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path)
	return cmd.Start()
}

// HideConsole is a no-op placeholder; the binary is linked as a GUI
// application so no console window is created in the first place.
func HideConsole() {}
