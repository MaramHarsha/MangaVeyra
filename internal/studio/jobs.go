package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/notify"
)

// Job types the studio enqueues. The gen module implements them; until it
// registers its handlers the studio's built-in placeholder handlers run them
// (registered at start-up only for types no other module claimed).
const (
	// JobScriptOutline: input OutlineInput, scope project, output ScriptDraft.
	JobScriptOutline = "script.outline"
	// JobScriptAssist: input AssistInput, scope chapter, output ScriptDiff.
	JobScriptAssist = "script.assist"
	// JobCharacterSheet: input SheetInput, scope project, output SheetOutput.
	JobCharacterSheet = "character.sheet"
	// JobCharacterTrain: input TrainInput, scope project, output TrainOutput.
	JobCharacterTrain = "character.train"
)

// SheetInput is the input of a character.sheet job.
type SheetInput struct {
	CharacterID string `json:"character_id"`
	Prompt      string `json:"prompt"`
	N           int    `json:"n"`
	Style       string `json:"style"`
	Actor       string `json:"actor,omitempty"`
}

// SheetImage is one generated sheet (the key of a stored image; AssetID when known).
type SheetImage struct {
	Key     string `json:"key"`
	AssetID string `json:"asset_id,omitempty"`
	Seed    int64  `json:"seed"`
}

// SheetOutput is the output of a character.sheet job.
type SheetOutput struct {
	Images []SheetImage `json:"images"`
}

// TrainInput is the input of a character.train job.
type TrainInput struct {
	CharacterID string   `json:"character_id"`
	Refs        []string `json:"refs"`
	Trigger     string   `json:"trigger,omitempty"`
	Actor       string   `json:"actor,omitempty"`
}

// TrainOutput is the output of a character.train job.
type TrainOutput struct {
	LoRA *struct {
		Key string `json:"key"`
	} `json:"lora,omitempty"`
	Score      float64      `json:"score"`
	Model      string       `json:"model,omitempty"`
	Method     string       `json:"method,omitempty"`
	TestImages []SheetImage `json:"test_images,omitempty"`
}

func actorWord(a Actor) string {
	if a.IsAgent() {
		return ActorAgent
	}
	return ""
}

func (s *Service) enqueue(ctx context.Context, a Actor, typ string, scope jobs.Scope, input any, est jobs.Cost, priority int) (*jobs.Job, error) {
	if s.d.Jobs == nil {
		return nil, errors.New("studio: no job queue")
	}
	return s.d.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: typ, Scope: scope, UserID: a.UserID, Input: input, Estimate: est, Priority: priority})
}

// EnqueueOutline starts script.outline for a project created by the wizard.
func (s *Service) EnqueueOutline(ctx context.Context, a Actor, p *Project, in OutlineInput) (*jobs.Job, error) {
	in.ProjectID, in.Actor = p.ID, actorWord(a)
	j, err := s.enqueue(ctx, a, JobScriptOutline, jobs.Scope{Kind: jobs.ScopeProject, ProjectID: p.ID}, in, jobs.Cost{GPUSeconds: 90}, 5)
	if err != nil {
		return nil, err
	}
	_ = s.SetProjectSetting(ctx, p.ID, "outline_job_id", j.ID)
	return j, nil
}

// ScriptAssist is one script assistant request and its proposal.
type ScriptAssist struct {
	ID        string              `db:"id" json:"id"`
	ProjectID string              `db:"project_id" json:"project_id"`
	ChapterID string              `db:"chapter_id" json:"chapter_id"`
	SceneID   string              `db:"scene_id" json:"scene_id,omitempty"`
	UserID    string              `db:"user_id" json:"user_id,omitempty"`
	Request   string              `db:"request" json:"request"`
	JobID     string              `db:"job_id" json:"job_id,omitempty"`
	Status    string              `db:"status" json:"status"`
	Diff      db.JSON[ScriptDiff] `db:"diff" json:"diff"`
	Error     string              `db:"error" json:"error,omitempty"`
	CreatedAt time.Time           `db:"created_at" json:"created_at"`
	UpdatedAt time.Time           `db:"updated_at" json:"updated_at"`
}

const assistCols = `id, project_id, chapter_id, COALESCE(scene_id, '') AS scene_id, COALESCE(user_id, '') AS user_id, request,
	COALESCE(job_id, '') AS job_id, status, diff, error, created_at, updated_at`

// Assists lists a chapter's script assistant requests, oldest first (last 20).
func (s *Service) Assists(ctx context.Context, chapterID string) ([]ScriptAssist, error) {
	var out []ScriptAssist
	err := s.db.Select(ctx, &out, `SELECT `+assistCols+` FROM script_assists WHERE chapter_id = ? ORDER BY created_at DESC LIMIT 20`, chapterID)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// Assist returns one assistant request.
func (s *Service) Assist(ctx context.Context, projectID, id string) (*ScriptAssist, error) {
	var a ScriptAssist
	if err := s.db.Get(ctx, &a, `SELECT `+assistCols+` FROM script_assists WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
		return nil, notFound(err)
	}
	return &a, nil
}

// RequestAssist records a request and enqueues script.assist with {chapter_id, scene_id, request}.
func (s *Service) RequestAssist(ctx context.Context, a *Access, chapterID, sceneID, request string) (*ScriptAssist, error) {
	if err := s.require(ctx, a, EditScript); err != nil {
		return nil, err
	}
	ch, err := s.ChapterByID(ctx, chapterID)
	if err != nil || ch.ProjectID != a.Project.ID {
		return nil, core.ErrNotFound()
	}
	if request = clip(request, 1000); request == "" {
		return nil, core.ErrValidation(map[string]string{"request": i18n.T(ctx, "studio.form.required")})
	}
	if sceneID != "" {
		if sc, err := s.Scene(ctx, sceneID); err != nil || sc.ChapterID != chapterID {
			return nil, core.ErrNotFound()
		}
	}
	now := s.now()
	as := &ScriptAssist{ID: db.NewID(), ProjectID: a.Project.ID, ChapterID: chapterID, SceneID: sceneID, UserID: a.Actor.UserID, Request: request,
		Status: "pending", CreatedAt: now, UpdatedAt: now}
	if _, err := s.db.Exec(ctx, `INSERT INTO script_assists (id, project_id, chapter_id, scene_id, user_id, request, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, as.ID, as.ProjectID, chapterID, db.NullString(sceneID), db.NullString(as.UserID), request, now, now); err != nil {
		return nil, err
	}
	j, err := s.enqueue(ctx, a.Actor, JobScriptAssist, jobs.Scope{Kind: jobs.ScopeChapter, ProjectID: a.Project.ID, ChapterID: chapterID},
		AssistInput{ChapterID: chapterID, SceneID: sceneID, Request: request, AssistID: as.ID, Actor: actorWord(a.Actor)}, jobs.Cost{GPUSeconds: 20}, 10)
	if err != nil {
		_, _ = s.db.Exec(ctx, `DELETE FROM script_assists WHERE id = ?`, as.ID)
		return nil, err
	}
	as.JobID = j.ID
	_, _ = s.db.Exec(ctx, `UPDATE script_assists SET job_id = ? WHERE id = ?`, j.ID, as.ID)
	return as, nil
}

// ApplyAssist applies a ready proposal to the chapter's script.
func (s *Service) ApplyAssist(ctx context.Context, a *Access, id string) error {
	as, err := s.Assist(ctx, a.Project.ID, id)
	if err != nil {
		return err
	}
	if as.Status != "ready" {
		return core.ErrConflict(i18n.T(ctx, "studio.assist.not_ready"))
	}
	if err := s.ApplyScriptDiff(ctx, a, as.ChapterID, as.Diff.V); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE script_assists SET status = 'applied', updated_at = ? WHERE id = ?`, s.now(), id)
	return err
}

// DismissAssist marks a proposal dismissed.
func (s *Service) DismissAssist(ctx context.Context, a *Access, id string) error {
	if _, err := s.Assist(ctx, a.Project.ID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE script_assists SET status = 'dismissed', updated_at = ? WHERE id = ? AND status IN ('ready', 'failed', 'pending')`, s.now(), id)
	return err
}

// GenerateSheets enqueues character.sheet {character_id, prompt, n: 4, style}.
func (s *Service) GenerateSheets(ctx context.Context, a *Access, c *Character, style string) (*jobs.Job, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	prompt := SheetPrompt(c, a.Project)
	j, err := s.enqueue(ctx, a.Actor, JobCharacterSheet, jobs.Scope{Kind: jobs.ScopeProject, ProjectID: a.Project.ID},
		SheetInput{CharacterID: c.ID, Prompt: prompt, N: 4, Style: style, Actor: actorWord(a.Actor)}, jobs.Cost{GPUSeconds: 30}, 10)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `UPDATE characters SET sheet_job_id = ?, style = ?, updated_at = ? WHERE id = ?`, j.ID, clip(style, 40), s.now(), c.ID)
	return j, err
}

// SheetPrompt builds the reference-sheet prompt of a character.
func SheetPrompt(c *Character, p *Project) string {
	parts := []string{"character reference sheet: front, side and face views, neutral pose, plain background"}
	if c.Name != "" {
		parts = append(parts, c.Name)
	}
	if c.RoleLine != "" {
		parts = append(parts, c.RoleLine)
	}
	if c.Age != "" {
		parts = append(parts, "age "+c.Age)
	}
	if c.Look != "" {
		parts = append(parts, c.Look)
	}
	if c.Personality != "" {
		parts = append(parts, "expression suits: "+c.Personality)
	}
	if p != nil && p.Style != "" {
		parts = append(parts, "style: "+p.Style)
	}
	return clip(joinNonEmpty(parts, "; "), 3000)
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// SheetCandidates returns the sheets of the character's latest character.sheet job and the job.
func (s *Service) SheetCandidates(ctx context.Context, c *Character) (*jobs.Job, []SheetImage, error) {
	if c.SheetJobID == "" || s.d.Jobs == nil {
		return nil, nil, nil
	}
	j, err := s.d.Jobs.Get(ctx, c.SheetJobID)
	if err != nil {
		return nil, nil, nil
	}
	var out SheetOutput
	if j.State == jobs.StateDone {
		_ = j.DecodeOutput(&out)
		for i := range out.Images {
			if out.Images[i].AssetID == "" && out.Images[i].Key != "" {
				if as, err := s.AssetByKey(ctx, out.Images[i].Key); err == nil {
					out.Images[i].AssetID = as.ID
				}
			}
		}
	}
	return j, out.Images, nil
}

// PickSheet keeps one generated sheet as the character's reference sheet and portrait.
func (s *Service) PickSheet(ctx context.Context, a *Access, c *Character, assetID string) error {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return err
	}
	_, imgs, _ := s.SheetCandidates(ctx, c)
	ok := false
	for _, im := range imgs {
		if im.AssetID == assetID {
			ok = true
		}
	}
	if !ok {
		return core.ErrValidation(map[string]string{"sheet": i18n.T(ctx, "studio.form.invalid")})
	}
	refs, _ := s.CharacterRefs(ctx, c.ID)
	for _, r := range refs {
		if r.AssetID == assetID {
			return s.SetPortrait(ctx, a, c.ID, assetID)
		}
	}
	if _, err := s.addRef(ctx, c, assetID, RefSheet, ""); err != nil {
		return err
	}
	return s.SetPortrait(ctx, a, c.ID, assetID)
}

// TrainLock enqueues character.train with the character's reference images
// (or refIDs when given) and marks the character as training.
func (s *Service) TrainLock(ctx context.Context, a *Access, c *Character, refIDs []string) (*jobs.Job, error) {
	if err := s.require(ctx, a, EditCharacters); err != nil {
		return nil, err
	}
	if c.LockStatus == LockTraining {
		return nil, core.ErrConflict(i18n.T(ctx, "studio.character.already_training"))
	}
	if len(refIDs) == 0 {
		refs, err := s.CharacterRefs(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			refIDs = append(refIDs, r.AssetID)
		}
	}
	if len(refIDs) == 0 {
		return nil, core.ErrValidation(map[string]string{"refs": i18n.T(ctx, "studio.character.need_refs")})
	}
	keys, err := s.AssetKeys(ctx, refIDs...)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for i, id := range refIDs {
		if k, ok := keys[id]; ok {
			files[fmt.Sprintf("ref_%d", i)] = k
		}
	}
	trigger := "mv_" + Slugify(c.Name)
	j, err := s.d.Jobs.Enqueue(ctx, jobs.EnqueueParams{Type: JobCharacterTrain, Scope: jobs.Scope{Kind: jobs.ScopeProject, ProjectID: a.Project.ID},
		UserID: a.Actor.UserID, Input: TrainInput{CharacterID: c.ID, Refs: refIDs, Trigger: trigger, Actor: actorWord(a.Actor)}, Files: files,
		Estimate: jobs.Cost{GPUSeconds: 720}, Batch: true})
	if err != nil {
		return nil, err
	}
	info := c.LockInfo.V
	info.Refs, info.Error = len(refIDs), ""
	if _, err := s.db.Exec(ctx, `UPDATE characters SET lock_status = 'training', lock_job_id = ?, trigger_word = ?, lock_info = ?, updated_at = ? WHERE id = ?`,
		j.ID, trigger, db.MustJSON(info), s.now(), c.ID); err != nil {
		return nil, err
	}
	s.Publish(ctx, a.Project.ID, "studio.character.updated", Event{Kind: "lock", ID: c.ID})
	return j, nil
}

// LockProgress returns the training progress (0–1) of a character in training.
func (s *Service) LockProgress(ctx context.Context, c *Character) float64 {
	if c.LockStatus != LockTraining || c.LockJobID == "" || s.d.Jobs == nil {
		return 0
	}
	j, err := s.d.Jobs.Get(ctx, c.LockJobID)
	if err != nil {
		return 0
	}
	return j.Progress
}

// ---- job completion ---------------------------------------------------------------

// onJobFinished applies the results of studio job types (it runs for every job).
func (s *Service) onJobFinished(ctx context.Context, j *jobs.Job) {
	switch j.Type {
	case JobScriptOutline:
		if j.State != jobs.StateDone || j.Scope.ProjectID == "" {
			return
		}
		var d ScriptDraft
		if err := j.DecodeOutput(&d); err != nil || !d.Valid() {
			s.log.Warn("studio: outline output is not a ScriptDraft", "job", j.ID, "err", err)
			return
		}
		if err := s.ApplyScriptDraft(ctx, j.Scope.ProjectID, d); err != nil {
			s.log.Error("studio: apply script draft", "job", j.ID, "err", err)
		}
	case JobScriptAssist:
		var in AssistInput
		_ = j.DecodeInput(&in)
		id := in.AssistID
		if id == "" {
			_ = s.db.Get(ctx, &id, `SELECT id FROM script_assists WHERE job_id = ?`, j.ID)
		}
		if id == "" {
			return
		}
		switch j.State {
		case jobs.StateDone:
			var d ScriptDiff
			if err := j.DecodeOutput(&d); err != nil {
				_, _ = s.db.Exec(ctx, `UPDATE script_assists SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`, err.Error(), s.now(), id)
				return
			}
			if d.Ops == nil {
				d.Ops = []ScriptOp{}
			}
			_, _ = s.db.Exec(ctx, `UPDATE script_assists SET status = 'ready', diff = ?, updated_at = ? WHERE id = ?`, db.MustJSON(d), s.now(), id)
		case jobs.StateFailed, jobs.StateCanceled:
			msg := j.State
			if j.Error != nil {
				msg = j.Error.Message
			}
			_, _ = s.db.Exec(ctx, `UPDATE script_assists SET status = 'failed', error = ?, updated_at = ? WHERE id = ?`, clip(msg, 500), s.now(), id)
		}
		s.Publish(ctx, j.Scope.ProjectID, "studio.assist.updated", Event{Kind: "assist", ID: id, ChapterID: in.ChapterID})
	case JobCharacterSheet:
		var in SheetInput
		_ = j.DecodeInput(&in)
		s.Publish(ctx, j.Scope.ProjectID, "studio.character.updated", Event{Kind: "sheet", ID: in.CharacterID})
	case JobCharacterTrain:
		s.finishTraining(ctx, j)
	}
}

func (s *Service) finishTraining(ctx context.Context, j *jobs.Job) {
	var in TrainInput
	_ = j.DecodeInput(&in)
	c, err := s.CharacterByID(ctx, in.CharacterID)
	if err != nil || c.LockJobID != j.ID {
		return
	}
	info := c.LockInfo.V
	status, title := LockFailed, "studio.character.lock_failed"
	var score any
	lora := ""
	if j.State == jobs.StateDone {
		var out TrainOutput
		_ = j.DecodeOutput(&out)
		status, title = LockReady, "studio.character.lock_ready"
		score = out.Score
		now := s.now()
		info.TrainedAt, info.Model, info.Method, info.Error = &now, out.Model, out.Method, ""
		if out.LoRA != nil && out.LoRA.Key != "" {
			if as, err := s.AssetByKey(ctx, out.LoRA.Key); err == nil {
				lora = as.ID
			}
		}
	} else if j.Error != nil {
		info.Error = j.Error.Message
	}
	if _, err := s.db.Exec(ctx, `UPDATE characters SET lock_status = ?, lock_score = COALESCE(?, lock_score), lock_info = ?,
		lora_asset_id = COALESCE(?, lora_asset_id), updated_at = ? WHERE id = ?`, status, score, db.MustJSON(info), db.NullString(lora), s.now(), c.ID); err != nil {
		s.log.Error("studio: finish training", "character", c.ID, "err", err)
		return
	}
	s.Publish(ctx, c.ProjectID, "studio.character.updated", Event{Kind: "lock", ID: c.ID})
	if j.UserID != "" && s.d.Notify != nil && j.State != jobs.StateCanceled {
		kind := notify.KindJobDone
		if status == LockFailed {
			kind = notify.KindJobFailed
		}
		if _, err := s.d.Notify.Notify(ctx, notify.Input{UserID: j.UserID, Kind: kind, Title: i18n.T(ctx, title, "name", c.Name),
			Link: "/studio/p/" + c.ProjectID + "/characters/" + c.ID, Data: map[string]any{"character_id": c.ID, "job_id": j.ID}}); err != nil {
			s.log.Warn("studio: notify training", "err", err)
		}
	}
}

// ---- built-in placeholder handlers ---------------------------------------------------

// FallbackHandlers are the placeholder handlers for the studio job types.
func (s *Service) FallbackHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{
		JobScriptOutline:  s.handleOutline,
		JobScriptAssist:   s.handleAssist,
		JobCharacterSheet: s.handleSheet,
		JobCharacterTrain: s.handleTrain,
	}
}

// RegisterFallbackHandlers registers the placeholder handlers for every
// studio job type that no other module registered. The studio calls it at
// start-up, after all modules registered theirs, so the gen module's
// handlers always win.
func (s *Service) RegisterFallbackHandlers(p *jobs.Pool) []string {
	if p == nil {
		return nil
	}
	have := map[string]bool{}
	for _, t := range p.Types() {
		have[t] = true
	}
	var added []string
	for typ, h := range s.FallbackHandlers() {
		if have[typ] {
			continue
		}
		func() {
			defer func() { _ = recover() }() // registered concurrently by someone else: theirs wins
			p.Register(typ, h)
			added = append(added, typ)
		}()
	}
	return added
}

func (s *Service) handleOutline(ctx context.Context, run *jobs.Run) error {
	var in OutlineInput
	if err := run.DecodeInput(&in); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	steps := []string{"reading premise", "outlining arcs", "drafting characters", "scripting chapter 1"}
	for i, st := range steps {
		if err := run.Progress(ctx, float64(i)/float64(len(steps)), i+1, len(steps), st); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	run.SetOutput(PlaceholderScript(in))
	run.Log(ctx, "built-in placeholder text generator (no text model connected)", map[string]any{"provider": "placeholder"})
	return nil
}

func (s *Service) handleAssist(ctx context.Context, run *jobs.Run) error {
	var in AssistInput
	if err := run.DecodeInput(&in); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	var panels []ScriptPanel
	var err error
	if in.SceneID != "" {
		panels, err = s.SceneScript(ctx, in.SceneID)
	} else {
		panels, err = s.ScriptPanels(ctx, in.ChapterID)
	}
	if err != nil {
		return err
	}
	_ = run.Progress(ctx, 0.5, 1, 2, "reading the scene")
	tctx := i18n.WithLocale(ctx, "en")
	if u, err := s.userLocale(ctx, run.Job.UserID); err == nil && u != "" {
		tctx = i18n.WithLocale(ctx, u)
	}
	run.SetOutput(PlaceholderAssist(in.Request, panels, func(key string, args ...any) string { return i18n.T(tctx, key, args...) }))
	return nil
}

func (s *Service) userLocale(ctx context.Context, userID string) (string, error) {
	var l string
	err := s.db.Get(ctx, &l, `SELECT locale FROM users WHERE id = ?`, userID)
	return l, err
}

func (s *Service) handleSheet(ctx context.Context, run *jobs.Run) error {
	var in SheetInput
	if err := run.DecodeInput(&in); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	c, err := s.CharacterByID(ctx, in.CharacterID)
	if err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	n := in.N
	if n <= 0 || n > 4 {
		n = 4
	}
	var out SheetOutput
	for i := 0; i < n; i++ {
		if err := run.Progress(ctx, float64(i)/float64(n), i+1, n, fmt.Sprintf("sheet %d", i+1)); err != nil {
			return err
		}
		seed := int64(i+1)*7919 + int64(len(in.Prompt))
		png := PlaceholderSheet(fmt.Sprintf("sheet %d · front / side / face", i+1), uint64(seed)^hash64(c.ID), 600, 800)
		as, err := s.SaveImage(ctx, c.ProjectID, run.Job.UserID, "character_sheet", "ai", png)
		if err != nil {
			return err
		}
		out.Images = append(out.Images, SheetImage{Key: as.Key, AssetID: as.ID, Seed: seed})
	}
	run.SetOutput(out)
	return nil
}

func (s *Service) handleTrain(ctx context.Context, run *jobs.Run) error {
	var in TrainInput
	if err := run.DecodeInput(&in); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	if _, err := s.CharacterByID(ctx, in.CharacterID); err != nil {
		return jobs.Permanent(jobs.ErrCodeBadInput, err)
	}
	for i := 1; i <= 4; i++ {
		if err := run.Progress(ctx, float64(i)/5, i, 4, "embedding references"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	run.SetOutput(TrainOutput{Score: PlaceholderLockScore(in.CharacterID, len(in.Refs)), Model: "placeholder",
		Method: "reference sheet (placeholder, no LoRA)"})
	return nil
}

func hash64(s string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// jobOutput decodes a job's output (helper for views).
func jobOutput[T any](j *jobs.Job) (T, bool) {
	var v T
	if j == nil || len(j.Output) == 0 {
		return v, false
	}
	return v, json.Unmarshal(j.Output, &v) == nil
}
