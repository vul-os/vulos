// Package peering implements Vulos' direct instance-to-instance communication layer.
//
// Every Vulos instance is a server — if you're running Vulos, you can receive.
// Peering enables server-to-server messaging, media transfer, calls, and
// real-time collaboration without relay infrastructure or third-party accounts.
//
// Storage layout (created idempotently on New):
//
//	~/.vulos/peering/
//	  ├── identity/    (keypair, Vula ID, verification token)
//	  ├── profile/     (avatar.webp, profile.json, visibility settings)
//	  ├── inbox/       (received messages, indexed by conversation)
//	  ├── outbox/      (sent messages, pending delivery)
//	  ├── media/       (received files, images, video)
//	  ├── groups/      (group definitions)
//	  └── contacts.json (approved contact list)
package peering

import (
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// subdirs are the directories created under the peering root on initialisation.
var subdirs = []string{
	"identity",
	"profile",
	"inbox",
	"outbox",
	"media",
	"groups",
}

// Service owns the ~/.vulos/peering/ storage tree and exposes the peering API.
type Service struct {
	// home is the user's home directory passed to New() (parent of .vulos).
	home string

	// root is the absolute path to ~/.vulos/peering/
	root string

	// Identity fields — populated by New() from loadOrGenerate.
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	vulaID string
}

// Home returns the user home directory the service was constructed with
// (the parent of .vulos). Constructors that derive their own paths from a
// home directory (NewContactStore, NewInboxStore, NewMediaStore, …) should be
// passed this value so every peering sub-store shares the same tree.
func (s *Service) Home() string { return s.home }

// Root returns the absolute path to ~/.vulos/peering/ — the peering storage
// root. Pass this to constructors that take the peering directory directly
// (NewFeedStore, NewCollabStore, the profile store dir, …).
func (s *Service) Root() string { return s.root }

// PrivateKey returns the node's Ed25519 private key (used to sign outbound
// envelopes / feed entries / signed media URLs). It is the same key the
// identity endpoints expose the public half of.
func (s *Service) PrivateKey() ed25519.PrivateKey { return s.priv }

// PublicKey returns the node's Ed25519 public key.
func (s *Service) PublicKey() ed25519.PublicKey { return s.pub }

// VulaID returns the node's canonical Vula ID ("vula:ed25519:<base58>").
func (s *Service) VulaID() string { return s.vulaID }

// New creates a Service and ensures the full ~/.vulos/peering/ directory tree
// exists. The tree is created idempotently — safe to call multiple times or
// when the directories already exist.
//
// home should be the value of os.UserHomeDir() (or the user's home path).
func New(home string) *Service {
	root := filepath.Join(home, "peering")

	// Create subdirectories idempotently.
	for _, sub := range subdirs {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0700); err != nil {
			log.Printf("[peering] mkdir %s: %v", dir, err)
		}
	}

	// Create contacts.json if absent.
	contactsPath := filepath.Join(root, "contacts.json")
	if _, err := os.Stat(contactsPath); os.IsNotExist(err) {
		initial, _ := json.Marshal(map[string]any{"contacts": []any{}})
		if err := os.WriteFile(contactsPath, initial, 0600); err != nil {
			log.Printf("[peering] init contacts.json: %v", err)
		}
	}

	// Load or generate the Ed25519 identity keypair.
	identityDir := filepath.Join(root, "identity")
	priv, pub, vulaID, err := loadOrGenerate(identityDir)
	if err != nil {
		log.Printf("[peering] identity init error: %v", err)
	}

	log.Printf("[peering] storage root: %s", root)
	return &Service{home: home, root: root, priv: priv, pub: pub, vulaID: vulaID}
}

// RegisterHandlers wires the node-identity peering routes onto mux.
//
// Implemented routes (PEER-01 + PEER-02):
//
//	GET  /api/peering/identity           → own Vula ID + public key (200)
//	POST /api/peering/identity/export    → export encrypted keypair bundle
//	POST /api/peering/identity/import    → import encrypted keypair bundle
//
// As of PEER-42 every other peering route (identity verify/confirm,
// profile/*, contacts/*, conversations/*, media/*, call/*, groups/*,
// feeds/*, relay/*, collab/*, drop/*, ice, discover, endpoints/*,
// call/history, inbound/*, …) is served by its own dedicated Register*
// handler set, wired in cmd/server/main.go onto a peering sub-mux (with
// the /api/peering/inbound/ subtree wrapped in InboundMiddleware). Those
// routes are no longer 501 stubs here — the stub registrations were
// removed because http.ServeMux panics on a duplicate "METHOD /path".
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	// --- Identity (PEER-02: implemented) ---

	// GET /api/peering/identity — returns the node's Vula ID and public key.
	mux.HandleFunc("GET /api/peering/identity", s.handleGetIdentity)

	// POST /api/peering/identity/export — export keypair encrypted with passphrase.
	mux.HandleFunc("POST /api/peering/identity/export", s.handleExportIdentity)

	// POST /api/peering/identity/import — import an encrypted keypair bundle.
	mux.HandleFunc("POST /api/peering/identity/import", s.handleImportIdentity)

	// --- PEER-42: the identity/verify+confirm, profile/*, contacts/*,
	//     conversations/*, media/upload, call/* and inbound/* routes that
	//     were previously 501 stubs here are now served by the real
	//     Register* handler sets wired in cmd/server/main.go (RegisterVerify
	//     Handlers, RegisterProfileHandlers, RegisterContactHandlers,
	//     RegisterMessageHandlers, RegisterMediaHandlers, RegisterCall
	//     Handlers, …). The stub registrations were removed: Go's
	//     http.ServeMux panics if the same "METHOD /path" is registered
	//     twice, so a stub and its real handler cannot coexist (same
	//     approach already used for the PEER-20b bandwidth stub above and
	//     in commit 001192c). The Service now only owns the identity
	//     get/export/import endpoints; every other peering route is owned
	//     by a dedicated Register* handler set.
}

// stub returns a handler that replies 501 Not Implemented with a JSON body
// describing the planned feature. Used to document and reserve routes before
// the implementing wave ships.
//
// As of PEER-42 no peering route is stubbed (every route has a real handler),
// so this helper currently has no callers. It is intentionally retained so a
// future not-yet-implemented route can be reserved with a 501 without
// re-deriving the JSON shape — and to keep the contract documented in one
// place. Unused methods are valid Go and do not affect build/vet.
//
// features that are declared in the API contract but not yet implemented. It is
// kept so the contract lives in one place rather than being re-derived per route.
//
//nolint:unused
//lint:ignore U1000 Reserved: this is the documented 501 responder for peering
func (s *Service) stub(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		json.NewEncoder(w).Encode(map[string]string{
			"error":   "not implemented",
			"feature": feature,
			"note":    "this route is scaffolded and will be implemented in a future wave",
		})
	}
}
