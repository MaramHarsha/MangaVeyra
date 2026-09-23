// SPDX-License-Identifier: MIT
// Copyright (c) 2026 MangaVeyra contributors

package mvpack

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Member names that differ only in case would be merged by encoding/json (last
// one wins) while canonicalization — and so the signature — keeps both: a
// parser differential on the honest-provenance label.
func TestParseManifestRejectsCaseVariantMembers(t *testing.T) {
	m := baseManifest(KindCharacter)
	m.Files = []File{{Path: "refs/a.png", Role: RoleReference, MediaType: "image/png", Size: 1, SHA256: strings.Repeat("a", 64)}}
	b, _ := json.Marshal(m)
	if !bytes.Contains(b, []byte(`"ai_label":{`)) {
		t.Fatalf("fixture changed: %s", b)
	}
	for name, doc := range map[string][]byte{
		"nested case variant": bytes.Replace(b, []byte(`"ai_label":{`), []byte(`"ai_label":{"ART":"ai",`), 1),
		"nested lone variant": bytes.Replace(b, []byte(`"path":"refs/a.png"`), []byte(`"Path":"refs/a.png"`), 1),
	} {
		if _, err := ParseManifest(doc); !errors.Is(err, ErrInvalidManifest) || !strings.Contains(err.Error(), "snake case") {
			t.Errorf("%s: want ErrInvalidManifest (member names), got %v", name, err)
		}
	}
	// Extension members may use any spelling inside.
	ext := bytes.Replace(b, []byte(`{"mvpack"`), []byte(`{"x-Studio":{"Tool":"Kestrel Ink","tool":2},"mvpack"`), 1)
	if _, err := ParseManifest(ext); err != nil {
		t.Fatalf("extension member: %v", err)
	}
}

func TestParseSignatureFileStrictness(t *testing.T) {
	p, err := openBytes(t, buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer)), nil)
	if err != nil {
		t.Fatal(err)
	}
	good, _ := json.Marshal(p.Signature())
	if _, err := ParseSignatureFile(good); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	variant := bytes.Replace(good, []byte(`"manifest_sha256":`), []byte(`"MANIFEST_SHA256":"`+strings.Repeat("0", 64)+`","manifest_sha256":`), 1)
	if _, err := ParseSignatureFile(variant); !errors.Is(err, ErrBadSignature) {
		t.Errorf("case variant: want ErrBadSignature, got %v", err)
	}
	stamp := fixedTime.Format("2006-01-02T15:04:05Z07:00")
	for name, repl := range map[string]string{
		"fractional seconds": strings.TrimSuffix(stamp, "Z") + ".5Z",
		"non-UTC offset":     fixedTime.Add(9*3600e9).Format("2006-01-02T15:04:05") + "+09:00",
	} {
		doc := bytes.Replace(good, []byte(stamp), []byte(repl), 1)
		if bytes.Equal(doc, good) {
			t.Fatalf("%s: fixture has no %s", name, stamp)
		}
		if _, err := ParseSignatureFile(doc); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: want ErrBadSignature, got %v", name, err)
		}
	}
}

// A trusted server key that labels its signature "world" must not satisfy
// RequireSigner: SignerWorld when the resolver knows key roles.
func TestRoleKeySetBindsSignerRoles(t *testing.T) {
	impostor := buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerWorld))
	p, err := openBytes(t, impostor, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverPub := testKey("server").Public().(ed25519.PublicKey)
	roles := RoleKeySet{"srv_7b1d": {Public: serverPub, Role: SignerServer}}
	if _, err := p.Verify(VerifyOptions{Keys: roles, RequireSigner: SignerWorld}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("server key claiming world: want ErrUntrustedKey, got %v", err)
	}
	// A plain KeySet trusts keys for any declared role (documented), so the role-aware set is what callers mixing roles use.
	if _, err := p.Verify(VerifyOptions{Keys: KeySet{"srv_7b1d": serverPub}, RequireSigner: SignerWorld}); err != nil {
		t.Fatalf("plain KeySet: %v", err)
	}
	// Honest signatures verify with roles bound.
	honest, err := openBytes(t, buildCharacterPack(t, testSigner("server", "srv_7b1d", SignerServer)), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := AddSignature(honest, &out, testSigner("world", "world_2026", SignerWorld)); err != nil {
		t.Fatal(err)
	}
	both, err := openBytes(t, out.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	roles["world_2026"] = RoleKey{Public: testKey("world").Public().(ed25519.PublicKey), Role: SignerWorld}
	res, err := both.Verify(VerifyOptions{Keys: roles, RequireSigner: SignerWorld, RequireTrusted: true})
	if err != nil || len(res.Signatures) != 2 {
		t.Fatalf("countersigned pack: %v %+v", err, res)
	}
}

func TestLicenceTokenNeedsWorldKeyAndStrictPayload(t *testing.T) {
	key := testKey("world-licence")
	claims := LicenceClaims{ItemID: "wi_iron_koi", AccountID: "acc_1", Handle: "jerry-ink", ItemVersion: "1.0.0",
		Licence: "personal", Acquisition: "paid", IssuedAt: fixedTime.Unix()}
	tok, err := SignLicenceToken(claims, "wlt_1", key)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	if _, err := ParseLicenceToken(tok, RoleKeySet{"wlt_1": {Public: pub, Role: SignerWorld}}); err != nil {
		t.Fatalf("world key: %v", err)
	}
	if _, err := ParseLicenceToken(tok, RoleKeySet{"wlt_1": {Public: pub, Role: SignerServer}}); !errors.Is(err, ErrLicenceToken) {
		t.Fatalf("server key: want ErrLicenceToken, got %v", err)
	}
	// A validly signed payload whose members differ only in case is refused (Go would read the last one).
	payload := []byte(`{"v":1,"kid":"wlt_1","item_id":"wi_a","ITEM_ID":"wi_b","account_id":"acc_1","handle":"jerry-ink",` +
		`"version":"1.0.0","licence":"personal","acquisition":"free","iat":1}`)
	signed := licenceTokenPrefix + b64url(payload)
	forged := signed + "." + b64url(ed25519.Sign(key, []byte(signed)))
	if _, err := ParseLicenceToken(forged, KeySet{"wlt_1": pub}); !errors.Is(err, ErrLicenceToken) {
		t.Fatalf("case-variant payload: want ErrLicenceToken, got %v", err)
	}
}

func TestCheckMemberNames(t *testing.T) {
	for doc, ok := range map[string]bool{
		`{"a_1":[{"b":{"c":null}}],"d":"X"}`: true,
		`[{"ok":1},{"Bad":2}]`:               false,
		`{"x-Any":{"Up":1},"k":1}`:           false, // extensions only with a prefix
		`{"a":1} {"b":2}`:                    false,
		`{"":1}`:                             false,
	} {
		if err := CheckMemberNames([]byte(doc), ""); (err == nil) != ok {
			t.Errorf("%s: ok=%v err=%v", doc, ok, err)
		}
	}
	if err := CheckMemberNames([]byte(`{"x-Any":{"Up":1},"k":1}`), "x-"); err != nil {
		t.Errorf("extension prefix: %v", err)
	}
	if err := CheckMemberNames([]byte(`{"k":{"x-Nested":1}}`), "x-"); err == nil {
		t.Error("the extension exemption applies to top-level members only")
	}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
