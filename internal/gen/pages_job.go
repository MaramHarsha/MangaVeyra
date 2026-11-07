package gen

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/lettering"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

type pagesPayload struct {
	Version           int                    `json:"version"`
	Request           PagesFromScriptRequest `json:"request"`
	Actor             storedActor            `json:"actor"`
	ExpectedUpdatedAt time.Time              `json:"expected_updated_at"`
	Estimate          jobs.Cost              `json:"estimate"`
	Remote            bool                   `json:"remote"`
}
type pagesOutput struct {
	Pages []string `json:"pages"`
	Jobs  []string `json:"jobs"`
}

func (s *service) checkPagesTx(ctx context.Context, tx *db.Tx, p pagesPayload) error {
	actor := studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
	if p.Request.Replace && actor.TokenID != "" {
		return studio.ErrForbidden
	}
	if err := s.authorizeImageTx(ctx, tx, actor, p.Request.ProjectID); err != nil {
		return err
	}
	if p.Request.Letter {
		if err := s.studio.AuthorizeTx(ctx, tx, actor, p.Request.ProjectID, studio.ActLetter); err != nil {
			return err
		}
	}
	if err := s.approvalTx(ctx, tx, actor, p.Estimate, p.Remote); err != nil {
		return err
	}
	if err := s.studio.CheckScopeWritableTx(ctx, tx, jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: p.Request.ProjectID, ChapterID: p.Request.ChapterID}); err != nil {
		return err
	}
	var rev time.Time
	if err := tx.Get(ctx, &rev, `SELECT updated_at FROM chapters WHERE id=? AND project_id=?`+lockSQL(tx), p.Request.ChapterID, p.Request.ProjectID); err != nil {
		return err
	}
	if !rev.UTC().Equal(p.ExpectedUpdatedAt.UTC()) {
		return studio.ErrConflict
	}
	var count int
	if err := tx.Get(ctx, &count, `SELECT COUNT(*) FROM pages WHERE chapter_id=?`, p.Request.ChapterID); err != nil {
		return err
	}
	if count > 0 && !p.Request.Replace {
		return studio.ErrConflict
	}
	if p.Request.Replace {
		var locked int
		if err := tx.Get(ctx, &locked, `SELECT COUNT(*) FROM pages p WHERE p.chapter_id=? AND (p.locked=TRUE OR EXISTS(SELECT 1 FROM panels pn WHERE pn.page_id=p.id AND pn.locked=TRUE) OR EXISTS(SELECT 1 FROM panels pn JOIN layers l ON l.panel_id=pn.id WHERE pn.page_id=p.id AND l.locked=TRUE))`, p.Request.ChapterID); err != nil {
			return err
		}
		if locked > 0 {
			return studio.ErrLocked
		}
	}
	return nil
}
func (s *service) EnqueuePagesFromScript(ctx context.Context, actor studio.Actor, req PagesFromScriptRequest) (*jobs.Job, error) {
	if !db.ValidID(req.ProjectID) || !db.ValidID(req.ChapterID) || req.Replace && actor.TokenID != "" || req.RoutingMode != "" && !validRoutingMode(req.RoutingMode) {
		return nil, studio.ErrInvalid
	}
	if _, err := s.resolveImageActor(ctx, actor, req.ProjectID, jobs.Cost{}, false); err != nil {
		return nil, err
	}
	ch, err := s.studio.GetChapter(ctx, req.ChapterID)
	if err != nil {
		return nil, err
	}
	if ch.ProjectID != req.ProjectID {
		return nil, studio.ErrForbidden
	}
	if !req.ExpectedUpdatedAt.IsZero() && !req.ExpectedUpdatedAt.UTC().Equal(ch.UpdatedAt.UTC()) {
		return nil, studio.ErrConflict
	}
	req.ExpectedUpdatedAt = ch.UpdatedAt
	p := pagesPayload{Version: imagePayloadVersion, Request: req, Actor: storedActor{actor.UserID, actor.TokenID}, ExpectedUpdatedAt: ch.UpdatedAt}
	if req.Generate {
		pages, err := s.scriptPages(ctx, req)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, page := range pages {
			for _, pn := range page.Panels {
				n++
				if n > 256 {
					return nil, studio.ErrInvalid
				}
				child, err := s.preparePanel(ctx, actor, PanelRequest{ProjectID: req.ProjectID, PageID: page.ID, PanelID: pn.ID, Variations: 1, RoutingMode: req.RoutingMode, ExpectedVersion: page.Version}, page, "")
				if err != nil {
					return nil, err
				}
				cost, err := scaleCost(child.Plan.Estimate, len(child.Components)*imageAttempts)
				if err != nil {
					return nil, err
				}
				p.Estimate = p.Estimate.Add(cost)
				p.Remote = p.Remote || child.Plan.Remote
			}
		}
		if _, err = s.resolveImageActor(ctx, actor, req.ProjectID, p.Estimate, p.Remote); err != nil {
			return nil, err
		}
	}
	return s.queue.Enqueue(ctx, jobs.EnqueueParams{Estimate: p.Estimate, Type: JobPagesFromScript, Executor: jobs.ExecutorGo, Scope: jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: req.ProjectID, ChapterID: req.ChapterID}, UserID: actor.UserID, Input: p, MaxAttempts: imageAttempts, Guard: func(ctx context.Context, tx *db.Tx) error { return s.checkPagesTx(ctx, tx, p) }})
}
func (s *service) scriptPages(ctx context.Context, req PagesFromScriptRequest) ([]pagedoc.Page, error) {
	project, err := s.studio.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	script, err := s.studio.GetScript(ctx, req.ChapterID)
	if err != nil {
		return nil, err
	}
	if err = s.studio.ValidateScript(ctx, req.ProjectID, script); err != nil {
		return nil, err
	}
	characters, err := s.studio.ListCharacters(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, c := range characters {
		names[c.ID] = c.Name
	}
	grouped := map[int][]studio.ScriptPanel{}
	for _, scene := range script.Scenes {
		for _, panel := range scene.Panels {
			grouped[panel.PageNumber] = append(grouped[panel.PageNumber], panel)
		}
	}
	if len(grouped) == 0 || len(grouped) > 256 {
		return nil, studio.ErrInvalid
	}
	numbers := make([]int, 0, len(grouped))
	for n := range grouped {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	pages := make([]pagedoc.Page, 0, len(numbers))
	for _, n := range numbers {
		panels := grouped[n]
		if len(panels) > pagedoc.MaxPanels {
			return nil, studio.ErrInvalid
		}
		page := pagedoc.New(db.NewID(), project.ID, req.ChapterID, len(pages)+1, project.PageWidth, project.PageHeight, pagedoc.DirectionForFormat(project.Format))
		points, layoutID := scriptLayout(len(panels), page)
		page.LayoutID = layoutID
		for i, sp := range panels {
			pn := pagedoc.Panel{ID: db.NewID(), Order: i, Points: points[i], ScriptPanelID: sp.ID, Prompt: truncateRunes(strings.Join([]string{sp.Camera, sp.Shot, sp.Emotion, sp.Action, sp.Notes}, ". "), pagedoc.MaxPromptRunes), PromptSource: "script", Camera: sp.Camera, Shot: sp.Shot, Style: project.Style, Pose: map[string]any{}, Layers: []pagedoc.Layer{}}
			add := func(kind, name, cid string) {
				pn.Layers = append(pn.Layers, pagedoc.Layer{ID: db.NewID(), Kind: kind, Name: name, CharacterID: cid, Order: len(pn.Layers), Visible: true, Opacity: 1, Blend: "normal", Provenance: pagedoc.ProvenanceHuman})
			}
			add(pagedoc.LayerBackground, "Background", "")
			for _, id := range sp.CharacterIDs {
				if names[id] == "" {
					return nil, studio.ErrForbidden
				}
				add(pagedoc.LayerCharacter, names[id], id)
			}
			add(pagedoc.LayerFX, "Effects", "")
			add(pagedoc.LayerLettering, "Lettering", "")
			if len(pn.Layers) > pagedoc.MaxLayersPerPane {
				return nil, studio.ErrInvalid
			}
			page.Panels = append(page.Panels, pn)
			if req.Letter {
				lines := make([]lettering.Line, 0, len(sp.Dialogue)+len(sp.SFX))
				for _, d := range sp.Dialogue {
					lines = append(lines, lettering.Line{Kind: d.Kind, Text: d.Text, CharacterID: d.CharacterID, ScriptPanelID: sp.ID})
				}
				for _, fx := range sp.SFX {
					lines = append(lines, lettering.Line{Kind: pagedoc.BubbleSFX, Text: fx.Text, ScriptPanelID: sp.ID})
				}
				bubbles, err := s.lettering.AutoPlace(page, pn.ID, lines, project.Language, pagedoc.VerticalByDefault(project.Language))
				if err != nil {
					return nil, err
				}
				page.Bubbles = append(page.Bubbles, bubbles...)
			}
		}
		if err = page.Validate(); err != nil {
			return nil, err
		}
		pages = append(pages, page)
	}
	return pages, nil
}
func scriptLayout(n int, p pagedoc.Page) ([][]pagedoc.Point, string) {
	for _, l := range pagedoc.Suggest(n, p.Direction) {
		if l.Panels == n {
			return l.PanelPoints(p.Width, p.Height, p.Direction), l.ID
		}
	}
	cols := min(3, int(math.Ceil(math.Sqrt(float64(n)))))
	rows := (n + cols - 1) / cols
	margin := float64(p.Width) * .05
	gap := float64(p.Width) * .018
	w := (float64(p.Width) - 2*margin - float64(cols-1)*gap) / float64(cols)
	h := (float64(p.Height) - 2*margin - float64(rows-1)*gap) / float64(rows)
	var out [][]pagedoc.Point
	for i := 0; i < n; i++ {
		col := i % cols
		if p.Direction == pagedoc.DirRTL {
			col = cols - col - 1
		}
		out = append(out, pagedoc.RectPoints(pagedoc.Rect{X: margin + float64(col)*(w+gap), Y: margin + float64(i/cols)*(h+gap), W: w, H: h}))
	}
	return out, "script-grid"
}
func (s *service) handlePages(ctx context.Context, run *jobs.Run) error {
	var p pagesPayload
	if _, err := strictDecodeJSON(run.Job.Input, 64<<10, &p); err != nil || p.Version != imagePayloadVersion || p.Actor.UserID != run.Job.UserID || p.ExpectedUpdatedAt.IsZero() || run.Job.Type != JobPagesFromScript || run.Job.Executor != jobs.ExecutorGo || run.Job.Scope.Kind != jobs.ScopeChapter || p.Request.ProjectID != run.Job.Scope.ProjectID || p.Request.ChapterID != run.Job.Scope.ChapterID {
		return mapImageJobError(ctx, studio.ErrInvalid)
	}
	actor := studio.Actor{UserID: p.Actor.UserID, TokenID: p.Actor.TokenID}
	if err := s.db.WithTx(ctx, func(tx *db.Tx) error { return s.checkPagesTx(ctx, tx, p) }); err != nil {
		return mapImageJobError(ctx, err)
	}
	pages, err := s.scriptPages(ctx, p.Request)
	if err != nil {
		return mapImageJobError(ctx, err)
	}
	children := []imagePayload{}
	total := jobs.Cost{}
	if p.Request.Generate {
		for _, page := range pages {
			for _, pn := range page.Panels {
				if len(children) >= 256 {
					return mapImageJobError(ctx, studio.ErrInvalid)
				}
				child, err := s.preparePanel(ctx, actor, PanelRequest{ProjectID: p.Request.ProjectID, PageID: page.ID, PanelID: pn.ID, Variations: 1, RoutingMode: p.Request.RoutingMode, ExpectedVersion: page.Version}, page, run.Job.ID)
				if err != nil {
					return mapImageJobError(ctx, err)
				}
				cost, err := scaleCost(child.Plan.Estimate, len(child.Components)*imageAttempts)
				if err != nil {
					return mapImageJobError(ctx, err)
				}
				total = total.Add(cost)
				children = append(children, child)
			}
		}
		if !validCost(total) || total.USD > p.Estimate.USD+.0000001 || total.GPUSeconds > p.Estimate.GPUSeconds+.0000001 {
			return mapImageJobError(ctx, studio.ErrInvalid)
		}
		if _, err = s.resolveImageActor(ctx, actor, p.Request.ProjectID, total, total.USD > 0); err != nil {
			return mapImageJobError(ctx, err)
		}
	}
	returnErr := run.CommitSideEffect(ctx, func(ctx context.Context, tx *db.Tx) (any, error) {
		if err := s.checkPagesTx(ctx, tx, p); err != nil {
			return nil, err
		}
		var saved []pagedoc.Page
		var err error
		ch := changeFor(actor, run.Job.ID, "pages_from_script")
		if p.Request.Replace {
			saved, err = s.pages.ReplaceChapterPagesTx(ctx, tx, p.Request.ChapterID, pages, ch)
		} else {
			saved, err = s.pages.CreatePagesTx(ctx, tx, p.Request.ChapterID, pages, ch)
		}
		if err != nil {
			return nil, err
		}
		out := pagesOutput{Pages: []string{}, Jobs: []string{}}
		stored := map[string]pagedoc.Page{}
		for _, page := range saved {
			out.Pages = append(out.Pages, page.ID)
			stored[page.ID] = page
		}
		for _, child := range children {
			page, ok := stored[child.PageID]
			if !ok {
				return nil, errors.New("generated page identity changed")
			}
			pn := page.Panel(child.PanelID)
			if pn == nil {
				return nil, studio.ErrInvalid
			}
			child.ExpectedVersion = page.Version
			child.TargetHash = hashPanel(*pn)
			cost, err := scaleCost(child.Plan.Estimate, len(child.Components)*imageAttempts)
			if err != nil {
				return nil, err
			}
			j, err := s.queue.EnqueueTx(ctx, tx, s.imageParams(child, actor, cost))
			if err != nil {
				return nil, err
			}
			out.Jobs = append(out.Jobs, j.ID)
		}
		return out, nil
	})
	if returnErr != nil {
		return mapImageJobError(ctx, returnErr)
	}
	return nil
}
