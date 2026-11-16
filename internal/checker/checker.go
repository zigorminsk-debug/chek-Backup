// Package checker inspects the mailbox and decides whether the backup looks
// healthy: a report from the expected sender must be present and it must not
// announce a failure.
package checker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/config"
	"github.com/zigorminsk-debug/chek-Backup/internal/imapx"
	"github.com/zigorminsk-debug/chek-Backup/internal/logx"
	"github.com/zigorminsk-debug/chek-Backup/internal/neterr"
	"github.com/zigorminsk-debug/chek-Backup/internal/probe"
	"github.com/zigorminsk-debug/chek-Backup/internal/rules"
)

// Status is the overall verdict of a check.
type Status string

// Possible verdicts.
const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusError   Status = "error"
	StatusUnknown Status = "unknown"
)

// NeedsAttention reports whether the result should be shown to the user.
func (s Status) NeedsAttention() bool { return s != StatusOK }

// Message describes one analysed mailbox message.
type Message struct {
	UID     uint32
	Date    time.Time
	From    string
	Subject string
	Class   string
}

// Result is the outcome of a single check.
type Result struct {
	Status     Status
	Title      string
	Summary    string
	Details    []string
	CheckedAt  time.Time
	WindowFrom time.Time
	Duration   time.Duration
	Messages   []Message
	// Err keeps the technical error of a failed check, if any.
	Err error
}

// Diagnose connects to the endpoint step by step (DNS, TCP, TLS, приветствие
// IMAP) and returns a report with a Russian explanation of the first failure.
// It is used by the "Диагностика" button and by the failure path of a check.
func Diagnose(ctx context.Context, cfg *config.Config) *probe.Report {
	return probe.Run(ctx, probe.Options{
		Host:               cfg.IMAP.Host,
		Port:               cfg.IMAP.Port,
		Mode:               string(cfg.IMAP.Mode),
		Timeout:            cfg.Timeout(),
		InsecureSkipVerify: cfg.IMAP.InsecureSkipVerify,
	})
}

// Mailbox is the subset of the IMAP client used by the checker.
type Mailbox interface {
	Headers(ctx context.Context, since time.Time) ([]imapx.Header, error)
	RawMessage(ctx context.Context, uid uint32) ([]byte, error)
}

// Opener connects to a mailbox and returns a function that closes it again.
type Opener func(ctx context.Context) (Mailbox, func() error, error)

// Checker performs mailbox checks.
type Checker struct {
	cfg     *config.Config
	matcher *rules.Matcher
	log     *logx.Logger
	open    Opener
	now     func() time.Time
}

// New builds a checker that talks to the configured IMAP server.
func New(cfg *config.Config, log *logx.Logger) (*Checker, error) {
	matcher, err := rules.New(cfg.Rules)
	if err != nil {
		return nil, err
	}
	return &Checker{
		cfg:     cfg,
		matcher: matcher,
		log:     log,
		open:    IMAPOpener(cfg),
		now:     time.Now,
	}, nil
}

// IMAPOpener returns an Opener for the given configuration.
func IMAPOpener(cfg *config.Config) Opener {
	return func(ctx context.Context) (Mailbox, func() error, error) {
		password, err := cfg.Password()
		if err != nil {
			return nil, nil, err
		}
		client, err := imapx.Dial(ctx, imapx.Options{
			Host:               cfg.IMAP.Host,
			Port:               cfg.IMAP.Port,
			Mode:               imapx.TLSMode(cfg.IMAP.Mode),
			Username:           cfg.IMAP.Username,
			Password:           password,
			Mailbox:            cfg.IMAP.Mailbox,
			Timeout:            cfg.Timeout(),
			InsecureSkipVerify: cfg.IMAP.InsecureSkipVerify,
			MaxLiteralBytes:    4 << 20,
		})
		if err != nil {
			return nil, nil, err
		}
		return client, client.Close, nil
	}
}

// WithNow overrides the clock, which keeps the tests deterministic.
func (c *Checker) WithNow(now func() time.Time) *Checker {
	if now != nil {
		c.now = now
	}
	return c
}

// WithOpener overrides the mailbox connection, which keeps the tests offline.
func (c *Checker) WithOpener(o Opener) *Checker {
	if o != nil {
		c.open = o
	}
	return c
}

// Run performs one check and returns the verdict.
func (c *Checker) Run(ctx context.Context) Result {
	started := c.now()
	res := Result{CheckedAt: started, WindowFrom: started.Add(-c.cfg.Lookback())}
	if c.log != nil {
		c.log.Infof("Проверка ящика %s@%s: окно с %s", c.cfg.IMAP.Username, c.cfg.IMAP.Host, FormatTime(res.WindowFrom))
	}

	mailbox, closeFn, err := c.open(ctx)
	if err != nil {
		res = c.fail(res, "Нет связи с почтовым ящиком", err)
		// The raw network error rarely helps the user, a step by step
		// diagnosis names the failing stage and gives a hint. It is only
		// added when it really explains this failure: a custom opener (tests)
		// can fail for reasons the network probe cannot see.
		if rep := Diagnose(ctx, c.cfg); rep != nil && !rep.OK() && isNetworkError(err) {
			res.Summary = rep.Reason
			if rep.Hint != "" {
				res.Details = append(res.Details, "Что делать: "+rep.Hint)
			}
			for _, st := range rep.Steps {
				state := "успешно"
				if !st.OK {
					state = "ошибка"
				}
				res.Details = append(res.Details, fmt.Sprintf("%s — %s (%s)", st.Kind.Title(), state, st.Detail))
			}
			res.Err = err
		}
		return res
	}
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	headers, err := mailbox.Headers(ctx, res.WindowFrom)
	if err != nil {
		return c.fail(res, "Не удалось прочитать почтовый ящик", err)
	}

	candidates := make([]imapx.Header, 0, len(headers))
	for _, h := range headers {
		if !h.Date.IsZero() && h.Date.Before(res.WindowFrom) {
			continue
		}
		if !c.matcher.SenderMatches(h.From) {
			continue
		}
		if c.matcher.Ignored(h.Subject, "") {
			continue
		}
		candidates = append(candidates, h)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Date.After(candidates[j].Date) })

	res.Details = append(res.Details,
		fmt.Sprintf("Проверено писем в ящике: %d", len(headers)),
		fmt.Sprintf("Писем от ожидаемых отправителей: %d", len(candidates)),
	)
	if senders := c.matcher.Senders(); len(senders) > 0 {
		res.Details = append(res.Details, "Ожидаемые отправители: "+strings.Join(senders, ", "))
	}

	var verdict *Message
	bodiesFetched := 0
	for i, h := range candidates {
		msg := Message{
			UID:     h.UID,
			Date:    h.Date,
			From:    strings.Join(h.From, ", "),
			Subject: h.Subject,
		}
		outcome := c.matcher.ClassifySubject(h.Subject)
		if outcome.Class == rules.Unknown && verdict == nil && !c.matcher.SubjectOnly() {
			if bodiesFetched >= c.cfg.MaxMessagesPerCheck {
				res.Details = append(res.Details, fmt.Sprintf("Достигнут лимит загрузки писем (%d), часть писем не проанализирована", c.cfg.MaxMessagesPerCheck))
				candidates = candidates[:i]
				break
			}
			raw, fetchErr := mailbox.RawMessage(ctx, h.UID)
			bodiesFetched++
			if fetchErr != nil {
				if c.log != nil {
					c.log.Warnf("Не удалось загрузить письмо «%s»: %v", h.Subject, fetchErr)
				}
				res.Details = append(res.Details, fmt.Sprintf("Не удалось загрузить письмо «%s»: %v", h.Subject, fetchErr))
			} else {
				if msg.Subject == "" {
					msg.Subject = imapx.SubjectFromRaw(raw)
				}
				outcome = c.matcher.Classify(msg.Subject, imapx.TextBody(raw))
			}
		}
		msg.Class = outcome.Class.String()
		res.Messages = append(res.Messages, msg)
		if verdict == nil && outcome.Class != rules.Unknown {
			v := msg
			verdict = &v
		}
	}

	res.Duration = c.now().Sub(started)
	switch {
	case verdict != nil && verdict.Class == rules.Failure.String():
		res.Status = StatusError
		res.Title = "Бэкап не выполнен"
		res.Summary = fmt.Sprintf("Получено письмо об ошибке: «%s» (%s, %s). Резервная копия, скорее всего, отсутствует.",
			truncate(verdict.Subject, 120), FormatTime(verdict.Date), verdict.From)
		res.Details = append(res.Details, "Сработало правило: "+"письмо об ошибке резервного копирования")
	case verdict != nil && verdict.Class == rules.Success.String():
		res.Status = StatusOK
		res.Title = "Бэкап выполнен"
		res.Summary = fmt.Sprintf("Получено письмо об успешном выполнении: «%s» (%s).", truncate(verdict.Subject, 120), FormatTime(verdict.Date))
	case len(candidates) == 0:
		res.Status = StatusWarning
		res.Title = "Письмо о бэкапе не получено"
		res.Summary = fmt.Sprintf("За последние %d ч. не пришло ни одного письма от %s. Служба резервного копирования, возможно, не работает.",
			c.cfg.Schedule.LookbackHours, senderList(c.matcher.Senders()))
		res.Details = append(res.Details, "Сработало правило: отсутствие письма от ожидаемого отправителя")
	default:
		res.Status = StatusWarning
		res.Title = "Результат бэкапа не распознан"
		subjects := make([]string, 0, len(res.Messages))
		for _, m := range res.Messages {
			subjects = append(subjects, "«"+truncate(m.Subject, 80)+"»")
		}
		res.Summary = fmt.Sprintf("Письма от %s есть (%s), но ни одно не удалось классифицировать как успешное или ошибочное. Проверьте правила в настройках.",
			senderList(c.matcher.Senders()), strings.Join(subjects, ", "))
	}

	c.logResult(res)
	return res
}

// isNetworkError reports whether err looks like a socket level failure that the
// step by step probe is able to explain.
func isNetworkError(err error) bool {
	return neterr.IsNetwork(err)
}

func (c *Checker) fail(res Result, title string, err error) Result {
	res.Status = StatusUnknown
	res.Title = title
	res.Summary = err.Error()
	res.Err = err
	res.Duration = c.now().Sub(res.CheckedAt)
	if c.log != nil {
		c.log.Errorf("%s: %v", title, err)
	}
	return res
}

func (c *Checker) logResult(res Result) {
	if c.log == nil {
		return
	}
	line := fmt.Sprintf("Результат проверки: %s — %s. %s (%s)", res.Status, res.Title, res.Summary, res.Duration.Round(time.Millisecond))
	switch res.Status {
	case StatusOK:
		c.log.Infof("%s", line)
	case StatusError, StatusUnknown:
		c.log.Errorf("%s", line)
	default:
		c.log.Warnf("%s", line)
	}
}

func senderList(senders []string) string {
	if len(senders) == 0 {
		return "любого отправителя"
	}
	return strings.Join(senders, ", ")
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// FormatTime renders a timestamp in the local time zone.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("02.01.2006 15:04")
}
