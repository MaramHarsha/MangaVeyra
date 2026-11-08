package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/MaramHarsha/MangaVeyra/internal/db"
)

// refIDs writes a reference list as its one-byte ids, a contact sheet in
// brackets: "[12]3" is a sheet of references 1 and 2, then reference 3.
func refIDs(refs []routeRef) string {
	var b strings.Builder
	for _, r := range refs {
		if len(r.Sheet) > 0 {
			b.WriteByte('[')
			for _, s := range r.Sheet {
				b.WriteByte('0' + s.Data[0])
			}
			b.WriteByte(']')
			continue
		}
		b.WriteByte('0' + r.Ref.Data[0])
	}
	return b.String()
}

// Routes that send only their first references never trade an identity
// reference for a control image. When identity references and a control do
// not fit together, the identity references share one contact sheet so the
// control still travels; without a control nothing is packed.
func TestRouteReferencesPackIdentityToKeepControls(t *testing.T) {
	ref := func(id byte, role, label string) ImageRef {
		return ImageRef{Data: []byte{id}, Role: role, Label: label}
	}
	front, side := ref(1, "reference", "Rin Asagiri · front"), ref(2, "reference", "Rin Asagiri · side")
	pose := ref(3, "pose", "Pose skeleton, not identity")
	neighbour := ref(4, "reference", neighbourReferenceLabel)
	face, back, outfit := ref(5, "reference", ""), ref(6, "reference", ""), ref(7, "reference", "")
	for _, tt := range []struct {
		in    []ImageRef
		limit int
		want  string
	}{
		{[]ImageRef{pose, front, side}, gptImageMaxRefs, "[12]3"},
		{[]ImageRef{front, side, pose}, gptImageMaxRefs, "[12]3"},
		{[]ImageRef{front, side, face, back, outfit, pose}, gptImageMaxRefs, "[1256]3"},
		{[]ImageRef{front, pose}, gptImageMaxRefs, "13"},
		{[]ImageRef{pose, front}, gptImageMaxRefs, "13"},
		{[]ImageRef{front, side}, gptImageMaxRefs, "12"},
		{[]ImageRef{front, side, face}, gptImageMaxRefs, "12"},
		{[]ImageRef{front, pose, neighbour}, gptImageMaxRefs, "13"},
		{[]ImageRef{pose, front, side}, nanoBananaMaxRefs, "123"},
		{[]ImageRef{neighbour, pose}, gptImageMaxRefs, "43"},
		{[]ImageRef{front, side, pose}, 1, "1"},
		{nil, gptImageMaxRefs, ""},
		{[]ImageRef{front}, 0, ""},
	} {
		got := routeReferences(tt.in, tt.limit)
		if refIDs(got) != tt.want {
			t.Errorf("routeReferences(%v, %d) = %s, want %s", tt.in, tt.limit, refIDs(got), tt.want)
		}
		if len(got) > max(tt.limit, 0) {
			t.Errorf("routeReferences sent %d images over a %d-slot route", len(got), tt.limit)
		}
	}
}

// The prompt lists exactly the images a call sends, in order, and guides
// only what was sent.
func TestGPTImagePromptDescribesExactlyTheImagesSent(t *testing.T) {
	front := ImageRef{Data: []byte{1}, Role: "reference"}
	pose := ImageRef{Data: []byte{3}, Role: "pose", Label: "Pose skeleton, not identity"}
	neighbour := ImageRef{Data: []byte{4}, Role: "reference", Label: neighbourReferenceLabel}
	req := ImageGenerateRequest{Prompt: "Rin crouches at the tide gate."}
	p := gptImagePrompt(req, []routeRef{{Ref: front}, {Ref: front}}, true)
	if !strings.Contains(p, "\nAttached images, in order: 1. an approved reference image of this character; 2. an approved reference image of this character.\n") ||
		!strings.Contains(p, "consistent with the reference images") || strings.Contains(p, "pose skeleton") || strings.Contains(p, "contact sheet") {
		t.Fatalf("two identity references: %q", p)
	}
	p = gptImagePrompt(req, []routeRef{{Sheet: []ImageRef{front, front, front}}, {Ref: pose}}, true)
	if !strings.Contains(p, "Attached images, in order: 1. a contact sheet of 3 approved reference images of this character, in reading order (left to right, then top to bottom); 2. a pose skeleton.") ||
		!strings.Contains(p, "Follow the pose skeleton for body pose only") || !strings.Contains(p, "consistent with the reference images") {
		t.Fatalf("sheet and pose: %q", p)
	}
	edit := req
	edit.Init = &ImageRef{Data: []byte{8}, Role: "init"}
	edit.Mask = &ImageRef{Data: []byte{9}, Role: "mask"}
	p = gptImagePrompt(edit, []routeRef{{Sheet: []ImageRef{front, front}}, {Ref: pose}}, false)
	if !strings.Contains(p, "Attached images, in order: 1. the image to edit; 2. a contact sheet of 2 approved reference images of this character, in reading order (left to right, then top to bottom); 3. a pose skeleton; 4. the edit mask.") ||
		!strings.Contains(p, "Change only the white area of the edit mask in the first image") {
		t.Fatalf("edit sent as references: %q", p)
	}
	// The direct route sends the mask as its own parameter, not an image.
	p = gptImagePrompt(edit, nil, true)
	if !strings.Contains(p, "Attached images, in order: 1. the image to edit.") || strings.Contains(p, "edit mask") ||
		!strings.Contains(p, "Change only the area marked by the mask on the first image") || strings.Contains(p, "reference images") {
		t.Fatalf("direct edit: %q", p)
	}
	p = gptImagePrompt(req, []routeRef{{Ref: neighbour}}, true)
	if !strings.Contains(p, "1. a neighbouring page of this chapter.") || !strings.Contains(p, "setting and lighting only") || strings.Contains(p, "reference images") {
		t.Fatalf("neighbour: %q", p)
	}
	if p = gptImagePrompt(req, nil, true); strings.Contains(p, "Attached images") {
		t.Fatalf("no images: %q", p)
	}
}

// A contact sheet is deterministic, at most 1,024 px on its longest edge,
// keeps every reference undistorted in reading order on white with plain
// gutters, and draws nothing else.
func TestContactSheetIsBoundedAndDeterministic(t *testing.T) {
	red := imageTestPNG(t, 1500, 1500, color.NRGBA{220, 30, 30, 255})
	blue := imageTestPNG(t, 300, 600, color.NRGBA{30, 30, 220, 255})
	clear := imageTestPNG(t, 200, 200, color.NRGBA{0, 0, 0, 0})
	green := imageTestPNG(t, 800, 400, color.NRGBA{30, 200, 30, 255})
	refs := []ImageRef{{Data: red}, {Data: blue}, {Data: clear}, {Data: green}}
	decode := func(data []byte) image.Image {
		t.Helper()
		im, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return im
	}
	at := func(im image.Image, x, y int) color.NRGBA { return color.NRGBAModel.Convert(im.At(x, y)).(color.NRGBA) }
	white := color.NRGBA{255, 255, 255, 255}

	two, err := contactSheet(refs[:2], gptImageRefEdge)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := contactSheet(refs[:2], gptImageRefEdge)
	if !bytes.Equal(two, again) {
		t.Fatal("contact sheet is not deterministic")
	}
	im := decode(two)
	if b := im.Bounds(); b.Dx() != 1024 || b.Dy() != 504 {
		t.Fatalf("two-reference sheet = %v, want 1024 × 504", b)
	}
	// Cell 1 (0–503) is filled by the square reference; the 16 px gutter is
	// white; cell 2 (520–1023) holds the 1:2 reference centred, 252 × 504.
	if at(im, 250, 250) != (color.NRGBA{220, 30, 30, 255}) || at(im, 511, 250) != white || at(im, 771, 250) != (color.NRGBA{30, 30, 220, 255}) ||
		at(im, 530, 250) != white || at(im, 1015, 250) != white {
		t.Fatalf("two-reference layout: %v %v %v %v %v", at(im, 250, 250), at(im, 511, 250), at(im, 771, 250), at(im, 530, 250), at(im, 1015, 250))
	}

	four, err := contactSheet(refs, gptImageRefEdge)
	if err != nil {
		t.Fatal(err)
	}
	im = decode(four)
	if b := im.Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
		t.Fatalf("four-reference sheet = %v, want 1024²", b)
	}
	// Reading order: red, blue / transparent (flattened to white), green.
	if at(im, 250, 250) != (color.NRGBA{220, 30, 30, 255}) || at(im, 771, 250) != (color.NRGBA{30, 30, 220, 255}) ||
		at(im, 250, 771) != white || at(im, 771, 771) != (color.NRGBA{30, 200, 30, 255}) || at(im, 771, 600) != white {
		t.Fatal("four-reference reading order or fit is wrong")
	}
	three, err := contactSheet(refs[:3], gptImageRefEdge)
	if err != nil {
		t.Fatal(err)
	}
	if b := decode(three).Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
		t.Fatalf("three-reference sheet = %v", b)
	}
	for _, n := range []int{0, 1} {
		if _, err := contactSheet(refs[:n], gptImageRefEdge); err == nil {
			t.Fatalf("a %d-reference contact sheet was made", n)
		}
	}
	if _, err := contactSheet(append(refs, refs[0]), gptImageRefEdge); err == nil {
		t.Fatal("a five-reference contact sheet was made")
	}
}

// A two-slot GPT Image call with two identity references and a pose sends
// [identity sheet, pose] and says exactly that in its prompt.
func TestOpenAIImageSendsAnIdentitySheetAndThePose(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		out := imageTestPNG(t, 1024, 1024, color.NRGBA{0, 0, 0, 0})
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){gptImageOK([][]byte{out}, nil)}}
		m, f := imageTestModule(t, d, fake)
		createOpenAIImageProvider(t, m, f, "sk-pose")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, Needs: ImageNeeds{Width: 512, Height: 512, Count: 1, References: 3, Pose: true}})
		quote, _ := quoteImage(providerKindOpenAI, p.ModelID, p.Needs)
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		req.Background = "transparent"
		front := imageTestPNG(t, 1500, 1500, color.NRGBA{200, 40, 40, 255})
		side := imageTestPNG(t, 1500, 1500, color.NRGBA{40, 40, 200, 255})
		skeleton := imageTestPNG(t, 512, 512, color.NRGBA{0, 0, 0, 255})
		// The legacy order put the pose first; identity still leads.
		req.References = []ImageRef{{Data: skeleton, ContentType: "image/png", Role: "pose", Label: "Pose skeleton, not identity"},
			{Data: front, ContentType: "image/png", Role: "reference"}, {Data: side, ContentType: "image/png", Role: "reference"}}
		result, err := m.image.GenerateImage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if result.Actual.USD > quote.USD {
			t.Fatalf("actual %v above the two-input quote %v", result.Actual.USD, quote.USD)
		}
		var body struct {
			Prompt string `json:"prompt"`
			Images []struct {
				ImageURL string `json:"image_url"`
			} `json:"images"`
			Mask any `json:"mask"`
		}
		seen := fake.requests()
		if len(seen) != 1 || seen[0].URL != openAIImageEditURL {
			t.Fatalf("requests = %+v", seen)
		}
		if err := json.Unmarshal(seen[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Images) != gptImageMaxRefs || body.Mask != nil {
			t.Fatalf("sent %d images", len(body.Images))
		}
		sheet := decodeDataURLImage(t, body.Images[0].ImageURL)
		if b := sheet.Bounds(); b.Dx() != 1024 || b.Dy() != 504 {
			t.Fatalf("identity sheet = %v", b)
		}
		left := color.NRGBAModel.Convert(sheet.At(250, 250)).(color.NRGBA)
		right := color.NRGBAModel.Convert(sheet.At(771, 250)).(color.NRGBA)
		if left.R < 180 || left.B > 80 || right.B < 180 || right.R > 80 {
			t.Fatalf("sheet order: left %v right %v", left, right)
		}
		if guide := decodeDataURLImage(t, body.Images[1].ImageURL); guide.Bounds().Dx() != 512 || guide.Bounds().Dy() != 512 {
			t.Fatalf("pose guide = %v", guide.Bounds())
		}
		for _, want := range []string{
			"Attached images, in order: 1. a contact sheet of 2 approved reference images of this character, in reading order (left to right, then top to bottom); 2. a pose skeleton.",
			"Follow the pose skeleton for body pose only",
			"consistent with the reference images",
			"Keep the background fully transparent.",
		} {
			if !strings.Contains(body.Prompt, want) {
				t.Fatalf("prompt lacks %q: %q", want, body.Prompt)
			}
		}
	})
}

// The OpenRouter GPT Image backup has the same two slots and packs the same
// way, after the init image and before the mask.
func TestOpenRouterGPTImagePacksIdentityBesideThePose(t *testing.T) {
	forEachAIDialect(t, func(t *testing.T, d *db.DB) {
		ctx := context.Background()
		out := imageTestPNG(t, 1024, 1024, color.NRGBA{9, 9, 9, 255})
		fake := &queuedDoer{responders: []func(*http.Request) (*http.Response, error){func(*http.Request) (*http.Response, error) {
			return httpJSON(200, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(out), "media_type": "image/png"}}, "usage": map[string]any{"cost": 0.05}}, nil), nil
		}}}
		m, f := imageTestModule(t, d, fake)
		f.createProvider(t, m.providers, providerScopeServer, "or-pack")
		p := planWith(t, m, f, ImagePlanRequest{Task: TaskPanel, PinModelID: openRouterGPTImageSunburst, Needs: ImageNeeds{Width: 1024, Height: 1024, Count: 1, References: 4, Pose: true}})
		id := imageTestJob(t, m, f, p)
		req := imageTestRequest(f, p, id)
		identity := imageTestPNG(t, 600, 600, color.NRGBA{120, 120, 120, 255})
		skeleton := imageTestPNG(t, 512, 512, color.NRGBA{0, 0, 0, 255})
		req.References = []ImageRef{{Data: identity, ContentType: "image/png", Role: "reference"}, {Data: identity, ContentType: "image/png", Role: "reference"},
			{Data: identity, ContentType: "image/png", Role: "reference"}, {Data: skeleton, ContentType: "image/png", Role: "pose"}}
		if _, err := m.image.GenerateImage(ctx, req); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Prompt string `json:"prompt"`
			Refs   []struct {
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"input_references"`
		}
		if err := json.Unmarshal(fake.requests()[0].Body, &body); err != nil || len(body.Refs) != 2 {
			t.Fatalf("references = %d, %v", len(body.Refs), err)
		}
		if b := decodeDataURLImage(t, body.Refs[0].ImageURL.URL).Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
			t.Fatalf("three-reference sheet = %v", b)
		}
		if !strings.Contains(body.Prompt, "1. a contact sheet of 3 approved reference images of this character") || !strings.Contains(body.Prompt, "2. a pose skeleton.") {
			t.Fatalf("prompt = %q", body.Prompt)
		}
	})
}

// Seedream sends every reference unchanged; its prompt names the pose guide
// by the position it is sent at, and says nothing when no pose is sent.
func TestSeedreamNamesThePoseGuideItSends(t *testing.T) {
	png1 := imageTestPNG(t, 64, 64, color.NRGBA{1, 1, 1, 255})
	req := ImageGenerateRequest{Plan: ImagePlan{ModelID: seedreamModel}, Prompt: "Rin crouches.", Width: 64, Height: 64, Count: 1,
		References: []ImageRef{{Data: png1, ContentType: "image/png", Role: "reference"}, {Data: png1, ContentType: "image/png", Role: "pose"}}}
	payload, err := seedreamPayload(req)
	if err != nil || !strings.Contains(payload["prompt"].(string), "Reference image 2 is a pose skeleton: follow it for body pose only") {
		t.Fatalf("payload prompt = %v, %v", payload["prompt"], err)
	}
	req.Init = &ImageRef{Data: png1, ContentType: "image/png", Role: "init"}
	if payload, _ = seedreamPayload(req); !strings.Contains(payload["prompt"].(string), "Reference image 3 is a pose skeleton") {
		t.Fatalf("with init = %v", payload["prompt"])
	}
	req.References = req.References[:1]
	if payload, _ = seedreamPayload(req); strings.Contains(payload["prompt"].(string), "pose skeleton") {
		t.Fatalf("no pose sent = %v", payload["prompt"])
	}
}
