package gen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"

	"golang.org/x/image/draw"

	"github.com/MaramHarsha/MangaVeyra/internal/ai"
	"github.com/MaramHarsha/MangaVeyra/internal/compose"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

func imageRefCount(p imagePayload) int {
	n := len(p.References) + len(p.ContextPages)
	if len(p.Pose) > 0 {
		n++
	}
	return n
}

func (s *service) controlReferences(ctx context.Context, p imagePayload) ([]ai.ImageRef, *ai.ImageRef, error) {
	var refs []ai.ImageRef
	for _, item := range p.ContextPages {
		page, err := s.pages.GetPage(ctx, item.ID)
		if err != nil || page.ProjectID != p.Scope.ProjectID || page.Version != item.Version {
			return nil, nil, studio.ErrConflict
		}
		data, err := s.renderer.PNG(ctx, page, compose.Options{NoLettering: true, Scale: min(1, 768/float64(page.Width), 1024/float64(page.Height))})
		if err != nil {
			return nil, nil, err
		}
		refs = append(refs, ai.ImageRef{Data: data, ContentType: "image/png", Role: "reference", Label: "Neighbouring page: setting and lighting only"})
	}
	if len(p.Pose) == 0 {
		return refs, nil, nil
	}
	pose, err := pagedoc.DecodePose(p.Pose)
	if err != nil {
		return nil, nil, studio.ErrInvalid
	}
	im := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	draw.Draw(im, im.Bounds(), image.Black, image.Point{}, draw.Src)
	// Conventional COCO-18 ControlNet OpenPose colours on black. This is a
	// machine control image, not artwork or a UI palette.
	points := append([]pagedoc.Point(nil), pose.Joints[:8]...)
	hip := pose.Joints[8]
	points = append(points, pagedoc.Point{X: min(1, hip.X+.06), Y: hip.Y}, pose.Joints[9], pose.Joints[10], pagedoc.Point{X: max(0, hip.X-.06), Y: hip.Y}, pose.Joints[11], pose.Joints[12])
	edges := [][2]int{{1, 2}, {1, 5}, {2, 3}, {3, 4}, {5, 6}, {6, 7}, {1, 8}, {8, 9}, {9, 10}, {1, 11}, {11, 12}, {12, 13}, {1, 0}}
	colours := [][3]uint8{{255, 0, 0}, {255, 85, 0}, {255, 170, 0}, {255, 255, 0}, {170, 255, 0}, {85, 255, 0}, {0, 255, 0}, {0, 255, 85}, {0, 255, 170}, {0, 255, 255}, {0, 170, 255}, {0, 85, 255}, {0, 0, 255}, {85, 0, 255}}
	dot := func(x, y, r int, c color.Color) {
		for yy := y - r; yy <= y+r; yy++ {
			for xx := x - r; xx <= x+r; xx++ {
				if (xx-x)*(xx-x)+(yy-y)*(yy-y) <= r*r {
					im.Set(xx, yy, c)
				}
			}
		}
	}
	for i, edge := range edges {
		a, b := points[edge[0]], points[edge[1]]
		dx, dy := (b.X-a.X)*511, (b.Y-a.Y)*511
		steps := max(1, int(math.Ceil(math.Hypot(dx, dy))))
		rgb := colours[i]
		for n := 0; n <= steps; n++ {
			f := float64(n) / float64(steps)
			dot(int(a.X*511+dx*f), int(a.Y*511+dy*f), 4, color.NRGBA{R: uint8(int(rgb[0]) * 3 / 5), G: uint8(int(rgb[1]) * 3 / 5), B: uint8(int(rgb[2]) * 3 / 5), A: 255})
		}
	}
	for i, pt := range points {
		rgb := colours[i]
		dot(int(pt.X*511), int(pt.Y*511), 6, color.NRGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255})
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		return nil, nil, err
	}
	ref := &ai.ImageRef{Data: out.Bytes(), ContentType: "image/png", Role: "pose", Label: "Pose skeleton, not identity"}
	return refs, ref, nil
}

func (s *service) sketchCrop(ctx context.Context, p imagePayload) ([]byte, error) {
	if !db.ValidID(p.SketchAssetID) {
		return nil, studio.ErrInvalid
	}
	data, _, err := s.readOwned(ctx, p.Scope.ProjectID, p.SketchAssetID)
	if err != nil {
		return nil, err
	}
	im, err := decodePixels(data)
	if err != nil {
		return nil, err
	}
	page, err := s.pages.GetPage(ctx, p.PageID)
	if err != nil {
		return nil, err
	}
	pn := page.Panel(p.PanelID)
	if pn == nil {
		return nil, studio.ErrInvalid
	}
	b := pagedoc.Bounds(pn.Points)
	sx, sy := float64(im.Bounds().Dx())/float64(page.Width), float64(im.Bounds().Dy())/float64(page.Height)
	crop := image.Rect(int(math.Floor(b.X*sx)), int(math.Floor(b.Y*sy)), int(math.Ceil((b.X+b.W)*sx)), int(math.Ceil((b.Y+b.H)*sy))).Intersect(im.Bounds())
	if crop.Empty() {
		return nil, studio.ErrInvalid
	}
	out := image.NewNRGBA(image.Rect(0, 0, p.Plan.Needs.Width, p.Plan.Needs.Height))
	draw.CatmullRom.Scale(out, out.Bounds(), im, crop, draw.Src, nil)
	var pngData bytes.Buffer
	err = png.Encode(&pngData, out)
	return pngData.Bytes(), err
}
