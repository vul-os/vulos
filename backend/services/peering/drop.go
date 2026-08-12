// Package peering implements Vula OS peer-to-peer communication services.
// drop.go implements AirDrop-style LAN discovery and file transfer ("Drop").
//
// mDNS service type: _vulos-drop._tcp.local
// Discoverability: everyone | peers | nobody (default: peers)
// Transfer: LAN-first via media-path HTTP, internet fallback.
package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	pionmdns "github.com/pion/mdns/v2"
	"golang.org/x/net/ipv4"
)

// ---------------------------------------------------------------------------
// Discoverability constants
// ---------------------------------------------------------------------------

// dropDiscoverability controls who can see this instance via mDNS.
type dropDiscoverability string

const (
	// dropDiscoverEveryone advertises to all Vula peers on the LAN.
	dropDiscoverEveryone dropDiscoverability = "everyone"
	// dropDiscoverPeers advertises only to approved contacts.
	dropDiscoverPeers dropDiscoverability = "peers"
	// dropDiscoverNobody disables all mDNS advertisement.
	dropDiscoverNobody dropDiscoverability = "nobody"
)

// dropServiceType is the mDNS service name advertised for Drop.
const dropServiceType = "_vulos-drop._tcp.local"

// dropDefaultPort is the HTTP port for inbound Drop requests.
const dropDefaultPort = 8080

// ---------------------------------------------------------------------------
// External service interfaces (contacts + media, resolved at runtime)
// ---------------------------------------------------------------------------

// DropContactChecker allows drop.go to ask whether a given Vula ID is an
// approved contact. Satisfied by the contacts sub-service.
type DropContactChecker interface {
	IsApproved(vulosID string) bool
}

// DropMediaSender allows drop.go to initiate server-to-server media transfers.
// Satisfied by the media sub-service.
type DropMediaSender interface {
	// SendFile transfers a file to the target peer.
	// targetAddr is the peer's HTTP base URL (e.g. "http://192.168.1.5:8080").
	// mediaPath is the local absolute path of the file to send.
	// Returns a transfer ID or error.
	SendFile(ctx context.Context, targetAddr, mediaPath, mimeType string) (transferID string, err error)

	// ReceiveFile downloads an accepted inbound transfer from the sender's
	// signed downloadURL and lands it locally (content store + a user-visible
	// copy under the recipient's Downloads). fileName is the sender-declared
	// name, used for the landed copy.
	ReceiveFile(ctx context.Context, downloadURL, fileName, mimeType string) error
}

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

// DropPeer is a discovered nearby Vula peer on the local network.
type DropPeer struct {
	VulosID      string    `json:"vulos_id"`
	DisplayName  string    `json:"display_name"`
	Addr         string    `json:"addr"` // host:port
	DiscoveredAt time.Time `json:"discovered_at"`
	IsContact    bool      `json:"is_contact"`
}

// dropSettings holds persistent discoverability configuration.
type dropSettings struct {
	Discoverability dropDiscoverability `json:"discoverability"`
	// Port this instance listens on for Drop HTTP transfers.
	Port int `json:"port"`
}

// dropInboundRequest is the payload sent by a peer initiating a Drop.
type dropInboundRequest struct {
	TransferID  string `json:"transfer_id"`
	FromVulosID string `json:"from_vulos_id"`
	DisplayName string `json:"display_name"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	MimeType    string `json:"mime_type"`
	// DownloadURL is the URL from which this instance can fetch the file.
	DownloadURL string `json:"download_url"`
}

// dropDecision is the user's accept/decline response.
type dropDecision struct {
	TransferID string `json:"transfer_id"`
	Accept     bool   `json:"accept"`
}

// dropSendRequest is the payload for POST /api/peering/drop/send.
type dropSendRequest struct {
	// TargetVulosID is the recipient's Vula ID.
	TargetVulosID string `json:"target_vulos_id"`
	// TargetAddr is the peer's HTTP base URL; if empty, resolved from nearby list.
	TargetAddr string `json:"target_addr,omitempty"`
	// MediaPath is the absolute local path of the file to send.
	MediaPath string `json:"media_path"`
	// MimeType is the MIME type of the file.
	MimeType string `json:"mime_type"`
}

// dropSettingsUpdate is the payload for PUT /api/peering/drop/settings.
type dropSettingsUpdate struct {
	Discoverability dropDiscoverability `json:"discoverability"`
}

// ---------------------------------------------------------------------------
// MDNSAdvertiser — mockable interface wrapping pion/mdns Conn
// ---------------------------------------------------------------------------

// dropMDNSConn is the subset of *pionmdns.Conn that Drop uses, allowing
// tests to inject a fake without a real multicast socket.
type dropMDNSConn interface {
	Close() error
}

// dropMDNSFactory creates an mDNS server for a given local hostname. Tests
// replace this with a no-op factory.
type dropMDNSFactory func(localName string) (dropMDNSConn, error)

// dropDefaultMDNSFactory is the real factory using pion/mdns.
func dropDefaultMDNSFactory(localName string) (dropMDNSConn, error) {
	addr, err := net.ResolveUDPAddr("udp4", pionmdns.DefaultAddressIPv4)
	if err != nil {
		return nil, fmt.Errorf("drop mDNS resolve addr: %w", err)
	}
	l, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("drop mDNS listen: %w", err)
	}
	conn, err := pionmdns.Server(ipv4.NewPacketConn(l), nil, &pionmdns.Config{
		LocalNames: []string{localName},
	})
	if err != nil {
		l.Close()
		return nil, fmt.Errorf("drop mDNS server: %w", err)
	}
	return conn, nil
}

// ---------------------------------------------------------------------------
// DropService
// ---------------------------------------------------------------------------

// DropService manages AirDrop-style LAN discovery and file transfer.
type DropService struct {
	mu sync.RWMutex

	// configuration
	selfVulosID string
	selfDisplay string
	settings    dropSettings

	// dependencies
	contacts DropContactChecker
	media    DropMediaSender
	mdnsNew  dropMDNSFactory

	// mDNS server (nil when discoverability == nobody)
	mdnsConn dropMDNSConn

	// discovered peers — keyed by VulosID
	peers map[string]*DropPeer

	// pending inbound transfers — keyed by TransferID
	pending map[string]*dropInboundRequest

	// auto-accept from contacts flag
	autoAcceptContacts bool
}

// NewDropService creates a DropService. selfVulosID and selfDisplay are this
// instance's identity. contacts and media may be nil (functionality degrades
// gracefully).
func NewDropService(
	selfVulosID, selfDisplay string,
	contacts DropContactChecker,
	media DropMediaSender,
) *DropService {
	s := &DropService{
		selfVulosID: selfVulosID,
		selfDisplay: selfDisplay,
		settings: dropSettings{
			Discoverability: dropDiscoverPeers,
			Port:            dropDefaultPort,
		},
		contacts: contacts,
		media:    media,
		mdnsNew:  dropDefaultMDNSFactory,
		peers:    make(map[string]*DropPeer),
		pending:  make(map[string]*dropInboundRequest),
	}
	return s
}

// Start begins mDNS advertisement according to current discoverability.
// It is safe to call multiple times; each call replaces the current server.
func (s *DropService) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropStartAdvertising()
	// Periodically prune stale peers (older than 5 minutes)
	go s.dropPruneLoop(ctx)
}

// Stop shuts down mDNS advertisement.
func (s *DropService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropStopAdvertising()
}

// dropStartAdvertising starts mDNS; must be called with s.mu held.
func (s *DropService) dropStartAdvertising() {
	s.dropStopAdvertising()
	if s.settings.Discoverability == dropDiscoverNobody {
		return
	}
	hostname := dropHostname(s.selfVulosID)
	conn, err := s.mdnsNew(hostname)
	if err != nil {
		log.Printf("[drop] mDNS start: %v", err)
		return
	}
	s.mdnsConn = conn
	log.Printf("[drop] mDNS advertising %s", hostname)
}

// dropStopAdvertising closes the current mDNS server; must hold s.mu.
func (s *DropService) dropStopAdvertising() {
	if s.mdnsConn != nil {
		if err := s.mdnsConn.Close(); err != nil {
			log.Printf("[drop] mDNS close: %v", err)
		}
		s.mdnsConn = nil
	}
}

// dropHostname derives the mDNS local hostname from a Vula ID.
// Uses the last 16 chars of the ID to keep names short.
func dropHostname(vulosID string) string {
	suffix := vulosID
	if len(suffix) > 16 {
		suffix = suffix[len(suffix)-16:]
	}
	return fmt.Sprintf("vula-%s.%s", suffix, dropServiceType)
}

// dropPruneLoop removes peers that have not been refreshed for >5 minutes.
func (s *DropService) dropPruneLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			threshold := time.Now().Add(-5 * time.Minute)
			s.mu.Lock()
			for id, p := range s.peers {
				if p.DiscoveredAt.Before(threshold) {
					delete(s.peers, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// ---------------------------------------------------------------------------
// Peer registration (called by mDNS browse loop / TXT-record announcements)
// ---------------------------------------------------------------------------

// DropRegisterPeer registers or refreshes a discovered nearby peer.
// Callers (e.g., a separate mDNS browse loop) invoke this when they observe
// a _vulos-drop._tcp advertisement.
func (s *DropService) DropRegisterPeer(vulosID, displayName, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	isContact := false
	if s.contacts != nil {
		isContact = s.contacts.IsApproved(vulosID)
	}

	// Filter: if discoverability is "peers", only show contacts in our list.
	// (We still record everyone so that "everyone" mode works correctly on reload.)
	s.peers[vulosID] = &DropPeer{
		VulosID:      vulosID,
		DisplayName:  displayName,
		Addr:         addr,
		DiscoveredAt: time.Now(),
		IsContact:    isContact,
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// RegisterDropHandlers wires all Drop endpoints onto mux.
//
//	GET  /api/peering/drop/nearby           → list discovered peers
//	POST /api/peering/drop/send             → send a file to a peer
//	PUT  /api/peering/drop/settings         → update discoverability
//	POST /api/peering/inbound/drop          → receive an inbound drop request
//	POST /api/peering/drop/decide           → accept or decline a pending drop
func RegisterDropHandlers(mux *http.ServeMux, svc *DropService) {
	mux.HandleFunc("/api/peering/drop/nearby", svc.dropHandleNearby)
	mux.HandleFunc("/api/peering/drop/send", svc.dropHandleSend)
	mux.HandleFunc("/api/peering/drop/settings", svc.dropHandleSettings)
	mux.HandleFunc("/api/peering/inbound/drop", svc.dropHandleInbound)
	mux.HandleFunc("/api/peering/drop/decide", svc.dropHandleDecide)
}

// dropHandleNearby returns the list of discovered nearby Vula peers.
// Applies the discoverability filter: in "peers" mode only contacts are shown.
func (s *DropService) dropHandleNearby(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	disc := s.settings.Discoverability
	var out []DropPeer
	for _, p := range s.peers {
		switch disc {
		case dropDiscoverEveryone:
			out = append(out, *p)
		case dropDiscoverPeers:
			if p.IsContact {
				out = append(out, *p)
			}
		case dropDiscoverNobody:
			// return empty list — this node is not discoverable
		}
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// dropHandleSend initiates a Drop transfer to a nearby peer.
func (s *DropService) dropHandleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req dropSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetVulosID == "" {
		http.Error(w, "target_vulos_id required", http.StatusBadRequest)
		return
	}
	if req.MediaPath == "" {
		http.Error(w, "media_path required", http.StatusBadRequest)
		return
	}

	// Resolve target address: prefer explicitly supplied addr, then nearby list.
	targetAddr := req.TargetAddr
	if targetAddr == "" {
		s.mu.RLock()
		p, ok := s.peers[req.TargetVulosID]
		if ok {
			targetAddr = "http://" + p.Addr
		}
		s.mu.RUnlock()
		if targetAddr == "" {
			http.Error(w, "peer not found in nearby list", http.StatusNotFound)
			return
		}
	}

	if s.media == nil {
		http.Error(w, "media service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Validate file exists before initiating.
	if _, err := os.Stat(req.MediaPath); err != nil {
		http.Error(w, "media_path not accessible: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	transferID, err := s.media.SendFile(ctx, targetAddr, req.MediaPath, req.MimeType)
	if err != nil {
		http.Error(w, "transfer failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"transfer_id": transferID}) //nolint:errcheck
}

// dropHandleSettings updates discoverability and restarts mDNS if needed.
func (s *DropService) dropHandleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		cfg := s.settings
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg) //nolint:errcheck
		return

	case http.MethodPut:
		var upd dropSettingsUpdate
		if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch upd.Discoverability {
		case dropDiscoverEveryone, dropDiscoverPeers, dropDiscoverNobody:
		default:
			http.Error(w, "discoverability must be everyone|peers|nobody", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.settings.Discoverability = upd.Discoverability
		s.dropStartAdvertising() // restart with new setting
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// dropHandleInbound handles an inbound drop request from a remote peer.
// The remote peer POSTs metadata; this instance shows a notification.
// Auto-accept is applied if the sender is an approved contact.
func (s *DropService) dropHandleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req dropInboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TransferID == "" || req.FromVulosID == "" {
		http.Error(w, "transfer_id and from_vulos_id required", http.StatusBadRequest)
		return
	}

	isContact := false
	if s.contacts != nil {
		isContact = s.contacts.IsApproved(req.FromVulosID)
	}

	s.mu.Lock()
	s.pending[req.TransferID] = &req
	autoAccept := s.autoAcceptContacts && isContact
	s.mu.Unlock()

	if autoAccept {
		log.Printf("[drop] auto-accepting transfer %s from contact %s", req.TransferID, req.FromVulosID)
		s.dropExecuteAccept(&req)
	} else {
		log.Printf("[drop] pending transfer %s from %s (%s, %.1f KB)",
			req.TransferID, req.DisplayName, req.FileName, float64(req.FileSize)/1024)
	}

	status := "pending"
	if autoAccept {
		status = "accepted"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"status":      status,
		"transfer_id": req.TransferID,
	})
}

// dropHandleDecide accepts or declines a pending inbound transfer.
func (s *DropService) dropHandleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var dec dropDecision
	if err := json.NewDecoder(r.Body).Decode(&dec); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if dec.TransferID == "" {
		http.Error(w, "transfer_id required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	req, ok := s.pending[dec.TransferID]
	if ok {
		delete(s.pending, dec.TransferID)
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}

	if !dec.Accept {
		log.Printf("[drop] declined transfer %s", dec.TransferID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "declined"}) //nolint:errcheck
		return
	}

	s.dropExecuteAccept(req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"}) //nolint:errcheck
}

// dropExecuteAccept triggers the actual file download from the sender.
func (s *DropService) dropExecuteAccept(req *dropInboundRequest) {
	if req.DownloadURL == "" {
		log.Printf("[drop] accept transfer %s: no download_url", req.TransferID)
		return
	}
	if s.media == nil {
		log.Printf("[drop] accept transfer %s: media service unavailable", req.TransferID)
		return
	}
	fileName := req.FileName
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// Fetch from the sender's signed download URL; media service verifies
		// the content hash and lands a copy in Downloads under fileName.
		err := s.media.ReceiveFile(ctx, req.DownloadURL, fileName, req.MimeType)
		if err != nil {
			log.Printf("[drop] transfer %s download error: %v", req.TransferID, err)
		} else {
			log.Printf("[drop] transfer %s complete: %s", req.TransferID, fileName)
		}
	}()
}

// ---------------------------------------------------------------------------
// Auto-accept toggle
// ---------------------------------------------------------------------------

// DropSetAutoAcceptContacts enables or disables automatic acceptance of
// incoming drops from approved contacts.
func (s *DropService) DropSetAutoAcceptContacts(enabled bool) {
	s.mu.Lock()
	s.autoAcceptContacts = enabled
	s.mu.Unlock()
}

// DropAutoAcceptContacts returns the current auto-accept setting.
func (s *DropService) DropAutoAcceptContacts() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.autoAcceptContacts
}

// ---------------------------------------------------------------------------
// Exported accessors for tests / health checks
// ---------------------------------------------------------------------------

// DropNearby returns a snapshot of the current nearby peer list.
// Applies the same discoverability filter as the HTTP handler.
func (s *DropService) DropNearby() []DropPeer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	disc := s.settings.Discoverability
	var out []DropPeer
	for _, p := range s.peers {
		switch disc {
		case dropDiscoverEveryone:
			out = append(out, *p)
		case dropDiscoverPeers:
			if p.IsContact {
				out = append(out, *p)
			}
		}
	}
	return out
}

// DropSettings returns a copy of current settings.
func (s *DropService) DropSettings() dropSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// DropIsAdvertising reports whether the mDNS server is currently running.
func (s *DropService) DropIsAdvertising() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mdnsConn != nil
}
