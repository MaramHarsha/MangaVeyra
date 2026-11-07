package gen

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func TestRealOpenRouterApprovalBoundaryUsesMicrodollars(t *testing.T) {
	action, err := scaleCost(jobs.Cost{USD: 0.890490}, scriptMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	if action.USD != 2.671470 {
		t.Fatalf("action USD = %.18f, want %.18f", action.USD, 2.671470)
	}
	exceeded, err := approvalExceeded(action.USD, 2.671470)
	if err != nil || exceeded {
		t.Fatalf("equal displayed threshold exceeded = %v, %v", exceeded, err)
	}
	exceeded, err = approvalExceeded(action.USD, 2.671469)
	if err != nil || !exceeded {
		t.Fatalf("lower threshold exceeded = %v, %v", exceeded, err)
	}
	perAttempt, err := unscaleCost(action, scriptMaxAttempts)
	if err != nil || perAttempt.USD != 0.890490 {
		t.Fatalf("unscaled cost = %+v, %v", perAttempt, err)
	}
}

func TestJobCancellationMappersPreserveCanceledCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, mapped := range map[string]error{
		"studio context":        mapStudioJobError(ctx, fmt.Errorf("wrapped: %w", context.Canceled)),
		"studio queue sentinel": mapStudioJobError(ctx, jobs.ErrCanceled),
		"auth context":          mapAuthorizationJobError(ctx, fmt.Errorf("wrapped: %w", context.Canceled)),
		"auth queue sentinel":   mapAuthorizationJobError(ctx, jobs.ErrCanceled),
	} {
		t.Run(name, func(t *testing.T) {
			var jobErr *jobs.JobError
			if !errors.As(mapped, &jobErr) || jobErr.Code != jobs.ErrCodeCanceled {
				t.Fatalf("mapped error = %#v", mapped)
			}
		})
	}
}

func TestPromptMetadataBoundsValidStudioProjects(t *testing.T) {
	genres := make([]string, maxPromptGenres+8)
	for i := range genres {
		genres[i] = fmt.Sprintf("genre-%02d", i)
	}
	boundedGenres := promptGenres(genres)
	if len(boundedGenres) != maxPromptGenres || boundedGenres[len(boundedGenres)-1] != "genre-31" {
		t.Fatalf("bounded genres = %d / %q", len(boundedGenres), boundedGenres[len(boundedGenres)-1])
	}
	projectID := db.NewID()
	longJapanese := strings.Repeat("夜", maxPromptCharacterText+1)
	characters := make([]studio.Character, maxPromptCharacters+1)
	for i := range characters {
		characters[i] = studio.Character{ID: db.NewID(), ProjectID: projectID, Name: fmt.Sprintf("Character %d", i),
			Role: "supporting", Look: longJapanese, Personality: longJapanese}
	}
	boundedCharacters, err := promptCharacters(projectID, characters)
	if err != nil {
		t.Fatal(err)
	}
	if len(boundedCharacters) != maxPromptCharacters {
		t.Fatalf("bounded character count = %d", len(boundedCharacters))
	}
	if utf8.RuneCountInString(boundedCharacters[0].Look) != maxPromptCharacterText ||
		utf8.RuneCountInString(boundedCharacters[0].Personality) != maxPromptCharacterText {
		t.Fatalf("character metadata was not bounded by runes")
	}
	characters[len(characters)-1].ProjectID = db.NewID()
	if _, err := promptCharacters(projectID, characters); !errors.Is(err, studio.ErrForbidden) {
		t.Fatalf("cross-project character error = %v", err)
	}
}
