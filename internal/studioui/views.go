package studioui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
	"github.com/MaramHarsha/MangaVeyra/internal/web"
)

const defaultPagesTarget = 24

type newProjectValues struct {
	Title        string
	ChapterTitle string
	Mode         string
	Premise      string
	SourceText   string
	Genres       []string
	Style        string
	Format       string
	Language     string
	PagesTarget  int
}

type newProjectView struct {
	Values newProjectValues
	Errors web.FormErrors
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
	return newProjectValues{
		ChapterTitle: chapter,
		Mode:         "premise",
		Style:        "bw_screentone",
		Format:       "manga_rtl",
		Language:     ctxLocale,
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
	default:
		return "studioui.job.failed_detail"
	}
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
