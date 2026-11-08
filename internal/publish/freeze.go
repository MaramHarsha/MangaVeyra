package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Freezing turns a chapter's live page documents into what readers see: one
// image per page rendered through compose and lettering in the publication
// language, plus guided-view data (panel geometry, bubbles and alt-text in
// reading order). Later Studio edits never touch the frozen copy.

// maxFreezePages bounds one publication (the page store returns at most 256).
const maxFreezePages = 256

// frozenPanel is a GuidedPanel as stored in publication_pages.panels. The
// extra panel_id (ignored by readers) lets E1 prefill alt-text edits when a
// chapter is updated.
type frozenPanel struct {
	GuidedPanel
	PanelID string `json:"panel_id,omitempty"`
}

// chapterSource is everything a freeze reads about one chapter.
type chapterSource struct {
	project studio.Project
	chapter studio.Chapter
	pages   []pagedoc.Page
	script  scriptIndex
	names   map[string]string // character ID → name
	rev     revision
}

// revision identifies the chapter state a freeze rendered, so the commit can
// refuse a copy of pages that changed meanwhile (as export does).
type revision struct {
	ChapterUpdatedAt time.Time `json:"chapter_updated_at"`
	Pages            []pageRev `json:"pages"`
}

type pageRev struct {
	ID      string `db:"id" json:"id"`
	Version int    `db:"version" json:"version"`
	Number  int    `db:"number" json:"number"`
}

func readRevision(ctx context.Context, q db.Querier, projectID, chapterID string) (revision, error) {
	var ch struct {
		ProjectID string    `db:"project_id"`
		UpdatedAt time.Time `db:"updated_at"`
	}
	if err := q.Get(ctx, &ch, `SELECT project_id, updated_at FROM chapters WHERE id = ?`+forUpdate(q), chapterID); err != nil {
		if db.IsNotFound(err) {
			return revision{}, ErrNotFound
		}
		return revision{}, fmt.Errorf("publish: chapter revision: %w", err)
	}
	if ch.ProjectID != projectID {
		return revision{}, ErrNotFound
	}
	rev := revision{ChapterUpdatedAt: ch.UpdatedAt.UTC(), Pages: []pageRev{}}
	if err := q.Select(ctx, &rev.Pages, `SELECT id, version, number FROM pages WHERE chapter_id = ? ORDER BY number, id`+forUpdate(q), chapterID); err != nil {
		return revision{}, fmt.Errorf("publish: page revisions: %w", err)
	}
	return rev, nil
}

func (r revision) equal(o revision) bool {
	return r.ChapterUpdatedAt.Equal(o.ChapterUpdatedAt) && slices.Equal(r.Pages, o.Pages)
}

// token is an opaque digest of the revision. E1 renders it as a hidden field
// so a publish is bound to the chapter state the creator reviewed.
func (r revision) token() string {
	b, _ := json.Marshal(revision{ChapterUpdatedAt: r.ChapterUpdatedAt.UTC(), Pages: r.Pages})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// loadChapter reads the project, chapter, page documents, script and cast
// as one consistent revision (ErrConflict when pages change while loading).
func (s *service) loadChapter(ctx context.Context, projectID, chapterID string) (chapterSource, error) {
	var src chapterSource
	var err error
	if src.project, err = s.studio.GetProject(ctx, projectID); err != nil {
		return src, ErrNotFound
	}
	if src.chapter, err = s.studio.GetChapter(ctx, chapterID); err != nil || src.chapter.ProjectID != projectID {
		return src, ErrNotFound
	}
	if src.rev, err = readRevision(ctx, s.db, projectID, chapterID); err != nil {
		return src, err
	}
	if len(src.rev.Pages) > maxFreezePages {
		return src, fmt.Errorf("%w: too many pages", ErrInvalid)
	}
	if src.pages, err = s.pages.ChapterPages(ctx, chapterID); err != nil {
		return src, fmt.Errorf("publish: chapter pages: %w", err)
	}
	sort.SliceStable(src.pages, func(i, j int) bool {
		if src.pages[i].Number != src.pages[j].Number {
			return src.pages[i].Number < src.pages[j].Number
		}
		return src.pages[i].ID < src.pages[j].ID
	})
	got := make([]pageRev, len(src.pages))
	for i, p := range src.pages {
		if p.ProjectID != projectID || p.ChapterID != chapterID {
			return src, ErrNotFound
		}
		got[i] = pageRev{ID: p.ID, Version: p.Version, Number: p.Number}
	}
	if !slices.Equal(got, src.rev.Pages) {
		return src, fmt.Errorf("%w: pages changed while loading", ErrConflict)
	}
	doc, err := s.studio.GetScript(ctx, chapterID)
	if err != nil {
		doc = studio.ScriptDoc{}
	}
	src.script = newScriptIndex(doc)
	src.names = map[string]string{}
	if cast, err := s.studio.ListCharacters(ctx, projectID); err == nil {
		for _, c := range cast {
			src.names[c.ID] = c.Name
		}
	}
	return src, nil
}

// sameLang reports whether a stored text key is the publication language.
func sameLang(key, lang string) bool {
	if key == lang {
		return true
	}
	a, b := pagedoc.NormalizeContentLanguage(key), pagedoc.NormalizeContentLanguage(lang)
	return a != "" && a == b
}

// bubbleText returns a bubble's text in lang. missing is true when the bubble
// is lettered in another language but not in this one (rendering would fall
// back silently, so publishing refuses).
func bubbleText(b pagedoc.Bubble, lang string) (text string, missing bool) {
	other := false
	for key, t := range b.Texts {
		v := strings.TrimSpace(t.Text)
		if v == "" {
			continue
		}
		if sameLang(key, lang) {
			return v, false
		}
		other = true
	}
	return "", other
}

// missingBubbles counts bubbles that have no text in lang.
func missingBubbles(pages []pagedoc.Page, lang string) int {
	n := 0
	for _, p := range pages {
		for _, b := range p.Bubbles {
			if _, missing := bubbleText(b, lang); missing {
				n++
			}
		}
	}
	return n
}

// chapterLanguages lists the project language and every language lettered
// somewhere in the chapter, in content-language order.
func chapterLanguages(projectLang string, pages []pagedoc.Page) []string {
	set := map[string]bool{projectLang: true}
	for _, p := range pages {
		for _, b := range p.Bubbles {
			for key, t := range b.Texts {
				if c := pagedoc.NormalizeContentLanguage(key); c != "" && strings.TrimSpace(t.Text) != "" {
					set[c] = true
				}
			}
		}
	}
	var out []string
	for _, l := range pagedoc.ContentLanguages {
		if set[l] {
			out = append(out, l)
			delete(set, l)
		}
	}
	rest := make([]string, 0, len(set))
	for l := range set {
		rest = append(rest, l)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// letteringVisible mirrors compose: bubbles of a panel whose lettering layer is
// hidden (or fully transparent) are not drawn, so they are not read either.
func letteringVisible(p pagedoc.Page, panelID string) bool {
	if pn := p.Panel(panelID); pn != nil {
		for _, l := range pn.Layers {
			if l.Kind == pagedoc.LayerLettering {
				return l.Visible && l.Opacity > 0
			}
		}
	}
	return true
}

var bubbleKinds = []string{pagedoc.BubbleSpeech, pagedoc.BubbleThought, pagedoc.BubbleShout, pagedoc.BubbleWhisper,
	pagedoc.BubbleCaption, pagedoc.BubbleSFX}

// guidedPage builds a page's guided-view data in lang: panels in reading
// order with their geometry, their bubbles in lettering reading order and
// their alt-text (the creator's edit, else the panel's stored alt-text, else a
// deterministic draft). It also returns the page-level alt-text.
func (s *service) guidedPage(ctx context.Context, src chapterSource, p pagedoc.Page, lang string, edits map[string]string) ([]frozenPanel, string) {
	panels := slices.Clone(p.Panels)
	sort.SliceStable(panels, func(i, j int) bool { return panels[i].Order < panels[j].Order })
	bubbles := map[string][]GuidedBubble{}
	if len(panels) > 0 {
		for _, id := range s.lettering.ReadingOrder(p) {
			b := p.Bubble(id)
			if b == nil {
				continue
			}
			text, _ := bubbleText(*b, lang)
			if text == "" {
				continue
			}
			owner := panelFor(p, panels, *b)
			if !letteringVisible(p, owner) {
				continue
			}
			kind := b.Kind
			if !slices.Contains(bubbleKinds, kind) {
				kind = pagedoc.BubbleSpeech
			}
			speaker := ""
			if kind != pagedoc.BubbleCaption && kind != pagedoc.BubbleSFX {
				speaker = src.names[b.SpeakerCharacterID]
			}
			bubbles[owner] = append(bubbles[owner], GuidedBubble{Order: len(bubbles[owner]) + 1, Kind: kind, Speaker: speaker, Text: text})
		}
	}
	out := make([]frozenPanel, 0, len(panels))
	alts := make([]string, 0, len(panels))
	for i, pn := range panels {
		r := pagedoc.Bounds(pn.Points)
		fp := frozenPanel{PanelID: pn.ID, GuidedPanel: GuidedPanel{Order: i + 1, Points: slices.Clone(pn.Points),
			X: r.X, Y: r.Y, W: r.W, H: r.H, Bubbles: bubbles[pn.ID]}}
		if fp.Points == nil {
			fp.Points = []pagedoc.Point{}
		}
		if fp.Bubbles == nil {
			fp.Bubbles = []GuidedBubble{}
		}
		fp.AltText = s.panelAlt(ctx, src, p, pn, lang, fp.Bubbles, edits)
		if fp.AltText != "" {
			alts = append(alts, fp.AltText)
		}
		out = append(out, fp)
	}
	return out, clip(strings.Join(alts, " "), 4*maxAltText)
}

// panelAlt picks the creator's edit, the panel's stored alt-text, or a draft.
func (s *service) panelAlt(ctx context.Context, src chapterSource, p pagedoc.Page, pn pagedoc.Panel, lang string, bubbles []GuidedBubble, edits map[string]string) string {
	if v, ok := edits[pn.ID]; ok {
		if v = clip(strings.Join(strings.Fields(v), " "), maxAltText); v != "" {
			return v
		}
	}
	if v := clip(strings.TrimSpace(pn.AltText), maxAltText); v != "" {
		return v
	}
	return draftAltText(ctx, lang, altSourceFor(src, pn, bubbles))
}

func altSourceFor(src chapterSource, pn pagedoc.Panel, bubbles []GuidedBubble) altSource {
	as := altSource{Bubbles: bubbles}
	var ids []string
	if sp, ok := src.script[pn.ScriptPanelID]; ok {
		as.Action = sp.panel.Action
		as.Setting = sp.scene.Title
		if as.Setting == "" {
			as.Setting = sp.scene.Summary
		}
		as.TimeOfDay = sp.scene.TimeOfDay
		ids = append(ids, sp.panel.CharacterIDs...)
	}
	for _, l := range pn.Layers {
		if l.Kind == pagedoc.LayerCharacter && l.CharacterID != "" {
			ids = append(ids, l.CharacterID)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if name := strings.TrimSpace(src.names[id]); name != "" && !seen[name] {
			seen[name] = true
			as.Characters = append(as.Characters, name)
		}
	}
	return as
}

// panelFor returns the panel a bubble belongs to: its own panel, the panel
// containing its centre, or the first panel in reading order.
func panelFor(p pagedoc.Page, panels []pagedoc.Panel, b pagedoc.Bubble) string {
	if b.PanelID != "" && p.Panel(b.PanelID) != nil {
		return b.PanelID
	}
	c := pagedoc.Point{X: b.Box.X + b.Box.W/2, Y: b.Box.Y + b.Box.H/2}
	for _, pn := range panels {
		if pagedoc.Contains(pn.Points, c) {
			return pn.ID
		}
	}
	return panels[0].ID
}
