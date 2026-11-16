// Package neterr classifies low level network errors in a portable way.
//
// On Windows the socket layer reports WSA* codes (for example 10061 for a
// refused connection), while the constants of package syscall such as
// syscall.ECONNREFUSED are "invented" values that never equal them: neither
// errors.Is nor a direct comparison matches. The package therefore checks both
// the invented constants and the real WSA numbers, and falls back to the
// message text, which is what the user sees in the log and in the settings
// window.
package neterr

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// Windows socket error codes. They are declared here as plain numbers because
// package syscall only exposes WSAECONNRESET and WSAECONNABORTED.
const (
	wsaECONNREFUSED syscall.Errno = 10061
	wsaETIMEDOUT    syscall.Errno = 10060
	wsaECONNRESET   syscall.Errno = 10054
	wsaEHOSTUNREACH syscall.Errno = 10065
	wsaENETUNREACH  syscall.Errno = 10051
	wsaENETDOWN     syscall.Errno = 10050
)

func errnoOf(err error) (syscall.Errno, bool) {
	var e syscall.Errno
	if errors.As(err, &e) {
		return e, true
	}
	return 0, false
}

func hasAny(err error, codes ...syscall.Errno) bool {
	if err == nil {
		return false
	}
	if e, ok := errnoOf(err); ok {
		for _, c := range codes {
			if e == c {
				return true
			}
		}
	}
	for _, c := range codes {
		if errors.Is(err, c) {
			return true
		}
	}
	return false
}

func mentions(err error, needles ...string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, n := range needles {
		if strings.Contains(msg, n) {
			return true
		}
	}
	return false
}

// IsRefused reports whether the peer or the network actively rejected the
// connection (RST), which is what Windows prints as "No connection could be
// made because the target machine actively refused it".
func IsRefused(err error) bool {
	return hasAny(err, syscall.ECONNREFUSED, wsaECONNREFUSED) ||
		mentions(err, "actively refused", "connection refused", "refused the connection", "отказано в соединении")
}

// IsTimeout reports whether the connection attempt ran out of time.
func IsTimeout(err error) bool {
	return hasAny(err, syscall.ETIMEDOUT, wsaETIMEDOUT) ||
		errors.Is(err, context.DeadlineExceeded) ||
		mentions(err, "i/o timeout", "timed out", "timeout")
}

// IsReset reports whether the connection was closed abruptly by the other side.
func IsReset(err error) bool {
	return hasAny(err, syscall.ECONNRESET, syscall.ECONNABORTED, wsaECONNRESET) ||
		mentions(err, "connection reset", "forcibly closed", "разорвано")
}

// IsUnreachable reports a missing route to the host or the network.
func IsUnreachable(err error) bool {
	return hasAny(err, syscall.EHOSTUNREACH, syscall.ENETUNREACH, wsaEHOSTUNREACH, wsaENETUNREACH, wsaENETDOWN) ||
		mentions(err, "host unreachable", "network is unreachable", "no route to host")
}

// IsDNS reports a name resolution error.
func IsDNS(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// IsEOF reports a closed connection.
func IsEOF(err error) bool {
	return errors.Is(err, io.EOF)
}

// IsNetwork reports whether err looks like a socket level failure, that is
// something a step by step connectivity probe is able to explain. Application
// level errors (a rejected login, a missing password) return false.
func IsNetwork(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	var sysErr *os.SyscallError
	var dnsErr *net.DNSError
	if errors.As(err, &opErr) || errors.As(err, &sysErr) || errors.As(err, &dnsErr) {
		return true
	}
	if IsRefused(err) || IsTimeout(err) || IsReset(err) || IsUnreachable(err) || IsEOF(err) {
		return true
	}
	return mentions(err, "dial tcp", "connectex", "no such host", "tls handshake",
		"does not look like a tls", "certificate", "сертификат", "не ответил", "соединени")
}
