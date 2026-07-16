package appsplatform

// Open-core registry seam (standalone default vs. cloud control plane).
//
// The Registry interface (registry.go) is the seam. The STANDALONE DEFAULT
// (StandaloneRegistry) lives in this package and is what every product gets by
// default — pure-Go SQLite with an in-memory fallback, no external services.
//
// A Vulos Cloud "developer console" / control plane implements the SAME Registry
// interface in a SEPARATE package that THIS CORE NEVER IMPORTS, e.g.:
//
//	package cloudapps // in vulos-cloud, NOT imported by appsplatform
//
//	type CloudRegistry struct { /* gRPC/HTTP client to the control plane */ }
//	var _ appsplatform.Registry = (*CloudRegistry)(nil)
//	func (r *CloudRegistry) Create(p appsplatform.CreateParams) (*appsplatform.Created, error) { ... }
//	// ...the rest of the interface, brokered org-scoped + centrally audited...
//
// Only a product's composition root (its main.go) decides which implementation
// to wire, and only when explicitly selected:
//
//	var reg appsplatform.Registry
//	if cfg.UseCloudControlPlane {
//	    reg = cloudapps.New(cfg.CloudEndpoint) // optional adapter
//	} else {
//	    reg, _ = appsplatform.NewStandaloneRegistry(cfg.AppsDB) // standalone default
//	}
//	handler, _ := appsplatform.NewHandler(appsplatform.MountConfig{Registry: reg, ...})
//
// Because the data plane (the HTTP handler set, dispatcher, signing, webhooks,
// SSE) depends only on the Registry interface, removing the cloud package never
// breaks the core build. RegistryFactory documents that wiring contract for
// composition roots that want to select an implementation by name/config.
type RegistryFactory func() (Registry, error)
