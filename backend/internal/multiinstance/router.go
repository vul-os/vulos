// MINST-03: per-instance app routing — {app}--{profile}.{ulid}.vulos.org
//
// AppRouter builds a cross-instance routing table from the local instance
// Registry and a list of published apps. It exposes:
//
//	GET /api/routing/apps — returns the full routing table as JSON
//
// Each row in the table describes one (app, profile, instance) tuple with
// the canonical FQDN derived from the subdomain scheme:
//
//	{app}--{profile}.{ulid}.vulos.org
//
// The cloud DNS plane uses these FQDNs to route requests to the correct
// instance's WireGuard / relay endpoint.  Locally the table lets the OS
// shell and dashboard show all reachable published apps across all account
// instances without hitting the network.
//
// Wiring: call RegisterHandlers(mux, router) from the server bootstrap.
// Do NOT import this from main.go — wire it from a routes_*.go file that
// already imports the multiinstance package.
package multiinstance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// BaseDomain is the top-level domain used for per-instance subdomains.
// Override with the VULOS_BASE_DOMAIN environment variable (or supply a
// custom base when constructing the AppRouter).
const BaseDomain = "vulos.org"

// AppEntry describes one published app reachable across all account instances.
type AppEntry struct {
	// App is the app slug (e.g. "browser", "notes").
	App string `json:"app"`
	// Profile is the user-profile slug (e.g. "work", "personal", "default").
	Profile string `json:"profile"`
	// InstanceULID is the ULID of the instance that hosts the app.
	InstanceULID string `json:"instance_ulid"`
	// InstanceDisplayName is the human-readable name of the instance.
	InstanceDisplayName string `json:"instance_display_name"`
	// InstanceEndpoint is the WireGuard / relay endpoint URL stored in the registry.
	InstanceEndpoint string `json:"instance_endpoint"`
	// FQDN is the canonical subdomain: {app}--{profile}.{ulid}.{baseDomain}
	FQDN string `json:"fqdn"`
}

// PublishedApp is the minimal description of a published app that the AppRouter
// needs. Callers construct this from their own app-manifest store and pass the
// slice to AppRouter.BuildTable or supply a provider function via WithAppProvider.
type PublishedApp struct {
	// App is the app slug.
	App string
	// Profile is the profile slug. Use "default" when profiles are not supported.
	Profile string
	// InstanceULID is the ULID of the hosting instance.  When empty, the router
	// iterates over all instances in the registry for this app.
	InstanceULID string
}

// AppRouter holds the Registry and the base domain needed to derive FQDNs.
type AppRouter struct {
	reg        *Registry
	baseDomain string
	// appProvider is an optional function returning the current set of published
	// apps.  When nil, BuildTable must be called with an explicit list.
	appProvider func() []PublishedApp
}

// NewAppRouter creates an AppRouter backed by reg, using baseDomain as the
// TLD (e.g. "vulos.org").  Pass an empty string to fall back to BaseDomain.
func NewAppRouter(reg *Registry, baseDomain string) *AppRouter {
	if baseDomain == "" {
		baseDomain = BaseDomain
	}
	return &AppRouter{reg: reg, baseDomain: baseDomain}
}

// WithAppProvider sets a function that the HTTP handler calls on every request
// to retrieve the live list of published apps (instead of requiring a static
// slice passed to BuildTable).
func (ar *AppRouter) WithAppProvider(fn func() []PublishedApp) *AppRouter {
	ar.appProvider = fn
	return ar
}

// FQDN derives the canonical subdomain for one (app, profile, ulid) tuple.
//
//	{app}--{profile}.{ulid}.{baseDomain}
//
// The app and profile slugs are lower-cased; any "--" within them is
// sanitised to "-" to prevent ambiguity with the scheme separator.
func FQDN(app, profile, ulid, baseDomain string) string {
	if baseDomain == "" {
		baseDomain = BaseDomain
	}
	app = sanitiseSlug(app)
	profile = sanitiseSlug(profile)
	return fmt.Sprintf("%s--%s.%s.%s", app, profile, ulid, baseDomain)
}

// sanitiseSlug lower-cases s and replaces embedded "--" with "-" so the
// result is safe to use as a subdomain label component.
func sanitiseSlug(s string) string {
	s = strings.ToLower(s)
	// Replace consecutive hyphens (reserved separator) with single hyphen.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// BuildTable constructs the routing table for the given published apps against
// all instances currently held in the Registry.
//
// If a PublishedApp specifies a non-empty InstanceULID, only that instance is
// looked up.  If InstanceULID is empty, the app is expanded across every
// instance in the registry (useful for apps that are published on all
// instances).
//
// Instances that are not found in the registry are silently skipped.
func (ar *AppRouter) BuildTable(apps []PublishedApp) ([]AppEntry, error) {
	instances, err := ar.reg.List()
	if err != nil {
		return nil, fmt.Errorf("multiinstance/router: list instances: %w", err)
	}

	// Index instances by ULID for fast lookup.
	byULID := make(map[string]Instance, len(instances))
	for _, inst := range instances {
		byULID[inst.ULID] = inst
	}

	var table []AppEntry
	for _, pub := range apps {
		if pub.InstanceULID != "" {
			// App pinned to a specific instance.
			inst, ok := byULID[pub.InstanceULID]
			if !ok {
				continue // instance not in registry
			}
			// NODE-CAP-01: a store-only member syncs but is never an ingress /
			// route target — advertising an FQDN for it would send client
			// traffic to a box that does not serve. Skip it even when pinned.
			if inst.StoreOnly {
				continue
			}
			table = append(table, ar.entry(pub.App, pub.Profile, inst))
		} else {
			// Expand across all known instances.
			for _, inst := range instances {
				if inst.StoreOnly {
					continue // NODE-CAP-01: store-only members are not route targets
				}
				table = append(table, ar.entry(pub.App, pub.Profile, inst))
			}
		}
	}
	return table, nil
}

// entry creates one AppEntry for the given app/profile on inst.
func (ar *AppRouter) entry(app, profile string, inst Instance) AppEntry {
	return AppEntry{
		App:                 app,
		Profile:             profile,
		InstanceULID:        inst.ULID,
		InstanceDisplayName: inst.DisplayName,
		InstanceEndpoint:    inst.EndpointURL,
		FQDN:                FQDN(app, profile, inst.ULID, ar.baseDomain),
	}
}

// RegisterHandlers wires GET /api/routing/apps into mux.
//
// When an appProvider was registered via WithAppProvider, it is called on each
// request to obtain the current published-app list.  Otherwise the handler
// returns an empty table (callers must use BuildTable directly in that case).
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterHandlers(mux, router)
func RegisterHandlers(mux *http.ServeMux, ar *AppRouter) {
	mux.HandleFunc("GET /api/routing/apps", func(w http.ResponseWriter, r *http.Request) {
		var apps []PublishedApp
		if ar.appProvider != nil {
			apps = ar.appProvider()
		}

		table, err := ar.BuildTable(apps)
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if table == nil {
			table = []AppEntry{} // return [] not null
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(table); err != nil {
			// Headers already sent; nothing useful we can do.
			return
		}
	})
}
