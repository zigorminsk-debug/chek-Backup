package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/chek-Backup/internal/config"
	"github.com/zigorminsk-debug/chek-Backup/internal/platform"
)

// tlsOptions maps the user visible labels to configuration values.
var tlsOptions = []struct {
	Label string
	Mode  config.TLSMode
}{
	{"SSL/TLS (обычно порт 993)", config.TLSImplicit},
	{"STARTTLS (обычно порт 143)", config.TLSStartTLS},
	{"Без шифрования (не рекомендуется)", config.TLSNone},
}

type settingsForm struct {
	ui *UI
	w  fyne.Window

	host     *widget.Entry
	port     *widget.Entry
	mode     *widget.Select
	username *widget.Entry
	password *widget.Entry
	mailbox  *widget.Entry
	timeout  *widget.Entry
	insecure *widget.Check

	senders     *widget.Entry
	failure     *widget.Entry
	success     *widget.Entry
	ignore      *widget.Entry
	subjectOnly *widget.Check

	dailyAt      *widget.Entry
	lookback     *widget.Entry
	maxMessages  *widget.Entry
	maxLogSize   *widget.Entry
	runOnStartup *widget.Check
	autostart    *widget.Check

	onSuccess   *widget.Check
	onWarning   *widget.Check
	onError     *widget.Check
	alertWindow *widget.Check
	sound       *widget.Check

	saveBtn *widget.Button
	testBtn *widget.Button
	diagBtn *widget.Button

	reportTitle *widget.Label
	reportText  *widget.Label
	reportBox   *fyne.Container
}

// ShowSettings opens (or focuses) the settings window.
func (u *UI) ShowSettings() {
	fyne.Do(func() {
		if u.settingsWin == nil {
			u.settingsWin = u.buildSettingsWindow()
		}
		u.settingsOpen = true
		u.settingsWin.Show()
		u.settingsWin.RequestFocus()
	})
}

func (u *UI) buildSettingsWindow() fyne.Window {
	w := u.app.NewWindow("Chek Backup — настройки")
	w.SetIcon(u.appIcon)
	w.SetCloseIntercept(func() {
		u.settingsOpen = false
		w.Hide()
	})

	f := &settingsForm{ui: u, w: w}
	cfg := u.Settings().Clone()

	f.host = widget.NewEntry()
	f.host.SetText(cfg.IMAP.Host)
	f.host.SetPlaceHolder(config.DefaultHost)

	f.port = widget.NewEntry()
	f.port.SetText(strconv.Itoa(cfg.IMAP.Port))

	f.mode = widget.NewSelect(tlsLabels(), nil)
	f.mode.SetSelected(tlsLabel(cfg.IMAP.Mode))

	f.username = widget.NewEntry()
	f.username.SetText(cfg.IMAP.Username)
	f.username.SetPlaceHolder("backup@csl.by")

	f.password = widget.NewPasswordEntry()
	if pw, err := cfg.Password(); err == nil {
		f.password.SetText(pw)
	}
	f.password.SetPlaceHolder("пароль почтового ящика")

	f.mailbox = widget.NewEntry()
	f.mailbox.SetText(cfg.IMAP.Mailbox)

	f.timeout = widget.NewEntry()
	f.timeout.SetText(strconv.Itoa(cfg.IMAP.TimeoutSeconds))

	f.insecure = widget.NewCheck("Не проверять сертификат сервера", nil)
	f.insecure.SetChecked(cfg.IMAP.InsecureSkipVerify)

	imapForm := widget.NewForm(
		widget.NewFormItem("IMAP-сервер", f.host),
		widget.NewFormItem("Порт", f.port),
		widget.NewFormItem("Шифрование", f.mode),
		widget.NewFormItem("Логин (ящик)", f.username),
		widget.NewFormItem("Пароль", f.password),
		widget.NewFormItem("Папка", f.mailbox),
		widget.NewFormItem("Таймаут, сек", f.timeout),
		widget.NewFormItem("", f.insecure),
	)

	f.senders = widget.NewEntry()
	f.senders.SetText(strings.Join(cfg.Rules.ExpectedSenders, ", "))
	f.senders.SetPlaceHolder("backup@csl.by — можно несколько через запятую")

	f.failure = newMultiLine(cfg.Rules.FailurePatterns)
	f.success = newMultiLine(cfg.Rules.SuccessPatterns)
	f.ignore = newMultiLine(cfg.Rules.IgnorePatterns)

	f.subjectOnly = widget.NewCheck("Искать только в теме письма", nil)
	f.subjectOnly.SetChecked(cfg.Rules.SubjectOnly)

	rulesForm := widget.NewForm(
		widget.NewFormItem("Отправители", f.senders),
		widget.NewFormItem("Признаки ошибки", f.failure),
		widget.NewFormItem("Признаки успеха", f.success),
		widget.NewFormItem("Игнорировать письма", f.ignore),
		widget.NewFormItem("Область поиска", f.subjectOnly),
	)
	rulesHint := widget.NewLabel("Признаки — это регулярные выражения без учёта регистра, по одному в строке.\n" +
		"Если письмо от нужного отправителя не найдено — будет предупреждение об отсутствии бэкапа.")
	rulesHint.Wrapping = fyne.TextWrapWord

	f.dailyAt = widget.NewEntry()
	f.dailyAt.SetText(cfg.Schedule.DailyAt)
	f.dailyAt.SetPlaceHolder("09:30")

	f.lookback = widget.NewEntry()
	f.lookback.SetText(strconv.Itoa(cfg.Schedule.LookbackHours))

	f.maxMessages = widget.NewEntry()
	f.maxMessages.SetText(strconv.Itoa(cfg.MaxMessagesPerCheck))

	f.maxLogSize = widget.NewEntry()
	f.maxLogSize.SetText(strconv.Itoa(cfg.MaxLogSizeMB))

	f.runOnStartup = widget.NewCheck("Проверять почту сразу после запуска", nil)
	f.runOnStartup.SetChecked(cfg.Schedule.RunOnStartup)

	f.autostart = widget.NewCheck("Запускать программу при входе в Windows", nil)
	f.autostart.SetChecked(platform.AutostartEnabled())

	scheduleForm := widget.NewForm(
		widget.NewFormItem("Время проверки", f.dailyAt),
		widget.NewFormItem("Окно проверки, ч", f.lookback),
		widget.NewFormItem("Максимум писем", f.maxMessages),
		widget.NewFormItem("Размер журнала, МБ", f.maxLogSize),
		widget.NewFormItem("", f.runOnStartup),
		widget.NewFormItem("", f.autostart),
	)

	f.onSuccess = widget.NewCheck("Об успешном бэкапе", nil)
	f.onSuccess.SetChecked(cfg.Notify.OnSuccess)

	f.onWarning = widget.NewCheck("Об отсутствии письма о бэкапе", nil)
	f.onWarning.SetChecked(cfg.Notify.OnWarning)

	f.onError = widget.NewCheck("О письме с ошибкой", nil)
	f.onError.SetChecked(cfg.Notify.OnError)

	f.alertWindow = widget.NewCheck("Показывать окно с предупреждением", nil)
	f.alertWindow.SetChecked(cfg.Notify.AlertWindow)

	f.sound = widget.NewCheck("Подавать звуковой сигнал", nil)
	f.sound.SetChecked(cfg.Notify.Sound)

	notifyForm := widget.NewForm(
		widget.NewFormItem("Уведомлять:", f.onSuccess),
		widget.NewFormItem("", f.onWarning),
		widget.NewFormItem("", f.onError),
		widget.NewFormItem("", f.alertWindow),
		widget.NewFormItem("", f.sound),
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Почта", container.NewVScroll(container.NewPadded(imapForm))),
		container.NewTabItem("Правила", container.NewVScroll(container.NewPadded(container.NewVBox(rulesForm, rulesHint)))),
		container.NewTabItem("Расписание", container.NewVScroll(container.NewPadded(scheduleForm))),
		container.NewTabItem("Уведомления", container.NewVScroll(container.NewPadded(notifyForm))),
	)

	f.saveBtn = widget.NewButton("Сохранить", f.save)
	f.saveBtn.Importance = widget.HighImportance
	f.testBtn = widget.NewButton("Проверить подключение", f.testConnection)
	f.diagBtn = widget.NewButton("Диагностика", f.diagnose)
	cancelBtn := widget.NewButton("Отмена", func() {
		u.settingsOpen = false
		w.Hide()
	})

	footer := container.NewHBox(
		f.saveBtn,
		f.testBtn,
		f.diagBtn,
		layout.NewSpacer(),
		cancelBtn,
	)

	// The diagnosis report lives under the tabs so that it does not cover
	// the settings with a modal dialog. A Label is used instead of an Entry:
	// labels are read only and wrap long lines, so the report cannot be
	// edited or scrolled sideways by accident.
	f.reportTitle = widget.NewLabel("")
	f.reportTitle.TextStyle = fyne.TextStyle{Bold: true}
	f.reportTitle.Wrapping = fyne.TextWrapWord
	f.reportTitle.Hide()
	f.reportText = widget.NewLabel("")
	f.reportText.Wrapping = fyne.TextWrapWord
	f.reportText.Hide()
	report := container.NewVBox(f.reportTitle, f.reportText)
	report.Hide()
	f.reportBox = report

	pathNote := widget.NewLabel("Файл настроек: " + u.configPathHint())
	pathNote.TextStyle = fyne.TextStyle{Italic: true}

	content := container.NewBorder(
		nil,
		container.NewVBox(report, footer, pathNote),
		nil, nil,
		tabs,
	)
	w.SetContent(container.NewPadded(content))
	w.Resize(fyne.NewSize(760, 660))
	w.CenterOnScreen()
	return w
}

func newMultiLine(values []string) *widget.Entry {
	e := widget.NewMultiLineEntry()
	e.SetText(strings.Join(values, "\n"))
	e.SetMinRowsVisible(3)
	return e
}

func tlsLabels() []string {
	labels := make([]string, 0, len(tlsOptions))
	for _, opt := range tlsOptions {
		labels = append(labels, opt.Label)
	}
	return labels
}

func tlsLabel(mode config.TLSMode) string {
	for _, opt := range tlsOptions {
		if opt.Mode == mode {
			return opt.Label
		}
	}
	return tlsOptions[0].Label
}

func tlsMode(label string) config.TLSMode {
	for _, opt := range tlsOptions {
		if opt.Label == label {
			return opt.Mode
		}
	}
	return config.TLSImplicit
}

func splitList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == ' ' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitLines(s string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// collect builds a configuration from the form values.
func (f *settingsForm) collect() (*config.Config, error) {
	cfg := f.ui.Settings().Clone()

	cfg.IMAP.Host = strings.TrimSpace(f.host.Text)
	port, err := strconv.Atoi(strings.TrimSpace(f.port.Text))
	if err != nil {
		return nil, fmt.Errorf("порт должен быть числом")
	}
	cfg.IMAP.Port = port
	cfg.IMAP.Mode = tlsMode(f.mode.Selected)
	cfg.IMAP.Username = strings.TrimSpace(f.username.Text)
	cfg.IMAP.Mailbox = strings.TrimSpace(f.mailbox.Text)
	cfg.IMAP.InsecureSkipVerify = f.insecure.Checked
	if timeout, convErr := strconv.Atoi(strings.TrimSpace(f.timeout.Text)); convErr == nil {
		cfg.IMAP.TimeoutSeconds = timeout
	}
	if err := cfg.SetPassword(f.password.Text); err != nil {
		return nil, err
	}

	cfg.Rules.ExpectedSenders = splitList(f.senders.Text)
	cfg.Rules.FailurePatterns = splitLines(f.failure.Text)
	cfg.Rules.SuccessPatterns = splitLines(f.success.Text)
	cfg.Rules.IgnorePatterns = splitLines(f.ignore.Text)
	cfg.Rules.SubjectOnly = f.subjectOnly.Checked

	cfg.Schedule.DailyAt = strings.TrimSpace(f.dailyAt.Text)
	if hours, convErr := strconv.Atoi(strings.TrimSpace(f.lookback.Text)); convErr == nil {
		cfg.Schedule.LookbackHours = hours
	}
	if maxMessages, convErr := strconv.Atoi(strings.TrimSpace(f.maxMessages.Text)); convErr == nil {
		cfg.MaxMessagesPerCheck = maxMessages
	}
	if maxLog, convErr := strconv.Atoi(strings.TrimSpace(f.maxLogSize.Text)); convErr == nil {
		cfg.MaxLogSizeMB = maxLog
	}
	cfg.Schedule.RunOnStartup = f.runOnStartup.Checked
	cfg.Autostart = f.autostart.Checked

	cfg.Notify.OnSuccess = f.onSuccess.Checked
	cfg.Notify.OnWarning = f.onWarning.Checked
	cfg.Notify.OnError = f.onError.Checked
	cfg.Notify.AlertWindow = f.alertWindow.Checked
	cfg.Notify.Sound = f.sound.Checked

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (f *settingsForm) save() {
	cfg, err := f.collect()
	if err != nil {
		dialog.ShowError(err, f.w)
		return
	}
	if f.ui.cb.SaveConfig != nil {
		if err := f.ui.cb.SaveConfig(cfg); err != nil {
			dialog.ShowError(fmt.Errorf("не удалось сохранить настройки: %w", err), f.w)
			return
		}
	}
	f.ui.SetConfig(cfg)
	if err := platform.SetAutostart(cfg.Autostart); err != nil {
		dialog.ShowError(fmt.Errorf("не удалось изменить автозапуск: %w", err), f.w)
	}
	f.ui.SetAutostartState(platform.AutostartEnabled())
	f.ui.ShowInfo("Настройки сохранены", "Новые параметры применены. Проверка будет выполнена по новому расписанию.")
	if f.ui.log != nil {
		f.ui.log.Infof("настройки сохранены")
	}
}

// connectionCandidate is one combination of port and encryption that is tried
// by the connection test.
type connectionCandidate struct {
	Port int
	Mode config.TLSMode
}

// candidates returns the endpoint variants that are worth trying: first the one
// from the form, then the other two standard combinations.
func (f *settingsForm) candidates(cfg *config.Config) []connectionCandidate {
	current := connectionCandidate{Port: cfg.IMAP.Port, Mode: cfg.IMAP.Mode}
	others := []connectionCandidate{
		{Port: 993, Mode: config.TLSImplicit},
		{Port: 143, Mode: config.TLSStartTLS},
		{Port: 143, Mode: config.TLSNone},
	}
	out := []connectionCandidate{current}
	for _, c := range others {
		if c.Port == current.Port && c.Mode == current.Mode {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (f *settingsForm) setBusy(busy bool) {
	for _, b := range []*widget.Button{f.testBtn, f.diagBtn, f.saveBtn} {
		if b == nil {
			continue
		}
		if busy {
			b.Disable()
			continue
		}
		b.Enable()
	}
}

func (f *settingsForm) testConnection() {
	cfg, err := f.collect()
	if err != nil {
		dialog.ShowError(err, f.w)
		return
	}
	if f.ui.cb.TestConfig == nil {
		return
	}
	candidates := f.candidates(cfg)
	f.setBusy(true)
	f.hideReport()
	go func() {
		type attempt struct {
			cand connectionCandidate
			err  error
		}
		var tries []attempt
		var success *connectionCandidate
		for _, cand := range candidates {
			try := cfg.Clone()
			try.IMAP.Port = cand.Port
			try.IMAP.Mode = cand.Mode
			testErr := f.ui.cb.TestConfig(try)
			tries = append(tries, attempt{cand: cand, err: testErr})
			if testErr == nil {
				c := cand
				success = &c
				break
			}
		}
		fyne.Do(func() {
			f.setBusy(false)
			if success != nil {
				f.port.SetText(strconv.Itoa(success.Port))
				f.mode.SetSelected(tlsLabel(success.Mode))
				dialog.ShowInformation("Подключение успешно",
					fmt.Sprintf("Ящик %s на %s доступен.\nПараметры для сохранения: порт %d, %s.",
						cfg.IMAP.Username, cfg.IMAP.Host, success.Port, tlsLabel(success.Mode)), f.w)
				return
			}
			var b strings.Builder
			b.WriteString("Подключиться не удалось ни одним из стандартных способов:\n\n")
			for _, t := range tries {
				fmt.Fprintf(&b, "• порт %d, %s — %v\n", t.cand.Port, tlsLabel(t.cand.Mode), t.err)
			}
			b.WriteString("\nНажмите «Диагностика», чтобы увидеть, на каком шаге (DNS, порт, TLS, IMAP) всё останавливается.")
			dialog.ShowError(errors.New(b.String()), f.w)
		})
	}()
}

// diagnose runs the step by step check and shows a report under the tabs.
func (f *settingsForm) diagnose() {
	cfg, err := f.collect()
	if err != nil {
		dialog.ShowError(err, f.w)
		return
	}
	if f.ui.cb.Diagnose == nil {
		return
	}
	f.setBusy(true)
	f.showReport("Диагностика…", "Проверяю DNS, TCP-порт, TLS и приветствие IMAP. Это занимает до "+strconv.Itoa(int(cfg.Timeout().Seconds()))+" с.")
	go func() {
		rep := f.ui.cb.Diagnose(cfg)
		fyne.Do(func() {
			f.setBusy(false)
			if rep == nil {
				f.showReport("Диагностика", "Не удалось получить отчёт.")
				return
			}
			title := "Диагностика: подключение работает"
			if !rep.OK() {
				title = "Диагностика: " + strings.TrimSuffix(rep.Reason, ".")
			}
			f.showReport(title, rep.Text())
		})
	}()
}

func (f *settingsForm) showReport(title, text string) {
	f.reportTitle.SetText(title)
	f.reportText.SetText(text)
	f.reportBox.Show()
	f.reportTitle.Show()
	f.reportText.Show()
	f.reportBox.Refresh()
}

func (f *settingsForm) hideReport() {
	f.reportBox.Hide()
}

func (u *UI) configPathHint() string {
	if u.cb.ConfigPath == nil {
		return "config.json"
	}
	if path := u.cb.ConfigPath(); path != "" {
		return path
	}
	return "config.json"
}
