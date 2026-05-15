// Package peering implements Vula's direct instance-to-instance communication layer.
//
// Every Vula instance is a server — if you're running Vula, you can receive.
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
	// root is the absolute path to ~/.vulos/peering/
	root string
}

// New creates a Service and ensures the full ~/.vulos/peering/ directory tree
// exists. The tree is created idempotently — safe to call multiple times or
// when the directories already exist.
//
// home should be the value of os.UserHomeDir() (or the user's home path).
func New(home string) *Service {
	root := filepath.Join(home, ".vulos", "peering")

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

	log.Printf("[peering] storage root: %s", root)
	return &Service{root: root}
}

// RegisterHandlers wires all peering API routes onto mux.
//
// Implemented routes:
//
//	GET  /api/peering/identity           → own Vula ID stub (200)
//
// Planned routes (501 Not Implemented until the relevant wave ships):
//
//	POST /api/peering/identity/export
//	POST /api/peering/identity/import
//	POST /api/peering/identity/verify
//	POST /api/peering/identity/confirm
//	GET  /api/peering/profile
//	PUT  /api/peering/profile
//	POST /api/peering/profile/image
//	GET  /api/peering/profile/image
//	GET  /api/peering/contacts
//	POST /api/peering/contacts/request
//	GET  /api/peering/contacts/requests
//	POST /api/peering/contacts/approve/{id}
//	POST /api/peering/contacts/block/{id}
//	DELETE /api/peering/contacts/{id}
//	GET  /api/peering/conversations
//	GET  /api/peering/conversations/{id}/messages
//	POST /api/peering/conversations/{id}/send
//	POST /api/peering/media/upload
//	POST /api/peering/call/initiate
//	POST /api/peering/call/answer
//	POST /api/peering/call/reject
//	POST /api/peering/call/signal
//	POST /api/peering/call/hangup
//	GET  /api/peering/bandwidth
//	POST /api/peering/inbound/request
//	POST /api/peering/inbound/message
//	POST /api/peering/inbound/signal
//	POST /api/peering/inbound/media
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	// --- Identity (implemented) ---

	// GET /api/peering/identity — returns own Vula ID stub.
	// Full implementation (keypair generation, persistent Vula ID) ships in PEER-02.
	mux.HandleFunc("GET /api/peering/identity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"vula_id":      "",
			"display_name": "",
			"verified":     false,
			"storage_root": s.root,
			"status":       "scaffold — keypair generation not yet implemented",
		})
	})

	// --- Identity (planned) ---
	mux.HandleFunc("POST /api/peering/identity/export", s.stub("identity export"))
	mux.HandleFunc("POST /api/peering/identity/import", s.stub("identity import"))
	mux.HandleFunc("POST /api/peering/identity/verify", s.stub("email verification initiation"))
	mux.HandleFunc("POST /api/peering/identity/confirm", s.stub("email verification confirmation"))

	// --- Profile (planned) ---
	mux.HandleFunc("GET /api/peering/profile", s.stub("own profile"))
	mux.HandleFunc("PUT /api/peering/profile", s.stub("update profile"))
	mux.HandleFunc("POST /api/peering/profile/image", s.stub("upload avatar"))
	mux.HandleFunc("GET /api/peering/profile/image", s.stub("serve avatar"))

	// --- Contacts (planned) ---
	mux.HandleFunc("GET /api/peering/contacts", s.stub("contact list"))
	mux.HandleFunc("POST /api/peering/contacts/request", s.stub("send contact request"))
	mux.HandleFunc("GET /api/peering/contacts/requests", s.stub("pending requests"))
	mux.HandleFunc("POST /api/peering/contacts/approve/{id}", s.stub("approve contact request"))
	mux.HandleFunc("POST /api/peering/contacts/block/{id}", s.stub("block contact"))
	mux.HandleFunc("DELETE /api/peering/contacts/{id}", s.stub("remove contact"))

	// --- Messaging (planned) ---
	mux.HandleFunc("GET /api/peering/conversations", s.stub("conversation list"))
	mux.HandleFunc("GET /api/peering/conversations/{id}/messages", s.stub("messages in conversation"))
	mux.HandleFunc("POST /api/peering/conversations/{id}/send", s.stub("send message"))
	mux.HandleFunc("POST /api/peering/media/upload", s.stub("upload media attachment"))

	// --- Calls / Signaling (planned) ---
	mux.HandleFunc("POST /api/peering/call/initiate", s.stub("initiate call"))
	mux.HandleFunc("POST /api/peering/call/answer", s.stub("answer call"))
	mux.HandleFunc("POST /api/peering/call/reject", s.stub("reject call"))
	mux.HandleFunc("POST /api/peering/call/signal", s.stub("relay ICE/SDP"))
	mux.HandleFunc("POST /api/peering/call/hangup", s.stub("end call"))

	// --- Bandwidth (planned) ---
	mux.HandleFunc("GET /api/peering/bandwidth", s.stub("bandwidth report"))

	// --- Inbound server-to-server (planned) ---
	mux.HandleFunc("POST /api/peering/inbound/request", s.stub("inbound contact request"))
	mux.HandleFunc("POST /api/peering/inbound/message", s.stub("inbound message"))
	mux.HandleFunc("POST /api/peering/inbound/signal", s.stub("inbound call signal"))
	mux.HandleFunc("POST /api/peering/inbound/media", s.stub("inbound media"))
}

// stub returns a handler that replies 501 Not Implemented with a JSON body
// describing the planned feature. Used to document and reserve routes before
// the implementing wave ships.
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
