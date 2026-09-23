package web

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/web/ui"
)

// Compute summarises the server's compute state for the compute meter, the
// dashboard compute card and the setup wizard's GPU detection. The
// self-hosted edition shows GPU and queue load instead of any balance.
type Compute struct {
	Queued         int
	Running        int
	WorkersOnline  int
	WorkersTotal   int
	FailedLastHour int
	// GPU is the name of the largest GPU on an online worker ("NVIDIA RTX 4090").
	GPU string
	// VRAMTotalMB / VRAMUsedMB are summed over online workers' GPUs.
	VRAMTotalMB int
	VRAMUsedMB  int
	// GPUs lists every GPU reported by online workers.
	GPUs []jobs.GPU
	// WorkerName is the name of the first online worker ("Studio PC").
	WorkerName string
}

// Load returns 0–1: VRAM in use when known, otherwise queue pressure.
func (c Compute) Load() float64 {
	if c.VRAMTotalMB > 0 {
		return float64(c.VRAMUsedMB) / float64(c.VRAMTotalMB)
	}
	busy := c.Queued + c.Running
	if busy == 0 {
		return 0
	}
	slots := c.WorkersOnline * 2
	if slots < 2 {
		slots = 2
	}
	l := float64(busy) / float64(slots)
	if l > 1 {
		l = 1
	}
	return l
}

// ComputeExtra is what the AI module adds to the compute meter: remote spend
// on the member's own keys this month (against the limit), the queue ETA and
// this week's GPU time, already translated. Empty strings are left out.
type ComputeExtra struct {
	Remote  string
	ETA     string
	WeekGPU string
}

// ComputeExtender is provided by the ai module (core.Provide[web.ComputeExtender]).
type ComputeExtender interface {
	ComputeExtra(ctx context.Context, userID string) ComputeExtra
}

// computeExtra returns the AI module's additions (zero without the module).
func (u *UI) computeExtra(ctx context.Context) ComputeExtra {
	ext, ok := core.Use[ComputeExtender](u.d)
	if !ok {
		return ComputeExtra{}
	}
	id := ""
	if usr := core.UserFrom(ctx); usr != nil {
		id = usr.ID
	}
	return ext.ComputeExtra(ctx, id)
}

type computeCache struct {
	mu  sync.Mutex
	at  time.Time
	val Compute
}

// ComputeSummary reads queue counts and online workers (no caching).
func ComputeSummary(ctx context.Context, q *jobs.Queue) (Compute, error) {
	var c Compute
	st, err := q.Stats(ctx, "")
	if err != nil {
		return c, err
	}
	c.Queued, c.Running, c.WorkersOnline, c.FailedLastHour = st.Queued, st.Running, st.WorkersOnline, st.FailedLastHour
	workers, err := q.ListWorkers(ctx)
	if err != nil {
		return c, err
	}
	c.WorkersTotal = len(workers)
	best := 0
	for _, w := range workers {
		if w.Status != jobs.WorkerOnline {
			continue
		}
		if c.WorkerName == "" {
			c.WorkerName = w.Name
		}
		for _, g := range w.GPUs.V {
			c.GPUs = append(c.GPUs, g)
			c.VRAMTotalMB += g.VRAMTotalMB
			if used := g.VRAMTotalMB - g.VRAMFreeMB; used > 0 {
				c.VRAMUsedMB += used
			}
			if g.VRAMTotalMB > best {
				best, c.GPU = g.VRAMTotalMB, g.Name
			}
		}
	}
	return c, nil
}

// Compute returns the compute summary, cached for two seconds.
func (u *UI) Compute(ctx context.Context) Compute {
	u.cc.mu.Lock()
	defer u.cc.mu.Unlock()
	if time.Since(u.cc.at) < 2*time.Second {
		return u.cc.val
	}
	c, err := ComputeSummary(ctx, u.d.Jobs)
	if err != nil {
		u.log.Warn("web: compute summary", "err", err)
		return u.cc.val
	}
	u.cc.val, u.cc.at = c, time.Now()
	return c
}

// GB formats megabytes as gigabytes ("16.3").
func GB(mb int) string {
	g := float64(mb) / 1024
	if g >= 10 && g == float64(int(g)) {
		return fmt.Sprintf("%d", int(g))
	}
	return fmt.Sprintf("%.1f", g)
}

// meter builds the top-bar compute meter.
func (u *UI) meter(ctx context.Context) *ui.MeterProps {
	c := u.Compute(ctx)
	m := &ui.MeterProps{Href: "/queue", Load: c.Load()}
	busy := c.Queued + c.Running
	switch {
	case c.Queued > 0:
		m.Text = i18n.T(ctx, "meter.queued", "count", c.Queued)
	case c.Running > 0:
		m.Text = i18n.T(ctx, "meter.running", "count", c.Running)
	case c.WorkersOnline > 0:
		m.Text, m.Idle = i18n.T(ctx, "meter.idle"), true
	default:
		m.Text, m.Offline = i18n.T(ctx, "meter.no_worker"), true
	}
	switch {
	case c.GPU != "":
		m.Title = i18n.T(ctx, "meter.title_gpu", "gpu", c.GPU, "used", GB(c.VRAMUsedMB), "total", GB(c.VRAMTotalMB), "jobs", busy)
	case c.WorkersOnline > 0:
		m.Title = i18n.T(ctx, "meter.title_workers", "count", c.WorkersOnline, "jobs", busy)
	default:
		m.Title = i18n.T(ctx, "meter.title_none", "jobs", busy)
	}
	if ex := u.computeExtra(ctx); busy > 0 && ex.ETA != "" {
		m.Title += " · " + ex.ETA
	}
	return m
}
