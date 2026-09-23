package ai

import (
	"context"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// Meter is the compute meter: GPU and VRAM from online workers, the queue and
// its ETA, this week's GPU time and this month's remote spend on the members'
// own keys against the limit. The self-hosted edition shows this instead of any balance.
type Meter struct {
	GPU           string  `json:"gpu"`
	VRAMTotalMB   int     `json:"vram_total_mb"`
	VRAMUsedMB    int     `json:"vram_used_mb"`
	WorkersOnline int     `json:"workers_online"`
	WorkersTotal  int     `json:"workers_total"`
	Queued        int     `json:"queued"`
	Running       int     `json:"running"`
	ETASeconds    float64 `json:"eta_seconds"`
	// WeekGPUSeconds is GPU time used in the last 7 days (the whole server).
	WeekGPUSeconds float64 `json:"week_gpu_seconds"`
	// MonthUSD / LimitUSD are the member's remote spend this month and their limit (0 = none).
	MonthUSD float64 `json:"month_usd"`
	LimitUSD float64 `json:"limit_usd"`
	// ServerMonthUSD / ServerLimitUSD cover the whole server.
	ServerMonthUSD float64 `json:"server_month_usd"`
	ServerLimitUSD float64 `json:"server_limit_usd"`
	LocalOnly      bool    `json:"local_only"`
}

// ComputeMeter is the interface the web layer reads (core.Provide[ai.ComputeMeter]).
type ComputeMeter interface {
	Meter(ctx context.Context, userID string) (Meter, error)
}

// Meter computes the compute meter for userID.
func (s *Service) Meter(ctx context.Context, userID string) (Meter, error) {
	var m Meter
	inst := s.d.Settings.MustInstance(ctx)
	m.LocalOnly = inst.LocalOnly
	st, err := s.d.Jobs.Stats(ctx, "")
	if err != nil {
		return m, err
	}
	m.Queued, m.Running, m.WorkersOnline = st.Queued, st.Running, st.WorkersOnline
	ws, err := s.d.Jobs.ListWorkers(ctx)
	if err != nil {
		return m, err
	}
	m.WorkersTotal = len(ws)
	best, slots := 0, 0
	for _, w := range ws {
		if w.Status != jobs.WorkerOnline {
			continue
		}
		slots += max(1, w.MaxConcurrent)
		for _, g := range w.GPUs.V {
			m.VRAMTotalMB += g.VRAMTotalMB
			if u := g.VRAMTotalMB - g.VRAMFreeMB; u > 0 {
				m.VRAMUsedMB += u
			}
			if g.VRAMTotalMB > best {
				best, m.GPU = g.VRAMTotalMB, g.Name
			}
		}
	}
	// ETA: queued worker jobs times the average GPU seconds per job over the online slots.
	var avg float64
	_ = s.d.DB.Get(ctx, &avg, `SELECT COALESCE(AVG(gpu_seconds), 0) FROM spend_ledger WHERE kind = 'gpu' AND gpu_seconds > 0`)
	if avg <= 0 {
		avg = 20
	}
	var queuedWorker int
	_ = s.d.DB.Get(ctx, &queuedWorker, `SELECT COUNT(*) FROM jobs WHERE state = 'queued' AND executor = 'worker'`)
	if slots > 0 {
		m.ETASeconds = float64(queuedWorker+st.RunningWorker) * avg / float64(slots)
	}
	since := s.Now().UTC().Add(-7 * 24 * time.Hour)
	_ = s.d.DB.Get(ctx, &m.WeekGPUSeconds, `SELECT COALESCE(SUM(gpu_seconds), 0) FROM spend_ledger WHERE created_at > ?`, since)
	m.ServerMonthUSD, _ = s.MonthSpend(ctx, "")
	m.ServerLimitUSD = inst.Limits.ServerMonthlyRemoteSpendUSD
	if userID != "" {
		m.MonthUSD, _ = s.MonthSpend(ctx, userID)
		m.LimitUSD = inst.Limits.MonthlyRemoteSpendUSD
	}
	return m, nil
}
