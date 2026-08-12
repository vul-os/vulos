// inbound.go — middleware for inbound server-to-server requests (PEER-04).
//
// InboundMiddleware wraps a http.Handler and enforces two security checks on
// every inbound /api/peering/inbound/* request:
//
//  1. Signature verification (envelope.Verify). The request body must be a
//     JSON-encoded Envelope whose Ed25519 signature is valid over the
//     canonical bytes of the envelope. Missing or invalid signature → 401.
//
//  2. Allow-list check (contacts.IsApproved). The sender (Envelope.From)
//     must be in the local contacts store with StateApproved. Unknown or
//     non-approved senders → 403.
//
// Exception: POST /api/peering/inbound/request (contact-request delivery) is
// exempt from the allow-list check — only signature verification applies.
// This is necessary to allow unknown peers to send their first contact request.
//
// The decoded Envelope is stored in the request context under EnvelopeKey so
// downstream handlers can retrieve it without re-parsing the body.
//
// # Usage
//
//	mux.Handle("/api/peering/inbound/", InboundMiddleware(contacts, innerMux))
package peering

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ─── Context key ─────────────────────────────────────────────────────────────

// contextKey is an unexported type for context keys in this package, preventing
// collisions with keys from other packages.
type contextKey int

// EnvelopeKey is the context key under which the parsed, verified *Envelope is
// stored by InboundMiddleware. Downstream handlers retrieve it with:
//
//	env := r.Context().Value(peering.EnvelopeKey).(*peering.Envelope)
const EnvelopeKey contextKey = iota

// ─── Middleware ───────────────────────────────────────────────────────────────

// inboundContactRequestPath is the route exempt from the allow-list check.
// A peer that is not yet in the contacts store may still deliver a contact
// request so the local user can decide whether to approve them.
const inboundContactRequestPath = "/api/peering/inbound/request"

// maxBodyBytes is the maximum request body size accepted by the middleware.
// Large bodies are rejected to prevent memory exhaustion.
const maxBodyBytes = 1 << 20 // 1 MiB

// InboundMiddleware returns an http.Handler that:
//
//  1. Reads and size-limits the request body.
//  2. Decodes the body as a signed Envelope.
//  3. Verifies the Ed25519 signature (401 on failure).
//  4. For all paths except /api/peering/inbound/request, checks that
//     Envelope.From is approved in the contacts store (403 on failure).
//  5. Stores the verified *Envelope in the request context and calls next.
//
// contacts must be non-nil. next is the downstream mux/handler that handles
// the specific inbound route.
func InboundMiddleware(contacts *ContactStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only POST is expected on inbound routes.
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
				"error": "method not allowed; use POST",
			})
			return
		}

		// Read body with a size cap.
		limited := io.LimitReader(r.Body, maxBodyBytes+1)
		raw, err := io.ReadAll(limited)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "failed to read request body",
			})
			return
		}
		if int64(len(raw)) > maxBodyBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": "request body exceeds 1 MiB limit",
			})
			return
		}

		// Decode envelope.
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "body is not a valid JSON envelope",
			})
			return
		}

		// 1. Signature verification — required for all inbound routes.
		if err := env.Verify(); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":  "invalid or missing signature",
				"detail": err.Error(),
			})
			return
		}

		// 1b. Revocation gate (identity lifecycle). A valid signature from a
		// REVOKED identity must be rejected at admission — a compromised key that
		// has been revoked (self- or recovery-anchor-signed) can still produce
		// structurally valid envelopes. When no lifecycle subsystem is wired the
		// checker is absent and this is a no-op; once wired it fails closed.
		if isVulosIDRevoked(env.From) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "sender identity has been revoked",
				"from":  env.From,
			})
			return
		}

		// 2. Allow-list check — required for all routes except inbound/request.
		path := r.URL.Path
		isContactRequest := strings.TrimRight(path, "/") == strings.TrimRight(inboundContactRequestPath, "/")

		if !isContactRequest {
			approved := contacts.IsApproved(env.From)
			if !approved {
				// Follow a rotation/recovery: if the sender's key resolves (via a
				// VERIFIED, ingested lifecycle chain) to a ROOT that IS an approved
				// contact, admit it on the new key. A forged chain records no mapping,
				// so this cannot be used to smuggle in an unapproved key.
				if root, ok := resolvePresentedRoot(env.From); ok && contacts.IsApproved(root) {
					approved = true
				}
			}
			if !approved {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "sender is not in the approved contacts list",
					"from":  env.From,
				})
				return
			}
		}

		// Store the verified envelope in the context for downstream handlers.
		ctx := context.WithValue(r.Context(), EnvelopeKey, &env)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── Helper ───────────────────────────────────────────────────────────────────

// writeJSON writes a JSON-encoded body with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck — response write errors are not actionable
}
