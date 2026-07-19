package osroute

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testBoxHost = "01hb-boxid.os.vulos.org"
	testSecret  = "router-signing-secret-32-bytes!!"
	testSecret2 = "previous-router-secret-rotation!!"
)

// mint builds a token the way the CP minter does. Kept in the test only — the OS
// box is a verifier, never a minter.
func mint(t *testing.T, secret string, c Claims) string {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := base64.RawURLEncoding.EncodeToString(sign([]byte(secret), payload))
	return body + "." + mac
}

func validClaims(now time.Time) Claims {
	return Claims{
		Sub: "acct_123",
		Org: "org_abc",
		Aud: testBoxHost,
		Iss: RouterTokenIssuer,
		Iat: now.Unix(),
		Exp: now.Add(5 * time.Minute).Unix(),
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()

	tamper := func(tok string) string {
		// Flip a character in the payload body so the signature no longer matches.
		body, sig, _ := strings.Cut(tok, ".")
		b := []byte(body)
		if b[0] == 'A' {
			b[0] = 'B'
		} else {
			b[0] = 'A'
		}
		return string(b) + "." + sig
	}

	tests := []struct {
		name    string
		verify  *Verifier
		token   string
		now     time.Time
		wantErr error // nil ⇒ expect success
	}{
		{
			name:   "valid token passes",
			verify: NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:  mint(t, testSecret, validClaims(now)),
			now:    now,
		},
		{
			name:   "valid token, audience not pinned, passes",
			verify: NewVerifier([][]byte{[]byte(testSecret)}, ""),
			token:  mint(t, testSecret, validClaims(now)),
			now:    now,
		},
		{
			name:   "rotation: previous secret still verifies",
			verify: NewVerifier([][]byte{[]byte(testSecret), []byte(testSecret2)}, testBoxHost),
			token:  mint(t, testSecret2, validClaims(now)),
			now:    now,
		},
		{
			name:    "forged signature (wrong secret) rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:   mint(t, "attacker-controlled-secret-xxxxx", validClaims(now)),
			now:     now,
			wantErr: ErrSignature,
		},
		{
			name:    "tampered payload rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:   tamper(mint(t, testSecret, validClaims(now))),
			now:     now,
			wantErr: ErrSignature,
		},
		{
			name:    "expired token rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:   mint(t, testSecret, validClaims(now)),
			now:     now.Add(6 * time.Minute),
			wantErr: ErrExpired,
		},
		{
			name:    "wrong audience rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, "someone-else.os.vulos.org"),
			token:   mint(t, testSecret, validClaims(now)),
			now:     now,
			wantErr: ErrAudience,
		},
		{
			name:   "bad issuer rejected",
			verify: NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token: mint(t, testSecret, func() Claims {
				c := validClaims(now)
				c.Iss = "not-the-cp"
				return c
			}()),
			now:     now,
			wantErr: ErrIssuer,
		},
		{
			name:   "missing required field rejected",
			verify: NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token: mint(t, testSecret, func() Claims {
				c := validClaims(now)
				c.Org = ""
				return c
			}()),
			now:     now,
			wantErr: ErrFields,
		},
		{
			name:    "malformed (no dot) rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:   "not-a-token",
			now:     now,
			wantErr: ErrMalformed,
		},
		{
			name:    "empty token rejected",
			verify:  NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost),
			token:   "",
			now:     now,
			wantErr: ErrMalformed,
		},
		{
			name:    "disabled verifier returns ErrNoSecret",
			verify:  NewVerifier(nil, testBoxHost),
			token:   mint(t, testSecret, validClaims(now)),
			now:     now,
			wantErr: ErrNoSecret,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.verify.Verify(tc.token, tc.now)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestMiddleware exercises the fail-closed + self-host-inert HTTP semantics.
func TestMiddleware(t *testing.T) {
	now := time.Now()
	okBody := "reached-next"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(okBody))
	})

	validTok := mint(t, testSecret, Claims{
		Sub: "a", Org: "o", Aud: testBoxHost, Iss: RouterTokenIssuer,
		Iat: now.Unix(), Exp: now.Add(5 * time.Minute).Unix(),
	})
	forgedTok := mint(t, "wrong-secret-wrong-secret-wrong!", Claims{
		Sub: "a", Org: "o", Aud: testBoxHost, Iss: RouterTokenIssuer,
		Iat: now.Unix(), Exp: now.Add(5 * time.Minute).Unix(),
	})
	expiredTok := mint(t, testSecret, Claims{
		Sub: "a", Org: "o", Aud: testBoxHost, Iss: RouterTokenIssuer,
		Iat: now.Add(-1 * time.Hour).Unix(), Exp: now.Add(-10 * time.Minute).Unix(),
	})

	enabled := NewVerifier([][]byte{[]byte(testSecret)}, testBoxHost)

	tests := []struct {
		name     string
		verifier *Verifier
		setup    func(r *http.Request)
		wantCode int
		wantNext bool
	}{
		{
			name:     "configured + valid header token → pass",
			verifier: enabled,
			setup:    func(r *http.Request) { r.Header.Set(RouterTokenHeader, validTok) },
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "configured + valid query token → pass",
			verifier: enabled,
			setup:    func(r *http.Request) { r.URL.RawQuery = RouterTokenQueryParam + "=" + validTok },
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "configured + forged token → 403",
			verifier: enabled,
			setup:    func(r *http.Request) { r.Header.Set(RouterTokenHeader, forgedTok) },
			wantCode: http.StatusForbidden,
			wantNext: false,
		},
		{
			name:     "configured + expired token → 403",
			verifier: enabled,
			setup:    func(r *http.Request) { r.Header.Set(RouterTokenHeader, expiredTok) },
			wantCode: http.StatusForbidden,
			wantNext: false,
		},
		{
			name:     "configured + garbage token → 403",
			verifier: enabled,
			setup:    func(r *http.Request) { r.Header.Set(RouterTokenHeader, "garbage") },
			wantCode: http.StatusForbidden,
			wantNext: false,
		},
		{
			name:     "configured + absent token → pass through to auth layer",
			verifier: enabled,
			setup:    func(r *http.Request) {},
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "unconfigured (self-host) + forged token → inert pass",
			verifier: NewVerifier(nil, ""),
			setup:    func(r *http.Request) { r.Header.Set(RouterTokenHeader, forgedTok) },
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "unconfigured (self-host) + absent token → inert pass",
			verifier: NewVerifier(nil, ""),
			setup:    func(r *http.Request) {},
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "nil verifier → inert pass",
			verifier: nil,
			setup:    func(r *http.Request) {},
			wantCode: http.StatusOK,
			wantNext: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.verifier.Middleware(next)
			r := httptest.NewRequest(http.MethodGet, "/anything", nil)
			tc.setup(r)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.wantCode {
				t.Fatalf("code: want %d got %d", tc.wantCode, rec.Code)
			}
			reached := rec.Body.String() == okBody
			if reached != tc.wantNext {
				t.Fatalf("reached-next: want %v got %v (body=%q)", tc.wantNext, reached, rec.Body.String())
			}
		})
	}
}

func TestVerifierFromEnv(t *testing.T) {
	t.Run("unset ⇒ disabled", func(t *testing.T) {
		t.Setenv(EnvSecret, "")
		t.Setenv(EnvSecretPrev, "")
		if VerifierFromEnv().Enabled() {
			t.Fatal("expected disabled verifier when no secret configured")
		}
	})
	t.Run("set ⇒ enabled, comma-separated rotation", func(t *testing.T) {
		t.Setenv(EnvSecret, testSecret+" , "+testSecret2)
		t.Setenv(EnvSecretPrev, "")
		t.Setenv(EnvAudience, testBoxHost)
		v := VerifierFromEnv()
		if !v.Enabled() {
			t.Fatal("expected enabled verifier")
		}
		now := time.Now()
		// Both secrets must verify.
		for _, s := range []string{testSecret, testSecret2} {
			tok := mint(t, s, Claims{
				Sub: "a", Org: "o", Aud: testBoxHost, Iss: RouterTokenIssuer,
				Iat: now.Unix(), Exp: now.Add(time.Minute).Unix(),
			})
			if _, err := v.Verify(tok, now); err != nil {
				t.Fatalf("secret %q should verify: %v", s, err)
			}
		}
	})
}
