package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// Routes that send only their first references never trade an identity
// reference for a control image: identity references come first, a pose or
// neighbouring page only fills slots that remain.
func TestRouteReferencesKeepIdentityReferencesBeforeControls(t *testing.T) {
	front := ImageRef{Data: []byte{1}, Role: "reference", Label: "Rin Asagiri · front"}
	side := ImageRef{Data: []byte{2}, Role: "reference", Label: "Rin Asagiri · side"}
	pose := ImageRef{Data: []byte{3}, Role: "pose", Label: "Pose skeleton, not identity"}
	neighbour := ImageRef{Data: []byte{4}, Role: "reference", Label: neighbourReferenceLabel}
	ids := func(refs []ImageRef) string {
		var b strings.Builder
		for _, r := range refs {
			b.WriteByte('0' + r.Data[0])
		}
		return b.String()
	}
	for _, tt := range []struct {
		in    []ImageRef
		limit int
		want  string
	}{
		{[]ImageRef{pose, front, side}, gptImageMaxRefs, "12"},
		{[]ImageRef{front, side, pose}, gptImageMaxRefs, "12"},
		{[]ImageRef{front, pose}, gptImageMaxRefs, "13"},
		{[]ImageRef{pose, front}, gptImageMaxRefs, "13"},
		{[]ImageRef{pose, front, side}, nanoBananaMaxRefs, "123"},
		{[]ImageRef{neighbour, pose}, gptImageMaxRefs, "43"},
		{nil, gptImageMaxRefs, ""},
		{[]ImageRef{front}, 0, ""},
	} {
		if got := ids(routeReferences(tt.in, tt.limit)); got != tt.want {
			t.Errorf("routeReferences(%s, %d) = %s, want %s", ids(tt.in), tt.limit, got, tt.want)
		}
	}
	req := ImageGenerateRequest{Prompt: "Rin crouches at the tide gate."}
	if p := gptImagePrompt(req, []ImageRef{front, side}, true); strings.Contains(p, "pose skeletons") || !strings.Contains(p, "reference sheets") {
		t.Fatalf("prompt describes a pose that is not sent: %q", p)
	}
	if p := gptImagePrompt(req, []ImageRef{front, pose}, true); !strings.Contains(p, "pose skeletons control body pose only") {
		t.Fatalf("prompt omits the sent pose control: %q", p)
	}
}

func TestOpenAIImageSendsIdentityReferencesWhenAPoseIsSet(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		out := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
		var b bytes.Buffer
		_ = png.Encode(&b, out)
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){gptImageOK([][]byte{b.Bytes()}, nil)}}
		m, f := imageTestModule(t, d, fake)
		createOpenAIImageProvider(t, m, f, "sk-pose")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1, References: 3, Pose: true}})
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		identity := imageTestPNG(t, 1500, 1500, color.NRGBA{200, 200, 200, 255})
		skeleton := imageTestPNG(t, 96, 96, color.NRGBA{0, 0, 0, 255})
		// The legacy order put the pose first; the adapter still keeps both
		// identity references within the two-reference route ceiling.
		req.References = []ImageRef{{Data: skeleton, ContentType: "image/png", Role: "pose", Label: "Pose skeleton, not identity"}, {Data: identity, ContentType: "image/png", Role: "reference"}, {Data: identity, ContentType: "image/png", Role: "reference"}}
		if _, err := m.image.GenerateImage(ctx, req); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Prompt string `json:"prompt"`
			Images []struct {
				ImageURL string `json:"image_url"`
			} `json:"images"`
		}
		if err := json.Unmarshal(fake.requests()[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Images) != gptImageMaxRefs {
			t.Fatalf("sent %d references", len(body.Images))
		}
		for _, im := range body.Images {
			if ref := decodeDataURLImage(t, im.ImageURL); ref.Bounds().Dx() != 1024 {
				t.Fatalf("a pose guide displaced an identity reference: %v", ref.Bounds())
			}
		}
		if strings.Contains(body.Prompt, "pose skeletons") {
			t.Fatalf("prompt describes the omitted pose guide: %q", body.Prompt)
		}
	})
}
