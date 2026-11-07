// phase2check is an explicitly authorized, resumable live acceptance harness.
// It is never part of the server's routes or normal startup. See plan 0007.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/app"
	"github.com/MaramHarsha/MangaVeyra/internal/config"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

const modelID = "gpt-image-2.5-sunburst"
const capUSD = 10.0

type state struct {
	ApprovedCap float64           `json:"approved_cap_usd"`
	UserID      string            `json:"user_id"`
	ProviderID  string            `json:"provider_id"`
	ProjectID   string            `json:"project_id"`
	ChapterID   string            `json:"chapter_id"`
	CharacterID string            `json:"character_id"`
	Curated     bool              `json:"curated_individual_references"`
	Jobs        map[string]string `json:"jobs"`
	HandRect    []int             `json:"hand_rect,omitempty"`
}
type harness struct {
	ctx     context.Context
	a       *app.App
	s       state
	root    string
	actor   studio.Actor
	studio  studio.Service
	pages   pagestore.Service
	gen     gen.Service
	control gen.ControlService
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "phase2check:", err)
		os.Exit(1)
	}
}
func run() error {
	stage := flag.String("stage", "report", "init, references, chapter, assess, region, report, serve")
	root := flag.String("data", "", "new isolated temporary data directory")
	source := flag.String("source", "", "read-only source data directory (init only)")
	provider := flag.String("provider", "", "saved OpenAI provider ID (init only)")
	user := flag.String("user", "", "existing account ID (init only)")
	python := flag.String("python", "", "isolated scoring Python executable")
	dino := flag.String("dino", "", "explicit offline DINOv2 Small directory")
	worker := flag.String("worker", "worker", "worker package source directory")
	upTo := flag.Int("pages", 30, "generate appearances through this page (1–30)")
	handRect := flag.String("hand-rect", "", "manually inspected page-7 hand rectangle, x,y,width,height")
	refCrops := flag.String("reference-crops", "", "inspected sheet crops: front:x,y,w,h;side:x,y,w,h;back:x,y,w,h")
	flag.Parse()
	if os.Getenv("MV_PHASE2_LIVE_APPROVED") != "1" {
		return errors.New("explicit owner approval is required (MV_PHASE2_LIVE_APPROVED=1)")
	}
	*root = filepath.Clean(*root)
	if !filepath.IsAbs(*root) || (!strings.HasPrefix(*root, "/private/tmp/mv-phase2-") && !strings.HasPrefix(*root, "/tmp/mv-phase2-")) {
		return errors.New("data must be a new mv-phase2- directory under the temporary directory")
	}
	if *stage == "init" {
		if _, err := os.Stat(*root); !os.IsNotExist(err) {
			return errors.New("init refuses an existing data directory")
		}
		if !db.ValidID(*user) || !db.ValidID(*provider) || *source == "" {
			return errors.New("init needs source, user and provider IDs")
		}
		if err := os.Mkdir(*root, 0700); err != nil {
			return err
		}
		key, err := os.ReadFile(filepath.Join(*source, "secret.key"))
		if err != nil {
			return errors.New("cannot read the saved encryption key file")
		}
		if err = os.WriteFile(filepath.Join(*root, "secret.key"), key, 0600); err != nil {
			return err
		}
		buf := make([]byte, 32)
		if _, err = rand.Read(buf); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(*root, "worker-token"), []byte(base64.RawURLEncoding.EncodeToString(buf)), 0600); err != nil {
			return err
		}
	}
	token, err := os.ReadFile(filepath.Join(*root, "worker-token"))
	if err != nil {
		return errors.New("isolated worker token is missing")
	}
	env := map[string]string{"MV_DATA_DIR": *root, "MV_ADDR": "127.0.0.1:64021", "MV_PUBLIC_URL": "http://127.0.0.1:64021", "MV_WORKER_TOKEN": string(token), "MV_LOG_LEVEL": "error"}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, err := app.New(ctx, cfg, app.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ExecutorSize: 2})
	if err != nil {
		return err
	}
	defer a.Close()
	h := &harness{ctx: ctx, a: a, root: *root, studio: core.MustUse[studio.Service](a.Deps), pages: core.MustUse[pagestore.Service](a.Deps), gen: core.MustUse[gen.Service](a.Deps), control: core.MustUse[gen.ControlService](a.Deps)}
	if *stage == "init" {
		if err = copyConnection(ctx, a.Deps.DB, *source, *user, *provider); err != nil {
			return err
		}
		h.s = state{ApprovedCap: capUSD, UserID: *user, ProviderID: *provider, Jobs: map[string]string{}}
	} else {
		b, e := os.ReadFile(filepath.Join(*root, "acceptance.json"))
		if e != nil {
			return e
		}
		if err = json.Unmarshal(b, &h.s); err != nil {
			return err
		}
		if h.s.ApprovedCap != capUSD {
			return errors.New("the recorded approval cap differs")
		}
	}
	h.actor, err = h.studio.ActorFromUser(ctx, h.s.UserID)
	if err != nil {
		return err
	}
	_, err = a.Deps.Settings.UpdateInstance(ctx, h.s.UserID, func(i *settings.Instance) error {
		i.Name = "MangaVeyra · Phase 2 live acceptance"
		i.Mode = settings.ModePersonal
		i.SignUp = settings.SignUpClosed
		i.LocalOnly = false
		i.World.Enabled = false
		i.World.ShowTab = false
		i.RoutingMode = settings.RoutingBest
		i.NightMode.Enabled = false
		i.Limits.MonthlyRemoteSpendUSD = capUSD
		i.Limits.ServerMonthlyRemoteSpendUSD = capUSD
		return nil
	})
	if err != nil {
		return err
	}
	// A second guard covers all months and rejects other billable routes. The
	// application's durable per-call reservation still owns dispatch and billing.
	a.Deps.Jobs.AddTxValidator(func(ctx context.Context, tx *db.Tx, p *jobs.EnqueueParams) error {
		if p.Estimate.USD <= 0 {
			return nil
		}
		if p.ProviderID != h.s.ProviderID || p.ModelID != modelID {
			return errors.New("live approval covers only the saved OpenAI image route")
		}
		var micro int64
		if err := tx.Get(ctx, &micro, `SELECT COALESCE(SUM(COALESCE(actual_microusd,estimate_microusd)),0) FROM spend_reservations`); err != nil {
			return err
		}
		if float64(micro)/1e6+p.Estimate.USD > capUSD {
			return errors.New("live approval budget would be exceeded")
		}
		return nil
	})
	if *stage == "init" {
		if err = h.initialize(); err != nil {
			return err
		}
		if err = h.save(); err != nil {
			return err
		}
	}
	if *handRect != "" {
		h.s.HandRect = nil
		for _, v := range strings.Split(*handRect, ",") {
			n, e := strconv.Atoi(v)
			if e != nil {
				return errors.New("hand-rect needs four integers")
			}
			h.s.HandRect = append(h.s.HandRect, n)
		}
		if len(h.s.HandRect) != 4 {
			return errors.New("hand-rect needs four integers")
		}
		if err = h.save(); err != nil {
			return err
		}
	}
	h.registerStructure()
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	go server.Serve(ln)
	a.Start(ctx)
	if *stage == "references" || *stage == "chapter" || *stage == "assess" || *stage == "region" || *stage == "repair" {
		if *python == "" || *dino == "" {
			return errors.New("an explicitly provisioned scoring environment and model are required")
		}
		logfile, e := os.OpenFile(filepath.Join(*root, "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer logfile.Close()
		cmd := exec.CommandContext(ctx, *python, "-m", "mangaveyra_worker")
		cmd.Env = append(os.Environ(), "PYTHONPATH="+*worker, "MV_SERVER="+cfg.PublicURL, "MV_WORKER_TOKEN=", "MV_WORKER_TOKEN_FILE="+filepath.Join(*root, "worker-token"), "MV_WORKER_NAME=phase2-dinov2-assessor", "MV_BACKENDS=vision", "MV_MODELS=", "MV_OCR_ENGINE=none", "MV_EMBEDDER=dinov2", "MV_DINO_MODEL_DIR="+*dino, "MV_WORKER_DATA_DIR="+filepath.Join(*root, "worker-scratch"), "MV_LOG_LEVEL=warning", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "OMP_NUM_THREADS=2")
		cmd.Stdout = logfile
		cmd.Stderr = logfile
		workerStarted := db.Now()
		if e = cmd.Start(); e != nil {
			return e
		}
		defer func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait() }()
		ready := false
		for n := 0; n < 100; n++ {
			var count int
			_ = a.Deps.DB.Get(ctx, &count, `SELECT COUNT(*) FROM workers WHERE status='online' AND models LIKE ? AND last_seen_at>=?`, "%character-lock-dinov2-v1%", workerStarted)
			if count > 0 {
				ready = true
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		if !ready {
			return errors.New("offline DINOv2 worker did not become ready; see isolated worker.log")
		}
	}
	switch *stage {
	case "init":
		fmt.Println("Created isolated acceptance project:", h.s.ProjectID)
	case "references":
		err = h.references()
	case "curate":
		err = h.curate(*refCrops)
	case "plates":
		err = h.plates()
	case "repair":
		err = h.repair()
	case "chapter":
		if *upTo < 1 || *upTo > 30 {
			return errors.New("pages must be 1–30")
		}
		err = h.chapter(*upTo)
	case "assess":
		var c studio.Character
		c, err = h.studio.GetCharacter(ctx, h.s.CharacterID)
		if err == nil {
			_, err = h.step("assessment-"+time.Now().UTC().Format("20060102T150405"), func() (*jobs.Job, error) {
				return h.control.EnqueueScoring(ctx, h.actor, h.s.ProjectID, c.ID, c.UpdatedAt)
			})
		}
	case "region":
		err = h.region()
	case "report":
	case "serve":
		fmt.Println("Isolated live preview:", cfg.PublicURL)
		<-ctx.Done()
	default:
		return errors.New("unknown stage")
	}
	if err != nil {
		return err
	}
	return h.report()
}

func copyConnection(ctx context.Context, target *db.DB, source, user, provider string) error {
	src, err := sql.Open("sqlite", "file:"+filepath.Join(source, "mangaveyra.db")+"?mode=ro")
	if err != nil {
		return err
	}
	defer src.Close()
	var kind string
	var enabled bool
	if err = src.QueryRowContext(ctx, `SELECT kind,enabled FROM providers WHERE id=?`, provider).Scan(&kind, &enabled); err != nil {
		return err
	}
	if kind != "openai" || !enabled {
		return errors.New("source must be an enabled saved OpenAI connection")
	}
	return target.WithTx(ctx, func(tx *db.Tx) error {
		for _, row := range []struct{ table, id string }{{"users", user}, {"providers", provider}} {
			r, err := src.QueryContext(ctx, "SELECT * FROM "+row.table+" WHERE id=?", row.id)
			if err != nil {
				return err
			}
			cols, err := r.Columns()
			if err != nil {
				r.Close()
				return err
			}
			if !r.Next() {
				r.Close()
				return errors.New("saved connection/account not found")
			}
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = r.Scan(ptrs...); err != nil {
				r.Close()
				return err
			}
			r.Close()
			marks := make([]string, len(cols))
			for i := range marks {
				marks[i] = "?"
			}
			if _, err = tx.Exec(ctx, "INSERT INTO "+row.table+" ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(marks, ",")+")", values...); err != nil {
				return errors.New("cannot copy sealed connection/account into isolated database")
			}
		}
		return nil
	})
}
func (h *harness) save() error {
	b, err := json.MarshalIndent(h.s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.root, "acceptance.json"), b, 0600)
}
func (h *harness) step(key string, enqueue func() (*jobs.Job, error)) (*jobs.Job, error) {
	id := h.s.Jobs[key]
	if id == "" {
		j, err := enqueue()
		if err != nil {
			return nil, err
		}
		id = j.ID
		h.s.Jobs[key] = id
		if err = h.save(); err != nil {
			return nil, err
		}
	}
	for {
		j, err := h.a.Deps.Jobs.Get(h.ctx, id)
		if err != nil {
			return nil, err
		}
		switch j.State {
		case jobs.StateDone:
			fmt.Printf("%s: done · USD %.6f\n", key, j.Actual.USD)
			return j, nil
		case jobs.StateFailed, jobs.StateCanceled:
			return j, fmt.Errorf("%s stopped (%s); no automatic replacement call: %s", key, j.State, j.Error.Code)
		}
		select {
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
