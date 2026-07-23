package main

import (
	"errors"
	"net/http"
	"strings"

	"vulos/backend/services/webhost"
)

// routes_webhost.go wires the box-side static web-hosting surface (PUBWEB "host
// a built site from your own box") onto the authenticated mux.
//
// TWO surfaces, deliberately kept apart:
//
//  1. The MANAGEMENT API (/api/web/*): create / list / delete sites and attach
//     custom domains. Every route is scoped to the auth-middleware-stamped
//     X-User-ID and FAILS CLOSED with 401 on an empty header — a request the
//     middleware did not authenticate can never touch another owner's sites.
//     Identity is taken from the header only, never from a path/body field, so
//     the site tree is structurally per-user.
//
//  2. The public SERVING handler (webhostServePrefix): serves the extracted
//     static tree anonymously with the package's SPA-fallback + traversal
//     guard. This is intentionally NOT behind X-User-ID — a published site is
//     public — so the owner id is carried in the request path, not a session.
//     See newWebhostHostServer for the host-based (`<site>.<user>.os.vulos.org`)
//     entrypoint the edge/pre-auth wrapper forwards to.
//
// The Deploy body is the raw tar.gz bundle with the site name in the `?site=`
// query parameter (the documented single option — no multipart). The service
// treats that bundle as hostile: see services/webhost for the extraction guards.

// webhostServePrefix is the internal rewrite target for public site serving.
// The public edge (or the pre-auth host wrapper in main.go) rewrites
// `<site>.<user>.os.vulos.org/<path>` to `<box>/__webhost__/<user>/<site>/<path>`,
// mirroring how appnet's PubwebPathPrefix works for proxied apps. It is mounted
// on the mux by registerWebhostRoutes so a single box handler backs both the
// host-based and rewrite-based paths.
const webhostServePrefix = "/__webhost__/"

// maxWebhostUpload caps the COMPRESSED deploy body. The service already bounds
// the DECOMPRESSED size and file count (decompression-bomb guard), but this
// keeps a single upload from streaming unbounded bytes before gzip even starts.
const maxWebhostUpload = 256 << 20 // 256 MiB compressed

// registerWebhostRoutes constructs a webhost.Service and wires the /api/web/*
// management endpoints (X-User-ID scoped, 401 fail-closed) plus the public
// per-site serving handler onto mux. opts are forwarded to webhost.New so the
// caller can set the site root, instance A-records (WithInstanceIPs), or a real
// CertProvider (WithCertProvider) without this file depending on any main.go
// global. Returns the Service so the caller can also wire a host-based public
// entrypoint via newWebhostHostServer.
func registerWebhostRoutes(mux *http.ServeMux, opts ...webhost.Option) *webhost.Service {
	svc := webhost.New("", opts...)

	// guard resolves the authenticated owner from X-User-ID and fails closed on
	// an empty header. Identity is NEVER read from the path or body.
	guard := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeErr(w, 401, "unauthorized")
				return
			}
			h(w, r, userID)
		}
	}

	// POST /api/web/sites?site=<slug> — deploy a built static bundle. The raw
	// request body IS the tar.gz.
	mux.HandleFunc("POST /api/web/sites", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		site := strings.TrimSpace(r.URL.Query().Get("site"))
		if !webhost.ValidSiteName(site) {
			writeErr(w, 400, "invalid or missing ?site= name")
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxWebhostUpload)
		if err := svc.Deploy(uid, site, body); err != nil {
			writeWebhostErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "url": svc.SiteURL(uid, site)})
	}))

	// GET /api/web/sites — list the caller's sites.
	mux.HandleFunc("GET /api/web/sites", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		sites, err := svc.ListSites(uid)
		if err != nil {
			writeWebhostErr(w, err)
			return
		}
		out := make([]map[string]any, 0, len(sites))
		for _, s := range sites {
			out = append(out, map[string]any{
				"name":       s.Name,
				"url":        svc.SiteURL(uid, s.Name),
				"domains":    webhostDomainNames(s.Domains),
				"updated_ts": s.UpdatedTS,
				"bytes":      s.Bytes,
			})
		}
		writeJSON(w, map[string]any{"sites": out})
	}))

	// DELETE /api/web/sites/{site} — remove a site and all its content.
	mux.HandleFunc("DELETE /api/web/sites/{site}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		if err := svc.DeleteSite(uid, r.PathValue("site")); err != nil {
			writeWebhostErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}))

	// POST /api/web/sites/{site}/domains — attach a custom domain; returns the
	// DNS the owner must publish. The domain stays "pending" until a real
	// CertProvider confirms a certificate (honest default: never).
	mux.HandleFunc("POST /api/web/sites/{site}/domains", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Domain string `json:"domain"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		res, err := svc.AttachDomain(r.Context(), uid, r.PathValue("site"), req.Domain)
		if err != nil {
			writeWebhostDomainErr(w, err)
			return
		}
		writeJSON(w, res)
	}))

	// GET /api/web/sites/{site}/domains/{domain} — poll attachment status.
	mux.HandleFunc("GET /api/web/sites/{site}/domains/{domain}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		status, err := svc.DomainStatus(r.Context(), uid, r.PathValue("site"), r.PathValue("domain"))
		if err != nil {
			writeWebhostDomainErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"status": status})
	}))

	// DELETE /api/web/sites/{site}/domains/{domain} — detach a custom domain.
	mux.HandleFunc("DELETE /api/web/sites/{site}/domains/{domain}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		if err := svc.DetachDomain(r.Context(), uid, r.PathValue("site"), r.PathValue("domain")); err != nil {
			writeWebhostDomainErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	}))

	// Public per-site serving. Mounted at the rewrite prefix so the edge (or the
	// pre-auth host wrapper) can forward `<site>.<user>.os.vulos.org/<path>` here.
	// This route is deliberately anonymous — a published site is public — so it
	// carries owner+site in the path and does NOT read X-User-ID.
	mux.HandleFunc(webhostServePrefix+"{user}/{site}/", func(w http.ResponseWriter, r *http.Request) {
		serveWebhostSite(svc, r.PathValue("user"), r.PathValue("site"), w, r)
	})

	return svc
}

// webhostDomainNames flattens the domain records to the name list the list API
// reports (`"domains":[...]`).
func webhostDomainNames(domains []webhost.Domain) []string {
	names := make([]string, 0, len(domains))
	for _, d := range domains {
		names = append(names, d.Name)
	}
	return names
}

// serveWebhostSite strips the `/__webhost__/<user>/<site>` prefix from the
// request path and delegates to the site's SPA-fallback handler. The handler
// (services/webhost.ServeSite) 404s for an unknown site and applies its own
// traversal / dotfile guards, so an attacker-controlled remainder is safe.
func serveWebhostSite(svc *webhost.Service, user, site string, w http.ResponseWriter, r *http.Request) {
	prefix := webhostServePrefix + user + "/" + site
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" || rest[0] != '/' {
		rest = "/" + rest
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = rest
	r2.URL.RawPath = "" // force re-derivation from the rewritten Path
	svc.ServeSite(user, site).ServeHTTP(w, r2)
}

// newWebhostHostServer returns the anonymous host-based public entrypoint for
// published sites. It parses `<site>.<user>.<baseDomain>` (e.g.
// "blog.<user>.os.vulos.org") off the request Host and serves that site. The
// orchestrator wires this into the pre-auth request path in main.go — the same
// place appnet's public app subdomains are handled — so published sites stay
// reachable without a session. A host that does not match the expected
// `<site>.<user>.<baseDomain>` shape 404s.
//
// baseDomain is the two-label-and-up suffix the box serves sites under (for
// production: "os.vulos.org"). It is passed in rather than hard-coded so a
// self-host box on a different domain still works. If baseDomain is empty the
// handler serves nothing (404) — it does not guess.
func newWebhostHostServer(svc *webhost.Service, baseDomain string) http.Handler {
	suffix := "." + strings.Trim(baseDomain, ".")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if baseDomain == "" {
			http.NotFound(w, r)
			return
		}
		host := r.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		host = strings.ToLower(host)
		if !strings.HasSuffix(host, suffix) {
			http.NotFound(w, r)
			return
		}
		sub := strings.TrimSuffix(host, suffix)
		// Expect exactly two labels: <site>.<user>. Anything else (bare apex,
		// deeper nesting) is not a site URL we own.
		parts := strings.Split(sub, ".")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		site, user := parts[0], parts[1]
		svc.ServeSite(user, site).ServeHTTP(w, r)
	})
}

// writeWebhostErr maps the service's sentinel errors to HTTP status codes.
// Validation failures are 4xx; an over-cap bundle is 413; everything else is a
// 500. ErrInvalidUser surfaces as 401 (the header the middleware stamped is not
// a well-formed id) rather than leaking it as a 400 on user input.
func writeWebhostErr(w http.ResponseWriter, err error) {
	// A compressed upload that blows the MaxBytesReader cap surfaces (wrapped)
	// from the gzip/tar read; map it to 413, not the generic 500.
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		writeErr(w, 413, "upload exceeds the maximum bundle size")
	case errors.Is(err, webhost.ErrNotFound):
		writeErr(w, 404, "site not found")
	case errors.Is(err, webhost.ErrInvalidUser):
		writeErr(w, 401, "unauthorized")
	case errors.Is(err, webhost.ErrInvalidSite):
		writeErr(w, 400, "invalid site name")
	case errors.Is(err, webhost.ErrBundleTooLarge):
		writeErr(w, 413, "bundle exceeds size or file-count limit")
	case errors.Is(err, webhost.ErrQuotaExceeded):
		writeErr(w, 413, "storage or site-count quota exceeded")
	case errors.Is(err, webhost.ErrUnsafeEntry):
		writeErr(w, 400, "unsafe archive entry")
	default:
		writeErr(w, 500, "webhost error")
	}
}

// writeWebhostDomainErr maps errors from the custom-domain operations. These
// methods' dominant failure is a malformed domain (normalizeDomain returns a
// plain, non-sentinel error), so anything that is not a recognised sentinel is
// treated as bad input (400 "invalid domain") rather than a server fault.
func writeWebhostDomainErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, webhost.ErrNotFound):
		writeErr(w, 404, "site not found")
	case errors.Is(err, webhost.ErrInvalidUser):
		writeErr(w, 401, "unauthorized")
	case errors.Is(err, webhost.ErrInvalidSite):
		writeErr(w, 400, "invalid site name")
	default:
		writeErr(w, 400, "invalid domain")
	}
}
