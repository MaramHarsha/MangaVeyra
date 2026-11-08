package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Edit tools need the edit scope (projects:write) and the owner's script role.

type dialogueIn struct {
	Speaker     string `json:"speaker,omitempty" jsonschema:"speaker name as written in the script"`
	CharacterID string `json:"character_id,omitempty" jsonschema:"the speaking character's id (from list_characters), empty for captions"`
	Kind        string `json:"kind" jsonschema:"speech, thought or caption"`
	Text        string `json:"text" jsonschema:"the line"`
}

type sfxIn struct {
	Text string `json:"text" jsonschema:"the sound effect lettering"`
	Note string `json:"note,omitempty" jsonschema:"an optional note for the letterer"`
}

type panelPatch struct {
	PanelID  string        `json:"panel_id" jsonschema:"the script panel id from get_script"`
	Camera   *string       `json:"camera,omitempty" jsonschema:"new camera angle"`
	Shot     *string       `json:"shot,omitempty" jsonschema:"new shot size"`
	Emotion  *string       `json:"emotion,omitempty" jsonschema:"new emotion"`
	Action   *string       `json:"action,omitempty" jsonschema:"new action description"`
	Notes    *string       `json:"notes,omitempty" jsonschema:"new notes"`
	Dialogue *[]dialogueIn `json:"dialogue,omitempty" jsonschema:"replaces every dialogue line of the panel"`
	SFX      *[]sfxIn      `json:"sfx,omitempty" jsonschema:"replaces every sound effect of the panel"`
}

type editScriptIn struct {
	ProjectID string       `json:"project_id" jsonschema:"the project id"`
	ChapterID string       `json:"chapter_id" jsonschema:"the chapter id"`
	Revision  string       `json:"revision" jsonschema:"the revision get_script returned; the edit is refused if the chapter changed since"`
	Panels    []panelPatch `json:"panels" jsonschema:"the panels to change; fields you leave out keep their value"`
}

type characterFields struct {
	Name        *string `json:"name,omitempty" jsonschema:"the character's name"`
	Kanji       *string `json:"kanji,omitempty" jsonschema:"name in kanji (optional)"`
	Role        *string `json:"role,omitempty" jsonschema:"story role, e.g. lead or rival"`
	RoleLine    *string `json:"role_line,omitempty" jsonschema:"one line about their role"`
	Age         *string `json:"age,omitempty" jsonschema:"age"`
	Look        *string `json:"look,omitempty" jsonschema:"appearance used for art prompts"`
	Personality *string `json:"personality,omitempty" jsonschema:"personality"`
	SpeechStyle *string `json:"speech_style,omitempty" jsonschema:"how they talk"`
	Category    *string `json:"category,omitempty" jsonschema:"main, supporting or extra"`
}

type createCharacterIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project id"`
	characterFields
}

type editCharacterIn struct {
	ProjectID   string `json:"project_id" jsonschema:"the project id"`
	CharacterID string `json:"character_id" jsonschema:"the character id from list_characters"`
	Revision    string `json:"revision" jsonschema:"the revision list_characters returned; the edit is refused if the character changed since"`
	characterFields
}

func (m *Module) addEditTools(s *mcpsdk.Server) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "edit_script", Annotations: writes("Edit script panels", true),
		Description: "Change script panels of a chapter (action, camera, shot, emotion, notes, dialogue, SFX). Send the revision from get_script; scenes and panels keep their ids. A person can undo the edit."}, m.editScript)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "create_character", Annotations: writes("Create a character", false),
		Description: "Add a character to the project bible."}, m.createCharacter)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "edit_character", Annotations: writes("Edit a character", true),
		Description: "Change a character's bible fields. Send the revision from list_characters. A person can undo the edit."}, m.editCharacter)
}

// Editable script panel fields, compared and restored one by one.
var scriptFields = []string{"camera", "shot", "emotion", "action", "notes", "dialogue", "sfx"}

func panelField(p *studio.ScriptPanel, field string) any {
	switch field {
	case "camera":
		return p.Camera
	case "shot":
		return p.Shot
	case "emotion":
		return p.Emotion
	case "action":
		return p.Action
	case "notes":
		return p.Notes
	case "dialogue":
		if p.Dialogue == nil {
			return []studio.ScriptDialogue{}
		}
		return p.Dialogue
	case "sfx":
		if p.SFX == nil {
			return []studio.ScriptSFX{}
		}
		return p.SFX
	}
	return nil
}

// setPanelField sets field from a JSON value (as stored in undo data).
func setPanelField(p *studio.ScriptPanel, field string, raw json.RawMessage) error {
	var target any
	switch field {
	case "camera":
		target = &p.Camera
	case "shot":
		target = &p.Shot
	case "emotion":
		target = &p.Emotion
	case "action":
		target = &p.Action
	case "notes":
		target = &p.Notes
	case "dialogue":
		target = &p.Dialogue
	case "sfx":
		target = &p.SFX
	default:
		return fmt.Errorf("unknown field %q", field)
	}
	return json.Unmarshal(raw, target)
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// scriptUndo restores the fields an agent changed, if nobody changed them since.
type scriptUndo struct {
	ChapterID string        `json:"chapter_id"`
	Panels    []panelChange `json:"panels"`
}

type panelChange struct {
	ID     string                     `json:"id"`
	Label  string                     `json:"label"`
	Before map[string]json.RawMessage `json:"before"`
	After  map[string]json.RawMessage `json:"after"`
}

func applyPatch(p *studio.ScriptPanel, in panelPatch) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&p.Camera, in.Camera)
	set(&p.Shot, in.Shot)
	set(&p.Emotion, in.Emotion)
	set(&p.Action, in.Action)
	set(&p.Notes, in.Notes)
	if in.Dialogue != nil {
		p.Dialogue = []studio.ScriptDialogue{}
		for _, d := range *in.Dialogue {
			p.Dialogue = append(p.Dialogue, studio.ScriptDialogue{Speaker: d.Speaker, CharacterID: d.CharacterID, Kind: d.Kind, Text: d.Text})
		}
	}
	if in.SFX != nil {
		p.SFX = []studio.ScriptSFX{}
		for _, x := range *in.SFX {
			p.SFX = append(p.SFX, studio.ScriptSFX{Text: x.Text, Note: x.Note})
		}
	}
}

func (m *Module) editScript(ctx context.Context, req *mcpsdk.CallToolRequest, in editScriptIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead, scopeWrite)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActEditScript)
	if err != nil {
		return nil, nil, err
	}
	c, err := m.chapterOf(ctx, p.ID, in.ChapterID)
	if err != nil {
		return nil, nil, err
	}
	revision, err := parseRev(in.Revision)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Panels) == 0 || len(in.Panels) > 200 {
		return nil, nil, errors.New("send between 1 and 200 panel changes")
	}
	if err := m.writeAllowed(a); err != nil {
		return nil, nil, err
	}
	doc, err := m.studio.GetScript(ctx, c.ID)
	if err != nil {
		return nil, nil, toolError(err)
	}
	if !c.UpdatedAt.UTC().Equal(revision) {
		return nil, nil, toolError(studio.ErrConflict)
	}
	byID := map[string]*studio.ScriptPanel{}
	for si := range doc.Scenes {
		for pi := range doc.Scenes[si].Panels {
			sp := &doc.Scenes[si].Panels[pi]
			byID[sp.ID] = sp
		}
	}
	seen := map[string]bool{}
	var changes []panelChange
	onlyDialogue, onlyCaptions := true, true
	for _, patch := range in.Panels {
		id := strings.TrimSpace(patch.PanelID)
		sp := byID[id]
		if sp == nil {
			return nil, nil, fmt.Errorf("panel %q is not in this chapter's script", id)
		}
		if seen[id] {
			return nil, nil, fmt.Errorf("panel %q is listed twice", id)
		}
		seen[id] = true
		before := *sp
		applyPatch(sp, patch)
		ch := panelChange{ID: id, Label: sp.Label, Before: map[string]json.RawMessage{}, After: map[string]json.RawMessage{}}
		for _, f := range scriptFields {
			b, af := panelField(&before, f), panelField(sp, f)
			if sameJSON(b, af) {
				continue
			}
			ch.Before[f], _ = json.Marshal(b)
			ch.After[f], _ = json.Marshal(af)
			if f != "dialogue" {
				onlyDialogue, onlyCaptions = false, false
			} else {
				for _, d := range sp.Dialogue {
					if d.Kind != "caption" {
						onlyCaptions = false
					}
				}
			}
		}
		if len(ch.After) > 0 {
			changes = append(changes, ch)
		}
	}
	if len(changes) == 0 {
		return nil, map[string]any{"changed": []string{}, "revision": rev(c.UpdatedAt)}, nil
	}
	edited, err := m.studio.EditScript(ctx, studio.EditScriptInput{Actor: a.actor, ProjectID: p.ID, ChapterID: c.ID, Doc: doc, ExpectedUpdatedAt: revision})
	if err != nil {
		return nil, nil, toolError(err)
	}
	// Undo compares against what was stored (the service normalises text), so
	// read the saved panels back for the after values.
	if saved, err := m.studio.GetScript(ctx, c.ID); err == nil {
		stored := map[string]*studio.ScriptPanel{}
		for si := range saved.Scenes {
			for pi := range saved.Scenes[si].Panels {
				stored[saved.Scenes[si].Panels[pi].ID] = &saved.Scenes[si].Panels[pi]
			}
		}
		for i := range changes {
			if sp := stored[changes[i].ID]; sp != nil {
				for f := range changes[i].After {
					changes[i].After[f], _ = json.Marshal(panelField(sp, f))
				}
			}
		}
	}
	labels := make([]string, 0, len(changes))
	for _, ch := range changes {
		labels = append(labels, ch.Label)
	}
	detail := actionDetail{ChapterNumber: c.Number, Labels: labels, Dialogue: onlyDialogue && !onlyCaptions, Caption: onlyCaptions}
	newRev, row, err := m.recordScriptEdit(ctx, a, p.ID, edited, newAction{UserID: a.user.ID, TokenID: a.tok.ID, TokenName: a.tok.Name,
		ProjectID: p.ID, Kind: kindScriptEdit, Action: "edit_script", TargetKind: "chapter", TargetID: c.ID, Status: statusDone,
		Detail: detail, Undo: scriptUndo{ChapterID: c.ID, Panels: changes}})
	if err != nil {
		m.d.Log.ErrorContext(ctx, "mcp: record agent script edit", "chapter", c.ID, "err", err)
		return nil, nil, toolError(err)
	}
	m.audit(ctx, a, "agent.script.edit", "chapter", c.ID, p.ID, map[string]any{"action_id": row.ID, "panels": labels})
	out := map[string]any{"changed": labels, "action_id": row.ID}
	if newRev.IsZero() {
		out["note"] = "the chapter changed again meanwhile; call get_script before the next edit"
	} else {
		out["revision"] = rev(newRev)
	}
	return nil, out, nil
}

// recordScriptEdit records the labelled agent action and returns the chapter
// revision after the edit. Studio already labelled the script AI-assisted in
// the edit's own transaction (studio.Actor.Agent).
func (m *Module) recordScriptEdit(ctx context.Context, a *agent, projectID string, edited studio.Chapter, in newAction) (time.Time, actionRow, error) {
	row, err := m.insertAction(ctx, m.d.DB, in)
	return edited.UpdatedAt, row, err
}

func (f characterFields) apply(c *studio.NewCharacter) {
	for _, x := range []struct {
		dst *string
		v   *string
	}{{&c.Name, f.Name}, {&c.Kanji, f.Kanji}, {&c.Role, f.Role}, {&c.RoleLine, f.RoleLine}, {&c.Age, f.Age},
		{&c.Look, f.Look}, {&c.Personality, f.Personality}, {&c.SpeechStyle, f.SpeechStyle}, {&c.Category, f.Category}} {
		if x.v != nil {
			*x.dst = *x.v
		}
	}
}

func newCharacterOf(c studio.Character) studio.NewCharacter {
	return studio.NewCharacter{ProjectID: c.ProjectID, Name: c.Name, Kanji: c.Kanji, Role: c.Role, RoleLine: c.RoleLine,
		Age: c.Age, Look: c.Look, Personality: c.Personality, SpeechStyle: c.SpeechStyle, Category: c.Category}
}

func (m *Module) createCharacter(ctx context.Context, req *mcpsdk.CallToolRequest, in createCharacterIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead, scopeWrite)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActEditScript)
	if err != nil {
		return nil, nil, err
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, nil, errors.New("a character needs a name")
	}
	if err := m.writeAllowed(a); err != nil {
		return nil, nil, err
	}
	nc := studio.NewCharacter{ProjectID: p.ID}
	in.apply(&nc)
	c, err := m.studio.CreateCharacter(ctx, a.actor, nc)
	if err != nil {
		return nil, nil, toolError(err)
	}
	row, err := m.insertAction(ctx, m.d.DB, newAction{UserID: a.user.ID, TokenID: a.tok.ID, TokenName: a.tok.Name, ProjectID: p.ID,
		Kind: kindCharacterCreate, Action: "create_character", TargetKind: "character", TargetID: c.ID, Status: statusDone,
		Detail: actionDetail{Name: c.Name}})
	if err != nil {
		m.d.Log.ErrorContext(ctx, "mcp: record agent character", "character", c.ID, "err", err)
		return nil, nil, toolError(err)
	}
	m.audit(ctx, a, "agent.character.create", "character", c.ID, p.ID, map[string]any{"action_id": row.ID})
	return nil, map[string]any{"character": characterOut(c), "action_id": row.ID}, nil
}

// characterUndo restores the bible fields an agent changed.
type characterUndo struct {
	CharacterID string            `json:"character_id"`
	Fields      []string          `json:"fields"`
	Before      map[string]string `json:"before"`
	After       map[string]string `json:"after"`
}

func characterValues(c studio.NewCharacter) map[string]string {
	return map[string]string{"name": c.Name, "kanji": c.Kanji, "role": c.Role, "role_line": c.RoleLine, "age": c.Age,
		"look": c.Look, "personality": c.Personality, "speech_style": c.SpeechStyle, "category": c.Category}
}

func setCharacterValue(c *studio.NewCharacter, field, v string) {
	switch field {
	case "name":
		c.Name = v
	case "kanji":
		c.Kanji = v
	case "role":
		c.Role = v
	case "role_line":
		c.RoleLine = v
	case "age":
		c.Age = v
	case "look":
		c.Look = v
	case "personality":
		c.Personality = v
	case "speech_style":
		c.SpeechStyle = v
	case "category":
		c.Category = v
	}
}

func (m *Module) editCharacter(ctx context.Context, req *mcpsdk.CallToolRequest, in editCharacterIn) (*mcpsdk.CallToolResult, any, error) {
	a, err := m.agentFor(ctx, req, scopeRead, scopeWrite)
	if err != nil {
		return nil, nil, err
	}
	p, err := m.project(ctx, a, in.ProjectID, studio.ActEditScript)
	if err != nil {
		return nil, nil, err
	}
	revision, err := parseRev(in.Revision)
	if err != nil {
		return nil, nil, err
	}
	if !db.ValidID(strings.TrimSpace(in.CharacterID)) {
		return nil, nil, toolError(studio.ErrNotFound)
	}
	current, err := m.studio.GetCharacter(ctx, strings.TrimSpace(in.CharacterID))
	if err != nil || current.ProjectID != p.ID {
		return nil, nil, toolError(studio.ErrNotFound)
	}
	if err := m.writeAllowed(a); err != nil {
		return nil, nil, err
	}
	next := newCharacterOf(current)
	in.apply(&next)
	if reflect.DeepEqual(next, newCharacterOf(current)) {
		return nil, map[string]any{"character": characterOut(current), "changed": []string{}}, nil
	}
	updated, err := m.bible.UpdateCharacter(ctx, a.actor, studio.UpdateCharacterInput{ProjectID: p.ID, CharacterID: current.ID,
		Character: next, ExpectedUpdatedAt: revision})
	if err != nil {
		return nil, nil, toolError(err)
	}
	before, after := characterValues(newCharacterOf(current)), characterValues(newCharacterOf(updated))
	undo := characterUndo{CharacterID: current.ID, Before: map[string]string{}, After: map[string]string{}}
	for _, f := range []string{"name", "kanji", "role", "role_line", "age", "look", "personality", "speech_style", "category"} {
		if before[f] != after[f] {
			undo.Fields = append(undo.Fields, f)
			undo.Before[f], undo.After[f] = before[f], after[f]
		}
	}
	row, err := m.insertAction(ctx, m.d.DB, newAction{UserID: a.user.ID, TokenID: a.tok.ID, TokenName: a.tok.Name, ProjectID: p.ID,
		Kind: kindCharacterEdit, Action: "edit_character", TargetKind: "character", TargetID: current.ID, Status: statusDone,
		Detail: actionDetail{Name: updated.Name}, Undo: undo})
	if err != nil {
		m.d.Log.ErrorContext(ctx, "mcp: record agent character edit", "character", current.ID, "err", err)
		return nil, nil, toolError(err)
	}
	m.audit(ctx, a, "agent.character.edit", "character", current.ID, p.ID, map[string]any{"action_id": row.ID, "fields": undo.Fields})
	return nil, map[string]any{"character": characterOut(updated), "changed": undo.Fields, "action_id": row.ID}, nil
}
