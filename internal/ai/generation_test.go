package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

func TestRemoteTextReservationsRepairUsageAndAttemptPurpose(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		fixture := newAIDBFixture(t, d)
		fixed := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
		providers := fixture.providers(func() time.Time { return fixed })
		provider := fixture.createProvider(t, providers, providerScopeServer, "dispatch-key")
		input := testScriptInput()
		inputRaw := testScriptInputJSON(t)
		valid := testScriptDocument(t, input)
		plan := TextPlan{Task: TaskScript, Schema: SchemaScriptV1, RoutingMode: ModeBalanced,
			ProviderID: provider.ID, ModelID: openRouterModel, Remote: true, Decision: json.RawMessage(`{"remote":false}`)}

		t.Run("reported cost and exact request", func(t *testing.T) {
			jobID := fixture.seedRunningJob(t, provider.ID, 1)
			doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
				func(*http.Request) (*http.Response, error) { return completionResponse(valid, 0.123), nil },
			}}
			spend := NewSpendStore(d, fixture.settings)
			spend.Now = func() time.Time { return fixed }
			service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, func() time.Time { return fixed })
			result, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: inputRaw})
			if err != nil || math.Abs(result.Actual.USD-0.123) > 1e-12 || !json.Valid(result.Document) {
				t.Fatalf("generate = %+v, %v", result, err)
			}
			seen := doer.requests()
			if len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].URL != openRouterChatURL ||
				seen[0].Authorization != "Bearer dispatch-key" || seen[0].ContentType != "application/json" {
				t.Fatalf("dispatch request = %+v", seen)
			}
			var sent openRouterRequest
			if err := json.Unmarshal(seen[0].Body, &sent); err != nil || sent.Model != openRouterModel ||
				!sent.Usage.Include || !sent.Provider.RequireParameters {
				t.Fatalf("request JSON = %+v, %v", sent, err)
			}
			var row struct {
				Purpose      string `db:"purpose"`
				State        string `db:"state"`
				Outcome      string `db:"outcome"`
				ActualSource string `db:"actual_source"`
				Actual       *int64 `db:"actual_microusd"`
			}
			if err := d.Get(ctx, &row, `SELECT purpose, state, outcome, actual_source, actual_microusd
				FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
				t.Fatal(err)
			}
			if row.Purpose != SpendPurposeInitial || row.State != SpendStateReconciled || row.Outcome != SpendOutcomeSuccess ||
				row.ActualSource != SpendActualReported || row.Actual == nil || *row.Actual != 123_000 {
				t.Fatalf("initial reservation = %+v", row)
			}
		})

		t.Run("invalid initial gets one separately reserved repair", func(t *testing.T) {
			jobID := fixture.seedRunningJob(t, provider.ID, 1)
			doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
				func(*http.Request) (*http.Response, error) {
					return completionResponse(json.RawMessage(`{"bad":true}`), 0.0), nil
				},
				func(*http.Request) (*http.Response, error) { return completionResponse(valid, nil), nil },
			}}
			spend := NewSpendStore(d, fixture.settings)
			spend.Now = func() time.Time { return fixed }
			service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, func() time.Time { return fixed })
			result, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: inputRaw})
			if err != nil || doer.count() != 2 || math.Abs(result.Actual.USD-openRouterReserveUSD) > 1e-12 {
				t.Fatalf("repair generate = %+v, %v, calls=%d", result, err, doer.count())
			}
			var rows []struct {
				Purpose      string `db:"purpose"`
				ActualSource string `db:"actual_source"`
				Actual       *int64 `db:"actual_microusd"`
			}
			if err := d.Select(ctx, &rows, `SELECT purpose, actual_source, actual_microusd
				FROM spend_reservations WHERE job_id = ? ORDER BY call_seq`, jobID); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0].Purpose != SpendPurposeInitial || rows[0].ActualSource != SpendActualReported ||
				rows[0].Actual == nil || *rows[0].Actual != 0 || rows[1].Purpose != SpendPurposeRepair ||
				rows[1].ActualSource != SpendActualEstimate || rows[1].Actual == nil || *rows[1].Actual != 445_245 {
				t.Fatalf("repair reservations = %+v", rows)
			}
		})

		t.Run("later job attempt uses retry purpose", func(t *testing.T) {
			jobID := fixture.seedRunningJob(t, provider.ID, 2)
			doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
				func(*http.Request) (*http.Response, error) { return completionResponse(valid, 0.01), nil },
			}}
			spend := NewSpendStore(d, fixture.settings)
			spend.Now = func() time.Time { return fixed }
			service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, func() time.Time { return fixed })
			_, err := service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: jobID, Attempt: 2, Input: inputRaw})
			if err != nil {
				t.Fatal(err)
			}
			var purpose string
			if err := d.Get(ctx, &purpose, `SELECT purpose FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
				t.Fatal(err)
			}
			if purpose != SpendPurposeRetry {
				t.Fatalf("purpose = %q", purpose)
			}
		})

		t.Run("manual queue retry attempt one uses retry purpose", func(t *testing.T) {
			jobID := fixture.seedRunningJob(t, provider.ID, 1)
			queue := jobs.New(d, fixture.settings, nil, jobs.Options{})
			failed, err := queue.Fail(ctx, jobID, "", jobs.JobError{Code: "upstream", Message: "failed", Retryable: false}, jobs.Cost{})
			if err != nil || failed.State != jobs.StateFailed {
				t.Fatalf("fail = %+v, %v", failed, err)
			}
			retried, err := queue.Retry(ctx, jobID)
			if err != nil || retried.State != jobs.StateQueued || retried.Attempts != 0 {
				t.Fatalf("retry = %+v, %v", retried, err)
			}
			claimed, err := queue.Claim(ctx, jobs.ClaimParams{Executor: jobs.ExecutorGo, Types: []string{TaskScript}})
			if err != nil || claimed == nil || claimed.ID != jobID || claimed.Attempts != 1 {
				t.Fatalf("claim = %+v, %v", claimed, err)
			}
			doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
				func(*http.Request) (*http.Response, error) { return completionResponse(valid, 0.01), nil },
			}}
			spend := NewSpendStore(d, fixture.settings)
			spend.Now = func() time.Time { return fixed }
			service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, func() time.Time { return fixed })
			_, err = service.GenerateText(ctx, TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
				ProjectID: fixture.projectID, JobID: jobID, Attempt: claimed.Attempts, Input: inputRaw})
			if err != nil {
				t.Fatal(err)
			}
			var purpose string
			if err := d.Get(ctx, &purpose, `SELECT purpose FROM spend_reservations WHERE job_id = ?`, jobID); err != nil {
				t.Fatal(err)
			}
			if purpose != SpendPurposeRetry {
				t.Fatalf("manual retry purpose = %q", purpose)
			}
		})
	})
}
