package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	xdraw "golang.org/x/image/draw"
)

// Direct OpenAI image route (ADR 0009). Endpoints, models, quality, sizes and
// tariffs are frozen; the connection is the same sealed OpenAI provider row the
// text route uses (ADR 0006).
const (
	openAIImageGenerateURL = "https://api.openai.com/v1/images/generations"
	openAIImageEditURL     = "https://api.openai.com/v1/images/edits"
	// gptImageRefEdge is the longest edge of a reference image after the
	// adapter downscales it, which also bounds its input tokens and bytes.
	gptImageRefEdge = 1024
	// gptImageEditEdge bounds an init image and its mask when a route takes
	// them as references; both scale together so they stay aligned.
	gptImageEditEdge = 1536
)

type gptImageInput struct {
	ImageURL string `json:"image_url"`
}

type gptImageRequest struct {
	Model        string          `json:"model"`
	Prompt       string          `json:"prompt"`
	N            int             `json:"n"`
	Size         string          `json:"size"`
	Quality      string          `json:"quality"`
	Background   string          `json:"background"`
	OutputFormat string          `json:"output_format"`
	Moderation   string          `json:"moderation"`
	Images       []gptImageInput `json:"images,omitempty"`
	Mask         *gptImageInput  `json:"mask,omitempty"`
}

type gptImageUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	InputDetails *struct {
		ImageTokens *int64 `json:"image_tokens"`
		TextTokens  *int64 `json:"text_tokens"`
	} `json:"input_tokens_details"`
}

type gptImageResponse struct {
	Data []struct {
		B64 string `json:"b64_json"`
	} `json:"data"`
	Usage *gptImageUsage `json:"usage"`
	Error *struct {
		Code              string `json:"code"`
		Type              string `json:"type"`
		ModerationDetails *struct {
			Stage string `json:"moderation_stage"`
		} `json:"moderation_details"`
	} `json:"error"`
}

// gptImageUsageCost prices reported usage at the frozen tariff, rounded up to
// whole microdollars. Without a text/image split every input token is priced
// at the higher image rate. It is not clamped: ADR 0003 records an actual
// above the estimate, which then blocks later reservations.
func gptImageUsageCost(u *gptImageUsage) *float64 {
	if u == nil || u.InputTokens == nil || u.OutputTokens == nil || *u.InputTokens < 0 || *u.OutputTokens < 0 {
		return nil
	}
	text, img := int64(0), *u.InputTokens
	if d := u.InputDetails; d != nil && d.TextTokens != nil && d.ImageTokens != nil && *d.TextTokens >= 0 && *d.ImageTokens >= 0 && *d.TextTokens+*d.ImageTokens == *u.InputTokens {
		text, img = *d.TextTokens, *d.ImageTokens
	}
	usd := nanoToCost(text*gptImageTextInNano + img*gptImageImageInNano + *u.OutputTokens*gptImageImageOutNano).USD
	return &usd
}

// gptImageBackground is the requested alpha behaviour. Transparency needs PNG
// or WebP; the adapter always asks for PNG.
func gptImageBackground(v string) string {
	if v == "transparent" {
		return "transparent"
	}
	return "opaque"
}

// gptImagePrompt is the exact text a GPT Image call sends, so the byte bound
// can be checked before any reservation.
func gptImagePrompt(req ImageGenerateRequest, refs int, maskParam bool) string {
	var b strings.Builder
	b.WriteString(req.Prompt)
	if req.Negative != "" {
		b.WriteString("\nAvoid: ")
		b.WriteString(req.Negative)
	}
	if refs > 0 {
		first := "The attached images are"
		if req.Init != nil {
			first = "After the first image, the attached images are"
		}
		b.WriteString("\n" + first + " this character's approved reference sheets: keep the face, hairstyle, costume and proportions consistent with them. Do not copy their layout or poses.")
	}
	if req.Mask != nil {
		if maskParam {
			b.WriteString("\nChange only the area marked by the mask on the first image and keep everything else unchanged. Return the full image.")
		} else {
			b.WriteString("\nThe final attached image is an edit mask: change only its white area in the first image and keep its black area unchanged. Return the full image.")
		}
	}
	if gptImageBackground(req.Background) == "transparent" {
		b.WriteString("\nKeep the background fully transparent.")
	}
	return b.String()
}

// validateGPTImageRequest runs before the reservation: nothing is reserved or
// sent for a request the route cannot carry.
func validateGPTImageRequest(req ImageGenerateRequest, maskParam bool, maxRefs int) error {
	if req.Count < 1 || req.Count > 4 || len(gptImagePrompt(req, min(len(req.References), maxRefs), maskParam)) > imagePromptBytes {
		return imageError(CodeBadInput, false, jobs.Cost{})
	}
	return nil
}

func (s *imageService) openAIImage(ctx context.Context, key string, req ImageGenerateRequest) (imageCall, error) {
	var out imageCall
	if strings.TrimSpace(key) == "" {
		return out, providerAdapterError(CodeProviderAuth, false, false)
	}
	if req.Plan.ModelID != gptImageSunburst && req.Plan.ModelID != gptImageFlare {
		return out, badInputAdapterError()
	}
	size := gptImageSizeFor(req.Width, req.Height)
	background := gptImageBackground(req.Background)
	refs := min(len(req.References), gptImageMaxRefs)
	payload := gptImageRequest{Model: req.Plan.ModelID, Prompt: gptImagePrompt(req, refs, true), N: req.Count,
		Size: fmt.Sprintf("%dx%d", size.W, size.H), Quality: gptImageQuality, Background: background, OutputFormat: "png", Moderation: "auto"}
	if len(payload.Prompt) > imagePromptBytes {
		return out, badInputAdapterError()
	}
	endpoint := openAIImageGenerateURL
	var box image.Rectangle
	if req.Init != nil {
		init, mask, rect, err := letterboxEdit(*req.Init, req.Mask, size, background == "transparent")
		if err != nil {
			return out, badInputAdapterError()
		}
		box = rect
		payload.Images = append(payload.Images, gptImageInput{ImageURL: pngDataURL(init)})
		if mask != nil {
			payload.Mask = &gptImageInput{ImageURL: pngDataURL(mask)}
		}
	}
	for _, r := range req.References[:refs] {
		data, err := downscaleReference(r.Data, gptImageRefEdge)
		if err != nil {
			return out, badInputAdapterError()
		}
		payload.Images = append(payload.Images, gptImageInput{ImageURL: pngDataURL(data)})
	}
	if len(payload.Images) > 0 {
		endpoint = openAIImageEditURL
	}
	body, status, sent, err := s.postImage(ctx, endpoint, "Bearer "+key, payload, nil)
	out.Sent = sent
	var decoded gptImageResponse
	decodeErr := json.Unmarshal(body, &decoded)
	if err != nil {
		if status >= 400 {
			out.ActualUSD, err = classifyGPTImageError(status, decoded, decodeErr == nil)
		}
		return out, err
	}
	if decodeErr != nil {
		return out, badOutputAdapterError(false)
	}
	out.ActualUSD = gptImageUsageCost(decoded.Usage)
	if len(decoded.Data) != req.Count {
		return out, badOutputAdapterError(false)
	}
	for _, item := range decoded.Data {
		data, err := decodeBase64Image(item.B64)
		if err != nil || storage.DetectContentType(data) != "image/png" {
			return out, badOutputAdapterError(false)
		}
		var g GeneratedImage
		if req.Init != nil {
			g, err = unboxEdit(data, box, size, req.Width, req.Height)
		} else {
			g, err = fitGeneratedImage(data, req.Width, req.Height, 0, true)
		}
		if err != nil {
			return out, err
		}
		out.Images = append(out.Images, g)
	}
	return out, nil
}

// classifyGPTImageError maps an Image API rejection to a safe code and its
// accounting. Rejections reconcile at zero (ADR 0006), except a moderation
// block at the output or an unknown stage, which may have consumed generation
// and so keeps the reservation (nil actual).
func classifyGPTImageError(status int, decoded gptImageResponse, parsed bool) (*float64, error) {
	zero := 0.
	if parsed && decoded.Error != nil {
		switch {
		case decoded.Error.Code == "moderation_blocked":
			refused := providerAdapterError(CodeProviderRefuse, false, false)
			if d := decoded.Error.ModerationDetails; d != nil && d.Stage == "input" {
				return &zero, refused
			}
			return nil, refused
		case decoded.Error.Type == "image_generation_user_error":
			return &zero, &adapterError{code: CodeBadInput, safe: "The image request is invalid.", cause: errors.New("ai: image request rejected")}
		}
	}
	var text openAIResponse
	if parsed && decoded.Error != nil {
		text.Error = &struct {
			Code string `json:"code"`
			Type string `json:"type"`
		}{Code: decoded.Error.Code, Type: decoded.Error.Type}
	}
	return &zero, classifyOpenAIStatus(status, text)
}

func pngDataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func encodePNG(im image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func decodeBounded(data []byte) (image.Image, error) {
	im, _, err := storage.DecodeImage(data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	return im, err
}

func decodeBase64Image(raw string) ([]byte, error) {
	if raw == "" || len(raw) > (maxImageBytes*4/3)+4 {
		return nil, errors.New("ai: invalid image payload")
	}
	return base64.StdEncoding.DecodeString(raw)
}

// downscaleReference re-encodes a reference as PNG with its longest edge at
// most edge pixels. Re-encoding also drops any embedded metadata.
func downscaleReference(data []byte, edge int) ([]byte, error) {
	im, err := decodeBounded(data)
	if err != nil {
		return nil, err
	}
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	scale := math.Min(1, float64(edge)/float64(max(w, h)))
	dw, dh := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), im, im.Bounds(), xdraw.Src, nil)
	return encodePNG(dst)
}

// letterboxEdit fits the init image inside the route's frozen size without
// cropping and converts the server mask (white = change) into the Image API
// mask (alpha 0 = change). Padding is never editable. It returns the content
// rectangle so the result maps back pixel-for-pixel.
func letterboxEdit(init ImageRef, mask *ImageRef, size gptImageSize, transparent bool) ([]byte, []byte, image.Rectangle, error) {
	src, err := decodeBounded(init.Data)
	if err != nil {
		return nil, nil, image.Rectangle{}, err
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	scale := math.Min(float64(size.W)/float64(w), float64(size.H)/float64(h))
	cw, ch := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	ox, oy := (size.W-cw)/2, (size.H-ch)/2
	box := image.Rect(ox, oy, ox+cw, oy+ch)
	canvas := image.NewNRGBA(image.Rect(0, 0, size.W, size.H))
	if !transparent {
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	}
	xdraw.CatmullRom.Scale(canvas, box, src, src.Bounds(), xdraw.Src, nil)
	initPNG, err := encodePNG(canvas)
	if err != nil {
		return nil, nil, image.Rectangle{}, err
	}
	if mask == nil {
		return initPNG, nil, box, nil
	}
	m, err := decodeBounded(mask.Data)
	if err != nil || m.Bounds().Dx() != w || m.Bounds().Dy() != h {
		return nil, nil, image.Rectangle{}, errors.New("ai: mask does not match the init image")
	}
	scaled := image.NewNRGBA(image.Rect(0, 0, size.W, size.H))
	xdraw.NearestNeighbor.Scale(scaled, box, m, m.Bounds(), xdraw.Src, nil)
	out := image.NewNRGBA(image.Rect(0, 0, size.W, size.H))
	for y := 0; y < size.H; y++ {
		for x := 0; x < size.W; x++ {
			c := scaled.NRGBAAt(x, y)
			if image.Pt(x, y).In(box) && c.R >= 128 && c.G >= 128 && c.B >= 128 && c.A >= 128 {
				out.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	maskPNG, err := encodePNG(out)
	return initPNG, maskPNG, box, err
}

// unboxEdit maps an edited image back to the init's own pixel grid by cropping
// the letterbox content rectangle (scaled to the returned size).
func unboxEdit(data []byte, box image.Rectangle, size gptImageSize, w, h int) (GeneratedImage, error) {
	im, err := decodeBounded(data)
	if err != nil || im.Bounds().Dx() < 64 || im.Bounds().Dy() < 64 {
		return GeneratedImage{}, badOutputAdapterError(false)
	}
	sx, sy := float64(im.Bounds().Dx())/float64(size.W), float64(im.Bounds().Dy())/float64(size.H)
	crop := image.Rect(int(math.Round(float64(box.Min.X)*sx)), int(math.Round(float64(box.Min.Y)*sy)),
		int(math.Round(float64(box.Max.X)*sx)), int(math.Round(float64(box.Max.Y)*sy))).Add(im.Bounds().Min).Intersect(im.Bounds())
	if crop.Empty() {
		return GeneratedImage{}, badOutputAdapterError(false)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), im, crop, xdraw.Src, nil)
	out, err := encodePNG(dst)
	if err != nil {
		return GeneratedImage{}, badOutputAdapterError(false)
	}
	return GeneratedImage{Data: out, ContentType: "image/png", Width: w, Height: h}, nil
}

// fitGeneratedImage validates provider output and fits it into w × h. With
// cover it scales to fill and centre-crops (the layer's own object-fit), so
// a nearest-size result is never distorted; otherwise it stretches, as ADR
// 0007's routes always did.
func fitGeneratedImage(data []byte, w, h int, seed int64, cover bool) (GeneratedImage, error) {
	var zero GeneratedImage
	if len(data) > maxImageBytes {
		return zero, badOutputAdapterError(false)
	}
	ct := storage.DetectContentType(data)
	if ct != "image/png" && ct != "image/jpeg" && ct != "image/webp" {
		return zero, badOutputAdapterError(false)
	}
	im, err := decodeBounded(data)
	if err != nil || im.Bounds().Dx() < 1 || im.Bounds().Dy() < 1 {
		return zero, badOutputAdapterError(false)
	}
	if im.Bounds().Dx() == w && im.Bounds().Dy() == h {
		return GeneratedImage{Data: data, ContentType: ct, Width: w, Height: h, Seed: seed}, nil
	}
	src := im.Bounds()
	if cover {
		sw, sh := float64(src.Dx()), float64(src.Dy())
		scale := math.Max(float64(w)/sw, float64(h)/sh)
		cw, ch := float64(w)/scale, float64(h)/scale
		x0, y0 := float64(src.Min.X)+(sw-cw)/2, float64(src.Min.Y)+(sh-ch)/2
		src = image.Rect(int(math.Round(x0)), int(math.Round(y0)), int(math.Round(x0+cw)), int(math.Round(y0+ch))).Intersect(im.Bounds())
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), im, src, xdraw.Src, nil)
	out, err := encodePNG(dst)
	if err != nil {
		return zero, badOutputAdapterError(false)
	}
	return GeneratedImage{Data: out, ContentType: "image/png", Width: w, Height: h, Seed: seed}, nil
}
