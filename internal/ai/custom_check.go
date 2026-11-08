package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// checkCustomConnection runs the non-generating test of a Hugging Face,
// ComfyUI or HTTP connection (G12 step 2 and G2 "Test connection"). A
// Hugging Face serverless test re-reads the provider's listed tariff and
// stores it in the connection, so the next reservation uses it. ComfyUI and
// HTTP servers are reached only through the private-network client, which
// refuses every request while the admin's setting is off.
func checkCustomConnection(ctx context.Context, store *providerStore, localDoer, hfDoer httpDoer, p dispatchProvider) (comfyTestResult, error) {
	var comfy comfyTestResult
	switch p.Kind {
	case providerKindHuggingFace:
		cfg, err := decodeHFConfig(p.Config)
		if err != nil {
			return comfy, errProviderInvalid
		}
		updated, testErr := testHF(ctx, hfDoer, p.BaseURL, p.Key, cfg)
		if updated.Priced != cfg.Priced || updated.InputNano != cfg.InputNano || updated.OutputNano != cfg.OutputNano || updated.ContextTokens != cfg.ContextTokens {
			if err := store.updateCustomConfig(context.WithoutCancel(ctx), p.ID, p.Kind, p.BaseURL, p.KeyEnc, updated); err != nil {
				return comfy, err
			}
		}
		return comfy, testErr
	case providerKindComfyUI:
		cfg, err := decodeComfyConfig(p.Config)
		if err != nil {
			return comfy, errProviderInvalid
		}
		return testComfy(ctx, comfyClient{doer: localDoer, base: p.BaseURL, key: p.Key}, cfg)
	case providerKindHTTP:
		cfg, err := decodeHTTPConfig(p.Config)
		if err != nil {
			return comfy, errProviderInvalid
		}
		return comfy, testHTTP(ctx, httpClient{doer: localDoer, base: p.BaseURL, key: p.Key}, cfg)
	}
	return comfy, errProviderInvalid
}

// updateCustomConfig replaces a custom connection's description for the key
// that was tested only (a replaced key keeps its own state).
func (s *providerStore) updateCustomConfig(ctx context.Context, id, kind, base, expectedKeyEnc string, cfg any) error {
	raw, err := json.Marshal(cfg)
	if err != nil || validateCustomConfig(kind, base, string(raw)) != nil {
		return errProviderInvalid
	}
	res, err := s.db.Exec(ctx, `UPDATE providers SET config = ?, updated_at = ? WHERE id = ? AND kind = ? AND key_enc = ?`,
		string(raw), s.timeNow(), id, kind, expectedKeyEnc)
	if err != nil {
		return fmt.Errorf("ai: update provider config: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errProviderUnavailable
	}
	return nil
}

// customFingerprint identifies what a connection test covered: the kind, the
// endpoint, the sealed key and the parts of the description that decide what
// is sent (the workflow and its input map, the request template and reply
// paths, the Hugging Face endpoint, task and model, a local model). Routing
// modes, image limits, token budgets, the licence answer and a listed tariff
// are left out: they change what routing may ask, not where requests go or
// what they carry. An unreadable description has no fingerprint.
func customFingerprint(kind, baseURL, keyEnc, config string) string {
	var target any
	switch kind {
	case providerKindComfyUI:
		c, err := decodeComfyConfig(config)
		if err != nil {
			return ""
		}
		target = struct {
			Workflow json.RawMessage `json:"workflow"`
			Map      comfyMap        `json:"map"`
		}{c.Workflow, c.Map}
	case providerKindHTTP:
		c, err := decodeHTTPConfig(config)
		if err != nil {
			return ""
		}
		c.Image, c.Modes = customImageSpec{}, nil
		target = c
	case providerKindHuggingFace:
		c, err := decodeHFConfig(config)
		if err != nil {
			return ""
		}
		target = [3]string{c.Endpoint, c.Task, c.Model}
	case providerKindLocalText:
		c := providerRecord{Kind: kind, Config: config}.localConfig()
		if c.Model == "" {
			return ""
		}
		target = c.Model
	default:
		return ""
	}
	raw, err := json.Marshal(target)
	if err != nil {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{kind, baseURL, keyEnc, string(raw)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (r providerRecord) fingerprint() string {
	return customFingerprint(r.Kind, r.BaseURL, r.KeyEnc, r.Config)
}

// testPassed reports a connection whose last test passed for exactly its
// current endpoint, key and description.
func (r providerRecord) testPassed() bool {
	last := r.lastTest()
	fp := r.fingerprint()
	return last.State == "ok" && fp != "" && last.Fingerprint == fp
}

// enableReady reports whether a custom connection may be turned on: its last
// test passed for what it is now. A local text connection tested before
// fingerprints were recorded keeps its passed state (every change to its
// endpoint, model or key has always cleared the test).
func (r providerRecord) enableReady() bool {
	if r.Kind == providerKindLocalText {
		last := r.lastTest()
		return last.State == "ok" && (last.Fingerprint == "" || last.Fingerprint == r.fingerprint())
	}
	return r.testPassed()
}

// runnable reports whether routing and dispatch may use a connection: an
// admin-only kind must be a server connection, and a tested kind must have
// passed its test for its current endpoint, key and description.
func (r providerRecord) runnable() bool {
	if adminOnlyKind(r.Kind) && r.Scope != providerScopeServer {
		return false
	}
	return !testedKind(r.Kind) || r.testPassed()
}

// recordCustomTest stores the result of a custom connection's test for the
// endpoint, key and description that were tested. A passed result carries
// their fingerprint, so a change made while the test ran (or any later one)
// leaves the connection untested.
func (s *providerStore) recordCustomTest(ctx context.Context, p dispatchProvider, state string) error {
	if state != "ok" && state != "error" {
		return errProviderInvalid
	}
	test := providerTestState{State: state, Time: s.timeNow()}
	if state == "ok" {
		test.Fingerprint = customFingerprint(p.Kind, p.BaseURL, p.KeyEnc, p.Config)
	}
	raw, err := json.Marshal(test)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE providers SET last_test = ?, updated_at = ? WHERE id = ? AND kind = ? AND key_enc = ?`,
		string(raw), s.timeNow(), p.ID, p.Kind, p.KeyEnc)
	if err != nil {
		return fmt.Errorf("ai: record provider test: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errProviderUnavailable
	}
	return nil
}
