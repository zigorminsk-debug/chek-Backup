package neterr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// syscallErrno builds a fake OS socket error with the given code.
func syscallErrno(code uintptr) error {
	return syscall.Errno(code)
}

func TestIsRefused(t *testing.T) {
	// 10061 is WSAECONNREFUSED, the code Windows really returns. It is not
	// equal to syscall.ECONNREFUSED, which is an invented constant.
	wsa := syscallErrno(10061)
	if !IsRefused(fmt.Errorf("dial tcp 1.2.3.4:993: %w", wsa)) {
		t.Fatal("WSAECONNREFUSED не распознан")
	}
	wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: wsa}}
	if !IsRefused(wrapped) {
		t.Fatal("WSAECONNREFUSED в net.OpError не распознан")
	}
	text := errors.New("dial tcp 37.17.55.196:993: connectex: No connection could be made because the target machine actively refused it.")
	if !IsRefused(text) {
		t.Fatal("текст про actively refused не распознан")
	}
	if IsRefused(errors.New("imapx: сервер ответил NO")) {
		t.Fatal("ложное срабатывание на прикладной ошибке")
	}
}

func TestIsTimeout(t *testing.T) {
	if !IsTimeout(fmt.Errorf("dial: %w", syscallErrno(10060))) {
		t.Fatal("WSAETIMEDOUT не распознан")
	}
	if !IsTimeout(context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded не распознан")
	}
	if !IsTimeout(errors.New("i/o timeout")) {
		t.Fatal("текст i/o timeout не распознан")
	}
	if IsTimeout(errors.New("пароль неверный")) {
		t.Fatal("ложное срабатывание")
	}
}

func TestIsNetwork(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"refused", errors.New("connectex: No connection could be made because the target machine actively refused it."), true},
		{"dns", &net.DNSError{Err: "no such host", Name: "imap.csl.by", IsNotFound: true}, true},
		{"op", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("boom")}, true},
		{"auth", errors.New("imapx: сервер отклонил вход для backup@csl.by"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsNetwork(tc.err); got != tc.want {
			t.Errorf("%s: IsNetwork = %v, хотим %v", tc.name, got, tc.want)
		}
	}
}

func TestIsResetAndUnreachable(t *testing.T) {
	if !IsReset(errors.New("read tcp: An existing connection was forcibly closed by the remote host.")) {
		t.Fatal("сброс соединения не распознан")
	}
	if !IsUnreachable(fmt.Errorf("dial: %w", syscallErrno(10051))) {
		t.Fatal("WSAENETUNREACH не распознан")
	}
}
