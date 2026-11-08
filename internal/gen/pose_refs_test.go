package gen_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

// A hosted route sends only its first references. With a pose, the character's
// identity references stay first and the pose guide is last, so the pose never
// displaces an identity reference.
func TestPoseGuideFollowsTheCharactersIdentityReferences(t *testing.T) {
	forEachGenDialect(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		f, ps, pages := controlPages(t, ta, 1)
		sheet, err := core.MustUse[*assets.Service](ta.Deps).Save(ctx, assets.SaveParams{OwnerID: f.actor.UserID, ProjectID: f.project.ID, Kind: "reference", Source: assets.SourceUpload, Data: imagePNG(t, 160, 120)})
		if err != nil {
			t.Fatal(err)
		}
		if err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			_, err := f.studio.AddCharacterRefTx(ctx, tx, f.characters[0].ID, sheet.ID, "sheet", "Kept sheet")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		target := pages[0]
		poseBytes, _ := json.Marshal(pagedoc.PosePresets()["crouch"])
		if err := json.Unmarshal(poseBytes, &target.Panels[0].Pose); err != nil {
			t.Fatal(err)
		}
		if _, err := ps.SavePage(ctx, target, target.Version, pagestore.Change{ActorKind: "user", ActorID: f.actor.UserID, Action: "save"}); err != nil {
			t.Fatal(err)
		}
		target, err = ps.GetPage(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		fake := &fakeImages{}
		gen.SetImageServiceForTest(f.gen, fake)
		controls := core.MustUse[gen.ControlService](ta.Deps)
		j, err := controls.EnqueuePage(ctx, f.actor, gen.PageRequest{ProjectID: f.project.ID, PageID: target.ID, ExpectedVersion: target.Version})
		if err != nil {
			t.Fatal(err)
		}
		if done := runArtJob(t, ta, j.ID); done.State != jobs.StateDone {
			t.Fatalf("pose generation: %+v", done.Error)
		}
		found := false
		for _, call := range fake.requests {
			refs := call.References
			if len(refs) == 0 || refs[len(refs)-1].Role != "pose" {
				continue
			}
			found = true
			if len(refs) != 3 {
				t.Fatalf("character call references = %d, want 2 identity + pose", len(refs))
			}
			for _, r := range refs[:len(refs)-1] {
				if r.Role != "reference" {
					t.Fatalf("identity reference displaced: %+v", refs)
				}
			}
		}
		if !found {
			t.Fatal("the pose guide was not the last reference of the character call")
		}
	})
}
