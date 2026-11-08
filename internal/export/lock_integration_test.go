package export_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func exportPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 64, 96))
	for y := range 96 {
		for x := range 64 {
			im.SetNRGBA(x, y, color.NRGBA{R: shade, G: uint8(x * 3), B: uint8(y * 2), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// E2 shows only current Character Lock evidence: a percentage stored on the
// layer at generation time is never displayed, a current measurement uses the
// floor-and-cap label, and changed references retire it.
func TestExportLayerListShowsOnlyCurrentLockEvidence(t *testing.T) {
	for _, dialect := range testdb.Dialects() {
		t.Run(dialect, func(t *testing.T) {
			ta := testutil.NewTestApp(t, testutil.WithDialect(dialect))
			ctx := context.Background()
			ss := core.MustUse[studio.Service](ta.Deps)
			ps := core.MustUse[pagestore.Service](ta.Deps)
			control := core.MustUse[gen.ControlService](ta.Deps)
			store := assets.From(ta.Deps)
			actor := studio.Actor{UserID: ta.Admin.ID}
			project, err := ss.CreateProject(ctx, actor, studio.NewProject{Title: "Tidebreak Courier", PageWidth: 200, PageHeight: 300})
			if err != nil {
				t.Fatal(err)
			}
			ch, err := ss.CreateChapter(ctx, actor, project.ID, "The Lighthouse Debt")
			if err != nil {
				t.Fatal(err)
			}
			rin, err := ss.CreateCharacter(ctx, actor, studio.NewCharacter{ProjectID: project.ID, Name: "Rin Asagiri", Category: "main"})
			if err != nil {
				t.Fatal(err)
			}
			save := func(shade uint8) assets.Asset {
				a, err := store.Save(ctx, assets.SaveParams{OwnerID: actor.UserID, ProjectID: project.ID, Kind: "reference", Source: assets.SourceUpload, Data: exportPNG(t, shade)})
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			front, art, side := save(10), save(90), save(170)
			if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ss.AddCharacterRefTx(ctx, tx, rin.ID, front.ID, "front", "Front")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			legacy := .95
			page := pagedoc.New(db.NewID(), project.ID, ch.ID, 1, 200, 300, "rtl")
			page.Panels = []pagedoc.Panel{{ID: db.NewID(), PromptSource: "user", Points: pagedoc.RectPoints(pagedoc.Rect{X: 10, Y: 10, W: 180, H: 280}), Layers: []pagedoc.Layer{
				{ID: db.NewID(), Kind: pagedoc.LayerCharacter, Name: "Rin Asagiri", CharacterID: rin.ID, Visible: true, Opacity: 1, Provenance: pagedoc.ProvenanceAI, Asset: &pagedoc.AssetRef{ID: art.ID}, LockScore: &legacy},
			}}}
			if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ps.CreatePagesTx(ctx, tx, ch.ID, []pagedoc.Page{page}, pagestore.Change{ActorKind: "user", ActorID: actor.UserID, Action: "create"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			client := ta.Login(t, ta.Admin.Username)
			base := "/studio/p/" + project.ID + "/chapters/" + strconv.Itoa(ch.Number) + "/export"
			get := func() string {
				t.Helper()
				resp := client.Get(base)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("E2 GET: %d %s", resp.StatusCode, resp.Body)
				}
				return string(resp.Body)
			}
			if html := get(); strings.Contains(html, "LOCK ") {
				t.Fatal("a percentage persisted at generation time was shown as current evidence")
			}

			// A current measurement just below the threshold reads 89%, never 90%.
			status, err := control.LockStatus(ctx, actor, project.ID, rin.ID)
			if err != nil {
				t.Fatal(err)
			}
			job, err := ta.Deps.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: "test.assessment_fixture", Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: project.ID}, UserID: actor.UserID, Executor: jobs.ExecutorWorker, MaxAttempts: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ta.Deps.DB.Exec(ctx, `INSERT INTO character_assessments(id,project_id,character_id,asset_id,reference_hash,method,identity_score,outfit_score,job_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, db.NewID(), project.ID, rin.ID, art.ID, status.ReferenceFingerprint, "dinov2-small-v1:"+strings.Repeat("a", 64), .895, nil, job.ID, db.Now()); err != nil {
				t.Fatal(err)
			}
			html := get()
			if !strings.Contains(html, "LOCK 89%") || strings.Contains(html, "LOCK 90%") || strings.Contains(html, "LOCK 95%") {
				t.Fatal("current evidence was not shown with the floor-and-cap label")
			}

			// Changing the kept references retires that evidence.
			if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
				_, err := ss.AddCharacterRefTx(ctx, tx, rin.ID, side.ID, "side", "Side")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if html := get(); strings.Contains(html, "LOCK ") {
				t.Fatal("evidence for retired references was shown")
			}
		})
	}
}
