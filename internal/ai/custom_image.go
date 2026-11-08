package ai

import (
	"context"
	"errors"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/core/netguard"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// customImagePoll is how often a ComfyUI history, an HTTP status URL or a
// provider prediction is polled.
const customImagePoll = 1500 * time.Millisecond

func (s *imageService) pollInterval() time.Duration {
	if s != nil && s.pollEvery > 0 {
		return s.pollEvery
	}
	return customImagePoll
}

// customImageManifest is the router's view of one G12 image connection: a
// manifest built from its Describe step, and its routing modes.
func customImageManifest(c providerCandidate) (ModelManifest, []string, bool) {
	switch c.Kind {
	case providerKindComfyUI:
		cfg, err := decodeComfyConfig(c.Config)
		if err != nil {
			return ModelManifest{}, nil, false
		}
		return comfyManifest(c.ID, c.Name, cfg), cfg.routeModes(), true
	case providerKindHTTP:
		cfg, err := decodeHTTPConfig(c.Config)
		if err != nil {
			return ModelManifest{}, nil, false
		}
		return httpManifest(c.ID, c.Name, cfg), cfg.routeModes(), true
	case providerKindHuggingFace:
		cfg, err := decodeHFConfig(c.Config)
		if err != nil || cfg.Task != hfTaskImage {
			return ModelManifest{}, nil, false
		}
		return hfManifest(c.ID, c.Name, cfg), cfg.routeModes(), true
	}
	return ModelManifest{}, nil, false
}

// customImageCandidates adds the eligible custom image connections. A
// ComfyUI or HTTP server is local (no spend, may join Free only); a Hugging
// Face endpoint is remote and paid per hour. Unpinned routing honours each
// connection's routing modes.
func customImageCandidates(providers []providerCandidate, req ImagePlanRequest, n ImageNeeds, licenceGuard bool, exclude map[string]bool, skipped *[]string) []imageCandidate {
	out := []imageCandidate{}
	for _, p := range providers {
		m, modes, ok := customImageManifest(p)
		if !ok || (req.PinModelID != "" && req.PinModelID != m.ID) || (req.PinProviderID != "" && req.PinProviderID != p.ID) {
			continue
		}
		pinned := req.PinModelID == m.ID || req.PinProviderID == p.ID
		if !pinned && !modesAllow(modes, req.RoutingMode) {
			*skipped = append(*skipped, m.ID+": routing mode")
			continue
		}
		if !modelSupports(m, n) {
			*skipped = append(*skipped, m.ID+": capabilities")
			continue
		}
		if licenceGuard && !m.Commercial {
			*skipped = append(*skipped, m.ID+": licence")
			continue
		}
		if exclude[p.ID+"\x00"+m.ID] || exclude["kind:"+p.Kind] {
			continue
		}
		remote := p.Kind == providerKindHuggingFace
		if remote && req.RoutingMode == ModeFreeOnly {
			continue
		}
		out = append(out, imageCandidate{Model: m, ProviderID: p.ID, Kind: p.Kind, Executor: jobs.ExecutorGo, Remote: remote,
			Score: modelScore(req.RoutingMode, m, jobs.Cost{})})
	}
	return out
}

// customImage runs a plan routed to a custom image connection. The plan is
// untrusted: the connection, its description and the licence guard are
// re-read, and settings were re-read uncached by dispatchGuard.
func (s *imageService) customImage(ctx context.Context, req ImageGenerateRequest, n ImageNeeds) (ImageResult, error) {
	result := ImageResult{ProviderID: req.Plan.ProviderID, ModelID: req.Plan.ModelID}
	c, err := s.providers.Candidate(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	m, _, ok := customImageManifest(c)
	if !ok || m.ID != req.Plan.ModelID || !modelSupports(m, n) || req.Plan.Executor != jobs.ExecutorGo || req.Plan.Remote != (c.Kind == providerKindHuggingFace) {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	if req.Plan.PinProviderID != "" && req.Plan.PinProviderID != req.Plan.ProviderID || req.Plan.PinModelID != "" && req.Plan.PinModelID != req.Plan.ModelID {
		return result, imageError(CodeBadInput, false, jobs.Cost{})
	}
	inst, err := s.readSettings(ctx)
	if err != nil {
		return result, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	var public int
	if err := s.d.DB.Get(ctx, &public, `SELECT COUNT(*) FROM series WHERE project_id = ? AND visibility <> 'private'`, req.ProjectID); err != nil {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	if inst.Content.LicenceGuard && (req.Plan.PublicSeries || public > 0) && !m.Commercial {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	if c.Kind == providerKindHuggingFace {
		return s.hfImageReserved(ctx, req)
	}
	// A server on this machine or network: ADR 0007's custom text rules. No
	// reservation; the private-network client refuses every request while
	// the live setting is off.
	if inst.LocalOnly || req.Plan.RoutingMode == ModeLocalOnly {
		return result, imageError(CodeLocalOnly, false, jobs.Cost{})
	}
	p, err := s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil || p.Kind != c.Kind {
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	var call imageCall
	switch p.Kind {
	case providerKindComfyUI:
		cfg, err := decodeComfyConfig(p.Config)
		if err != nil {
			return result, imageError(CodeNoProvider, false, jobs.Cost{})
		}
		call, err = comfyImage(ctx, comfyClient{doer: s.localDoer, base: p.BaseURL, key: p.Key, poll: s.pollInterval()}, cfg, req)
		if err != nil {
			return result, safeImageAdapterError(err, jobs.Cost{})
		}
	case providerKindHTTP:
		cfg, err := decodeHTTPConfig(p.Config)
		if err != nil {
			return result, imageError(CodeNoProvider, false, jobs.Cost{})
		}
		call, err = httpImage(ctx, httpClient{doer: s.localDoer, base: p.BaseURL, key: p.Key, poll: s.pollInterval()}, cfg, req)
		if err != nil {
			return result, safeImageAdapterError(err, jobs.Cost{})
		}
	default:
		return result, imageError(CodeNoProvider, false, jobs.Cost{})
	}
	result.Images = call.Images
	return result, nil
}

// localTransportError reports a request the private-network gate refused
// (the admin turned private endpoints off, or the address is public) as
// no_provider, terminal, rather than a retryable outage.
func localTransportError(err error) error {
	if errors.Is(err, errPrivateEndpointsOff) || errors.Is(err, netguard.ErrBlocked) {
		return &adapterError{code: CodeNoProvider, safe: "Private-network endpoints are off on this server.", cause: errPrivateEndpointsOff}
	}
	return classifyTransportError(err)
}

// hfImageReserved runs a Hugging Face endpoint call under an ADR 0003
// reservation of zero: the endpoint is billed per hour, so a call has no
// per-call charge, but the reservation still fences local_only, the job
// attempt and the connection at the spend lock.
func (s *imageService) hfImageReserved(ctx context.Context, req ImageGenerateRequest) (ImageResult, error) {
	result := ImageResult{ProviderID: req.Plan.ProviderID, ModelID: req.Plan.ModelID}
	if !req.Plan.Remote || req.Plan.RoutingMode == ModeFreeOnly {
		return result, imageError(CodeBadInput, false, jobs.Cost{})
	}
	purpose, err := s.spend.initialSpendPurpose(ctx, req.JobID, req.Attempt)
	if err != nil {
		return result, imageError(CodeUpstream, false, jobs.Cost{})
	}
	reservation, err := s.spend.reserveRemoteCall(ctx, ReserveRemoteCallParams{ReservationID: db.NewID(), JobID: req.JobID, Attempt: req.Attempt,
		Purpose: purpose, ProviderID: req.Plan.ProviderID, ModelID: req.Plan.ModelID, EstimateUSD: 0, ExpiresAt: s.spend.timeNow().Add(5 * time.Minute)})
	if err != nil {
		return result, safeImageAdapterError(mapSpendError(err), jobs.Cost{})
	}
	if !reservation.Dispatch {
		return result, imageError(CodeUpstream, false, jobs.Cost{})
	}
	zero := 0.0
	finish := func(callErr error) (ImageResult, error) {
		outcome := SpendOutcomeSuccess
		if callErr != nil {
			outcome = SpendOutcomeFailure
		}
		if _, err := s.spend.reconcile(reservation.ID, outcome, &zero); err != nil {
			return result, imageError(CodeUpstream, false, jobs.Cost{})
		}
		if callErr != nil {
			return result, safeImageAdapterError(callErr, jobs.Cost{})
		}
		return result, nil
	}
	if _, err := s.dispatchGuard(ctx, req); err != nil {
		return finish(err)
	}
	p, err := s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil || p.Kind != providerKindHuggingFace {
		return finish(imageError(CodeNoProvider, false, jobs.Cost{}))
	}
	cfg, err := decodeHFConfig(p.Config)
	if err != nil || cfg.Task != hfTaskImage || cfg.Endpoint != hfDedicated {
		return finish(imageError(CodeNoProvider, false, jobs.Cost{}))
	}
	call, err := hfImage(ctx, s.hfDoer, p.BaseURL, p.Key, req)
	if err == nil {
		result.Images = call.Images
	}
	return finish(err)
}
