package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/settings"
)

const (
	reservationTTL        = 5 * time.Minute
	reservationDBAttempts = 2
)

type remoteSpendStore interface {
	ReserveRemoteCall(context.Context, ReserveRemoteCallParams) (RemoteReservation, error)
	ReconcileRemoteCall(context.Context, ReconcileRemoteCallParams) (RemoteReservation, error)
}

type textService struct {
	db        *db.DB
	settings  instanceReader
	providers *providerStore
	// clients holds one transport per remote provider kind (remoteRoutes).
	clients   map[string]remoteTextClient
	localDoer httpDoer
	spend     remoteSpendStore
	now       func() time.Time
}

type textDecision struct {
	Mode       string `json:"mode"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Remote     bool   `json:"remote"`
	Reason     string `json:"reason"`
}

type pinChoice struct {
	placeholder bool
	remote      bool
	providerID  string
	modelID     string
	explicit    bool
}

func newTextService(d *db.DB, st instanceReader, providers *providerStore, client *openRouterClient,
	spend remoteSpendStore, now func() time.Time) *textService {
	if now == nil {
		now = db.Now
	}
	clients := map[string]remoteTextClient{}
	if client != nil {
		clients[providerKindOpenRouter] = client
	}
	return &textService{db: d, settings: st, providers: providers, clients: clients, spend: spend, now: now}
}

// withClient registers the transport for another remote provider kind.
func (s *textService) withClient(kind string, client remoteTextClient) *textService {
	if s.clients == nil {
		s.clients = map[string]remoteTextClient{}
	}
	if client != nil && validProviderKind(kind) {
		s.clients[kind] = client
	}
	return s
}

func (s *textService) PlanText(ctx context.Context, req TextPlanRequest) (TextPlan, error) {
	var zero TextPlan
	if req.Task != TaskScript || req.Schema != SchemaScriptV1 || strings.TrimSpace(req.ActorID) == "" ||
		strings.TrimSpace(req.ProjectID) == "" {
		return zero, badInputError(nil)
	}
	requestedMode := strings.TrimSpace(req.RoutingMode)
	if requestedMode != "" && !validRoutingMode(requestedMode) {
		return zero, badInputError(nil)
	}
	in, err := decodeScriptInput(req.Input)
	if err != nil {
		return zero, badInputError(err)
	}
	if local, handled, err := s.planLocalText(ctx, req, in); handled {
		return local, err
	}
	pin, err := validatePins(req.PinProviderID, req.PinModelID)
	if err != nil {
		return zero, badInputError(err)
	}
	var (
		inst       settings.Instance
		instLoaded bool
	)
	if requestedMode == "" {
		inst, err = loadRemoteSettings(ctx, s.settings)
		if err != nil {
			// There is no trustworthy configured default. Pin the resulting plan
			// to local-only so dispatch remains fail closed as well.
			req.RoutingMode = ModeLocalOnly
			return placeholderPlan(req, "routing settings unavailable"), nil
		}
		instLoaded = true
		requestedMode = inst.RoutingMode
	}
	req.RoutingMode = requestedMode
	forcedPlaceholder := req.RoutingMode == ModeLocalOnly || req.RoutingMode == ModeFreeOnly
	if forcedPlaceholder && pin.remote {
		if req.RoutingMode == ModeLocalOnly {
			return zero, localOnlyError(nil)
		}
		return zero, noProviderError(nil)
	}
	if pin.placeholder || forcedPlaceholder {
		reason := "placeholder pin"
		if !pin.placeholder {
			reason = req.RoutingMode + " mode"
		}
		return placeholderPlan(req, reason), nil
	}

	var settingsErr error
	if !instLoaded {
		inst, settingsErr = loadRemoteSettings(ctx, s.settings)
	}
	if settingsErr != nil || inst.LocalOnly {
		if pin.remote {
			return zero, localOnlyError(settingsErr)
		}
		return placeholderPlan(req, "remote access unavailable"), nil
	}
	// Pick the provider: an explicit pin, or the first eligible connection in
	// store order (personal before server, oldest first). A candidate whose
	// frozen route cannot hold this input, or does not match a model pin, is
	// skipped rather than truncating private source text.
	var chosen providerCandidate
	tooLarge := false
	if pin.providerID != "" {
		candidate, err := s.providers.Candidate(ctx, req.ActorID, pin.providerID)
		if err != nil {
			return zero, noProviderError(err)
		}
		route, ok := routeForKind(candidate.Kind)
		if !ok || (pin.modelID != "" && pin.modelID != route.Model) || !candidate.Test.textAvailable() {
			return zero, noProviderError(nil)
		}
		if !route.fits(in) {
			return zero, badInputError(errors.New("ai: script input exceeds remote context"))
		}
		chosen = candidate
	} else {
		candidates, err := s.providers.Candidates(ctx, req.ActorID)
		if err != nil {
			if pin.remote {
				return zero, noProviderError(err)
			}
			return placeholderPlan(req, "provider lookup unavailable"), nil
		}
		for _, candidate := range candidates {
			route, ok := routeForKind(candidate.Kind)
			// A connection whose last model-list check did not list the
			// frozen text model is skipped like any other unusable route.
			if !ok || (pin.modelID != "" && pin.modelID != route.Model) || !candidate.Test.textAvailable() {
				continue
			}
			if !route.fits(in) {
				tooLarge = true
				continue
			}
			chosen = candidate
			break
		}
		if chosen.ID == "" {
			if pin.remote {
				if tooLarge {
					return zero, badInputError(errors.New("ai: script input exceeds remote context"))
				}
				return zero, noProviderError(nil)
			}
			if tooLarge {
				return placeholderPlan(req, "input exceeds remote context"), nil
			}
			return placeholderPlan(req, "no remote provider connected"), nil
		}
	}
	route, _ := routeForKind(chosen.Kind)
	decision := marshalDecision(textDecision{Mode: req.RoutingMode, ProviderID: chosen.ID, ModelID: route.Model,
		Remote: true, Reason: "eligible " + route.Label + " connection"})
	return TextPlan{Task: req.Task, Schema: req.Schema, RoutingMode: req.RoutingMode, ProviderID: chosen.ID,
		ModelID: route.Model, Remote: true, Decision: decision, Estimate: jobs.Cost{USD: route.PlanUSD}}, nil
}

func (s *textService) GenerateText(ctx context.Context, req TextGenerateRequest) (TextResult, error) {
	var zero TextResult
	if req.Plan.Task != TaskScript || req.Plan.Schema != SchemaScriptV1 || !validRoutingMode(req.Plan.RoutingMode) ||
		strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.ProjectID) == "" {
		return zero, badInputError(nil)
	}
	in, err := decodeScriptInput(req.Input)
	if err != nil {
		return zero, badInputError(err)
	}
	if req.Plan.ProviderID == PlaceholderID || req.Plan.ModelID == PlaceholderText {
		if req.Plan.ProviderID != PlaceholderID || req.Plan.ModelID != PlaceholderText || req.Plan.Remote {
			return zero, badInputError(nil)
		}
		doc, err := generatePlaceholderScript(in)
		if err != nil {
			return zero, &GenerateError{Code: CodeBadOutput, SafeMessage: "The offline script could not be generated.", Err: err}
		}
		return TextResult{Document: doc, ProviderID: PlaceholderID, ModelID: PlaceholderText}, nil
	}
	if !req.Plan.Remote && req.Plan.ProviderID != PlaceholderID {
		return s.generateLocalText(ctx, req, in)
	}
	route, known := routeForModel(req.Plan.ModelID)
	if !req.Plan.Remote || req.Plan.ProviderID == "" || !known || req.JobID == "" || req.Attempt <= 0 {
		return zero, badInputError(nil)
	}
	if req.Plan.RoutingMode == ModeLocalOnly {
		return zero, localOnlyError(nil)
	}
	if req.Plan.RoutingMode == ModeFreeOnly {
		return zero, noProviderError(nil)
	}
	if !route.fits(in) {
		return zero, badInputError(errors.New("ai: script input exceeds remote context"))
	}

	purpose, err := s.initialSpendPurpose(ctx, req.JobID, req.Attempt)
	if err != nil {
		return zero, upstreamError(err, false, jobs.Cost{})
	}
	initial, initialErr := s.remoteCall(ctx, req, in, scriptAttempt{}, purpose)
	total := initial.cost
	reason := ""
	if initialErr == nil {
		_, normalized, decodeErr := decodeScriptDocument(initial.content, in)
		if decodeErr == nil {
			return TextResult{Document: normalized, ProviderID: req.Plan.ProviderID, ModelID: route.Model, Actual: total}, nil
		}
		reason = scriptOutputReason(decodeErr)
		initialErr = badOutputAdapterError(true)
	}
	if !isRepairable(initialErr) {
		return zero, generateErrorFrom(initialErr, total)
	}

	repair, repairErr := s.remoteCall(ctx, req, in, scriptAttempt{Repair: true, Reason: reason}, SpendPurposeRepair)
	total = total.Add(repair.cost)
	if repairErr != nil {
		return zero, generateErrorFrom(repairErr, total)
	}
	_, normalized, decodeErr := decodeScriptDocument(repair.content, in)
	if decodeErr != nil {
		if repairReason := scriptOutputReason(decodeErr); repairReason != "" {
			if reason != "" && reason != repairReason {
				reason += "; repair: " + repairReason
			} else {
				reason = repairReason
			}
		}
		return zero, &GenerateError{Code: CodeBadOutput, SafeMessage: invalidScriptMessage(reason),
			Retryable: false, Actual: total, Err: errInvalidScriptOutput}
	}
	return TextResult{Document: normalized, ProviderID: req.Plan.ProviderID, ModelID: route.Model, Actual: total}, nil
}

type remoteCallResult struct {
	content json.RawMessage
	cost    jobs.Cost
}

func (s *textService) remoteCall(ctx context.Context, req TextGenerateRequest, in ScriptInput, attempt scriptAttempt, purpose string) (remoteCallResult, error) {
	var out remoteCallResult
	if err := s.remoteGuard(ctx, req); err != nil {
		return out, err
	}
	route, known := routeForModel(req.Plan.ModelID)
	client := s.clients[route.Kind]
	if !known || client == nil {
		return out, badInputAdapterError()
	}
	reservationID := db.NewID()
	reserveParams := ReserveRemoteCallParams{
		ReservationID: reservationID,
		JobID:         req.JobID,
		Attempt:       req.Attempt,
		Purpose:       purpose,
		ProviderID:    req.Plan.ProviderID,
		ModelID:       route.Model,
		EstimateUSD:   route.ReserveUSD,
		ExpiresAt:     s.timeNow().Add(reservationTTL),
	}
	reservation, err := s.reserveRemoteCall(ctx, reserveParams)
	if err != nil {
		return out, mapSpendError(err)
	}
	if !reservation.Dispatch {
		return out, upstreamAdapterError(false)
	}
	if reservation.UserID != req.ActorID || reservation.ProjectID != req.ProjectID {
		zero := 0.0
		closed, reconcileErr := s.reconcile(reservation.ID, SpendOutcomeFailure, &zero)
		out.cost = reservationCost(closed, reservation)
		if reconcileErr != nil {
			return out, upstreamAdapterError(false)
		}
		return out, badInputAdapterError()
	}

	// The reservation repeats job/settings/provider checks under the spend lock.
	// Re-read settings and decrypt the current row only after it commits, then do
	// no unrelated work before the transport call.
	if err := s.remoteGuard(ctx, req); err != nil {
		zero := 0.0
		closed, reconcileErr := s.reconcile(reservation.ID, SpendOutcomeFailure, &zero)
		out.cost = reservationCost(closed, reservation)
		if reconcileErr != nil {
			return out, upstreamAdapterError(false)
		}
		return out, err
	}
	provider, err := s.providers.ResolveDispatch(ctx, req.ActorID, req.Plan.ProviderID)
	if err == nil && provider.Kind != route.Kind {
		err = errProviderUnavailable
	}
	if err != nil {
		zero := 0.0
		closed, reconcileErr := s.reconcile(reservation.ID, SpendOutcomeFailure, &zero)
		out.cost = reservationCost(closed, reservation)
		if reconcileErr != nil {
			return out, upstreamAdapterError(false)
		}
		return out, noProviderError(err)
	}

	call, callErr := client.Complete(ctx, provider.Key, in, attempt)
	outcome := SpendOutcomeSuccess
	if callErr != nil {
		outcome = SpendOutcomeFailure
		if errors.Is(callErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			outcome = SpendOutcomeCanceled
		}
	}
	actual := call.ActualUSD
	if !call.Sent {
		zero := 0.0
		actual = &zero
	}
	closed, reconcileErr := s.reconcile(reservation.ID, outcome, actual)
	out.cost = reservationCost(closed, reservation)
	if reconcileErr != nil {
		return out, upstreamAdapterError(false)
	}
	if callErr != nil {
		return out, callErr
	}
	out.content = call.Content
	return out, nil
}

// initialSpendPurpose distinguishes a first execution from a retry. Attempts
// never repeat (Queue.Retry starts a new round at attempt_base), so any
// attempt above 1 is a retry; job history also covers rows created before
// attempt_base existed.
func (s *textService) initialSpendPurpose(ctx context.Context, jobID string, attempt int) (string, error) {
	if attempt > 1 {
		return SpendPurposeRetry, nil
	}
	if s == nil || s.db == nil {
		return "", errors.New("ai: retry history unavailable")
	}
	var retried int
	if err := s.db.Get(ctx, &retried, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND kind = 'retried'`, jobID); err != nil {
		return "", errors.New("ai: retry history unavailable")
	}
	if retried > 0 {
		return SpendPurposeRetry, nil
	}
	return SpendPurposeInitial, nil
}

// reserveRemoteCall retries one ambiguous database result with the exact same
// id and payload. Known policy/state errors and canceled contexts are
// definitive and must not be retried.
func (s *textService) reserveRemoteCall(ctx context.Context, p ReserveRemoteCallParams) (RemoteReservation, error) {
	var zero RemoteReservation
	if s == nil || s.spend == nil {
		return zero, errors.New("ai: spend store unavailable")
	}
	var last error
	for attempt := 0; attempt < reservationDBAttempts; attempt++ {
		reservation, err := s.spend.ReserveRemoteCall(ctx, p)
		if err == nil {
			return reservation, nil
		}
		last = err
		if definitiveReserveError(err) || ctx.Err() != nil {
			return zero, err
		}
	}
	return zero, last
}

func definitiveReserveError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, sentinel := range []error{
		ErrInvalidSpendAmount,
		ErrRemoteDisabled,
		ErrReservationNotFound,
		ErrReservationConflict,
		ErrReservationExpired,
		ErrJobAttemptNotRunning,
		ErrProviderUnavailable,
		ErrUserRemoteSpendLimit,
		ErrServerRemoteSpendLimit,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

func (s *textService) remoteGuard(ctx context.Context, req TextGenerateRequest) error {
	inst, err := loadRemoteSettings(ctx, s.settings)
	if err != nil || inst.LocalOnly || req.Plan.RoutingMode == ModeLocalOnly {
		return localOnlyError(err)
	}
	if req.Plan.RoutingMode == ModeFreeOnly {
		return noProviderError(nil)
	}
	route, known := routeForModel(req.Plan.ModelID)
	if !known || req.Plan.ProviderID == "" || req.Plan.ProviderID == PlaceholderID {
		return badInputAdapterError()
	}
	candidate, err := s.providers.Candidate(ctx, req.ActorID, req.Plan.ProviderID)
	if err != nil {
		return noProviderError(err)
	}
	if candidate.Kind != route.Kind {
		return noProviderError(errProviderUnavailable)
	}
	return nil
}

func (s *textService) reconcile(id, outcome string, actual *float64) (RemoteReservation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.spend.ReconcileRemoteCall(ctx, ReconcileRemoteCallParams{ReservationID: id, Outcome: outcome, ActualUSD: actual})
}

func (s *textService) timeNow() time.Time {
	if s == nil || s.now == nil {
		return db.Now()
	}
	return s.now().UTC().Truncate(time.Microsecond)
}

func validatePins(providerID, modelID string) (pinChoice, error) {
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	choice := pinChoice{providerID: providerID, modelID: modelID, explicit: providerID != "" || modelID != ""}
	if !choice.explicit {
		return choice, nil
	}
	if providerID == PlaceholderID || modelID == PlaceholderText {
		if providerID != "" && providerID != PlaceholderID {
			return pinChoice{}, errInvalidScriptInput
		}
		if modelID != "" && modelID != PlaceholderText {
			return pinChoice{}, errInvalidScriptInput
		}
		choice.placeholder = true
		return choice, nil
	}
	if _, known := routeForModel(modelID); modelID != "" && !known {
		return pinChoice{}, errInvalidScriptInput
	}
	if providerID == PlaceholderID {
		return pinChoice{}, errInvalidScriptInput
	}
	choice.remote = true
	return choice, nil
}

func validRoutingMode(mode string) bool {
	switch mode {
	case ModeBalanced, ModeBest, ModeFastest, ModeLocalOnly, ModeFreeOnly:
		return true
	default:
		return false
	}
}

func placeholderPlan(req TextPlanRequest, reason string) TextPlan {
	decision := marshalDecision(textDecision{Mode: req.RoutingMode, ProviderID: PlaceholderID,
		ModelID: PlaceholderText, Remote: false, Reason: reason})
	return TextPlan{Task: req.Task, Schema: req.Schema, RoutingMode: req.RoutingMode, ProviderID: PlaceholderID,
		ModelID: PlaceholderText, Remote: false, Decision: decision, Estimate: jobs.Cost{}}
}

func marshalDecision(decision textDecision) json.RawMessage {
	b, err := json.Marshal(decision)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func reservationCost(closed, fallback RemoteReservation) jobs.Cost {
	micro := fallback.EstimateMicroUSD
	if closed.ActualMicroUSD != nil {
		micro = *closed.ActualMicroUSD
	}
	return jobs.Cost{USD: float64(micro) / 1_000_000}
}

func isRepairable(err error) bool {
	var adapter *adapterError
	return errors.As(err, &adapter) && adapter.repairable
}

func generateErrorFrom(err error, actual jobs.Cost) *GenerateError {
	var generated *GenerateError
	if errors.As(err, &generated) {
		return &GenerateError{Code: generated.Code, SafeMessage: generated.SafeMessage, Retryable: generated.Retryable,
			Actual: actual, Err: generated.Err}
	}
	var adapter *adapterError
	if errors.As(err, &adapter) {
		return &GenerateError{Code: adapter.code, SafeMessage: adapter.safe, Retryable: adapter.retryable, Actual: actual,
			Err: adapter.cause}
	}
	return upstreamError(err, false, actual)
}

func mapSpendError(err error) error {
	switch {
	case errors.Is(err, ErrRemoteDisabled):
		return localOnlyError(err)
	case errors.Is(err, ErrUserRemoteSpendLimit), errors.Is(err, ErrServerRemoteSpendLimit):
		return &GenerateError{Code: CodeSpendLimit, SafeMessage: "The monthly remote-spend limit has been reached.", Err: err}
	case errors.Is(err, ErrProviderUnavailable):
		return noProviderError(err)
	case errors.Is(err, ErrJobAttemptNotRunning):
		return badInputError(err)
	default:
		return upstreamError(err, false, jobs.Cost{})
	}
}

func badInputError(err error) *GenerateError {
	if err == nil {
		err = errInvalidScriptInput
	}
	return &GenerateError{Code: CodeBadInput, SafeMessage: "The script request is invalid.", Err: err}
}

func localOnlyError(err error) *GenerateError {
	if err == nil {
		err = ErrRemoteDisabled
	}
	return &GenerateError{Code: CodeLocalOnly, SafeMessage: "Remote text generation is disabled by local-only mode.", Err: err}
}

func noProviderError(err error) *GenerateError {
	if err == nil {
		err = ErrNoProvider
	}
	return &GenerateError{Code: CodeNoProvider, SafeMessage: "No eligible text provider is available.", Err: err}
}

func upstreamError(err error, retryable bool, actual jobs.Cost) *GenerateError {
	if err == nil {
		err = errors.New("ai: upstream failure")
	}
	return &GenerateError{Code: CodeUpstream, SafeMessage: "The text provider could not complete the request.",
		Retryable: retryable, Actual: actual, Err: err}
}
