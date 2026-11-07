package ui

import (
	"strings"
	"testing"
)

func TestVariationPickerShowsCommittedImageAndKeepsPlaceholderFallback(t *testing.T) {
	markup := render(t, VariationPicker("Pick a look", "Choose one", "index", "0", []Variation{{Value: "0", Label: "Front & side", ImageURL: "/media/candidate.png?sig=local", Lock: -1}, {Value: "1", Label: "Pending sheet", Lock: -1}}))
	has(t, markup, `src="/media/candidate.png?sig=local"`, `alt="Front &amp; side"`, `width="512" height="512"`, "Pending sheet", "mv-tone", `name="index" value="0" checked`, `aria-keyshortcuts="2"`)
	if strings.Contains(markup, "LOCK ") {
		t.Fatal("unscored references displayed a lock score")
	}
}

func TestSegmentedLinksIconsRetainAccessibleLabelsAndSelection(t *testing.T) {
	markup := render(t, SegmentedLinks("Project view", []SegLink{{Href: "/studio/projects?view=grid", Label: "Grid", Icon: "layout-grid", Current: true}, {Href: "/studio/projects?view=list", Label: "List", Icon: "list"}}))
	has(t, markup, `aria-label="Project view"`, `aria-current="true"`, `title="Grid"`, `ti-layout-grid`, `<span class="mv-sr-only">Grid</span>`, `ti-list`, `<span class="mv-sr-only">List</span>`)
}
