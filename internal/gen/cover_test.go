package gen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// coverFixture is a project with two lead characters: Rin keeps two
// references, Kaito one.
func coverFixture(t *testing.T, ta *testutil.TestApp) (*genFixture, gen.CoverService) {
	t.Helper()
	ctx := context.Background()
	f := newGenFixture(t, ta, "", 2)
	store := core.MustUse[*assets.Service](ta.Deps)
	err := ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
		for i, kinds := range [][]string{{"sheet", "front"}, {"sheet"}} {
			for n, kind := range kinds {
				a, err := store.SaveTx(ctx, tx, assets.SaveParams{OwnerID: f.owner.ID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: imagePNG(t, 96+16*n+32*i, 128)})
				if err != nil {
					return err
				}
				if _, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[i].ID, a.ID, kind, kind); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, core.MustUse[gen.CoverService](ta.Deps)
}

func coverAssets(t *testing.T, ta *testutil.TestApp, projectID string) int {
	t.Helper()
	var n int
	if err := ta.Deps.DB.Get(context.Background(), &n, `SELECT COUNT(*) FROM assets WHERE project_id = ? AND kind = 'cover_candidate'`, projectID); err != nil {
		t.Fatal(err)
	}
	return n
}

func projectCover(t *testing.T, ta *testutil.TestApp, projectID string) string {
	t.Helper()
	p, err := core.MustUse[studio.Service](ta.Deps).GetProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return p.CoverAssetID
}

func TestCoverJobMakesCandidatesAndKeepSetsTheProjectCover(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		j, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID})
		if err != nil {
			t.Fatal(err)
		}
		if j.Type != gen.JobCover || j.Scope.Kind != jobs.ScopeChapter || j.Scope.ChapterID != f.chapter.ID || j.MaxAttempts != 3 || j.Executor != jobs.ExecutorGo || j.Deduplicated {
			t.Fatalf("cover job envelope: %+v", j)
		}
		if ta.Deps.Jobs.PublicView(j).Input != nil {
			t.Fatal("private cover prompt exposed by the jobs API")
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("cover job: %+v", done.Error)
		}
		if len(fake.requests) != 1 {
			t.Fatalf("calls=%d", len(fake.requests))
		}
		call := fake.requests[0]
		if call.Plan.Task != ai.TaskCover || call.Width != 768 || call.Height != 1024 || call.Count != 4 || call.Background != "opaque" || call.CallSlot != 0 || call.Init != nil || call.Mask != nil {
			t.Fatalf("cover call: %+v", call.Plan)
		}
		// Interleaved, sheet-first: Rin's sheet, Kaito's sheet, then Rin's front view.
		if len(call.References) != 3 || !strings.HasPrefix(call.References[0].Label, "Rin") || !strings.HasPrefix(call.References[1].Label, "Kaito") || !strings.HasPrefix(call.References[2].Label, "Rin") {
			t.Fatalf("cover references: %+v", call.References)
		}
		for _, want := range []string{"chapter cover", "The Closing Gate", "One last delivery.", "Lead character: Rin", "Lead character: Kaito", "kept references", "no text"} {
			if !strings.Contains(call.Prompt, want) {
				t.Fatalf("cover prompt lacks %q:\n%s", want, call.Prompt)
			}
		}
		if projectCover(t, ta, f.project.ID) != "" {
			t.Fatal("generation alone changed the project cover")
		}
		result, err := covers.CoverCandidates(ctx, f.actor, j.ID)
		if err != nil || len(result.Candidates) != 4 || result.KeptIndex != nil || result.Candidates[0].URL == "" {
			t.Fatalf("candidates %+v %v", result, err)
		}
		store := core.MustUse[*assets.Service](ta.Deps)
		for _, c := range result.Candidates {
			a, err := store.Get(ctx, c.AssetID)
			var meta map[string]string
			if err != nil || a.Source != assets.SourceAI || a.Kind != "cover_candidate" || json.Unmarshal(a.Meta, &meta) != nil || meta["job_id"] != j.ID || meta["model_id"] != ai.PlaceholderArt || meta["chapter_id"] != f.chapter.ID || a.Width != 768 || a.Height != 1024 {
				t.Fatalf("candidate provenance %+v %v", a, err)
			}
		}

		// An agent token cannot choose the cover; the owner can, once.
		_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAgent, Name: "Cover agent", Scopes: []string{"projects:read", "projects:write", "generate"}, ProjectIDs: []string{f.project.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := covers.KeepCover(ctx, studio.Actor{UserID: f.owner.ID, TokenID: token.ID}, j.ID, 1); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("agent keep = %v", err)
		}
		kept, err := covers.KeepCover(ctx, f.actor, j.ID, 2)
		if err != nil || kept != result.Candidates[2].AssetID || projectCover(t, ta, f.project.ID) != kept {
			t.Fatalf("keep = %q %v, cover %q", kept, err, projectCover(t, ta, f.project.ID))
		}
		if _, err := covers.KeepCover(ctx, f.actor, j.ID, 1); !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("second keep = %v", err)
		}
		if again, err := covers.CoverCandidates(ctx, f.actor, j.ID); err != nil || again.KeptIndex == nil || *again.KeptIndex != 2 {
			t.Fatalf("kept index %+v %v", again, err)
		}
		if _, err := covers.KeepCover(ctx, f.actor, j.ID, 7); !errors.Is(err, studio.ErrInvalid) && !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("out-of-range keep = %v", err)
		}
	})
}

func TestCoverAuthorizationFollowsPanelArt(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		req := gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID}
		member := func(role string) studio.Actor {
			u := ta.CreateUser(t, "", auth.RoleCreator)
			if role != "" {
				if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, f.project.ID, u.ID, role); err != nil {
					t.Fatal(err)
				}
			}
			return studio.Actor{UserID: u.ID}
		}
		// Generating needs both generate and edit_art, exactly like panel jobs.
		for _, role := range []string{"", studio.RoleViewer, studio.RoleWriter, studio.RoleLetterer} {
			if _, err := covers.EnqueueCover(ctx, member(role), req); !errors.Is(err, studio.ErrForbidden) {
				t.Fatalf("%q enqueue = %v", role, err)
			}
		}
		artist := member(studio.RoleArtist)
		j, err := covers.EnqueueCover(ctx, artist, req)
		if err != nil {
			t.Fatal(err)
		}
		foreign := newGenFixture(t, ta, "", 0)
		if _, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: foreign.chapter.ID}); !errors.Is(err, studio.ErrNotFound) {
			t.Fatalf("foreign chapter = %v", err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("artist cover: %+v", done.Error)
		}
		// Only the member who generated the candidates sees and keeps them.
		if _, err := covers.CoverCandidates(ctx, f.actor, j.ID); !errors.Is(err, studio.ErrNotFound) {
			t.Fatalf("other member read = %v", err)
		}
		if _, err := covers.KeepCover(ctx, f.actor, j.ID, 0); !errors.Is(err, studio.ErrNotFound) {
			t.Fatalf("other member keep = %v", err)
		}
		// A role downgrade after generation blocks the keep.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE project_members SET role = ? WHERE project_id = ? AND user_id = ?`, studio.RoleViewer, f.project.ID, artist.UserID); err != nil {
			t.Fatal(err)
		}
		if _, err := covers.KeepCover(ctx, artist, j.ID, 0); !errors.Is(err, studio.ErrForbidden) {
			t.Fatalf("viewer keep = %v", err)
		}
		if projectCover(t, ta, f.project.ID) != "" {
			t.Fatal("refused keep changed the cover")
		}
	})
}

func TestCoverLocalOnlyNeverLeavesTheServer(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		setLocalOnly := func(on bool) {
			t.Helper()
			if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.LocalOnly = on; return nil }); err != nil {
				t.Fatal(err)
			}
		}
		// The real router keeps local-only covers on the in-process placeholder.
		setLocalOnly(true)
		j, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Variations: 2})
		if err != nil {
			t.Fatal(err)
		}
		if j.ProviderID != ai.PlaceholderID || j.Estimate != (jobs.Cost{}) {
			t.Fatalf("local-only plan: %s %+v", j.ProviderID, j.Estimate)
		}
		var payload struct {
			Plan ai.ImagePlan `json:"plan"`
		}
		if err := j.DecodeInput(&payload); err != nil || payload.Plan.RoutingMode != ai.ModeLocalOnly || payload.Plan.Remote {
			t.Fatalf("stored plan %+v %v", payload.Plan, err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("placeholder cover: %+v", done.Error)
		}
		result, err := covers.CoverCandidates(ctx, f.actor, j.ID)
		if err != nil || len(result.Candidates) != 2 {
			t.Fatalf("placeholder candidates %+v %v", result, err)
		}
		data, _, err := core.MustUse[*assets.Service](ta.Deps).Read(ctx, result.Candidates[0].AssetID, assets.MaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		im, err := png.Decode(bytes.NewReader(data))
		if err != nil || im.Bounds().Dx() != 768 || im.Bounds().Dy() != 1024 {
			t.Fatalf("placeholder cover %v %v", im.Bounds(), err)
		}
		if _, _, _, a := im.At(10, 10).RGBA(); a != 0xffff || color.NRGBAModel.Convert(im.At(700, 1000)).(color.NRGBA).A != 255 {
			t.Fatal("cover is not opaque")
		}

		// A remote plan queued before local_only was switched on never dispatches.
		setLocalOnly(false)
		fake := &fakeImages{planHook: func(p *ai.ImagePlan) {
			p.ProviderID, p.ModelID, p.Remote, p.Estimate = "fixture-remote", "fixture-cover", true, jobs.Cost{USD: .2}
		}}
		gen.SetImageServiceForTest(f.gen, fake)
		remote, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID})
		if err != nil {
			t.Fatal(err)
		}
		setLocalOnly(true)
		before := coverAssets(t, ta, f.project.ID)
		done := runArtJob(t, ta, remote.ID)
		if done.State != jobs.StateFailed || done.Error == nil || done.Error.Code != ai.CodeLocalOnly || fake.calls() != 0 {
			t.Fatalf("local-only remote dispatch: %s %+v calls=%d", done.State, done.Error, fake.calls())
		}
		if coverAssets(t, ta, f.project.ID) != before {
			t.Fatal("refused cover stored candidates")
		}
	})
}

// reservingImages behaves like a remote adapter at its spend boundary: every
// call first reserves its estimate in the ADR 0003 ledger for the exact job
// attempt, and reports an actual cost.
type reservingImages struct {
	fakeImages
	spend *ai.SpendStore
}

func (r *reservingImages) GenerateImage(ctx context.Context, req ai.ImageGenerateRequest) (ai.ImageResult, error) {
	if _, err := r.spend.ReserveRemoteCall(ctx, ai.ReserveRemoteCallParams{ReservationID: db.NewID(), JobID: req.JobID, Attempt: req.Attempt, Purpose: "initial",
		ProviderID: req.Plan.ProviderID, ModelID: req.Plan.ModelID, EstimateUSD: req.Plan.Estimate.USD, ExpiresAt: time.Now().Add(10 * time.Minute)}); err != nil {
		code := ai.CodeUpstream
		if errors.Is(err, ai.ErrUserRemoteSpendLimit) || errors.Is(err, ai.ErrServerRemoteSpendLimit) {
			code = ai.CodeSpendLimit
		}
		return ai.ImageResult{}, &ai.GenerateError{Code: code, SafeMessage: "The remote spend guard refused the call."}
	}
	out, err := r.fakeImages.GenerateImage(ctx, req)
	out.Actual = jobs.Cost{USD: .15}
	return out, err
}

func TestCoverSpendIsApprovedAndReservedLikePanelArt(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		provider := db.NewID()
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO providers (id, kind, name, key_enc, scope, enabled) VALUES (?, 'openai', 'Fixture images', 'sealed-fixture', 'server', TRUE)`, provider); err != nil {
			t.Fatal(err)
		}
		fake := &reservingImages{spend: ai.NewSpendStore(ta.Deps.DB, ta.Deps.Settings)}
		fake.planHook = func(p *ai.ImagePlan) {
			p.ProviderID, p.ModelID, p.Remote, p.Estimate = provider, "fixture-cover", true, jobs.Cost{USD: .2}
		}
		gen.SetImageServiceForTest(f.gen, fake)
		threshold := .5
		_, token, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: f.owner.ID, Kind: auth.TokenAgent, Name: "Cover agent", Scopes: []string{"projects:read", "projects:write", "generate"}, ProjectIDs: []string{f.project.ID}, ApproveOverUSD: &threshold})
		if err != nil {
			t.Fatal(err)
		}
		agent := studio.Actor{UserID: f.owner.ID, TokenID: token.ID}
		req := gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID}
		// Three attempts of a $0.20 route need approval above $0.50.
		var refused *ai.GenerateError
		if _, err := covers.EnqueueCover(ctx, agent, req); !errors.As(err, &refused) || refused.Code != ai.CodeApproval {
			t.Fatalf("aggregate approval ignored: %v", err)
		}
		limit := func(usd float64) {
			t.Helper()
			if _, err := ta.Deps.Settings.UpdateInstance(ctx, ta.Admin.ID, func(inst *settings.Instance) error { inst.Limits.MonthlyRemoteSpendUSD = usd; return nil }); err != nil {
				t.Fatal(err)
			}
		}
		limit(.1)
		j, err := covers.EnqueueCover(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		if j.Estimate.USD < .6-1e-9 || j.Estimate.USD > .6+1e-9 || j.ProviderID != provider {
			t.Fatalf("cover envelope %+v", j.Estimate)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateFailed || done.Error == nil || done.Error.Code != ai.CodeSpendLimit || coverAssets(t, ta, f.project.ID) != 0 {
			t.Fatalf("over-cap cover: %s %+v", done.State, done.Error)
		}
		limit(5)
		j, err = covers.EnqueueCover(ctx, f.actor, req)
		if err != nil || j.Deduplicated {
			t.Fatalf("new cover after a failed one: %+v %v", j, err)
		}
		if done = runArtJob(t, ta, j.ID); done.State != jobs.StateDone || done.Actual.USD != .15 {
			t.Fatalf("cover: %s %+v actual=%+v", done.State, done.Error, done.Actual)
		}
		var rows []struct {
			Attempt   int    `db:"attempt"`
			UserID    string `db:"user_id"`
			ProjectID string `db:"project_id"`
			Estimate  int64  `db:"estimate_microusd"`
		}
		if err := ta.Deps.DB.Select(ctx, &rows, `SELECT attempt, user_id, project_id, estimate_microusd FROM spend_reservations WHERE job_id = ?`, j.ID); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Attempt != 1 || rows[0].UserID != f.owner.ID || rows[0].ProjectID != f.project.ID || rows[0].Estimate != 200000 {
			t.Fatalf("reservations %+v", rows)
		}
	})
}

func TestCoverFencesLocksRevisionsAndLease(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		for _, scenario := range []string{"title", "reference", "lock", "cancel", "lease", "suspend"} {
			t.Run(scenario, func(t *testing.T) {
				ctx := context.Background()
				f, covers := coverFixture(t, ta)
				fake := &fakeImages{}
				gen.SetImageServiceForTest(f.gen, fake)
				j, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Variations: 1})
				if err != nil {
					t.Fatal(err)
				}
				fake.hook = func() {
					var err error
					switch scenario {
					case "title":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE chapters SET title = 'Salt & Static' WHERE id = ?`, f.chapter.ID)
					case "reference":
						err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
							a, err := core.MustUse[*assets.Service](ta.Deps).SaveTx(ctx, tx, assets.SaveParams{OwnerID: f.owner.ID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: imagePNG(t, 40, 40)})
							if err == nil {
								_, err = f.studio.AddCharacterRefTx(ctx, tx, f.characters[1].ID, a.ID, "front", "front")
							}
							return err
						})
					case "lock":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = TRUE WHERE id = ?`, f.chapter.ID)
					case "cancel":
						_, err = ta.Deps.Jobs.Cancel(ctx, j.ID)
					case "lease":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE jobs SET attempts = attempts + 1 WHERE id = ?`, j.ID)
					case "suspend":
						_, err = ta.Deps.DB.Exec(ctx, `UPDATE users SET status = 'suspended' WHERE id = ?`, f.owner.ID)
					}
					if err != nil {
						t.Error(err)
					}
				}
				if done := runArtJob(t, ta, j.ID); done.State == jobs.StateDone {
					t.Fatalf("%s: a fenced cover attempt landed", scenario)
				}
				if n := coverAssets(t, ta, f.project.ID); n != 0 {
					t.Fatalf("%s: %d candidates survived the rejected commit", scenario, n)
				}
				objects, err := ta.Deps.Store.List(ctx, "projects/"+f.project.ID+"/"+"cover_candidate/")
				if err != nil || len(objects) != 0 {
					t.Fatalf("%s: staged objects survived %v %v", scenario, objects, err)
				}
			})
		}
	})
}

func TestCoverKeepAndEnqueueRespectLockAndRevision(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		gen.SetImageServiceForTest(f.gen, &fakeImages{})
		req := gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Variations: 2}
		lock := func(on bool) {
			t.Helper()
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET locked = ? WHERE id = ?`, on, f.chapter.ID); err != nil {
				t.Fatal(err)
			}
		}
		lock(true)
		if _, err := covers.EnqueueCover(ctx, f.actor, req); !errors.Is(err, studio.ErrLocked) {
			t.Fatalf("locked chapter enqueue = %v", err)
		}
		lock(false)
		j, err := covers.EnqueueCover(ctx, f.actor, req)
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("cover: %+v", done.Error)
		}
		lock(true)
		if _, err := covers.KeepCover(ctx, f.actor, j.ID, 0); !errors.Is(err, studio.ErrLocked) {
			t.Fatalf("locked chapter keep = %v", err)
		}
		lock(false)
		// Candidates drawn for an earlier title or cast are stale.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE characters SET look = 'Storm coat and a cracked brass compass' WHERE id = ?`, f.characters[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := covers.KeepCover(ctx, f.actor, j.ID, 0); !errors.Is(err, studio.ErrConflict) {
			t.Fatalf("stale keep = %v", err)
		}
		if projectCover(t, ta, f.project.ID) != "" {
			t.Fatal("refused keep changed the cover")
		}
	})
}

func TestCoverRequestsAreDeduplicatedAcrossServerProcesses(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		peer := peerApp(t, ta)
		services := []gen.CoverService{covers, core.MustUse[gen.CoverService](peer.Deps)}
		req := gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, RoutingMode: ai.ModeBalanced}
		first := raceEnqueue(t, "cover", services, func(s gen.CoverService) (*jobs.Job, error) { return s.EnqueueCover(ctx, f.actor, req) })

		// Another routing mode, another chapter or another user is different work.
		fastest := req
		fastest.RoutingMode = ai.ModeFastest
		chapter, err := f.studio.CreateChapter(ctx, f.actor, f.project.ID, "Harbour Ghosts")
		if err != nil {
			t.Fatal(err)
		}
		other := req
		other.ChapterID = chapter.ID
		editor := ta.CreateUser(t, "", auth.RoleCreator)
		if _, err := ta.Deps.DB.Exec(ctx, `INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,?)`, f.project.ID, editor.ID, studio.RoleEditor); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{first.ID: true}
		for name, call := range map[string]func() (*jobs.Job, error){
			"routing": func() (*jobs.Job, error) { return services[1].EnqueueCover(ctx, f.actor, fastest) },
			"chapter": func() (*jobs.Job, error) { return services[0].EnqueueCover(ctx, f.actor, other) },
			"user":    func() (*jobs.Job, error) { return services[1].EnqueueCover(ctx, studio.Actor{UserID: editor.ID}, req) },
		} {
			j, err := call()
			if err != nil || j.Deduplicated || seen[j.ID] {
				t.Fatalf("%s request merged: %+v %v", name, j, err)
			}
			seen[j.ID] = true
		}
		// A changed chapter title is a new cover even while the old job is active.
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE chapters SET title = 'Paper Crows' WHERE id = ?`, f.chapter.ID); err != nil {
			t.Fatal(err)
		}
		retitled, err := services[0].EnqueueCover(ctx, f.actor, req)
		if err != nil || retitled.Deduplicated || seen[retitled.ID] {
			t.Fatalf("retitled cover merged: %+v %v", retitled, err)
		}
		if again, err := services[1].EnqueueCover(ctx, f.actor, req); err != nil || again.ID != retitled.ID || !again.Deduplicated {
			t.Fatalf("repeat after retitle: %+v %v", again, err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE jobs SET state = 'done', finished_at = ? WHERE id = ?`, db.Now(), retitled.ID); err != nil {
			t.Fatal(err)
		}
		if after, err := services[1].EnqueueCover(ctx, f.actor, req); err != nil || after.Deduplicated || after.ID == retitled.ID {
			t.Fatalf("finished cover blocked a new one: %+v %v", after, err)
		}
	})
}

func TestCoverFallbackStaysWithinItsApprovedEnvelope(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, covers := coverFixture(t, ta)
		fake := &fakeImages{fail: 1, planHook: func(p *ai.ImagePlan) {
			p.ProviderID, p.ModelID, p.Remote, p.Estimate = "fixture-remote", "fixture-cover", true, jobs.Cost{USD: .2}
		}}
		gen.SetImageServiceForTest(f.gen, fake)
		j, err := covers.EnqueueCover(ctx, f.actor, gen.CoverRequest{ProjectID: f.project.ID, ChapterID: f.chapter.ID, Variations: 2, RoutingMode: ai.ModeBest})
		if err != nil {
			t.Fatal(err)
		}
		failed := runArtJob(t, ta, j.ID)
		if failed.State != jobs.StateQueued || failed.ProviderID != ai.PlaceholderID {
			t.Fatalf("expected a bounded fallback: %s %s %+v", failed.State, failed.ProviderID, failed.Error)
		}
		done := runArtJob(t, ta, j.ID)
		if done.State != jobs.StateDone || done.Attempts != 2 {
			t.Fatalf("fallback cover: %+v", done.Error)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, r := range fake.requests {
			if r.Plan.Task != ai.TaskCover || r.Plan.RoutingMode != ai.ModeBest || r.Plan.Needs.Count != 2 || r.Background != "opaque" {
				t.Fatalf("policy changed across fallback: %+v", r.Plan)
			}
		}
	})
}
