// opaque.go -- OPAQUE (asymmetric PAKE) groundwork (IDENTITY-SERVICE §3).
//
// OPAQUE lets the server verify a login password WITHOUT ever seeing it or
// storing a password-equivalent hash — closing the one residual seam where
// "central identity" could become "central access to content keys" (§3.1). This
// file is the SERVER half, built on the VETTED github.com/bytemare/opaque library
// (a maintained implementation tracking the IETF CFRG OPAQUE draft). We do NOT
// hand-roll the OPRF/AKE.
//
// STATUS: additive + OFF BY DEFAULT. argon2id (see argon2.go / Login) remains the
// live login path. Everything here is gated behind AUTH_OPAQUE_ENABLED and is
// dormant until explicitly enabled. There is NO change to existing auth behaviour
// — the existing POST /api/auth/login is untouched.
//
// Pinned ciphersuite (recorded per §3.2, "pick ONE ciphersuite and pin it"):
// ristretto255 + SHA-512 + Argon2id KSF, i.e. bytemare/opaque DefaultConfiguration.
// The suite id string is stored alongside every record so a future migration can
// detect and re-register mismatched records.
package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bytemare/opaque"
	"github.com/bytemare/opaque/message"

	"github.com/vul-os/vulos-management/pkg/env"
)

// OpaqueSuiteID is the pinned ciphersuite identifier stored with every OPAQUE
// record. It is intentionally opaque/versioned so a future ciphersuite change is
// detectable per-record.
const OpaqueSuiteID = "ristretto255-sha512-argon2id/v1"

// opaqueServerIdentity returns the server's stable AKE identity, bound into
// every handshake. It MUST be identical at registration and login.
// bytemare/opaque's ServerKeyMaterial.Encode/Decode also requires a
// NON-EMPTY Identity vector (an empty trailing vector fails
// DecodeLongVector), so this doubles as that.
//
// DOMAIN-CONSOLIDATION-01: derived from env.Domain() (the single source of
// truth every other identity string in this codebase uses) rather than a
// hardcoded "vulos.to" literal — safe to change today because OPAQUE is
// OFF BY DEFAULT / dormant (see the package doc comment): no opaque_record
// row has ever been written against the old literal in production.
func opaqueServerIdentity() string {
	return "idp." + env.Domain()
}

var (
	// ErrOpaqueDisabled is returned when an OPAQUE operation is attempted while
	// the feature flag is off. The live path stays argon2id.
	ErrOpaqueDisabled = errors.New("auth: OPAQUE is disabled (AUTH_OPAQUE_ENABLED unset)")

	// ErrOpaqueNotConfigured is returned when OPAQUE is enabled but no server key
	// material (OPAQUE_SERVER_KEY) is configured. Fail-closed.
	ErrOpaqueNotConfigured = errors.New("auth: OPAQUE enabled but OPAQUE_SERVER_KEY not set")

	// ErrOpaqueNoRecord is returned when no opaque_record exists for a user.
	ErrOpaqueNoRecord = errors.New("auth: no OPAQUE record for user")

	// ErrOpaqueHandshake is returned on any handshake / MAC verification failure.
	// Deliberately generic — no oracle about which step failed.
	ErrOpaqueHandshake = errors.New("auth: OPAQUE handshake failed")
)

// opaqueLoginTTL bounds how long an in-flight OPAQUE login handshake (KE1→KE2,
// then KE3) may take before its staged expected-MAC/session-secret row expires.
const opaqueLoginTTL = 2 * time.Minute

// OpaqueEnabled reports whether the OPAQUE feature flag is on. When false, the
// server behaves exactly as before (argon2id only).
func OpaqueEnabled() bool {
	v := os.Getenv("AUTH_OPAQUE_ENABLED")
	return v == "1" || v == "true"
}

// OpaqueService is the server-side OPAQUE handshake engine bound to the auth
// Store. It holds the pinned configuration and the loaded server key material.
// Construct via NewOpaqueService; nil when OPAQUE is disabled/unconfigured.
type OpaqueService struct {
	st   *Store
	conf *opaque.Configuration
	skm  *opaque.ServerKeyMaterial
}

// NewOpaqueService builds the OPAQUE service for st. Returns (nil, ErrOpaqueDisabled)
// when the flag is off (caller treats OPAQUE endpoints as unavailable), and
// (nil, ErrOpaqueNotConfigured) when enabled without OPAQUE_SERVER_KEY.
//
// OPAQUE_SERVER_KEY is the hex-encoded ServerKeyMaterial (see
// GenerateOpaqueServerKeyHex) and lives ONLY in the idp/auth process env (§6
// secret isolation).
func NewOpaqueService(st *Store) (*OpaqueService, error) {
	if !OpaqueEnabled() {
		return nil, ErrOpaqueDisabled
	}
	keyHex := os.Getenv("OPAQUE_SERVER_KEY")
	if keyHex == "" {
		return nil, ErrOpaqueNotConfigured
	}
	conf := opaque.DefaultConfiguration()
	raw, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("auth: decode OPAQUE_SERVER_KEY: %w", err)
	}
	skm, err := conf.DecodeServerKeyMaterial(raw)
	if err != nil {
		return nil, fmt.Errorf("auth: parse OPAQUE server key material: %w", err)
	}
	return &OpaqueService{st: st, conf: conf, skm: skm}, nil
}

// GenerateOpaqueServerKeyHex mints fresh server key material (AKE keypair + OPRF
// global seed) for the pinned suite and returns it hex-encoded, suitable for the
// OPAQUE_SERVER_KEY env var. Used by an operator setup command / test bootstrap;
// the key is long-term and must be kept secret and stable.
func GenerateOpaqueServerKeyHex() (string, error) {
	conf := opaque.DefaultConfiguration()
	sk, pk := conf.KeyGen()
	skm := &opaque.ServerKeyMaterial{
		PrivateKey:     sk,
		PublicKeyBytes: pk.Encode(),
		OPRFGlobalSeed: conf.GenerateOPRFSeed(),
		Identity:       []byte(opaqueServerIdentity()),
	}
	return skm.Hex(), nil
}

// ─── Registration (server half) ────────────────────────────────────────────────

// OpaqueRegistrationResponse computes the server's response to a client's OPAQUE
// registration request. credentialID is the stable per-user identifier — we use
// the user's account id. The returned bytes are the serialised
// RegistrationResponse the browser needs to finalise its envelope.
func (o *OpaqueService) OpaqueRegistrationResponse(userID string, requestBytes []byte) ([]byte, error) {
	srv, err := o.server()
	if err != nil {
		return nil, err
	}
	deser, err := o.conf.Deserializer()
	if err != nil {
		return nil, err
	}
	req, err := deser.RegistrationRequest(requestBytes)
	if err != nil {
		return nil, ErrOpaqueHandshake
	}
	resp, err := srv.RegistrationResponse(req, []byte(userID), nil)
	if err != nil {
		return nil, ErrOpaqueHandshake
	}
	return resp.Serialize(), nil
}

// StoreOpaqueRecord persists (dual-writes) the client's final OPAQUE registration
// record for userID. recordBytes is the serialised RegistrationRecord produced by
// the browser. This is the "dual-write on password set" seam (§3.5): callers do
// this ALONGSIDE writing the argon2 hash, never instead of it. Idempotent upsert.
func (o *OpaqueService) StoreOpaqueRecord(ctx context.Context, userID string, recordBytes []byte) error {
	deser, err := o.conf.Deserializer()
	if err != nil {
		return err
	}
	// Validate the record parses under the pinned suite before storing.
	if _, err := deser.RegistrationRecord(recordBytes); err != nil {
		return ErrOpaqueHandshake
	}
	created := now().Format(time.RFC3339)

	o.st.mu.Lock()
	defer o.st.mu.Unlock()
	// DELETE+INSERT keeps upsert dialect-neutral across SQLite/Postgres.
	if _, err := o.st.db.ExecContext(ctx,
		o.st.db.Rebind(`DELETE FROM opaque_records WHERE user_id = ?`), userID,
	); err != nil {
		return fmt.Errorf("auth: clear opaque record: %w", err)
	}
	if _, err := o.st.db.ExecContext(ctx,
		o.st.db.Rebind(`INSERT INTO opaque_records (user_id, envelope, suite, created_at) VALUES (?, ?, ?, ?)`),
		userID, recordBytes, OpaqueSuiteID, created,
	); err != nil {
		return fmt.Errorf("auth: insert opaque record: %w", err)
	}
	return nil
}

// HasOpaqueRecord reports whether userID has an OPAQUE record (i.e. whether the
// OPAQUE path is available for that account). Login's modality machinery can use
// this to prefer OPAQUE when present and fall back to argon2 otherwise (§3.5).
func (o *OpaqueService) HasOpaqueRecord(ctx context.Context, userID string) (bool, error) {
	var n int
	err := o.st.db.QueryRowContext(ctx,
		o.st.db.Rebind(`SELECT COUNT(1) FROM opaque_records WHERE user_id = ?`), userID,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ─── Login (server half) ────────────────────────────────────────────────────────

// OpaqueLoginStart runs the server's KE2 step. Given the user's account id and
// the client's KE1 bytes, it loads the stored record, produces KE2, and stages
// the expected client MAC + session secret in opaque_logins (PRIMARY-only,
// single-use, short TTL). Returns (handshakeID, ke2Bytes). The browser sends
// handshakeID back with its KE3 to OpaqueLoginFinish.
func (o *OpaqueService) OpaqueLoginStart(ctx context.Context, userID string, ke1Bytes []byte) (string, []byte, error) {
	srv, err := o.server()
	if err != nil {
		return "", nil, err
	}
	deser, err := o.conf.Deserializer()
	if err != nil {
		return "", nil, err
	}

	var envelope []byte
	if err := o.st.db.QueryRowContext(ctx,
		o.st.db.Rebind(`SELECT envelope FROM opaque_records WHERE user_id = ?`), userID,
	).Scan(&envelope); err != nil {
		return "", nil, ErrOpaqueNoRecord
	}

	rec, err := deser.RegistrationRecord(envelope)
	if err != nil {
		return "", nil, ErrOpaqueHandshake
	}
	ke1, err := deser.KE1(ke1Bytes)
	if err != nil {
		return "", nil, ErrOpaqueHandshake
	}

	clientRecord := &opaque.ClientRecord{
		RegistrationRecord:   rec,
		CredentialIdentifier: []byte(userID),
		ClientIdentity:       []byte(userID),
	}
	ke2, out, err := srv.GenerateKE2(ke1, clientRecord)
	if err != nil {
		return "", nil, ErrOpaqueHandshake
	}

	handshakeID, err := newSessionToken()
	if err != nil {
		return "", nil, err
	}
	n := now()
	o.st.mu.Lock()
	// Lazy GC of expired handshakes.
	_, _ = o.st.db.ExecContext(ctx,
		o.st.db.Rebind(`DELETE FROM opaque_logins WHERE expires_at < ?`), n.Format(time.RFC3339))
	_, err = o.st.db.ExecContext(ctx,
		o.st.db.Rebind(`INSERT INTO opaque_logins (id, user_id, expected_mac, session_secret, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`),
		handshakeID, userID, out.ClientMAC, out.SessionSecret,
		n.Add(opaqueLoginTTL).Format(time.RFC3339), n.Format(time.RFC3339),
	)
	o.st.mu.Unlock()
	if err != nil {
		return "", nil, fmt.Errorf("auth: stage opaque login: %w", err)
	}
	return handshakeID, ke2.Serialize(), nil
}

// OpaqueLoginFinish verifies the client's KE3 against the staged expected MAC for
// handshakeID. On success it returns the authenticated userID; the caller then
// issues a session via the SAME createSession / 2FA machinery as every other
// login path (§3.5 step 4). The staged row is consumed (single-use) regardless
// of outcome. No session is created here — that stays in the shared login flow.
func (o *OpaqueService) OpaqueLoginFinish(ctx context.Context, handshakeID string, ke3Bytes []byte) (string, error) {
	srv, err := o.server()
	if err != nil {
		return "", err
	}
	deser, err := o.conf.Deserializer()
	if err != nil {
		return "", err
	}

	var (
		userID      string
		expectedMac []byte
		expiresAt   string
	)
	o.st.mu.Lock()
	scanErr := o.st.db.QueryRowContext(ctx,
		o.st.db.Rebind(`SELECT user_id, expected_mac, expires_at FROM opaque_logins WHERE id = ?`), handshakeID,
	).Scan(&userID, &expectedMac, &expiresAt)
	// Single-use: delete regardless of scan outcome.
	_, _ = o.st.db.ExecContext(ctx,
		o.st.db.Rebind(`DELETE FROM opaque_logins WHERE id = ?`), handshakeID)
	o.st.mu.Unlock()
	if scanErr != nil {
		return "", ErrOpaqueHandshake
	}
	if exp, perr := time.Parse(time.RFC3339, expiresAt); perr != nil || now().After(exp) {
		return "", ErrOpaqueHandshake
	}

	ke3, err := deser.KE3(ke3Bytes)
	if err != nil {
		return "", ErrOpaqueHandshake
	}
	if err := srv.LoginFinish(ke3, expectedMac); err != nil {
		return "", ErrOpaqueHandshake
	}
	return userID, nil
}

// server returns a fresh *opaque.Server with key material set. bytemare/opaque's
// Server is safe for concurrent GenerateKE2 across clients, but we build a fresh
// one per operation to keep no shared mutable ceremony state.
func (o *OpaqueService) server() (*opaque.Server, error) {
	srv, err := o.conf.Server()
	if err != nil {
		return nil, err
	}
	if err := srv.SetKeyMaterial(o.skm); err != nil {
		return nil, err
	}
	return srv, nil
}

// compile-time assertion that message types are referenced (keeps the import
// meaningful even if the exported surface changes).
var _ = message.RegistrationRecord{}
