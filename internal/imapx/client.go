// Package imapx implements the small subset of IMAP4rev1 that is needed to
// look for backup report messages in a mailbox. It only depends on the Go
// standard library: a TLS connection, a LOGIN, an EXAMINE, a UID SEARCH and a
// few UID FETCH commands.
package imapx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultHost is only used to build a helpful message when a name cannot be
// resolved; it is not a fallback for the configuration.
const defaultHost = "mail.csl.by"

// TLSMode describes how the connection is protected.
type TLSMode string

// Supported TLS modes.
const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
	TLSNone     TLSMode = "none"
)

// Options are the connection settings.
type Options struct {
	Host               string
	Port               int
	Mode               TLSMode
	Username           string
	Password           string
	Mailbox            string
	Timeout            time.Duration
	InsecureSkipVerify bool
	// MaxLiteralBytes caps how much of a single message body is downloaded.
	MaxLiteralBytes int
}

// Header is the metadata of one mailbox message.
type Header struct {
	UID     uint32
	Date    time.Time
	From    []string
	Subject string
}

// Error is a negative IMAP response (NO or BAD).
type Error struct {
	Command string
	Status  string
	Message string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("imapx: %s: сервер ответил %s", e.Command, e.Status)
	}
	return fmt.Sprintf("imapx: %s: %s %s", e.Command, e.Status, e.Message)
}

// Client is a connected IMAP session.
type Client struct {
	conn    net.Conn
	r       *bufio.Reader
	w       *bufio.Writer
	tag     int
	timeout time.Duration
	maxLit  int
	mailbox string
}

// Dial connects, authenticates and selects the configured mailbox in read
// only mode so that the messages stay unread.
func Dial(ctx context.Context, opt Options) (*Client, error) {
	if opt.Host == "" {
		return nil, errors.New("imapx: не задан сервер")
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 60 * time.Second
	}
	port := opt.Port
	if port == 0 {
		if opt.Mode == TLSStartTLS || opt.Mode == TLSNone {
			port = 143
		} else {
			port = 993
		}
	}
	if opt.Mailbox == "" {
		opt.Mailbox = "INBOX"
	}
	if opt.MaxLiteralBytes <= 0 {
		opt.MaxLiteralBytes = 4 << 20
	}
	addr := net.JoinHostPort(opt.Host, strconv.Itoa(port))
	tlsConfig := &tls.Config{
		ServerName:         opt.Host,
		InsecureSkipVerify: opt.InsecureSkipVerify, //nolint:gosec // opt-in for self signed mail servers
		MinVersion:         tls.VersionTLS12,
	}
	dialer := &net.Dialer{Timeout: opt.Timeout}
	var conn net.Conn
	var err error
	if opt.Mode == TLSImplicit {
		td := &tls.Dialer{NetDialer: dialer, Config: tlsConfig}
		conn, err = td.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, dialError(opt, addr, err)
	}
	c := &Client{
		conn:    conn,
		r:       bufio.NewReaderSize(conn, 32*1024),
		w:       bufio.NewWriterSize(conn, 8*1024),
		timeout: opt.Timeout,
		maxLit:  opt.MaxLiteralBytes,
		mailbox: opt.Mailbox,
	}
	c.deadline(ctx)
	if err := c.greeting(); err != nil {
		_ = conn.Close()
		return nil, greetingError(opt, port, err)
	}
	if opt.Mode == TLSStartTLS {
		if err := c.startTLS(ctx, tlsConfig); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imapx: не удалось перейти на шифрование (STARTTLS) после подключения к %s: %w. Проверьте режим шифрования и порт (для STARTTLS обычно 143).", addr, err)
		}
	}
	if _, err := c.command(ctx, "LOGIN %s %s", quote(opt.Username), quote(opt.Password)); err != nil {
		_ = conn.Close()
		var imapErr *Error
		if errors.As(err, &imapErr) {
			return nil, fmt.Errorf("imapx: сервер отклонил вход для %s (%s %s): проверьте логин и пароль", opt.Username, imapErr.Status, imapErr.Message)
		}
		return nil, err
	}
	if _, err := c.command(ctx, "EXAMINE %s", quote(opt.Mailbox)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imapx: не удалось открыть ящик %q: %w", opt.Mailbox, err)
	}
	return c, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = c.command(ctx, "LOGOUT")
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *Client) deadline(ctx context.Context) {
	dl := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.conn.SetDeadline(dl)
}

func (c *Client) greeting() error {
	line, err := c.readLine()
	if err != nil {
		return fmt.Errorf("imapx: сервер не ответил: %w", err)
	}
	upper := strings.ToUpper(line)
	if strings.HasPrefix(upper, "* OK") || strings.HasPrefix(upper, "* PREAUTH") {
		return nil
	}
	if strings.HasPrefix(upper, "* BYE") {
		return fmt.Errorf("imapx: сервер закрыл соединение: %s", strings.TrimSpace(line))
	}
	return fmt.Errorf("imapx: неожиданный ответ сервера: %s", strings.TrimSpace(line))
}

func (c *Client) startTLS(ctx context.Context, cfg *tls.Config) error {
	if _, err := c.command(ctx, "STARTTLS"); err != nil {
		return fmt.Errorf("imapx: сервер не поддерживает STARTTLS: %w", err)
	}
	tlsConn := tls.Client(c.conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("imapx: ошибка TLS-рукопожатия: %w", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReaderSize(tlsConn, 32*1024)
	c.w = bufio.NewWriterSize(tlsConn, 8*1024)
	return nil
}

// Headers returns the headers of every message that is newer than since,
// ordered by UID.
func (c *Client) Headers(ctx context.Context, since time.Time) ([]Header, error) {
	c.deadline(ctx)
	// IMAP always uses English month names, independent of the locale.
	date := since.Format("2-Jan-2006")
	lines, err := c.command(ctx, "UID SEARCH SINCE %s", date)
	if err != nil {
		return nil, err
	}
	uids := parseSearch(lines)
	if len(uids) == 0 {
		return nil, nil
	}
	var out []Header
	const chunk = 100
	for start := 0; start < len(uids); start += chunk {
		end := start + chunk
		if end > len(uids) {
			end = len(uids)
		}
		set := joinUIDs(uids[start:end])
		c.deadline(ctx)
		lines, err := c.command(ctx, "UID FETCH %s (UID INTERNALDATE ENVELOPE)", set)
		if err != nil {
			return out, err
		}
		for _, line := range lines {
			h, ok, parseErr := parseFetch(line.text)
			if parseErr != nil || !ok {
				continue
			}
			out = append(out, h)
		}
	}
	return out, nil
}

// RawMessage downloads the complete RFC 822 message with the given UID. The
// message is fetched with BODY.PEEK[] so its \Seen flag is not changed.
func (c *Client) RawMessage(ctx context.Context, uid uint32) ([]byte, error) {
	c.deadline(ctx)
	lines, err := c.command(ctx, "UID FETCH %d (BODY.PEEK[])", uid)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if len(line.lits) > 0 {
			return line.lits[0], nil
		}
	}
	return nil, fmt.Errorf("imapx: сервер не вернул тело письма UID %d", uid)
}

func parseSearch(lines []respLine) []uint32 {
	var uids []uint32
	for _, line := range lines {
		text := strings.TrimSpace(line.text)
		if !strings.HasPrefix(strings.ToUpper(text), "* SEARCH") {
			continue
		}
		for _, f := range strings.Fields(text)[2:] {
			v, err := strconv.ParseUint(f, 10, 32)
			if err != nil {
				continue
			}
			uids = append(uids, uint32(v))
		}
	}
	return uids
}

func joinUIDs(uids []uint32) string {
	var sb strings.Builder
	for i, uid := range uids {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatUint(uint64(uid), 10))
	}
	return sb.String()
}

// respLine is one logical server line together with the literals it carried.
type respLine struct {
	text string
	lits [][]byte
}

func (c *Client) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readRespLine reads one response line, consuming literal payloads
// ("{123}\r\n...") that may be embedded in it.
func (c *Client) readRespLine() (respLine, error) {
	var rl respLine
	var sb bytes.Buffer
	for {
		line, err := c.readLine()
		if err != nil {
			return rl, err
		}
		sb.WriteString(line)
		size, marker, ok := literalAtEnd(line)
		if !ok {
			rl.text = sb.String()
			return rl, nil
		}
		sb.Truncate(sb.Len() - len(marker))
		lit, err := c.readLiteral(size)
		if err != nil {
			return rl, err
		}
		rl.lits = append(rl.lits, lit)
		// Small literals are header values (for example an encoded Subject),
		// so they are spliced back into the text as a quoted string to keep
		// the S-expression structure intact. Large literals are message
		// bodies and are only kept in lits.
		if size <= maxInlineLiteral {
			sb.WriteString(quoteBytes(lit))
		} else {
			sb.WriteString(`""`)
		}
	}
}

// maxInlineLiteral is the largest literal that is spliced back into the
// response text. Anything bigger is treated as a message body.
const maxInlineLiteral = 64 << 10

// quoteBytes renders arbitrary bytes as an IMAP quoted string.
func quoteBytes(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + 2)
	sb.WriteByte('"')
	for _, ch := range b {
		if ch == '"' || ch == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteByte(ch)
	}
	sb.WriteByte('"')
	return sb.String()
}

func (c *Client) readLiteral(size int) ([]byte, error) {
	limit := size
	if c.maxLit > 0 && limit > c.maxLit {
		limit = c.maxLit
	}
	buf := make([]byte, limit)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	if size > limit {
		if _, err := io.CopyN(io.Discard, c.r, int64(size-limit)); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// literalAtEnd reports whether line ends with an IMAP literal marker such as
// "{1024}" and returns its size together with the marker text.
func literalAtEnd(line string) (int, string, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, "", false
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return 0, "", false
	}
	body := line[open+1 : len(line)-1]
	// A trailing "+" marks a non synchronising literal.
	body = strings.TrimSuffix(body, "+")
	size, err := strconv.Atoi(strings.TrimSpace(body))
	if err != nil || size < 0 {
		return 0, "", false
	}
	return size, line[open:], true
}

func (c *Client) command(ctx context.Context, format string, args ...any) ([]respLine, error) {
	if c.conn == nil {
		return nil, errors.New("imapx: соединение закрыто")
	}
	c.tag++
	tag := fmt.Sprintf("A%04d", c.tag)
	cmd := strings.TrimSpace(fmt.Sprintf(format, args...))
	c.deadline(ctx)
	if _, err := c.w.WriteString(tag + " " + cmd + "\r\n"); err != nil {
		return nil, fmt.Errorf("imapx: ошибка отправки команды: %w", err)
	}
	if err := c.w.Flush(); err != nil {
		return nil, fmt.Errorf("imapx: ошибка отправки команды: %w", err)
	}
	var out []respLine
	for {
		line, err := c.readRespLine()
		if err != nil {
			return out, fmt.Errorf("imapx: ошибка чтения ответа: %w", err)
		}
		text := line.text
		if strings.HasPrefix(text, "+") {
			// Server asks for more input; nothing to send for the commands
			// used here, so just skip the continuation request.
			continue
		}
		if strings.HasPrefix(text, tag+" ") {
			status := ""
			rest := strings.TrimPrefix(text, tag+" ")
			if sp := strings.IndexByte(rest, ' '); sp >= 0 {
				status = rest[:sp]
				rest = rest[sp+1:]
			} else {
				status = rest
				rest = ""
			}
			if strings.EqualFold(status, "OK") {
				return out, nil
			}
			return out, &Error{Command: firstWord(cmd), Status: strings.ToUpper(status), Message: strings.TrimSpace(rest)}
		}
		out = append(out, line)
	}
}

func firstWord(s string) string {
	if sp := strings.IndexByte(s, ' '); sp >= 0 {
		return s[:sp]
	}
	return s
}

// quote renders a value as an IMAP quoted string.
func quote(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
	}
	sb.WriteByte('"')
	return sb.String()
}

// dialError turns a network error into a message that names the stage that
// failed and lists the most probable causes for the given endpoint.
func dialError(opt Options, addr string, err error) error {
	port := opt.Port
	if port == 0 {
		if opt.Mode == TLSStartTLS || opt.Mode == TLSNone {
			port = 143
		} else {
			port = 993
		}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("imapx: %s (%s) отказал в соединении на порту %d (RST). "+
			"Вероятные причины: доступ к IMAP для вашей сети запрещён на сервере (Kerio Connect: "+
			"Services → IMAP → Access, а также Security → Intrusion Prevention — заблокированные IP); "+
			"порт закрыт файрволом; либо сервер IMAP слушает другой порт (SSL/TLS обычно 993, STARTTLS — 143). "+
			"Проверить по шагам можно кнопкой «Диагностика» в настройках", addr, opt.Host, port)
	}
	if errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("imapx: %s: порт %d не ответил за отведённое время — соединение фильтруется "+
			"(файрвол, антивирус или шлюз). Проверьте сеть и правила доступа", addr, port)
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return fmt.Errorf("imapx: %s: сеть или сервер недоступны (нет маршрута). Проверьте подключение к сети", addr)
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("imapx: %s: сервер разорвал соединение при подключении. "+
			"Часто это блокировка IP после нескольких неудачных попыток входа "+
			"(Kerio Connect → Security → Intrusion Prevention) либо запрет IMAP по адресу клиента", addr)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("imapx: имя сервера %q не найдено в DNS. Укажите существующее имя, "+
			"например %s", opt.Host, defaultHost)
	}
	return fmt.Errorf("imapx: не удалось подключиться к %s: %w", addr, err)
}

// greetingError explains a failure that happened after the TCP (or TLS)
// connection was established: the server accepted the socket but did not send
// an IMAP greeting.
func greetingError(opt Options, port int, err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("imapx: %s:%d принял соединение, но сразу закрыл его, не прислав приветствие IMAP. "+
			"Обычно это блокировка IP или запрет доступа к почтовым протоколам для вашей сети на стороне сервера "+
			"(Kerio Connect: Security → Intrusion Prevention, Services → IMAP → Access); "+
			"реже — неверный режим шифрования (для 993 нужен SSL/TLS, для 143 — STARTTLS)", opt.Host, port)
	}
	return fmt.Errorf("imapx: сервер %s:%d не ответил как IMAP: %w. Проверьте порт и режим шифрования", opt.Host, port, err)
}
