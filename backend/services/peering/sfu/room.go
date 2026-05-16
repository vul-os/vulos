// Package sfu implements a Selective Forwarding Unit (SFU) for multi-party
// WebRTC calls on the host's Vula server. It handles N participant
// PeerConnections, 2-layer simulcast per sender, Last-N video forwarding
// (configurable: 4/6/9), and join/leave lifecycle.
//
// No transcoding — RTP packets are forwarded as-is to selected receivers.
// Built with Pion WebRTC (github.com/pion/webrtc/v4).
package sfu

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// SimulcastLayer identifies a simulcast encoding layer.
type SimulcastLayer int

const (
	// LayerHigh is the high-quality simulcast layer (e.g. 720p).
	LayerHigh SimulcastLayer = 1
	// LayerLow is the low-quality simulcast layer (e.g. 180p).
	LayerLow SimulcastLayer = 0
)

// DefaultLastN is the default maximum number of video streams forwarded
// to each receiver when no explicit LastN is set on the room.
const DefaultLastN = 6

// MaxParticipants is the hard cap on participants per room.
const MaxParticipants = 50

// trackEntry holds a received simulcast track and the RTP packets
// buffered for each layer (high/low). The SFU picks the appropriate
// layer to forward to each subscriber.
type trackEntry struct {
	// trackID is the RTP track source identifier.
	trackID string
	// kind is "video" or "audio".
	kind string
	// rid is the RTP stream ID (simulcast layer identifier: "h" = high, "l" = low).
	rid string
	// remote is the Pion remote track.
	remote *webrtc.TrackRemote
	// local maps receiver participant ID → output track written to that peer.
	local map[string]*webrtc.TrackLocalStaticRTP
	mu    sync.Mutex
}

// Participant represents a single peer connection inside a Room.
type Participant struct {
	// ID is a stable identifier for this participant (assigned on join).
	ID string
	// pc is the Pion PeerConnection for this participant.
	pc *webrtc.PeerConnection
	// room is back-reference to the owning room.
	room *Room
	// audioTrack is the audio track this participant sends.
	audioTrack *webrtc.TrackRemote
	// videoTracks holds video tracks keyed by RID (simulcast layer).
	videoTracks map[string]*webrtc.TrackRemote

	// outputAudio is the LocalStaticRTP we write forwarded audio into.
	outputAudio *webrtc.TrackLocalStaticRTP
	// outputVideo maps source participant ID → local video track for that source.
	outputVideo map[string]*webrtc.TrackLocalStaticRTP

	// speaking indicates whether this participant is currently the
	// dominant (most-recently-active) speaker.
	speaking bool

	// lastAudioLevel is the most recently observed audio level (0–127,
	// lower = louder as per RFC 6464). Used for dominant-speaker detection.
	lastAudioLevel uint8

	mu sync.Mutex
}

// Room is a multi-party SFU room. It manages participant PeerConnections,
// simulcast layer selection, and Last-N video forwarding.
type Room struct {
	// ID uniquely identifies this room.
	ID string

	// LastN controls how many video streams are forwarded to each receiver.
	// Must be 4, 6, or 9. Defaults to DefaultLastN (6).
	LastN int

	// api is the Pion WebRTC API instance shared across all PeerConnections
	// in this room.
	api *webrtc.API

	mu           sync.RWMutex
	participants map[string]*Participant

	// dominantSpeaker is the participant ID of the current active speaker.
	dominantSpeaker string

	done chan struct{}
	once sync.Once
}

// NewRoom creates a new SFU room with a random ID and the given Last-N value.
// Valid lastN values are 4, 6, and 9; any other value falls back to DefaultLastN.
func NewRoom(lastN int) *Room {
	if lastN != 4 && lastN != 6 && lastN != 9 {
		lastN = DefaultLastN
	}

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		// Should never happen; RegisterDefaultCodecs only fails on bad state.
		panic("sfu: RegisterDefaultCodecs: " + err.Error())
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	return &Room{
		ID:           uuid.NewString(),
		LastN:        lastN,
		api:          api,
		participants: make(map[string]*Participant),
		done:         make(chan struct{}),
	}
}

// Join adds a new participant PeerConnection to the room. It negotiates
// transceivers so the new participant both sends media to the SFU and
// receives forwarded media back.
//
// Join returns the Participant and the SDP answer to be sent back to the
// browser. The caller is responsible for completing ICE exchange (trickle
// ICE candidates) via the returned Participant.
func (r *Room) Join(offer webrtc.SessionDescription) (*Participant, webrtc.SessionDescription, error) {
	r.mu.Lock()
	if len(r.participants) >= MaxParticipants {
		r.mu.Unlock()
		return nil, webrtc.SessionDescription{}, errRoomFull
	}
	r.mu.Unlock()

	pc, err := r.api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		return nil, webrtc.SessionDescription{}, err
	}

	p := &Participant{
		ID:          uuid.NewString(),
		pc:          pc,
		room:        r,
		videoTracks: make(map[string]*webrtc.TrackRemote),
		outputVideo: make(map[string]*webrtc.TrackLocalStaticRTP),
	}

	// Receive audio from this participant.
	if _, err := pc.AddTransceiverFromKind(
		webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	); err != nil {
		pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	// Receive 2-layer simulcast video from this participant.
	// We add two recv-only transceivers, one for each simulcast encoding.
	for range 2 {
		if _, err := pc.AddTransceiverFromKind(
			webrtc.RTPCodecTypeVideo,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
		); err != nil {
			pc.Close()
			return nil, webrtc.SessionDescription{}, err
		}
	}

	// Wire up incoming tracks.
	pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		r.onTrack(p, remote, receiver)
	})

	// Renegotiate when ICE state changes.
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[sfu] room=%s participant=%s ICE=%s", r.ID, p.ID, state)
		if state == webrtc.ICEConnectionStateFailed ||
			state == webrtc.ICEConnectionStateDisconnected ||
			state == webrtc.ICEConnectionStateClosed {
			r.Leave(p.ID)
		}
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, webrtc.SessionDescription{}, err
	}

	// Register the participant and wire existing participants' output tracks
	// into this connection.
	r.mu.Lock()
	r.participants[p.ID] = p
	existing := make([]*Participant, 0, len(r.participants))
	for _, ep := range r.participants {
		if ep.ID != p.ID {
			existing = append(existing, ep)
		}
	}
	r.mu.Unlock()

	// Add forwarding output tracks to existing participants for this new peer.
	for _, ep := range existing {
		r.addOutputTracksTo(ep, p)
	}

	log.Printf("[sfu] room=%s participant=%s joined (total=%d)", r.ID, p.ID, len(r.participants)+1)
	return p, *pc.LocalDescription(), nil
}

// Leave removes a participant from the room and closes their PeerConnection.
// All output tracks associated with this participant are removed from other peers.
func (r *Room) Leave(participantID string) {
	r.mu.Lock()
	p, ok := r.participants[participantID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.participants, participantID)
	remaining := make([]*Participant, 0, len(r.participants))
	for _, ep := range r.participants {
		remaining = append(remaining, ep)
	}
	r.mu.Unlock()

	if p.pc != nil {
		p.pc.Close()
	}

	// Remove output tracks for this participant from all remaining peers.
	for _, ep := range remaining {
		ep.mu.Lock()
		delete(ep.outputVideo, participantID)
		ep.mu.Unlock()
	}

	// Clear dominant speaker if it was this participant.
	r.mu.Lock()
	if r.dominantSpeaker == participantID {
		r.dominantSpeaker = ""
	}
	r.mu.Unlock()

	log.Printf("[sfu] room=%s participant=%s left (remaining=%d)", r.ID, participantID, len(remaining))
}

// AddICECandidate trickles an ICE candidate into the named participant's
// PeerConnection.
func (r *Room) AddICECandidate(participantID string, candidate webrtc.ICECandidateInit) error {
	r.mu.RLock()
	p, ok := r.participants[participantID]
	r.mu.RUnlock()
	if !ok {
		return errParticipantNotFound
	}
	return p.pc.AddICECandidate(candidate)
}

// ParticipantCount returns the current number of participants in the room.
func (r *Room) ParticipantCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.participants)
}

// Close shuts down the room and all participant connections.
func (r *Room) Close() {
	r.once.Do(func() {
		close(r.done)
		r.mu.Lock()
		pids := make([]string, 0, len(r.participants))
		for id := range r.participants {
			pids = append(pids, id)
		}
		r.mu.Unlock()
		for _, id := range pids {
			r.Leave(id)
		}
		log.Printf("[sfu] room=%s closed", r.ID)
	})
}

// onTrack is called when the SFU receives a new media track from a participant.
// It reads RTP packets from the remote track and forwards them to eligible receivers
// according to the Last-N policy.
func (r *Room) onTrack(p *Participant, remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	rid := remote.RID()
	kind := remote.Kind().String()
	log.Printf("[sfu] room=%s participant=%s new track kind=%s rid=%q", r.ID, p.ID, kind, rid)

	p.mu.Lock()
	if kind == "audio" {
		p.audioTrack = remote
	} else if kind == "video" {
		p.videoTracks[rid] = remote
	}
	p.mu.Unlock()

	// Periodically send PLI (Picture Loss Indication) upstream so the sender
	// refreshes keyframes. This keeps late-joiners and layer-switches working.
	if kind == "video" {
		go r.sendPeriodicPLI(p, remote, receiver)
	}

	buf := make([]byte, 1500)
	for {
		n, _, readErr := remote.Read(buf)
		if readErr != nil {
			return
		}

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}

		if kind == "audio" {
			r.forwardAudio(p, pkt)
		} else {
			r.forwardVideo(p, pkt, rid)
		}
	}
}

// sendPeriodicPLI sends a RTCP Picture Loss Indication on the given receiver
// every 3 seconds so the sender keeps providing keyframes.
func (r *Room) sendPeriodicPLI(p *Participant, remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{
					MediaSSRC: uint32(remote.SSRC()),
				},
			}); err != nil {
				return
			}
		case <-r.done:
			return
		}
	}
}

// forwardAudio delivers an audio RTP packet from sender p to all other
// participants' output audio tracks.
func (r *Room) forwardAudio(sender *Participant, pkt *rtp.Packet) {
	raw, err := pkt.Marshal()
	if err != nil {
		return
	}

	r.mu.RLock()
	receivers := make([]*Participant, 0, len(r.participants))
	for _, ep := range r.participants {
		if ep.ID != sender.ID {
			receivers = append(receivers, ep)
		}
	}
	r.mu.RUnlock()

	for _, recv := range receivers {
		recv.mu.Lock()
		track := recv.outputAudio
		recv.mu.Unlock()
		if track != nil {
			if _, err := track.Write(raw); err != nil {
				log.Printf("[sfu] audio write to %s: %v", recv.ID, err)
			}
		}
	}
}

// preferredVideoLayer returns the RID of the simulcast layer to forward for a
// given source participant. The dominant speaker always gets the high layer;
// everyone else gets the low layer.
func (r *Room) preferredVideoLayer(sourceID string) string {
	r.mu.RLock()
	ds := r.dominantSpeaker
	r.mu.RUnlock()
	if sourceID == ds {
		return "h"
	}
	return "l"
}

// lastNSourceIDs returns the participant IDs that are eligible to have their
// video forwarded to a given receiver, ordered by priority (dominant speaker
// first, then others). The list is capped at r.LastN.
func (r *Room) lastNSourceIDs(receiverID string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ds := r.dominantSpeaker
	result := make([]string, 0, r.LastN)

	// Dominant speaker gets priority slot.
	if ds != "" && ds != receiverID {
		result = append(result, ds)
	}

	for id := range r.participants {
		if id == receiverID || id == ds {
			continue
		}
		result = append(result, id)
		if len(result) >= r.LastN {
			break
		}
	}
	return result
}

// forwardVideo delivers a video RTP packet from sender p to eligible receivers
// according to the Last-N policy and the preferred simulcast layer.
func (r *Room) forwardVideo(sender *Participant, pkt *rtp.Packet, rid string) {
	r.mu.RLock()
	receivers := make([]*Participant, 0, len(r.participants))
	for _, ep := range r.participants {
		if ep.ID != sender.ID {
			receivers = append(receivers, ep)
		}
	}
	r.mu.RUnlock()

	// Determine the preferred layer for this sender.
	preferred := r.preferredVideoLayer(sender.ID)

	// Only forward if this packet's RID matches the preferred layer
	// (or if the track has no RID, treat it as the single available layer).
	if rid != "" && rid != preferred {
		return
	}

	raw, err := pkt.Marshal()
	if err != nil {
		return
	}

	for _, recv := range receivers {
		// Last-N check: only forward if sender is within this receiver's Last-N.
		eligible := r.lastNSourceIDs(recv.ID)
		inLastN := false
		for _, id := range eligible {
			if id == sender.ID {
				inLastN = true
				break
			}
		}
		if !inLastN {
			continue
		}

		recv.mu.Lock()
		track := recv.outputVideo[sender.ID]
		recv.mu.Unlock()
		if track != nil {
			if _, err := track.Write(raw); err != nil {
				log.Printf("[sfu] video write to %s: %v", recv.ID, err)
			}
		}
	}
}

// addOutputTracksTo attaches forwarding output tracks from source participant
// into target participant's PeerConnection so target can receive source's media.
func (r *Room) addOutputTracksTo(target *Participant, source *Participant) {
	// Audio output track from source → target.
	audioOut, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"audio-"+source.ID,
		"sfu-"+source.ID,
	)
	if err != nil {
		log.Printf("[sfu] create audio output track for %s→%s: %v", source.ID, target.ID, err)
		return
	}

	if _, err := target.pc.AddTrack(audioOut); err != nil {
		log.Printf("[sfu] add audio track %s→%s: %v", source.ID, target.ID, err)
		return
	}

	target.mu.Lock()
	target.outputAudio = audioOut
	target.mu.Unlock()

	// Video output track from source → target.
	videoOut, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video-"+source.ID,
		"sfu-"+source.ID,
	)
	if err != nil {
		log.Printf("[sfu] create video output track for %s→%s: %v", source.ID, target.ID, err)
		return
	}

	if _, err := target.pc.AddTrack(videoOut); err != nil {
		log.Printf("[sfu] add video track %s→%s: %v", source.ID, target.ID, err)
		return
	}

	target.mu.Lock()
	target.outputVideo[source.ID] = videoOut
	target.mu.Unlock()

	log.Printf("[sfu] wired output tracks %s→%s", source.ID, target.ID)
}
