package authui

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/i18n"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
	"github.com/MaramHarsha/MangaVeyra/internal/web/layout"
)

// Onboarding is what A5 stores in users.onboarding (JSON).
type Onboarding struct {
	CreatorType string     `json:"creator_type,omitempty"`
	Genres      []string   `json:"genres,omitempty"`
	ArtStyle    string     `json:"art_style,omitempty"`
	Step        int        `json:"step,omitempty"`
	Skipped     bool       `json:"skipped,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Done reports whether onboarding was finished or skipped.
func (o Onboarding) Done() bool { return o.CompletedAt != nil || o.Skipped }

// Creator types (step 1).
var CreatorTypes = []struct{ Value, Icon string }{
	{"writer", "pencil"}, {"artist", "brush"}, {"reader", "book"}, {"team", "users"},
}

// Genres (step 2). At least MinGenres should be picked.
var Genres = []string{"action", "fantasy", "mystery", "romance", "horror", "scifi", "slice_of_life", "comedy",
	"sports", "historical", "isekai", "thriller", "drama", "mecha", "supernatural"}

// MinGenres is the number of genres step 2 asks for.
const MinGenres = 3

// ArtStyles (step 3): default art style for new projects.
var ArtStyles = []struct{ Value, Icon string }{
	{"seinen_bw", "contrast-2"}, {"shonen_ink", "bolt"}, {"shojo_soft", "flower"},
	{"webtoon_color", "palette"}, {"chibi", "mood-smile"}, {"sketch", "pencil"},
}

// shelfSeries is original sample content for the Explore shelf preview.
type shelfSeries struct {
	Title  string
	Genres []string
}

var shelf = []shelfSeries{
	{"Paper Crows", []string{"mystery", "supernatural", "drama"}},
	{"Lanterns of Oru", []string{"fantasy", "drama", "historical"}},
	{"Glass Orchard", []string{"romance", "drama", "slice_of_life"}},
	{"Ninth Bell", []string{"horror", "mystery", "thriller"}},
	{"Salt & Static", []string{"scifi", "action", "mecha"}},
	{"Iron Koi", []string{"action", "sports", "comedy"}},
	{"Low Tide Radio", []string{"slice_of_life", "comedy", "romance"}},
	{"Harbour Ghosts", []string{"supernatural", "thriller", "horror"}},
	{"Moth & Mortar", []string{"historical", "fantasy", "mystery"}},
	{"Tidebreak Courier", []string{"fantasy", "action", "mystery"}},
	{"Second Sun Gate", []string{"isekai", "fantasy", "action"}},
	{"Rust Choir", []string{"mecha", "scifi", "drama"}},
}

// ShelfItem is one preview cover with its match count.
type ShelfItem struct {
	Title   string
	Matches int
}

// ShelfFor ranks the sample series by how many picked genres they match (top 6).
func ShelfFor(picks []string) []ShelfItem {
	out := make([]ShelfItem, 0, len(shelf))
	for _, s := range shelf {
		n := 0
		for _, g := range s.Genres {
			if slices.Contains(picks, g) {
				n++
			}
		}
		out = append(out, ShelfItem{Title: s.Title, Matches: n})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Matches > out[j].Matches })
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

func loadOnboarding(ctx context.Context, d *core.Deps, userID string) Onboarding {
	var raw string
	var o Onboarding
	if err := d.DB.Get(ctx, &raw, `SELECT onboarding FROM users WHERE id = ?`, userID); err == nil {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	return o
}

func saveOnboarding(ctx context.Context, d *core.Deps, userID string, o Onboarding) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = d.DB.Exec(ctx, `UPDATE users SET onboarding = ?, updated_at = ? WHERE id = ?`, string(b), db.Now(), userID)
	return err
}

func onboardingDone(ctx context.Context, d *core.Deps, userID string) bool {
	return loadOnboarding(ctx, d, userID).Done()
}

type onboardingView struct {
	Step   int
	Data   Onboarding
	Shelf  []ShelfItem
	Errors web.FormErrors
}

func stepOf(r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("step"))
	return n, err == nil && n >= 1 && n <= 3
}

func (m *Module) renderOnboarding(w http.ResponseWriter, r *http.Request, status int, v onboardingView) error {
	c := m.ui.Chrome(w, r, web.PageOpts{Title: i18n.T(r.Context(), "onboarding.title"), Transition: "turn"})
	return web.Render(w, r, status, web.Wrap(layout.OnboardingShell(c, SkipLink()), OnboardingPage(v)), nil)
}

func (m *Module) onboardingPage(w http.ResponseWriter, r *http.Request) error {
	step, ok := stepOf(r)
	if !ok {
		return core.ErrNotFound()
	}
	o := loadOnboarding(r.Context(), m.d, core.UserFrom(r.Context()).ID)
	return m.renderOnboarding(w, r, http.StatusOK, onboardingView{Step: step, Data: o, Shelf: ShelfFor(o.Genres), Errors: web.FormErrors{}})
}

func (m *Module) onboardingSave(w http.ResponseWriter, r *http.Request) error {
	step, ok := stepOf(r)
	if !ok {
		return core.ErrNotFound()
	}
	if err := web.ParseForm(w, r); err != nil {
		return core.ErrBadRequest("invalid form")
	}
	ctx := r.Context()
	u := core.UserFrom(ctx)
	o := loadOnboarding(ctx, m.d, u.ID)
	v := onboardingView{Step: step, Errors: web.FormErrors{}}
	switch step {
	case 1:
		if t := web.FormValue(r, "creator_type"); validCreator(t) {
			o.CreatorType = t
		}
	case 2:
		var picks []string
		for _, g := range r.PostForm["genres"] {
			if slices.Contains(Genres, g) && !slices.Contains(picks, g) {
				picks = append(picks, g)
			}
		}
		o.Genres = picks
		if len(picks) < MinGenres {
			v.Data, v.Shelf = o, ShelfFor(picks)
			v.Errors.Add("genres", i18n.T(ctx, "onboarding.genres.min", "count", MinGenres))
			return m.renderOnboarding(w, r, http.StatusUnprocessableEntity, v)
		}
	case 3:
		if s := web.FormValue(r, "art_style"); validStyle(s) {
			o.ArtStyle = s
		}
	}
	if step < 3 {
		o.Step = step + 1
		if err := saveOnboarding(ctx, m.d, u.ID, o); err != nil {
			return err
		}
		core.Redirect(w, r, "/onboarding/"+strconv.Itoa(step+1))
		return nil
	}
	now := time.Now().UTC()
	o.CompletedAt, o.Step = &now, 3
	if err := saveOnboarding(ctx, m.d, u.ID, o); err != nil {
		return err
	}
	web.Flash(w, r, "success", i18n.T(ctx, "onboarding.done"), "")
	core.Redirect(w, r, "/studio")
	return nil
}

func (m *Module) onboardingSkip(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := core.UserFrom(ctx)
	o := loadOnboarding(ctx, m.d, u.ID)
	o.Skipped = true
	if err := saveOnboarding(ctx, m.d, u.ID, o); err != nil {
		return err
	}
	core.Redirect(w, r, "/studio")
	return nil
}

// onboardingPreview re-renders the Explore shelf for the ticked genres (HTMX).
func (m *Module) onboardingPreview(w http.ResponseWriter, r *http.Request) error {
	var picks []string
	for _, g := range r.URL.Query()["genres"] {
		if slices.Contains(Genres, g) {
			picks = append(picks, g)
		}
	}
	return web.Fragment(w, r, http.StatusOK, templ.Join(ShelfPreview(ShelfFor(picks), len(picks)), GenreCount(len(picks), true)))
}

func validCreator(v string) bool {
	for _, c := range CreatorTypes {
		if c.Value == v {
			return true
		}
	}
	return false
}

func validStyle(v string) bool {
	for _, s := range ArtStyles {
		if s.Value == v {
			return true
		}
	}
	return false
}
