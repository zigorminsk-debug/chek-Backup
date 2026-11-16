package rules

import (
	"testing"

	"github.com/zigorminsk-debug/chek-Backup/internal/config"
)

func newMatcher(t *testing.T, spec config.Rules) *Matcher {
	t.Helper()
	m, err := New(spec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestSenderMatching(t *testing.T) {
	m := newMatcher(t, config.Rules{ExpectedSenders: []string{"backup@csl.by", "@veeam.local"}})
	cases := []struct {
		from []string
		want bool
	}{
		{[]string{"backup@csl.by"}, true},
		{[]string{"Резервное копирование <BACKUP@CSL.BY>"}, true},
		{[]string{"user@csl.by"}, false},
		{[]string{"veeam@veeam.local"}, true},
		{[]string{"someone@example.com"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := m.SenderMatches(c.from); got != c.want {
			t.Errorf("SenderMatches(%v) = %v, хотим %v", c.from, got, c.want)
		}
	}
}

func TestEmptySenderListMatchesEverything(t *testing.T) {
	m := newMatcher(t, config.Rules{})
	if !m.SenderMatches([]string{"whatever@example.com"}) {
		t.Fatal("пустой список отправителей должен пропускать любые письма")
	}
}

func TestClassify(t *testing.T) {
	m := newMatcher(t, config.Default().Rules)

	cases := []struct {
		name    string
		subject string
		body    string
		want    Class
	}{
		{"успех по теме", "Резервное копирование завершено успешно", "", Success},
		{"ошибка по теме", "Ошибка резервного копирования", "", Failure},
		{"ошибка в тексте", "Отчёт о задании", "Задание не выполнено: нет места на диске", Failure},
		{"служба не работает", "Уведомление", "Служба резервного копирования не работает", Failure},
		{"успех в тексте", "Отчёт о задании", "Задание выполнено, копия создана", Success},
		{"неизвестно", "Привет", "Ничего интересного", Unknown},
		{"english error", "Veeam Backup job failed", "", Failure},
		{"english success", "Backup job completed successfully", "", Success},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := m.Classify(c.subject, c.body).Class; got != c.want {
				t.Fatalf("Classify(%q, %q) = %v, хотим %v", c.subject, c.body, got, c.want)
			}
		})
	}
}

func TestFailureWinsOverSuccess(t *testing.T) {
	m := newMatcher(t, config.Rules{
		FailurePatterns: []string{"ошибк"},
		SuccessPatterns: []string{"завершен"},
	})
	out := m.Classify("Задание завершено с ошибками", "")
	if out.Class != Failure {
		t.Fatalf("ожидали Failure, получили %v", out.Class)
	}
}

func TestSubjectOnlySkipsBody(t *testing.T) {
	m := newMatcher(t, config.Rules{
		FailurePatterns: []string{"ошибк"},
		SuccessPatterns: []string{"успешн"},
		SubjectOnly:     true,
	})
	if got := m.Classify("Отчёт", "Произошла ошибка").Class; got != Unknown {
		t.Fatalf("в режиме SubjectOnly текст не должен учитываться, получили %v", got)
	}
}

func TestIgnorePatterns(t *testing.T) {
	m := newMatcher(t, config.Rules{
		FailurePatterns: []string{"ошибк"},
		IgnorePatterns:  []string{"рассылк", "newsletter"},
	})
	if !m.Ignored("Новости компании", "Наша рассылка") {
		t.Error("письмо должно быть проигнорировано по тексту")
	}
	if !m.Ignored("Weekly newsletter", "") {
		t.Error("письмо должно быть проигнорировано по теме")
	}
	if m.Ignored("Ошибка копирования", "Диск переполнен") {
		t.Error("важное письмо не должно игнорироваться")
	}
}

func TestInvalidPattern(t *testing.T) {
	if _, err := New(config.Rules{FailurePatterns: []string{"([a-z"}}); err == nil {
		t.Fatal("ожидали ошибку компиляции регулярного выражения")
	}
}
