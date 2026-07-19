// wire_abuse.go — wires the abuse / Trust & Safety subsystem (RISK-ABUSE-01).
//
// The abuse package itself is fully self-contained; the wiring here only:
//
//  1. opens the SQLite store,
//  2. constructs the Detector with the existing auditlog as the hash-chain sink,
//  3. provides a SessionAuth adapter so the routes can gate by auth.Store sessions
//     + the admin-account marker,
//  4. registers the /api/abuse/* routes on the mux.
//
// The CSAM hash-matcher is left as the no-op default (NoopCSAMMatcher). The
// real PhotoDNA-style integration is the human team's responsibility per
// /docs/RISKS-HUMAN-TEAM.md §2 — when it lands, plug it in here by replacing
// the nil arg to abuse.NewDetector with the production implementation.
package cproutes

import (
	"context"
	"log"
	"net/http"
	"sync"

	"github.com/vul-os/vulos-management/pkg/abuse"
	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// abuseAuthAdapter bridges auth.Store + the abuse.SessionAuth interface.
// Admin = the configured ADMIN_ACCOUNT_ID account, mirroring the gating used
// by other admin-only endpoints (OTA, DNSPlane, PoP admin).
//
// Because abuse.SessionAuth splits Authenticate + IsAdmin across two calls
// (rather than threading the full user object), we cache "actor → user-id"
// per-request via a tiny in-memory map keyed by the actor string. The map is
// process-global but bounded — we only add on Authenticate success and never
// delete; for a long-lived process this is a tiny number of unique accounts.
type abuseAuthAdapter struct {
	store          *auth.Store
	adminAccountID string

	mu        sync.RWMutex
	actorToID map[string]string
}

func newAbuseAuthAdapter(store *auth.Store, adminAccountID string) *abuseAuthAdapter {
	return &abuseAuthAdapter{
		store:          store,
		adminAccountID: adminAccountID,
		actorToID:      map[string]string{},
	}
}

func (a *abuseAuthAdapter) Authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	u := a.store.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", false
	}
	actor := u.Email
	if actor == "" {
		actor = u.ID
	}
	a.mu.Lock()
	a.actorToID[actor] = u.ID
	a.mu.Unlock()
	return actor, true
}

func (a *abuseAuthAdapter) IsAdmin(_ context.Context, actor string) bool {
	if a.adminAccountID == "" {
		return false
	}
	if actor == a.adminAccountID {
		return true
	}
	a.mu.RLock()
	id, ok := a.actorToID[actor]
	a.mu.RUnlock()
	return ok && id == a.adminAccountID
}

// auditlogAuditAdapter adapts *auditlog.Logger to abuse.AuditRecorder. The
// nil-safe wrapper falls back to a no-op if the audit log isn't wired (dev).
type auditlogAuditAdapter struct{ l *auditlog.Logger }

func (a *auditlogAuditAdapter) Record(ctx context.Context, actor, action, target string, meta map[string]string) error {
	if a == nil || a.l == nil {
		return nil
	}
	return a.l.Record(ctx, actor, action, target, meta)
}

// WireAbuse opens the abuse store via cpdb, builds the detector, and registers routes.
// Returns a closer that must be called on shutdown.
func WireAbuse(mux *http.ServeMux, dbDir string, authStore *auth.Store, adminAccountID string, audit *auditlog.Logger, sharedSecret string) (*abuse.Detector, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[abuse] WARNING: could not set DB dir (%v) — abuse subsystem disabled", err)
		return nil, func() {}
	}
	db, err := cpdb.Open("abuse")
	if err != nil {
		log.Printf("[abuse] WARNING: open cpdb failed (%v) — abuse subsystem disabled", err)
		return nil, func() {}
	}
	st, err := abuse.OpenStore(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[abuse] WARNING: open store failed (%v) — abuse subsystem disabled", err)
		return nil, func() {}
	}
	log.Printf("[abuse] store opened (backend=%s)", db.Backend())

	auditAdapter := &auditlogAuditAdapter{l: audit}
	det := abuse.NewDetector(abuse.Config{}, st, nil /* default ContentClassifier */, nil /* default CSAMHashMatcher */, auditAdapter, nil)
	authAdapter := newAbuseAuthAdapter(authStore, adminAccountID)

	// SECRET-ROTATE-01: accept the current OR previous CP_SHARED_SECRET on
	// X-CP-Auth so an internal caller can roll its key without downtime.
	h := abuse.NewHandler(det, st, authAdapter, sharedSecret).
		WithPreviousSecret(secretOrEnv(context.Background(), "CP_SHARED_SECRET_PREVIOUS"))
	h.Register(mux)
	log.Printf("[abuse] routes registered (admin=%s, shared_secret=%v)", maskID(adminAccountID), sharedSecret != "")

	return det, func() {
		if err := st.Close(); err != nil {
			log.Printf("[abuse] store close: %v", err)
		}
	}
}

// maskID returns a short, log-safe rendering of an account ID.
func maskID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:4] + "…" + id[len(id)-4:]
}
