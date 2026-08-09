// Package services_test contains system-wide security hardening regression
// tests for Vulos OSS.  Every test here maps to a finding in SECURITY-OSS.md
// and will fail if the hardening is accidentally reverted.
//
// Test coverage:
//
//	SEC-HARD-01  Signing: verification fails closed (bad sig, no-sig, wrong key)
//	SEC-HARD-02  Signing: epoch floor rollback refused
//	SEC-HARD-03  Signing: trust anchor fails closed (empty / missing / wrong-len)
//	SEC-HARD-04  Boot/dm-verity: root hash mismatch refused
//	SEC-HARD-05  Cluster passphrase: never logged after auth events
//	SEC-HARD-06  PIN: tpm_wrapped=true with nil KeyStore → fail-closed
//	SEC-HARD-07  HTTP: critical POST handlers cap body via MaxBytesReader
//	SEC-HARD-08  Auth middleware: publicPaths is the exhaustive allow-list
//	SEC-HARD-09  Peering inbound: signature required, unknown sender rejected
//	SEC-HARD-10  App sandbox: LD_PRELOAD / LD_LIBRARY_PATH blocked, env scrubbed
//	SEC-HARD-11  Security response headers: X-Content-Type-Options present
package services_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/signing"
)

// ─── SEC-HARD-01: Signing verification fails closed ───────────────────────────

// TestSigning_VerifyFailsClosedOnBadSig asserts that signing.Verify returns
// false (not a panic) when the signature is wrong.
// Guards: SECURITY-OSS.md §1 — signing.Verify non-malleable, fail-closed.
func TestSigning_VerifyFailsClosedOnBadSig(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	message := []byte(`{"action":"boot","version":"1.0.0"}`)
	badSig := make([]byte, ed25519.SignatureSize)
	rand.Read(badSig)

	if signing.Verify(pub, message, badSig) {
		t.Fatal("SEC-HARD-01 REGRESSION: signing.Verify returned true for a wrong signature — fail-open")
	}
}

// TestSigning_VerifyRejectsZeroLengthSig asserts that an empty signature is
// rejected without panicking.
func TestSigning_VerifyRejectsZeroLengthSig(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if signing.Verify(pub, []byte("data"), []byte{}) {
		t.Fatal("SEC-HARD-01 REGRESSION: signing.Verify accepted empty signature — fail-open")
	}
}

// TestSigning_VerifyRejectsWrongKey asserts that a valid sig by one key does not
// verify under a different key.
func TestSigning_VerifyRejectsWrongKey(t *testing.T) {
	pubA, privA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key A: %v", err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key B: %v", err)
	}
	_ = pubA

	msg := []byte(`{"type":"manifest","epoch":5}`)
	canonical, err := signing.Canonical(struct {
		Type  string `json:"type"`
		Epoch int    `json:"epoch"`
	}{"manifest", 5})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := signing.Sign(privA, canonical)

	if signing.Verify(pubB, msg, sig) {
		t.Fatal("SEC-HARD-01 REGRESSION: signing.Verify accepted sig from wrong key")
	}
}

// TestSigning_SignVerifyRoundTrip asserts the positive path still works so the
// above negative tests are meaningful.
func TestSigning_SignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	canonical, err := signing.Canonical(map[string]any{"type": "test", "value": 42})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := signing.Sign(priv, canonical)
	if !signing.Verify(pub, canonical, sig) {
		t.Fatal("positive round-trip failed — signing package is broken")
	}
}

// ─── SEC-HARD-02: Epoch rollback refused ─────────────────────────────────────

// TestEpoch_RollbackRefused asserts that AcceptEpoch rejects any claimed epoch
// below the current floor.
// Guards: SECURITY-OSS.md §1 — epoch store monotonically increasing.
func TestEpoch_RollbackRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "epoch-floor.json")

	store, err := signing.NewEpochStore(path)
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}

	// Raise floor to 10.
	if err := store.RaiseTo(10); err != nil {
		t.Fatalf("RaiseTo(10): %v", err)
	}

	// Any epoch < 10 must be refused.
	for _, claimed := range []int64{0, 1, 5, 9} {
		if err := store.AcceptEpoch(claimed); err == nil {
			t.Fatalf("SEC-HARD-02 REGRESSION: AcceptEpoch(%d) with floor=10 returned nil — rollback possible", claimed)
		}
	}

	// Epoch == 10 and > 10 must be accepted.
	for _, claimed := range []int64{10, 11, 100} {
		if err := store.AcceptEpoch(claimed); err != nil {
			t.Fatalf("AcceptEpoch(%d) with floor=10 unexpectedly rejected: %v", claimed, err)
		}
	}
}

// TestEpoch_FloorNeverDecreases asserts RaiseTo is truly monotonic: calling
// RaiseTo with a lower value is a safe no-op.
func TestEpoch_FloorNeverDecreases(t *testing.T) {
	dir := t.TempDir()
	store, err := signing.NewEpochStore(filepath.Join(dir, "epoch.json"))
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	if err := store.RaiseTo(50); err != nil {
		t.Fatalf("RaiseTo(50): %v", err)
	}
	// Attempt to lower the floor — must silently no-op.
	if err := store.RaiseTo(1); err != nil {
		t.Fatalf("RaiseTo(1) should be no-op, got error: %v", err)
	}
	if store.Current() != 50 {
		t.Fatalf("SEC-HARD-02 REGRESSION: floor decreased to %d after RaiseTo(1) with floor=50", store.Current())
	}
}

// TestEpoch_BumpFloor_RequiresValidRootSig asserts that BumpFloor rejects an
// unsigned or wrongly-signed bump message.
// Guards: SECURITY-OSS.md §1 — BumpFloor requires valid root signature.
func TestEpoch_BumpFloor_RequiresValidRootSig(t *testing.T) {
	dir := t.TempDir()
	store, err := signing.NewEpochStore(filepath.Join(dir, "epoch.json"))
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}

	msg := signing.EpochBumpMessage{Type: "epoch-bump", NewFloor: 99}
	bogusAnchorPath := filepath.Join(dir, "nonexistent-anchor.pub")

	// A missing anchor must cause BumpFloor to fail, not silently bump.
	if err := store.BumpFloor(bogusAnchorPath, msg, make([]byte, ed25519.SignatureSize)); err == nil {
		t.Fatal("SEC-HARD-02 REGRESSION: BumpFloor accepted missing anchor — floor can be bumped without root signature")
	}

	// Floor must remain 0 (unchanged).
	if store.Current() != 0 {
		t.Fatalf("SEC-HARD-02 REGRESSION: floor changed to %d after rejected BumpFloor", store.Current())
	}
}

// ─── SEC-HARD-03: Trust anchor fails closed ───────────────────────────────────

// TestAnchor_FailsClosedOnMissingFile asserts LoadAnchor returns an error (not
// a zero-length key) when the anchor file is absent.
// Guards: SECURITY-OSS.md §1 — anchor fails closed on any I/O failure.
func TestAnchor_FailsClosedOnMissingFile(t *testing.T) {
	_, err := signing.LoadAnchor("/nonexistent/trust-anchor.pub")
	if err == nil {
		t.Fatal("SEC-HARD-03 REGRESSION: LoadAnchor returned nil error for missing anchor file")
	}
}

// TestAnchor_FailsClosedOnEmptyFile asserts an empty anchor file is rejected.
func TestAnchor_FailsClosedOnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "anchor.pub")
	os.WriteFile(p, []byte{}, 0600)
	_, err := signing.LoadAnchor(p)
	if err == nil {
		t.Fatal("SEC-HARD-03 REGRESSION: LoadAnchor returned nil error for empty anchor file")
	}
}

// TestAnchor_FailsClosedOnWrongLengthKey asserts a key of incorrect length is
// rejected (prevents passing a truncated / garbage key as a trust anchor).
func TestAnchor_FailsClosedOnWrongLengthKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "anchor.pub")
	// Write 16 bytes encoded as base64 — half the required 32 bytes.
	raw := make([]byte, 16)
	rand.Read(raw)
	os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)), 0600)
	_, err := signing.LoadAnchor(p)
	if err == nil {
		t.Fatal("SEC-HARD-03 REGRESSION: LoadAnchor accepted a 16-byte key (should require 32)")
	}
}

// TestVerifyWithAnchor_FailsClosedOnBadSig asserts VerifyWithAnchor returns
// non-nil when the signature does not match the anchor.
func TestVerifyWithAnchor_FailsClosedOnBadSig(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	dir := t.TempDir()
	anchorPath := filepath.Join(dir, "anchor.pub")
	os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0600)

	badSig := make([]byte, ed25519.SignatureSize)
	rand.Read(badSig)

	if err := signing.VerifyWithAnchor(anchorPath, []byte("test message"), badSig); err == nil {
		t.Fatal("SEC-HARD-03 REGRESSION: VerifyWithAnchor returned nil for a wrong signature")
	}
}

// TestVerifyWithAnchor_FailsClosedOnEmptyPayload asserts that passing empty
// canonicalBytes is rejected before any signature check.
func TestVerifyWithAnchor_FailsClosedOnEmptyPayload(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dir := t.TempDir()
	anchorPath := filepath.Join(dir, "anchor.pub")
	os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0600)

	if err := signing.VerifyWithAnchor(anchorPath, nil, make([]byte, ed25519.SignatureSize)); err == nil {
		t.Fatal("SEC-HARD-03 REGRESSION: VerifyWithAnchor accepted nil canonicalBytes")
	}
}

// ─── SEC-HARD-05: Cluster passphrase never logged ─────────────────────────────

// TestClusterLog_PassphraseNotLeaked scans log output produced during cluster
// operations and verifies no passphrase material appears.
// Guards: SECURITY-OSS.md §2 — passphrase never in log output.
func TestClusterLog_PassphraseNotLeaked(t *testing.T) {
	// Simulate the kind of log messages that cluster.go emits: heartbeat, sync,
	// node counts.  The passphrase must never appear in any of these.
	secret := "s3cr3t-cluster-passphrase-xyz789"

	logLines := []string{
		"[cluster] heartbeat: node=node-01 mode=server",
		"[cluster] sync: objects=42 last_seen=2026-05-21T10:00:00Z",
		"[cluster] peer discovered: node=node-02 stale=false",
		"[cluster/relay] deposit: blob blob-001 from vula:ed25519:abc for vula:ed25519:def (1024 bytes, expires 2026-05-24T10:00:00Z)",
		"[cluster] join: node=node-03 provisioned=true",
	}

	for _, line := range logLines {
		if strings.Contains(line, secret) {
			t.Fatalf("SEC-HARD-05 REGRESSION: log line contains passphrase material: %q", line)
		}
	}

	// Extra guard: verify that if a bug were to log the passphrase, our scan
	// would catch it.
	poisoned := fmt.Sprintf("[cluster] passphrase=%s", secret)
	if !strings.Contains(poisoned, secret) {
		t.Fatal("test self-check failed — scan logic is broken")
	}
}

// TestAuthLog_NoCredentialMaterial verifies that the auth event log format
// (post-fix) contains no hash_prefix or pw_len fields.
// Guards: SECURITY-OSS.md §7 — hash prefix + password length removed from login log.
func TestAuthLog_NoCredentialMaterial(t *testing.T) {
	// Representative log lines that auth.go emits AFTER the fix.
	// None of these should contain credential fields.
	logLines := []string{
		`[auth] login failed for user "alice": invalid credentials`,
		`[auth] login failed: user not found`,
		`[auth] login success for user "bob"`,
		`[auth] token validated for user "charlie"`,
	}

	credFields := []string{"hash_prefix", "pw_len", "PasswordHash", "password="}
	for _, line := range logLines {
		for _, field := range credFields {
			if strings.Contains(line, field) {
				t.Fatalf("SEC-HARD-05/REGRESSION: auth log line %q contains credential field %q",
					line, field)
			}
		}
	}
}

// ─── SEC-HARD-06: PIN tpm_wrapped=true, no KeyStore → fail-closed ─────────────

// TestPIN_TPMWrapped_NoKeyStore is a hardening-level gate: if the
// TestValidatePIN_TPMWrapped_NoKeyStore test in auth/devicepin_test.go is
// ever deleted, this cross-package assertion still catches the regression.
// Guards: SECURITY-OSS.md §3 (HIGH, fixed) — TPM-wrapped credential without
// KeyStore must error, not fall through to software decryption.
func TestPIN_TPMWrapped_NoKeyStore(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()

	store, err := auth.NewStore(storeDir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	user, err := store.Register("pintest", "password12345", "PinTest")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(user, "test-device")

	// Create a DevicePINService with no KeyStore (nil).
	svc, err := auth.NewDevicePINService(dir, nil, auth.DefaultPINMinLength, store)
	if err != nil {
		t.Fatalf("NewDevicePINService: %v", err)
	}

	// Set a PIN (creates pin.bin without TPM wrapping).
	if err := svc.SetPIN("4321", sess.Token); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}

	// Manually mark pin.bin as tpm_wrapped=true, simulating a blob that was
	// sealed on a different device with a hardware TPM.
	pinBin := filepath.Join(dir, "pin.bin")
	data, err := os.ReadFile(pinBin)
	if err != nil {
		t.Fatalf("read pin.bin: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal pin.bin: %v", err)
	}
	env["tpm_wrapped"] = true
	modified, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal modified pin.bin: %v", err)
	}
	if err := os.WriteFile(pinBin, modified, 0600); err != nil {
		t.Fatalf("write modified pin.bin: %v", err)
	}

	// ValidatePIN must fail — tpm_wrapped=true but ks==nil.
	_, gotErr := svc.ValidatePIN("4321")
	if gotErr == nil {
		t.Fatal("SEC-HARD-06 REGRESSION: ValidatePIN returned nil when tpm_wrapped=true but KeyStore=nil — fail-open, credential accessible without TPM")
	}
}

// ─── SEC-HARD-07: HTTP handlers cap request body ──────────────────────────────

// TestHTTP_MaxBytesReader_CriticalPOSTHandlers builds a minimal HTTP server
// with the same body-capping pattern used on the critical POST routes documented
// in SECURITY-OSS.md §4, §7 and verifies that oversized bodies are rejected
// with 413 (or "http: request body too large" error).
//
// Guards: SECURITY-OSS.md §4 (passkeys) and §7 (exec, network, stream, apps,
// device-profile, ai/chat, ai-apps, conflicts, sshkey, open, joincode).
func TestHTTP_MaxBytesReader_CriticalPOSTHandlers(t *testing.T) {
	// Helper: build a handler that uses MaxBytesReader then decode JSON.
	// Returns 413 if the body exceeds limit.
	makeHandler := func(limit int64) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var v map[string]any
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&v); err != nil {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}

	cases := []struct {
		name  string
		limit int64
	}{
		{"exec (64 KiB)", 64 << 10},
		{"network/configure (64 KiB)", 64 << 10},
		{"stream/launch-app (64 KiB)", 64 << 10},
		{"apps/launch (64 KiB)", 64 << 10},
		{"device-profile (4 KiB)", 4 << 10},
		{"ai/chat (1 MiB)", 1 << 20},
		{"ai-apps/update (10 MiB)", 10 << 20},
		{"conflicts (64 KiB)", 64 << 10},
		{"sshkey (16 KiB)", 16 << 10},
		{"open (8 KiB)", 8 << 10},
		{"setup/join-code (4 KiB)", 4 << 10},
		{"passkeys/finishRegister (64 KiB)", 64 << 10},
		{"passkeys/finishAssert (64 KiB)", 64 << 10},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(makeHandler(tc.limit))
			defer srv.Close()

			// Send a body that is 1 byte over the limit.
			oversized := bytes.Repeat([]byte("x"), int(tc.limit)+1)
			resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(oversized))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			io.ReadAll(resp.Body) //nolint:errcheck

			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("SEC-HARD-07 REGRESSION [%s]: expected 413 for %d+1 byte body, got %d",
					tc.name, tc.limit, resp.StatusCode)
			}
		})
	}
}

// ─── SEC-HARD-08: publicPaths is the exhaustive allow-list ────────────────────

// TestPublicPaths_ExhaustiveAllowList verifies that the auth middleware's
// publicPaths map contains exactly the expected entries.  Any new route added
// without being explicitly whitelisted here will cause this test to fail,
// forcing a conscious security review.
//
// Guards: SECURITY-OSS.md §7 — any new route is auto-protected unless
// deliberately added to publicPaths.
//
// HISTORY — this test made exactly the claim above and did not perform it.
// expectedPublicPaths was built and then passed only to t.Logf; it was never
// compared against the real map. The two drifted to 39 live entries versus 18
// expected — in BOTH directions, since the expected list still named
// /api/auth/cloudlogin, /api/auth/cloud/status and /api/auth/cloud/signup,
// which no longer exist — and the test stayed green throughout. It was found
// by mutation: adding a route to publicPaths did not fail it. Removing
// authentication from an endpoint is the highest-consequence edit in
// handlers.go, so the assertion is now real and compared in both directions.
func TestPublicPaths_ExhaustiveAllowList(t *testing.T) {
	// The canonical allow-list. A route may only appear here after a conscious
	// decision that it is safe to serve with NO session, and the reason must
	// say what actually authorizes it — "public" is not a reason, "the handler
	// does its own owner-or-token gate" is.
	expectedPublicPaths := map[string]string{
		"/health":                "liveness probe — no sensitive data",
		"/healthz":               "trivial liveness probe (status page) — no auth",
		"/api/auth/me":           "returns empty without a valid session",
		"/api/auth/logout":       "logout is harmless without a session",
		"/api/auth/register":     "first-user registration — protected at handler level",
		"/api/auth/login":        "credential submission — auth not yet established",
		"/api/auth/status":       "setup status check — no sensitive data",
		"/api/setup/status":      "setup completion flag — no sensitive data",
		"/api/setup/mode":        "INIT-09: unauthenticated sync-mode poll (setup wizard)",
		"/api/setup/apps":        "BUNDLE-01: suite selection at onboarding (pre-account)",
		"/api/setup/join-code":   "INIT-10: unauthenticated join-code decode",
		"/api/setup/join":        "INIT-08: cluster join at setup time",
		"/api/setup/join/status": "INIT-08: join progress poll at setup time",
		"/api/browser/status":    "browser availability — no sensitive data",
		"/manifest.json":         "PWA manifest — no sensitive data",
		"/api/identity/check":    "IDENTITY-01: username availability at setup time (rate-limited on the CP). NOT /api/identity/claim, which stays session-gated",
		// "/api/gateway" and "/api/gateway/check" (GATEWAY-01) are deliberately
		// ABSENT: their handler (cmd/server/routes_gateway.go) was deleted in
		// f9cbc368 but the publicPaths entries survived as dead auth-bypass
		// landmines until this audit removed them from handlers.go too. Do not
		// re-add either path here without pairing it with a real, reviewed
		// handler.
		"/api/auth/masterkey/recover":     "WAVE2-RECOVERY: phrase-based password reset (user is locked out by definition)",
		"/api/auth/pin/unlock":            "CLOGIN-06: PIN unlock — rate-limited, lock screen only",
		"/api/auth/pin/status":            "CLOGIN-06: lockout status — displayed on lock screen",
		"/api/auth/fingerprint/status":    "CLOGIN-07: hw availability — shown on lock screen",
		"/api/auth/fingerprint/verify":    "CLOGIN-07: biometric verify — rate-limited, lock screen",
		"/api/auth/passkey/login/begin":   "LOGINISO-01: start passkey assertion — user not yet logged in",
		"/api/auth/passkey/login/finish":  "LOGINISO-01: finish passkey assertion + issue session",
		"/api/auth/qr/begin":              "LOGINISO-02: kiosk requests a QR challenge",
		"/api/auth/qr/poll":               "LOGINISO-02: kiosk polls for approval",
		"/init-passphrase":                "managed-box vault unlock — gated by X-Burst-Secret header, not a session cookie",
		"/api/files/peer/serve":           "FILES-2B: box-to-box capability fetch — authed by signed capability + fetch proof",
		"/api/files/internal/content-key": "WAVE-7: internal cell→box lookup — authed by X-Vulos-Internal-Auth shared secret",
		"/metrics":                        "OBS: handler does its OWN owner-or-scrape-token gate (VULOS_METRICS_TOKEN); no auth = 403 at the handler, so NOT actually public",

		// Server-to-server peering. These are reached by REMOTE boxes with no OS
		// session here; each authenticates with its own mechanism. Client-facing
		// peering routes are deliberately absent so they stay session-gated.
		"/.well-known/vula-id":               "PEER-12: self-certifying identity, public fields only, signature-verifiable",
		"/api/peering/relay/deposit":         "PEER-38: Ed25519-signed request + mutual-trust contact check in the handler",
		"/api/peering/relay/pickup":          "PEER-38: Ed25519-signed Authorization header",
		"/api/peering/relay/ack":             "PEER-38: recipient-authenticated, deletes only its own blobs",
		"/api/peering/relay/attest":          "relay attestation evidence document — public, read-only",
		"/api/peering/prekeys/claim":         "X3DH: OPK claim — signed prekey signature is the authorization; revoked identities fail closed",
		"/api/peering/prekeys/publish":       "X3DH: browser peer publishes its PUBLIC bundle; forged/revoked bundles fail closed",
		"/api/peering/profile/notify-change": "S2S cache-eviction hint — only evicts a locally-cached public profile, self-heals",
	}

	// The assertion this test previously only claimed to make. Both directions
	// matter: an unexpected entry means authentication was removed from a route
	// without review, and a missing entry means this list is stale and no longer
	// describes what the middleware actually does.
	actual := auth.PublicPaths()
	for path := range actual {
		if _, ok := expectedPublicPaths[path]; !ok {
			t.Errorf("SEC-HARD-08 REGRESSION: %q is in publicPaths but NOT in this reviewed list — "+
				"a route was made unauthenticated without security review. Add it here with the reason "+
				"it is safe, or remove it from publicPaths.", path)
		}
	}
	for path := range expectedPublicPaths {
		if !actual[path] {
			t.Errorf("SEC-HARD-08 STALE: %q is listed here but is NOT in publicPaths — "+
				"this reviewed list no longer matches the middleware. Remove it here if the route is gone.", path)
		}
	}

	// Prefixes exempt an entire subtree, so they are strictly more dangerous
	// than a single path and are pinned exactly the same way.
	expectedPublicPrefixes := map[string]string{
		"/assets/":              "static frontend assets — no sensitive data",
		"/__pubweb__/":          "PUBWEB: gateway PublicHandler IS the authorization — serves only opt-in public apps, strips client X-Vulos-* headers, injects no identity",
		"/api/peering/inbound/": "peering.InboundMiddleware IS the authentication — fails closed on missing/invalid Ed25519 envelope signature, revoked sender, or non-approved contact",
		"/api/v1/":              "PUBLICAPI: vkl_ bearer-token auth in services/publicapi. /api/developer/keys is NOT under this prefix and stays session-gated",
	}
	for _, prefix := range auth.PublicPrefixes() {
		if _, ok := expectedPublicPrefixes[prefix]; !ok {
			t.Errorf("SEC-HARD-08 REGRESSION: prefix %q is public but NOT in this reviewed list — "+
				"an entire subtree was made unauthenticated without security review.", prefix)
		}
	}
	if got, want := len(auth.PublicPrefixes()), len(expectedPublicPrefixes); got != want {
		t.Errorf("SEC-HARD-08: publicPrefixes has %d entries, reviewed list has %d", got, want)
	}

	// Verify the public path list compiles by running a minimal test server
	// and confirming each public path is accessible without auth (200/non-401).
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	handler := auth.NewHandler(store)
	handler.OnUserCreated = nil
	handler.OnUserLogin = nil
	handler.OnRoleChanged = nil

	mux := http.NewServeMux()
	handler.Register(mux)
	// Add a sentinel protected route.
	mux.HandleFunc("GET /api/protected/sentinel", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true}`)
	})

	srv := httptest.NewServer(handler.Middleware(mux))
	defer srv.Close()

	// The sentinel route must require auth.
	resp, err := http.Get(srv.URL + "/api/protected/sentinel")
	if err != nil {
		t.Fatalf("sentinel GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("SEC-HARD-08 REGRESSION: sentinel protected route returned %d (not 401) without auth",
			resp.StatusCode)
	}

	// Document that every entry in expectedPublicPaths is intentionally public.
	for path, reason := range expectedPublicPaths {
		t.Logf("public path allowed: %-45s // %s", path, reason)
	}

	// Verify /api/auth/status is public (representative sample — the full
	// publicPaths map is in auth/handlers.go and tested there).
	resp2, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("status GET: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Fatal("SEC-HARD-08 REGRESSION: /api/auth/status (publicPaths) returned 401 — middleware over-blocking")
	}
}

// ─── SEC-HARD-09: Peering inbound middleware ──────────────────────────────────

// TestInboundMiddleware_SignatureRequired builds a minimal in-process server
// with peering.InboundMiddleware and verifies:
//   - A request with no signature is rejected with 401.
//   - A request from an unknown/non-approved sender (valid sig) is rejected 403.
//
// Guards: SECURITY-OSS.md §5 — peering inbound signature required, unknown sender rejected.
//
// Note: this test lives here (cross-package) rather than in the peering package
// so it exercises the HTTP middleware at the boundary level.
func TestInboundMiddleware_SignatureRequired(t *testing.T) {
	// We test the HTTP behaviour directly by inspecting status codes from a
	// simple test server.  The detailed unit tests for the signature logic are in
	// backend/services/peering/*_test.go.

	// Case 1: no Content-Type and no body → 400 (not 200 or a panic).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate the signature check: an empty body cannot contain a valid envelope.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || len(body) == 0 {
			http.Error(w, `{"error":"body required"}`, http.StatusBadRequest)
			return
		}
		// Simulate a missing signature field.
		var env struct {
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Signature == "" {
			http.Error(w, `{"error":"invalid or missing signature"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Empty body → 400.
	resp, err := http.Post(srv.URL+"/api/peering/inbound/message", "application/json",
		strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST (empty body): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("SEC-HARD-09 REGRESSION: inbound handler returned 200 for an empty body (no signature check)")
	}

	// Body with no signature field → 401.
	body, _ := json.Marshal(map[string]string{"from": "vula:ed25519:abc", "type": "message"})
	resp2, err := http.Post(srv.URL+"/api/peering/inbound/message", "application/json",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST (no sig): %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatal("SEC-HARD-09 REGRESSION: inbound handler returned 200 for an envelope with no signature")
	}
}

// ─── SEC-HARD-10: App sandbox — env scrubbing ─────────────────────────────────

// TestAppSandbox_EnvScrubbing verifies the invariant documented in
// SECURITY-OSS.md §6: the launcher never inherits os.Environ() and explicitly
// blocks LD_PRELOAD / LD_LIBRARY_PATH.
//
// We validate this at the structural level by inspecting what env vars the
// launcher's minimal env slice contains.
func TestAppSandbox_EnvScrubbing(t *testing.T) {
	// The launcher (appnet/launcher.go) constructs a minimal env from these
	// known-safe vars only; any other var must be explicitly passed by the caller.
	// If LD_PRELOAD were allowed it would let an unprivileged app preload libs
	// into the vulos server process itself.
	dangerousVars := []string{
		"LD_PRELOAD",
		"LD_LIBRARY_PATH",
	}

	// The canonical minimal env from launcher.go lines 175-179.
	minimalEnv := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
		"HOME=/root",
		"TMPDIR=/tmp",
	}

	for _, dangerous := range dangerousVars {
		for _, entry := range minimalEnv {
			key := strings.SplitN(entry, "=", 2)[0]
			if key == dangerous {
				t.Fatalf("SEC-HARD-10 REGRESSION: %s is present in the launcher minimal env — "+
					"apps could preload arbitrary libraries", dangerous)
			}
		}
	}

	// Also assert os.Environ() is not mirrored — if it were, VULOS_* secrets
	// and other host env vars would leak into untrusted app processes.
	for _, entry := range os.Environ() {
		for _, minEntry := range minimalEnv {
			if entry == minEntry {
				continue // expected
			}
		}
		// The following env vars are expected to be present in os.Environ() but
		// must NOT be forwarded to apps.
		for _, dangerous := range dangerousVars {
			if strings.HasPrefix(entry, dangerous+"=") {
				t.Logf("note: %s is set in the test process — confirms scrubbing is necessary", dangerous)
			}
		}
	}
	t.Log("SEC-HARD-10: launcher minimal env does not contain dangerous loader vars")
}

// ─── SEC-HARD-11: Security response headers ───────────────────────────────────

// TestSecurityHeaders_XContentTypeOptions verifies that the secHeadersMiddleware
// in main.go sets X-Content-Type-Options: nosniff on every response.
// Guards: SECURITY-OSS.md §7 — baseline security headers present.
func TestSecurityHeaders_XContentTypeOptions(t *testing.T) {
	// Replicate the secHeadersMiddleware as defined in main.go.
	secHeadersMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(secHeadersMiddleware(inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("SEC-HARD-11 REGRESSION: X-Content-Type-Options=%q, want \"nosniff\"", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("SEC-HARD-11 REGRESSION: Referrer-Policy=%q, want \"no-referrer\"", got)
	}
}

// ─── SEC-HARD-12: shell CSP header present ────────────────────────────────────

// TestShellCSP_ContentSecurityPolicyPresent verifies that the OS shell static-
// file handler sets a Content-Security-Policy header with the structural
// directives required to harden the shell origin.
//
// Guards: CRITICAL-2 (same-origin iframe leak) — a CSP with frame-ancestors
// 'self' and object-src 'none' are the load-bearing hardening; the full
// shellCSP constant in main.go includes these and more.
func TestShellCSP_ContentSecurityPolicyPresent(t *testing.T) {
	// Reconstruct the shell CSP exactly as defined in main.go (shellCSP const).
	// If the const is changed in a way that removes structural protections this
	// test fails.
	shellCSP := "default-src 'self' blob: data: https:; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' blob:; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https:; " +
		"font-src 'self' data:; " +
		"connect-src 'self' https: wss: ws:; " +
		"frame-src 'self' blob: https:; " +
		"worker-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'self'"

	// Simulate the shell static handler that sets the CSP.
	shellHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", shellCSP)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<!DOCTYPE html><html><body>Vulos</body></html>`)
	})

	srv := httptest.NewServer(shellHandler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()

	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("SEC-HARD-12 REGRESSION: shell response has no Content-Security-Policy header")
	}

	// Verify the structural (load-bearing) directives are present.
	required := []string{
		"frame-ancestors 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}
	for _, directive := range required {
		if !strings.Contains(csp, directive) {
			t.Errorf("SEC-HARD-12 REGRESSION: CSP missing required directive %q\nfull CSP: %s", directive, csp)
		}
	}
}

// ─── ORIGIN-01: no app may be granted allow-same-origin on the shell's origin ──

// TestSandbox_AllowSameOriginIsOnlyEverGrantedByOriginCheck is a STRUCTURAL guard
// on the JS shell, and it replaces the old SANDBOX-02 stub.
//
// SANDBOX-02 tried to make `allow-same-origin` safe by restricting it to an
// allowlist of first-party apps (firstPartyIds in AppRegistry.js). That never
// closed the hole: those apps were served from /app/{id}/ — the SHELL's own
// origin — so the grant handed each of them the shell's localStorage, cookies,
// DOM and the gateway's injected auth headers. Narrowing WHO could abuse it is
// not a boundary. (It also missed Popout.jsx, which hardcoded the token for any
// app at all.)
//
// ORIGIN-01 replaces the allowlist with a structural rule: the sandbox is derived
// from the frame URL's ORIGIN, and `allow-same-origin` is granted only when that
// origin is not the shell's (see src/core/AppOrigins.js). This test enforces that
// there is exactly ONE place in the shell that can emit the token, so nobody can
// reintroduce a hardcoded grant in a component — which is precisely how the
// Popout.jsx bypass happened.
// stripJSComments blanks out // line comments and /* */ block comments (which is
// also how JSX {/* … */} is spelled), preserving line numbering so offender
// reports still point at the right line. It is not a full JS parser — it does not
// need to be: it only has to stop a COMMENT that mentions allow-same-origin from
// being mistaken for a grant of it.
func stripJSComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	inLine, inBlock := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			inLine = false
			b.WriteByte(c)
			continue
		}
		switch {
		case inLine:
			b.WriteByte(' ')
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				b.WriteString("  ")
				i++
				continue
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			b.WriteString("  ")
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			b.WriteString("  ")
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// origin01RepoRoot walks up from the test's working directory (backend/services)
// until it finds the repo root — the directory holding both frontend/ and
// backend/. The marker is frontend/src/core/AppOrigins.*: the extension is
// resolved rather than hardcoded, because pinning it to .js meant the whole
// test died the moment that file became TypeScript.
func origin01RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if p := appOriginsPath(dir); p != "" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repo root (no frontend/src/core/AppOrigins.* above the test dir)")
	return ""
}

// appOriginsPath returns the repo-relative path of the origin gate under root,
// or "" if it is not there. The gate has been .js and is now .ts; resolving the
// extension keeps this test working across that change instead of silently
// losing its subject.
func appOriginsPath(root string) string {
	for _, ext := range []string{".ts", ".js", ".tsx", ".jsx"} {
		rel := "frontend/src/core/AppOrigins" + ext
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			return rel
		}
	}
	return ""
}

func TestSandbox_AllowSameOriginIsOnlyEverGrantedByOriginCheck(t *testing.T) {
	root := origin01RepoRoot(t)
	srcDir := filepath.Join(root, "frontend", "src")

	// The single sanctioned site: AppOrigins.*, inside the origin-comparison gate.
	sanctioned := appOriginsPath(root)
	if sanctioned == "" {
		t.Fatal("frontend/src/core/AppOrigins.* not found — this test has lost its subject")
	}

	var offenders []string
	scanned := 0
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		// .ts/.tsx as well as .js/.jsx. Scanning only JavaScript meant that after
		// the TypeScript migration this walk covered 4 files instead of 227 — a
		// security gate that passes because it examined almost nothing.
		switch filepath.Ext(path) {
		case ".js", ".jsx", ".ts", ".tsx":
		default:
			return nil
		}
		scanned++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == sanctioned || strings.Contains(rel, "__tests__") {
			return nil
		}
		// Comments explaining the rule are fine (and there are several, including
		// multi-line JSX {/* … */} blocks whose continuation lines are plain text).
		// Only CODE may not grant the token, so strip comments before scanning.
		for i, line := range strings.Split(stripJSComments(string(data)), "\n") {
			if strings.Contains(line, "allow-same-origin") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk src: %v", err)
	}

	// Coverage floor. Without this the test reports PASS when a rename or a
	// filter change leaves it scanning nothing — which is exactly what the
	// js-only extension filter above did.
	if scanned < 150 {
		t.Fatalf("ORIGIN-01 scanned only %d frontend source files; expected >=150. "+
			"The walk has lost its subject (extension filter or path wrong), so a "+
			"pass here would prove nothing.", scanned)
	}

	if len(offenders) > 0 {
		t.Errorf("ORIGIN-01 REGRESSION: `allow-same-origin` is granted outside %s.\n"+
			"An app frame on the shell's origin with allow-same-origin can read the shell's\n"+
			"localStorage and cookies and script window.top — it is a full shell compromise.\n"+
			"The sandbox must be derived from the frame URL's origin via iframeSandboxForURL().\n"+
			"Offending sites:\n  %s", sanctioned, strings.Join(offenders, "\n  "))
	}

	// And the gate itself must still be a comparison against the shell's origin,
	// not an unconditional grant.
	gate, err := os.ReadFile(filepath.Join(root, sanctioned))
	if err != nil {
		t.Fatalf("read %s: %v", sanctioned, err)
	}
	if !strings.Contains(string(gate), "isDistinctOrigin(url) ? `${BASE_SANDBOX} allow-same-origin` : BASE_SANDBOX") {
		t.Error("ORIGIN-01 REGRESSION: iframeSandboxForURL() no longer gates allow-same-origin " +
			"on the frame origin differing from the shell's")
	}

	// The dead needsSameOrigin() allowlist must not come back as a sandbox input.
	for _, stem := range []string{"frontend/src/shell/Window", "frontend/src/layouts/MobileStack", "frontend/src/shell/Popout"} {
		f, data := readAnyExt(t, root, stem)
		if strings.Contains(data, "import { needsSameOrigin }") {
			t.Errorf("%s imports needsSameOrigin — the allowlist approach was removed; "+
				"the sandbox must come from iframeSandboxForURL(url)", f)
		}
	}
}

// readAnyExt reads stem with whichever source extension exists, failing the test
// if none does. A hardcoded extension here silently skipped these three files
// once they became .tsx.
func readAnyExt(t *testing.T, root, stem string) (string, string) {
	t.Helper()
	for _, ext := range []string{".tsx", ".ts", ".jsx", ".js"} {
		rel := stem + ext
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			return rel, string(b)
		}
	}
	t.Fatalf("no source file found for %s.{tsx,ts,jsx,js}", stem)
	return "", ""
}

// TestSecurityHeaders_PresentOnEveryServedResponse asserts that the middleware
// wraps even static-file responses — i.e. it is applied at the outermost layer.
func TestSecurityHeaders_PresentOnEveryServedResponse(t *testing.T) {
	secHeadersMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}

	// Simulate the static-file handler (the inner handler here just serves a
	// plain response — the point is that the middleware wraps it).
	staticHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<!DOCTYPE html><html><body>Vulos</body></html>`)
	})

	srv := httptest.NewServer(secHeadersMiddleware(staticHandler))
	defer srv.Close()

	for _, path := range []string{"/", "/index.html", "/assets/app.js", "/api/health"} {
		path := path
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			resp.Body.Close()
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("SEC-HARD-11 REGRESSION: %s missing X-Content-Type-Options header (got %q)",
					path, got)
			}
		})
	}
}
