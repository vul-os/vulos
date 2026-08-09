package stream

// gaming_session.go — GAME-SESSION-01: the GPU cloud-gaming session on the box.
//
// A gaming session is an ordinary streaming Session launched with Gaming=true
// (zero-latency encoder profile, gaming bitrate tiers 6000–10000 kbps, no idle
// FPS throttle, no resolution step-down) inside the sandboxed cage, plus two
// gaming-specific policies enforced here:
//
//  1. ONE active gaming session per authorized user. Cloud gaming pins a scarce
//     GPU; a single user must not spin up N concurrent GPU sessions on the box.
//     GamingManager tracks the owner→session mapping and refuses a second
//     concurrent launch for the same owner (returns ErrGamingSessionActive).
//
//  2. Teardown frees the cage + GPU. Stopping a gaming session tears down the
//     underlying Session (which kills cage/Xvfb/app/gst and removes the
//     per-session runtime dir) AND drops the owner slot so the user can start a
//     fresh session. The CP releases the *rented* GPU on its side (streamsession
//     Teardown → gpusku Detach); on the box, freeing the cage releases the local
//     GPU/DRM handles the compositor held.
//
// Auth / per-session binding is inherited from the pool + transport layer:
//   - the session carries OwnerID (from X-User-ID at launch),
//   - the WebRTC signaling endpoint is owner-gated via Pool.GetForUser, so a
//     peer cannot attach to (and therefore cannot inject input into) a session
//     it does not own,
//   - the AUTH-13 gate can additionally require a WebAuthn assertion before any
//     injected input is accepted.
// GamingManager adds the one-per-user policy on top of that existing binding.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"vulos/backend/services/gpu"
)

// ErrGamingSessionActive is returned by GamingManager.Start when the owner
// already has an active gaming session. Cloud gaming is one-per-user by policy.
var ErrGamingSessionActive = fmt.Errorf("stream: user already has an active gaming session")

// ErrGamingOwnerRequired is returned when a gaming session is requested without
// an authenticated owner. Unlike system/CLI streaming sessions, a gaming
// session MUST be owned so the one-per-user policy and input binding apply.
var ErrGamingOwnerRequired = fmt.Errorf("stream: gaming session requires an authenticated user")

// GamingBitrateTier returns the initial gaming Quality tier the encoder pipeline
// should start at, given the detected GPU. With a hardware encoder present
// (NVENC/VA-API) we can sustain the top gaming tier; on software VP8 we start at
// the base gaming tier to keep encode latency in budget. The adaptive controller
// (gaming path) only adjusts bitrate, never resolution, so this is the ceiling.
func GamingBitrateTier(info gpu.Info) Quality {
	switch info.Tier {
	case gpu.TierNVENC, gpu.TierVAAPI:
		return QualityGamingMax // 10000 kbps — hardware encode can sustain it
	default:
		return QualityGaming // 6000 kbps — software VP8 base gaming tier
	}
}

// NVENCAvailable reports whether hardware NVENC encode is present. When true the
// gaming pipeline uses nvh264enc/nvav1enc (see gpu.GamingEncoderArgs); when false
// the session cleanly falls back to VA-API or software VP8. The gaming session is
// FUNCTIONAL in every tier — this only affects encode latency/quality.
func NVENCAvailable(info gpu.Info) bool { return info.Tier == gpu.TierNVENC }

// GamingCapability summarises what the box can offer for a gaming session. It is
// returned to the client so it can display the effective quality and know
// whether it is on a hardware-accelerated path. Nothing here requires GPU
// hardware to compute — it is a read of gpu.Detect().
type GamingCapability struct {
	Encoder        string `json:"encoder"`         // GStreamer encoder element
	Tier           string `json:"tier"`            // software | vaapi | nvenc
	Vendor         string `json:"vendor"`          // none | intel | amd | nvidia
	Device         string `json:"device"`          // GPU device name
	HardwareEncode bool   `json:"hardware_encode"` // NVENC or VA-API present
	NVENC          bool   `json:"nvenc"`           // NVIDIA NVENC specifically
	HasAV1         bool   `json:"has_av1"`         // AV1 hardware encode
	InitialQuality string `json:"initial_quality"` // gaming | gaming-max
	InitialKbps    int    `json:"initial_kbps"`    // bitrate for InitialQuality
	Codec          string `json:"webrtc_codec"`    // negotiated WebRTC codec
}

// DetectGamingCapability reads the detected GPU and reports the gaming
// capability profile. HardwareEncode=false means the box has no GPU encoder and
// will stream via software VP8 — still a working gaming session, just at the
// base tier and higher encode latency (flagged so the caller/UI can surface it).
func DetectGamingCapability(info gpu.Info) GamingCapability {
	q := GamingBitrateTier(info)
	return GamingCapability{
		Encoder:        info.Encoder,
		Tier:           info.TierName,
		Vendor:         string(info.Vendor),
		Device:         info.Device,
		HardwareEncode: info.Tier == gpu.TierNVENC || info.Tier == gpu.TierVAAPI,
		NVENC:          NVENCAvailable(info),
		HasAV1:         info.HasAV1,
		InitialQuality: q.String(),
		InitialKbps:    q.Bitrate(),
		Codec:          info.WebRTCCodec(),
	}
}

// GamingManager enforces the one-active-gaming-session-per-user policy on top of
// a Pool. It is safe for concurrent use.
type GamingManager struct {
	pool *Pool

	mu       sync.Mutex
	byOwner  map[string]string // ownerID → session ID
	bySessID map[string]string // session ID → ownerID (reverse, for Stop)
}

// NewGamingManager wraps a Pool with the gaming one-per-user policy.
func NewGamingManager(pool *Pool) *GamingManager {
	return &GamingManager{
		pool:     pool,
		byOwner:  make(map[string]string),
		bySessID: make(map[string]string),
	}
}

// GamingLaunchOpts configures a gaming session. It is a thin subset of
// LaunchOpts — the gaming profile (Gaming=true, gaming bitrate tier, no idle
// throttle) is applied here, not chosen by the caller.
type GamingLaunchOpts struct {
	// UserID is the authenticated owner. REQUIRED (ErrGamingOwnerRequired).
	UserID string
	// Name is a human-readable label (e.g. the game title).
	Name string
	// Command + Args are the game/app to launch inside the cage.
	Command string
	Args    []string
	// Env are additional environment variables (validated for library-injection).
	Env []string
	// Width/Height/FPS of the virtual display (defaults applied by Pool.Launch).
	Width, Height, FPS int
	// UserHome is the owner's home directory for the sandboxed app.
	UserHome string
}

// Start launches a new gaming session for opts.UserID, enforcing one active
// gaming session per user. It applies the gaming profile (Gaming=true, gaming
// bitrate tiers, priority scheduling) and returns the Session plus the detected
// gaming capability. Returns ErrGamingSessionActive if the user already has one,
// ErrGamingOwnerRequired if no owner is set.
func (m *GamingManager) Start(opts GamingLaunchOpts) (*Session, GamingCapability, error) {
	if opts.UserID == "" {
		return nil, GamingCapability{}, ErrGamingOwnerRequired
	}
	if opts.Command == "" {
		return nil, GamingCapability{}, fmt.Errorf("stream: gaming session requires a command")
	}
	if err := validateLaunchEnv(opts.Env); err != nil {
		return nil, GamingCapability{}, err
	}

	// One-per-user reservation. We reserve the owner slot with a placeholder
	// BEFORE launching so two concurrent Start calls for the same owner cannot
	// both pass the check and each launch a GPU session. The loser gets
	// ErrGamingSessionActive; the reservation is released on any launch failure.
	m.mu.Lock()
	if existing, ok := m.byOwner[opts.UserID]; ok {
		// If the existing session is still live in the pool, refuse. If it was
		// torn down out-of-band (app exited), reclaim the slot and continue.
		if m.pool.GetForUser(existing, opts.UserID) != nil {
			m.mu.Unlock()
			return nil, GamingCapability{}, ErrGamingSessionActive
		}
		delete(m.byOwner, opts.UserID)
		delete(m.bySessID, existing)
	}
	// Reserve with a sentinel so a racing duplicate observes an active owner.
	const reserving = "\x00reserving"
	m.byOwner[opts.UserID] = reserving
	m.mu.Unlock()

	releaseReservation := func() {
		m.mu.Lock()
		if m.byOwner[opts.UserID] == reserving {
			delete(m.byOwner, opts.UserID)
		}
		m.mu.Unlock()
	}

	sess, err := m.pool.Launch(LaunchOpts{
		Name:     opts.Name,
		Command:  opts.Command,
		Args:     opts.Args,
		Env:      opts.Env,
		Width:    opts.Width,
		Height:   opts.Height,
		FPS:      opts.FPS,
		UserHome: opts.UserHome,
		UserID:   opts.UserID,
		Gaming:   true, // GAME-SESSION-01: zero-latency profile + gaming bitrate tiers
	})
	if err != nil {
		releaseReservation()
		return nil, GamingCapability{}, err
	}

	// Seed the session's target bitrate to the gaming tier so the first encode
	// starts at the gaming ceiling rather than the medium default. The adaptive
	// controller (gaming path) only moves bitrate down under loss, never
	// resolution — so latency stays bounded.
	info := gpu.Detect()
	tier := GamingBitrateTier(info)
	sess.mu.Lock()
	sess.bitrate = tier.Bitrate()
	sess.Quality = tier.String()
	sess.mu.Unlock()

	// Commit the reservation to the real session id.
	m.mu.Lock()
	m.byOwner[opts.UserID] = sess.ID
	m.bySessID[sess.ID] = opts.UserID
	m.mu.Unlock()

	return sess, DetectGamingCapability(info), nil
}

// Stop tears down the gaming session id if it is owned by userID, freeing the
// cage/GPU and clearing the owner slot so the user may start a new session.
// Returns an error if the session is not found or not owned by the caller.
func (m *GamingManager) Stop(id, userID string) error {
	// Ownership check via the pool (foreign users get "not found").
	if m.pool.GetForUser(id, userID) == nil {
		return fmt.Errorf("stream: gaming session not found")
	}
	// Verify this manager considers the session owned by userID before dropping
	// the slot — defence against dropping another owner's mapping.
	m.mu.Lock()
	owner, tracked := m.bySessID[id]
	if tracked && owner == userID {
		delete(m.bySessID, id)
		delete(m.byOwner, owner)
	}
	m.mu.Unlock()

	return m.pool.Stop(id)
}

// ActiveFor returns the active gaming session ID for userID, or "" if none.
func (m *GamingManager) ActiveFor(userID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.byOwner[userID]
	if id == "\x00reserving" {
		return "" // a reservation in flight is not yet an active session
	}
	return id
}

// reap drops the owner mapping for a session that ended out-of-band (app exit /
// pool teardown). Called by the pool's exit path if wired; also self-heals in
// Start when a stale mapping is observed.
func (m *GamingManager) reap(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if owner, ok := m.bySessID[id]; ok {
		delete(m.bySessID, id)
		if m.byOwner[owner] == id {
			delete(m.byOwner, owner)
		}
	}
}

// RegisterGamingHandlers registers the gaming-session HTTP surface on mux.
//
//	GET  /api/stream/gaming/capability   — detected GPU / NVENC / initial tier
//	POST /api/stream/gaming/start        — launch a gaming session (one per user)
//	POST /api/stream/gaming/stop?id=...  — teardown (owner only; frees cage+GPU)
//	GET  /api/stream/gaming/active       — the caller's active gaming session id
//
// The WebRTC signaling itself reuses the existing owner-gated GET /api/stream/ws.
//
// isAdmin and execDisabled gate POST .../start exactly like every other
// exec-spawning stream route (streamPool.RegisterHandlers' launch/vnc
// endpoints, POST /api/stream/launch-app — see main.go's "launch + vnc
// endpoints require admin" comment): Start ultimately calls Pool.Launch,
// which runs `exec.CommandContext(ctx, opts.Command, opts.Args...)` directly
// on the host with no uid drop, no mount/network namespace, and the full
// server os.Environ() inherited (unlike the app-sandbox launcher, which never
// inherits the host environment). Before this gate existed, /start accepted
// Command/Args/Env straight from any authenticated session's JSON body — ANY
// logged-in profile on the box, not just the admin, could run an arbitrary
// command as the vulos server process, which on a bare-metal box runs
// unsandboxed (docs/SECURITY.md: "single-user appliance image... unsandboxed
// on that unit today"). That is a full box compromise from a non-admin
// profile, so isAdmin==nil fails CLOSED (403) rather than defaulting open —
// callers must wire a real check, there is no "not recommended for
// production" escape hatch here.
func (m *GamingManager) RegisterGamingHandlers(mux *http.ServeMux, isAdmin func(*http.Request) bool, execDisabled func() bool) {
	mux.HandleFunc("GET /api/stream/gaming/capability", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DetectGamingCapability(gpu.Detect()))
	})

	mux.HandleFunc("POST /api/stream/gaming/start", func(w http.ResponseWriter, r *http.Request) {
		if execDisabled != nil && execDisabled() {
			http.Error(w, `{"error":"exec disabled by administrator"}`, http.StatusServiceUnavailable)
			return
		}
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}
		// SEC (RCE, see doc comment above RegisterGamingHandlers): this handler
		// launches an arbitrary caller-supplied host Command/Args unsandboxed as
		// the server process. Admin-only, fail-closed on a nil gate.
		if isAdmin == nil || !isAdmin(r) {
			http.Error(w, `{"error":"admin only"}`, http.StatusForbidden)
			return
		}
		var req struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []string `json:"env"`
			Width   int      `json:"width"`
			Height  int      `json:"height"`
			FPS     int      `json:"fps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		userHome := ""
		if m.pool.resolveUserHome != nil {
			userHome = m.pool.resolveUserHome(r)
		}
		sess, cap, err := m.Start(GamingLaunchOpts{
			UserID: userID, Name: req.Name, Command: req.Command,
			Args: req.Args, Env: req.Env,
			Width: req.Width, Height: req.Height, FPS: req.FPS,
			UserHome: userHome,
		})
		if err != nil {
			code := http.StatusInternalServerError
			switch err {
			case ErrGamingSessionActive:
				code = http.StatusConflict
			case ErrGamingOwnerRequired:
				code = http.StatusUnauthorized
			}
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"session": sess, "capability": cap})
	})

	mux.HandleFunc("POST /api/stream/gaming/stop", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}
		id := r.URL.Query().Get("id")
		if err := m.Stop(id, userID); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"stopped"}`))
	})

	mux.HandleFunc("GET /api/stream/gaming/active", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"active_session_id": m.ActiveFor(userID)})
	})
}
