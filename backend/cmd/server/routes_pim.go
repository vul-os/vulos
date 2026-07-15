package main

// routes_pim.go — the OS→lilmail PIM proxy behind the box's standalone
// Calendar and Contacts widgets.
//
// The GNOME model: lilmail is the "Evolution-Data-Server" — it connects the
// user's IMAP/CalDAV/CardDAV (directly, or via an OAuth-connected Google/
// Microsoft account) and exposes a stable /v1 contract. The OS ships thin
// standalone Calendar + Contacts surfaces (the "GNOME Calendar/Contacts") that
// read/write that data. Those surfaces run in the browser, so they must NEVER
// see the mail credentials.
//
// This proxy is the credential-brokering seam. The browser calls, session-authed:
//
//	GET/POST/PUT/PATCH/DELETE /api/pim/calendar/*   → lilmail <base>/v1/calendar/*
//	GET/POST/PUT/PATCH/DELETE /api/pim/contacts/*    → lilmail <base>/v1/contacts/*
//
// The box forwards the caller's session cookie and injects the CP-brokered
// X-Vulos-Mail-* credential headers (from the environment) — exactly the seam
// services/assistant already uses to read /v1. The browser supplies neither the
// broker secret nor the /v1 origin, so credentials stay on the box.
//
// SECURITY:
//   - /api/pim/* is NOT a public path, so the auth middleware 401s any
//     unauthenticated request and stamps a trustworthy X-User-ID before we run.
//   - Only the calendar/ and contacts/ subtrees are reachable; any other /v1
//     path (mail bodies, drafts, send) is 404'd here — this proxy is PIM-only.
//   - Any client-supplied X-Vulos-Mail-* header is stripped before the broker
//     headers are injected, so a browser cannot forge the broker credential.

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// registerPIMRoutes wires the PIM proxy into mux. mailBaseURL is the lilmail
// origin (VULOS_MAIL_URL, default http://localhost:3000); brokerHeaders are the
// CP-brokered X-Vulos-Mail-* credential headers (nil ⇒ session-cookie mode).
func registerPIMRoutes(mux *http.ServeMux, mailBaseURL string, brokerHeaders map[string]string) {
	target, err := url.Parse(strings.TrimRight(mailBaseURL, "/"))
	if err != nil || target.Host == "" {
		// Misconfigured mail base: serve an honest 503 so the widgets show their
		// "Calendar/Contacts unavailable" state rather than hanging.
		mux.HandleFunc("/api/pim/", func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusServiceUnavailable, "mail service not configured")
		})
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// req.URL.Path was rewritten to the /v1/... form by the handler below.

			// Strip any client-supplied broker headers BEFORE injecting the real
			// ones — a browser must not be able to forge the broker credential.
			for k := range req.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Vulos-Mail") {
					req.Header.Del(k)
				}
			}
			for k, v := range brokerHeaders {
				if v != "" {
					req.Header.Set(k, v)
				}
			}
			req.Header.Set("Accept", "application/json")
			// The X-User-ID the auth middleware stamped is for the OS's own routes;
			// lilmail authenticates via the forwarded cookie + broker headers, so
			// don't leak our internal identity header downstream.
			req.Header.Del("X-User-ID")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// lilmail unreachable → 502 so the widget degrades honestly.
			writeErr(w, http.StatusBadGateway, "mail service unreachable")
		},
	}

	mux.HandleFunc("/api/pim/", func(w http.ResponseWriter, r *http.Request) {
		// Defensive: the auth middleware already 401s non-public paths, but never
		// proxy an unauthenticated request to the mailbox.
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		sub := strings.TrimPrefix(r.URL.Path, "/api/pim/")
		// PIM-only allowlist: calendar + contacts subtrees, nothing else on /v1.
		if !(sub == "calendar" || strings.HasPrefix(sub, "calendar/") ||
			sub == "contacts" || strings.HasPrefix(sub, "contacts/")) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		r.URL.Path = "/v1/" + sub
		proxy.ServeHTTP(w, r)
	})
}
