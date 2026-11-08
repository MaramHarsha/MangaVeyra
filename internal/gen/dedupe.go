package gen

import (
	"encoding/json"
)

// Interactive generation is de-duplicated in the database
// (jobs.EnqueueParams.DedupeKey and migration 0204), not in process memory,
// so the guarantee holds when several server processes share one database:
// while one user's identical request is queued or running, a repeated or
// concurrent submission receives that job instead of spending again.
//
// A key names everything that makes two requests the same piece of work: the
// user, the resolved target (page, panel, layer or chapter), the prompt, the
// routing mode, the variation count and any pin. Revisions are not part of
// it; the queued job is fenced to its own revision. Requests from another
// user, for another target, mask or prompt, or with another routing mode are
// different work and are never merged.

// dedupeKey canonically encodes the parts of a key.
func dedupeKey(parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	return string(b)
}

// panelDedupeKey keys a panel (scope panel) or layer (scope layer) request
// by its normalized stored request.
func panelDedupeKey(p imagePayload) string {
	var req PanelRequest
	if json.Unmarshal(p.Request, &req) != nil {
		return ""
	}
	req.ExpectedVersion = 0
	return dedupeKey("panel", p.Actor.UserID, p.Scope.Kind, p.PageID, p.PanelID, req)
}

// regionDedupeKey keys a region edit by its resolved target and the content
// hash of its mask: every submission uploads its own copy of a mask, so
// identical masks have different asset ids.
func regionDedupeKey(p imagePayload, maskSHA256 string) string {
	var req RegionEditRequest
	if json.Unmarshal(p.Request, &req) != nil {
		return ""
	}
	mask := "sha256:" + maskSHA256
	if maskSHA256 == "" {
		mask = "asset:" + req.MaskAssetID
	}
	req.ExpectedVersion, req.MaskAssetID = 0, ""
	return dedupeKey("region", p.Actor.UserID, p.PageID, p.PanelID, mask, req)
}
