package auth_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/MaramHarsha/MangaVeyra/internal/auth"
	"github.com/MaramHarsha/MangaVeyra/internal/db"
	"github.com/MaramHarsha/MangaVeyra/internal/testutil"
)

func TestTokenApprovalThresholdValidation(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		owner := ta.CreateUser(t, "", auth.RoleCreator)
		for name, value := range map[string]float64{
			"negative":          -0.01,
			"nan":               math.NaN(),
			"positive infinity": math.Inf(1),
			"overflow":          math.MaxFloat64,
		} {
			t.Run(name, func(t *testing.T) {
				if _, _, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
					UserID: owner.ID, Kind: auth.TokenAgent, Name: "invalid approval", ApproveOverUSD: &value,
				}); err == nil {
					t.Fatal("invalid approval threshold was accepted")
				}
			})
		}

		zero := 0.0
		raw, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
			UserID: owner.ID, Kind: auth.TokenAgent, Name: "approval boundary", ApproveOverUSD: &zero,
		})
		if err != nil {
			t.Fatalf("zero approval threshold: %v", err)
		}
		if _, _, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID); err != nil {
			t.Fatalf("resolve valid threshold: %v", err)
		}
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET approve_over_usd = -1 WHERE id = ?`, info.ID); err != nil {
			t.Fatal(err)
		}
		if tok, user, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID); !errors.Is(err, auth.ErrTokenInvalid) || tok != nil || user != nil {
			t.Fatalf("malformed stored threshold resolved: token=%#v user=%#v err=%v", tok, user, err)
		}
		if tok, user, err := ta.Deps.Auth.VerifyToken(ctx, raw, "192.0.2.20"); !errors.Is(err, auth.ErrTokenInvalid) || tok != nil || user != nil {
			t.Fatalf("malformed stored threshold verified: token=%#v user=%#v err=%v", tok, user, err)
		}
	})
}

func TestResolveStoredToken(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		ctx := context.Background()
		now := time.Date(2026, 11, 7, 12, 30, 0, 123456000, time.UTC)
		ta.Deps.Auth.Now = func() time.Time { return now }

		assertInvalid := func(t *testing.T, tokenID string) {
			t.Helper()
			tok, user, err := ta.Deps.Auth.ResolveStoredToken(ctx, tokenID)
			if !errors.Is(err, auth.ErrTokenInvalid) || tok != nil || user != nil {
				t.Fatalf("ResolveStoredToken(%q) = %#v, %#v, %v; want ErrTokenInvalid", tokenID, tok, user, err)
			}
		}

		t.Run("active agent and API match raw verification", func(t *testing.T) {
			owner := ta.CreateUser(t, "", auth.RoleCreator)
			threshold := 0.75
			for _, kind := range []string{auth.TokenAgent, auth.TokenAPI} {
				t.Run(kind, func(t *testing.T) {
					raw, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
						UserID: owner.ID, Kind: kind, Name: kind + " token",
						Scopes: []string{"projects:read", "generate"}, ProjectIDs: []string{"project-a"}, ApproveOverUSD: &threshold,
					})
					if err != nil {
						t.Fatal(err)
					}
					wantToken, wantUser, err := ta.Deps.Auth.VerifyToken(ctx, raw, "192.0.2.10")
					if err != nil {
						t.Fatal(err)
					}
					gotToken, gotUser, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(gotToken, wantToken) || !reflect.DeepEqual(gotUser, wantUser) {
						t.Fatalf("resolved identity = %#v, %#v; raw identity = %#v, %#v", gotToken, gotUser, wantToken, wantUser)
					}
				})
			}
		})

		t.Run("stored worker validates owner and returns no user", func(t *testing.T) {
			raw, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
				UserID: ta.Admin.ID, Kind: auth.TokenWorker, Name: "render worker", Scopes: []string{"*"},
			})
			if err != nil {
				t.Fatal(err)
			}
			wantToken, wantUser, err := ta.Deps.Auth.VerifyToken(ctx, raw, "192.0.2.11")
			if err != nil {
				t.Fatal(err)
			}
			gotToken, gotUser, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID)
			if err != nil || wantUser != nil || gotUser != nil || !reflect.DeepEqual(gotToken, wantToken) {
				t.Fatalf("resolved worker = %#v, %#v, %v; raw = %#v, %#v", gotToken, gotUser, err, wantToken, wantUser)
			}
		})

		t.Run("missing and environment IDs", func(t *testing.T) {
			for _, tokenID := range []string{"", "missing-token-id", auth.EnvWorkerTokenID} {
				t.Run(tokenID, func(t *testing.T) { assertInvalid(t, tokenID) })
			}
		})

		t.Run("revoked", func(t *testing.T) {
			owner := ta.CreateUser(t, "", auth.RoleCreator)
			_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAgent, Name: "revoked"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ta.Deps.Auth.RevokeToken(ctx, owner.ID, info.ID, false); err != nil {
				t.Fatal(err)
			}
			assertInvalid(t, info.ID)
		})

		t.Run("expiry boundary and future", func(t *testing.T) {
			owner := ta.CreateUser(t, "", auth.RoleCreator)
			for _, tc := range []struct {
				name    string
				expires time.Time
				valid   bool
			}{
				{name: "exactly now", expires: now},
				{name: "future", expires: now.Add(time.Microsecond), valid: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
						UserID: owner.ID, Kind: auth.TokenAPI, Name: tc.name, ExpiresAt: &tc.expires,
					})
					if err != nil {
						t.Fatal(err)
					}
					if !tc.valid {
						assertInvalid(t, info.ID)
						return
					}
					if tok, user, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID); err != nil || tok == nil || user == nil {
						t.Fatalf("future token = %#v, %#v, %v", tok, user, err)
					}
				})
			}
		})

		t.Run("non-active and deleted owners", func(t *testing.T) {
			for _, status := range []string{auth.StatusPending, auth.StatusSuspended} {
				t.Run(status, func(t *testing.T) {
					owner := ta.CreateUser(t, "", auth.RoleCreator)
					_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAgent, Name: status})
					if err != nil {
						t.Fatal(err)
					}
					// Directly changing status keeps the token unrevoked, proving owner
					// validation itself rejects every non-active account state.
					if _, err := ta.Deps.DB.Exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, status, owner.ID); err != nil {
						t.Fatal(err)
					}
					assertInvalid(t, info.ID)
				})
			}
			t.Run("deleted", func(t *testing.T) {
				owner := ta.CreateUser(t, "", auth.RoleCreator)
				_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAPI, Name: "deleted owner"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ta.Deps.DB.Exec(ctx, `DELETE FROM users WHERE id = ?`, owner.ID); err != nil {
					t.Fatal(err)
				}
				assertInvalid(t, info.ID)
			})
		})

		t.Run("nil JSON normalizes to empty slices", func(t *testing.T) {
			owner := ta.CreateUser(t, "", auth.RoleCreator)
			_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAPI, Name: "nil JSON"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET scopes = 'null', project_ids = 'null' WHERE id = ?`, info.ID); err != nil {
				t.Fatal(err)
			}
			tok, user, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID)
			if err != nil || user == nil || tok == nil || tok.Scopes == nil || tok.ProjectIDs == nil || len(tok.Scopes) != 0 || len(tok.ProjectIDs) != 0 {
				t.Fatalf("resolved nil JSON = %#v, %#v, %v", tok, user, err)
			}
		})

		t.Run("does not update last used fields", func(t *testing.T) {
			owner := ta.CreateUser(t, "", auth.RoleCreator)
			_, info, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{UserID: owner.ID, Kind: auth.TokenAgent, Name: "queued job"})
			if err != nil {
				t.Fatal(err)
			}
			lastUsed := now.Add(-24 * time.Hour)
			if _, err := ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, lastUsed, "198.51.100.7", info.ID); err != nil {
				t.Fatal(err)
			}
			before, err := ta.Deps.Auth.GetToken(ctx, info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := ta.Deps.Auth.ResolveStoredToken(ctx, info.ID); err != nil {
				t.Fatal(err)
			}
			after, err := ta.Deps.Auth.GetToken(ctx, info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if before.LastUsedAt == nil || after.LastUsedAt == nil || !before.LastUsedAt.Equal(*after.LastUsedAt) || before.LastUsedIP != after.LastUsedIP {
				t.Fatalf("last used changed: before=%+v after=%+v", before, after)
			}
		})
	})
}

func TestResolveStoredTokenTxUsesTransactionSnapshot(t *testing.T) {
	forEach(t, func(t *testing.T, ta *testutil.TestApp) {
		now := time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)
		ta.Deps.Auth.Now = func() time.Time { return now }
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		revokedOwner := ta.CreateUser(t, "", auth.RoleCreator)
		_, revoked, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
			UserID: revokedOwner.ID, Kind: auth.TokenAgent, Name: "revoked in transaction",
		})
		if err != nil {
			t.Fatal(err)
		}
		statusOwner := ta.CreateUser(t, "", auth.RoleCreator)
		_, statusChanged, err := ta.Deps.Auth.CreateToken(ctx, auth.TokenInput{
			UserID: statusOwner.ID, Kind: auth.TokenAPI, Name: "owner changed in transaction",
		})
		if err != nil {
			t.Fatal(err)
		}
		lastUsed := now.Add(-24 * time.Hour)
		if _, err := ta.Deps.DB.Exec(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, lastUsed, "203.0.113.9", revoked.ID); err != nil {
			t.Fatal(err)
		}

		err = ta.Deps.DB.WithTx(ctx, func(tx *db.Tx) error {
			if tok, user, err := ta.Deps.Auth.ResolveStoredTokenTx(ctx, tx, statusChanged.ID); err != nil || tok == nil || user == nil {
				return errors.New("active token did not resolve through transaction")
			}
			if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ?`, now, revoked.ID); err != nil {
				return err
			}
			if tok, user, err := ta.Deps.Auth.ResolveStoredTokenTx(ctx, tx, revoked.ID); !errors.Is(err, auth.ErrTokenInvalid) || tok != nil || user != nil {
				return errors.New("resolver did not see in-transaction revocation")
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET status = ? WHERE id = ?`, auth.StatusPending, statusOwner.ID); err != nil {
				return err
			}
			if tok, user, err := ta.Deps.Auth.ResolveStoredTokenTx(ctx, tx, statusChanged.ID); !errors.Is(err, auth.ErrTokenInvalid) || tok != nil || user != nil {
				return errors.New("resolver did not see in-transaction owner status")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		after, err := ta.Deps.Auth.GetToken(context.Background(), revoked.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.LastUsedAt == nil || !after.LastUsedAt.Equal(lastUsed) || after.LastUsedIP != "203.0.113.9" {
			t.Fatalf("transaction resolver changed last-used fields: %+v", after)
		}
	})
}
