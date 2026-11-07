package ai

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

type scriptedSpendStore struct {
	userID, projectID string
	reserveErrors     []error
	reserveCalls      []ReserveRemoteCallParams
	reconcileCalls    []ReconcileRemoteCallParams
	estimateMicroUSD  int64
}

func (s *scriptedSpendStore) ReserveRemoteCall(_ context.Context, p ReserveRemoteCallParams) (RemoteReservation, error) {
	s.reserveCalls = append(s.reserveCalls, p)
	if len(s.reserveErrors) > 0 {
		err := s.reserveErrors[0]
		s.reserveErrors = s.reserveErrors[1:]
		if err != nil {
			return RemoteReservation{}, err
		}
	}
	micro, _ := usdToMicrodollars(p.EstimateUSD)
	s.estimateMicroUSD = micro
	return RemoteReservation{ID: p.ReservationID, JobID: p.JobID, Attempt: p.Attempt, Purpose: p.Purpose,
		UserID: s.userID, ProjectID: s.projectID, ProviderID: p.ProviderID, ModelID: p.ModelID,
		EstimateMicroUSD: micro, State: SpendStateReserved, Dispatch: true}, nil
}

func (s *scriptedSpendStore) ReconcileRemoteCall(_ context.Context, p ReconcileRemoteCallParams) (RemoteReservation, error) {
	s.reconcileCalls = append(s.reconcileCalls, p)
	actual := s.estimateMicroUSD
	if p.ActualUSD != nil {
		actual, _ = usdToMicrodollars(*p.ActualUSD)
	}
	return RemoteReservation{ID: p.ReservationID, EstimateMicroUSD: s.estimateMicroUSD,
		ActualMicroUSD: &actual, State: SpendStateReconciled, Outcome: p.Outcome}, nil
}

func TestAmbiguousReservationRetriesSameIDBeforeOneHTTPCall(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		provider := fixture.createProvider(t, providers, providerScopeServer, "ambiguous-key")
		jobID := fixture.seedRunningJob(t, provider.ID, 1)
		input := testScriptInput()
		valid := testScriptDocument(t, input)
		doer := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){
			func(*http.Request) (*http.Response, error) { return completionResponse(valid, 0.02), nil },
		}}
		spend := &scriptedSpendStore{userID: fixture.userID, projectID: fixture.projectID,
			reserveErrors: []error{errors.New("ambiguous database result"), nil}}
		service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, nil)
		plan := TextPlan{Task: TaskScript, Schema: SchemaScriptV1, RoutingMode: ModeBalanced,
			ProviderID: provider.ID, ModelID: openRouterModel, Remote: true}
		if _, err := service.GenerateText(context.Background(), TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
			ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)}); err != nil {
			t.Fatal(err)
		}
		if len(spend.reserveCalls) != 2 || !reflect.DeepEqual(spend.reserveCalls[0], spend.reserveCalls[1]) ||
			!db.ValidID(spend.reserveCalls[0].ReservationID) {
			t.Fatalf("reserve calls did not reuse an exact payload: %+v", spend.reserveCalls)
		}
		if doer.count() != 1 || len(spend.reconcileCalls) != 1 ||
			spend.reconcileCalls[0].ReservationID != spend.reserveCalls[0].ReservationID {
			t.Fatalf("calls: reserve=%d http=%d reconcile=%+v", len(spend.reserveCalls), doer.count(), spend.reconcileCalls)
		}
	})
}

func TestDefinitiveReservationErrorsAreNotRetried(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		fixture := newAIDBFixture(t, d)
		providers := fixture.providers(nil)
		provider := fixture.createProvider(t, providers, providerScopeServer, "definitive-key")
		jobID := fixture.seedRunningJob(t, provider.ID, 1)
		plan := TextPlan{Task: TaskScript, Schema: SchemaScriptV1, RoutingMode: ModeBalanced,
			ProviderID: provider.ID, ModelID: openRouterModel, Remote: true}
		for _, tc := range []struct {
			name string
			err  error
			code string
		}{
			{"policy", ErrRemoteDisabled, CodeLocalOnly},
			{"sentinel", ErrProviderUnavailable, CodeNoProvider},
			{"context", context.Canceled, CodeUpstream},
		} {
			t.Run(tc.name, func(t *testing.T) {
				doer := &queuedDoer{}
				spend := &scriptedSpendStore{userID: fixture.userID, projectID: fixture.projectID,
					reserveErrors: []error{tc.err}}
				service := newTextService(d, fixture.settings, providers, newOpenRouterClient(doer), spend, nil)
				_, err := service.GenerateText(context.Background(), TextGenerateRequest{Plan: plan, ActorID: fixture.userID,
					ProjectID: fixture.projectID, JobID: jobID, Attempt: 1, Input: testScriptInputJSON(t)})
				requireGenerateCode(t, err, tc.code)
				if len(spend.reserveCalls) != 1 || doer.count() != 0 {
					t.Fatalf("definitive error calls: reserve=%d http=%d", len(spend.reserveCalls), doer.count())
				}
			})
		}
	})
}
