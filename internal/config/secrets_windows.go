//go:build windows

package config

import (
	"encoding/base64"
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtect   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree      = kernel32.NewProc("LocalFree")
)

// dataBlob mirrors the Win32 DATA_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

const cryptProtectUIForbidden = 0x1

func newBlob(data []byte) *dataBlob {
	if len(data) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
}

func (b *dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func (b *dataBlob) free() {
	if b.pbData != nil {
		_, _, _ = procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
		b.pbData = nil
		b.cbData = 0
	}
}

// ProtectSecret encrypts plain with the current Windows user account (DPAPI)
// and returns a base64 representation of the ciphertext.
func ProtectSecret(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", nil
	}
	in := newBlob(plain)
	var out dataBlob
	var descr *uint16
	if s, err := syscall.UTF16PtrFromString("chek-backup"); err == nil {
		descr = s
	}
	ret, _, err := procCryptProtect.Call(
		uintptr(unsafe.Pointer(in)),
		uintptr(unsafe.Pointer(descr)),
		0, 0, 0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(plain)
	if ret == 0 {
		return "", err
	}
	defer out.free()
	return base64.StdEncoding.EncodeToString(out.bytes()), nil
}

// UnprotectSecret decrypts a value produced by ProtectSecret.
func UnprotectSecret(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	in := newBlob(raw)
	var out dataBlob
	ret, _, callErr := procCryptUnprotect.Call(
		uintptr(unsafe.Pointer(in)),
		0, 0, 0, 0,
		uintptr(cryptProtectUIForbidden),
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(raw)
	if ret == 0 {
		if callErr == nil {
			callErr = errors.New("CryptUnprotectData failed")
		}
		return nil, callErr
	}
	defer out.free()
	return out.bytes(), nil
}
