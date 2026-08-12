// identity_lifecycle_http.go — session-authed box endpoint for self-revocation.
//
// POST /api/peering/identity/revoke retires THIS node's current identity key: it
// signs a self-revocation, records it (so IsRevoked(head) is true and every wired
// admission/verify point rejects the key), and publishes it on the well-known
// lifecycle bundle so peers ingest and honor it. This is the operator's "my key is
// compromised / I am retiring this identity" action.
package peering

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
)

// RegisterIdentityLifecycleHandlers mounts the self-revocation endpoint on mux.
//
//	POST /api/peering/identity/revoke   (session-authed; signs + publishes a self-revocation)
//
// priv must correspond to the node's current head identity. The handler is
// SESSION-authed: it requires an authenticated OS user (the X-User-ID header set by
// the auth middleware). It deliberately does NOT accept a peer envelope — only the
// box owner may revoke the box's own identity.
func RegisterIdentityLifecycleHandlers(mux *http.ServeMux, store *LifecycleStore, priv ed25519.PrivateKey) {
	if mux == nil || store == nil {
		return
	}
	mux.HandleFunc("POST /api/peering/identity/revoke", func(w http.ResponseWriter, r *http.Request) {
		// Session gate: the auth middleware strips any client-supplied X-User-ID and
		// re-sets it only for an authenticated session, so a non-empty value here
		// means a logged-in OS user. Fail closed otherwise.
		if r.Header.Get("X-User-ID") == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		var req struct {
			Reason string `json:"reason"`
		}
		// Body is optional; ignore decode errors for an empty/absent body.
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)

		cert, err := store.SelfRevoke(priv, req.Reason)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to self-revoke: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "revoked",
			"vulos_id":   cert.VulosID,
			"reason":     cert.Reason,
			"issued_at":  cert.IssuedAt,
			"revocation": cert,
		})
	})
}
