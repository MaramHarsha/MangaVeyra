package studioui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

const defaultPagesTarget = 24

// Story editor views (C1 Prose / Panels toggle).
const (
	viewProse  = "prose"
	viewPanels = "panels"
)

type newProjectValues struct {
	Title        string
	ChapterTitle string
	Mode         string
	Premise      string
	SourceText   string
	Genres       []string
	Style        string
	Format       string
	PageSize     string
	Language     string
	PagesTarget  int
}

type newProjectView struct {
	Values newProjectValues
	Errors web.FormErrors
	// Step is the wizard step named in the URL (/studio/new/{step}).
	Step string
}

// wizardSteps are the five B3 steps in order; each is also a URL slug.
var wizardSteps = []string{"premise", "genre", "style", "format", "generate"}

// stepOfField maps every field the wizard posts to the step that holds it, so
// a rejected submit reopens the step with the error. The form-level message
// ("") belongs to the final step, beside the Generate button.
var stepOfField = map[string]string{
	"title": "premise", "mode": "premise", "premise": "premise", "source_text": "premise", "source_file": "premise",
	"genres": "genre", "style": "style", "format": "format", "page_size": "format", "pages_target": "format",
	"chapter_title": "generate", "language": "generate", "": "generate",
}

// openStep is the step the wizard opens on: the first step holding an error,
// else the step from the URL, else the first step.
func (v newProjectView) openStep() string {
	if steps := v.errorSteps(); len(steps) > 0 {
		return steps[0]
	}
	if contains(wizardSteps, v.Step) {
		return v.Step
	}
	return wizardSteps[0]
}

// errorSteps lists, in wizard order, the steps that hold a validation error.
func (v newProjectView) errorSteps() []string {
	var out []string
	for _, step := range wizardSteps {
		for field := range v.Errors {
			target, ok := stepOfField[field]
			if !ok {
				target = "generate"
			}
			if target == step {
				out = append(out, step)
				break
			}
		}
	}
	return out
}

func stepIndex(step string) int {
	for i, s := range wizardSteps {
		if s == step {
			return i
		}
	}
	return 0
}

type noChapterView struct {
	Project  studio.Project
	CanEdit  bool
	Error    string
	Chapters []studio.Chapter
}

type storyView struct {
	Project     studio.Project
	Chapter     studio.Chapter
	Chapters    []studio.Chapter
	Script      studio.ScriptDoc
	Job         *jobView
	CanEdit     bool
	CanGenerate bool
	ReadOnly    bool
	Conflict    bool
	Notice      string
	Error       string
	// View is "prose" for the read-only prose rendering; otherwise the
	// editable panel table shows.
	View string
	// SubmittedRevision is set when the editor is re-rendered from a rejected
	// save. The form must post back the revision the user started from, never
	// the chapter's current one, or a resubmit would silently overwrite a save
	// that landed in between.
	SubmittedRevision *string
}

// formRevision is the optimistic revision the script form posts back.
func formRevision(v storyView) string {
	if v.SubmittedRevision != nil {
		return *v.SubmittedRevision
	}
	return v.Chapter.UpdatedAt.Format(time.RFC3339Nano)
}

type jobView struct {
	Job         *jobs.Job
	Position    int
	StatusURL   string
	RetryURL    string
	StoryURL    string
	Active      bool
	Retryable   bool
	State       string
	Title       string
	Detail      string
	Progress    string
	ResultLabel string
}

func defaultNewProjectValues(ctxLocale string) newProjectValues {
	chapter := "Chapter 1"
	if strings.HasPrefix(ctxLocale, "ja") {
		chapter = "第1話"
	}
	language := "en"
	if contains(languageValues, ctxLocale) {
		language = ctxLocale
	}
	return newProjectValues{
		ChapterTitle: chapter,
		Mode:         "premise",
		Style:        styleValues[0],
		Format:       "manga_rtl",
		PageSize:     pageSizes[0].Value,
		Language:     language,
		PagesTarget:  defaultPagesTarget,
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func storyPath(projectID, chapterID string) string {
	base := "/studio/p/" + url.PathEscape(projectID) + "/story"
	if chapterID == "" {
		return base
	}
	return base + "/" + url.PathEscape(chapterID)
}

func jobPath(projectID, chapterID, jobID string) string {
	return storyPath(projectID, chapterID) + "/jobs/" + url.PathEscape(jobID)
}

func sceneField(sceneID, field string) string {
	return "scene." + sceneID + "." + field
}

func panelField(panelID, field string) string {
	return "panel." + panelID + "." + field
}

func dialogueField(panelID string, index int, field string) string {
	return fmt.Sprintf("panel.%s.dialogue.%d.%s", panelID, index, field)
}

func sfxField(panelID string, index int, field string) string {
	return fmt.Sprintf("panel.%s.sfx.%d.%s", panelID, index, field)
}

func chapterNumber(n int) string { return strconv.Itoa(n) }

func jobErrorKey(j *jobs.Job) string {
	if j == nil || j.State == jobs.StateCanceled {
		return "studioui.job.canceled_detail"
	}
	if j.Error == nil {
		return "studioui.job.failed_detail"
	}
	switch j.Error.Code {
	case jobs.ErrCodeTimeout:
		return "studioui.job.timeout_detail"
	case jobs.ErrCodeSafety:
		return "studioui.job.safety_detail"
	case jobs.ErrCodeBadInput:
		return "studioui.job.input_detail"
	case jobs.ErrCodeOOM:
		return "studioui.job.capacity_detail"
	case jobs.ErrCodeCanceled:
		return "studioui.job.canceled_detail"
	case ai.CodeSpendLimit:
		return "studioui.job.spend_limit_detail"
	case ai.CodeLocalOnly:
		return "studioui.job.local_only_detail"
	case ai.CodeNoProvider:
		return "studioui.job.no_provider_detail"
	case ai.CodeProviderAuth:
		return "studioui.job.provider_auth_detail"
	case ai.CodeProviderRefuse:
		return "studioui.job.provider_refused_detail"
	case ai.CodeRateLimited:
		return "studioui.job.rate_limited_detail"
	case ai.CodeBadOutput:
		return "studioui.job.bad_output_detail"
	case ai.CodeUpstream:
		return "studioui.job.upstream_detail"
	case ai.CodeApproval:
		return "studioui.job.approval_detail"
	default:
		return "studioui.job.failed_detail"
	}
}

// manualRetryAllowed reports whether retrying the same stored route can help.
// Configuration failures (local-only, spend cap, missing or rejected provider,
// approval, policy refusal, invalid brief) fail the same way again; the user
// generates a new job instead, which plans a fresh route.
func manualRetryAllowed(j *jobs.Job) bool {
	if j == nil {
		return false
	}
	if j.State == jobs.StateCanceled {
		return true
	}
	if j.State != jobs.StateFailed {
		return false
	}
	if j.Error == nil {
		return true
	}
	switch j.Error.Code {
	case ai.CodeSpendLimit, ai.CodeLocalOnly, ai.CodeNoProvider, ai.CodeProviderAuth, ai.CodeProviderRefuse,
		ai.CodeApproval, jobs.ErrCodeBadInput, jobs.ErrCodeSafety:
		return false
	}
	return true
}

func percent(progress float64) int {
	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 100
	}
	return int(progress*100 + .5)
}
