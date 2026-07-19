// Package appsgate implements the VULOS_APPS kill switch.
//
// docs/APPS.md and docs/ASSISTANT.md document VULOS_APPS=off as the way to
// disable the apps surface wholesale. Before this package the variable had no
// reader anywhere in the backend: an operator who set it believed they had shut
// the surface off while every route stayed live. A security control that is
// documented but not implemented is worse than one that does not exist, because
// it is relied upon.
//
// Recognised values (case-insensitive, surrounding space ignored):
//
//	unset, "", "on", "1", "true", "yes"   → surface ENABLED (default)
//	"off", "0", "false", "no"             → surface DISABLED
//	anything else                         → surface DISABLED (fail closed)
//
// An unrecognised value fails closed deliberately: a typo such as
// VULOS_APPS=Off-please must not silently leave the surface exposed.
package appsgate

import (
	"log"
	"net/http"
	"os"
	"strings"
)

// gatedPrefixes are the request paths the switch controls: the apps platform
// mounted at /api/apps and the MCP endpoint at /mcp.
var gatedPrefixes = []string{"/api/apps", "/mcp"}

// Enabled reports whether the apps surface is enabled, per VULOS_APPS.
func Enabled() bool {
	return parse(os.Getenv("VULOS_APPS"))
}

func parse(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "on", "1", "true", "yes":
		return true
	case "off", "0", "false", "no":
		return false
	default:
		// Unrecognised → fail closed.
		log.Printf("[apps] VULOS_APPS=%q is not a recognised value; failing closed and DISABLING the apps surface (use on/off)", raw)
		return false
	}
}

// isGated reports whether path belongs to the apps surface. It matches the
// prefix exactly or as a path segment, so "/api/appsomething" is not gated.
func isGated(path string) bool {
	for _, p := range gatedPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// Middleware blocks the apps surface when VULOS_APPS disables it, and is a
// passthrough otherwise. The env var is read once at construction so a running
// box has one consistent answer.
//
// Blocked requests get 404 rather than 503: a disabled surface should not
// advertise that it exists.
func Middleware(next http.Handler) http.Handler {
	if Enabled() {
		return next
	}
	log.Printf("[apps] VULOS_APPS=off — the apps platform (/api/apps) and MCP endpoint (/mcp) are DISABLED")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGated(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
