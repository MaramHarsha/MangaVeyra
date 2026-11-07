package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/httpx"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
)

// ManifestLookup returns the model manifest JSON handed to workers with a claimed job.
type ManifestLookup func(ctx context.Context, modelID string) (json.RawMessage, error)

// API serves the worker protocol (/api/worker/v1) and the job JSON API (/api/v1/jobs).
type API struct {
	Q      *Queue
	Store  storage.Store
	Signer *storage.Signer
	// PublicURL is used for signed input URLs when set; otherwise the URL the worker called is used.
	PublicURL string
	// Manifests resolves model manifests for claims (set by the ai module; nil sends {}).
	Manifests ManifestLookup
	// MaxArtifactBytes caps one uploaded artifact (default 1 GiB, LoRAs and videos are large).
	MaxArtifactBytes int64
}

// Name implements core.Module.
func (a *API) Name() string { return "jobs" }

// Routes registers the worker protocol and the job API.
func (a *API) Routes(r *httpx.Router) {
	w := r.Group("/api/worker/v1", httpx.RequireToken(httpx.TokenWorker))
	w.API("POST /register", a.register)
	w.API("POST /heartbeat", a.heartbeat)
	w.API("POST /claim", a.claim)
	w.API("POST /jobs/{id}/progress", a.progress)
	w.API("POST /jobs/{id}/artifacts", a.artifact)
	w.API("POST /jobs/{id}/complete", a.complete)
	w.API("POST /jobs/{id}/fail", a.fail)

	j := r.Group("/api/v1", httpx.RequireUser)
	j.API("GET /jobs", a.list, httpx.RequireScope("projects:read"))
	j.API("GET /jobs/stats", a.stats, httpx.RequireSession)
	j.API("GET /jobs/{id}", a.get, httpx.RequireScope("projects:read"))
	j.API("GET /jobs/{id}/events", a.events, httpx.RequireScope("projects:read"))
	j.API("POST /jobs/{id}/cancel", a.cancel, httpx.RequireScope("projects:write"), httpx.RequireScope("generate"))
	j.API("POST /jobs/{id}/retry", a.retry, httpx.RequireSession)
	r.API("GET /api/v1/workers", a.listWorkers, httpx.RequireRole(httpx.RoleAdmin))
	adm := r.Group("/api/v1/workers", httpx.RequireRole(httpx.RoleAdmin))
	adm.API("POST /{id}/drain", a.drainWorker)
	adm.API("POST /{id}/resume", a.resumeWorker)
	adm.API("DELETE /{id}", a.deleteWorker)
}

func tokenKey(r *http.Request) string { return httpx.TokenFrom(r.Context()).ID }

// ---- worker protocol -----------------------------------------------------------

func (a *API) register(w http.ResponseWriter, r *http.Request) error {
	var reg Registration
	if err := httpx.DecodeJSON(w, r, &reg); err != nil {
		return err
	}
	wk, err := a.Q.RegisterWorker(r.Context(), tokenKey(r), httpx.ClientIP(r), reg)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"worker_id":            wk.ID,
		"heartbeat_interval_s": int(HeartbeatInterval.Seconds()),
		"lease_s":              int(a.Q.opts.LeaseWorker.Seconds()),
	})
	return nil
}

type heartbeatReq struct {
	WorkerID string   `json:"worker_id"`
	GPUs     []GPU    `json:"gpus"`
	Running  []string `json:"running"`
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) error {
	var in heartbeatReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	wk, err := a.Q.WorkerFor(r.Context(), in.WorkerID, tokenKey(r))
	if err != nil {
		return err
	}
	stop, err := a.Q.WorkerHeartbeat(r.Context(), wk, in.GPUs, in.Running)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"cancel": stop})
	return nil
}

type claimReq struct {
	WorkerID string   `json:"worker_id"`
	JobTypes []string `json:"job_types"`
	Models   []string `json:"models"`
	WaitS    int      `json:"wait_s"`
}

type claimedJob struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"`
	Attempt int               `json:"attempt"`
	LeaseS  int               `json:"lease_s"`
	Model   claimedModel      `json:"model"`
	Input   json.RawMessage   `json:"input"`
	Files   map[string]string `json:"files"`
}

type claimedModel struct {
	ID       string          `json:"id"`
	Manifest json.RawMessage `json:"manifest"`
}

func (a *API) claim(w http.ResponseWriter, r *http.Request) error {
	var in claimReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	wk, err := a.Q.WorkerFor(r.Context(), in.WorkerID, tokenKey(r))
	if err != nil {
		return err
	}
	if wk.Status == WorkerDraining {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	types := claimTypes(wk, in.JobTypes)
	models := in.Models
	if len(models) == 0 {
		models = wk.Models.V
	}
	wait := time.Duration(max(0, min(in.WaitS, 30))) * time.Second
	// The long poll outlives the server's default write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(wait + 30*time.Second))
	j, err := a.Q.ClaimWait(r.Context(), ClaimParams{Executor: ExecutorWorker, Types: types, Models: models, WorkerID: wk.ID, MaxRunning: wk.MaxConcurrent}, wait)
	if err != nil {
		return err
	}
	if j == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	manifest := json.RawMessage("{}")
	if a.Manifests != nil && j.ModelID != "" {
		if m, err := a.Manifests(r.Context(), j.ModelID); err == nil && len(m) > 0 {
			manifest = m
		}
	}
	files := map[string]string{}
	base := a.baseURL(r)
	for name, key := range j.Files {
		files[name] = a.Signer.AbsoluteURL(base, key, a.Q.opts.LeaseWorker+10*time.Minute)
	}
	input := j.Input
	if input == nil {
		input = json.RawMessage("{}")
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"job": claimedJob{
		ID: j.ID, Type: j.Type, Attempt: j.Attempts, LeaseS: int(a.Q.opts.LeaseWorker.Seconds()),
		Model: claimedModel{ID: j.ModelID, Manifest: manifest}, Input: input, Files: files,
	}})
	return nil
}

func (a *API) baseURL(r *http.Request) string {
	if a.PublicURL != "" {
		return a.PublicURL
	}
	scheme := "http"
	if httpx.IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// workerFromQuery resolves the worker from ?worker_id= or the X-Worker-Id
// header (the progress/complete/fail bodies of the protocol do not carry it)
// and falls back to the job's current worker when it belongs to the same token.
func (a *API) jobWorker(r *http.Request, jobID string) (string, error) {
	j, err := a.Q.Get(r.Context(), jobID)
	if err != nil {
		return "", err
	}
	if j.Executor != ExecutorWorker || j.WorkerID == "" {
		return "", ErrLeaseLost
	}
	if _, err := a.Q.WorkerFor(r.Context(), j.WorkerID, tokenKey(r)); err != nil {
		return "", httpx.ErrForbidden("job is leased to another worker")
	}
	if claimed := r.Header.Get("X-Worker-Id"); claimed != "" && claimed != j.WorkerID {
		return "", ErrLeaseLost
	}
	return j.WorkerID, nil
}

func leaseErr(err error) error {
	if errors.Is(err, ErrLeaseLost) {
		return httpx.NewError(http.StatusConflict, "lease_lost", "this job is no longer leased to the worker; drop it")
	}
	return err
}

type progressReq struct {
	Progress      float64 `json:"progress"`
	Step          int     `json:"step"`
	Steps         int     `json:"steps"`
	Message       string  `json:"message"`
	PreviewPNGB64 string  `json:"preview_png_b64"`
}

func (a *API) progress(w http.ResponseWriter, r *http.Request) error {
	var in progressReq
	if err := httpx.DecodeJSONLimit(w, r, &in, 256<<10); err != nil {
		return err
	}
	id := r.PathValue("id")
	wid, err := a.jobWorker(r, id)
	if err != nil {
		if errors.Is(err, ErrLeaseLost) {
			httpx.JSON(w, http.StatusOK, map[string]bool{"cancel": true})
			return nil
		}
		return err
	}
	var preview []byte
	if in.PreviewPNGB64 != "" {
		if b, err := base64.StdEncoding.DecodeString(in.PreviewPNGB64); err == nil && len(b) <= 64<<10 && storage.DetectContentType(b) == "image/png" {
			preview = b
		}
	}
	cancel, err := a.Q.Progress(r.Context(), id, wid, ProgressUpdate{Progress: in.Progress, Step: in.Step, Steps: in.Steps, Message: in.Message, Preview: preview})
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"cancel": cancel})
	return nil
}

var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (a *API) artifact(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := a.jobWorker(r, id); err != nil {
		return leaseErr(err)
	}
	j, err := a.Q.Get(r.Context(), id)
	if err != nil {
		return err
	}
	limit := a.MaxArtifactBytes
	if limit <= 0 {
		limit = 1 << 30
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+1<<20)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute))
	mr, err := r.MultipartReader()
	if err != nil {
		return httpx.ErrBadRequest("expected multipart/form-data")
	}
	var (
		name, meta string
		key, ct    string
		size       int64
		stored     bool
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return httpx.AsAPIError(err)
		}
		switch part.FormName() {
		case "name":
			name = readSmall(part)
		case "meta":
			meta = readSmall(part)
		case "file":
			if stored {
				return httpx.ErrBadRequest("one file per upload")
			}
			if name == "" {
				name = strings.TrimSuffix(part.FileName(), extOf(part.FileName()))
			}
			if !artifactName.MatchString(name) {
				return httpx.ErrBadRequest("name must be 1–64 characters of letters, digits, dot, dash or underscore (send it before the file)")
			}
			sniffed, body, err := storage.Sniff(part)
			if err != nil {
				return err
			}
			ct = sniffed
			if ct == "application/octet-stream" || strings.HasPrefix(ct, "text/plain") {
				if pct := part.Header.Get("Content-Type"); pct != "" && !strings.Contains(pct, "html") && !strings.Contains(pct, "svg") {
					ct = pct
				}
			}
			key = fmt.Sprintf("jobs/%s/%s%s", j.ID, name, storage.ExtensionFor(ct))
			cr := &countingReader{r: body}
			if err := a.Store.Put(r.Context(), key, cr, -1, ct); err != nil {
				return httpx.AsAPIError(err)
			}
			size, stored = cr.n, true
		}
		part.Close()
	}
	if !stored {
		return httpx.ErrBadRequest("missing file field")
	}
	if meta != "" && !json.Valid([]byte(meta)) {
		meta = ""
	}
	if meta == "" {
		meta = "{}"
	}
	assetID := db.NewID()
	if _, err := a.Q.db.Exec(r.Context(), `INSERT INTO assets (id, key, content_type, bytes, kind, source, owner_id, project_id, meta, created_at)
		VALUES (?, ?, ?, ?, 'job_artifact', 'worker', ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET content_type = excluded.content_type, bytes = excluded.bytes, meta = excluded.meta`,
		assetID, key, ct, size, db.NullString(j.UserID), db.NullString(j.Scope.ProjectID), meta, db.Now()); err != nil {
		return err
	}
	_ = a.Q.Log(r.Context(), j.ID, "artifact", name, map[string]any{"key": key, "bytes": size, "content_type": ct})
	httpx.JSON(w, http.StatusOK, map[string]any{"key": key})
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func readSmall(p *multipart.Part) string {
	b, _ := io.ReadAll(io.LimitReader(p, 64<<10))
	return strings.TrimSpace(string(b))
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

type completeReq struct {
	Output json.RawMessage `json:"output"`
	Actual Cost            `json:"actual"`
}

func (a *API) complete(w http.ResponseWriter, r *http.Request) error {
	var in completeReq
	if err := httpx.DecodeJSONLimit(w, r, &in, 4<<20); err != nil {
		return err
	}
	id := r.PathValue("id")
	wid, err := a.jobWorker(r, id)
	if err != nil {
		return leaseErr(err)
	}
	if _, err := a.Q.Complete(r.Context(), id, wid, in.Output, in.Actual); err != nil {
		return leaseErr(err)
	}
	httpx.OK(w)
	return nil
}

type failReq struct {
	Error  JobError `json:"error"`
	Actual Cost     `json:"actual"`
}

func (a *API) fail(w http.ResponseWriter, r *http.Request) error {
	var in failReq
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	wid, err := a.jobWorker(r, id)
	if err != nil {
		return leaseErr(err)
	}
	if _, err := a.Q.Fail(r.Context(), id, wid, in.Error, in.Actual); err != nil {
		return leaseErr(err)
	}
	httpx.OK(w)
	return nil
}

// ---- job API --------------------------------------------------------------------

func (a *API) load(r *http.Request) (*Job, error) {
	j, err := a.Q.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if !a.Q.CanAccess(r.Context(), httpx.IdentityFrom(r.Context()), j) {
		return nil, httpx.ErrNotFound()
	}
	return j, nil
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	u := httpx.UserFrom(r.Context())
	q := r.URL.Query()
	p := httpx.ParsePage(r, 50, 200)
	f := Filter{UserID: u.ID, ProjectID: q.Get("project"), ChapterID: q.Get("chapter"), PageID: q.Get("page"),
		Executor: q.Get("executor"), Before: p.Cursor, Limit: p.Limit + 1}
	if token := httpx.TokenFrom(r.Context()); token != nil && len(token.ProjectIDs) != 0 {
		f.ProjectIDs = append([]string(nil), token.ProjectIDs...)
	}
	if s := q.Get("state"); s != "" {
		f.States = strings.Split(s, ",")
	}
	if t := q.Get("type"); t != "" {
		f.Types = strings.Split(t, ",")
	}
	if token := httpx.TokenFrom(r.Context()); q.Get("all") == "1" && u.IsAdmin() &&
		(token == nil || token.HasScope("admin")) {
		f.UserID = ""
	}
	list, err := a.Q.List(r.Context(), f)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, httpx.Paginate(list, p.Limit, func(j *Job) string { return j.ID }))
	return nil
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) error {
	u := httpx.UserFrom(r.Context())
	mine, err := a.Q.Stats(r.Context(), u.ID)
	if err != nil {
		return err
	}
	all, err := a.Q.Stats(r.Context(), "")
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"mine": mine, "server": all})
	return nil
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	j, err := a.load(r)
	if err != nil {
		return err
	}
	pos, err := a.Q.Position(r.Context(), j)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"job": j, "position": pos})
	return nil
}

func (a *API) events(w http.ResponseWriter, r *http.Request) error {
	j, err := a.load(r)
	if err != nil {
		return err
	}
	ev, err := a.Q.Events(r.Context(), j.ID)
	if err != nil {
		return err
	}
	if ev == nil {
		ev = []Event{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": ev})
	return nil
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request) error {
	j, err := a.load(r)
	if err != nil {
		return err
	}
	j, err = a.Q.Cancel(r.Context(), j.ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"job": j})
	return nil
}

func (a *API) retry(w http.ResponseWriter, r *http.Request) error {
	j, err := a.load(r)
	if err != nil {
		return err
	}
	j, err = a.Q.Retry(r.Context(), j.ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"job": j})
	return nil
}

func (a *API) listWorkers(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Q.ListWorkers(r.Context())
	if err != nil {
		return err
	}
	if list == nil {
		list = []Worker{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
	return nil
}

func (a *API) drainWorker(w http.ResponseWriter, r *http.Request) error {
	if err := a.Q.SetWorkerStatus(r.Context(), r.PathValue("id"), WorkerDraining); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (a *API) resumeWorker(w http.ResponseWriter, r *http.Request) error {
	if err := a.Q.SetWorkerStatus(r.Context(), r.PathValue("id"), WorkerOnline); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (a *API) deleteWorker(w http.ResponseWriter, r *http.Request) error {
	if err := a.Q.DeleteWorker(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

// Authorizer returns the ws authorizer for "job:" topics (owner, admins, or the access hook).
func (q *Queue) Authorizer() func(ctx context.Context, id httpx.Identity, topic string, write bool) error {
	return func(ctx context.Context, id httpx.Identity, topic string, write bool) error {
		if write {
			return httpx.ErrForbidden("")
		}
		if id.Token != nil && !id.Token.HasScope("projects:read") {
			return httpx.ErrForbidden("")
		}
		j, err := q.Get(ctx, strings.TrimPrefix(topic, "job:"))
		if err != nil || !q.CanAccess(ctx, id, j) {
			return httpx.ErrForbidden("")
		}
		return nil
	}
}
