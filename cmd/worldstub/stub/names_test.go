package stub

import (
	"testing"

	"github.com/MaramHarsha/MangaVeyra/sdk/mvpack"
)

// Reserved names are also blocked in look-alike spellings and as a part of a
// handle (screen WA9: "Adm1n_Official" is blocked as "admin").
func TestReservedHandleLookalikes(t *testing.T) {
	h := newHarness(t, Options{Seed: true})
	for handle, want := range map[string]string{
		"adm1n":               "reserved",
		"w0rld":               "reserved",
		"mangaveyr4":          "reserved",
		"adm1n-official":      "reserved",
		"official-jerry":      "reserved",
		"trust-and-5afety":    "reserved",
		"st4ff":               "reserved",
		"captain-p1aceh0ld":   "", // not a full match of a blocked name
		"capta1n-placeh0lder": "blocked",
		"worldbuilder":        "", // containing a reserved word inside a part is fine
		"harbour-admiral":     "",
	} {
		m := h.get("/v1/handles/"+handle, "").json(t)
		reason, _ := m["reason"].(string)
		if reason != want || (want == "") != (m["available"] == true) {
			t.Errorf("%s: available=%v reason=%q, want reason %q", handle, m["available"], reason, want)
		}
	}
	// Suggestions never offer a reserved spelling either.
	if isReserved("fresh-name") || !isReserved("0fficial") {
		t.Error("isReserved")
	}
	// Changing to a look-alike is rejected with the reserved code.
	tok := h.link("new")
	r := h.sendJSON("PATCH", "/v1/me/profile", tok, map[string]any{"display_name": "Mika Tern", "handle": "adm1n"})
	if r.status != 422 || r.errorCode(t) != "validation_failed" {
		t.Fatalf("look-alike handle accepted: %d %s", r.status, r.body)
	}
}

// A server key registered to the account cannot pass off its signature as a
// World signature: the submission's signature check fails.
func TestSubmissionKeyCannotClaimWorldRole(t *testing.T) {
	h := newHarness(t, Options{Seed: true, CheckDelay: -1})
	tok := h.link("aiko")
	signer := h.registerKey(tok, "aiko-pc")
	signer.Role = mvpack.SignerWorld
	r := h.submit(tok, buildPack(t, "Heron Courier", "aiko", "aiko", "1.0.0", signer))
	wantStatus(t, r, 202)
	if got := checkStatuses(t, r.json(t))["signature"]; got != "failed" {
		t.Fatalf("server key signing as world: signature check %q (%s)", got, r.body)
	}
	signer.Role = mvpack.SignerServer
	r = h.submit(tok, buildPack(t, "Heron Courier Two", "aiko", "aiko", "1.0.0", signer))
	wantStatus(t, r, 202)
	if got := checkStatuses(t, r.json(t))["signature"]; got != "passed" {
		t.Fatalf("honest server signature: %q (%s)", got, r.body)
	}
}
