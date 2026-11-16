package checker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/config"
	"github.com/zigorminsk-debug/chek-Backup/internal/imapx"
)

var fixedNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeMailbox struct {
	headers    []imapx.Header
	bodies     map[uint32]string
	headersErr error
	bodyErr    error
	fetched    []uint32
}

func (f *fakeMailbox) Headers(ctx context.Context, since time.Time) ([]imapx.Header, error) {
	if f.headersErr != nil {
		return nil, f.headersErr
	}
	return f.headers, nil
}

func (f *fakeMailbox) RawMessage(ctx context.Context, uid uint32) ([]byte, error) {
	f.fetched = append(f.fetched, uid)
	if f.bodyErr != nil {
		return nil, f.bodyErr
	}
	return []byte(f.bodies[uid]), nil
}

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Rules.ExpectedSenders = []string{"backup@csl.by"}
	cfg.Schedule.LookbackHours = 26
	cfg.MaxMessagesPerCheck = 10
	return cfg
}

func newChecker(t *testing.T, cfg *config.Config, mb *fakeMailbox) *Checker {
	t.Helper()
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.WithNow(func() time.Time { return fixedNow })
	c.WithOpener(func(ctx context.Context) (Mailbox, func() error, error) {
		return mb, func() error { return nil }, nil
	})
	return c
}

func header(uid uint32, hoursAgo float64, from, subject string) imapx.Header {
	return imapx.Header{
		UID:     uid,
		Date:    fixedNow.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		From:    []string{from},
		Subject: subject,
	}
}

func TestMissingReport(t *testing.T) {
	mb := &fakeMailbox{headers: []imapx.Header{
		header(1, 2, "news@example.com", "Новости"),
	}}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusWarning {
		t.Fatalf("статус = %s, хотим warning (%s)", res.Status, res.Summary)
	}
	if !strings.Contains(res.Summary, "не пришло ни одного письма") {
		t.Fatalf("неожиданное описание: %s", res.Summary)
	}
	if res.Title != "Письмо о бэкапе не получено" {
		t.Fatalf("заголовок: %s", res.Title)
	}
}

func TestReportOutsideWindowIsIgnored(t *testing.T) {
	mb := &fakeMailbox{headers: []imapx.Header{
		header(1, 40, "backup@csl.by", "Резервное копирование завершено успешно"),
	}}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusWarning {
		t.Fatalf("статус = %s, хотим warning", res.Status)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("письмо вне окна не должно анализироваться: %v", res.Messages)
	}
}

func TestFailureReport(t *testing.T) {
	mb := &fakeMailbox{headers: []imapx.Header{
		header(7, 6, "backup@csl.by", "Ошибка резервного копирования задания «Ежедневно»"),
	}}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusError {
		t.Fatalf("статус = %s, хотим error", res.Status)
	}
	if res.Title != "Бэкап не выполнен" {
		t.Fatalf("заголовок: %s", res.Title)
	}
	if !strings.Contains(res.Summary, "Ошибка резервного копирования") {
		t.Fatalf("описание не содержит тему письма: %s", res.Summary)
	}
	if len(mb.fetched) != 0 {
		t.Fatalf("тело письма не нужно загружать, когда тема уже говорит об ошибке: %v", mb.fetched)
	}
}

func TestSuccessReport(t *testing.T) {
	mb := &fakeMailbox{headers: []imapx.Header{
		header(9, 5, "Служба <backup@csl.by>", "Veeam: job completed successfully"),
	}}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusOK {
		t.Fatalf("статус = %s, хотим ok (%s)", res.Status, res.Summary)
	}
}

func TestFailureInBody(t *testing.T) {
	mb := &fakeMailbox{
		headers: []imapx.Header{header(11, 3, "backup@csl.by", "Отчёт о задании")},
		bodies: map[uint32]string{
			11: "From: backup@csl.by\r\nSubject: Отчёт о задании\r\n\r\nЗадание не выполнено: служба резервного копирования не работает.\r\n",
		},
	}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusError {
		t.Fatalf("статус = %s, хотим error (%s)", res.Status, res.Summary)
	}
	if len(mb.fetched) != 1 || mb.fetched[0] != 11 {
		t.Fatalf("ожидали загрузку письма 11, получили %v", mb.fetched)
	}
}

func TestNewestMessageWins(t *testing.T) {
	mb := &fakeMailbox{headers: []imapx.Header{
		header(21, 30, "backup@csl.by", "Ошибка резервного копирования"),
		header(22, 3, "backup@csl.by", "Резервное копирование завершено успешно"),
	}}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusOK {
		t.Fatalf("статус = %s, хотим ok (%s)", res.Status, res.Summary)
	}
}

func TestUnclassifiedReport(t *testing.T) {
	mb := &fakeMailbox{
		headers: []imapx.Header{header(31, 4, "backup@csl.by", "Ежедневный отчёт")},
		bodies:  map[uint32]string{31: "From: backup@csl.by\r\n\r\nОтчёт во вложении.\r\n"},
	}
	res := newChecker(t, testConfig(), mb).Run(context.Background())
	if res.Status != StatusWarning {
		t.Fatalf("статус = %s, хотим warning", res.Status)
	}
	if res.Title != "Результат бэкапа не распознан" {
		t.Fatalf("заголовок: %s", res.Title)
	}
}

func TestConnectionError(t *testing.T) {
	c, err := New(testConfig(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.WithNow(func() time.Time { return fixedNow })
	c.WithOpener(func(ctx context.Context) (Mailbox, func() error, error) {
		return nil, nil, errors.New("dial tcp: i/o timeout")
	})
	res := c.Run(context.Background())
	if res.Status != StatusUnknown {
		t.Fatalf("статус = %s, хотим unknown", res.Status)
	}
	if res.Title != "Нет связи с почтовым ящиком" {
		t.Fatalf("заголовок: %s", res.Title)
	}
	if res.Err == nil {
		t.Fatal("ожидали сохранённую ошибку")
	}
}

func TestFetchLimit(t *testing.T) {
	cfg := testConfig()
	cfg.MaxMessagesPerCheck = 1
	mb := &fakeMailbox{
		headers: []imapx.Header{
			header(41, 1, "backup@csl.by", "Отчёт 1"),
			header(42, 2, "backup@csl.by", "Отчёт 2"),
			header(43, 3, "backup@csl.by", "Отчёт 3"),
		},
		bodies: map[uint32]string{
			41: "Отчёт без ключевых слов",
			42: "Отчёт без ключевых слов",
			43: "Отчёт без ключевых слов",
		},
	}
	res := newChecker(t, cfg, mb).Run(context.Background())
	if len(mb.fetched) != 1 {
		t.Fatalf("должно быть загружено 1 письмо, загружено %v", mb.fetched)
	}
	if !strings.Contains(strings.Join(res.Details, " "), "лимит") {
		t.Fatalf("в деталях нет упоминания лимита: %v", res.Details)
	}
}

func TestNeedsAttention(t *testing.T) {
	cases := map[Status]bool{
		StatusOK:      false,
		StatusWarning: true,
		StatusError:   true,
		StatusUnknown: true,
	}
	for status, want := range cases {
		if got := status.NeedsAttention(); got != want {
			t.Errorf("%s.NeedsAttention() = %v", status, got)
		}
	}
}

// TestNetworkErrorsAreRecognised guards the gate that decides whether the
// step by step diagnosis is attached to a failed check.
func TestNetworkErrorsAreRecognised(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("dial tcp 37.17.55.196:993: connectex: No connection could be made because the target machine actively refused it."), true},
		{errors.New("dial tcp: lookup imap.csl.by: no such host"), true},
		{errors.New("imapx: сервер отклонил вход для backup@csl.by"), false},
		{errors.New("пароль не задан"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isNetworkError(tc.err); got != tc.want {
			t.Errorf("isNetworkError(%v) = %v, хотим %v", tc.err, got, tc.want)
		}
	}
}
