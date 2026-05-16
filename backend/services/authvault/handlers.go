package authvault

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Handler exposes TOTP store operations over HTTP.
// Each authenticated user gets an isolated store under ~/.vulos/auth/totp/<userID>/.
type Handler struct {
	mu     sync.Mutex
	stores map[string]*Store // userID → *Store
}

// NewHandler creates a TOTP HTTP handler.
func NewHandler() *Handler {
	return &Handler{stores: make(map[string]*Store)}
}

// RegisterHandlers wires the 4 TOTP endpoints into mux.
func (h *Handler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/totp/add", h.handleAdd)
	mux.HandleFunc("GET /api/auth/totp/list", h.handleList)
	mux.HandleFunc("GET /api/auth/totp/code/{id}", h.handleCode)
	mux.HandleFunc("DELETE /api/auth/totp/{id}", h.handleDelete)
}

// storeFor returns (or lazily opens) the per-user TOTP store.
func (h *Handler) storeFor(userID string) (*Store, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.stores[userID]; ok {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("authvault: cannot determine home dir: %w", err)
	}
	dir := filepath.Join(home, ".vulos", "auth", "totp", userID)
	s, err := NewStoreAt(dir)
	if err != nil {
		return nil, err
	}
	h.stores[userID] = s
	return s, nil
}

// ─── Endpoint handlers ────────────────────────────────────────────────────────

// POST /api/auth/totp/add
// Body: { "uri": "otpauth://totp/..." }
// or:   { "service": "...", "issuer": "...", "account": "...", "secret": "..." }
func (h *Handler) handleAdd(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	var req struct {
		URI     string `json:"uri"`
		Service string `json:"service"`
		Issuer  string `json:"issuer"`
		Account string `json:"account"`
		Secret  string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, 400, "invalid request body")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	var acc *Account
	if req.URI != "" {
		acc, err = store.AddFromURI(req.URI)
	} else {
		if req.Service == "" || req.Secret == "" {
			writeJSONErr(w, 400, "uri or (service + secret) required")
			return
		}
		acc, err = store.AddManual(req.Service, req.Issuer, req.Account, req.Secret)
	}
	if err != nil {
		writeJSONErr(w, 400, err.Error())
		return
	}

	writeJSONOK(w, acc)
}

// GET /api/auth/totp/list
func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	writeJSONOK(w, store.List())
}

// GET /api/auth/totp/code/{id}
func (h *Handler) handleCode(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, 400, "id required")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	code, remaining, err := store.Code(id)
	if err != nil {
		writeJSONErr(w, 404, err.Error())
		return
	}

	writeJSONOK(w, map[string]any{
		"id":                id,
		"code":              code,
		"remaining_seconds": int(remaining.Seconds()),
	})
}

// DELETE /api/auth/totp/{id}
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, 400, "id required")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	if err := store.Delete(id); err != nil {
		writeJSONErr(w, 404, err.Error())
		return
	}

	writeJSONOK(w, map[string]string{"status": "deleted"})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
