package main

// routes_apifallback.go — terminal fallback for the /api/ namespace.
//
// Go's ServeMux routes to the MOST SPECIFIC matching pattern, and the SPA
// catch-all ("/") matches every path and every method. Without a pattern below
// it, an unmatched — or method-mismatched — /api/* request fell through to the
// SPA handler and was answered 200 text/html. Every client that only checks
// res.ok therefore read a typo'd or unregistered API route as a SILENT SUCCESS,
// and the mux's own 405 logic never fired (the catch-all always matched, so the
// method-mismatch branch was unreachable).
//
// "/api/" gives the API namespace its own terminal handler:
//
//	404 {"error":"not found"}           — no route claims this path
//	405 {"error":"method not allowed"}  — the path IS routed under other
//	                                      methods (Allow header lists them)
//
// It is shadowed by every concrete /api/... pattern, so it only ever sees
// requests no real route claimed.

import (
	"net/http"
	"strings"
)

// apiProbeMethods are the methods probed to tell a method mismatch (405) apart
// from an unknown path (404).
var apiProbeMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// registerAPIFallbackRoutes wires the terminal /api/ handler onto mux. It must
// be registered on the SAME mux that serves the API routes, so the method probe
// observes every registered pattern.
func registerAPIFallbackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if allowed := apiAllowedMethods(mux, r); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
	})
}

// apiAllowedMethods returns the methods under which r's path IS served by a real
// route. It re-asks the mux with each candidate method and keeps those that land
// on a pattern other than this fallback or the SPA catch-all.
func apiAllowedMethods(mux *http.ServeMux, r *http.Request) []string {
	var allowed []string
	for _, m := range apiProbeMethods {
		if m == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = m
		_, pattern := mux.Handler(probe)
		switch pattern {
		case "", "/", "/api/":
			continue
		}
		allowed = append(allowed, m)
	}
	return allowed
}
