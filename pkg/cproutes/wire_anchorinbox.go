// wire_anchorinbox.go — opens the anchor-inbox SQLite store and exposes it
// package-wide so the signup handler can provision an inbox at account creation.
//
// ANCHOR_WIRE anchor block: parallel agents must NOT edit this file.
package cproutes

import (
	"context"
	"errors"
	"log"

	"github.com/vul-os/vulos-management/pkg/anchorinbox"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// anchorInboxStore is the process-wide anchor-inbox registry.
// Set by wireAnchorInbox during server startup.  nil until then.
var anchorInboxStore *anchorinbox.Store

type anchorInboxResult struct {
	store  *anchorinbox.Store
	health StoreHealth
	closer func()
}

// wireAnchorInbox opens (or creates) the anchor-inbox SQLite store at
// <dbDir>/anchorinbox.db and returns a health-check pinger + closer.
func wireAnchorInbox(dbDir string) anchorInboxResult {
	// cpdb.Open selects Postgres when DATABASE_URL / VULOS_DATABASE_URL is set;
	// otherwise SQLite at <VULOS_DB_DIR>/anchorinbox.db (self-host default).
	var st *anchorinbox.Store
	var err error
	if e := setDBDirIfUnset(dbDir); e != nil {
		err = e
	} else if db, e := cpdb.Open("anchorinbox"); e != nil {
		err = e
	} else if s, e := anchorinbox.Open(db); e != nil {
		_ = db.Close()
		err = e
	} else {
		st = s
	}
	health := StoreHealth{
		Name: "anchorinbox",
		Ping: func(ctx context.Context) error {
			if err != nil {
				return err
			}
			return st.Ping(ctx)
		},
	}
	if err != nil {
		log.Printf("[anchorinbox] WARNING: could not open store (%v) — anchor inbox provisioning disabled", err)
		return anchorInboxResult{store: nil, health: health, closer: func() {}}
	}
	anchorInboxStore = st
	log.Printf("[anchorinbox] store opened (cpdb)")
	return anchorInboxResult{
		store:  st,
		health: health,
		closer: func() {
			if err := st.Close(); err != nil {
				log.Printf("[anchorinbox] close: %v", err)
			}
		},
	}
}

// provisionAnchorInbox provisions the anchor inbox for a newly created account.
// It is called from the signup handler immediately after the auth row is committed.
// Errors are logged but do not fail the signup — the inbox can be back-filled.
func provisionAnchorInbox(ctx context.Context, accountID, handle string) {
	if anchorInboxStore == nil {
		log.Printf("[anchorinbox] store not initialised — skipping anchor provision for %s", accountID)
		return
	}
	inbox, err := anchorInboxStore.Provision(ctx, accountID, handle)
	if err != nil {
		if errors.Is(err, anchorinbox.ErrAlreadyProvisioned) {
			// Idempotent: already done (e.g. replay or duplicate call).
			return
		}
		log.Printf("[anchorinbox] provision failed for account=%s handle=%s: %v", accountID, handle, err)
		return
	}
	log.Printf("[anchorinbox] provisioned anchor inbox %s for account=%s (quota=%dMB)",
		inbox.Address, accountID, inbox.QuotaMB)
}
