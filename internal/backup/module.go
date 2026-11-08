package backup

import (
	"context"
	"errors"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

// JobCreate is the Go-executor job G10 uses for "Back up now" and "Export
// everything", so the archive is written with progress outside the request.
const JobCreate = "backup.create"

// MemberExports is the narrow view Settings → Data uses for "Last export".
type MemberExports interface {
	LastUserExport(ctx context.Context, userID string) (Backup, bool, error)
}

// Module is the backup feature module.
type Module struct {
	d   *core.Deps
	svc *service
	ui  *web.UI
}

// New creates the module and provides backup.Service and MemberExports.
func New(d *core.Deps) *Module {
	svc := newService(d)
	core.Provide[Service](d, svc)
	core.Provide[MemberExports](d, svc)
	return &Module{d: d, svc: svc}
}

// NewService builds a standalone service for CLI tools that run without the
// HTTP modules (the backup and update commands).
func NewService(d *core.Deps) Service { return newService(d) }

// Name implements core.Module.
func (m *Module) Name() string { return "backup" }

// Init resolves the web layer.
func (m *Module) Init(context.Context) error {
	if ui, ok := core.Use[*web.UI](m.d); ok {
		m.ui = ui
	}
	m.d.Jobs.MarkNoRetry(JobCreate)
	return nil
}

// Start runs scheduled backups, expiry and the server presence heartbeat.
func (m *Module) Start(ctx context.Context) error { return m.svc.loop(ctx) }

// JobHandlers implements core.JobHandlers.
func (m *Module) JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{JobCreate: m.createJob}
}

type createInput struct {
	Kind string `json:"kind"`
}

func (m *Module) createJob(ctx context.Context, run *jobs.Run) error {
	var in createInput
	if err := run.DecodeInput(&in); err != nil || (in.Kind != KindManual && in.Kind != KindExport) {
		return jobs.Permanent(jobs.ErrCodeBadInput, errors.New("unknown backup kind"))
	}
	pctx := WithProgress(ctx, func(p float64) { _ = run.Progress(ctx, p, 0, 0, "") })
	b, err := m.svc.Create(pctx, Request{Kind: in.Kind, CreatedBy: run.Job.UserID})
	switch {
	case errors.Is(err, ErrBusy):
		return jobs.Permanent("busy", err)
	case errors.Is(err, ErrDestination):
		return jobs.Permanent("destination", err)
	case err != nil:
		return err
	}
	run.SetOutput(map[string]string{"backup_id": b.ID})
	return nil
}

// LastUserExport implements MemberExports.
func (s *service) LastUserExport(ctx context.Context, userID string) (Backup, bool, error) {
	var r backupRow
	err := s.db.Get(ctx, &r, `SELECT `+backupCols+` FROM backups WHERE kind = 'user_export' AND user_id = ? AND status = 'done'
		ORDER BY created_at DESC, id DESC LIMIT 1`, userID)
	if err != nil {
		if db.IsNotFound(err) {
			return Backup{}, false, nil
		}
		return Backup{}, false, err
	}
	return r.backup(), true, nil
}
