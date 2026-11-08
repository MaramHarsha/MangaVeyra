// Command mangaveyra is the MangaVeyra server and admin CLI.
//
//	mangaveyra serve                     run the server (SQLite in ./data, :8080)
//	mangaveyra migrate [status]          apply or list database migrations
//	mangaveyra user create|reset-password|set-role ...
//	mangaveyra seed --demo               create the original demo content
//	mangaveyra mcp-stdio                 MCP over stdin/stdout for local agents (bridges to /mcp)
//	mangaveyra backup ...                create, list or restore backups
//	mangaveyra version
//
// Every command accepts --config <file.yaml>; MV_* environment variables override it.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/MaramHarsha/MangaVeyra/internal/app"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/backup"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/mcp"
	"github.com/MaramHarsha/MangaVeyra/internal/seed"
	"github.com/MaramHarsha/MangaVeyra/internal/version"
)

const usage = `MangaVeyra — open-source, self-hosted AI manga studio

Usage:
  mangaveyra <command> [flags]

Commands:
  serve                              Run the server
  migrate [status]                   Apply pending database migrations (or list them)
  user create                        Create an account (--username --email --password --role admin|creator|reader)
  user reset-password                Set a new password (--user <username|email> [--password])
  user set-role                      Change a site role (--user --role)
  seed [--demo]                      Create the demo content
  mcp-stdio [--url --token mva_...]  Bridge MCP over stdin/stdout to a server
  backup <create|list|restore> ...   Manage backups
  version                            Print version information

Common flags:
  --config <file>                    YAML config file (MV_* environment variables override it)

Run "mangaveyra <command> -h" for command flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errors.New("missing command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(ctx, rest, stderr)
	case "migrate":
		return cmdMigrate(ctx, rest, stdout, stderr)
	case "user":
		return cmdUser(ctx, rest, stdout, stderr)
	case "seed":
		return cmdSeed(ctx, rest, stdout, stderr)
	case "mcp-stdio":
		return cmdMCPStdio(ctx, rest, stdin, stdout, stderr)
	case "backup":
		return cmdBackup(ctx, rest, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.Get().String())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "YAML config file")
	return fs, cfgPath
}

func loadConfig(path string) (*config.Config, error) { return config.Load(path, os.Getenv) }

func cmdServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs, cfgPath := newFlags("serve", stderr)
	addr := fs.String("addr", "", "listen address (overrides MV_ADDR)")
	dataDir := fs.String("data", "", "data directory (overrides MV_DATA_DIR)")
	dev := fs.Bool("dev", false, "development mode (text logs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *dev {
		cfg.Dev = true
	}
	a, err := app.New(ctx, cfg, app.Options{LogOutput: stderr})
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// withDeps opens the services without the HTTP server for CLI commands.
func withDeps(ctx context.Context, cfgPath string, stderr io.Writer, skipMigrate bool, fn func(d *core.Deps) error) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if cfg.LogLevel == "info" {
		cfg.LogLevel = "warn" // keep CLI output clean
	}
	d, closeFn, err := app.NewDeps(ctx, cfg, app.Options{LogOutput: stderr, SkipMigrate: skipMigrate})
	if err != nil {
		return err
	}
	defer closeFn()
	return fn(d)
}

func cmdMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cfgPath := newFlags("migrate", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	status := fs.Arg(0) == "status"
	return withDeps(ctx, *cfgPath, stderr, true, func(d *core.Deps) error {
		if status {
			st, err := d.DB.MigrationStatuses(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tFILE\tAPPLIED")
			for _, s := range st {
				applied := "pending"
				if !s.AppliedAt.IsZero() {
					applied = s.AppliedAt.Format("2006-01-02 15:04:05")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Version, s.File, applied)
			}
			return tw.Flush()
		}
		applied, err := d.DB.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Fprintln(stdout, "database is up to date")
			return nil
		}
		fmt.Fprintf(stdout, "applied %d migration(s): %s\n", len(applied), strings.Join(applied, ", "))
		return nil
	})
}

func cmdUser(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: mangaveyra user <create|reset-password|set-role> [flags]")
	}
	sub, rest := args[0], args[1:]
	fs, cfgPath := newFlags("user "+sub, stderr)
	username := fs.String("username", "", "username (create)")
	email := fs.String("email", "", "email address (create, optional)")
	display := fs.String("name", "", "display name (create, optional)")
	password := fs.String("password", "", "password (empty: generate one and print it; '-' reads it from stdin)")
	role := fs.String("role", "", "site role: admin, creator or reader")
	who := fs.String("user", "", "username or email (reset-password, set-role)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	pw, generated, err := resolvePassword(*password)
	if err != nil {
		return err
	}
	return withDeps(ctx, *cfgPath, stderr, false, func(d *core.Deps) error {
		switch sub {
		case "create":
			r := *role
			if r == "" {
				r = auth.RoleCreator
				if ok, _ := d.Auth.HasAdmin(ctx); !ok {
					r = auth.RoleAdmin // the first account created from the CLI is the admin
				}
			}
			u, err := d.Auth.CreateUser(ctx, auth.NewUser{Username: *username, Email: *email, DisplayName: *display, Password: pw, Role: r})
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "created %s (%s, id %s)\n", u.Username, u.Role, u.ID)
		case "reset-password":
			u, err := d.Auth.UserByLogin(ctx, *who)
			if err != nil {
				return fmt.Errorf("user %q: %w", *who, err)
			}
			if err := d.Auth.SetPassword(ctx, u.ID, pw); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "password changed for %s; all sessions were signed out\n", u.Username)
		case "set-role":
			u, err := d.Auth.UserByLogin(ctx, *who)
			if err != nil {
				return fmt.Errorf("user %q: %w", *who, err)
			}
			if err := d.Auth.SetRole(ctx, u.ID, *role); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s is now %s\n", u.Username, *role)
			return nil
		default:
			return fmt.Errorf("unknown user command %q", sub)
		}
		if generated {
			fmt.Fprintf(stdout, "password: %s\n(store it now; it is not shown again)\n", pw)
		}
		return nil
	})
}

func resolvePassword(p string) (string, bool, error) {
	switch p {
	case "":
		return generatePassword(), true, nil
	case "-":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
		if err != nil {
			return "", false, err
		}
		return strings.TrimRight(string(b), "\r\n"), false, nil
	}
	return p, false, nil
}

func generatePassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func cmdSeed(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, cfgPath := newFlags("seed", stderr)
	demo := fs.Bool("demo", false, "create the Tidebreak Courier demo project")
	owner := fs.String("owner", "", "username that owns the demo content (default: first admin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withDeps(ctx, *cfgPath, stderr, false, func(d *core.Deps) error {
		if err := seed.Run(ctx, d, seed.Options{Demo: *demo, OwnerUsername: *owner}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "seed complete")
		return nil
	})
}

func cmdMCPStdio(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs, cfgPath := newFlags("mcp-stdio", stderr)
	token := fs.String("token", os.Getenv("MV_AGENT_TOKEN"), "agent token (mva_...), default $MV_AGENT_TOKEN")
	serverURL := fs.String("url", "", "server URL, default $MV_PUBLIC_URL (or public_url in --config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The bridge talks to a running server's /mcp over HTTP; it opens no database.
	if *serverURL == "" {
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			return err
		}
		*serverURL = cfg.PublicURL
	}
	return mcp.RunStdio(ctx, mcp.StdioOptions{URL: *serverURL, Token: *token}, stdin, stdout)
}

func cmdBackup(ctx context.Context, args []string, stderr io.Writer) error {
	fs, cfgPath := newFlags("backup", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withDeps(ctx, *cfgPath, stderr, false, func(d *core.Deps) error {
		return backup.RunCLI(ctx, d, fs.Args())
	})
}
