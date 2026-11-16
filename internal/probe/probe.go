// Package probe runs a step by step diagnosis of an IMAP connection: name
// resolution, TCP connect, TLS handshake and finally the IMAP login. It turns
// the raw Go network errors into short Russian explanations together with a
// hint, so the user knows what to fix (DNS, firewall, server side access list)
// instead of reading "dial tcp ... connectex: No connection could be made".
package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/neterr"
)

// StepKind identifies a diagnosis stage.
type StepKind string

// Possible stages of the diagnosis.
const (
	StepDNS  StepKind = "dns"  // name resolution
	StepTCP  StepKind = "tcp"  // TCP connect
	StepTLS  StepKind = "tls"  // TLS handshake
	StepIMAP StepKind = "imap" // IMAP greeting
)

// Titles of the stages as they are shown to the user.
func (k StepKind) Title() string {
	switch k {
	case StepDNS:
		return "DNS"
	case StepTCP:
		return "TCP-порт"
	case StepTLS:
		return "TLS"
	case StepIMAP:
		return "IMAP"
	default:
		return "Проверка"
	}
}

// Step is the outcome of a single diagnosis stage.
type Step struct {
	Kind    StepKind
	OK      bool
	Detail  string
	Elapsed time.Duration
}

// Report is the complete outcome of a diagnosis.
type Report struct {
	Host     string
	Port     int
	Mode     string // "SSL/TLS", "STARTTLS" or "без шифрования"
	Steps    []Step
	Failed   StepKind
	Reason   string // short Russian explanation of the failure
	Hint     string // what to do next
	IPs      []string
	Addr     string // "host:port" or "host:port → ip:port"
	Duration time.Duration
}

// OK reports whether every stage succeeded.
func (r *Report) OK() bool {
	return r != nil && r.Failed == "" && r.Reason == ""
}

// Text renders the report as a human readable block for the text widget.
func (r *Report) Text() string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Сервер: %s, порт %d, шифрование: %s\n", r.Host, r.Port, r.Mode)
	if len(r.IPs) > 0 {
		fmt.Fprintf(&sb, "Адрес: %s (IP: %s)\n", r.Addr, strings.Join(r.IPs, ", "))
	} else {
		fmt.Fprintf(&sb, "Адрес: %s\n", r.Addr)
	}
	fmt.Fprintf(&sb, "Время: %s\n\n", r.Duration.Round(10*time.Millisecond))
	for _, st := range r.Steps {
		mark := "OK  "
		if !st.OK {
			mark = "ОШИБ"
		}
		fmt.Fprintf(&sb, "[%s] %-12s %s\n", mark, st.Kind.Title(), st.Detail)
	}
	if r.Reason != "" {
		sb.WriteString("\n" + r.Reason + "\n")
	}
	if r.Hint != "" {
		sb.WriteString("\nЧто делать: " + r.Hint + "\n")
	}
	return sb.String()
}

// Options describe the endpoint that has to be checked.
type Options struct {
	Host               string
	Port               int
	Mode               string // "implicit", "starttls", "none" (config.TLSMode values)
	Timeout            time.Duration
	InsecureSkipVerify bool
}

// Run resolves the name, opens a TCP connection, performs the TLS handshake and
// (when Mode is not "none") waits for the IMAP greeting. It never logs in, so it
// is safe to call with a wrong password.
func Run(ctx context.Context, opt Options) *Report {
	started := time.Now()
	host := strings.TrimSpace(opt.Host)
	mode := opt.Mode
	if mode == "" {
		mode = "implicit"
	}
	port := opt.Port
	if port <= 0 {
		if mode == "starttls" || mode == "none" {
			port = 143
		} else {
			port = 993
		}
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if timeout > 20*time.Second {
		timeout = 20 * time.Second
	}

	rep := &Report{Host: host, Port: port, Mode: modeLabel(mode)}
	if host == "" {
		rep.Failed = StepDNS
		rep.Reason = "Не задан адрес IMAP-сервера."
		rep.Hint = "Укажите сервер в настройках, например mail.csl.by."
		return rep
	}

	// Stage 1: name resolution. An IP literal is accepted as is.
	stepStart := time.Now()
	ips, err := resolve(ctx, host, timeout)
	rep.IPs = ips
	if len(ips) > 0 {
		rep.Addr = net.JoinHostPort(host, strconv.Itoa(port))
		if ips[0] != host {
			rep.Addr = fmt.Sprintf("%s → %s", net.JoinHostPort(host, strconv.Itoa(port)),
				net.JoinHostPort(ips[0], strconv.Itoa(port)))
		}
	} else {
		rep.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if err != nil {
		rep.Steps = append(rep.Steps, Step{Kind: StepDNS, Detail: err.Error(), Elapsed: time.Since(stepStart)})
		rep.Failed = StepDNS
		rep.Reason = fmt.Sprintf("Имя «%s» не удалось разрешить в IP-адрес.", host)
		rep.Hint = "Проверьте имя сервера в настройках и работу DNS. Для csl.by правильное имя — mail.csl.by (имени imap.csl.by не существует)."
		rep.Duration = time.Since(started)
		return rep
	}
	rep.Steps = append(rep.Steps, Step{Kind: StepDNS, OK: true,
		Detail: strings.Join(ips, ", "), Elapsed: time.Since(stepStart)})

	// Stage 2: TCP connect to the first resolved address.
	stepStart = time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	target := net.JoinHostPort(ips[0], strconv.Itoa(port))
	rawConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		kind, text := explain(err, port, mode, "tcp")
		rep.Steps = append(rep.Steps, Step{Kind: StepTCP, Detail: text, Elapsed: time.Since(stepStart)})
		rep.Failed = StepTCP
		rep.Reason = kind
		rep.Hint = tcpHint(err, port, mode)
		rep.Duration = time.Since(started)
		return rep
	}
	rep.Steps = append(rep.Steps, Step{Kind: StepTCP, OK: true, Detail: "соединение установлено",
		Elapsed: time.Since(stepStart)})

	if mode == "none" {
		// Plain IMAP: only the greeting can be checked.
		stepStart = time.Now()
		text, err := readGreeting(rawConn, timeout)
		_ = rawConn.Close()
		if err != nil {
			kind, msg := explain(err, port, mode, "greeting")
			rep.Steps = append(rep.Steps, Step{Kind: StepIMAP, Detail: msg, Elapsed: time.Since(stepStart)})
			rep.Failed = StepIMAP
			rep.Reason = kind
			rep.Hint = "Возможно, сервер требует шифрование: включите SSL/TLS (порт 993) или STARTTLS (порт 143)."
		} else {
			rep.Steps = append(rep.Steps, Step{Kind: StepIMAP, OK: true, Detail: firstLine(text),
				Elapsed: time.Since(stepStart)})
		}
		rep.Duration = time.Since(started)
		return rep
	}

	tlsConfig := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: opt.InsecureSkipVerify, //nolint:gosec // opt-in for self signed mail servers
		MinVersion:         tls.VersionTLS12,
	}
	conn := rawConn
	if mode == "implicit" {
		stepStart = time.Now()
		tlsConn := tls.Client(rawConn, tlsConfig)
		hsCtx, cancel := context.WithTimeout(ctx, timeout)
		err = tlsConn.HandshakeContext(hsCtx)
		cancel()
		if err != nil {
			_ = rawConn.Close()
			kind, msg := explain(err, port, mode, "tls")
			rep.Steps = append(rep.Steps, Step{Kind: StepTLS, Detail: msg, Elapsed: time.Since(stepStart)})
			rep.Failed = StepTLS
			rep.Reason = kind
			rep.Hint = "Если сервер использует самоподписанный сертификат или вы подключаетесь по IP, включите «Не проверять сертификат сервера»."
			rep.Duration = time.Since(started)
			return rep
		}
		state := tlsConn.ConnectionState()
		rep.Steps = append(rep.Steps, Step{Kind: StepTLS, OK: true,
			Detail:  fmt.Sprintf("%s, сертификат на %s", tls.VersionName(state.Version), certName(state)),
			Elapsed: time.Since(stepStart)})
		conn = tlsConn
	} else {
		// STARTTLS: the greeting must arrive in clear text first.
		stepStart = time.Now()
		greeting, err := readGreeting(rawConn, timeout)
		if err != nil {
			_ = rawConn.Close()
			kind, msg := explain(err, port, mode, "greeting")
			rep.Steps = append(rep.Steps, Step{Kind: StepIMAP, Detail: msg, Elapsed: time.Since(stepStart)})
			rep.Failed = StepIMAP
			rep.Reason = kind
			rep.Hint = "Для этого порта может требоваться сразу TLS: выберите «SSL/TLS» и порт 993."
			rep.Duration = time.Since(started)
			return rep
		}
		rep.Steps = append(rep.Steps, Step{Kind: StepTLS, OK: true,
			Detail:  "приглашение получено " + firstLine(greeting) + ", STARTTLS выполнит приложение",
			Elapsed: time.Since(stepStart)})
		_ = conn.Close()
		rep.Duration = time.Since(started)
		return rep
	}

	// Stage 4: the IMAP greeting proves that the endpoint really speaks IMAP.
	stepStart = time.Now()
	text, err := readGreeting(conn, timeout)
	_ = conn.Close()
	if err != nil {
		kind, msg := explain(err, port, mode, "greeting")
		rep.Steps = append(rep.Steps, Step{Kind: StepIMAP, Detail: msg, Elapsed: time.Since(stepStart)})
		rep.Failed = StepIMAP
		rep.Reason = kind
		rep.Hint = "Проверьте порт и режим шифрования: для IMAP обычно 993 + SSL/TLS или 143 + STARTTLS."
		rep.Duration = time.Since(started)
		return rep
	}
	rep.Steps = append(rep.Steps, Step{Kind: StepIMAP, OK: true, Detail: firstLine(text),
		Elapsed: time.Since(stepStart)})
	rep.Duration = time.Since(started)
	return rep
}

func modeLabel(mode string) string {
	switch mode {
	case "starttls":
		return "STARTTLS"
	case "none":
		return "без шифрования"
	default:
		return "SSL/TLS"
	}
}

func resolve(ctx context.Context, host string, timeout time.Duration) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		if de := (*net.DNSError)(nil); errors.As(err, &de) && de.IsNotFound {
			return nil, fmt.Errorf("имя не найдено (NXDOMAIN)")
		}
		return nil, fmt.Errorf("ошибка DNS: %w", err)
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP.String())
	}
	if len(out) == 0 {
		return nil, errors.New("DNS не вернул ни одного адреса")
	}
	return out, nil
}

func readGreeting(conn net.Conn, timeout time.Duration) (string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func firstLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return "«" + strings.TrimSpace(s) + "»"
}

func certName(state tls.ConnectionState) string {
	if len(state.PeerCertificates) == 0 {
		return "неизвестен"
	}
	c := state.PeerCertificates[0]
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	return "неизвестен"
}

// explain converts a network error into a short Russian reason and a technical
// detail line.
func explain(err error, port int, mode, stage string) (reason, detail string) {
	switch {
	case neterr.IsRefused(err):
		return fmt.Sprintf("Соединение с портом %d отклонено (RST).", port),
			"порт закрыт: сервер или сеть ответили отказом в соединении"
	case neterr.IsTimeout(err):
		return fmt.Sprintf("Порт %d не ответил за отведённое время.", port),
			"таймаут: пакеты уходят, но ответ не приходит (фильтрация на пути)"
	case neterr.IsUnreachable(err):
		return "Сеть или хост недоступны.", "нет маршрута до сервера"
	case neterr.IsReset(err):
		return "Соединение разорвано удалённой стороной.", "сервер сбросил соединение"
	}
	var unknownAuthority *tls.CertificateVerificationError
	if errors.As(err, &unknownAuthority) {
		return "Сертификат сервера не прошёл проверку.", "неизвестный или самоподписанный сертификат"
	}
	var recErr *tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return "Сервер не говорит по TLS на этом порту.",
			"получен ответ, не похожий на TLS (возможно, порт без шифрования, например 143)"
	}
	if strings.Contains(strings.ToLower(err.Error()), "does not look like a tls handshake") {
		return "Сервер не говорит по TLS на этом порту.",
			"получен ответ, не похожий на TLS (возможно, порт без шифрования, например 143)"
	}
	if stage == "greeting" || stage == "tls" {
		if neterr.IsEOF(err) {
			return "Сервер закрыл соединение, не поздоровавшись.",
				"получен EOF: соединение принято, но сервер сразу его закрыл"
		}
		return "Сервер не ответил как IMAP.", err.Error()
	}
	return "Не удалось подключиться.", err.Error()
}

func tcpHint(err error, port int, mode string) string {
	switch {
	case neterr.IsRefused(err):
		if mode == "implicit" && port != 993 {
			return fmt.Sprintf("Похоже, порт %d не обслуживается. Для SSL/TLS обычно используется 993, для STARTTLS — 143. Если порты верные, значит доступ к IMAP закрыт на стороне сервера: проверьте список разрешённых IP в Kerio Connect (Services → IMAP → Access) и блокировку IP в Intrusion Prevention.", port)
		}
		return "Сервер жив, но порт отбивает соединение. Проверьте: 1) разрешён ли IMAP/POP3 для вашей сети в Kerio Connect (Services → Access, Intrusion Prevention — заблокированные IP); 2) нет ли правила файрвола, отбивающего этот порт с вашего адреса. Снаружи (с другого провайдера) тот же порт обычно доступен."
	case neterr.IsTimeout(err):
		return "Порт фильтруется по пути: проверьте файрвол/антивирус на этом компьютере и правила на шлюзе."
	case neterr.IsUnreachable(err):
		return "Проверьте сеть и маршрут до сервера."
	}
	return "Проверьте адрес сервера, порт и доступность сети."
}
