package imapx

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// sampleHeaders is a message that a Windows backup tool would send: the
// subject is a Windows-1251 encoded word and the body is 8 bit Windows-1251.
const sampleHeaders = "From: \"Служба резервного копирования\" <backup@csl.by>\r\n" +
	"To: backup@csl.by\r\n" +
	"Subject: =?windows-1251?B?zvjo4ergIOjx7+7r7eXt6P8g4f3q4O/g?=\r\n" +
	"Date: Mon, 16 Nov 2026 03:10:00 +0300\r\n" +
	"Content-Type: text/plain; charset=windows-1251\r\n" +
	"Content-Transfer-Encoding: 8bit\r\n" +
	"\r\n"

const sampleBody = "\xc7\xe0\xe4\xe0\xed\xe8\xe5 \xe7\xe0\xe2\xe5\xf0\xf8\xe5\xed\xee \xf3\xf1\xef\xe5\xf8\xed\xee" // "Задание завершено успешно"

// fakeServer is a minimal IMAP server that answers from a canned script.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	mu       sync.Mutex
	commands []string
	body     []byte
	envelope []string
}

func newFakeServer(t *testing.T, body []byte, envelope []string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeServer{t: t, ln: ln, body: body, envelope: envelope}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() (string, int) {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	return host, port
}

func (s *fakeServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprint(conn, "* OK IMAP4rev1 Service Ready\r\n")
	br := newTestReader(conn)
	for {
		line, err := br.readLine()
		if err != nil {
			return
		}
		s.commands = append(s.commands, line)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		tag := fields[0]
		cmd := strings.ToUpper(fields[1])
		switch cmd {
		case "LOGIN":
			fmt.Fprintf(conn, "%s OK LOGIN completed\r\n", tag)
		case "EXAMINE", "SELECT":
			fmt.Fprint(conn, "* 2 EXISTS\r\n* OK [UIDVALIDITY 1] UIDs valid\r\n")
			fmt.Fprintf(conn, "%s OK [READ-ONLY] done\r\n", tag)
		case "CAPABILITY":
			fmt.Fprint(conn, "* CAPABILITY IMAP4rev1\r\n")
			fmt.Fprintf(conn, "%s OK done\r\n", tag)
		case "UID":
			if len(fields) < 3 {
				return
			}
			switch strings.ToUpper(fields[2]) {
			case "SEARCH":
				fmt.Fprint(conn, "* SEARCH 101 102\r\n")
				fmt.Fprintf(conn, "%s OK SEARCH completed\r\n", tag)
			case "FETCH":
				if strings.Contains(strings.ToUpper(line), "BODY.PEEK") {
					fmt.Fprintf(conn, "* 2 FETCH (UID 102 BODY[] {%d}\r\n", len(s.body))
					_, _ = conn.Write(s.body)
					fmt.Fprint(conn, ")\r\n")
					fmt.Fprintf(conn, "%s OK FETCH completed\r\n", tag)
					continue
				}
				for _, env := range s.envelope {
					fmt.Fprintf(conn, "* 1 FETCH (%s)\r\n", env)
				}
				fmt.Fprintf(conn, "%s OK FETCH completed\r\n", tag)
			default:
				fmt.Fprintf(conn, "%s BAD unsupported\r\n", tag)
			}
		case "LOGOUT":
			fmt.Fprint(conn, "* BYE\r\n")
			fmt.Fprintf(conn, "%s OK LOGOUT completed\r\n", tag)
			return
		default:
			fmt.Fprintf(conn, "%s OK %s completed\r\n", tag, cmd)
		}
	}
}

// testReader reads CRLF terminated lines.
type testReader struct {
	r io.Reader
}

func newTestReader(r io.Reader) *testReader { return &testReader{r: r} }

func (t *testReader) readLine() (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		if _, err := t.r.Read(buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return strings.TrimRight(sb.String(), "\r"), nil
		}
		sb.WriteByte(buf[0])
	}
}

func envelopeLine(uid int, date, subject, from, host string) string {
	return fmt.Sprintf(`UID %d INTERNALDATE "%s" ENVELOPE ("%s" "%s" (("Имя" NIL "%s" "%s")) NIL NIL NIL NIL NIL NIL NIL)`,
		uid, date, date, subject, from, host)
}

func TestHeadersAndBody(t *testing.T) {
	srv := newFakeServer(t, []byte(sampleHeaders+sampleBody+"\r\n"), []string{
		envelopeLine(101, "16-Nov-2026 03:10:00 +0300", "Backup completed", "backup", "csl.by"),
		envelopeLine(102, "16-Nov-2026 04:10:00 +0300", "=?utf-8?B?0J7RiNC40LHQutCw?=", "backup", "csl.by"),
	})
	host, port := srv.addr()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Dial(ctx, Options{
		Host:     host,
		Port:     port,
		Mode:     TLSNone,
		Username: "backup@csl.by",
		Password: `pa"ss\word`,
		Mailbox:  "INBOX",
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()

	headers, err := client.Headers(ctx, time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("получили %d заголовков, хотим 2", len(headers))
	}
	if headers[0].UID != 101 || headers[1].UID != 102 {
		t.Fatalf("неожиданные UID: %d, %d", headers[0].UID, headers[1].UID)
	}
	if headers[0].Subject != "Backup completed" {
		t.Errorf("тема первого письма: %q", headers[0].Subject)
	}
	if headers[1].Subject != "Ошибка" {
		t.Errorf("тема второго письма (utf-8 base64): %q", headers[1].Subject)
	}
	if len(headers[0].From) != 1 || !strings.Contains(headers[0].From[0], "backup@csl.by") {
		t.Errorf("отправитель: %v", headers[0].From)
	}
	wantDate := time.Date(2026, 11, 16, 3, 10, 0, 0, time.FixedZone("", 3*3600))
	if !headers[0].Date.Equal(wantDate) {
		t.Errorf("дата письма: %s", headers[0].Date)
	}

	raw, err := client.RawMessage(ctx, 102)
	if err != nil {
		t.Fatalf("RawMessage: %v", err)
	}
	if string(raw) != sampleHeaders+sampleBody+"\r\n" {
		t.Fatalf("тело письма не совпало: %q", string(raw))
	}
	if body := TextBody(raw); !strings.Contains(body, "успешно") {
		t.Errorf("текст письма в windows-1251 не декодирован: %q", body)
	}

	// The password must be quoted, the search date must use English months.
	found := false
	for _, cmd := range srv.commands {
		if strings.HasPrefix(cmd, "A0001 LOGIN") {
			found = true
			if !strings.Contains(cmd, `"pa\"ss\\word"`) {
				t.Errorf("пароль не экранирован: %s", cmd)
			}
		}
		if strings.Contains(cmd, "UID SEARCH SINCE") && !strings.Contains(cmd, "15-Nov-2026") {
			t.Errorf("дата поиска не в формате IMAP: %s", cmd)
		}
	}
	if !found {
		t.Errorf("LOGIN не выполнялся, команды: %v", srv.commands)
	}
}

func TestDialRejectsBadPassword(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "* OK ready\r\n")
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		line := string(buf[:n])
		tag := strings.SplitN(strings.TrimSpace(line), " ", 2)[0]
		fmt.Fprintf(conn, "%s NO [AUTHENTICATIONFAILED] Invalid credentials\r\n", tag)
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = Dial(ctx, Options{Host: host, Port: port, Mode: TLSNone, Username: "u", Password: "p", Timeout: 3 * time.Second})
	if err == nil {
		t.Fatal("ожидали ошибку аутентификации")
	}
	if !strings.Contains(err.Error(), "логин и пароль") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}

func TestParseFetchWithLiteralSubject(t *testing.T) {
	line := `* 3 FETCH (UID 77 INTERNALDATE "16-Nov-2026 09:30:00 +0300" ENVELOPE ("Mon, 16 Nov 2026 09:30:00 +0300" "Резервная копия не выполнена" (("" NIL "backup" "csl.by")) NIL NIL NIL NIL NIL NIL))`
	h, ok, err := parseFetch(line)
	if err != nil || !ok {
		t.Fatalf("parseFetch: ok=%v err=%v", ok, err)
	}
	if h.UID != 77 {
		t.Errorf("UID = %d", h.UID)
	}
	if h.Subject != "Резервная копия не выполнена" {
		t.Errorf("тема = %q", h.Subject)
	}
	if len(h.From) != 1 || h.From[0] != "backup@csl.by" {
		t.Errorf("отправители = %v", h.From)
	}
}

func TestParseFetchIgnoresForeignLines(t *testing.T) {
	for _, line := range []string{
		"* OK done",
		"* 3 EXISTS",
		"* SEARCH 1 2 3",
		"",
	} {
		if _, ok, err := parseFetch(line); ok || err != nil {
			t.Errorf("строка %q не должна разбираться как FETCH (ok=%v err=%v)", line, ok, err)
		}
	}
}

func TestLiteralAtEnd(t *testing.T) {
	cases := []struct {
		line string
		size int
		ok   bool
	}{
		{"* 1 FETCH (BODY[] {123}", 123, true},
		{"A0001 OK done", 0, false},
		{"* 1 FETCH (BODY[] {0}", 0, true},
		{"* 1 FETCH (X {~5}}", 0, false},
	}
	for _, c := range cases {
		size, marker, ok := literalAtEnd(c.line)
		if ok != c.ok || size != c.size {
			t.Errorf("literalAtEnd(%q) = (%d, %q, %v)", c.line, size, marker, ok)
		}
	}
}

func TestDecodeHeaderEncodings(t *testing.T) {
	cases := map[string]string{
		"=?utf-8?B?0J7RiNC40LHQutCw?=":                                  "Ошибка",
		"=?windows-1251?B?zvjo4erg?=":                                   "Ошибка",
		"=?koi8-r?B?79vJwsvB?=":                                         "Ошибка",
		"Backup completed":                                              "Backup completed",
		"=?utf-8?Q?=D0=9E=D1=88=D0=B8=D0=B1=D0=BA=D0=B0?= (job 1)":      "Ошибка (job 1)",
		"=?utf-8?B?0J7RiNC40LHQutCw?= =?utf-8?B?INCx0Y3QutCw0L/QsA==?=": "Ошибка бэкапа",
	}
	for in, want := range cases {
		if got := DecodeHeader(in); got != want {
			t.Errorf("DecodeHeader(%q) = %q, хотим %q", in, got, want)
		}
	}
}

func TestTextBodyMultipart(t *testing.T) {
	raw := strings.Join([]string{
		"From: backup@csl.by",
		"Subject: Report",
		"Content-Type: multipart/mixed; boundary=XX",
		"",
		"--XX",
		"Content-Type: multipart/alternative; boundary=YY",
		"",
		"--YY",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Бэкап завершён успешно",
		"--YY",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"<p>=D0=9E=D1=88=D0=B8=D0=B1=D0=BA=D0=B0</p>",
		"--YY--",
		"--XX",
		"Content-Type: application/octet-stream",
		"Content-Transfer-Encoding: base64",
		"",
		"AAECAwQ=",
		"--XX--",
	}, "\r\n")
	body := TextBody([]byte(raw))
	if !strings.Contains(body, "завершён успешно") {
		t.Fatalf("текст не извлечён: %q", body)
	}
	if !strings.Contains(body, "Ошибка") {
		t.Fatalf("html часть не преобразована в текст: %q", body)
	}
	if strings.Contains(body, "AAECAwQ") {
		t.Fatalf("бинарное вложение попало в текст: %q", body)
	}
}
