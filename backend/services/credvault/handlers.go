package credvault

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Handler exposes the credential vault over HTTP. One Handler can serve
// multiple users: each user gets an isolated Vault instance keyed by their
// X-User-ID header.
//
// Routes (all under /api/auth/vault/):
//
//	POST /api/auth/vault/unlock       unlock with master password
//	POST /api/auth/vault/lock         lock (clear key from memory)
//	GET  /api/auth/vault/entries      list entries — metadata only (no passwords)
//	GET  /api/auth/vault/entry/{id}   full entry (vault must be unlocked)
//	POST /api/auth/vault/entry        create entry
//	PUT  /api/auth/vault/entry/{id}   update entry
//	DELETE /api/auth/vault/entry/{id} delete entry
//	POST /api/auth/vault/generate     generate a password
type Handler struct {
	mu     sync.Mutex
	vaults map[string]*Vault // keyed by userID
	newDir func(userID string) string

	// gate throttles failed master-password step-ups on /export (see import.go).
	gate *stepUpGate
}

// stepUp returns the handler's step-up throttle, creating it on first use so a
// zero-value Handler can never silently run without one.
func (h *Handler) stepUp() *stepUpGate {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gate == nil {
		h.gate = &stepUpGate{fails: make(map[string]*stepUpState)}
	}
	return h.gate
}

// NewHandler returns an HTTP handler for the credential vault.
// dirFn maps a userID to the directory where that user's vault files live.
// A sensible default: filepath.Join(home, ".vulos", "auth", "vault", userID).
func NewHandler(dirFn func(userID string) string) *Handler {
	return &Handler{
		vaults: make(map[string]*Vault),
		newDir: dirFn,
	}
}

// RegisterHandlers wires all credential vault routes into mux.
func (h *Handler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/vault/unlock", h.handleUnlock)
	mux.HandleFunc("POST /api/auth/vault/lock", h.handleLock)
	mux.HandleFunc("GET /api/auth/vault/entries", h.handleListEntries)
	mux.HandleFunc("GET /api/auth/vault/entry/{id}", h.handleGetEntry)
	mux.HandleFunc("POST /api/auth/vault/entry", h.handleCreateEntry)
	mux.HandleFunc("PUT /api/auth/vault/entry/{id}", h.handleUpdateEntry)
	mux.HandleFunc("DELETE /api/auth/vault/entry/{id}", h.handleDeleteEntry)
	mux.HandleFunc("POST /api/auth/vault/generate", h.handleGenerate)
}

// vaultFor returns the Vault for userID, creating it if necessary.
func (h *Handler) vaultFor(userID string) (*Vault, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.vaults[userID]
	if !ok {
		var err error
		v, err = New(h.newDir(userID))
		if err != nil {
			return nil, err
		}
		h.vaults[userID] = v
	}
	return v, nil
}

// requireUserID extracts X-User-ID from the request. Returns "" and writes 401
// if missing.
func requireUserID(w http.ResponseWriter, r *http.Request) string {
	uid := r.Header.Get("X-User-ID")
	if uid == "" {
		writeErrCV(w, 401, "unauthorized")
	}
	return uid
}

// ---- Handlers ------------------------------------------------------------

func (h *Handler) handleUnlock(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeErrCV(w, 400, "password required")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	if err := v.Unlock(req.Password); err != nil {
		if errors.Is(err, ErrAlreadyUnlocked) {
			writeJSONCV(w, map[string]string{"status": "already unlocked"})
			return
		}
		if errors.Is(err, ErrWrongPassword) {
			writeErrCV(w, 401, "wrong master password")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("unlock: %v", err))
		return
	}

	writeJSONCV(w, map[string]string{"status": "unlocked"})
}

func (h *Handler) handleLock(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	if err := v.Lock(); err != nil {
		if errors.Is(err, ErrAlreadyLocked) {
			writeJSONCV(w, map[string]string{"status": "already locked"})
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("lock: %v", err))
		return
	}

	writeJSONCV(w, map[string]string{"status": "locked"})
}

// entryMeta is a sanitised view of an Entry with the password stripped.
type entryMeta struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Username     string    `json:"username"`
	Notes        string    `json:"notes,omitempty"`
	LinkedTOTPID string    `json:"linked_totp_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toMeta(e Entry) entryMeta {
	return entryMeta{
		ID:           e.ID,
		URL:          e.URL,
		Username:     e.Username,
		Notes:        e.Notes,
		LinkedTOTPID: e.LinkedTOTPID,
		CreatedAt:    e.CreatedAt,
		UpdatedAt:    e.UpdatedAt,
	}
}

func (h *Handler) handleListEntries(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	entries, err := v.ListEntries()
	if err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("list: %v", err))
		return
	}

	metas := make([]entryMeta, len(entries))
	for i, e := range entries {
		metas[i] = toMeta(e)
	}
	writeJSONCV(w, metas)
}

func (h *Handler) handleGetEntry(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeErrCV(w, 400, "entry id required")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	entry, err := v.GetEntry(id)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeErrCV(w, 404, "entry not found")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("get entry: %v", err))
		return
	}

	writeJSONCV(w, entry)
}

func (h *Handler) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	var req struct {
		URL          string `json:"url"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		Notes        string `json:"notes"`
		LinkedTOTPID string `json:"linked_totp_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrCV(w, 400, "invalid request")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	entry := Entry{
		ID:           uuid.New().String(),
		URL:          req.URL,
		Username:     req.Username,
		Password:     req.Password,
		Notes:        req.Notes,
		LinkedTOTPID: req.LinkedTOTPID,
	}

	if err := v.AddEntry(entry); err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("add entry: %v", err))
		return
	}

	w.WriteHeader(201)
	writeJSONCV(w, entry)
}

func (h *Handler) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeErrCV(w, 400, "entry id required")
		return
	}

	var req struct {
		URL          string `json:"url"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		Notes        string `json:"notes"`
		LinkedTOTPID string `json:"linked_totp_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrCV(w, 400, "invalid request")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	// Read existing to preserve CreatedAt.
	existing, err := v.GetEntry(id)
	if err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeErrCV(w, 404, "entry not found")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("get entry: %v", err))
		return
	}

	existing.URL = req.URL
	existing.Username = req.Username
	existing.Password = req.Password
	existing.Notes = req.Notes
	existing.LinkedTOTPID = req.LinkedTOTPID

	if err := v.UpdateEntry(existing); err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeErrCV(w, 404, "entry not found")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("update entry: %v", err))
		return
	}

	writeJSONCV(w, existing)
}

func (h *Handler) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeErrCV(w, 400, "entry id required")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}

	if err := v.DeleteEntry(id); err != nil {
		if errors.Is(err, ErrLocked) {
			writeErrCV(w, 423, "vault is locked")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeErrCV(w, 404, "entry not found")
			return
		}
		writeErrCV(w, 500, fmt.Sprintf("delete entry: %v", err))
		return
	}

	writeJSONCV(w, map[string]string{"status": "deleted"})
}

func (h *Handler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	// No vault unlock required — generation is stateless.
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	var req struct {
		Mode       string           `json:"mode"` // "random" or "passphrase"
		Random     RandomConfig     `json:"random"`
		Passphrase PassphraseConfig `json:"passphrase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrCV(w, 400, "invalid request")
		return
	}

	var mode GeneratorMode
	switch req.Mode {
	case "passphrase":
		mode = ModePassphrase
	default:
		mode = ModeRandom
	}

	cfg := GeneratorConfig{
		Mode:       mode,
		Random:     req.Random,
		Passphrase: req.Passphrase,
	}

	password, err := Generate(cfg)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("generate: %v", err))
		return
	}

	writeJSONCV(w, map[string]string{"password": password})
}

// ---- JSON helpers --------------------------------------------------------

func writeJSONCV(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErrCV(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
