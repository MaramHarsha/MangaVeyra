package studio

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// Service is the studio's exported service layer: typed repositories for
// projects, members, story structure, script, pages, panels, layers, bubbles,
// characters, setting items and templates, the page document loader/saver,
// versions and branches, permissions and lock checks, layout application and
// script → pages. Other modules get it with core.MustUse[*studio.Service](d).
//
// Every write that other people may be looking at publishes an event on the
// WebSocket topic project:{id} (see Event).
type Service struct {
	d   *core.Deps
	db  *db.DB
	log *slog.Logger
	// Now is the clock (tests replace it).
	Now func() time.Time
}

// NewService creates the service. The studio module creates and provides the
// one instance; other code uses core.MustUse[*studio.Service](d).
func NewService(d *core.Deps) *Service {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{d: d, db: d.DB, log: log, Now: db.Now}
}

// DB returns the database handle (for modules that join studio tables).
func (s *Service) DB() *db.DB { return s.db }

func (s *Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

// Event is published on the WebSocket topic project:{id} after a change.
// Type is "studio.<entity>.<action>" (e.g. "studio.script.updated").
type Event struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	ChapterID string `json:"chapter_id,omitempty"`
	PageID    string `json:"page_id,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// Publish sends an event on project:{projectID}.
func (s *Service) Publish(ctx context.Context, projectID, typ string, ev Event) {
	if s.d.Hub == nil || projectID == "" {
		return
	}
	if ev.ActorID == "" {
		if u := httpx.UserFrom(ctx); u != nil {
			ev.ActorID = u.ID
		}
	}
	s.d.Hub.Publish("project:"+projectID, typ, ev)
}

// touchProject bumps projects.updated_at (the projects list sorts by it).
func (s *Service) touchProject(ctx context.Context, q db.Querier, projectID string) {
	_, _ = q.Exec(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, s.now(), projectID)
}

// Actor is who changes studio data: a signed-in user, an agent acting for a
// user through an MCP token, or the system (job handlers without a user).
type Actor struct {
	Kind    string `json:"kind"`
	UserID  string `json:"user_id,omitempty"`
	TokenID string `json:"token_id,omitempty"`
	JobID   string `json:"job_id,omitempty"`
	// Token is the bearer token (agents) with its project restrictions.
	Token *httpx.Token `json:"-"`
	// SiteRole is the user's site role (admin, creator, reader).
	SiteRole string `json:"-"`
}

// IsAgent reports whether the actor is an AI agent (MCP token).
func (a Actor) IsAgent() bool { return a.Kind == ActorAgent }

// ActorFrom returns the actor of a request context.
func ActorFrom(ctx context.Context) Actor {
	id := httpx.IdentityFrom(ctx)
	a := Actor{Kind: ActorSystem}
	if id.User != nil {
		a.Kind, a.UserID, a.SiteRole = ActorUser, id.User.ID, id.User.Role
	}
	if id.Token != nil {
		a.TokenID, a.Token = id.Token.ID, id.Token
		if id.Token.Kind == httpx.TokenAgent {
			a.Kind = ActorAgent
		}
	}
	return a
}

// UserActor returns an actor for a user acting in person.
func UserActor(userID string) Actor { return Actor{Kind: ActorUser, UserID: userID} }

// SystemActor returns the actor for background work of a job.
func SystemActor(jobID, userID string) Actor {
	return Actor{Kind: ActorSystem, UserID: userID, JobID: jobID}
}

// ActorForJob returns the actor a job handler acts as: the agent when the
// job was enqueued by an agent token, else the system on behalf of the user.
func ActorForJob(j *jobs.Job) Actor {
	a := SystemActor(j.ID, j.UserID)
	var d struct {
		Actor string `json:"actor"`
	}
	if len(j.Input) > 0 && j.DecodeInput(&d) == nil && d.Actor == ActorAgent {
		a.Kind = ActorAgent
	}
	return a
}

// Slugify turns a title into a URL slug ("Tidebreak Courier" → "tidebreak-courier").
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > 60 {
		out = strings.Trim(string(r[:60]), "-")
	}
	if out == "" {
		out = "untitled"
	}
	return out
}

// MediaURL returns a signed, short-lived URL for a storage key ("" for "").
func (s *Service) MediaURL(key string) string {
	if key == "" || s.d.Media == nil {
		return ""
	}
	return s.d.Media.URL(key, 6*time.Hour)
}

// AssetKeys resolves asset ids to storage keys (missing ids are left out).
func (s *Service) AssetKeys(ctx context.Context, ids ...string) (map[string]string, error) {
	out := map[string]string{}
	var want []string
	for _, id := range ids {
		if id != "" {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return out, nil
	}
	q, args, err := db.In(`SELECT id, key FROM assets WHERE id IN (?)`, want)
	if err != nil {
		return nil, err
	}
	rows := []struct {
		ID  string `db:"id"`
		Key string `db:"key"`
	}{}
	if err := s.db.Select(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.Key
	}
	return out, nil
}

// AssetURLs resolves asset ids to signed media URLs.
func (s *Service) AssetURLs(ctx context.Context, ids ...string) map[string]string {
	keys, err := s.AssetKeys(ctx, ids...)
	out := map[string]string{}
	if err != nil {
		s.log.Warn("studio: resolve assets", "err", err)
		return out
	}
	for id, k := range keys {
		out[id] = s.MediaURL(k)
	}
	return out
}

// Asset is a stored file row.
type Asset struct {
	ID          string `db:"id" json:"id"`
	Key         string `db:"key" json:"key"`
	ContentType string `db:"content_type" json:"content_type"`
	Bytes       int64  `db:"bytes" json:"bytes"`
	Width       int    `db:"width" json:"width"`
	Height      int    `db:"height" json:"height"`
}

// SaveImage stores an uploaded or generated image and records it in assets.
// The content type is sniffed; only raster images within the decode limits are accepted.
func (s *Service) SaveImage(ctx context.Context, projectID, ownerID, kind, source string, data []byte) (*Asset, error) {
	ct := storage.DetectContentType(data)
	if err := storage.CheckType(ct, storage.ImageTypes...); err != nil {
		return nil, core.NewError(415, "unsupported_type", "")
	}
	cfg, _, err := storage.ImageConfig(data, storage.DefaultImageLimits)
	if err != nil {
		return nil, core.NewError(422, "bad_image", err.Error())
	}
	prefix := "projects/" + projectID + "/" + kind
	if projectID == "" {
		prefix = "studio/" + kind
	}
	key := storage.NewKey(prefix, ct)
	if err := storage.PutBytes(ctx, s.d.Store, key, data, ct); err != nil {
		return nil, err
	}
	if source == "" {
		source = "upload"
	}
	a := &Asset{ID: db.NewID(), Key: key, ContentType: ct, Bytes: int64(len(data)), Width: cfg.Width, Height: cfg.Height}
	if _, err := s.db.Exec(ctx, `INSERT INTO assets (id, key, content_type, bytes, width, height, kind, source, owner_id, project_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, a.Key, a.ContentType, a.Bytes, a.Width, a.Height, kind, source,
		db.NullString(ownerID), db.NullString(projectID), s.now()); err != nil {
		_ = s.d.Store.Delete(ctx, key)
		return nil, err
	}
	return a, nil
}

// AssetByKey returns the asset stored under key.
func (s *Service) AssetByKey(ctx context.Context, key string) (*Asset, error) {
	var a Asset
	err := s.db.Get(ctx, &a, `SELECT id, key, content_type, bytes, width, height FROM assets WHERE key = ?`, key)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func jsonList(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
