// Package app wires the server together: config → logger → database and
// migrations → storage → secrets → settings → auth → i18n → jobs → realtime
// hub → notifications → modules → routes → http.Server, plus the background
// loops and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"github.com/MaramHarsha/MangaVeyra/internal/version"
	"github.com/MaramHarsha/MangaVeyra/internal/ws"
)

// Options tune wiring (mostly for tests and CLI commands).
type Options struct {
	// Logger overrides the configured logger.
	Logger *slog.Logger
	// LogOutput is where the default logger writes (default os.Stderr).
	LogOutput io.Writer
	// HashParams overrides argon2id parameters (tests use auth.FastHashParams).
	HashParams *auth.HashParams
	// SkipMigrate opens the database without applying migrations.
	SkipMigrate bool
	// ExecutorSize is the number of Go-executor runners (default 4).
	ExecutorSize int
}

// NewLogger builds the configured slog logger: text in dev mode, JSON otherwise.
func NewLogger(cfg *config.Config, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stderr
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	ho := &slog.HandlerOptions{Level: level}
	if cfg.Dev {
		return slog.New(slog.NewTextHandler(out, ho))
	}
	return slog.New(slog.NewJSONHandler(out, ho))
}

// NewDeps opens every shared service. Call Close on the returned closer when done.
func NewDeps(ctx context.Context, cfg *config.Config, opts Options) (*core.Deps, func() error, error) {
	log := opts.Logger
	if log == nil {
		log = NewLogger(cfg, opts.LogOutput)
	}
	httpx.Logger.Store(log)
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, nil, err
	}
	dialect, dsn, err := cfg.Database()
	if err != nil {
		return nil, nil, err
	}
	database, err := db.Open(ctx, dialect, dsn, db.Options{})
	if err != nil {
		return nil, nil, err
	}
	closers := []func() error{database.Close}
	closeAll := func() error {
		var errs []error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = append(errs, closers[i]())
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (*core.Deps, func() error, error) {
		_ = closeAll()
		return nil, nil, err
	}
	if !opts.SkipMigrate {
		applied, err := database.Migrate(ctx)
		if err != nil {
			return fail(err)
		}
		if len(applied) > 0 {
			log.Info("database migrated", "dialect", dialect, "applied", applied)
		}
	}

	var store storage.Store
	switch cfg.Storage {
	case "s3":
		store, err = storage.NewS3(ctx, storage.S3Options{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket,
			AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey, UseSSL: cfg.S3.UseSSL, PathStyle: cfg.S3.PathStyle,
			Prefix: cfg.S3.Prefix, CreateBucket: true})
	default:
		store, err = storage.NewLocal(cfg.MediaDir())
	}
	if err != nil {
		return fail(err)
	}

	box, err := secrets.Load(cfg.MasterKey, cfg.SecretKeyPath())
	if err != nil {
		return fail(err)
	}
	st := settings.New(database)

	authSvc := auth.New(database, st, box, auth.Options{
		PublicURL: cfg.PublicURL, WorkerToken: cfg.WorkerToken, SecureCookies: cfg.SecureCookies(),
		HashParams: opts.HashParams, Logger: log,
	})

	bundle := i18n.Default()
	if err := bundle.AddDir(cfg.LocaleDir()); err != nil {
		log.Warn("community translations could not be loaded", "dir", cfg.LocaleDir(), "err", err)
	}

	queue := jobs.New(database, st, nil, jobs.Options{Logger: log})
	hub := ws.New(database, log)
	if cfg.PublicURL != "" {
		if u, err := urlHost(cfg.PublicURL); err == nil {
			hub.OriginPatterns = []string{u}
		}
	}
	queue.SetPublisher(hub)
	hub.Authorize("job:", queue.Authorizer())
	// Open sockets re-check their session/token and account status periodically,
	// so signing out everywhere, revoking a token or suspending an account also
	// ends realtime access (and role changes reach topic authorizers).
	hub.Revalidate = func(ctx context.Context, id httpx.Identity) (httpx.Identity, error) {
		nid, err := authSvc.Revalidate(ctx, id)
		if errors.Is(err, auth.ErrTokenInvalid) {
			return id, fmt.Errorf("%w: %w", ws.ErrSessionEnded, err)
		}
		return nid, err
	}

	var mailer *notify.SMTPMailer
	if cfg.SMTP.Enabled() {
		mailer, err = notify.NewSMTPMailer(notify.SMTPConfig{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username,
			Password: cfg.SMTP.Password, From: cfg.SMTP.From, TLS: cfg.SMTP.TLS})
		if err != nil {
			return fail(err)
		}
	}
	notifier := notify.New(database, st, box, hub, notify.Options{PublicURL: cfg.PublicURL, Mailer: mailer,
		AllowPrivatePushEndpoints: cfg.AllowPrivateProviderURLs, Logger: log})
	authSvc.SetMailer(notifier)
	closers = append(closers, func() error { notifier.Wait(); return nil })

	size := opts.ExecutorSize
	if size <= 0 {
		size = 4
	}
	d := &core.Deps{
		Cfg: cfg, Log: log, DB: database, Store: store, Media: storage.NewSigner(box.Derive("media-urls")), Secrets: box,
		Settings: st, Auth: authSvc, I18n: bundle, Jobs: queue, Executor: jobs.NewPool(queue, size, log), Hub: hub,
		Notify: notifier, Version: version.Get(),
	}
	queue.OnFinish(jobNotifier(d))
	return d, closeAll, nil
}

// jobNotifier tells members about failed jobs and finished batch jobs.
func jobNotifier(d *core.Deps) func(context.Context, *jobs.Job) {
	return func(ctx context.Context, j *jobs.Job) {
		if j.UserID == "" {
			return
		}
		var kind, key string
		switch {
		case j.State == jobs.StateFailed:
			kind, key = notify.KindJobFailed, "notify.kind.job_failed"
		case j.State == jobs.StateDone && j.Batch:
			kind, key = notify.KindJobDone, "notify.kind.job_done"
		default:
			return
		}
		body := j.Type
		if j.Error != nil {
			body += ": " + j.Error.Message
		}
		if _, err := d.Notify.Notify(ctx, notify.Input{UserID: j.UserID, Kind: kind, Title: i18n.T(ctx, key), Body: body,
			Link: "/queue?job=" + j.ID, Data: map[string]any{"job_id": j.ID}, GroupKey: kind}); err != nil {
			d.Log.Warn("notify job result", "job", j.ID, "err", err)
		}
	}
}

func urlHost(raw string) (string, error) {
	u, err := parseURL(raw)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

// App is a wired server.
type App struct {
	Deps    *core.Deps
	Router  *core.Router
	Handler http.Handler
	Modules []core.Module
	// JobAPI serves the worker protocol and the job API.
	JobAPI *jobs.API

	closeDeps func() error
	loops     sync.WaitGroup
	cancel    context.CancelFunc
}

// New builds the application: services, modules and the HTTP handler.
func New(ctx context.Context, cfg *config.Config, opts Options) (*App, error) {
	d, closeDeps, err := NewDeps(ctx, cfg, opts)
	if err != nil {
		return nil, err
	}
	a := &App{Deps: d, Router: core.NewRouter(), closeDeps: closeDeps}
	a.JobAPI = &jobs.API{Q: d.Jobs, Store: d.Store, Signer: d.Media, PublicURL: cfg.PublicURL}

	a.Modules = append(foundationModules(a), FeatureModules(d)...)
	for _, m := range a.Modules {
		if in, ok := m.(core.Initer); ok {
			if err := in.Init(ctx); err != nil {
				_ = closeDeps()
				return nil, fmt.Errorf("app: init %s: %w", m.Name(), err)
			}
		}
	}
	for _, m := range a.Modules {
		if jh, ok := m.(core.JobHandlers); ok {
			for typ, h := range jh.JobHandlers() {
				d.Executor.Register(typ, h)
			}
		}
		m.Routes(a.Router)
	}
	if manifests, ok := core.Use[jobs.ManifestLookup](d); ok {
		a.JobAPI.Manifests = manifests
	}
	a.Handler = core.Stack(d)(a.Router)
	return a, nil
}

// Start launches the background loops (job maintenance, Go executor,
// session cleanup, digests, module starters). They stop when ctx ends or Close is called.
func (a *App) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	d := a.Deps
	run := func(name string, fn func(context.Context) error) {
		a.loops.Add(1)
		go func() {
			defer a.loops.Done()
			defer func() {
				if p := recover(); p != nil {
					d.Log.Error("background loop panicked", "loop", name, "panic", p)
				}
			}()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				d.Log.Error("background loop stopped", "loop", name, "err", err)
			}
		}()
	}
	run("jobs.maintenance", func(ctx context.Context) error {
		every(ctx, 10*time.Second, func() {
			if n, err := d.Jobs.RequeueExpired(ctx); err != nil {
				d.Log.Warn("requeue expired jobs", "err", err)
			} else if n > 0 {
				d.Log.Info("requeued jobs with expired leases", "count", n)
			}
			if n, err := d.Jobs.MarkOfflineWorkers(ctx); err != nil {
				d.Log.Warn("mark offline workers", "err", err)
			} else if n > 0 {
				d.Log.Info("workers went offline", "count", n)
			}
		})
		return nil
	})
	run("jobs.executor", d.Executor.Start)
	run("auth.cleanup", func(ctx context.Context) error {
		every(ctx, time.Hour, func() {
			if err := d.Auth.CleanupExpired(ctx); err != nil {
				d.Log.Warn("session cleanup", "err", err)
			}
		})
		return nil
	})
	// Module starters (including notify's digest loop).
	for _, m := range a.Modules {
		if s, ok := m.(core.Starter); ok {
			run(m.Name(), s.Start)
		}
	}
}

func every(ctx context.Context, interval time.Duration, fn func()) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

// Close stops background loops, closes realtime connections and the database.
func (a *App) Close() error {
	a.Deps.Hub.Close()
	if a.cancel != nil {
		a.cancel()
	}
	a.loops.Wait()
	return a.closeDeps()
}

// Server returns an http.Server for the app with production timeouts.
func (a *App) Server() *http.Server {
	return &http.Server{
		Addr:              a.Deps.Cfg.Addr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(a.Deps.Log.Handler(), slog.LevelWarn),
	}
}

// Run serves HTTP until ctx is canceled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	srv := a.Server()
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	a.Start(ctx)
	d := a.Deps
	d.Log.Info("MangaVeyra listening", "addr", ln.Addr().String(), "version", d.Version.Version,
		"db", string(d.DB.Dialect()), "storage", d.Store.Kind(), "email", d.Notify.EmailEnabled())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		_ = a.Close()
		return err
	case <-ctx.Done():
	}
	d.Log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), d.Cfg.ShutdownTimeout)
	defer cancel()
	d.Hub.Close()
	shutdownErr := srv.Shutdown(sctx)
	closeErr := a.Close()
	if shutdownErr != nil && !errors.Is(shutdownErr, http.ErrServerClosed) {
		return shutdownErr
	}
	return closeErr
}
