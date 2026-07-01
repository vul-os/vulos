package peering

// content_key_lookup.go — WAVE-7 INTERNAL content-key lookup for the Vulos cell.
//
// ─── Why this exists (closes the F2 recipient-targeting gap) ──────────────────
//
// Content-blind (VSEAL1) file shares are relayed by the Vulos cell, which is
// content-blind: it verifies a pulled blob is a sealed envelope but holds no key to
// open it. As an INTEGRITY gate the cell also checks the seal is WRAPPED TO the
// recipient's published X25519 content key (contentseal.VerifyTargets) — but that
// check only runs when the cell can look the key up. This endpoint is that lookup:
// the cell (vulos-cloud cloudhome.httpContentKeyLookup, via CLOUDHOME_CONTENT_KEY_URL)
// calls it to resolve a recipient account's published content public key.
//
// It is NOT a confidentiality control — the recipient pubkey is public and the
// recipient proves decryptability on download. It only stops the cell staging a
// seal that names the wrong recipient (a griefing / misaddressed share).
//
// ─── Trust model (internal cell↔box, NOT a client header) ─────────────────────
//
// Gated by a shared secret (CP_SHARED_SECRET) presented in ContentKeyAuthHeader and
// compared in constant time — the same box↔control-plane trust model as the billing
// (X-Relay-Auth) and LAN-cert (X-Device-Auth) seams. It is NOT authenticated by a
// spoofable identity header: the session middleware strips X-User-ID, and this path
// is listed in auth.publicPaths so it is reached by the internal caller and gated
// solely by the secret. Fail-closed: no secret configured → 503; wrong secret →
// 401; missing account → 400. A 503/401/timeout on the caller side MUST fail closed
// (the cell must not fall open and accept an untargeted seal).
//
// ─── Wire contract (single-sourced; the cloud client must match) ──────────────
//
//	GET /api/files/internal/content-key?account=<accountID>
//	Header: X-Vulos-Internal-Auth: <CP_SHARED_SECRET>
//	200 → {"content_pub_key":"<base64-std X25519 pub, or empty if unpublished>"}
//
// NOTE (single-identity box): a vulos box hosts ONE peering identity/profile, so it
// answers with THAT profile's published content key — correct for the account whose
// cloud-home lives on this box. A future multi-account cell would swap the resolver
// for a per-account directory lookup; the endpoint + auth + fail-closed contract are
// unchanged.

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// ContentKeyAuthHeader carries the CP_SHARED_SECRET on the internal content-key
// lookup. Part of the cross-repo wire contract.
const ContentKeyAuthHeader = "X-Vulos-Internal-Auth"

// ContentKeyLookupPath is the internal content-key lookup route. Listed in
// auth.publicPaths (secret-gated, not session-gated).
const ContentKeyLookupPath = "/api/files/internal/content-key"

// RegisterContentKeyLookup wires GET /api/files/internal/content-key onto mux.
// store supplies the published content key; sharedSecret gates access (empty =
// endpoint disabled → 503, which the cell treats as fail-closed).
func RegisterContentKeyLookup(mux *http.ServeMux, store *ProfileStore, sharedSecret string) {
	mux.HandleFunc("GET "+ContentKeyLookupPath, func(w http.ResponseWriter, r *http.Request) {
		if sharedSecret == "" {
			http.Error(w, `{"error":"content-key lookup not configured"}`, http.StatusServiceUnavailable)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(ContentKeyAuthHeader)), []byte(sharedSecret)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("account") == "" {
			http.Error(w, `{"error":"account required"}`, http.StatusBadRequest)
			return
		}
		pub := ""
		if store != nil {
			pub = store.ContentPubKey()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"content_pub_key": pub})
	})
}
