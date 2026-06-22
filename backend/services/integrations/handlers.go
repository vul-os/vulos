package integrations

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// RegisterHandlers wires the box-local integration endpoints onto mux. They are
// session-gated by the OS auth middleware (which injects X-User-ID); apps reach
// the access token through these endpoints (Wave 4 adds gateway injection).
//
//	GET /api/integrations/google/status — { configured, connected }
//	GET /api/integrations/google/token  — { access_token, expires_at, scopes }
func RegisterHandlers(mux *http.ServeMux, c *Client) {
	mux.HandleFunc("GET /api/integrations/google/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		configured, connected, err := c.Status(r.Context(), ProviderGoogle)
		resp := map[string]any{
			"provider":   ProviderGoogle,
			"configured": configured,
			"connected":  connected,
		}
		if err != nil {
			// Cloud unreachable / transient — report degraded, not fatal.
			resp["error"] = "cloud broker unavailable"
		}
		writeJSON(w, resp)
	})

	mux.HandleFunc("GET /api/integrations/google/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		tok, err := c.MintToken(r.Context(), ProviderGoogle)
		if err != nil {
			switch {
			case errors.Is(err, ErrNotConfigured):
				writeErr(w, http.StatusServiceUnavailable, "cloud integrations not configured on this box")
			case errors.Is(err, ErrNotConnected):
				writeErr(w, http.StatusNotFound, "Google is not connected for this account")
			default:
				writeErr(w, http.StatusBadGateway, "could not obtain access token")
			}
			return
		}
		expiresIn := int(time.Until(tok.Expiry).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		writeJSON(w, map[string]any{
			"access_token": tok.AccessToken,
			"expires_at":   tok.Expiry.UTC().Format(time.RFC3339),
			"expires_in":   expiresIn,
			"scopes":       tok.Scopes,
			"provider":     ProviderGoogle,
		})
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
