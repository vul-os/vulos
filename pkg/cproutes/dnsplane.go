// dnsplane.go — dnsplane HTTP routes for the control plane.
//
// Endpoints registered (per DNSPLANE_API.md):
//
//	GET  /api/dnsplane/zone/{ulid}    — records cp thinks should exist; session+owner
//	POST /api/dnsplane/resync/{ulid}  — force reconcile for a ULID; session+owner
//	POST /api/dnsplane/resync/pool    — force pool reconcile; session+admin
//	GET  /api/dnsplane/status         — provider status + counts; session+admin
//
// Error mapping (per DNSPLANE_API.md):
//
//	ErrInvalidFQDN | ErrInvalidType → 400
//	ErrProviderRefused              → 502
//	ErrUnknownULID                  → 404
//
// Vanity-redirect is NOT implemented here — that is the b-vanity surface.
package cproutes

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/dnsplane"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// dnsplaneResyncTimeout bounds a detached background reconcile spawned by the
// fire-and-forget resync endpoints.
const dnsplaneResyncTimeout = 2 * time.Minute

// RegisterDNSPlane wires the dnsplane endpoints into mux.
// authStore is used for session validation; adminAccountID is the account ID
// that has admin privileges (guards pool/status endpoints).
func RegisterDNSPlane(
	mux *http.ServeMux,
	st dnsplane.Store,
	rec *dnsplane.Reconciler,
	authStore *auth.Store,
	adminAccountID string,
) {
	// ------------------------------------------------------------------ //
	// GET /api/dnsplane/zone/{ulid}                                       //
	// Returns the records the cp thinks should exist for this ULID.       //
	// Auth: session required; caller must own the ULID (or be admin).     //
	// ------------------------------------------------------------------ //
	mux.HandleFunc("GET /api/dnsplane/zone/{ulid}", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		rawULID := r.PathValue("ulid")
		ulid, err := routing.CanonULID(rawULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Owner check: verify session user owns this ULID (or is admin).
		if u.ID != adminAccountID {
			b, bErr := rec.RoutingStore.GetBinding(r.Context(), ulid)
			if bErr != nil {
				httpx.Err(w, dnsplaneErrStatus(bErr), bErr.Error())
				return
			}
			if b.AccountID != u.ID {
				httpx.Err(w, http.StatusForbidden, "not your ULID")
				return
			}
		}

		records, err := st.ByULID(r.Context(), ulid)
		if err != nil {
			httpx.Err(w, dnsplaneErrStatus(err), err.Error())
			return
		}
		if records == nil {
			records = []dnsplane.Record{}
		}
		httpx.JSON(w, records)
	})

	// ------------------------------------------------------------------ //
	// POST /api/dnsplane/resync/{ulid}                                    //
	// Forces a reconcile for a specific ULID. Returns 202 Accepted.      //
	// Auth: session required; caller must own the ULID (or be admin).    //
	// ------------------------------------------------------------------ //
	mux.HandleFunc("POST /api/dnsplane/resync/{ulid}", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		rawULID := r.PathValue("ulid")
		ulid, err := routing.CanonULID(rawULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Owner check.
		if u.ID != adminAccountID {
			b, bErr := rec.RoutingStore.GetBinding(r.Context(), ulid)
			if bErr != nil {
				httpx.Err(w, dnsplaneErrStatus(bErr), bErr.Error())
				return
			}
			if b.AccountID != u.ID {
				httpx.Err(w, http.StatusForbidden, "not your ULID")
				return
			}
		}

		// Run reconcile in the background; return 202 immediately. Detach from the
		// request context (net/http cancels r.Context() the instant this handler
		// returns) so the reconcile does not run against an already-cancelled ctx —
		// which previously turned every async resync into a silent no-op (or a
		// half-applied binding when cancellation landed mid-flight). Preserve request
		// values but bound the detached work with an explicit timeout.
		bgctx := context.WithoutCancel(r.Context())
		go func() {
			ctx, cancel := context.WithTimeout(bgctx, dnsplaneResyncTimeout)
			defer cancel()
			if err := rec.ReconcileULID(ctx, ulid); err != nil {
				log.Printf("[dnsplane] async resync ulid=%s: %v", ulid, err)
			}
		}()

		w.WriteHeader(http.StatusAccepted)
		httpx.JSON(w, map[string]string{"status": "accepted", "ulid": ulid})
	})

	// ------------------------------------------------------------------ //
	// POST /api/dnsplane/resync/pool                                      //
	// Forces a pool reconcile. Admin only.                                //
	// ------------------------------------------------------------------ //
	mux.HandleFunc("POST /api/dnsplane/resync/pool", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		if u.ID != adminAccountID {
			httpx.Err(w, http.StatusForbidden, "admin only")
			return
		}

		// Detach from the request context (cancelled once this handler returns) and
		// bound the detached reconcile with an explicit timeout.
		bgctx := context.WithoutCancel(r.Context())
		go func() {
			ctx, cancel := context.WithTimeout(bgctx, dnsplaneResyncTimeout)
			defer cancel()
			if err := rec.ReconcilePool(ctx); err != nil {
				log.Printf("[dnsplane] async pool resync: %v", err)
			}
		}()

		w.WriteHeader(http.StatusAccepted)
		httpx.JSON(w, map[string]string{"status": "accepted", "pool": "resync triggered"})
	})

	// ------------------------------------------------------------------ //
	// GET /api/dnsplane/status                                            //
	// Returns provider name, last reconcile timestamp, desired/applied/   //
	// failed counts. Admin only.                                          //
	// ------------------------------------------------------------------ //
	mux.HandleFunc("GET /api/dnsplane/status", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		if u.ID != adminAccountID {
			httpx.Err(w, http.StatusForbidden, "admin only")
			return
		}

		snap, err := rec.Status(r.Context())
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.JSON(w, snap)
	})
}

// dnsplaneErrStatus maps dnsplane sentinel errors to HTTP status codes per
// DNSPLANE_API.md.
func dnsplaneErrStatus(err error) int {
	switch {
	case errors.Is(err, dnsplane.ErrInvalidFQDN),
		errors.Is(err, dnsplane.ErrInvalidType):
		return http.StatusBadRequest
	case errors.Is(err, dnsplane.ErrProviderRefused):
		return http.StatusBadGateway
	case errors.Is(err, dnsplane.ErrUnknownULID),
		errors.Is(err, routing.ErrNotEnrolled),
		errors.Is(err, routing.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, routing.ErrNotOwner):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}
