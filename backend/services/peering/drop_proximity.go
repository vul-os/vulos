// drop_proximity.go — Drop: Proximity Code (PEER-35)
//
// Implements the "proximity code" fallback for Drop (see PEERING.md § Drop:
// Proximity Code).  When neither mDNS LAN discovery nor BLE is available
// (remote browser, no bare-metal radio), two users can pair via a 6-digit
// one-time code:
//
//  1. Alice calls POST /api/peering/drop/code/generate → receives {"code":"847291"}
//  2. Alice reads the code to Bob out-of-band (verbally, IM, etc.)
//  3. Bob calls POST /api/peering/drop/code/redeem with {"code":"847291"}
//  4. The server looks up who owns that code and returns their addr.
//  5. If the two instances can reach each other directly (LAN/internet) the
//     rendezvous is done locally; otherwise the request is forwarded to the
//     operator-configured rendezvous service (VULOS_RENDEZVOUS_URL). When no
//     rendezvous is configured, cross-network redemption is disabled and only
//     same-box/LAN redemption works — nothing is sent to any central service.
//
// Codes expire after proxCodeTTL (5 minutes) or on first successful redemption.
//
// Package: peering — same as drop.go; no symbols clash (all new names are
// prefixed prox/Prox).
package peering

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Constants & configuration
// ---------------------------------------------------------------------------

// proxCodeTTL is the lifetime of a generated proximity code.
const proxCodeTTL = 5 * time.Minute

// proxCodeDigits is the number of decimal digits in a proximity code.
const proxCodeDigits = 6

// proxDefaultRendezvousBase is the default rendezvous base URL. It is
// intentionally EMPTY: Vulos the org operates no hosted rendezvous, so there is
// no central endpoint to point at by default. An operator opts in by setting
// VULOS_RENDEZVOUS_URL to their own rendezvous. When it is unset, cross-network
// proximity discovery is disabled and only same-box/LAN redemption works — no
// request is ever made to a service that may not exist.
const proxDefaultRendezvousBase = ""

// proxRendezvousEnvKey is the env-var that overrides the rendezvous base URL.
const proxRendezvousEnvKey = "VULOS_RENDEZVOUS_URL"

// proxRendezvousDisabledLogOnce ensures the "no rendezvous configured" notice
// is logged at most once at startup rather than on every service construction.
var proxRendezvousDisabledLogOnce sync.Once

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

// proxCode holds a generated proximity code and its owner's address.
type proxCode struct {
	Code      string    `json:"code"`
	OwnerAddr string    `json:"owner_addr"` // "host:port" of the generating server
	VulosID   string    `json:"vulos_id"`   // Vulos ID of the generating user
	ExpiresAt time.Time `json:"expires_at"`
}

// proxGenerateRequest is the payload for POST /api/peering/drop/code/generate.
// SelfAddr is the callers's advertised address that a peer should connect to.
type proxGenerateRequest struct {
	SelfAddr string `json:"self_addr"`
}

// proxGenerateResponse is returned by POST /api/peering/drop/code/generate.
type proxGenerateResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// proxRedeemRequest is the payload for POST /api/peering/drop/code/redeem.
type proxRedeemRequest struct {
	Code string `json:"code"`
}

// proxRedeemResponse is returned on successful redemption.
type proxRedeemResponse struct {
	// OwnerAddr is the HTTP base address ("host:port") of the code generator.
	OwnerAddr string `json:"owner_addr"`
	// VulosID is the Vulos ID of the code generator.
	VulosID string `json:"vulos_id"`
}

// proxRendezvousPayload is sent to / received from the remote rendezvous.
type proxRendezvousPayload struct {
	Code      string    `json:"code"`
	OwnerAddr string    `json:"owner_addr"`
	VulosID   string    `json:"vulos_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ---------------------------------------------------------------------------
// ProxService
// ---------------------------------------------------------------------------

// ProxService manages proximity-code generation and redemption for Drop.
// It is safe for concurrent use.
type ProxService struct {
	mu sync.Mutex

	selfVulosID string
	selfAddr    string // default self addr (may be overridden per-request)

	// codes maps a 6-digit string to its entry (only unexpired/unredeemed ones).
	codes map[string]*proxCode

	// rendezvousBase is the base URL of the remote rendezvous service.
	rendezvousBase string

	// httpClient is used for outbound rendezvous calls; tests may replace it.
	httpClient proxHTTPClient
}

// proxHTTPClient is the subset of *http.Client the service uses, allowing
// tests to inject a fake.
type proxHTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewProxService creates a ProxService for the given Vulos ID.
// selfAddr is the default address ("host:port") advertised in generated codes.
// If empty, it is left to the caller to supply per-request.
func NewProxService(selfVulosID, selfAddr string) *ProxService {
	base := os.Getenv(proxRendezvousEnvKey)
	if base == "" {
		base = proxDefaultRendezvousBase
	}
	if base == "" {
		proxRendezvousDisabledLogOnce.Do(func() {
			log.Printf("[peering] no rendezvous configured — proximity discovery disabled (set %s)", proxRendezvousEnvKey)
		})
	}
	return &ProxService{
		selfVulosID:    selfVulosID,
		selfAddr:       selfAddr,
		codes:          make(map[string]*proxCode),
		rendezvousBase: base,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
	}
}

// ---------------------------------------------------------------------------
// Code generation
// ---------------------------------------------------------------------------

// ProxGenerate creates a new 6-digit one-time proximity code.
// selfAddr overrides the default address when non-empty.
func (s *ProxService) ProxGenerate(selfAddr string) (*proxCode, error) {
	if selfAddr == "" {
		selfAddr = s.selfAddr
	}
	code, err := proxRandCode()
	if err != nil {
		return nil, fmt.Errorf("prox generate code: %w", err)
	}

	entry := &proxCode{
		Code:      code,
		OwnerAddr: selfAddr,
		VulosID:   s.selfVulosID,
		ExpiresAt: time.Now().Add(proxCodeTTL),
	}

	s.mu.Lock()
	s.proxPruneExpired()
	s.codes[code] = entry
	s.mu.Unlock()

	log.Printf("[prox] generated code %s (expires %s)", code, entry.ExpiresAt.Format(time.RFC3339))
	return entry, nil
}

// proxRandCode generates a cryptographically random 6-digit decimal string,
// zero-padded to always be exactly proxCodeDigits characters.
func proxRandCode() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(proxCodeDigits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", proxCodeDigits, n), nil
}

// proxPruneExpired removes all expired codes; must be called with s.mu held.
func (s *ProxService) proxPruneExpired() {
	now := time.Now()
	for code, e := range s.codes {
		if now.After(e.ExpiresAt) {
			delete(s.codes, code)
		}
	}
}

// ---------------------------------------------------------------------------
// Code redemption
// ---------------------------------------------------------------------------

// ProxRedeem attempts to redeem a proximity code.
//
// Strategy:
//  1. Check local store (generated by this instance).
//  2. If not found locally, forward the redemption request to the
//     operator-configured rendezvous service (disabled when none is set).
//
// On success the entry is consumed (single-use).
func (s *ProxService) ProxRedeem(ctx context.Context, code string) (*proxRedeemResponse, error) {
	if len(code) != proxCodeDigits {
		return nil, fmt.Errorf("prox: code must be %d digits", proxCodeDigits)
	}

	// 1. Local lookup.
	s.mu.Lock()
	s.proxPruneExpired()
	entry, ok := s.codes[code]
	if ok {
		delete(s.codes, code) // single-use
	}
	s.mu.Unlock()

	if ok {
		log.Printf("[prox] redeemed local code %s → %s", code, entry.OwnerAddr)
		return &proxRedeemResponse{
			OwnerAddr: entry.OwnerAddr,
			VulosID:   entry.VulosID,
		}, nil
	}

	// 2. Rendezvous fallback.
	return s.proxRedeemRemote(ctx, code)
}

// proxRedeemRemote queries the remote rendezvous service for the code.
func (s *ProxService) proxRedeemRemote(ctx context.Context, code string) (*proxRedeemResponse, error) {
	if s.rendezvousBase == "" {
		// No rendezvous configured — cross-network redemption is disabled.
		// Only codes generated on this box (local store) can be redeemed.
		return nil, fmt.Errorf("prox: code %s not found (no rendezvous configured — cross-network proximity discovery disabled)", code)
	}
	url := fmt.Sprintf("%s/api/drop/code/%s", s.rendezvousBase, code)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("prox rendezvous request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prox rendezvous GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("prox: code %s not found (expired or invalid)", code)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prox rendezvous: unexpected status %d", resp.StatusCode)
	}

	var payload proxRendezvousPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("prox rendezvous decode: %w", err)
	}

	log.Printf("[prox] redeemed remote code %s → %s", code, payload.OwnerAddr)
	return &proxRedeemResponse{
		OwnerAddr: payload.OwnerAddr,
		VulosID:   payload.VulosID,
	}, nil
}

// ---------------------------------------------------------------------------
// Rendezvous publishing
// ---------------------------------------------------------------------------

// ProxPublishToRendezvous registers a generated code with the remote
// rendezvous service so peers on other networks can redeem it.
// This is called automatically by the HTTP generate handler.
func (s *ProxService) ProxPublishToRendezvous(ctx context.Context, entry *proxCode) error {
	if s.rendezvousBase == "" {
		// No rendezvous configured — nothing to publish to. Cross-network
		// discovery is disabled; the code still works for same-box/LAN
		// redemption via the local store. Clean no-op, not an error.
		return nil
	}
	payload := proxRendezvousPayload{
		Code:      entry.Code,
		OwnerAddr: entry.OwnerAddr,
		VulosID:   entry.VulosID,
		ExpiresAt: entry.ExpiresAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("prox rendezvous marshal: %w", err)
	}

	url := fmt.Sprintf("%s/api/drop/code", s.rendezvousBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("prox rendezvous publish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("prox rendezvous publish: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("prox rendezvous publish: unexpected status %d", resp.StatusCode)
	}

	log.Printf("[prox] published code %s to rendezvous %s", entry.Code, s.rendezvousBase)
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// RegisterProximityHandlers wires the proximity-code endpoints onto mux.
//
//	POST /api/peering/drop/code/generate  → generate a 6-digit code
//	POST /api/peering/drop/code/redeem    → redeem a code → peer address
func RegisterProximityHandlers(mux *http.ServeMux, svc *ProxService) {
	mux.HandleFunc("/api/peering/drop/code/generate", svc.proxHandleGenerate)
	mux.HandleFunc("/api/peering/drop/code/redeem", svc.proxHandleRedeem)
}

// proxHandleGenerate handles POST /api/peering/drop/code/generate.
func (s *ProxService) proxHandleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proxGenerateRequest
	// Body is optional — ignore decode errors for empty bodies.
	_ = json.NewDecoder(r.Body).Decode(&req)

	entry, err := s.ProxGenerate(req.SelfAddr)
	if err != nil {
		http.Error(w, "failed to generate code: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Best-effort: publish to rendezvous so cross-network peers can redeem.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if pubErr := s.ProxPublishToRendezvous(ctx, entry); pubErr != nil {
			log.Printf("[prox] rendezvous publish error (non-fatal): %v", pubErr)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(proxGenerateResponse{ //nolint:errcheck
		Code:      entry.Code,
		ExpiresAt: entry.ExpiresAt,
	})
}

// proxHandleRedeem handles POST /api/peering/drop/code/redeem.
func (s *ProxService) proxHandleRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req proxRedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Code == "" {
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}

	result, err := s.ProxRedeem(r.Context(), req.Code)
	if err != nil {
		// Distinguish "not found" from internal errors.
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Exported accessors (for tests and health checks)
// ---------------------------------------------------------------------------

// ProxCodeCount returns the number of currently active (unexpired) codes.
func (s *ProxService) ProxCodeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxPruneExpired()
	return len(s.codes)
}

// ProxSetRendezvousBase overrides the rendezvous base URL at runtime.
// Primarily useful for tests.
func (s *ProxService) ProxSetRendezvousBase(base string) {
	s.mu.Lock()
	s.rendezvousBase = base
	s.mu.Unlock()
}

// ProxRendezvousBase returns the current rendezvous base URL.
func (s *ProxService) ProxRendezvousBase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rendezvousBase
}
