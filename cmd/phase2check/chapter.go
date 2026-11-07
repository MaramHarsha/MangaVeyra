package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/pagestore"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

var beats = []struct{ action, line string }{
	{"Mina faces the viewer, holding a sealed letter at chest height, calm and determined", "The tide has turned."},
	{"Mina turns three quarters left, studying a folded harbour map with a raised eyebrow", "One bridge remains."},
	{"Mina steps forward, right hand on her satchel strap, wind lifting the ends of her scarf", "I can still make it."},
	{"Mina crouches and ties her right boot lace, looking up with alert eyes", "Every knot matters."},
	{"Mina walks towards the viewer with the letter in her left hand and her right hand shielding her eyes", "The bell is late."},
	{"Mina pauses, half turned right, listening intently with a tense expression", "Was that a voice?"},
	{"Mina faces three quarters left, extending her right hand open at waist height while holding the letter in her left hand", "Take my hand."},
	{"Mina leans forward, looking down at a brass compass in her gloved right palm", "North is below us."},
	{"Mina runs towards the viewer, coat and scarf moving, letter secured to her satchel", "There is no time."},
	{"Mina stands with feet apart, pointing firmly to the left and looking in the same direction", "Follow the lanterns."},
	{"Mina holds a small lantern in her right hand and the letter in her left, concerned", "Someone was here."},
	{"Mina bends her knees, spreading her arms for balance as she crosses an unseen narrow beam", "One step. Then another."},
	{"Mina kneels, carefully lifting the letter above her head, anxious but composed", "Keep the seal dry."},
	{"Mina looks over her left shoulder, both hands holding her satchel strap, surprised", "The water remembers."},
	{"Mina stands front facing, reading the letter's blank outer seal closely with narrowed eyes", "This mark is familiar."},
	{"Mina raises her right arm to wave at a distant person, smiling faintly", "Over here!"},
	{"Mina takes a long stride to the left, reaching forward with her right hand", "Almost there."},
	{"Mina crouches low with her right hand stretched to the side for balance, resolute", "The last crossing."},
	{"Mina stands in a three quarter right view, clutching the letter to her chest, relieved", "The ink survived."},
	{"Mina turns towards the viewer, holding out the sealed letter in both gloved hands", "This belongs to you."},
	{"Mina gestures towards herself with an open right hand, eyebrows lifted in disbelief", "You were waiting for me?"},
	{"Mina stands with her arms loosely folded, scarf ends falling still, thoughtful", "Thirty years of tides."},
	{"Mina looks up with a hopeful expression, right hand resting on the satchel clasp", "Then there is a way."},
	{"Mina steps backwards with both hands raised gently, a cautious smile", "Let the gate breathe."},
	{"Mina extends her left hand towards the viewer, holding the letter in her right hand", "We do this together."},
	{"Mina braces her legs and pulls an unseen rope with both hands, focused", "Now!"},
	{"Mina kneels on one knee, head lowered and hands on her thighs, catching her breath", "Listen to the silence."},
	{"Mina stands front facing, holding the opened envelope carefully in both hands, softly smiling", "A promise can travel."},
	{"Mina turns three quarters left, looking over her shoulder with a warm smile", "I will deliver the answer."},
	{"Mina walks away in a three quarter view, glancing back, one hand raised in farewell", "Before the next tide."},
}

func (h *harness) initialize() error {
	p, err := h.studio.CreateProject(h.ctx, h.actor, studio.NewProject{Title: "Tideglass Relay", Premise: "An original courier carries a letter across a harbour that changes with the tide.", Style: "seinen_bw", Format: "manga_rtl", PageSize: "b5", Language: "en", PageWidth: 600, PageHeight: 840, Genres: []string{"adventure"}})
	if err != nil {
		return err
	}
	h.s.ProjectID = p.ID
	c, err := h.studio.CreateChapter(h.ctx, h.actor, p.ID, "The Last Tide Gate")
	if err != nil {
		return err
	}
	h.s.ChapterID = c.ID
	ch, err := h.studio.CreateCharacter(h.ctx, h.actor, studio.NewCharacter{ProjectID: p.ID, Name: "Mina Sazanami", Role: "courier", RoleLine: "The courier who never abandons a letter", Age: "22", Category: "main", Look: "Young adult woman, short straight black bob ending at her jaw, blunt bangs, one small white diamond hair clip above her left temple, dark almond eyes. Slim athletic build. The same knee-length dark courier coat with broad white V-shaped lapels, white gloves, narrow black-and-white striped scarf, dark fitted trousers and ankle boots. A cross-body dark leather satchel with one round pale metal clasp. Keep these exact facial features, hair, clip and clothing in every view.", Personality: "Observant, patient, quietly brave", SpeechStyle: "Short, concrete sentences"})
	if err != nil {
		return err
	}
	h.s.CharacterID = ch.ID
	// A temporary session belongs only to this isolated database. Never print it.
	token, _, err := h.a.Deps.Auth.CreateSession(h.ctx, h.s.UserID, false, "127.0.0.1", "phase2-acceptance")
	if err != nil {
		return err
	}
	cookie := map[string]any{"name": "mv_session", "value": token, "domain": "127.0.0.1", "path": "/", "expires": -1, "httpOnly": true, "secure": false, "sameSite": "Lax"}
	b, _ := json.Marshal(map[string]any{"cookies": []any{cookie}, "origins": []any{}})
	return os.WriteFile(filepath.Join(h.root, "browser-session.json"), b, 0600)
}

func (h *harness) references() error {
	c, err := h.studio.GetCharacter(h.ctx, h.s.CharacterID)
	if err != nil {
		return err
	}
	j, err := h.step("reference-sheet", func() (*jobs.Job, error) {
		return h.gen.EnqueueCharacterSheet(h.ctx, h.actor, gen.CharacterSheetRequest{ProjectID: h.s.ProjectID, CharacterID: c.ID, Count: 1, RoutingMode: "best", ExpectedUpdatedAt: c.UpdatedAt})
	})
	if err != nil {
		return err
	}
	if j.ProviderID != h.s.ProviderID || j.ModelID != modelID {
		return fmt.Errorf("reference used an unapproved route: %s", j.ModelID)
	}
	c, err = h.studio.GetCharacter(h.ctx, c.ID)
	if err != nil {
		return err
	}
	for i, r := range c.Refs {
		data, _, e := core.MustUse[*assets.Service](h.a.Deps).Read(h.ctx, r.AssetID, assets.MaxBytes)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(h.root, fmt.Sprintf("reference-%d.png", i)), data, 0600); e != nil {
			return e
		}
	}
	return nil
}

func (h *harness) registerStructure() {
	h.a.Deps.Executor.Register("qa.phase2.structure", func(ctx context.Context, run *jobs.Run) error {
		return run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
			doc := studio.ScriptDoc{Schema: "mangaveyra.script.v1", ChapterID: h.s.ChapterID, ChapterSummary: "An original 30-page visual continuity assessment, authored by Codex. Three intentionally recurring harbour background plates; thirty separately generated Mina appearances. Lettering stays editable."}
			scene := studio.ScriptScene{Key: "last-tide", Title: "The Last Tide Gate", Summary: doc.ChapterSummary, TimeOfDay: "dawn"}
			for i, b := range beats {
				scene.Panels = append(scene.Panels, studio.ScriptPanel{Key: fmt.Sprintf("tide-%02d", i+1), PageNumber: i + 1, Camera: "eye level", Shot: "wide", Emotion: "quiet determination", Action: b.action, CharacterIDs: []string{h.s.CharacterID}, Dialogue: []studio.ScriptDialogue{{Speaker: "Mina", CharacterID: h.s.CharacterID, Kind: "speech", Text: b.line}}, Notes: "One portrait-format single-panel page, original black-and-white ink manga. Character, scenery, FX and structured lettering are independent."})
			}
			doc.Scenes = []studio.ScriptScene{scene}
			if _, err := h.studio.ReplaceGeneratedScriptTx(ctx, tx, studio.ReplaceScriptInput{Actor: h.actor, ProjectID: h.s.ProjectID, ChapterID: h.s.ChapterID, Doc: doc, JobID: run.Job.ID, ModelID: "codex-authored-acceptance-script", Provenance: "ai"}); err != nil {
				return nil, err
			}
			// Documents come from this validated source and retain the AI job
			// attribution. Reads through another connection cannot see this tx.
			input := make([]pagedoc.Page, 0, 30)
			for i, b := range beats {
				p := pagedoc.New(db.NewID(), h.s.ProjectID, h.s.ChapterID, i+1, 600, 840, "rtl")
				p.Note = "Original Codex-authored acceptance storyboard; repeated scenery plates are intentional. Each character appearance is generated separately."
				pn := pagedoc.Panel{ID: db.NewID(), Order: 0, Points: pagedoc.RectPoints(pagedoc.Rect{X: 20, Y: 20, W: 560, H: 800}), Prompt: b.action + ". Single original manga character, full body, strong readable silhouette. Clothing, face and hair must exactly follow the kept reference sheet.", PromptSource: "script", Camera: "eye level", Shot: "wide", Style: "seinen_bw"}
				for _, kind := range []string{"background", "character", "fx", "lettering"} {
					l := pagedoc.Layer{ID: db.NewID(), Kind: kind, Order: len(pn.Layers), Visible: true, Opacity: 1, Provenance: "ai"}
					if kind == "character" {
						l.CharacterID = h.s.CharacterID
						l.Name = "Mina Sazanami"
					}
					pn.Layers = append(pn.Layers, l)
				}
				p.Panels = []pagedoc.Panel{pn}
				p.Bubbles = []pagedoc.Bubble{{ID: db.NewID(), PanelID: pn.ID, Kind: "speech", Shape: "ellipse", Box: pagedoc.Rect{X: 360, Y: 40, W: 190, H: 85}, Texts: map[string]pagedoc.BubbleText{"en": {Text: b.line, Source: "ai", Status: "draft"}}}}
				input = append(input, p)
			}
			made, err := h.pages.CreatePagesTx(ctx, tx, h.s.ChapterID, input, pagestore.Change{ActorKind: "system", ActorID: h.s.UserID, JobID: run.Job.ID, Action: "create_page", Summary: "Codex-authored live acceptance storyboard"})
			if err != nil {
				return nil, err
			}
			if err = h.studio.SetChapterAILabelTx(ctx, tx, h.s.ChapterID, "lettering", "ai"); err != nil {
				return nil, err
			}
			return map[string]int{"pages": len(made)}, nil
		})
	})
}

func (h *harness) chapter(upTo int) error {
	if _, err := h.step("chapter-structure", func() (*jobs.Job, error) {
		return h.a.Deps.Jobs.Enqueue(h.ctx, jobs.EnqueueParams{Type: "qa.phase2.structure", Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: h.s.ProjectID, ChapterID: h.s.ChapterID}, UserID: h.s.UserID, Executor: jobs.ExecutorGo, MaxAttempts: 1})
	}); err != nil {
		return err
	}
	list, err := h.pages.ListPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	if len(list) != 30 {
		return fmt.Errorf("expected 30 pages, found %d", len(list))
	}
	for i := 0; i < upTo; i++ {
		p, err := h.pages.GetPage(h.ctx, list[i].ID)
		if err != nil {
			return err
		}
		pn := p.Panels[0]
		layer := pn.Layers[1]
		if _, err = h.step(fmt.Sprintf("appearance-%02d", i+1), func() (*jobs.Job, error) {
			return h.gen.EnqueuePanel(h.ctx, h.actor, gen.PanelRequest{ProjectID: h.s.ProjectID, PageID: p.ID, PanelID: pn.ID, LayerID: layer.ID, Variations: 1, RoutingMode: "best", PinModelID: modelID, ExpectedVersion: p.Version})
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) report() error {
	var charges struct {
		Calls   int   `db:"calls" json:"calls"`
		Micro   int64 `db:"micro" json:"accounted_microusd"`
		Pending int   `db:"pending" json:"pending_reservations"`
	}
	if err := h.a.Deps.DB.Get(h.ctx, &charges, `SELECT COUNT(*) AS calls,COALESCE(SUM(COALESCE(actual_microusd,estimate_microusd)),0) AS micro,COALESCE(SUM(CASE WHEN state='reserved' THEN 1 ELSE 0 END),0) AS pending FROM spend_reservations`); err != nil {
		return err
	}
	status, err := h.control.LockChapterStatus(h.ctx, h.actor, h.s.ProjectID, h.s.CharacterID, h.s.ChapterID)
	if err != nil {
		return err
	}
	var scores []struct {
		Page      int      `db:"number" json:"page"`
		Asset     string   `db:"asset_id" json:"asset_id"`
		Identity  float64  `db:"identity_score" json:"identity"`
		Outfit    *float64 `db:"outfit_score" json:"outfit"`
		Method    string   `db:"method" json:"method"`
		Reference string   `db:"reference_hash" json:"reference_fingerprint"`
	}
	err = h.a.Deps.DB.Select(h.ctx, &scores, `SELECT pg.number,l.asset_id,a.identity_score,a.outfit_score,a.method,a.reference_hash FROM layers l JOIN panels pn ON pn.id=l.panel_id JOIN pages pg ON pg.id=pn.page_id JOIN character_assessments a ON a.asset_id=l.asset_id AND a.character_id=l.character_id WHERE pg.chapter_id=? AND l.character_id=? AND a.reference_hash=? AND a.method=? ORDER BY pg.number,a.created_at DESC`, h.s.ChapterID, h.s.CharacterID, status.ReferenceFingerprint, status.Method)
	if err != nil {
		return err
	}
	evidence := map[string]any{"charges": charges, "cap_usd": capUSD, "status": status, "appearances": scores, "script": "Codex-authored original acceptance storyboard, labeled AI", "quality_limit": "0.90 is the product drift policy, not an identity probability; human visual review remains required"}
	b, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(h.root, "report.json"), b, 0600); err != nil {
		return err
	}
	art := filepath.Join(h.root, "evidence")
	if err = os.MkdirAll(art, 0700); err != nil {
		return err
	}
	pages, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	for _, p := range pages {
		if p.Panels[0].Layers[1].Asset == nil {
			continue
		}
		pixels, e := core.MustUse[compose.Renderer](h.a.Deps).PNG(h.ctx, p, compose.Options{Lang: "en", Scale: .8})
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(art, fmt.Sprintf("page-%02d.png", p.Number)), pixels, 0600); e != nil {
			return e
		}
	}
	fmt.Printf("%d remote calls · accounted USD %.6f / %.2f · %d appearances · %d drift · %d unassessed\n", charges.Calls, float64(charges.Micro)/1e6, capUSD, status.Panels, status.DriftTotal, status.Unscored)
	return nil
}
