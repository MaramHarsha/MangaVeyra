package editor

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// keyedMutex serialises work per key (here: one user's generation requests
// for one page) without blocking unrelated users or pages.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedEntry
}

type keyedEntry struct {
	mu    sync.Mutex
	users int
}

// lock acquires the mutex for key and returns its release function.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.entries == nil {
		k.entries = map[string]*keyedEntry{}
	}
	e := k.entries[key]
	if e == nil {
		e = &keyedEntry{}
		k.entries[key] = e
	}
	e.users++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.users--
		if e.users == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}

// generation is one editor generation request, reduced to the fields that
// make two requests the same piece of work.
type generation struct {
	Kind    string // panel | layer | region
	UserID  string
	PageID  string
	PanelID string
	LayerID string
	// MaskSHA is the content hash of the region mask. Every submission uploads
	// its own mask asset, so identical masks have different ids.
	MaskSHA string
	Prompt  string
	Routing string
}

// activeGeneration returns the user's queued or running job that already does
// exactly this generation, or nil. A repeated or concurrent submission then
// reuses that job instead of spending a second generation. Requests from
// another user, for another page, panel, layer or mask, or with a different
// prompt or routing mode are different work and are never merged.
func (m *Module) activeGeneration(ctx context.Context, projectID string, want generation) (*jobs.Job, error) {
	typ := gen.JobPanel
	if want.Kind == "region" {
		typ = gen.JobRegionEdit
	}
	list, err := m.d.Jobs.List(ctx, jobs.Filter{UserID: want.UserID, ProjectID: projectID, States: []string{jobs.StateQueued, jobs.StateRunning}, Types: []string{typ}, Limit: 200})
	if err != nil {
		return nil, err
	}
	for _, j := range list {
		// Batch children (pages from script) and jobs being canceled are not
		// interactive requests and must not absorb a new one.
		if j.CancelRequested || j.ParentID != "" || j.UserID != want.UserID {
			continue
		}
		var payload struct {
			Request json.RawMessage `json:"request"`
		}
		if j.DecodeInput(&payload) != nil {
			continue
		}
		if want.Kind == "region" {
			var req gen.RegionEditRequest
			if json.Unmarshal(payload.Request, &req) != nil || req.LayerID != want.LayerID || req.Prompt != want.Prompt || req.RoutingMode != want.Routing {
				continue
			}
			if j.Scope.LayerID != "" && j.Scope.LayerID != want.LayerID {
				continue
			}
			mask, err := m.assets.Get(ctx, req.MaskAssetID)
			if err != nil || want.MaskSHA == "" || mask.SHA256 != want.MaskSHA {
				continue
			}
			return j, nil
		}
		var req gen.PanelRequest
		if json.Unmarshal(payload.Request, &req) != nil {
			continue
		}
		if req.PageID == want.PageID && req.PanelID == want.PanelID && req.LayerID == want.LayerID && req.Prompt == want.Prompt && req.RoutingMode == want.Routing {
			return j, nil
		}
	}
	return nil, nil
}
