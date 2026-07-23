package appsplatform

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- Fix 1: Token TTL + age-revocation --------------------------------------

// TestExpiredTokenRejected verifies that a token past its absolute TokenExpiresAt
// is rejected with 401 by the TokenAuth middleware.
func TestExpiredTokenRejected(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	// Create with a TTL that has already elapsed.
	c, err := reg.Create(CreateParams{
		Name:     "ttl-app",
		OwnerID:  "alice",
		Products: []string{ProductOffice},
		TokenTTL: time.Millisecond, // expires immediately
	})
	if err != nil {
		t.Fatal(err)
	}
	// Give the token a chance to expire.
	time.Sleep(5 * time.Millisecond)

	w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token should be 401, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("response should mention expiry, got: %s", w.Body)
	}
}

// TestNonExpiredTokenAccepted verifies that a token with TTL set but not yet
// elapsed is still accepted — backward-compatible path.
func TestNonExpiredTokenAccepted(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, err := reg.Create(CreateParams{
		Name:     "live-app",
		OwnerID:  "alice",
		Products: []string{ProductOffice},
		TokenTTL: 10 * time.Minute, // well in the future
	})
	if err != nil {
		t.Fatal(err)
	}
	w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("live token should be 200, got %d: %s", w.Code, w.Body)
	}
}

// TestNoTTLTokenRemainsValid confirms backward compatibility: apps created
// without a TTL are not affected by the expiry check.
func TestNoTTLTokenRemainsValid(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, err := reg.Create(CreateParams{
		Name:     "no-ttl",
		OwnerID:  "alice",
		Products: []string{ProductOffice},
		// TokenTTL intentionally omitted — legacy behavior.
	})
	if err != nil {
		t.Fatal(err)
	}
	w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("no-TTL token should remain valid forever, got %d: %s", w.Code, w.Body)
	}
}

// TestAgeRevocationViaTokenTTLUpdate verifies that setting TokenTTL via Update
// retroactively revokes tokens that are older than the new TTL (age-revocation).
func TestAgeRevocationViaTokenTTLUpdate(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, err := reg.Create(CreateParams{
		Name:     "age-revoke",
		OwnerID:  "alice",
		Products: []string{ProductOffice},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Token should be valid before the policy is set.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token)); w.Code != http.StatusOK {
		t.Fatalf("token should be valid before TTL policy, got %d", w.Code)
	}

	// Wait a tiny bit so the token's age is measurable, then set a sub-millisecond TTL.
	time.Sleep(5 * time.Millisecond)
	ttl := time.Millisecond
	if _, err := reg.Update(c.App.ID, UpdateParams{TokenTTL: &ttl}); err != nil {
		t.Fatal(err)
	}

	// The existing token is now older than the TTL → 401.
	w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("age-revoked token should be 401, got %d: %s", w.Code, w.Body)
	}
}

// TestRotateTokenResetsTTL verifies that rotating a token on an app with a TTL
// policy resets TokenIssuedAt so the new token starts a fresh TTL window.
func TestRotateTokenResetsTTL(t *testing.T) {
	reg := NewMemoryRegistry()
	c, err := reg.Create(CreateParams{
		Name:     "rotate-ttl",
		OwnerID:  "alice",
		Products: []string{ProductOffice},
		TokenTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := c.App.TokenIssuedAt
	time.Sleep(2 * time.Millisecond)

	newTok, err := reg.RotateToken(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := reg.GetByTokenHash(HashToken(newTok))
	if !app.TokenIssuedAt.After(before) {
		t.Fatal("TokenIssuedAt should be reset on rotate")
	}
	if app.TokenExpiresAt.IsZero() {
		t.Fatal("TokenExpiresAt should be set after rotate with TTL policy")
	}
	if app.TokenExpiresAt.Before(time.Now()) {
		t.Fatal("new token should not already be expired after rotate")
	}
}

// TestSQLitePersistsTTLFields confirms token timing fields survive a DB close/reopen.
func TestSQLitePersistsTTLFields(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ttl.db")
	enc := testEncryptor(t)
	r1, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r1.Create(CreateParams{
		Name:     "ttl-persist",
		OwnerID:  "a",
		Products: []string{ProductOffice},
		TokenTTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	r1.Close()

	r2, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()

	app, err := r2.Get(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if app.TokenTTL != 5*time.Minute {
		t.Fatalf("TokenTTL not persisted: %v", app.TokenTTL)
	}
	if app.TokenIssuedAt.IsZero() {
		t.Fatal("TokenIssuedAt not persisted")
	}
	if app.TokenExpiresAt.IsZero() {
		t.Fatal("TokenExpiresAt not persisted")
	}
}

// ---- Fix 2: Default rate-limit in the library --------------------------------

// TestRateLimitTriggersOn429 creates a limiter with a tiny burst and hammers
// the auth.test endpoint — eventually the limiter must respond 429.
func TestRateLimitTriggersOn429(t *testing.T) {
	reg := NewMemoryRegistry()
	ad := &fakeAdapter{}
	// Burst=1, rate=1: second request from the same IP triggers the limit.
	limiter := NewTokenBucketLimiter(1, 1, 1, 1)
	h, err := NewHandler(MountConfig{
		Adapter:    ad,
		Registry:   reg,
		Dispatcher: NewDispatcher(reg, ProductOffice),
		Admin:      headerAdmin,
		Limiter:    limiter,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := reg.Create(CreateParams{Name: "rl", OwnerID: "a", Products: []string{ProductOffice}})

	got429 := false
	for i := 0; i < 20; i++ {
		w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("rate limit should have triggered 429 within 20 rapid requests")
	}
}

// TestNoopRateLimiterNeverBlocks confirms NoopRateLimiter allows all requests so
// operators can disable rate limiting explicitly.
func TestNoopRateLimiterNeverBlocks(t *testing.T) {
	lim := NoopRateLimiter{}
	for i := 0; i < 1000; i++ {
		if !lim.AllowToken("any") || !lim.AllowIP("1.2.3.4") {
			t.Fatal("NoopRateLimiter must always allow")
		}
	}
}

// TestDefaultRateLimiterAllowsBurstThenRefills verifies the token-bucket
// semantics: burst is consumed, then tokens refill over time.
func TestDefaultRateLimiterAllowsBurstThenRefills(t *testing.T) {
	// rate=100 /s, burst=3: after 3 requests the 4th is denied.
	lim := NewTokenBucketLimiter(100, 3, 100, 100)
	allowed := 0
	for i := 0; i < 10; i++ {
		if lim.AllowToken("tok") {
			allowed++
		}
	}
	if allowed > 3 {
		t.Fatalf("burst=3 should allow at most 3, allowed %d", allowed)
	}
	if allowed < 1 {
		t.Fatal("burst=3 should allow at least 1")
	}
	// Wait for refill, then at least one more should be allowed.
	time.Sleep(20 * time.Millisecond) // 100 /s → ~2 tokens in 20ms
	if !lim.AllowToken("tok") {
		t.Fatal("token should have refilled after sleep")
	}
}

// ---- Fix 3: Cross-product app creation restricted ----------------------------

// TestCrossProductCreateDeniedForNonAdmin asserts a non-admin cannot register
// an app targeting a product other than the mount's product.
func TestCrossProductCreateDeniedForNonAdmin(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice) // Office mount
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	// Explicit cross-product → 403.
	w := do(h, "POST", "/api/apps", `{"name":"multi","products":["board"]}`, alice)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-product create by non-admin should be 403, got %d: %s", w.Code, w.Body)
	}
	// Multi-product including the mount's own product is also denied for non-admin.
	w = do(h, "POST", "/api/apps", `{"name":"multi","products":["office","board"]}`, alice)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-product (multi) create by non-admin should be 403, got %d: %s", w.Code, w.Body)
	}
}

// TestCrossProductCreateAllowedForAdmin verifies that an admin can register an
// app targeting a product different from the mount's own.
func TestCrossProductCreateAllowedForAdmin(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	admin := map[string]string{"X-User": "root", "X-Admin": "1", "Content-Type": "application/json"}

	w := do(h, "POST", "/api/apps", `{"name":"cross","products":["board"]}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin cross-product create should be 201, got %d: %s", w.Code, w.Body)
	}
}

// TestSameProductCreateStillAllowed confirms that creating an app targeting the
// mount's own product is still allowed without admin rights.
func TestSameProductCreateStillAllowed(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	// Default (no products field) → auto-assigned to mount's product.
	w := do(h, "POST", "/api/apps", `{"name":"self-product"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("same-product create should be 201, got %d: %s", w.Code, w.Body)
	}
	// Explicit same-product.
	w = do(h, "POST", "/api/apps", `{"name":"explicit-self","products":["office"]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("explicit same-product create should be 201, got %d: %s", w.Code, w.Body)
	}
}

// TestCrossProductUpdateDeniedForNonAdmin asserts a non-admin who owns a
// same-product app cannot update it to also target another product — the
// privilege-escalation path that mirrors the create gate.
func TestCrossProductUpdateDeniedForNonAdmin(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice) // Office mount
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	// Create a legitimate same-product (office) app.
	w := do(h, "POST", "/api/apps", `{"name":"echo","products":["office"]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("same-product create should be 201, got %d: %s", w.Code, w.Body)
	}
	var created struct {
		App Summary `json:"app"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created.App.ID

	// Escalation attempt: add "board" to the products set → 403.
	w = do(h, "PUT", "/api/apps/"+id, `{"products":["office","board"]}`, alice)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-product update by non-admin should be 403, got %d: %s", w.Code, w.Body)
	}
	// Pure cross-product replacement is also denied.
	w = do(h, "PUT", "/api/apps/"+id, `{"products":["board"]}`, alice)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-product replacement update by non-admin should be 403, got %d: %s", w.Code, w.Body)
	}
}

// TestSameProductUpdateStillAllowed confirms a non-admin can still update their
// app within the mount's own product (name change, and explicit same-product set).
func TestSameProductUpdateStillAllowed(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	w := do(h, "POST", "/api/apps", `{"name":"echo","products":["office"]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create should be 201, got %d: %s", w.Code, w.Body)
	}
	var created struct {
		App Summary `json:"app"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created.App.ID

	// Name-only update (no products field) → allowed.
	w = do(h, "PUT", "/api/apps/"+id, `{"name":"echo2"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("same-product name update should be 200, got %d: %s", w.Code, w.Body)
	}
	// Explicit same-product set → allowed.
	w = do(h, "PUT", "/api/apps/"+id, `{"products":["office"]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit same-product update should be 200, got %d: %s", w.Code, w.Body)
	}
}

// TestCrossProductUpdateAllowedForAdmin verifies an admin can update an app to
// target a product different from the mount's own (gate unaffected for admins).
func TestCrossProductUpdateAllowedForAdmin(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	admin := map[string]string{"X-User": "root", "X-Admin": "1", "Content-Type": "application/json"}

	w := do(h, "POST", "/api/apps", `{"name":"echo","products":["office"]}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("create should be 201, got %d: %s", w.Code, w.Body)
	}
	var created struct {
		App Summary `json:"app"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created.App.ID

	w = do(h, "PUT", "/api/apps/"+id, `{"products":["office","board"]}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cross-product update should be 200, got %d: %s", w.Code, w.Body)
	}
}

// TestPerOwnerAppQuota asserts a single owner can register up to the quota and
// the next create is rejected with 429, while the quota is per-owner (a
// different owner is unaffected).
func TestPerOwnerAppQuota(t *testing.T) {
	t.Setenv(AppsMaxPerOwnerEnv, "2")
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}
	bob := map[string]string{"X-User": "bob", "Content-Type": "application/json"}

	// Alice creates up to the quota (2) — both succeed.
	for i := 0; i < 2; i++ {
		w := do(h, "POST", "/api/apps", `{"name":"echo"}`, alice)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %d within quota should be 201, got %d: %s", i, w.Code, w.Body)
		}
	}
	// The next create exceeds the quota → 429.
	w := do(h, "POST", "/api/apps", `{"name":"echo"}`, alice)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("create over quota should be 429, got %d: %s", w.Code, w.Body)
	}
	// The quota is per-owner: bob is unaffected and can still register.
	w = do(h, "POST", "/api/apps", `{"name":"echo"}`, bob)
	if w.Code != http.StatusCreated {
		t.Fatalf("different owner should be unaffected by alice's quota, got %d: %s", w.Code, w.Body)
	}
}

// ---- Fix 4: vas_ signing-secret at-rest encryption -------------------------

// TestSigningSecretEncryptedAtRest verifies that the AESGCMEncryptor roundtrips
// the signing secret and that what is written to the DB is NOT the plaintext.
func TestSigningSecretEncryptedAtRest(t *testing.T) {
	kek := make([]byte, 32)
	// Non-zero key derived deterministically for reproducibility.
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	enc, err := NewAESGCMEncryptor(kek)
	if err != nil {
		t.Fatal(err)
	}

	dsn := filepath.Join(t.TempDir(), "enc.db")
	r, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Create(CreateParams{Name: "enc-app", OwnerID: "a", Products: []string{ProductOffice}})
	if err != nil {
		t.Fatal(err)
	}
	plaintextSecret := c.SigningSecret // e.g. "vas_abc123..."

	// The in-memory app carries the plaintext.
	app, err := r.Get(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if app.SigningSecret != plaintextSecret {
		t.Fatalf("in-memory SigningSecret should be plaintext, got %q", app.SigningSecret)
	}

	// Verify the DB row is encrypted: open a RAW connection (no encryptor) and
	// read the signing_secret column directly.
	import_db_check(t, dsn, plaintextSecret)

	r.Close()

	// Reopening with the same encryptor should decrypt transparently.
	r2, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	app2, err := r2.Get(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if app2.SigningSecret != plaintextSecret {
		t.Fatalf("after reopen, SigningSecret = %q, want %q", app2.SigningSecret, plaintextSecret)
	}
	// Signing still works with the decrypted secret.
	ts := NowTimestamp()
	body := []byte(`{"event":"test"}`)
	sig := Sign(ts, body, app2.SigningSecret)
	if !Verify(ts, body, app2.SigningSecret, sig) {
		t.Fatal("signature verification failed after decrypt")
	}
}

// import_db_check opens the SQLite DB without an encryptor and asserts that
// the signing_secret column does NOT contain the plaintext secret.
func import_db_check(t *testing.T, dsn, plaintextSecret string) {
	t.Helper()
	// Use a raw StandaloneRegistry without an encryptor to read the stored value.
	raw, err := NewStandaloneRegistry(dsn)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer raw.Close()
	apps, err := raw.List("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) == 0 {
		t.Fatal("no apps found in DB")
	}
	stored := apps[0].SigningSecret
	if stored == plaintextSecret {
		t.Fatal("signing secret is stored in plaintext — should be encrypted")
	}
	if !strings.HasPrefix(stored, encPrefix) {
		t.Fatalf("stored value should have %q prefix, got %q", encPrefix, stored)
	}
}

// TestAESGCMEncryptorRoundtrip validates Encrypt/Decrypt symmetry and that two
// encryptions of the same plaintext produce distinct ciphertexts (random nonce).
func TestAESGCMEncryptorRoundtrip(t *testing.T) {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 7)
	}
	enc, err := NewAESGCMEncryptor(kek)
	if err != nil {
		t.Fatal(err)
	}
	plain := "vas_supersecretvalue123456789012345678"

	ct1, err := enc.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	ct2, err := enc.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if ct1 == ct2 {
		t.Fatal("two encryptions of the same plaintext should be distinct (random nonce)")
	}
	got1, err := enc.Decrypt(ct1)
	if err != nil {
		t.Fatal(err)
	}
	if got1 != plain {
		t.Fatalf("Decrypt(Encrypt(plain)) = %q, want %q", got1, plain)
	}

	// Plaintext passthrough (no enc: prefix → returned as-is for compat).
	passthrough, err := enc.Decrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if passthrough != plain {
		t.Fatal("plaintext passthrough failed")
	}
}

// TestAESGCMEncryptorBadKey confirms that invalid key sizes are rejected.
func TestAESGCMEncryptorBadKey(t *testing.T) {
	for _, sz := range []int{0, 1, 15, 17, 33} {
		if _, err := NewAESGCMEncryptor(make([]byte, sz)); err == nil {
			t.Errorf("key size %d should have been rejected", sz)
		}
	}
}

// TestLegacyPlaintextSecretReadableWithEncryptor verifies backward compatibility:
// rows written by an older binary (before KEK enforcement) carry a plaintext
// signing_secret with no enc: prefix. When reopened with an AESGCMEncryptor,
// those rows are still readable via the passthrough path in Decrypt.
func TestLegacyPlaintextSecretReadableWithEncryptor(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")

	// Build the encryptor we will use for reopening.
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 3)
	}
	enc, _ := NewAESGCMEncryptor(kek)

	// Bootstrap the schema via a throw-away registry (encryptor required now), then
	// close immediately. We will inject a legacy plaintext row via raw SQL below.
	bootstrap, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Close()

	// Directly insert a row with a plaintext signing_secret to simulate a DB
	// created by a pre-KEK-enforcement binary. The sqlite driver is already
	// registered by the production code in this package.
	plain := GenerateSecret() // e.g. "vas_abc..."
	legacyID := GenerateAppID()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(
		`INSERT INTO apps (id, signing_secret, created_at) VALUES (?, ?, ?)`,
		legacyID, plain, time.Now().UnixNano(),
	)
	db.Close()
	if err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// Reopen WITH encryptor — the plaintext row (no enc: prefix) must pass
	// through AESGCMEncryptor.Decrypt unchanged.
	r2, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()

	app, err := r2.Get(legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if app.SigningSecret != plain {
		t.Fatalf("legacy plaintext secret not readable after adding encryptor: got %q, want %q", app.SigningSecret, plain)
	}
}
