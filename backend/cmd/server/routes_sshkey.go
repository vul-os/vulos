package main

import (
	"encoding/json"
	"log"
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/sshkey"
)

// registerSSHKeyRoutes wires SSH key management endpoints into mux.
// All endpoints require admin authentication.
func registerSSHKeyRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	authKeys, err := sshkey.Open(home)
	if err != nil {
		log.Printf("[sshkey] authorized_keys store init warning: %v", err)
	}

	// adminGate checks that the request carries admin credentials.
	// Returns true if the caller is allowed to proceed.
	adminGate := func(w http.ResponseWriter, r *http.Request) bool {
		p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
		if p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin only")
			return false
		}
		return true
	}

	// GET /api/ssh/hostkey — return the public host key and its SHA256 fingerprint.
	mux.HandleFunc("GET /api/ssh/hostkey", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(w, r) {
			return
		}
		pub, fp, err := sshkey.EnsureHostKey(home)
		if err != nil {
			log.Printf("[sshkey] EnsureHostKey: %v", err)
			writeErr(w, 500, "failed to load host key")
			return
		}
		log.Printf("[sshkey] GET /api/ssh/hostkey user=%s fp=%s", r.Header.Get("X-User-ID"), fp)
		writeJSON(w, map[string]string{
			"pubkey":      string(pub),
			"fingerprint": fp,
		})
	})

	// GET /api/ssh/authorized — list all authorized public keys.
	mux.HandleFunc("GET /api/ssh/authorized", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(w, r) {
			return
		}
		if authKeys == nil {
			writeErr(w, 503, "authorized_keys store not available")
			return
		}
		keys, err := authKeys.List()
		if err != nil {
			log.Printf("[sshkey] List: %v", err)
			writeErr(w, 500, err.Error())
			return
		}
		log.Printf("[sshkey] GET /api/ssh/authorized user=%s count=%d", r.Header.Get("X-User-ID"), len(keys))
		if keys == nil {
			keys = []sshkey.AuthorizedKey{}
		}
		writeJSON(w, keys)
	})

	// POST /api/ssh/authorized — add an authorized public key.
	// Body: { "comment": "...", "pubkey": "ssh-ed25519 AAAA..." }
	mux.HandleFunc("POST /api/ssh/authorized", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(w, r) {
			return
		}
		if authKeys == nil {
			writeErr(w, 503, "authorized_keys store not available")
			return
		}
		var req struct {
			Comment string `json:"comment"`
			PubKey  string `json:"pubkey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PubKey == "" {
			writeErr(w, 400, "pubkey required")
			return
		}
		if err := authKeys.Add(req.Comment, req.PubKey); err != nil {
			log.Printf("[sshkey] Add: %v", err)
			writeErr(w, 400, err.Error())
			return
		}
		log.Printf("[sshkey] POST /api/ssh/authorized user=%s comment=%q", r.Header.Get("X-User-ID"), req.Comment)
		writeJSON(w, map[string]string{"status": "added"})
	})

	// DELETE /api/ssh/authorized/{fingerprint} — remove an authorized key by fingerprint.
	mux.HandleFunc("DELETE /api/ssh/authorized/{fingerprint}", func(w http.ResponseWriter, r *http.Request) {
		if !adminGate(w, r) {
			return
		}
		if authKeys == nil {
			writeErr(w, 503, "authorized_keys store not available")
			return
		}
		fp := r.PathValue("fingerprint")
		if fp == "" {
			writeErr(w, 400, "fingerprint required")
			return
		}
		if err := authKeys.Remove(fp); err != nil {
			log.Printf("[sshkey] Remove fp=%s: %v", fp, err)
			writeErr(w, 404, err.Error())
			return
		}
		log.Printf("[sshkey] DELETE /api/ssh/authorized/%s user=%s", fp, r.Header.Get("X-User-ID"))
		writeJSON(w, map[string]string{"status": "removed"})
	})
}
