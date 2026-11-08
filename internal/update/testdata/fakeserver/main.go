// Command fakeserver stands in for a new MangaVeyra release in the update
// tests. It implements `version`, `migrate status`, `migrate` and `serve`
// with /healthz and /readyz; MVFAKE_MODE makes one step fail
// (migrate_fail, health_fail, wrong_version).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

var version = "1.0.2"

func main() {
	mode := os.Getenv("MVFAKE_MODE")
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	dataDir := os.Getenv("MV_DATA_DIR")
	switch os.Args[1] {
	case "version":
		v := version
		if mode == "wrong_version" {
			v = "9.9.9"
		}
		fmt.Printf("MangaVeyra %s (fake) go\n", v)
	case "migrate":
		if len(os.Args) > 2 && os.Args[2] == "status" {
			fmt.Println("VERSION       FILE              APPLIED")
			fmt.Println("0001_core     0001_core.sql     2026-11-01 10:00:00")
			fmt.Println("0601_fake     0601_fake.sql     pending")
			fmt.Println("0602_fake     0602_fake.sql     pending")
			return
		}
		if mode == "migrate_fail" {
			fmt.Fprintln(os.Stderr, "error: db: migrate 0602_fake.sql: statement 1: write: no space left on device")
			os.Exit(1)
		}
		_ = os.WriteFile(filepath.Join(dataDir, "fake-migrated"), []byte(version), 0o600)
		fmt.Println("applied 2 migration(s): 0601_fake, 0602_fake")
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "", "")
		_ = fs.Bool("dev", false, "")
		_ = fs.Parse(os.Args[2:])
		env := map[string]string{"candidate": os.Getenv("MV_UPDATE_CANDIDATE"), "data": dataDir, "addr": *addr}
		b, _ := json.Marshal(env)
		_ = os.WriteFile(filepath.Join(dataDir, "fake-serve-env"), b, 0o600)
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			if mode == "health_fail" {
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "down"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": version})
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "checks": map[string]any{
				"database": map[string]any{"ok": true}, "jobs": map[string]any{"ok": true, "detail": map[string]int{"workers_online": 2}}}})
		})
		srv := &http.Server{Addr: *addr, Handler: mux}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			_ = srv.Shutdown(context.Background())
		}()
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
