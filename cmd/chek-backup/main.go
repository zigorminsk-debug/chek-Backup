// Command chek-backup watches a mailbox for daily backup reports and warns
// the user when the report is missing or announces a failure. It lives in the
// Windows system tray and is optionally started together with Windows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/zigorminsk-debug/chek-Backup/internal/checker"
	"github.com/zigorminsk-debug/chek-Backup/internal/config"
	"github.com/zigorminsk-debug/chek-Backup/internal/logx"
	"github.com/zigorminsk-debug/chek-Backup/internal/platform"
	"github.com/zigorminsk-debug/chek-Backup/internal/probe"
	"github.com/zigorminsk-debug/chek-Backup/internal/scheduler"
	"github.com/zigorminsk-debug/chek-Backup/internal/state"
	"github.com/zigorminsk-debug/chek-Backup/internal/ui"
)

// version is injected at build time with -X main.version=...
var version = "dev"

func main() {
	autostartFlag := flag.Bool("autostart", false, "программа запущена при входе в Windows")
	showVersion := flag.Bool("version", false, "показать версию и выйти")
	flag.Parse()

	if *showVersion {
		if logPath, err := platform.LogPath(); err == nil {
			if log, logErr := logx.Open(logPath, 5<<20); logErr == nil {
				log.Infof("Chek Backup %s", version)
				_ = log.Close()
			}
		}
		return
	}

	cfgPath, err := platform.ConfigPath()
	statePath, errState := platform.StatePath()
	if err != nil || errState != nil {
		return
	}

	cfg, created, repairedFrom, loadErr := config.LoadEx(cfgPath)
	if loadErr != nil {
		// A corrupt file must not prevent the watcher from starting: keep a
		// backup copy and fall back to the defaults.
		_ = os.Rename(cfgPath, fmt.Sprintf("%s.broken-%s", cfgPath, time.Now().Format("20060102-150405")))
		cfg = config.Default()
		created = true
	}
	migratedHost := repairedFrom
	if created {
		_ = cfg.Save(cfgPath)
	} else if repairedFrom != "" {
		// The server name was repaired (for example the non existing
		// imap.csl.by); persist the fixed value so the user sees it.
		if saveErr := cfg.Save(cfgPath); saveErr != nil {
			fmt.Fprintf(os.Stderr, "не удалось сохранить исправленные настройки: %v\n", saveErr)
		}
	}

	logPath, err := platform.LogPath()
	log := logx.Nop()
	if err == nil {
		if opened, logErr := logx.Open(logPath, int64(cfg.MaxLogSizeMB)<<20); logErr == nil {
			log = opened
		}
	}
	defer func() { _ = log.Close() }()
	log.SetStdout(true)
	log.Infof("Chek Backup %s запускается (автозапуск: %v)", version, *autostartFlag)
	if loadErr != nil {
		log.Errorf("файл настроек повреждён и переименован: %v", loadErr)
	}
	if migratedHost != "" {
		log.Infof("сервер IMAP в настройках исправлен автоматически: %s", migratedHost)
	}

	alreadyRunning, release, err := platform.SingleInstance(platform.AppName)
	if err != nil {
		log.Warnf("не удалось проверить единственность экземпляра: %v", err)
	}
	if alreadyRunning {
		log.Warnf("программа уже запущена, выходим")
		return
	}
	if release != nil {
		defer release()
	}

	c := newController(cfg, cfgPath, statePath, log, *autostartFlag)
	c.firstRun = created
	c.run()
	log.Infof("Chek Backup завершает работу")
}

// controller wires the configuration, the scheduler, the checker and the UI.
type controller struct {
	cfg       *config.Config
	cfgPath   string
	statePath string
	log       *logx.Logger
	ui        *ui.UI
	autostart bool
	firstRun  bool

	mu      sync.Mutex
	checker *checker.Checker
	sched   *scheduler.Scheduler

	checkMu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
}

func newController(cfg *config.Config, cfgPath, statePath string, log *logx.Logger, fromAutostart bool) *controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &controller{
		cfg:       cfg,
		cfgPath:   cfgPath,
		statePath: statePath,
		log:       log,
		autostart: fromAutostart,
		ctx:       ctx,
		cancel:    cancel,
	}
}

func (c *controller) run() {
	chk, err := checker.New(c.cfg, c.log)
	if err != nil {
		c.log.Errorf("некорректные правила проверки: %v", err)
		if fallback, fallbackErr := checker.New(config.Default(), c.log); fallbackErr == nil {
			chk = fallback
		}
	}
	c.mu.Lock()
	c.checker = chk
	c.mu.Unlock()

	c.ui = ui.New(c.cfg, c.log, ui.Callbacks{
		CheckNow:   func() { go c.checkNow("вручную") },
		SaveConfig: c.saveConfig,
		TestConfig: c.testConfig,
		Diagnose:   c.diagnose,
		Quit:       c.quit,
		LogPath:    func() string { return c.log.Path() },
		ConfigPath: func() string { return c.cfgPath },
	})

	// Show the outcome of the previous run so the tray icon is meaningful
	// right after a restart.
	c.ui.Restore(state.Load(c.statePath))

	if err := platform.SetAutostart(c.cfg.Autostart); err != nil {
		c.log.Warnf("не удалось настроить автозапуск: %v", err)
	}
	c.ui.SetAutostartState(platform.AutostartEnabled())

	c.startScheduler()

	if c.firstRun {
		// First start: let the user fill in the mailbox credentials.
		go func() {
			select {
			case <-time.After(1500 * time.Millisecond):
				c.ui.ShowSettings()
			case <-c.ctx.Done():
			}
		}()
	}

	if c.cfg.Schedule.RunOnStartup {
		delay := 10 * time.Second
		if c.autostart {
			// Give the network stack time to come up after a reboot.
			delay = 45 * time.Second
		}
		go func() {
			select {
			case <-time.After(delay):
				c.checkNow("запуск")
			case <-c.ctx.Done():
			}
		}()
	}

	c.ui.Run()

	c.cancel()
	c.mu.Lock()
	sched := c.sched
	c.mu.Unlock()
	if sched != nil {
		sched.Stop()
	}
}

// startScheduler (re)creates the daily scheduler from the current config.
func (c *controller) startScheduler() {
	c.mu.Lock()
	old := c.sched
	c.sched = nil
	c.mu.Unlock()
	if old != nil {
		old.Stop()
	}

	hour, minute, err := c.cfg.Schedule.Time()
	if err != nil {
		c.log.Errorf("расписание не задано: %v", err)
		return
	}
	sched := scheduler.New(hour, minute)
	sched.Location = time.Local
	sched.OnRun = func(ctx context.Context) {
		c.checkNow("по расписанию")
	}
	sched.Start(c.ctx)

	c.mu.Lock()
	c.sched = sched
	c.mu.Unlock()
	c.log.Infof("следующая проверка: %s", sched.NextRun().Format("02.01.2006 15:04"))
	c.ui.SetNextRun(sched.NextRun())
}

// checkNow performs one mailbox check. Concurrent calls are skipped.
func (c *controller) checkNow(reason string) {
	if !c.checkMu.TryLock() {
		c.log.Infof("проверка (%s) пропущена: предыдущая ещё выполняется", reason)
		return
	}
	defer c.checkMu.Unlock()

	c.log.Infof("запуск проверки (%s)", reason)
	c.ui.SetChecking(true)
	defer c.ui.SetChecking(false)

	c.mu.Lock()
	chk := c.checker
	cfg := c.cfg
	sched := c.sched
	c.mu.Unlock()
	if chk == nil {
		c.log.Errorf("проверка невозможна: проверяльщик не инициализирован")
		return
	}

	timeout := cfg.Timeout() + 30*time.Second
	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	res := chk.Run(ctx)

	c.ui.SetResult(res)
	c.saveState(res, sched)
	c.ui.Notify(res)
}

func (c *controller) saveState(res checker.Result, sched *scheduler.Scheduler) {
	st := state.FromResult(res)
	if sched != nil {
		st.NextCheck = sched.NextRun()
	}
	if err := st.Save(c.statePath); err != nil {
		c.log.Warnf("не удалось сохранить состояние: %v", err)
	}
}

// saveConfig persists a new configuration and applies it immediately.
func (c *controller) saveConfig(cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(c.cfgPath); err != nil {
		return err
	}
	chk, err := checker.New(cfg, c.log)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg = cfg
	c.checker = chk
	c.mu.Unlock()

	c.log.SetMaxSize(int64(cfg.MaxLogSizeMB) << 20)
	c.log.Infof("настройки обновлены: %s@%s, проверка в %s, окно %d ч",
		cfg.IMAP.Username, cfg.IMAP.Host, cfg.Schedule.DailyAt, cfg.Schedule.LookbackHours)
	c.startScheduler()
	return nil
}

// diagnose reports on which stage (DNS, TCP, TLS, IMAP) the connection fails.
func (c *controller) diagnose(cfg *config.Config) *probe.Report {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout()+5*time.Second)
	defer cancel()
	return checker.Diagnose(ctx, cfg)
}

// testConfig verifies the mailbox connection with the given settings.
func (c *controller) testConfig(cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout()+15*time.Second)
	defer cancel()
	mailbox, closeFn, err := checker.IMAPOpener(cfg)(ctx)
	if err != nil {
		return err
	}
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	if _, err := mailbox.Headers(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		return err
	}
	return nil
}

func (c *controller) quit() {
	c.cancel()
	c.ui.Quit()
}
