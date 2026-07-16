// wire_oauthprovider.go — OAUTHP-01: Vulos Cloud as an OAuth2 / OIDC provider.
// Opens the provider SQLite store, loads/generates the RS256 signing key, and
// builds the authorization-server Service. Falls back to an in-memory SQLite DB
// (dev/test) when the on-disk DB cannot be opened, matching the other wireXxx
// helpers' data-loss-on-restart warning.
package cproutes

import (
	"context"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

// disabledHealth is a non-nil pinger used when the provider could not be wired,
// so /readyz reports the subsystem as down rather than panicking on a nil ping.
// COORDINATOR: needs StoreHealth type — defined in wire_stores.go (NOT in my subsystem)
func disabledHealth(reason string) StoreHealth {
	err := errors.New("oauthprovider disabled: " + reason)
	return StoreHealth{Name: "oauthprovider", Ping: func(context.Context) error { return err }}
}

// OAuthProviderResult bundles the wired OIDC provider components. Exported (with
// exported fields) for a commercial composition root.
type OAuthProviderResult struct {
	Service *oauthprovider.Service
	Health  StoreHealth
	Closer  func()
}

// oauthIssuer resolves the public base URL used as the OIDC issuer and in
// id_token `iss`. Preference order: OAUTH_ISSUER, then derived from
// VULOS_COOKIE_DOMAIN (https://<domain>), then a localhost dev default.
func oauthIssuer() string {
	if iss := strings.TrimSpace(os.Getenv("OAUTH_ISSUER")); iss != "" {
		return strings.TrimRight(iss, "/")
	}
	if d := strings.TrimSpace(os.Getenv("VULOS_COOKIE_DOMAIN")); d != "" {
		d = strings.TrimPrefix(d, ".")
		return "https://" + d
	}
	return "http://localhost:8080"
}

// WireOAuthProvider opens the store and constructs the provider service.
// authStore is the identity source of truth (UserResolver via ProfileByID).
func WireOAuthProvider(dbDir string, authStore *auth.Store) OAuthProviderResult {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[oauthprovider] WARNING: could not set DB dir: %v", err)
	}

	// openStore opens a cpdb-backed oauthprovider store. cpdb.Open selects
	// Postgres (DATABASE_URL / VULOS_DATABASE_URL) or SQLite at
	// <VULOS_DB_DIR>/oauthprovider.db.
	openStore := func() (*cpdb.DB, *oauthprovider.Store, error) {
		db, err := cpdb.Open("oauthprovider")
		if err != nil {
			return nil, nil, err
		}
		store, err := oauthprovider.Open(db)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		return db, store, nil
	}

	db, store, err := openStore()
	if err != nil {
		// COORDINATOR: needs warnMemStoreFallback — defined in wire_stores.go (NOT in my subsystem)
		warnMemStoreFallback("oauthprovider", err)
		// In-memory SQLite keeps the provider functional in dev/test; tokens and
		// clients are lost on restart (acceptable for the fallback path).
		var merr error
		db, merr = cpdb.OpenSQLiteDSN("file:oauthprovider_mem?mode=memory&cache=shared")
		if merr != nil {
			log.Printf("[oauthprovider] FATAL: could not open in-memory fallback store: %v — provider disabled", merr)
			return OAuthProviderResult{Health: disabledHealth("store open failed"), Closer: func() {}}
		}
		store, merr = oauthprovider.Open(db)
		if merr != nil {
			_ = db.Close()
			log.Printf("[oauthprovider] FATAL: could not open in-memory fallback store: %v — provider disabled", merr)
			return OAuthProviderResult{Health: disabledHealth("store open failed"), Closer: func() {}}
		}
	} else {
		log.Printf("[oauthprovider] store opened (backend=%s)", db.Backend())
	}

	issuer := oauthIssuer()
	if _, perr := url.Parse(issuer); perr != nil {
		log.Printf("[oauthprovider] WARNING: invalid issuer %q: %v", issuer, perr)
	}

	svc, err := oauthprovider.NewService(context.Background(), store, authStore, issuer)
	if err != nil {
		log.Printf("[oauthprovider] WARNING: could not build service: %v — provider disabled", err)
		_ = store.Close()
		return OAuthProviderResult{Health: disabledHealth("service build failed"), Closer: func() {}}
	}
	log.Printf("[oauthprovider] issuer=%s signing-kid=%s", issuer, svc.SigningKey().KID)

	health := StoreHealth{
		Name: "oauthprovider",
		Ping: func(ctx context.Context) error {
			return db.PingContext(ctx)
		},
	}

	return OAuthProviderResult{
		Service: svc,
		Health:  health,
		Closer: func() {
			_ = store.Close()
		},
	}
}
