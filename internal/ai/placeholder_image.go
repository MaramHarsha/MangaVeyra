package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/MaramHarsha/MangaVeyra/internal/storage"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func placeholderImages(req ImageGenerateRequest) ([]GeneratedImage, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d", req.Prompt, req.Negative, req.Style, req.Seed)
	for _, ref := range req.References {
		hash.Write(ref.Data)
	}
	sum := hash.Sum(nil)
	base := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if req.Seed != 0 {
		base = req.Seed
	}
	images := make([]GeneratedImage, 0, req.Count)
	for index := 0; index < req.Count; index++ {
		seed := base + int64(index)
		im := image.NewNRGBA(image.Rect(0, 0, req.Width, req.Height))
		draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{245, 245, 240, 255}), image.Point{}, draw.Src)
		for y := 0; y < req.Height; y++ {
			for x := 0; x < req.Width; x++ {
				v := uint8(238)
				if (x+int(seed%13))%12 < 2 && (y+int(seed%17))%12 < 2 {
					v = 175
				}
				if y > req.Height*2/3+(x/32+int(seed%7))%17 {
					v = 90
				}
				im.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
			}
		}
		ink := color.NRGBA{25, 25, 28, 255}
		margin := max(4, req.Width/40)
		for k := 0; k < max(2, req.Width/250); k++ {
			for x := margin; x < req.Width-margin; x++ {
				im.SetNRGBA(x, margin+k, ink)
				im.SetNRGBA(x, req.Height-margin-k-1, ink)
			}
			for y := margin; y < req.Height-margin; y++ {
				im.SetNRGBA(margin+k, y, ink)
				im.SetNRGBA(req.Width-margin-k-1, y, ink)
			}
		}
		// Simple original storyboard silhouettes, deliberately identifiable as
		// offline stand-ins rather than artwork from an unavailable model.
		cx := req.Width/2 + int(seed%31) - 15
		cy := req.Height / 2
		radius := max(8, req.Width/12)
		for y := -radius; y <= radius; y++ {
			for x := -radius; x <= radius; x++ {
				if x*x+y*y <= radius*radius {
					im.SetNRGBA(cx+x, cy+y, ink)
				}
			}
		}
		draw.Draw(im, image.Rect(cx-radius, cy+radius, cx+radius, req.Height*4/5), image.NewUniform(ink), image.Point{}, draw.Src)
		d := font.Drawer{Dst: im, Src: image.NewUniform(ink), Face: basicfont.Face7x13, Dot: fixed.P(margin+6, margin+20)}
		d.DrawString("MANGAVEYRA PLACEHOLDER")
		d.Dot = fixed.P(margin+6, margin+36)
		d.DrawString(fmt.Sprintf("SEED %d / %d", seed, index+1))
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			return nil, err
		}
		images = append(images, GeneratedImage{Data: b.Bytes(), ContentType: "image/png", Width: req.Width, Height: req.Height, Seed: seed})
	}
	result := ImageResult{Images: images}
	if req.Mask != nil {
		if err := preserveMaskedImages(&result, req); err != nil {
			return nil, err
		}
	}
	return result.Images, nil
}

// The pixel invariant is enforced here as well as by the consuming generation
// handler. A remote edit is never trusted to preserve unmasked pixels.
func preserveMaskedImages(result *ImageResult, req ImageGenerateRequest) error {
	if req.Init == nil || req.Mask == nil {
		return imageError(CodeBadInput, false, result.Actual)
	}
	init, _, err := storage.DecodeImage(req.Init.Data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	if err != nil {
		return err
	}
	mask, _, err := storage.DecodeImage(req.Mask.Data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
	if err != nil {
		return err
	}
	if init.Bounds().Dx() != req.Width || init.Bounds().Dy() != req.Height || mask.Bounds().Dx() != req.Width || mask.Bounds().Dy() != req.Height {
		return imageError(CodeBadInput, false, result.Actual)
	}
	for i, g := range result.Images {
		art, _, err := storage.DecodeImage(g.Data, storage.ImageLimits{MaxWidth: 4096, MaxHeight: 4096, MaxPixels: 16 << 20})
		if err != nil || art.Bounds().Dx() != req.Width || art.Bounds().Dy() != req.Height {
			return imageError(CodeBadOutput, false, result.Actual)
		}
		dst := image.NewNRGBA(image.Rect(0, 0, req.Width, req.Height))
		for y := 0; y < req.Height; y++ {
			for x := 0; x < req.Width; x++ {
				r, g, b, _ := mask.At(x, y).RGBA()
				if r+g+b >= 3*32768 {
					dst.Set(x, y, art.At(x, y))
				} else {
					dst.Set(x, y, init.At(x, y))
				}
			}
		}
		var data bytes.Buffer
		if err := png.Encode(&data, dst); err != nil {
			return err
		}
		result.Images[i].Data = data.Bytes()
		result.Images[i].ContentType = "image/png"
		result.Images[i].Width = req.Width
		result.Images[i].Height = req.Height
	}
	return nil
}
