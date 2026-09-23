// Command worldstub runs a DEVELOPMENT-ONLY fake of the World public API
// (sdk/api/world.openapi.yaml). It is not the real World platform: it keeps
// everything in memory, signs with publicly known keys, runs fake automated
// checks and never handles payments. Use it to develop and test MangaVeyra's
// World module without an account on world.mangaveyra.org.
//
// Usage:
//
//	go run ./cmd/worldstub --seed --auto-approve
//
// Flags:
//
//	--addr            listen address (default 127.0.0.1:8090)
//	--public-url      base URL in links it returns (default http://<addr>)
//	--seed            load demo accounts, catalogue items and libraries
//	--auto-approve    approve device codes immediately (as --auto-approve-as)
//	--auto-approve-as seeded handle to link as, or "new" (default jerry-ink with --seed)
//	--check-delay     how long automated checks stay pending (default 2s)
//	--review-delay    approve clean submissions after this long; 0 = manual review at /_stub
//	--poll-interval   device-flow polling interval (default 5s)
//	--rate-limit      API requests per client per minute (default 600)
//	--max-upload      largest accepted submission in bytes (default 256 MiB)
//	--log             log one line per request
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MaramHarsha/MangaVeyra/cmd/worldstub/stub"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "worldstub:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("worldstub", flag.ContinueOnError)
	addr := fs.String("addr", stub.DefaultAddr, "listen address")
	publicURL := fs.String("public-url", "", "base URL used in returned links (default http://<addr>)")
	seed := fs.Bool("seed", false, "load demo accounts, catalogue and libraries (original sample content)")
	autoApprove := fs.Bool("auto-approve", false, "approve device codes as soon as they are issued")
	autoApproveAs := fs.String("auto-approve-as", "", `seeded handle used by --auto-approve, or "new" (default jerry-ink with --seed, else new)`)
	checkDelay := fs.Duration("check-delay", 2*time.Second, "how long automated checks stay pending")
	reviewDelay := fs.Duration("review-delay", 0, "approve submissions whose checks passed after this long (0: manual review at /_stub)")
	pollInterval := fs.Duration("poll-interval", 5*time.Second, "device flow polling interval")
	rateLimit := fs.Int("rate-limit", 600, "API requests per client per minute")
	maxUpload := fs.Int64("max-upload", 256<<20, "largest accepted submission upload in bytes")
	logRequests := fs.Bool("log", false, "log one line per request")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "worldstub: DEVELOPMENT-ONLY fake World API server (not the real platform).")
		fmt.Fprintln(fs.Output(), "\nUsage: worldstub [flags]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *checkDelay < 0 || *reviewDelay < 0 || *pollInterval < time.Second || *rateLimit < 1 || *maxUpload < 1 {
		return errors.New("durations must not be negative, --poll-interval must be at least 1s, --rate-limit and --max-upload positive")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	opts := stub.Options{
		PublicURL: *publicURL, Seed: *seed, AutoApprove: *autoApprove, AutoApproveAs: *autoApproveAs,
		CheckDelay: *checkDelay, ReviewDelay: *reviewDelay, PollInterval: *pollInterval,
		RateLimit: *rateLimit, MaxUpload: *maxUpload,
	}
	if *checkDelay == 0 {
		opts.CheckDelay = -1 // stub.Options: negative completes checks immediately

	}
	if *logRequests {
		opts.Logger = logger
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if opts.PublicURL == "" {
		opts.PublicURL = "http://" + ln.Addr().String()
	}
	s, err := stub.New(opts)
	if err != nil {
		ln.Close()
		return err
	}

	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	logger.Warn("DEVELOPMENT STUB — not the real World platform; in-memory, publicly known keys")
	logger.Info("worldstub listening", "url", opts.PublicURL, "control_panel", opts.PublicURL+"/_stub",
		"seed", *seed, "auto_approve", *autoApprove, "review_delay", reviewDelay.String())

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
