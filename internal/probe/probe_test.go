package probe

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// TestRunMissingHost checks that a name which does not exist is reported on the
// DNS stage with a hint about the real server name.
func TestRunMissingHost(t *testing.T) {
	rep := Run(context.Background(), Options{
		Host:    "imap.invalid.",
		Port:    993,
		Mode:    "implicit",
		Timeout: 2 * time.Second,
	})
	if rep.OK() {
		t.Fatalf("ожидалась ошибка, получено %+v", rep)
	}
	if rep.Failed != StepDNS {
		t.Fatalf("ожидался этап %q, получен %q (%s)", StepDNS, rep.Failed, rep.Reason)
	}
	if !strings.Contains(rep.Hint, "mail.csl.by") {
		t.Fatalf("подсказка не упоминает mail.csl.by: %s", rep.Hint)
	}
	if len(rep.Steps) != 1 || rep.Steps[0].OK {
		t.Fatalf("ожидался один неуспешный шаг, получено %+v", rep.Steps)
	}
}

// TestRunIPLiteralSkipsDNS checks that an IP address is used as is.
func TestRunIPLiteralSkipsDNS(t *testing.T) {
	rep := Run(context.Background(), Options{
		Host:    "127.0.0.1",
		Port:    1,
		Mode:    "implicit",
		Timeout: 1 * time.Second,
	})
	if rep.Failed != StepTCP {
		t.Fatalf("ожидалась ошибка TCP, получено %q (%s)", rep.Failed, rep.Reason)
	}
	if len(rep.Steps) != 2 || !rep.Steps[0].OK {
		t.Fatalf("DNS-шаг должен быть успешным: %+v", rep.Steps)
	}
	if !strings.Contains(rep.Reason, "отклонено") {
		t.Fatalf("неожиданная причина: %s", rep.Reason)
	}
}

// TestRunAgainstLocalIMAP starts a tiny fake server that answers with an IMAP
// greeting and verifies that the probe reaches the IMAP stage.
func TestRunAgainstLocalIMAP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("нет свободного порта: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = conn.Write([]byte("* OK fake IMAP4rev1 ready\r\n"))
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	rep := Run(context.Background(), Options{
		Host:    "127.0.0.1",
		Port:    port,
		Mode:    "none",
		Timeout: 2 * time.Second,
	})
	if !rep.OK() {
		t.Fatalf("ожидался успех, получено %s / %s", rep.Reason, rep.Text())
	}
	if !strings.Contains(rep.Text(), "fake IMAP4rev1") {
		t.Fatalf("приглашение не попало в отчёт: %s", rep.Text())
	}
}

// TestTextNotEmpty makes sure the report is renderable even on failures.
func TestTextNotEmpty(t *testing.T) {
	rep := Run(context.Background(), Options{Host: "", Timeout: time.Second})
	if rep.Text() == "" {
		t.Fatal("пустой отчёт")
	}
	if rep.Hint == "" {
		t.Fatal("нет подсказки для пустого адреса")
	}
}
