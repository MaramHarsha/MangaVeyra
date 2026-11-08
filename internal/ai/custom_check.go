package ai

import (
	"context"
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
