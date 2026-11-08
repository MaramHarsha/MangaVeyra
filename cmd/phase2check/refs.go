package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"strconv"
	"strings"

	"github.com/MaramHarsha/MangaVeyra/internal/assets"
	"github.com/MaramHarsha/MangaVeyra/internal/core"
	"github.com/MaramHarsha/MangaVeyra/internal/studio"
)

// Curated crops preserve the original generated pixels and AI provenance.
// Coordinates must be chosen after inspecting this run's actual kept sheet.
func (h *harness) curate(crops string) error {
	if h.s.Curated {
		return errors.New("individual references were already curated; refusing duplicates")
	}
	c, err := h.studio.GetCharacter(h.ctx, h.s.CharacterID)
	if err != nil {
		return err
	}
	if len(c.Refs) != 1 || c.Refs[0].Kind != "sheet" {
		return errors.New("expected one inspected kept sheet")
	}
	as := core.MustUse[*assets.Service](h.a.Deps)
	data, _, err := as.Read(h.ctx, c.Refs[0].AssetID, assets.MaxBytes)
	if err != nil {
		return err
	}
	sheet, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	bible := core.MustUse[studio.BibleService](h.a.Deps)
	parts := strings.Split(crops, ";")
	if len(parts) != 3 {
		return errors.New("provide exactly front, side and back crops after inspecting the sheet")
	}
	seen := map[string]bool{}
	for _, part := range parts {
		kind, coords, ok := strings.Cut(part, ":")
		if !ok || seen[kind] || kind != "front" && kind != "side" && kind != "back" {
			return errors.New("invalid reference crop kind")
		}
		seen[kind] = true
		var n []int
		for _, v := range strings.Split(coords, ",") {
			x, e := strconv.Atoi(v)
			if e != nil {
				return e
			}
			n = append(n, x)
		}
		if len(n) != 4 || n[2] < 64 || n[3] < 64 {
			return errors.New("invalid reference crop bounds")
		}
		rect := image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3])
		if !rect.In(sheet.Bounds()) {
			return errors.New("reference crop exceeds the sheet")
		}
		im := image.NewNRGBA(image.Rect(0, 0, n[2], n[3]))
		draw.Draw(im, im.Bounds(), sheet, rect.Min, draw.Src)
		var buf bytes.Buffer
		if err = png.Encode(&buf, im); err != nil {
			return err
		}
		a, err := as.Save(h.ctx, assets.SaveParams{OwnerID: h.s.UserID, ProjectID: h.s.ProjectID, Kind: "character_ref", Source: assets.SourceAI, Data: buf.Bytes(), Meta: map[string]any{"derived_from": c.Refs[0].AssetID, "crop": n, "source_job": h.s.Jobs["reference-sheet"], "curation": "manually inspected original sheet pixels"}})
		if err != nil {
			return err
		}
		c, err = h.studio.GetCharacter(h.ctx, c.ID)
		if err != nil {
			return err
		}
		if _, err = bible.AddCharacterRef(h.ctx, h.actor, studio.NewCharacterRef{ProjectID: h.s.ProjectID, CharacterID: c.ID, AssetID: a.ID, Kind: kind, Label: fmt.Sprintf("Kept %s view · original sheet crop", kind), ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
			return err
		}
		if kind == "front" {
			c, err = h.studio.GetCharacter(h.ctx, c.ID)
			if err != nil {
				return err
			}
			if _, err = bible.CreateOutfit(h.ctx, h.actor, studio.NewOutfit{ProjectID: h.s.ProjectID, CharacterID: c.ID, Name: "Tide courier", Description: "Dark coat with white V-shaped lapels, striped scarf, white gloves, dark trousers, ankle boots and cross-body satchel with a pale round clasp.", AssetID: a.ID, IsDefault: true, ExpectedUpdatedAt: c.UpdatedAt}); err != nil {
				return err
			}
		}
	}
	h.s.Curated = true
	return h.save()
}
