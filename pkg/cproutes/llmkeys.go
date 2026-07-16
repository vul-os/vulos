// llmkeys.go — per-user BYO LLM provider keys (LLM-BYOK-01).
//
// Endpoints (all session-gated; scoped to the authenticated user):
//
//	GET    /api/llm/keys           → { providers: [{provider,created_at,updated_at}] }
//	PUT    /api/llm/keys           { provider, key }  → { provider, stored:true }
//	DELETE /api/llm/keys           { provider }       → { provider, deleted:true }
//
// The raw key is encrypted at rest (AES-256-GCM under the LLM-keys KEK) and is
// NEVER returned by any read path and NEVER logged. GET lists providers only.
package cproutes

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/llmkeys"
)

// WireLLMKeys opens the LLM-keys store (KEK from LLM_KEYS_KEK / INTEGRATIONS_KEK,
// dev fallback under VULOS_DEV) and registers the routes. On a KEK or store-open
// failure the routes are NOT registered and a warning is logged (the feature is
// simply unavailable — it never weakens key custody by storing plaintext).
// Returns a closer (no-op when disabled).
func WireLLMKeys(mux *http.ServeMux, dbDir string, authStore *auth.Store) func() {
	kek, err := llmkeys.LoadKEK()
	if err != nil {
		log.Printf("[llmkeys] WARNING: KEK unavailable (%v) — BYO LLM keys disabled", err)
		return func() {}
	}
	// COORDINATOR: needs setDBDirIfUnset (composition-root helper, routes_developer_keys.go)
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[llmkeys] WARNING: could not set DB dir (%v) — BYO LLM keys disabled", err)
		return func() {}
	}
	db, err := cpdb.Open("llmkeys")
	if err != nil {
		log.Printf("[llmkeys] WARNING: cpdb unavailable (%v) — BYO LLM keys disabled", err)
		return func() {}
	}
	store, err := llmkeys.OpenSQLStore(db, kek)
	if err != nil {
		_ = db.Close()
		log.Printf("[llmkeys] WARNING: store unavailable (%v) — BYO LLM keys disabled", err)
		return func() {}
	}
	log.Printf("[llmkeys] store opened (backend=%s)", db.Backend())
	RegisterLLMKeys(mux, authStore, store)
	log.Println("[llmkeys] BYO LLM key routes registered (GET/PUT/DELETE /api/llm/keys)")
	return func() {
		if cerr := store.Close(); cerr != nil {
			log.Printf("[llmkeys] store close: %v", cerr)
		}
	}
}

// RegisterLLMKeys wires the three endpoints onto mux backed by store.
func RegisterLLMKeys(mux *http.ServeMux, authStore *auth.Store, store llmkeys.Store) {
	mux.HandleFunc("GET /api/llm/keys", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		providers, err := store.List(r.Context(), u.ID)
		if err != nil {
			log.Printf("[llmkeys] list: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if providers == nil {
			providers = []llmkeys.ProviderInfo{}
		}
		httpx.JSON(w, map[string]any{"providers": providers})
	})

	mux.HandleFunc("PUT /api/llm/keys", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			Provider string `json:"provider"`
			Key      string `json:"key"`
		}
		if !decodeLLMBody(w, r, &body) {
			return
		}
		err := store.Put(r.Context(), u.ID, body.Provider, body.Key)
		switch {
		case errors.Is(err, llmkeys.ErrInvalidProvider):
			httpx.Err(w, http.StatusBadRequest, "provider is required")
			return
		case errors.Is(err, llmkeys.ErrEmptyKey):
			httpx.Err(w, http.StatusBadRequest, "key is required")
			return
		case err != nil:
			// Never log the key or the raw error contents that might echo it.
			log.Printf("[llmkeys] put failed account=%s provider=%s", u.ID, llmkeys.NormalizeProvider(body.Provider))
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"provider": llmkeys.NormalizeProvider(body.Provider), "stored": true})
	})

	mux.HandleFunc("DELETE /api/llm/keys", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			Provider string `json:"provider"`
		}
		if !decodeLLMBody(w, r, &body) {
			return
		}
		err := store.Delete(r.Context(), u.ID, body.Provider)
		if errors.Is(err, llmkeys.ErrInvalidProvider) {
			httpx.Err(w, http.StatusBadRequest, "provider is required")
			return
		}
		if err != nil {
			log.Printf("[llmkeys] delete failed account=%s", u.ID)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"provider": llmkeys.NormalizeProvider(body.Provider), "deleted": true})
	})
}

// decodeLLMBody decodes a small JSON body with a cap. Writes a 400 + returns
// false on a malformed body.
func decodeLLMBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(dst); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}
