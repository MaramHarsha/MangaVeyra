package export

// Frozen Phase 1 contract (docs/plans/0002-phase-1-first-playable.md).

import (
	"context"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Formats.
const (
	FormatPNG = "png" // ZIP of PNG pages
	FormatPDF = "pdf" // print-ready PDF with trim and bleed
	FormatCBZ = "cbz" // comic book archive (ZIP of images + ComicInfo.xml)
)

// Trim presets for PDF (trim size in millimetres, bleed in millimetres).
type Trim struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
	BleedMM  float64 `json:"bleed_mm"`
	// KDP marks Amazon KDP paperback presets (show the AI-disclosure reminder).
	KDP bool `json:"kdp"`
}

// Trims are the built-in trim presets.
var Trims = []Trim{
	{ID: "b5", Name: "B5 manga · 182 × 257 mm", WidthMM: 182, HeightMM: 257, BleedMM: 3},
	{ID: "a5", Name: "A5 · 148 × 210 mm", WidthMM: 148, HeightMM: 210, BleedMM: 3},
	{ID: "kdp-5x7.5", Name: "KDP manga · 5 × 7.5 in", WidthMM: 127, HeightMM: 190.5, BleedMM: 3.175, KDP: true},
	{ID: "kdp-5.5x8.5", Name: "KDP · 5.5 × 8.5 in", WidthMM: 139.7, HeightMM: 215.9, BleedMM: 3.175, KDP: true},
	{ID: "kdp-6x9", Name: "KDP · 6 × 9 in", WidthMM: 152.4, HeightMM: 228.6, BleedMM: 3.175, KDP: true},
}

// Request describes an export.
type Request struct {
	ProjectID string `json:"project_id"`
	ChapterID string `json:"chapter_id"`
	Format    string `json:"format"`
	// PageIDs limits the export (empty → whole chapter).
	PageIDs []string `json:"page_ids,omitempty"`
	Lang    string   `json:"lang,omitempty"`
	// TrimID selects a PDF trim preset (default "b5").
	TrimID string `json:"trim_id,omitempty"`
	// DPI for raster pages (default 300 for PDF, 2× page size for PNG).
	DPI int `json:"dpi,omitempty"`
	// CreditsPage appends a page listing human and AI contributions per role.
	CreditsPage bool `json:"credits_page"`
}

// Result is a finished export.
type Result struct {
	AssetID  string `json:"asset_id"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	// ASCIIFilename is the Content-Disposition fallback for Filename.
	ASCIIFilename string `json:"ascii_filename,omitempty"`
	ContentType   string `json:"content_type"`
	Pages         int    `json:"pages"`
	Bytes         int64  `json:"bytes"`
}

// Service builds export files.
type Service interface {
	// Export renders and stores the file synchronously (used by the export job handler).
	Export(ctx context.Context, actorUserID string, req Request) (Result, error)
}

// QueueService enqueues a revision-bound, read-only chapter export.
type QueueService interface {
	Enqueue(context.Context, studio.Actor, Request) (*jobs.Job, error)
}

// JobType is the Go-executor job that runs an export.
const JobType = "export.chapter"
