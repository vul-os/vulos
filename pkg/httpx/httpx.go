// Package httpx provides shared HTTP helpers for cp handlers.
// New routes_<area>.go files in cmd/server import this for consistent
// JSON encoding, error responses, and request decoding.
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as application/json with status 200.
func JSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// JSONStatus writes v as application/json with an explicit status code. Use this
// (not WriteHeader followed by JSON) when the response is non-200: calling
// WriteHeader before JSON makes JSON's Content-Type header a no-op and logs a
// spurious "superfluous WriteHeader" — JSONStatus sets the header first, then the
// status, then the body, so the Content-Type is honoured and there is no warning.
func JSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Err writes a JSON error with the given status code.
func Err(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// DecodeJSON decodes the request body into v. Returns false + writes 400 on failure.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		Err(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
