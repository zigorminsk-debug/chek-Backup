//go:build !windows

package config

import "encoding/base64"

// ProtectSecret encodes a secret with a reversible transformation so that the
// configuration layout stays the same on every platform. Windows uses the
// DPAPI, see secrets_windows.go.
func ProtectSecret(plain []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(plain), nil
}

// UnprotectSecret decodes a value produced by ProtectSecret.
func UnprotectSecret(encoded string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encoded)
}
