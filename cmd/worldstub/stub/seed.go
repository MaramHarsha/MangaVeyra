package stub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"time"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// Demo content. Every name here is original sample content from the
// MangaVeyra design (Tidebreak Courier and friends); artwork is generated
// screentone placeholders and LoRA files are tiny placeholder weights.

type seedAccount struct {
	id, display, handle, bio string
	links                    []string
	trust                    int
	verified, developer      bool
	prevHandle               string
	serverName               string
}

var seedAccounts = []seedAccount{
	{id: "acc_jerry", display: "Jerry Ink", handle: "jerry-ink", bio: "Writes Tidebreak Courier. Shares characters under CC BY.",
		links: []string{"jerryink.art"}, trust: 3, verified: true, developer: true, prevHandle: "jerry", serverName: "studio.local"},
	{id: "acc_linpark", display: "lin.park", handle: "lin-park", bio: "Layouts and lighthouse people.", trust: 2, verified: true, serverName: "lin.home.arpa"},
	{id: "acc_aiko", display: "aiko", handle: "aiko", bio: "Iron Koi and Glass Orchard.", trust: 2, verified: true, serverName: "koi.studio.lan"},
	{id: "acc_kestrel", display: "kestrel", handle: "kestrel", bio: "Plugins and radio foxes.", trust: 1, developer: true, serverName: "kestrel.dev.lan"},
}

type seedItem struct {
	id, packID, name, creator string
	kind                      mvpack.Kind
	licence                   string
	ai                        mvpack.AILabel
	rating                    string
	summary, description      string
	tags, worksWith           []string
	paid                      bool
	uses                      int
	ratingAvg                 float64
	ratingCount               int
	versions                  []string
	fromSeries, seriesURL     string
	shade                     uint8
}

var seedItems = []seedItem{
	{id: "wi_rin_asagiri", packID: "rin-asagiri", name: "Rin Asagiri", creator: "jerry-ink", kind: mvpack.KindCharacter,
		licence: "CC-BY-4.0", ai: mvpack.AILabel{Art: mvpack.AIAssisted, Design: mvpack.AIHuman}, rating: mvpack.RatingTeen,
		summary:     "19-year-old courier in a long coat with a satchel.",
		description: "Reference sheet with front, side and face views and a LoRA trained for Illustrious XL.",
		tags:        []string{"courier", "tidebreak-courier", "hero"}, worksWith: []string{"illustrious-xl"},
		uses: 1204, ratingAvg: 4.8, ratingCount: 212, versions: []string{"1.0.0", "1.1.0", "1.2.0"}, fromSeries: "Tidebreak Courier", shade: 30},
	{id: "wi_lantern_warden", packID: "lantern-warden", name: "Lantern Warden", creator: "lin-park", kind: mvpack.KindCharacter,
		licence: "CC0-1.0", ai: mvpack.AILabel{Art: mvpack.AIAssisted, Design: mvpack.AIHuman},
		summary: "A lamplighter who guards the harbour's last lighthouse.", tags: []string{"lighthouse", "guardian"},
		worksWith: []string{"flux2-klein"}, uses: 860, ratingAvg: 4.6, ratingCount: 97, versions: []string{"1.0.0"}, shade: 60},
	{id: "wi_iron_koi", packID: "iron-koi", name: "Iron Koi", creator: "aiko", kind: mvpack.KindCharacter,
		licence: "personal", ai: mvpack.AILabel{Art: mvpack.AIGenerated, Design: mvpack.AIHuman}, rating: mvpack.RatingTeen,
		summary: "Armoured koi spirit who swims through city canals.", tags: []string{"spirit", "armour"},
		paid: true, uses: 431, ratingAvg: 4.7, ratingCount: 58, versions: []string{"1.0.0"}, fromSeries: "Iron Koi", shade: 90},
	{id: "wi_signal_fox", packID: "signal-fox", name: "Signal Fox", creator: "kestrel", kind: mvpack.KindCharacter,
		licence: "CC-BY-NC-4.0", ai: mvpack.AILabel{Art: mvpack.AIAssisted, Design: mvpack.AIHuman},
		summary: "Radio-operator fox from Low Tide Radio.", tags: []string{"fox", "radio"},
		paid: true, uses: 377, ratingAvg: 4.5, ratingCount: 41, versions: []string{"1.2.0", "1.3.0"}, fromSeries: "Low Tide Radio", shade: 120},
	{id: "wi_pip_the_gull", packID: "pip-the-gull", name: "Pip the Gull", creator: "jerry-ink", kind: mvpack.KindCharacter,
		licence: "CC-BY-4.0", ai: mvpack.AILabel{Art: mvpack.AIHuman, Design: mvpack.AIHuman},
		summary: "The harbour gull who steals Rin's lunch.", tags: []string{"animal", "tidebreak-courier"},
		uses: 512, ratingAvg: 4.9, ratingCount: 76, versions: []string{"1.0.0"}, fromSeries: "Tidebreak Courier", shade: 150},
	{id: "wi_night_market", packID: "night-market-layout", name: "Night Market layout", creator: "lin-park", kind: mvpack.KindLayout,
		licence: "CC-BY-4.0", ai: mvpack.AILabel{Design: mvpack.AIHuman},
		summary: "Five-panel night market spread with a wide establishing shot.", tags: []string{"layout", "night"},
		uses: 690, versions: []string{"1.0.0"}, shade: 45},
	{id: "wi_rooftop_chase", packID: "rooftop-chase", name: "Rooftop chase", creator: "jerry-ink", kind: mvpack.KindTemplate,
		licence: "CC-BY-4.0", ai: mvpack.AILabel{Design: mvpack.AIHuman, Script: mvpack.AIHuman},
		summary: "Eight-page chase template with beat notes.", tags: []string{"action", "chase"},
		uses: 214, versions: []string{"1.0.0"}, shade: 75},
	{id: "wi_salt_ink", packID: "salt-ink", name: "Salt ink", creator: "jerry-ink", kind: mvpack.KindStyle,
		licence: "CC-BY-SA-4.0", ai: mvpack.AILabel{Art: mvpack.AIHuman},
		summary: "Heavy brush ink with coarse screentone.", tags: []string{"ink", "screentone"},
		uses: 380, versions: []string{"1.0.0"}, shade: 10},
	{id: "wi_glass_orchard_wash", packID: "glass-orchard-wash", name: "Glass Orchard wash", creator: "aiko", kind: mvpack.KindStyle,
		licence: "CC-BY-NC-4.0", ai: mvpack.AILabel{Art: mvpack.AIGenerated},
		summary: "Soft grey wash used in Glass Orchard.", tags: []string{"wash", "soft"}, worksWith: []string{"qwen-image-edit"},
		paid: true, uses: 129, versions: []string{"1.0.0"}, shade: 170},
	{id: "wi_tidebreak_courier", packID: "tidebreak-courier", name: "Tidebreak Courier", creator: "jerry-ink", kind: mvpack.KindSeriesListing,
		licence: "all-rights-reserved", ai: mvpack.AILabel{Script: mvpack.AIHuman, Art: mvpack.AIAssisted, Lettering: mvpack.AIHuman},
		rating: mvpack.RatingTeen, summary: "A courier races the tide across a drowned harbour city.", tags: []string{"adventure", "ongoing"},
		uses: 2310, versions: []string{"1.0.0"}, seriesURL: "https://studio.local/series/tidebreak-courier", shade: 20},
	{id: "wi_harbour_ghosts_theme", packID: "harbour-ghosts-theme", name: "Harbour Ghosts theme", creator: "kestrel", kind: mvpack.KindPlugin,
		licence: "MIT", ai: mvpack.AILabel{Design: mvpack.AIHuman},
		summary: "A misty teal reader theme.", tags: []string{"theme", "reader"}, uses: 301, versions: []string{"0.2.0"}, shade: 100},
}

// seedLibrary lists what the demo account (Jerry Ink) owns, matching the My
// World library screen: free claims, paid items obtained on World, own items.
var seedLibrary = []struct {
	item, acquisition, version string
	daysAgo                    int
}{
	{"wi_lantern_warden", "free", "", 20},
	{"wi_iron_koi", "paid", "", 15},
	{"wi_signal_fox", "paid", "1.2.0", 30},
	{"wi_night_market", "free", "", 3},
}

// seedServerKey derives the fixed, PUBLICLY KNOWN signing key of a demo
// creator's server. Never use it outside local development.
func seedServerKey(handle string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("mangaveyra worldstub DEV ONLY seed server key: " + handle))
	return ed25519.NewKeyFromSeed(seed[:])
}

func (s *Server) seed() error {
	now := s.opts.Now().UTC().Truncate(time.Second)
	serverKeyIDs := map[string]string{}
	for _, sa := range seedAccounts {
		a := &account{
			ID: sa.id, DisplayName: sa.display, Bio: sa.bio, Links: sa.links, TrustLevel: sa.trust,
			Verified: sa.verified, Developer: sa.developer, TwoFactor: true, CreatedAt: now.AddDate(0, -8, 0),
		}
		s.accounts[a.ID] = a
		if sa.prevHandle != "" {
			// The demo account renamed itself yesterday, so its old handle is
			// reserved and redirects, and the next change is 30 days away.
			a.Handle, a.HandleChangedAt = sa.prevHandle, now.AddDate(0, 0, -21)
			s.handles[a.Handle] = a.ID
			s.setHandle(a, sa.handle, now.AddDate(0, 0, -1))
		} else {
			a.Handle, a.HandleChangedAt = sa.handle, a.CreatedAt
			s.handles[a.Handle] = a.ID
		}
		k := s.addServerKey(a.ID, sa.serverName, "1.0.0", seedServerKey(sa.handle).Public().(ed25519.PublicKey), a.CreatedAt)
		serverKeyIDs[sa.handle] = k.ID
	}
	for i, si := range seedItems {
		a := s.findAccountByHandle(si.creator)
		it := &item{ID: si.id, CreatorID: a.ID, Paid: si.paid, Uses: si.uses, RatingAvg: si.ratingAvg, RatingCount: si.ratingCount,
			FromSeries: si.fromSeries, ApprovedAt: now.AddDate(0, 0, -60+i)}
		signer := &mvpack.Signer{KeyID: serverKeyIDs[si.creator], Key: seedServerKey(si.creator), Role: mvpack.SignerServer}
		for j, ver := range si.versions {
			released := now.AddDate(0, 0, -40+i+j*5)
			signer.Now = func() time.Time { return released }
			pack, m, err := buildSeedPack(si, a, ver, released, signer)
			if err != nil {
				return fmt.Errorf("worldstub: seed %s %s: %w", si.id, ver, err)
			}
			if _, err := s.publishVersion(it, m, pack, released); err != nil {
				return fmt.Errorf("worldstub: seed %s %s: %w", si.id, ver, err)
			}
		}
		s.items[it.ID] = it
		s.itemOrder = append(s.itemOrder, it.ID)
		a.ApprovedItems++
		s.addToLibrary(a.ID, it, "creator", it.ApprovedAt, it.latest().Version)
	}
	jerry := s.findAccountByHandle("jerry-ink")
	for _, l := range seedLibrary {
		s.addToLibrary(jerry.ID, s.items[l.item], l.acquisition, now.AddDate(0, 0, -l.daysAgo), l.version)
	}
	return nil
}

// buildSeedPack writes a small but complete, signed pack for a seed item.
func buildSeedPack(si seedItem, creator *account, version string, created time.Time, signer *mvpack.Signer) ([]byte, *mvpack.Manifest, error) {
	m := mvpack.Manifest{
		Kind: si.kind, ID: si.packID, Name: si.name, Version: version, Summary: si.summary, Description: si.description,
		CreatedAt: created, Licence: si.licence, AILabel: si.ai, ContentRating: si.rating,
		Credit: mvpack.Credit{DisplayName: creator.DisplayName, Handle: creator.Handle, WorldItemID: si.id},
		Tags:   si.tags, WorksWith: si.worksWith,
		Extensions: map[string]json.RawMessage{"x-worldstub": json.RawMessage(`{"placeholder":true}`)},
	}
	if si.kind == mvpack.KindCharacter {
		base := ""
		if len(si.worksWith) > 0 {
			base = si.worksWith[0]
		}
		m.Character = &mvpack.CharacterInfo{FromSeries: si.fromSeries, TriggerWord: slugify(si.name), BaseModel: base}
	}
	if si.kind == mvpack.KindSeriesListing {
		m.SeriesListing = &mvpack.SeriesListing{URL: si.seriesURL, Languages: []string{"en", "ja"}, Status: "ongoing", Chapters: 12}
	}
	if si.kind == mvpack.KindPlugin {
		m.Plugin = &mvpack.PluginRef{Manifest: "plugin.json"}
	}
	var buf bytes.Buffer
	w, err := mvpack.NewWriter(&buf, m)
	if err != nil {
		return nil, nil, err
	}
	add := func(p string, role mvpack.Role, data []byte) {
		if err == nil {
			err = w.AddBytes(p, role, data)
		}
	}
	add("cover.png", mvpack.RoleCover, placeholderPNG(96, 128, si.shade, 0))
	switch si.kind {
	case mvpack.KindCharacter:
		add("refs/front.png", mvpack.RoleReference, placeholderPNG(96, 128, si.shade, 1))
		add("refs/side.png", mvpack.RoleReference, placeholderPNG(96, 128, si.shade, 2))
		add("refs/face.png", mvpack.RoleReference, placeholderPNG(96, 96, si.shade, 3))
		add("lora/"+si.packID+".safetensors", mvpack.RoleLoRA, placeholderSafetensors(slugify(si.name)))
		add("profile.json", mvpack.RoleProfile, mustJSON(map[string]any{
			"name": si.name, "summary": si.summary, "from_series": si.fromSeries,
			"personality": "Placeholder profile from the development World stub.",
		}))
	case mvpack.KindLayout:
		add("layout.json", mvpack.RoleLayout, []byte(`{"page":{"w":1600,"h":2400},"panels":[`+
			`{"order":1,"points":[[0,0],[1600,0],[1600,900],[0,760]]},{"order":2,"points":[[0,780],[800,900],[800,1650],[0,1650]]},`+
			`{"order":3,"points":[[820,905],[1600,920],[1600,1650],[820,1650]]},{"order":4,"points":[[0,1670],[1000,1670],[1000,2400],[0,2400]]},`+
			`{"order":5,"points":[[1020,1670],[1600,1670],[1600,2400],[1020,2400]]}]}`))
	case mvpack.KindTemplate:
		add("template.json", mvpack.RoleTemplate, mustJSON(map[string]any{
			"pages": 8, "beats": []string{"Spot the thief", "First leap", "Near miss", "Dead end", "Reversal", "Crowd scatter", "Catch", "Aftermath"},
		}))
	case mvpack.KindStyle:
		add("preview/sample.png", mvpack.RolePreview, placeholderPNG(128, 96, si.shade, 4))
		add("style.json", mvpack.RoleStylePreset, mustJSON(map[string]any{"finish": "bw_screentone", "line_weight": 1.4, "screentone": "coarse-dot"}))
	case mvpack.KindSeriesListing:
		add("series.json", mvpack.RoleSeries, mustJSON(map[string]any{"title": si.name, "chapters": 12, "url": si.seriesURL}))
	case mvpack.KindPlugin:
		add("plugin.json", mvpack.RolePluginManifest, mustJSON(map[string]any{
			"mvplugin": 1, "id": creator.Handle + "." + si.packID, "name": si.name, "version": version, "kind": "theme",
			"licence": si.licence, "min_app_version": "1.0.0", "permissions": []any{},
			"entry":  map[string]any{"type": "manifest", "contributes": map[string]any{"themes": []string{"themes/harbour-ghosts.json"}}},
			"source": map[string]any{"repo": "https://example.org/" + creator.Handle + "/" + si.packID, "tag": "v" + version, "signed_tag": true},
		}))
		add("themes/harbour-ghosts.json", mvpack.RoleTheme, mustJSON(map[string]any{"name": si.name, "tokens": map[string]string{"--mv-bg": "#061417", "--mv-accent": "#3DB8B0"}}))
	}
	if err != nil {
		return nil, nil, err
	}
	final, err := w.Close(signer)
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), final, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// placeholderPNG draws a labelled-screentone style placeholder: a dot screen
// whose density depends on shade, with a diagonal speed-line band that varies
// by variant so every view of an item looks different.
func placeholderPNG(w, h int, shade uint8, variant int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	period := 3 + int(shade)%5
	for y := range h {
		for x := range w {
			v := uint8(236)
			if (x%period == 0 && y%period == 0) || (x+y+variant*7)%(24+variant) < 2 {
				v = shade / 2
			}
			if x < 2 || y < 2 || x >= w-2 || y >= h-2 {
				v = 16 // panel border
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// placeholderSafetensors returns a tiny, structurally valid .safetensors file.
// It is NOT a usable LoRA.
func placeholderSafetensors(trigger string) []byte {
	hdr := mustJSON(map[string]any{
		"__metadata__":         map[string]string{"note": "placeholder weights from the development World stub", "trigger": trigger},
		"lora_unet_down.alpha": map[string]any{"dtype": "F32", "shape": []int{1}, "data_offsets": []int{0, 4}},
	})
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(hdr)))
	b.Write(hdr)
	b.Write([]byte{0, 0, 0x80, 0x3f}) // float32 1.0
	return b.Bytes()
}
