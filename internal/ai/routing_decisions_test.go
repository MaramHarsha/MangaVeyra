package ai

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// A no-key install routes every image job to the placeholder. Those jobs
// record their decision like any other route, once per job attempt, so G11
// "Recent decisions" is never empty on a fresh server.
func TestPlaceholderImageJobsRecordRoutingDecisions(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		m, f := imageTestModule(t, d, &queuedDoer{})
		p, err := m.image.PlanImage(ctx, ImagePlanRequest{Task: TaskPanel, ActorID: f.userID, ProjectID: f.projectID, Needs: ImageNeeds{Width: 64, Height: 64, Count: 1}})
		if err != nil || p.ProviderID != PlaceholderID {
			t.Fatalf("plan=%+v err=%v", p, err)
		}
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		for slot := 0; slot < 3; slot++ {
			req.CallSlot = slot
			if _, err := m.image.GenerateImage(ctx, req); err != nil {
				t.Fatalf("slot %d: %v", slot, err)
			}
		}
		var rows []struct {
			Provider    string `db:"chosen_provider_id"`
			Model       string `db:"chosen_model_id"`
			Task        string `db:"task"`
			Explanation string `db:"explanation"`
		}
		if err := d.Select(ctx, &rows, `SELECT chosen_provider_id, chosen_model_id, task, explanation FROM routing_decisions WHERE job_id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Provider != PlaceholderID || rows[0].Model != PlaceholderArt || rows[0].Task != TaskPanel || rows[0].Explanation != "64 × 64" {
			t.Fatalf("decisions=%+v", rows)
		}

		v, err := m.loadRoutingView(ctx, providerActor{UserID: f.userID})
		if err != nil || len(v.Decisions) != 1 {
			t.Fatalf("view decisions=%+v err=%v", v.Decisions, err)
		}
		var page bytes.Buffer
		if err := RoutingPage(v).Render(ctx, &page); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Panel art", "Placeholder art · built-in", "No connected model fits; placeholder art keeps the flow working · 64 × 64"} {
			if !strings.Contains(page.String(), want) {
				t.Fatalf("G11 lacks %q", want)
			}
		}
		if strings.Contains(page.String(), "Image jobs will show their model choices here.") {
			t.Fatal("G11 still shows the empty state")
		}
	})
}
