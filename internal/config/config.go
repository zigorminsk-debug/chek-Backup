// Package config defines the on-disk configuration of the check-backup
// watcher together with loading, saving and validation helpers.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TLSMode describes how the IMAP connection is secured.
type TLSMode string

// Supported TLS modes.
const (
	TLSImplicit TLSMode = "implicit" // straight TLS connection, usually port 993
	TLSStartTLS TLSMode = "starttls" // plain connection upgraded with STARTTLS, usually port 143
	TLSNone     TLSMode = "none"     // unencrypted, not recommended
)

// PasswordPrefix marks a secret that was encrypted with the Windows DPAPI.
const PasswordPrefix = "dpapi:"

// DefaultHost is the mailbox server of csl.by. There is no imap.csl.by name in
// DNS: the domain publishes mail.csl.by, which is why it is the default.
const DefaultHost = "mail.csl.by"

// LegacyHosts maps server names that were suggested by earlier versions (or by
// a copy-paste habit) to the real one. The names do not exist in DNS, so
// replacing them automatically cannot break a working configuration.
var LegacyHosts = map[string]string{
	"imap.csl.by":  DefaultHost,
	"mails.csl.by": DefaultHost,
	"smtp.csl.by":  DefaultHost,
	"pop.csl.by":   DefaultHost,
	"imap.csl.com": DefaultHost,
	"imap.csl.net": DefaultHost,
}

// IMAP holds the mailbox connection settings.
type IMAP struct {
	Host               string  `json:"host"`
	Port               int     `json:"port"`
	Mode               TLSMode `json:"mode"`
	Username           string  `json:"username"`
	Password           string  `json:"password"`
	Mailbox            string  `json:"mailbox"`
	TimeoutSeconds     int     `json:"timeoutSeconds"`
	InsecureSkipVerify bool    `json:"insecureSkipVerify"`
}

// Rules describes which messages are considered a backup report and how they
// are classified.
type Rules struct {
	// ExpectedSenders holds address fragments (for example "backup@csl.by" or
	// "@csl.by"). A message matches when one of the fragments is contained in
	// one of its From addresses. An empty list matches any sender.
	ExpectedSenders []string `json:"expectedSenders"`
	// FailurePatterns are regular expressions that mark a message as a
	// backup failure report.
	FailurePatterns []string `json:"failurePatterns"`
	// SuccessPatterns are regular expressions that mark a message as a
	// successful backup report.
	SuccessPatterns []string `json:"successPatterns"`
	// IgnorePatterns are regular expressions that drop a message from the
	// analysis (newsletters and similar noise).
	IgnorePatterns []string `json:"ignorePatterns"`
	// SubjectOnly restricts matching to the subject line.
	SubjectOnly bool `json:"subjectOnly"`
}

// Schedule describes when the mailbox is inspected.
type Schedule struct {
	// DailyAt is the local time of the daily check in "HH:MM" form.
	DailyAt string `json:"dailyAt"`
	// LookbackHours is the size of the window that is inspected, counted back
	// from the moment of the check.
	LookbackHours int `json:"lookbackHours"`
	// RunOnStartup triggers an extra check shortly after the application
	// starts.
	RunOnStartup bool `json:"runOnStartup"`
}

// Notify configures user facing alerts.
type Notify struct {
	OnSuccess   bool `json:"onSuccess"`
	OnWarning   bool `json:"onWarning"`
	OnError     bool `json:"onError"`
	AlertWindow bool `json:"alertWindow"`
	Sound       bool `json:"sound"`
}

// Config is the root of the configuration file.
type Config struct {
	IMAP         IMAP     `json:"imap"`
	Rules        Rules    `json:"rules"`
	Schedule     Schedule `json:"schedule"`
	Notify       Notify   `json:"notify"`
	Autostart    bool     `json:"autostart"`
	MaxLogSizeMB int      `json:"maxLogSizeMB"`
	// MaxMessagesPerCheck caps the number of message bodies that are
	// downloaded during a single check.
	MaxMessagesPerCheck int `json:"maxMessagesPerCheck"`
}

// Default returns a configuration with sensible values for a Russian speaking
// backup mailbox ($MAILBOX$ gets replaced by the caller where needed).
func Default() *Config {
	return &Config{
		IMAP: IMAP{
			Host:           DefaultHost,
			Port:           993,
			Mode:           TLSImplicit,
			Username:       "backup@csl.by",
			Mailbox:        "INBOX",
			TimeoutSeconds: 60,
		},
		Rules: Rules{
			ExpectedSenders: []string{"backup@csl.by"},
			FailurePatterns: []string{
				`ошибк`, `сбой`, `не удалось`, `не выполн`, `не работает`,
				`авари`, `недостаточно`, `прерван`, `error`, `fail`, `critical`,
			},
			SuccessPatterns: []string{
				`успешн`, `успешно`, `завершен`, `завершён`, `выполнен`,
				`success`, `completed`, `finished`,
			},
			IgnorePatterns: []string{},
			SubjectOnly:    false,
		},
		Schedule: Schedule{
			DailyAt:       "09:30",
			LookbackHours: 26,
			RunOnStartup:  true,
		},
		Notify: Notify{
			OnSuccess:   false,
			OnWarning:   true,
			OnError:     true,
			AlertWindow: true,
			Sound:       true,
		},
		Autostart:           true,
		MaxLogSizeMB:        5,
		MaxMessagesPerCheck: 25,
	}
}

// Clone returns a deep copy of the configuration.
func (c *Config) Clone() *Config {
	if c == nil {
		return Default()
	}
	cp := *c
	cp.Rules.ExpectedSenders = append([]string(nil), c.Rules.ExpectedSenders...)
	cp.Rules.FailurePatterns = append([]string(nil), c.Rules.FailurePatterns...)
	cp.Rules.SuccessPatterns = append([]string(nil), c.Rules.SuccessPatterns...)
	cp.Rules.IgnorePatterns = append([]string(nil), c.Rules.IgnorePatterns...)
	return &cp
}

// Load reads the configuration from path. A missing file yields the default
// configuration and created=false.
func Load(path string) (cfg *Config, created bool, err error) {
	cfg, created, _, err = LoadEx(path)
	return cfg, created, err
}

// LoadEx is Load with an extra report: when the stored IMAP server name was a
// known legacy value (for example the non existing imap.csl.by), repairedFrom
// describes the replacement as "imap.csl.by → mail.csl.by" so that the caller
// can persist the fixed configuration.
func LoadEx(path string) (cfg *Config, created bool, repairedFrom string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), true, "", nil
		}
		return nil, false, "", err
	}
	// The raw host is read separately: Normalize() replaces it, so afterwards
	// it is impossible to tell whether the file contained the legacy name.
	var raw struct {
		IMAP struct {
			Host string `json:"host"`
		} `json:"imap"`
	}
	_ = json.Unmarshal(data, &raw)
	oldHost := strings.TrimSpace(raw.IMAP.Host)

	cfg = Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, false, "", fmt.Errorf("config: %s: %w", path, err)
	}
	cfg.Normalize()
	if oldHost != "" && !strings.EqualFold(oldHost, cfg.IMAP.Host) {
		repairedFrom = oldHost + " → " + cfg.IMAP.Host
	}
	return cfg, false, repairedFrom, nil
}

// Save writes the configuration to path, creating parent directories.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Normalize fills in missing values so that partially written configuration
// files keep working.
func (c *Config) Normalize() {
	d := Default()
	if c.IMAP.Host == "" {
		c.IMAP.Host = d.IMAP.Host
	}
	if real, ok := LegacyHosts[strings.ToLower(strings.TrimSpace(c.IMAP.Host))]; ok {
		c.IMAP.Host = real
	}
	if c.IMAP.Port <= 0 {
		if c.IMAP.Mode == TLSStartTLS || c.IMAP.Mode == TLSNone {
			c.IMAP.Port = 143
		} else {
			c.IMAP.Port = d.IMAP.Port
		}
	}
	switch c.IMAP.Mode {
	case TLSImplicit, TLSStartTLS, TLSNone:
	default:
		c.IMAP.Mode = TLSImplicit
	}
	if c.IMAP.Mailbox == "" {
		c.IMAP.Mailbox = d.IMAP.Mailbox
	}
	if c.IMAP.TimeoutSeconds <= 0 {
		c.IMAP.TimeoutSeconds = d.IMAP.TimeoutSeconds
	}
	if len(c.Rules.FailurePatterns) == 0 {
		c.Rules.FailurePatterns = d.Rules.FailurePatterns
	}
	if len(c.Rules.SuccessPatterns) == 0 {
		c.Rules.SuccessPatterns = d.Rules.SuccessPatterns
	}
	if c.Schedule.DailyAt == "" {
		c.Schedule.DailyAt = d.Schedule.DailyAt
	}
	if c.Schedule.LookbackHours <= 0 {
		c.Schedule.LookbackHours = d.Schedule.LookbackHours
	}
	if c.MaxLogSizeMB <= 0 {
		c.MaxLogSizeMB = d.MaxLogSizeMB
	}
	if c.MaxMessagesPerCheck <= 0 {
		c.MaxMessagesPerCheck = d.MaxMessagesPerCheck
	}
}

// Validate reports configuration problems that prevent a check from running.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.IMAP.Host) == "" {
		return errors.New("не задан IMAP-сервер")
	}
	if c.IMAP.Port <= 0 || c.IMAP.Port > 65535 {
		return fmt.Errorf("некорректный порт IMAP: %d", c.IMAP.Port)
	}
	if strings.TrimSpace(c.IMAP.Username) == "" {
		return errors.New("не задан логин почтового ящика")
	}
	if _, _, err := c.Schedule.Time(); err != nil {
		return err
	}
	for _, p := range append(append([]string{}, c.Rules.FailurePatterns...), c.Rules.SuccessPatterns...) {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("некорректное регулярное выражение %q: %w", p, err)
		}
	}
	for _, p := range c.Rules.IgnorePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("некорректное регулярное выражение %q: %w", p, err)
		}
	}
	return nil
}

// Time returns hour and minute of the daily check.
func (s Schedule) Time() (hour int, minute int, err error) {
	return ParseClock(s.DailyAt)
}

// ParseClock parses "HH:MM" (or "H:MM") into hour and minute values.
func ParseClock(s string) (hour int, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("время проверки должно быть в формате ЧЧ:ММ, получено %q", s)
	}
	hour, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("некорректный час в %q", s)
	}
	minute, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("некорректные минуты в %q", s)
	}
	return hour, minute, nil
}

// Timeout returns the IMAP timeout.
func (c *Config) Timeout() time.Duration {
	return time.Duration(c.IMAP.TimeoutSeconds) * time.Second
}

// Lookback returns the inspected time window.
func (c *Config) Lookback() time.Duration {
	return time.Duration(c.Schedule.LookbackHours) * time.Hour
}

// SetPassword stores the mail password, encrypted with the Windows DPAPI when
// available.
func (c *Config) SetPassword(plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		c.IMAP.Password = ""
		return nil
	}
	enc, err := ProtectSecret([]byte(plain))
	if err != nil {
		// Fall back to plain text so the application keeps working on
		// machines where the DPAPI is unavailable.
		c.IMAP.Password = plain
		return nil
	}
	c.IMAP.Password = PasswordPrefix + enc
	return nil
}

// Password decrypts and returns the stored mail password.
func (c *Config) Password() (string, error) {
	raw := c.IMAP.Password
	if raw == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, PasswordPrefix) {
		return raw, nil
	}
	plain, err := UnprotectSecret(strings.TrimPrefix(raw, PasswordPrefix))
	if err != nil {
		return "", fmt.Errorf("не удалось расшифровать пароль: %w", err)
	}
	return string(plain), nil
}
