package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/secrets"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil/testdb"
)

func forEachAIDialect(t *testing.T, fn func(*testing.T, *db.DB)) {
	t.Helper()
	testdb.ForEach(t, fn)
}

type aiDBFixture struct {
	d         *db.DB
	settings  *settings.Service
	box       *secrets.Box
	userID    string
	otherID   string
	projectID string
}

func newAIDBFixture(t *testing.T, d *db.DB) *aiDBFixture {
	t.Helper()
	ctx := context.Background()
	userID, otherID, projectID := db.NewID(), db.NewID(), db.NewID()
	for _, row := range []struct {
		id, username string
	}{
		{userID, "ai-owner-" + userID},
		{otherID, "ai-other-" + otherID},
	} {
		if _, err := d.Exec(ctx, `INSERT INTO users (id, username, display_name, role, status)
			VALUES (?, ?, ?, 'creator', 'active')`, row.id, row.username, row.username); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(ctx, `INSERT INTO projects (id, owner_id, title, slug) VALUES (?, ?, ?, ?)`,
		projectID, userID, "Paper Comet", "paper-comet-"+projectID); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{0x6d}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return &aiDBFixture{d: d, settings: settings.New(d), box: box, userID: userID, otherID: otherID, projectID: projectID}
}

func (f *aiDBFixture) providers(now func() time.Time) *providerStore {
	return newProviderStore(f.d, f.box, f.settings, now)
}

func (f *aiDBFixture) createProvider(t *testing.T, store *providerStore, scope, key string) providerView {
	t.Helper()
	view, err := store.Create(context.Background(), providerActor{UserID: f.userID, Admin: true}, providerCreate{
		Name: "OpenRouter test", Key: key, Scope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (f *aiDBFixture) seedRunningJob(t *testing.T, providerID string, attempt int) string {
	t.Helper()
	id := db.NewID()
	if _, err := f.d.Exec(context.Background(), `INSERT INTO jobs (
		id, type, scope, scope_kind, project_id, user_id, state, executor, model_id, provider_id, attempts
	) VALUES (?, ?, '{}', 'chapter', ?, ?, 'running', 'go', ?, ?, ?)`,
		id, TaskScript, f.projectID, f.userID, openRouterModel, providerID, attempt); err != nil {
		t.Fatal(err)
	}
	return id
}

func updateInstance(t *testing.T, st *settings.Service, fn func(*settings.Instance)) {
	t.Helper()
	if _, err := st.UpdateInstance(context.Background(), "", func(inst *settings.Instance) error {
		fn(inst)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func testScriptInput() ScriptInput {
	return ScriptInput{
		Mode: "premise", Premise: "A courier discovers a paper moon above the rain market.",
		Genres: []string{"fantasy"}, Style: "ink wash", Format: "manga_rtl", Language: "en",
		ChapterNumber: 1, ChapterTitle: "Paper Comet", PagesTarget: 2,
		Characters: []ScriptCharacter{{ID: "char-rin", Name: "Rin", Role: "courier", Look: "rain cape", Personality: "steadfast"}},
	}
}

func testScriptInputJSON(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(testScriptInput())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testScriptDocument(t *testing.T, in ScriptInput) json.RawMessage {
	t.Helper()
	b, err := generatePlaceholderScript(in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type seenRequest struct {
	Method        string
	URL           string
	Authorization string
	ContentType   string
	Body          []byte
}

type queuedDoer struct {
	mu         sync.Mutex
	seen       []seenRequest
	responders []func(*http.Request) (*http.Response, error)
}

func (d *queuedDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	d.mu.Lock()
	d.seen = append(d.seen, seenRequest{Method: req.Method, URL: req.URL.String(),
		Authorization: req.Header.Get("Authorization"), ContentType: req.Header.Get("Content-Type"), Body: body})
	if len(d.responders) == 0 {
		d.mu.Unlock()
		return nil, errors.New("test transport: no response queued")
	}
	responder := d.responders[0]
	d.responders = d.responders[1:]
	d.mu.Unlock()
	return responder(req)
}

func (d *queuedDoer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func (d *queuedDoer) requests() []seenRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]seenRequest(nil), d.seen...)
}

func httpJSON(status int, payload any, headers map[string]string) *http.Response {
	b, _ := json.Marshal(payload)
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(b))}
}

func completionResponse(content json.RawMessage, cost any) *http.Response {
	usage := map[string]any{}
	if cost != nil {
		usage["cost"] = cost
	}
	return httpJSON(http.StatusOK, map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": string(content), "refusal": ""}, "finish_reason": "stop"}},
		"usage":   usage,
	}, nil)
}
