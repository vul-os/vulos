// lobby.go — pre-call lobby: bandwidth aggregation + SFU host / capacity
// estimation (PEER-25).
//
// # Overview
//
// The pre-call lobby collects bandwidth metrics from every participant before
// the call starts so users can:
//   - see per-peer upload / download / latency at a glance
//   - select (or let the system default to) the best SFU host
//   - see an upfront capacity estimate based on the host's upload speed
//
// # Routes registered by RegisterLobbyHandlers
//
//	POST /api/peering/call/lobby      — collect bandwidth for all participants
//	                                    and return consolidated report + capacity
//	POST /api/peering/call/lobby/host — recompute capacity for a chosen host
//	                                    without re-fetching bandwidth
//
// # Capacity formula (mirrors Lobby.jsx)
//
//	video_cap = min(50,  floor(host_upload_mbps / 3))        // Last-N=6, 360p, 500 kbps
//	audio_cap = min(200, floor(host_upload_mbps / 0.096))    // Opus 32 kbps + 3-speaker mix
//
// Fetching a peer's bandwidth is done by proxying
// GET https://{peer.server}/api/peering/bandwidth — the same endpoint
// already exposed by RegisterBandwidthHandlers.  LobbyService only needs
// the BandwidthMeter for the *local* node's own reading.
package peering

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"
)

// ─── Wire types ───────────────────────────────────────────────────────────────

// LobbyParticipant is one entry in the POST /api/peering/call/lobby request.
type LobbyParticipant struct {
	// ID is the participant's Vula ID (required).
	ID string `json:"id"`

	// DisplayName is shown in the bandwidth table (optional — falls back to ID).
	DisplayName string `json:"display_name,omitempty"`

	// Server is the peer's "host:port" address (optional; empty means this is
	// the local/initiating participant, whose bandwidth is read from the local
	// BandwidthMeter).
	Server string `json:"server,omitempty"`
}

// lobbyReq is the JSON body for POST /api/peering/call/lobby.
type lobbyReq struct {
	// Participants lists every attendee.  The first entry whose Server is empty
	// (or whose ID matches InitiatorID) is treated as the local node.
	Participants []LobbyParticipant `json:"participants"`

	// InitiatorID identifies the call initiator; used to choose the default SFU
	// host when no explicit HostID is given.  Defaults to Participants[0].
	InitiatorID string `json:"initiator_id,omitempty"`

	// HostID is an optional explicit host pre-selection.  When provided the
	// capacity estimate is computed for this participant.  When absent the
	// highest-upload participant is chosen automatically.
	HostID string `json:"host_id,omitempty"`
}

// LobbyParticipantReport is the per-participant bandwidth entry in the response.
type LobbyParticipantReport struct {
	ID           string  `json:"id"`
	DisplayName  string  `json:"display_name,omitempty"`
	UploadMbps   float64 `json:"upload_mbps"`
	DownloadMbps float64 `json:"download_mbps"`
	LatencyMs    float64 `json:"latency_ms"`
	Source       string  `json:"source,omitempty"` // "speedtest" | "traffic" | "cache" | "peer" | "unavailable"
	Error        string  `json:"error,omitempty"`
}

// LobbyCapacity is the SFU capacity estimate for the selected host.
type LobbyCapacity struct {
	// VideoParticipants is the max number of video streams the host can forward.
	// Formula: min(50, floor(upload_mbps / 3))
	VideoParticipants int `json:"video_participants"`

	// AudioParticipants is the max simultaneous audio-only participants.
	// Formula: min(200, floor(upload_mbps / 0.096))
	AudioParticipants int `json:"audio_participants"`

	// HostUploadMbps is the upload speed used for the calculation.
	HostUploadMbps float64 `json:"host_upload_mbps"`
}

// LobbyReport is the JSON response for POST /api/peering/call/lobby.
type LobbyReport struct {
	// Participants is the full bandwidth table, one entry per input participant.
	Participants []LobbyParticipantReport `json:"participants"`

	// HostID is the participant ID selected as the SFU host.
	HostID string `json:"host_id"`

	// Capacity is the capacity estimate for the selected host.
	Capacity LobbyCapacity `json:"capacity"`
}

// lobbyHostReq is the JSON body for POST /api/peering/call/lobby/host.
// It allows the frontend to recompute capacity for a new host selection
// without re-fetching bandwidth.
type lobbyHostReq struct {
	// HostID is the participant to use as the SFU host.
	HostID string `json:"host_id"`

	// KnownBandwidth is the already-fetched bandwidth map keyed by participant ID.
	// Values are upload_mbps for the corresponding participant.
	KnownBandwidth map[string]float64 `json:"known_bandwidth"`
}

// lobbyHostResp is the JSON response for POST /api/peering/call/lobby/host.
type lobbyHostResp struct {
	HostID   string        `json:"host_id"`
	Capacity LobbyCapacity `json:"capacity"`
}

// ─── LobbyService ─────────────────────────────────────────────────────────────

// LobbyService handles pre-call lobby requests.
// It is safe for concurrent use.
type LobbyService struct {
	meter  *BandwidthMeter
	client *http.Client
}

// NewLobbyService creates a LobbyService.
// meter provides the local node's own bandwidth reading.
func NewLobbyService(meter *BandwidthMeter) *LobbyService {
	return &LobbyService{
		meter: meter,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// RegisterLobbyHandlers wires all lobby-related HTTP routes onto mux.
//
// Routes registered:
//
//	POST /api/peering/call/lobby
//	POST /api/peering/call/lobby/host
func RegisterLobbyHandlers(mux *http.ServeMux, svc *LobbyService) {
	mux.HandleFunc("POST /api/peering/call/lobby", svc.handleLobby)
	mux.HandleFunc("POST /api/peering/call/lobby/host", svc.handleLobbyHost)
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleLobby processes POST /api/peering/call/lobby.
//
// For each participant:
//   - If participant.Server is empty → reads from the local BandwidthMeter.
//   - Otherwise → proxies GET https://{participant.server}/api/peering/bandwidth.
//
// Failures per-participant are recorded in the report (error field) rather than
// aborting the whole request, so callers always get a table even when some peers
// are temporarily unreachable.
func (s *LobbyService) handleLobby(w http.ResponseWriter, r *http.Request) {
	var req lobbyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeLobbyErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Participants) == 0 {
		writeLobbyErr(w, http.StatusBadRequest, "participants list must not be empty")
		return
	}

	// Determine the initiator (default host candidate).
	initiatorID := req.InitiatorID
	if initiatorID == "" {
		initiatorID = req.Participants[0].ID
	}

	// Fetch bandwidth for all participants concurrently.
	type indexedReport struct {
		idx    int
		report LobbyParticipantReport
	}
	ch := make(chan indexedReport, len(req.Participants))

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	for i, p := range req.Participants {
		i, p := i, p
		go func() {
			ch <- indexedReport{idx: i, report: s.fetchBandwidth(ctx, p)}
		}()
	}

	reports := make([]LobbyParticipantReport, len(req.Participants))
	for range req.Participants {
		ir := <-ch
		reports[ir.idx] = ir.report
	}

	// Build a map for quick lookup.
	bwByID := make(map[string]*LobbyParticipantReport, len(reports))
	for i := range reports {
		bwByID[reports[i].ID] = &reports[i]
	}

	// Determine SFU host.
	hostID := req.HostID
	if hostID == "" {
		// Auto-select: highest upload among participants without errors.
		// Default to initiator so the UI matches the "defaults initiator" AC.
		hostID = initiatorID
		bestUpload := -1.0
		for _, rpt := range reports {
			if rpt.Error == "" && rpt.UploadMbps > bestUpload {
				bestUpload = rpt.UploadMbps
				hostID = rpt.ID
			}
		}
	}

	// Capacity estimate.
	var hostUpload float64
	if hostRpt, ok := bwByID[hostID]; ok && hostRpt.Error == "" {
		hostUpload = hostRpt.UploadMbps
	}
	capacity := estimateLobbyCapacity(hostUpload)

	resp := LobbyReport{
		Participants: reports,
		HostID:       hostID,
		Capacity:     capacity,
	}
	writeLobbyOK(w, resp)
}

// handleLobbyHost processes POST /api/peering/call/lobby/host.
//
// The browser already has the bandwidth table (fetched via handleLobby); this
// endpoint lets it change the host selection and get a new capacity estimate
// without a second round of network fetches.
func (s *LobbyService) handleLobbyHost(w http.ResponseWriter, r *http.Request) {
	var req lobbyHostReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeLobbyErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.HostID == "" {
		writeLobbyErr(w, http.StatusBadRequest, "host_id is required")
		return
	}

	uploadMbps := req.KnownBandwidth[req.HostID]
	capacity := estimateLobbyCapacity(uploadMbps)

	writeLobbyOK(w, lobbyHostResp{
		HostID:   req.HostID,
		Capacity: capacity,
	})
}

// ─── Bandwidth fetch helpers ──────────────────────────────────────────────────

// fetchBandwidth fetches bandwidth for a single participant.
// Local participants (Server == "") are served from the BandwidthMeter;
// remote participants are proxied from their Vula server.
func (s *LobbyService) fetchBandwidth(ctx context.Context, p LobbyParticipant) LobbyParticipantReport {
	report := LobbyParticipantReport{
		ID:          p.ID,
		DisplayName: p.DisplayName,
	}

	if p.Server == "" {
		// Local node — read from the BandwidthMeter.
		result, ready := s.meter.Latest()
		if !ready {
			report.Error = "measuring"
			report.Source = "unavailable"
			return report
		}
		report.UploadMbps = result.UploadMbps
		report.DownloadMbps = result.DownloadMbps
		report.LatencyMs = result.LatencyMs
		report.Source = result.Source
		return report
	}

	// Remote peer — proxy GET https://{server}/api/peering/bandwidth.
	target := "https://" + p.Server + "/api/peering/bandwidth"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		report.Error = "invalid peer address"
		report.Source = "unavailable"
		return report
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("[lobby] bandwidth fetch from %s failed: %v", p.Server, err)
		report.Error = "unreachable"
		report.Source = "unavailable"
		return report
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		// Peer is still warming up its own measurement.
		report.Error = "measuring"
		report.Source = "unavailable"
		return report
	}
	if resp.StatusCode != http.StatusOK {
		report.Error = "http " + resp.Status
		report.Source = "unavailable"
		return report
	}

	var bw BandwidthResult
	if err := json.NewDecoder(resp.Body).Decode(&bw); err != nil {
		report.Error = "invalid response"
		report.Source = "unavailable"
		return report
	}

	report.UploadMbps = bw.UploadMbps
	report.DownloadMbps = bw.DownloadMbps
	report.LatencyMs = bw.LatencyMs
	report.Source = "peer"
	return report
}

// ─── Capacity formula ─────────────────────────────────────────────────────────

// estimateLobbyCapacity computes SFU capacity from the host's upload speed.
//
// Formula (mirrors Lobby.jsx):
//
//	video_participants = min(50,  floor(upload_mbps / 3))
//	audio_participants = min(200, floor(upload_mbps / 0.096))
func estimateLobbyCapacity(uploadMbps float64) LobbyCapacity {
	if uploadMbps <= 0 {
		return LobbyCapacity{}
	}
	video := int(math.Min(50, math.Floor(uploadMbps/3)))
	audio := int(math.Min(200, math.Floor(uploadMbps/0.096)))
	return LobbyCapacity{
		VideoParticipants: video,
		AudioParticipants: audio,
		HostUploadMbps:    uploadMbps,
	}
}

// ─── Response helpers ─────────────────────────────────────────────────────────

func writeLobbyOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeLobbyErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
