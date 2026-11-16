// Package rules turns the textual configuration into compiled matchers and
// classifies mailbox messages as a successful or failed backup report.
package rules

import (
	"regexp"
	"strings"

	"github.com/zigorminsk-debug/chek-Backup/internal/config"
)

// Class is the classification of a single message.
type Class int

// Possible message classes.
const (
	Unknown Class = iota
	Success
	Failure
)

// String returns a stable identifier of the class.
func (c Class) String() string {
	switch c {
	case Success:
		return "success"
	case Failure:
		return "failure"
	default:
		return "unknown"
	}
}

// Outcome is the result of matching a single message.
type Outcome struct {
	Class   Class
	Pattern string // the regular expression that matched
	Field   string // "тема" or "текст"
}

// Matcher holds the compiled rules.
type Matcher struct {
	senders     []string
	failure     []*regexp.Regexp
	success     []*regexp.Regexp
	ignore      []*regexp.Regexp
	subjectOnly bool
}

// New compiles the rules from the configuration.
func New(spec config.Rules) (*Matcher, error) {
	m := &Matcher{subjectOnly: spec.SubjectOnly}
	for _, s := range spec.ExpectedSenders {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		m.senders = append(m.senders, s)
	}
	var err error
	if m.failure, err = compileAll(spec.FailurePatterns); err != nil {
		return nil, err
	}
	if m.success, err = compileAll(spec.SuccessPatterns); err != nil {
		return nil, err
	}
	if m.ignore, err = compileAll(spec.IgnorePatterns); err != nil {
		return nil, err
	}
	return m, nil
}

func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

// SubjectOnly reports whether bodies must not be inspected.
func (m *Matcher) SubjectOnly() bool { return m == nil || m.subjectOnly }

// Senders returns the configured sender fragments.
func (m *Matcher) Senders() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.senders...)
}

// SenderMatches reports whether at least one From value contains one of the
// expected sender fragments. An empty sender list matches everything.
func (m *Matcher) SenderMatches(from []string) bool {
	if m == nil || len(m.senders) == 0 {
		return true
	}
	for _, addr := range from {
		lower := strings.ToLower(addr)
		for _, want := range m.senders {
			if strings.Contains(lower, want) {
				return true
			}
		}
	}
	return false
}

// Ignored reports whether the message must be skipped completely.
func (m *Matcher) Ignored(subject, body string) bool {
	if m == nil {
		return false
	}
	text := normalize(subject)
	if !m.subjectOnly {
		text += "\n" + normalize(body)
	}
	for _, re := range m.ignore {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// Classify returns the class of a message. Failure has priority over success
// so that a report that contains both words ("завершено с ошибками") is
// treated as a failure.
func (m *Matcher) Classify(subject, body string) Outcome {
	if m == nil {
		return Outcome{Class: Unknown}
	}
	subj := normalize(subject)
	if out, ok := m.classifyText(subj, "тема"); ok {
		return out
	}
	if m.subjectOnly || strings.TrimSpace(body) == "" {
		return Outcome{Class: Unknown}
	}
	text := normalize(body)
	if out, ok := m.classifyText(text, "текст"); ok {
		return out
	}
	return Outcome{Class: Unknown}
}

// ClassifySubject classifies a message using its subject only, which allows
// skipping the download of the message body.
func (m *Matcher) ClassifySubject(subject string) Outcome {
	if m == nil {
		return Outcome{Class: Unknown}
	}
	out, _ := m.classifyText(normalize(subject), "тема")
	return out
}

func (m *Matcher) classifyText(text, field string) (Outcome, bool) {
	for _, re := range m.failure {
		if re.MatchString(text) {
			return Outcome{Class: Failure, Pattern: re.String(), Field: field}, true
		}
	}
	for _, re := range m.success {
		if re.MatchString(text) {
			return Outcome{Class: Success, Pattern: re.String(), Field: field}, true
		}
	}
	return Outcome{Class: Unknown}, false
}

func normalize(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\u200b", "")
	return strings.ToLower(s)
}
