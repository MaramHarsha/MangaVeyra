package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/gen"
	"github.com/MaramHarsha/MangaVeyra/internal/jobs"
	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

func (h *harness) region() error {
	if len(h.s.HandRect) != 4 {
		return errors.New("inspect the actual page-7 character and provide --hand-rect x,y,width,height")
	}
	before, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	if len(before) != 30 {
		return errors.New("region acceptance needs the complete 30-page chapter")
	}
	baseline := filepath.Join(h.root, "before-region.json")
	if h.s.Jobs["page7-hand"] == "" {
		b, _ := json.Marshal(before)
		if err = os.WriteFile(baseline, b, 0600); err != nil {
			return err
		}
	} else {
		b, e := os.ReadFile(baseline)
		if e != nil {
			return e
		}
		if err = json.Unmarshal(b, &before); err != nil {
			return err
		}
	}
	p := before[6]
	pn := p.Panels[0]
	l := pn.Layers[1]
	if l.Asset == nil {
		return errors.New("page 7 has no generated character")
	}
	as := core.MustUse[*assets.Service](h.a.Deps)
	original, _, err := as.Read(h.ctx, l.Asset.ID, assets.MaxBytes)
	if err != nil {
		return err
	}
	im, err := png.Decode(bytes.NewReader(original))
	if err != nil {
		return err
	}
	r := h.s.HandRect
	rect := image.Rect(r[0], r[1], r[0]+r[2], r[1]+r[3])
	if r[2] < 1 || r[3] < 1 || !rect.In(im.Bounds()) {
		return errors.New("hand rectangle is outside the original character image")
	}
	mask := image.NewNRGBA(im.Bounds())
	for y := range im.Bounds().Dy() {
		for x := range im.Bounds().Dx() {
			c := color.NRGBA{A: 255}
			if image.Pt(x, y).In(rect) {
				c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			mask.SetNRGBA(x, y, c)
		}
	}
	var data bytes.Buffer
	if err = png.Encode(&data, mask); err != nil {
		return err
	}
	_, err = h.step("page7-hand", func() (*jobs.Job, error) {
		a, e := as.Save(h.ctx, assets.SaveParams{OwnerID: h.s.UserID, ProjectID: h.s.ProjectID, Kind: "mask", Source: assets.SourceSystem, Data: data.Bytes()})
		if e != nil {
			return nil, e
		}
		return h.gen.EnqueueRegionEdit(h.ctx, h.actor, gen.RegionEditRequest{ProjectID: h.s.ProjectID, LayerID: l.ID, MaskAssetID: a.ID, Prompt: "Correct only this extended right hand. Draw a natural original manga hand with five anatomically correct gloved fingers, an open palm and clean black ink. Preserve the same white glove and line weight. Keep the wrist, coat, face, hair, clothes and every unmasked pixel unchanged.", Variations: 1, RoutingMode: "best", ExpectedVersion: p.Version})
	})
	if err != nil {
		return err
	}
	after, err := h.pages.ChapterPages(h.ctx, h.s.ChapterID)
	if err != nil {
		return err
	}
	unchanged := 0
	hashes := map[string]string{}
	for i := range before {
		if i != 6 {
			if !wireEqual(before[i], after[i]) {
				return fmt.Errorf("page %d changed outside scope", i+1)
			}
			unchanged++
		}
		for _, panel := range before[i].Panels {
			for _, layer := range panel.Layers {
				if layer.Asset == nil || layer.ID == l.ID {
					continue
				}
				b, a, e := as.Read(h.ctx, layer.Asset.ID, assets.MaxBytes)
				if e != nil {
					return e
				}
				digest := fmt.Sprintf("%x", sha256.Sum256(b))
				if digest != a.SHA256 {
					return errors.New("unrelated asset checksum differs")
				}
				hashes[a.ID] = digest
			}
		}
	}
	newLayer := after[6].Panels[0].Layers[1]
	protected := after[6]
	// Restore only the explicitly allowed target fields before comparing the
	// rest of the page, including every other layer's metadata and lettering.
	protected.Panels = append([]pagedoc.Panel(nil), protected.Panels...)
	protected.Panels[0].Layers = append([]pagedoc.Layer(nil), protected.Panels[0].Layers...)
	protected.Panels[0].Layers[1] = l
	protected.Version = p.Version
	protected.Thumb = p.Thumb
	if !wireEqual(protected, p) {
		return errors.New("protected page-7 document content changed")
	}
	if newLayer.Asset == nil || newLayer.Asset.ID == l.Asset.ID {
		return errors.New("the hand correction did not produce a new owned asset")
	}
	pixels, _, err := as.Read(h.ctx, newLayer.Asset.ID, assets.MaxBytes)
	if err != nil {
		return err
	}
	edited, err := png.Decode(bytes.NewReader(pixels))
	if err != nil {
		return err
	}
	if edited.Bounds() != im.Bounds() {
		return errors.New("edited dimensions changed")
	}
	outside, changed := 0, 0
	for y := range im.Bounds().Dy() {
		for x := range im.Bounds().Dx() {
			same := color.NRGBAModel.Convert(im.At(x, y)) == color.NRGBAModel.Convert(edited.At(x, y))
			if !image.Pt(x, y).In(rect) {
				if !same {
					return errors.New("pixel outside hand mask changed")
				}
				outside++
			} else if !same {
				changed++
			}
		}
	}
	if changed == 0 {
		return errors.New("no hand pixels changed")
	}
	// Revert is exercised by the browser flow; record the exact before/after
	// snapshots here without silently reverting the approved visible result.
	versions, err := h.pages.Versions(h.ctx, p.ID, 50)
	if err != nil {
		return err
	}
	hasBefore, hasAfter := false, false
	for _, v := range versions {
		doc, e := h.pages.VersionDoc(h.ctx, v.ID)
		if e != nil {
			return e
		}
		hasBefore = hasBefore || wireEqual(doc, p)
		hasAfter = hasAfter || wireEqual(doc, after[6])
	}
	if !hasBefore || !hasAfter {
		return errors.New("visual history lacks the actual before/after snapshots")
	}
	verdict := map[string]any{"other_pages_unchanged": unchanged, "unrelated_asset_checksums": hashes, "outside_pixels_equal": outside, "inside_pixels_changed": changed, "mask_rect": r, "before_asset": l.Asset.ID, "after_asset": newLayer.Asset.ID, "before_snapshot": hasBefore, "after_snapshot": hasAfter, "lettering_unchanged": wireEqual(p.Bubbles, after[6].Bubbles)}
	b, _ := json.MarshalIndent(verdict, "", "  ")
	if err = os.WriteFile(filepath.Join(h.root, "region-result.json"), b, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(h.root, "page7-before-character.png"), original, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(h.root, "page7-after-character.png"), pixels, 0600); err != nil {
		return err
	}
	fmt.Printf("Hand correction: %d other pages unchanged, %d identical outside pixels, %d changed inside pixels\n", unchanged, outside, changed)
	return nil
}

// Page snapshots omit empty optional maps/slices. Compare their canonical wire
// documents, so a resumed run treats nil and omitted-empty values identically.
func wireEqual(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}
