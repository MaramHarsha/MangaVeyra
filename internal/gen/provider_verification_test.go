package gen

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
)

// An OpenAI organisation-verification refusal must reach the job as its own
// terminal public code, never as "internal" and never as a retryable failure.
func TestProviderVerificationRefusalStaysTerminalAndPublic(t *testing.T) {
	refusal := &ai.GenerateError{Code: ai.CodeProviderVerification, Retryable: true}
	if !knownAICode(ai.CodeProviderVerification) {
		t.Fatal("provider_verification is not a known AI code")
	}
	var planErr *ai.GenerateError
	if err := sanitizePlanningError(context.Background(), refusal); !errors.As(err, &planErr) || planErr.Code != ai.CodeProviderVerification {
		t.Fatalf("planning error = %v, want provider_verification", err)
	}
	var jobErr *jobs.JobError
	if err := mapAIJobError(context.Background(), &jobs.Run{}, refusal); !errors.As(err, &jobErr) || jobErr.Code != ai.CodeProviderVerification || jobErr.Retryable {
		t.Fatalf("job error = %#v, want terminal provider_verification", err)
	}
	if got := jobs.PublicErrorCode(ai.CodeProviderVerification); got != ai.CodeProviderVerification {
		t.Fatalf("public code = %q, want provider_verification", got)
	}
	if got := jobs.PublicErrorKey(ai.CodeProviderVerification); got != "activity.error.provider_verification" {
		t.Fatalf("public key = %q", got)
	}
}
