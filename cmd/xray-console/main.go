// Command xray-console is the panel: one binary with the API, the web
// interface and the recovery commands.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"xray-console/internal/alerts"
	"xray-console/internal/auth"
	"xray-console/internal/chains"
	"xray-console/internal/config"
	"xray-console/internal/devices"
	"xray-console/internal/events"
	"xray-console/internal/httpapi"
	"xray-console/internal/install"
	"xray-console/internal/lapse"
	"xray-console/internal/localclient"
	"xray-console/internal/ops"
	"xray-console/internal/planner"
	"xray-console/internal/probe"
	"xray-console/internal/rulesets"
	"xray-console/internal/secrets"
	"xray-console/internal/servers"
	"xray-console/internal/sshkeys"
	"xray-console/internal/sshx"
	"xray-console/internal/store"
	"xray-console/internal/subs"
	"xray-console/internal/templates"
	"xray-console/internal/traffic"
	"xray-console/internal/webui"
	"xray-console/internal/xray"

	// The zone names themselves, for a machine that has no zone database —
	// a minimal container — and would otherwise know only UTC.
	_ "time/tzdata"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

const usage = `Xray Console — панель управления серверами VLESS + REALITY + XHTTP

Использование:
  xray-console [serve] [флаги]     запустить панель
  xray-console reset-password      задать новый пароль администратора
  xray-console version             показать версию

Флаги serve:
  --listen адрес           где слушать (по умолчанию 127.0.0.1:8080, env XRAY_CONSOLE_LISTEN)
  --data каталог           где хранить базу и ключ (по умолчанию ./data, env XRAY_CONSOLE_DATA)
  --trusted-proxies список адреса обратных прокси через запятую (env XRAY_CONSOLE_TRUSTED_PROXIES)
  --check-url адрес        что запрашивать через новый сервер при проверке соединения
                           (по умолчанию https://www.gstatic.com/generate_204, env XRAY_CONSOLE_CHECK_URL)
  --check-interval время   как часто проверять доступность серверов (по умолчанию 1m0s)
  --traffic-interval время как часто снимать счётчики трафика с серверов (по умолчанию 5m0s)
  --timezone пояс          часовой пояс панели, например Europe/Moscow: по нему идут тихие часы
                           оповещений и сутки на графиках (по умолчанию пояс системы, env XRAY_CONSOLE_TZ)
  --debug                  подробный журнал

Флаги reset-password:
  --data каталог           тот же каталог, что у запущенной панели
  --disable-2fa            заодно отключить двухфакторный вход
`

func main() {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}

	var err error
	switch command {
	case "serve":
		err = runServe(args)
	case "reset-password":
		err = runResetPassword(args)
	case "version":
		fmt.Println(version)
	case "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", command, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs
}

func runServe(args []string) error {
	fs := newFlagSet("serve")
	listen := fs.String("listen", envOr("XRAY_CONSOLE_LISTEN", "127.0.0.1:8080"), "")
	dataDir := fs.String("data", envOr("XRAY_CONSOLE_DATA", "./data"), "")
	proxies := fs.String("trusted-proxies", os.Getenv("XRAY_CONSOLE_TRUSTED_PROXIES"), "")
	checkURL := fs.String("check-url", envOr("XRAY_CONSOLE_CHECK_URL", install.DefaultCheckURL), "")
	checkInterval := fs.Duration("check-interval", probe.DefaultInterval, "")
	trafficInterval := fs.Duration("traffic-interval", 5*time.Minute, "")
	timezone := fs.String("timezone", os.Getenv("XRAY_CONSOLE_TZ"), "")
	debug := fs.Bool("debug", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	trusted, err := config.ParseTrustedProxies(*proxies)
	if err != nil {
		return err
	}
	// The panel's own clock: quiet hours and the days of the graphs go by
	// it. A machine's zone is whatever it was installed with — in a
	// container, usually none at all, which is UTC — so it can be named.
	// Set once, before anything reads the time.
	if *timezone != "" {
		zone, err := time.LoadLocation(*timezone)
		if err != nil {
			return fmt.Errorf("--timezone %q: такого часового пояса нет; нужно имя вида Europe/Moscow", *timezone)
		}
		time.Local = zone
	}
	// The panel is made to sit behind a reverse proxy on its own machine. A
	// panel that listens only there can be reached by nothing else, so the
	// proxy is believed without being named: left unnamed it would make every
	// visitor the same address — one lockout for all — and the session
	// cookie would go out without "Secure".
	proxyAssumed := false
	if len(trusted) == 0 && config.ListensOnLoopback(*listen) {
		trusted, proxyAssumed = config.Loopback(), true
	}
	cfg := config.Config{Listen: *listen, DataDir: *dataDir, TrustedProxies: trusted}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	box, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}
	authService := auth.NewService(db, box)

	// Jobs get their own context: they must outlive the request that started
	// them, and on shutdown they are stopped and waited for.
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	manager := ops.NewManager(jobsCtx, db, logger)
	interrupted, err := manager.RecoverInterrupted(ctx)
	if err != nil {
		return err
	}
	serverStore := servers.NewStore(db, box)
	keyStore := sshkeys.NewStore(db, box)
	templateStore := templates.NewStore(db)
	if err := templateStore.EnsureDefault(ctx); err != nil {
		return err
	}
	// Servers added before templates existed take the default one.
	defaultTemplate, err := templateStore.DefaultID(ctx)
	if err != nil {
		return err
	}
	if err := serverStore.AdoptDefaultTemplate(ctx, defaultTemplate); err != nil {
		return err
	}
	chainStore := chains.NewStore(db, box)
	configPlanner := planner.New(serverStore, templateStore, chainStore)
	ruleSetStore := rulesets.NewStore(db)
	subStore := subs.NewStore(db, box)
	configPlanner.RuleSets, configPlanner.Subs = ruleSetStore, subStore
	// Links of a chain watch the next link with the same request the panel
	// uses for its own checks.
	configPlanner.ProbeURL, configPlanner.ProbeInterval = *checkURL, *checkInterval
	releases := xray.NewReleases(filepath.Join(cfg.DataDir, "cache", "xray"))
	installer := install.New(serverStore, keyStore, templateStore, configPlanner, releases, manager, logger)
	if err := installer.SetCheckURL(*checkURL); err != nil {
		return err
	}

	eventStore := events.NewStore(db, logger)
	probeStore := probe.NewStore(db)
	installer.Events = eventStore
	installer.ForgetChecks = probeStore.Forget
	prober := probe.NewProber(serverStore, probeStore, eventStore, manager, logger)
	if prober.Target, err = probe.ParseTarget(*checkURL); err != nil {
		return err
	}
	prober.Interval = *checkInterval
	prober.Chains = chainStore
	// A subscription's end date has to reach the server without anyone there
	// to apply it.
	sweeper := &lapse.Sweeper{
		Subs: subStore, RuleSets: ruleSetStore, Servers: serverStore,
		Plan: configPlanner.For, Busy: manager.Busy, Log: logger,
		Apply: func(ctx context.Context, serverID int64) error {
			_, err := installer.Apply(ctx, serverID)
			return err
		},
	}
	trafficStore := traffic.NewStore(db)
	collector := &traffic.Collector{
		Servers: serverStore, Store: trafficStore, Query: installer.TrafficCounters,
		Busy: manager.Busy, Interval: *trafficInterval, Log: logger,
	}
	alertStore := alerts.NewStore(db, box)
	notifier := &alerts.Notifier{
		Store: alertStore, Servers: serverStore, State: probeStore.StateOf,
		Latest: installer.Latest, Outdated: installer.Outdated,
		Client: &http.Client{Timeout: 20 * time.Second}, Log: logger,
	}
	// Clients the panel sets up itself: devices over SSH, and one inside the
	// panel for the computer it runs on. To the servers each is a user of a
	// rule set, next to the subscriptions.
	deviceStore := devices.NewStore(db, box)
	if err := deviceStore.RecoverInterrupted(ctx); err != nil {
		return err
	}
	deviceManager := &devices.Manager{
		Store: deviceStore, Keys: keyStore, Releases: releases, RuleSets: ruleSetStore, Servers: serverStore,
		Latest: installer.Latest, Log: logger, Ops: manager,
		EnsureUser: func(ctx context.Context, serverID int64, userID string) error {
			err := installer.EnsureUser(ctx, serverID, userID)
			if errors.Is(err, install.ErrOtherChanges) {
				return devices.ErrEntryPending
			}
			return err
		},
		Request: func(ctx context.Context, client *sshx.Client, proxyAddr, user, password string) (int, error) {
			var auth *install.SOCKSAuth
			if user != "" {
				auth = &install.SOCKSAuth{User: user, Password: password}
			}
			return install.RequestThroughSOCKS(ctx, client, proxyAddr, installer.Target(), auth)
		},
	}
	localClient := &localclient.Client{
		DB: db, Box: box, RuleSets: ruleSetStore, Servers: serverStore, Target: prober.Target, Log: logger,
	}
	configPlanner.Members = []planner.Members{deviceStore.Users, localClient.Users}
	if err := recoverServers(ctx, serverStore); err != nil {
		return err
	}
	installer.Interrupted(ctx, interrupted)

	server := &http.Server{
		Handler: httpapi.New(httpapi.Options{
			Auth:           authService,
			Servers:        serverStore,
			Keys:           keyStore,
			Templates:      templateStore,
			Chains:         chainStore,
			RuleSets:       ruleSetStore,
			Subs:           subStore,
			Planner:        configPlanner,
			Probes:         probeStore,
			Prober:         prober,
			Events:         eventStore,
			Traffic:        trafficStore,
			Alerts:         alertStore,
			Notifier:       notifier,
			Devices:        deviceManager,
			Local:          localClient,
			Ops:            manager,
			Installer:      installer,
			Logger:         logger,
			UI:             webui.FS(),
			Version:        version,
			TrustedProxies: cfg.TrustedProxies,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The slowest answers — a round of checks, a journal read over SSH —
		// take a minute or so; nothing takes this long honestly.
		WriteTimeout:   3 * time.Minute,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 64 << 10,
	}

	// Listen before announcing: the setup link below must not be printed for a
	// port the process then fails to bind.
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	logger.Info("panel is listening", "addr", listener.Addr().String(), "version", version, "data", cfg.DataDir)
	logger.Info("the panel's clock", "zone", time.Local.String(), "now", time.Now().Format("2006-01-02 15:04 -07:00"))
	switch {
	case proxyAssumed:
		logger.Info("listening on this machine only: a reverse proxy here is believed about who is asking and over what (X-Forwarded-For, X-Forwarded-Proto); it must set both")
	case !config.ListensOnLoopback(cfg.Listen) && len(cfg.TrustedProxies) == 0:
		logger.Warn("the panel is reachable from other machines over plain HTTP: passwords, and the root passwords of servers being added, travel unencrypted. Listen on 127.0.0.1 and put a reverse proxy with TLS in front")
	case !config.ListensOnLoopback(cfg.Listen):
		logger.Warn("the panel is reachable from other machines over plain HTTP, not only by its reverse proxy: make sure nothing but the proxy can connect to this port")
	}

	if err := announceSetup(ctx, authService, listener.Addr()); err != nil {
		return err
	}

	go pruneLoop(ctx, authService, eventStore, manager, logger)
	// Checks stop with the panel, not with a request: same lifetime as jobs.
	go prober.Run(jobsCtx)
	go sweeper.Run(jobsCtx)
	go collector.Run(jobsCtx)
	go notifier.Run(jobsCtx)
	go deviceManager.Run(jobsCtx, time.Minute)
	go localClient.Run(jobsCtx)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = server.Shutdown(shutdownCtx)
	// Stop running jobs and let them record where they stopped, so they can be
	// retried after the restart.
	stopJobs()
	manager.Wait()
	deviceManager.Wait()
	return err
}

// recoverServers puts servers whose job died with the previous process into
// the state that job's record now shows.
func recoverServers(ctx context.Context, store *servers.Store) error {
	list, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, srv := range list {
		if srv.Status != servers.StatusInstalling {
			continue
		}
		if err := store.SetStatus(ctx, srv.ID, servers.StatusFailed); err != nil {
			return err
		}
	}
	return nil
}

// announceSetup prints the one-time link for creating the administrator, for
// as long as there is none.
func announceSetup(ctx context.Context, authService *auth.Service, addr net.Addr) error {
	required, err := authService.SetupRequired(ctx)
	if err != nil || !required {
		return err
	}
	token, err := authService.SetupToken(ctx)
	if err != nil {
		return err
	}

	host, port, _ := net.SplitHostPort(addr.String())
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "<адрес-сервера>"
	} else if ip.To4() == nil {
		host = "[" + host + "]"
	}
	fmt.Fprintf(os.Stderr, `
  Администратор ещё не создан. Откройте ссылку и задайте имя и пароль:

    http://%s:%s/setup#token=%s

  Ссылка одноразовая. Код из неё: %s

`, host, port, token, token)
	return nil
}

// keptOperations is how long a finished job and its log stay, like the
// checks' own history.
const keptOperations = 90 * 24 * time.Hour

func pruneLoop(ctx context.Context, authService *auth.Service, eventStore *events.Store, jobs *ops.Manager, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := authService.Prune(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("could not prune sessions", "err", err)
		}
		if err := eventStore.Prune(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("could not prune events", "err", err)
		}
		if err := jobs.Prune(ctx, time.Now().Add(-keptOperations)); err != nil && ctx.Err() == nil {
			logger.Warn("could not prune finished jobs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runResetPassword(args []string) error {
	fs := newFlagSet("reset-password")
	dataDir := fs.String("data", envOr("XRAY_CONSOLE_DATA", "./data"), "")
	disableTOTP := fs.Bool("disable-2fa", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Refuse to create an empty database in the wrong directory and then
	// report that there is no administrator in it.
	if _, err := os.Stat(*dataDir); err != nil {
		return fmt.Errorf("каталог данных %s не найден — укажите тот же --data, что у панели", *dataDir)
	}

	ctx := context.Background()
	db, err := store.Open(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	box, err := secrets.LoadOrCreate(*dataDir)
	if err != nil {
		return err
	}
	authService := auth.NewService(db, box)

	password, err := readNewPassword()
	if err != nil {
		return err
	}

	err = authService.ResetPassword(ctx, password, *disableTOTP)
	switch {
	case errors.Is(err, auth.ErrSetupRequired):
		return errors.New("администратор ещё не создан — откройте панель в браузере")
	case errors.Is(err, auth.ErrPasswordTooShort):
		return fmt.Errorf("пароль должен быть не короче %d символов", auth.MinPasswordLength)
	case err != nil:
		return err
	}

	fmt.Println("Пароль изменён. Все открытые сеансы завершены, блокировки входа сняты.")
	if *disableTOTP {
		fmt.Println("Двухфакторный вход отключён.")
	}
	return nil
}

func readNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Piped in (a script, a password manager): one line, no confirmation.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("пароль не получен со стандартного ввода")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	fmt.Fprintf(os.Stderr, "Новый пароль (не короче %d символов): ", auth.MinPasswordLength)
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Пароль ещё раз: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("пароли не совпадают")
	}
	return string(first), nil
}
