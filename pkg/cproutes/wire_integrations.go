// wire_integrations.go — INTEGRATIONS_WIRE: third-party OAuth broker store +
// service construction (INTEG-01/02).
package cproutes

import (
	"context"
	"log"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/integrations"
)

// IntegrationsResult bundles the wired integrations components. Exported (with
// exported fields) so a commercial composition root can reach the service,
// device-key registry, health pinger, and closer it wired.
type IntegrationsResult struct {
	Service    *integrations.Service
	DeviceKeys integrations.DeviceKeyStore // INTEG-SEC-01: ULID→device pubkey registry
	Health     StoreHealth
	Closer     func()
}

// WireIntegrations opens the integrations store via the cpdb seam, loads the
// KEK that encrypts refresh tokens at rest (INTEGRATIONS_KEK, AES-256-GCM),
// and builds the broker Service backed by the production Google exchanger. Falls
// back to a MemStore (dev/test) if the DB cannot be opened.
func WireIntegrations(dbDir string) IntegrationsResult {
	var store integrations.Store
	var deviceKeys integrations.DeviceKeyStore
	var pingDB *cpdb.DB
	var closer func()

	// COORDINATOR: needs openCPDB — defined in wire_stores.go (NOT in my subsystem)
	db, dbErr := openCPDB(dbDir, "integrations")
	var sqlSt *integrations.SQLStore
	var openErr error
	if dbErr == nil {
		sqlSt, openErr = integrations.Open(db)
	} else {
		openErr = dbErr
	}

	if openErr != nil {
		// COORDINATOR: needs warnMemStoreFallback — defined in wire_stores.go (NOT in my subsystem)
		warnMemStoreFallback("integrations", openErr)
		if db != nil {
			_ = db.Close()
		}
		store = integrations.NewMemStore()
		deviceKeys = integrations.NewMemDeviceKeyStore()
		closer = func() {}
	} else {
		log.Printf("[integrations] SQL store opened (backend=%s)", db.Backend())
		store = sqlSt
		deviceKeys = sqlSt.DeviceKeys()
		pingDB = db
		closer = func() {
			if cerr := sqlSt.Close(); cerr != nil {
				log.Printf("[integrations] store close: %v", cerr)
			}
		}
	}

	kek, err := integrations.LoadKEK()
	if err != nil {
		// Fail loud but non-fatal: without a KEK the broker refuses to store
		// tokens. Configured() still gates the user routes, and the service is
		// constructed so /status reports correctly; Connect/Mint will error.
		log.Printf("[integrations] WARNING: %v — Google connections disabled until INTEGRATIONS_KEK is set", err)
		kek = nil
	}

	svc := integrations.NewService(store, integrations.GoogleExchanger{}, kek)
	// EXT-PROVIDERS: Dropbox is a second connected-account provider for Vulos
	// Files external stores, brokered the same way (encrypted refresh token at
	// rest; boxes mint short-lived access tokens). GCS reuses the Google grant via
	// a mint-only provider=gcs alias, so it needs no separate exchanger here.
	svc.RegisterExchanger(integrations.ProviderDropbox, integrations.DropboxExchanger{})
	// OAUTH-EVERYWHERE (JOB 1): Microsoft (Entra ID / Graph) is a peer
	// connected-account provider for the importer (OneDrive/Office files, Outlook
	// Contacts + Calendar), brokered identically — encrypted refresh token at rest;
	// boxes mint short-lived Graph access tokens.
	svc.RegisterExchanger(integrations.ProviderMicrosoft, integrations.MicrosoftExchanger{})

	health := StoreHealth{
		Name: "integrations",
		Ping: func(ctx context.Context) error {
			if pingDB == nil {
				return openErr
			}
			return pingDB.PingContext(ctx)
		},
	}

	return IntegrationsResult{
		Service:    svc,
		DeviceKeys: deviceKeys,
		Health:     health,
		Closer:     closer,
	}
}
