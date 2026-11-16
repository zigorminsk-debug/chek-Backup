// Package ui implements the Fyne based tray application: a tray menu with the
// current status, a status window, a settings window and desktop
// notifications.
package ui

import (
	"embed"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/zigorminsk-debug/chek-Backup/internal/checker"
	"github.com/zigorminsk-debug/chek-Backup/internal/config"
	"github.com/zigorminsk-debug/chek-Backup/internal/logx"
	"github.com/zigorminsk-debug/chek-Backup/internal/platform"
	"github.com/zigorminsk-debug/chek-Backup/internal/state"
)

//go:embed assets
var assets embed.FS

// AppID identifies the application for the Windows notification system.
const AppID = "by.csl.chek-backup"

// Callbacks connect the UI with the rest of the application.
type Callbacks struct {
	// CheckNow starts a check in the background.
	CheckNow func()
	// SaveConfig persists a modified configuration and applies it.
	SaveConfig func(*config.Config) error
	// TestConfig verifies the mailbox connection without changing anything.
	TestConfig func(*config.Config) error
	// Quit terminates the application.
	Quit func()
	// LogPath returns the log file location.
	LogPath func() string
	// ConfigPath returns the configuration file location.
	ConfigPath func() string
}

// UI is the tray application window layer.
type UI struct {
	app  fyne.App
	desk desktop.App

	mu sync.Mutex
	cb Callbacks

	cfg *config.Config
	log *logx.Logger

	appIcon  fyne.Resource
	trayIcon fyne.Resource
	icons    map[string]fyne.Resource

	statusWin   fyne.Window
	alertWin    fyne.Window
	settingsWin fyne.Window

	checking   bool
	last       checker.Result
	haveResult bool
	next       time.Time
	autostart  bool

	// Status window widgets.
	statusTitle    *canvas.Text
	statusSummary  *widget.Label
	statusMeta     *widget.Label
	statusMessages *widget.List
	messageRows    []string
	statusSpinner  *widget.ProgressBarInfinite
}

// New builds the tray application. It must be called from the main goroutine.
func New(cfg *config.Config, log *logx.Logger, cb Callbacks) *UI {
	fyneApp := app.NewWithID(AppID)
	u := &UI{
		app:  fyneApp,
		cfg:  cfg,
		log:  log,
		cb:   cb,
		next: time.Time{},
	}
	u.autostart = platform.AutostartEnabled()

	u.icons = map[string]fyne.Resource{
		"app":          loadResource("app-256.png"),
		"tray-ok":      loadResource("tray-ok-32.png"),
		"tray-warning": loadResource("tray-warning-32.png"),
		"tray-error":   loadResource("tray-error-32.png"),
		"tray-unknown": loadResource("tray-unknown-32.png"),
	}
	u.appIcon = u.icons["app"]
	u.trayIcon = u.icons["tray-unknown"]
	fyneApp.SetIcon(u.appIcon)

	if desk, ok := fyneApp.(desktop.App); ok {
		u.desk = desk
	} else {
		log.Errorf("системный трей недоступен на этой платформе")
	}

	u.statusWin = u.buildStatusWindow()
	u.refreshTray()
	return u
}

func loadResource(name string) fyne.Resource {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return fyne.NewStaticResource(name, nil)
	}
	return fyne.NewStaticResource(name, data)
}

// Run starts the Fyne event loop and blocks until the application quits.
func (u *UI) Run() {
	u.app.Run()
}

// Quit stops the event loop.
func (u *UI) Quit() {
	fyne.Do(func() { u.app.Quit() })
}

// SetAutostartState reflects the registry state in the tray menu.
func (u *UI) SetAutostartState(enabled bool) {
	fyne.Do(func() {
		u.mu.Lock()
		u.autostart = enabled
		u.mu.Unlock()
		u.refreshTray()
	})
}

// SetChecking shows that a check is currently running.
func (u *UI) SetChecking(checking bool) {
	fyne.Do(func() {
		u.mu.Lock()
		u.checking = checking
		u.mu.Unlock()
		if u.statusSpinner != nil {
			if checking {
				u.statusSpinner.Start()
			} else {
				u.statusSpinner.Stop()
			}
		}
		u.refreshTray()
	})
}

// SetNextRun updates the planned time of the next check.
func (u *UI) SetNextRun(next time.Time) {
	fyne.Do(func() {
		u.mu.Lock()
		u.next = next
		u.mu.Unlock()
		u.refreshTray()
		u.refreshStatus()
	})
}

// Restore shows the persisted result of a previous session.
func (u *UI) Restore(st *state.State) {
	if st == nil || st.LastCheck.IsZero() {
		return
	}
	res := checker.Result{
		Status:     checker.Status(st.Status),
		Title:      st.Title,
		Summary:    st.Summary,
		CheckedAt:  st.LastCheck,
		WindowFrom: st.WindowFrom,
	}
	for _, m := range st.Messages {
		res.Messages = append(res.Messages, checker.Message{
			Date:    m.Date,
			From:    m.From,
			Subject: m.Subject,
			Class:   m.Class,
		})
	}
	u.mu.Lock()
	u.last = res
	u.haveResult = true
	u.next = st.NextCheck
	u.mu.Unlock()
	u.applyStatusIcon(res.Status)
	u.refreshTray()
	u.refreshStatus()
}

// SetResult updates the UI with a finished check.
func (u *UI) SetResult(res checker.Result) {
	fyne.Do(func() {
		u.mu.Lock()
		u.last = res
		u.haveResult = true
		u.checking = false
		u.mu.Unlock()
		u.applyStatusIcon(res.Status)
		u.refreshTray()
		u.refreshStatus()
	})
}

func (u *UI) applyStatusIcon(status checker.Status) {
	icon, ok := u.icons["tray-"+string(status)]
	if !ok {
		icon = u.icons["tray-unknown"]
	}
	u.trayIcon = icon
	if u.desk != nil {
		u.desk.SetSystemTrayIcon(icon)
	}
}

// refreshTray rebuilds the tray menu so that it shows the latest state.
func (u *UI) refreshTray() {
	if u.desk == nil {
		return
	}
	u.mu.Lock()
	checking := u.checking
	last := u.last
	have := u.haveResult
	next := u.next
	autostart := u.autostart
	u.mu.Unlock()

	statusText := "Проверка ещё не выполнялась"
	if have {
		statusText = fmt.Sprintf("Последняя проверка: %s — %s", checker.FormatTime(last.CheckedAt), statusLabel(last.Status))
	}
	if checking {
		statusText = "Идёт проверка почты…"
	}
	nextText := "Следующая проверка: —"
	if !next.IsZero() {
		nextText = "Следующая проверка: " + checker.FormatTime(next)
	}

	checkItem := fyne.NewMenuItem("Проверить сейчас", func() {
		if u.cb.CheckNow != nil {
			u.cb.CheckNow()
		}
	})
	checkItem.Disabled = checking

	statusItem := fyne.NewMenuItem(statusText, nil)
	statusItem.Disabled = true
	nextItem := fyne.NewMenuItem(nextText, nil)
	nextItem.Disabled = true

	autostartItem := fyne.NewMenuItem("Запускать с Windows", func() {
		u.toggleAutostart()
	})
	autostartItem.Checked = autostart

	menu := fyne.NewMenu("Chek Backup",
		statusItem,
		nextItem,
		fyne.NewMenuItemSeparator(),
		checkItem,
		fyne.NewMenuItem("Открыть окно состояния", func() { u.ShowStatus() }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Настройки…", func() { u.ShowSettings() }),
		autostartItem,
		fyne.NewMenuItem("Журнал", func() {
			if u.cb.LogPath != nil {
				if err := platform.OpenFile(u.cb.LogPath()); err != nil && u.log != nil {
					u.log.Warnf("не удалось открыть журнал: %v", err)
				}
			}
		}),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Выход", func() {
			if u.cb.Quit != nil {
				u.cb.Quit()
			} else {
				u.app.Quit()
			}
		}),
	)
	u.desk.SetSystemTrayMenu(menu)
	u.desk.SetSystemTrayIcon(u.trayIcon)
	if u.statusWin != nil {
		u.desk.SetSystemTrayWindow(u.statusWin)
	}
}

func (u *UI) toggleAutostart() {
	u.mu.Lock()
	want := !u.autostart
	u.mu.Unlock()
	if err := platform.SetAutostart(want); err != nil {
		u.ShowError("Автозапуск", err)
		return
	}
	u.mu.Lock()
	u.autostart = want
	u.mu.Unlock()
	u.refreshTray()
	if u.log != nil {
		u.log.Infof("автозапуск: %v", want)
	}
}

func statusLabel(status checker.Status) string {
	switch status {
	case checker.StatusOK:
		return "ОК, бэкап выполнен"
	case checker.StatusWarning:
		return "Внимание (нет отчёта о бэкапе)"
	case checker.StatusError:
		return "ОШИБКА: бэкап не выполнен"
	case checker.StatusUnknown:
		return "не удалось проверить почту"
	default:
		return string(status)
	}
}

func statusColor(status checker.Status) color.Color {
	switch status {
	case checker.StatusOK:
		return color.NRGBA{R: 0x1e, G: 0x8e, B: 0x3e, A: 0xff}
	case checker.StatusWarning:
		return color.NRGBA{R: 0xb5, G: 0x6d, B: 0x00, A: 0xff}
	case checker.StatusError:
		return color.NRGBA{R: 0xc0, G: 0x2b, B: 0x1e, A: 0xff}
	default:
		return color.NRGBA{R: 0x5a, G: 0x63, B: 0x70, A: 0xff}
	}
}

// buildStatusWindow creates the window that is opened from the tray icon.
func (u *UI) buildStatusWindow() fyne.Window {
	w := u.app.NewWindow("Chek Backup — состояние")
	w.SetIcon(u.appIcon)

	u.statusTitle = canvas.NewText("Проверка ещё не выполнялась", statusColor(checker.StatusUnknown))
	u.statusTitle.TextSize = 20
	u.statusTitle.TextStyle = fyne.TextStyle{Bold: true}

	u.statusSummary = widget.NewLabel("Приложение следит за письмами о резервном копировании в ящике backup@csl.by.")
	u.statusSummary.Wrapping = fyne.TextWrapWord

	u.statusMeta = widget.NewLabel("")
	u.statusMeta.Wrapping = fyne.TextWrapWord

	u.messageRows = nil
	u.statusMessages = widget.NewList(
		func() int { return len(u.messageRows) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(u.messageRows) {
				return
			}
			obj.(*widget.Label).SetText(u.messageRows[id])
		},
	)
	u.statusMessages.Hide()

	checkBtn := widget.NewButton("Проверить сейчас", func() {
		if u.cb.CheckNow != nil {
			u.cb.CheckNow()
		}
	})
	settingsBtn := widget.NewButton("Настройки", func() { u.ShowSettings() })
	logBtn := widget.NewButton("Журнал", func() {
		if u.cb.LogPath != nil {
			if err := platform.OpenFile(u.cb.LogPath()); err != nil {
				u.ShowError("Журнал", err)
			}
		}
	})
	hideBtn := widget.NewButton("Скрыть", func() { w.Hide() })

	u.statusSpinner = widget.NewProgressBarInfinite()
	u.statusSpinner.Hide()

	header := container.NewVBox(u.statusTitle, u.statusSummary)
	body := container.NewBorder(
		header,
		container.NewVBox(
			container.NewHBox(u.statusSpinner, layout.NewSpacer(), checkBtn, settingsBtn, logBtn, hideBtn),
			u.statusMeta,
		),
		nil, nil,
		u.statusMessages,
	)

	w.SetContent(container.NewPadded(body))
	w.Resize(fyne.NewSize(620, 460))
	w.CenterOnScreen()
	w.SetCloseIntercept(func() {
		u.statusOpen = false
		w.Hide()
	})
	return w
}

// ShowStatus opens the status window.
func (u *UI) ShowStatus() {
	fyne.Do(func() {
		u.statusOpen = true
		u.refreshStatus()
		u.statusWin.Show()
		u.statusWin.RequestFocus()
	})
}

func (u *UI) refreshStatus() {
	if u.statusTitle == nil {
		return
	}
	u.mu.Lock()
	last := u.last
	have := u.haveResult
	next := u.next
	checking := u.checking
	u.mu.Unlock()

	if !have {
		u.statusTitle.Text = "Проверка ещё не выполнялась"
		u.statusTitle.Color = statusColor(checker.StatusUnknown)
		u.statusSummary.SetText(fmt.Sprintf("Ящик: %s на %s. Проверка по расписанию в %s.",
			u.cfg.IMAP.Username, u.cfg.IMAP.Host, u.cfg.Schedule.DailyAt))
		u.statusMeta.SetText("")
		u.messageRows = nil
		u.statusMessages.Refresh()
		u.statusMessages.Hide()
		u.statusTitle.Refresh()
		return
	}

	u.statusTitle.Text = strings.ToUpper(last.Title)
	u.statusTitle.Color = statusColor(last.Status)
	u.statusSummary.SetText(last.Summary)

	meta := []string{
		"Проверено: " + checker.FormatTime(last.CheckedAt),
		"Окно: с " + checker.FormatTime(last.WindowFrom),
	}
	if !next.IsZero() {
		meta = append(meta, "Следующая проверка: "+checker.FormatTime(next))
	}
	if checking {
		meta = append(meta, "Идёт проверка…")
	}
	if len(last.Details) > 0 {
		meta = append(meta, strings.Join(last.Details, "\n"))
	}
	u.statusMeta.SetText(strings.Join(meta, "   •   "))

	rows := make([]string, 0, len(last.Messages))
	for _, m := range last.Messages {
		mark := "•"
		switch m.Class {
		case "success":
			mark = "✔"
		case "failure":
			mark = "✖"
		}
		rows = append(rows, fmt.Sprintf("%s %s — «%s» — %s", mark, checker.FormatTime(m.Date), m.Subject, m.From))
	}
	u.messageRows = rows
	u.statusMessages.Refresh()
	if len(rows) > 0 {
		u.statusMessages.Show()
	} else {
		u.statusMessages.Hide()
	}
	u.statusTitle.Refresh()
}

// ShowError displays an error dialog.
func (u *UI) ShowError(title string, err error) {
	if err == nil {
		return
	}
	if u.log != nil {
		u.log.Errorf("%s: %v", title, err)
	}
	fyne.Do(func() {
		dialog.ShowError(fmt.Errorf("%s: %w", title, err), u.activeWindow())
	})
}

func (u *UI) activeWindow() fyne.Window {
	if u.settingsWin != nil && u.settingsOpen {
		return u.settingsWin
	}
	if u.statusWin != nil && u.statusOpen {
		return u.statusWin
	}
	return nil
}

// ShowInfo displays an informational dialog.
func (u *UI) ShowInfo(title, message string) {
	fyne.Do(func() {
		dialog.ShowInformation(title, message, u.activeWindow())
	})
}

// Notify shows a desktop notification, an optional alert window and an
// optional system sound for a finished check.
func (u *UI) Notify(res checker.Result) {
	u.mu.Lock()
	cfg := u.cfg
	u.mu.Unlock()
	if cfg == nil {
		return
	}
	notify := true
	switch res.Status {
	case checker.StatusOK:
		notify = cfg.Notify.OnSuccess
	case checker.StatusWarning:
		notify = cfg.Notify.OnWarning
	case checker.StatusError, checker.StatusUnknown:
		notify = cfg.Notify.OnError
	}
	if !notify {
		return
	}
	fyne.Do(func() {
		u.app.SendNotification(&fyne.Notification{
			Title:   "Chek Backup: " + res.Title,
			Content: res.Summary,
		})
		if cfg.Notify.AlertWindow && res.Status != checker.StatusOK {
			u.showAlert(res)
		}
	})
	if cfg.Notify.Sound {
		switch res.Status {
		case checker.StatusOK:
			platform.Beep(platform.BeepInfo)
		case checker.StatusWarning:
			platform.Beep(platform.BeepWarning)
		default:
			platform.Beep(platform.BeepError)
		}
	}
}

// showAlert opens (or updates) the alert window for an attention worthy
// result. It must be called from the Fyne goroutine.
func (u *UI) showAlert(res checker.Result) {
	if u.alertWin == nil {
		u.alertWin = u.app.NewWindow("Chek Backup")
		u.alertWin.SetIcon(u.appIcon)
		u.alertWin.SetCloseIntercept(func() { u.alertWin.Hide() })
	}
	u.alertWin.SetTitle("Chek Backup: " + res.Title)

	title := canvas.NewText(strings.ToUpper(res.Title), statusColor(res.Status))
	title.TextSize = 20
	title.TextStyle = fyne.TextStyle{Bold: true}

	summary := widget.NewLabel(res.Summary)
	summary.Wrapping = fyne.TextWrapWord

	details := widget.NewLabel(fmt.Sprintf("Проверено: %s\nОкно: с %s", checker.FormatTime(res.CheckedAt), checker.FormatTime(res.WindowFrom)))
	details.Wrapping = fyne.TextWrapWord

	buttons := container.NewHBox(
		layout.NewSpacer(),
		widget.NewButton("Открыть состояние", func() {
			u.alertWin.Hide()
			u.ShowStatus()
		}),
		widget.NewButton("Настройки", func() {
			u.alertWin.Hide()
			u.ShowSettings()
		}),
		widget.NewButton("Понятно", func() { u.alertWin.Hide() }),
	)

	content := container.NewVBox(
		title,
		container.NewPadded(summary),
		container.NewPadded(details),
		layout.NewSpacer(),
		buttons,
	)
	u.alertWin.SetContent(container.NewPadded(content))
	u.alertWin.Resize(fyne.NewSize(560, 320))
	u.alertWin.CenterOnScreen()
	u.alertWin.Show()
	u.alertWin.RequestFocus()
}

// Settings returns the configuration the UI currently displays.
func (u *UI) Settings() *config.Config {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.cfg
}

// SetConfig replaces the configuration used by the UI.
func (u *UI) SetConfig(cfg *config.Config) {
	u.mu.Lock()
	u.cfg = cfg
	u.mu.Unlock()
	fyne.Do(func() { u.refreshTray() })
}
