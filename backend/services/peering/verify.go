// Package peering implements Vula OS peer-to-peer communication services.
// This file handles vulos.org email verification for peering identities.
package peering

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// verifyDefaultBaseURL is the default vulos.org API base URL.
// Override with the VULOS_VERIFY_URL environment variable.
const verifyDefaultBaseURL = "https://vulos.org"

// verifyTokenFile is the filename used to persist the signed verification token.
const verifyTokenFile = "verify_token.json"

// VerifyToken is the signed verification token returned by vulos.org after a
// successful email verification confirmation. It proves "this Vula ID controls
// <email>" without exposing the email to every peer.
type VerifyToken struct {
	// VulaID is the cryptographic identity string (e.g. "vula:ed25519:<base58-pubkey>").
	VulaID string `json:"vula_id"`
	// Email is the verified email address.
	Email string `json:"email"`
	// IssuedAt is a Unix timestamp (seconds) of when the token was issued.
	IssuedAt int64 `json:"issued_at"`
	// Signature is a base64-encoded ed25519 signature over the canonical payload,
	// produced by the vulos.org verification key.
	Signature string `json:"signature"`
	// SigningKey is the base64-encoded ed25519 public key used to produce Signature.
	// Clients verify this key against a pinned or well-known vulos.org key.
	SigningKey string `json:"signing_key"`
}

// verifyCanonicalPayload returns the deterministic bytes that the vulos.org
// server signs, and that we must verify locally.
func verifyCanonicalPayload(t *VerifyToken) []byte {
	return []byte(fmt.Sprintf("vulos.org:verify:%s:%s:%d", t.VulaID, t.Email, t.IssuedAt))
}

// verifySignatureValid reports whether the token's Signature was produced by
// SigningKey over the canonical payload.
func verifySignatureValid(t *VerifyToken) error {
	sigBytes, err := base64.StdEncoding.DecodeString(t.Signature)
	if err != nil {
		return fmt.Errorf("verify: decode signature: %w", err)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(t.SigningKey)
	if err != nil {
		return fmt.Errorf("verify: decode signing key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("verify: signing key has wrong length %d (want %d)", len(keyBytes), ed25519.PublicKeySize)
	}
	pub := ed25519.PublicKey(keyBytes)
	payload := verifyCanonicalPayload(t)
	if !ed25519.Verify(pub, payload, sigBytes) {
		return errors.New("verify: signature is invalid")
	}
	return nil
}

// VerifyService manages email verification state for a single Vula identity.
// It calls the vulos.org /api/verify/* endpoints and persists the resulting
// token to disk.  Instances that have not completed verification continue to
// function normally — only VerifiedEmail() returns a non-empty value.
type VerifyService struct {
	mu      sync.RWMutex
	token   *VerifyToken // nil until verified
	dataDir string       // ~/.vulos/peering/identity/
	baseURL string       // vulos.org API base URL (configurable via env)
	client  *http.Client // injectable for tests
}

// VerifyNewService creates a VerifyService that stores its token under dataDir
// and reads the API base URL from VULOS_VERIFY_URL (falls back to the default).
// Any previously stored token is loaded and validated on construction.
func VerifyNewService(dataDir string) (*VerifyService, error) {
	baseURL := strings.TrimRight(os.Getenv("VULOS_VERIFY_URL"), "/")
	if baseURL == "" {
		baseURL = verifyDefaultBaseURL
	}
	svc := &VerifyService{
		dataDir: dataDir,
		baseURL: baseURL,
		client:  &http.Client{},
	}
	// Best-effort load of a pre-existing token; ignore absence or corruption.
	_ = svc.verifyLoadToken()
	return svc, nil
}

// verifyLoadToken reads and validates a persisted token from disk.
func (s *VerifyService) verifyLoadToken() error {
	path := filepath.Join(s.dataDir, verifyTokenFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var t VerifyToken
	if err := json.Unmarshal(data, &t); err != nil {
		return fmt.Errorf("verify: load token: %w", err)
	}
	if err := verifySignatureValid(&t); err != nil {
		return err
	}
	s.mu.Lock()
	s.token = &t
	s.mu.Unlock()
	return nil
}

// verifySaveToken persists the token to disk under dataDir.
func (s *VerifyService) verifySaveToken(t *VerifyToken) error {
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("verify: create dir: %w", err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("verify: marshal token: %w", err)
	}
	path := filepath.Join(s.dataDir, verifyTokenFile)
	return os.WriteFile(path, data, 0o600)
}

// VerifiedEmail returns the verified email address attached to this identity,
// or an empty string if the instance has not completed email verification.
// Callers must treat an empty return as "unverified" — not an error.
func (s *VerifyService) VerifiedEmail() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.token == nil {
		return ""
	}
	return s.token.Email
}

// VerifiedToken returns the raw VerifyToken or nil if not yet verified.
// The returned pointer is a copy — callers must not modify it.
func (s *VerifyService) VerifiedToken() *VerifyToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.token == nil {
		return nil
	}
	cp := *s.token
	return &cp
}

// verifySend calls POST /api/verify/send on vulos.org, initiating the
// email verification flow. The caller supplies the Vula ID string (e.g.
// "vula:ed25519:<pubkey>") and the email address to verify.
func (s *VerifyService) verifySend(vulaID, email string) error {
	body := fmt.Sprintf(`{"vula_id":%q,"email":%q}`, vulaID, email)
	resp, err := s.client.Post(
		s.baseURL+"/api/verify/send",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("verify: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("verify: send: server returned %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

// verifyConfirm calls POST /api/verify/confirm on vulos.org with the 6-digit
// code the user received by email. On success the server returns a signed
// VerifyToken which is validated and persisted to disk.
func (s *VerifyService) verifyConfirm(vulaID, email, code string) error {
	body := fmt.Sprintf(`{"vula_id":%q,"email":%q,"code":%q}`, vulaID, email, code)
	resp, err := s.client.Post(
		s.baseURL+"/api/verify/confirm",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("verify: confirm request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("verify: confirm: server returned %d: %s", resp.StatusCode, string(raw))
	}
	var t VerifyToken
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return fmt.Errorf("verify: confirm: decode token: %w", err)
	}
	if err := verifySignatureValid(&t); err != nil {
		return err
	}
	if err := s.verifySaveToken(&t); err != nil {
		return err
	}
	s.mu.Lock()
	s.token = &t
	s.mu.Unlock()
	return nil
}

// RegisterVerifyHandlers registers the two peering email verification HTTP
// endpoints into mux:
//
//	POST /api/peering/identity/verify   — initiate verification (send code)
//	POST /api/peering/identity/confirm  — submit code, receive + store token
//
// Both handlers are self-contained and require no authentication beyond what
// the caller's mux already enforces. The Vula ID and email are supplied by the
// request body so that this file never needs to import an identity package.
func RegisterVerifyHandlers(mux *http.ServeMux, svc *VerifyService) {
	mux.HandleFunc("POST /api/peering/identity/verify", svc.handleVerifySend)
	mux.HandleFunc("POST /api/peering/identity/confirm", svc.handleVerifyConfirm)
	mux.HandleFunc("GET /api/peering/identity/verify/status", svc.handleVerifyStatus)
}

// handleVerifySend handles POST /api/peering/identity/verify.
// Expected JSON body: {"vula_id":"vula:ed25519:...","email":"user@example.com"}
func (s *VerifyService) handleVerifySend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VulaID string `json:"vula_id"`
		Email  string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.VulaID == "" || req.Email == "" {
		http.Error(w, "vula_id and email are required", http.StatusBadRequest)
		return
	}
	if err := s.verifySend(req.VulaID, req.Email); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"code_sent"}`))
}

// handleVerifyConfirm handles POST /api/peering/identity/confirm.
// Expected JSON body: {"vula_id":"vula:ed25519:...","email":"user@example.com","code":"123456"}
func (s *VerifyService) handleVerifyConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VulaID string `json:"vula_id"`
		Email  string `json:"email"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.VulaID == "" || req.Email == "" || req.Code == "" {
		http.Error(w, "vula_id, email and code are required", http.StatusBadRequest)
		return
	}
	if err := s.verifyConfirm(req.VulaID, req.Email, req.Code); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	t := s.VerifiedToken()
	data, _ := json.Marshal(t)
	_, _ = w.Write(data)
}

// handleVerifyStatus handles GET /api/peering/identity/verify/status.
// Returns {"verified":true,"email":"..."} or {"verified":false}.
// Unverified instances return HTTP 200 with verified:false — this is not an error.
func (s *VerifyService) handleVerifyStatus(w http.ResponseWriter, r *http.Request) {
	email := s.VerifiedEmail()
	w.Header().Set("Content-Type", "application/json")
	if email == "" {
		_, _ = w.Write([]byte(`{"verified":false}`))
		return
	}
	resp := fmt.Sprintf(`{"verified":true,"email":%q}`, email)
	_, _ = w.Write([]byte(resp))
}
