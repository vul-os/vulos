// cloudhome.go — account-only document sharing, cloud side.
//
// Wires the cloud-home subsystem (pkg/cloudhome) and its HTTP surfaces:
//
//	GET  /api/verify/lookup?email=<email>          (Contract 2 — the directory)
//	     -> DiscoveryResult{ vula_id, server, display_name }
//	     Box-running users resolve to their box peer; account-only users resolve
//	     to a lazily-provisioned cloud-home VulaID hosted by this cell.
//
//	POST /api/files/peer/inbound                    (Contract 3 — share intake)
//	     A remote sharer delivers a per-document peershare capability bound to an
//	     account's cloud-home VulaID (the vulos files.CapabilityDelivery shape:
//	     {recipient, link, capability}); the cell redeems it on the account's
//	     behalf by signing the fetch proof with the cloud-home key and PULLing the
//	     bytes from the sharer's /api/files/peer/serve, then stages them into the
//	     account's Drive (SaveReceivedToDrive bridge).
//
// FAIL CLOSED: when the cloud-home Service cannot be constructed (e.g.
// CLOUDHOME_KEK unset in production), both routes return 503 and the cell does
// NOT custody peering keys under a default key.
package cproutes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cloudhome"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/ddos"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/keydir"
)

// cloudHomeAuditor adapts the process-wide control-plane audit log
// (auditlog via the nil-safe auditRecord wrapper) to
// cloudhome.KeyAuditor, so every custodied-key access (sign/redeem/rotate) is
// recorded. It reads the global audit log at emit-time, because cloud-home is
// wired before the audit log during startup; by the time a key is actually used
// the log is set. Best-effort: a nil/failed log never blocks signing.
type cloudHomeAuditor struct{}

func (cloudHomeAuditor) RecordKeyAccess(ctx context.Context, op, accountID, vulaID string, detail map[string]string) {
	meta := make(map[string]string, len(detail)+1)
	meta["account_id"] = accountID
	for k, v := range detail {
		meta[k] = v
	}
	// COORDINATOR: needs auditRecord (package main helper, cloud cmd/server wire_auditlog.go)
	auditRecord(ctx, "cloudhome", "cloudhome.key_"+op, vulaID, meta)
}

// Directory-enumeration hardening (DIR-ENUM-01). The lookup endpoint is public
// so the share-by-email UX works for cross-instance callers, but a naive public
// directory is an account-enumeration oracle. We bound it three ways:
//
//   - Per-IP rate limit for ANONYMOUS callers (hard) and a generous per-caller
//     limit for TRUSTED callers (a logged-in session or a relay/service caller
//     presenting X-Relay-Auth: <CP_SHARED_SECRET>).
//   - A UNIFORM negative response: "no account for this email" and "account
//     exists but is not directory-discoverable" return a byte-identical 404 body
//     so a prober cannot distinguish the two. Resolvable lookups still return the
//     unchanged DiscoveryResult shape.
//   - A timing floor on the anonymous path so the DB hit/miss latency does not
//     leak which emails exist.
const (
	dirAnonPerMin   = 20  // anonymous per-IP lookups per minute (hard)
	dirCallerPerMin = 120 // authenticated/relay per-caller lookups per minute
)

// dirNegativeFloor is the minimum response time for anonymous lookups (both the
// negative and the resolvable path) so the existence side channel is blunted.
const dirNegativeFloor = 120 * time.Millisecond

// uniformNotFoundBody is the single, content-identical body returned for every
// non-resolvable lookup (no account OR not-discoverable), so the two are
// indistinguishable by shape.
var uniformNotFoundBody = map[string]string{"error": "not discoverable"}

// RegisterCloudHome wires the directory + peering-intake endpoints.
// svc may be nil (degraded / misconfigured); both routes then return 503.
// authStore classifies callers for the directory rate-limit auth-gate; it may be
// nil (every caller is then treated as anonymous and hard-throttled).
func RegisterCloudHome(mux *http.ServeMux, svc *cloudhome.Service, authStore *auth.Store) {
	anonByIP := newMintRateLimiter(dirAnonPerMin, time.Minute)
	callerLim := newMintRateLimiter(dirCallerPerMin, time.Minute)

	// ── GET /api/verify/lookup?email= ── the directory (Contract 2). Public, but
	// rate-limited + enumeration-hardened (DIR-ENUM-01). It returns only a public
	// VulaID + server + display name for resolvable accounts; non-resolvable
	// lookups get a uniform 404.
	mux.HandleFunc("GET /api/verify/lookup", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if svc == nil {
			writeCloudHomeError(w, "directory unavailable", http.StatusServiceUnavailable)
			return
		}

		// Classify the caller and apply the appropriate rate limit. Trusted callers
		// (logged-in session or relay/service secret) get a generous per-caller
		// budget; anonymous callers are throttled hard per source IP.
		callerKey, trusted := cloudHomeCaller(r.Context(), r, authStore)
		now := time.Now()
		if trusted {
			if !callerLim.allow("caller:"+callerKey, now) {
				w.Header().Set("Retry-After", "60")
				writeCloudHomeError(w, "rate limited", http.StatusTooManyRequests)
				return
			}
		} else {
			if !anonByIP.allow("ip:"+cloudHomeClientIP(r), now) {
				w.Header().Set("Retry-After", "60")
				// Floor even the throttle response so a 429 vs 404 timing/branch
				// cannot be turned into an oracle.
				cloudHomeFloor(start, trusted)
				writeCloudHomeError(w, "rate limited", http.StatusTooManyRequests)
				return
			}
		}

		email := strings.TrimSpace(r.URL.Query().Get("email"))
		if email == "" {
			writeCloudHomeError(w, "email query parameter required", http.StatusBadRequest)
			return
		}
		res, err := svc.Resolve(r.Context(), email)
		if err != nil {
			switch {
			case errors.Is(err, cloudhome.ErrInvalidInput):
				writeCloudHomeError(w, "invalid email", http.StatusBadRequest)
			case errors.Is(err, cloudhome.ErrNoAccount), errors.Is(err, cloudhome.ErrNotDiscoverable):
				// UNIFORM negative: do not reveal whether the account is absent or
				// merely private. Identical body + (anonymous) timing floor.
				cloudHomeFloor(start, trusted)
				writeCloudHomeJSON(w, http.StatusNotFound, uniformNotFoundBody)
			default:
				log.Printf("[cloudhome] resolve: %v", err)
				writeCloudHomeError(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
		// Floor the resolvable path for anonymous callers too, so "found" and
		// "private" cannot be told apart by latency (only by the — intentionally
		// unchanged — success body shape).
		cloudHomeFloor(start, trusted)
		writeCloudHomeJSON(w, http.StatusOK, res)
	})

	// ── POST /api/files/peer/inbound ── share intake (Contract 3). Body is the
	// vulos files.CapabilityDelivery shape {recipient, link, capability}.
	// Recipient binding (the capability is bound to a cloud-home VulaID this cell
	// custodies) is the authorization; unknown recipients fail closed with 404.
	mux.HandleFunc("POST /api/files/peer/inbound", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCloudHomeError(w, "peering intake unavailable", http.StatusServiceUnavailable)
			return
		}
		var in cloudhome.InboundShare
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			writeCloudHomeError(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		rcpt, err := svc.RedeemCapability(r.Context(), in)
		if err != nil {
			switch {
			case errors.Is(err, cloudhome.ErrNotFound):
				// Not this account's home, or unknown recipient VulaID.
				writeCloudHomeError(w, "recipient not hosted here", http.StatusNotFound)
			case errors.Is(err, cloudhome.ErrInvalidInput):
				writeCloudHomeError(w, "invalid capability delivery", http.StatusBadRequest)
			default:
				log.Printf("[cloudhome] redeem: %v", err)
				writeCloudHomeError(w, "redemption failed", http.StatusBadGateway)
			}
			return
		}
		writeCloudHomeJSON(w, http.StatusOK, rcpt)
	})

	// CONTENT-BLIND: there is deliberately NO prekey-claim route here. The cell
	// does not custody or serve any content-decryption (X3DH/message) prekey for a
	// cloud-home account — content forward secrecy is negotiated client-side. The
	// cell only signs/vouches/revokes with the IDENTITY key (routes below).

	// ── GET /api/peering/lifecycle?identity_vula_id= ── lifecycle relay. Returns
	// the verbatim self-revocation cert (base58 inside) so a peer that pinned a
	// cloud-home VulaID can confirm a revocation and reject it. Unknown → 404.
	mux.HandleFunc("GET /api/peering/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCloudHomeError(w, "lifecycle unavailable", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("identity_vula_id"))
		if id == "" {
			writeCloudHomeError(w, "identity_vula_id query parameter required", http.StatusBadRequest)
			return
		}
		doc, err := svc.Lifecycle(r.Context(), id)
		if err != nil {
			if errors.Is(err, cloudhome.ErrNotFound) {
				writeCloudHomeError(w, "identity not hosted here", http.StatusNotFound)
				return
			}
			log.Printf("[cloudhome] lifecycle: %v", err)
			writeCloudHomeError(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeCloudHomeJSON(w, http.StatusOK, doc)
	})

	// ── POST /api/cloudhome/revoke ── account-driven revocation of one's OWN
	// cloud-home identity. Requires a logged-in session (the account); the cell
	// self-signs the RevocationCert with the custodied identity key and publishes
	// it via the lifecycle relay so peers reject the VulaID henceforth.
	mux.HandleFunc("POST /api/cloudhome/revoke", func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeCloudHomeError(w, "revocation unavailable", http.StatusServiceUnavailable)
			return
		}
		accountID := ""
		if authStore != nil {
			if tok := auth.SessionFromRequest(r); tok != "" {
				if u, err := authStore.LookupSession(r.Context(), tok); err == nil && u != nil {
					accountID = u.ID
				}
			}
		}
		if accountID == "" {
			writeCloudHomeError(w, "authentication required", http.StatusUnauthorized)
			return
		}
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		cert, err := svc.RevokeIdentity(r.Context(), accountID, strings.TrimSpace(body.Reason))
		if err != nil {
			if errors.Is(err, cloudhome.ErrNotFound) {
				writeCloudHomeError(w, "no cloud-home identity for this account", http.StatusNotFound)
				return
			}
			log.Printf("[cloudhome] revoke: %v", err)
			writeCloudHomeError(w, "revocation failed", http.StatusInternalServerError)
			return
		}
		writeCloudHomeJSON(w, http.StatusOK, cert)
	})
}

// cloudHomeCaller classifies a directory caller for the rate-limit auth-gate.
// It returns (key, trusted): trusted callers are throttled per-caller-key with a
// generous budget; anonymous callers are throttled hard per source IP.
//
// A caller is trusted when it presents EITHER:
//   - a valid login session (rate-limit key = account ID), OR
//   - a relay/service secret in X-Relay-Auth that matches the current or previous
//     CP_SHARED_SECRET (rate-limit key = "relay").
func cloudHomeCaller(ctx context.Context, r *http.Request, authStore *auth.Store) (key string, trusted bool) {
	// Relay/service-to-service caller (rotation-aware, constant-time compare).
	if presented := strings.TrimSpace(r.Header.Get("X-Relay-Auth")); presented != "" {
		// COORDINATOR: needs validSharedSecretEither (package main helper, cloud cmd/server wire_secrets.go)
		if validSharedSecretEither(ctx, presented) {
			return "relay", true
		}
	}
	// Logged-in caller.
	if authStore != nil {
		if tok := auth.SessionFromRequest(r); tok != "" {
			if u, err := authStore.LookupSession(ctx, tok); err == nil && u != nil && u.ID != "" {
				return u.ID, true
			}
		}
	}
	return "", false
}

// cloudHomeClientIP returns the source IP for per-IP rate limiting. It uses the
// canonical trusted-edge resolver rather than a raw leftmost X-Forwarded-For:
// this keys the directory-enumeration rate limiter, so trusting an
// attacker-settable header would let a caller defeat it by rotating XFF per
// request. ddos.RealClientIP honours a forwarding header only from a trusted-CIDR
// upstream, else RemoteAddr.
func cloudHomeClientIP(r *http.Request) string {
	return ddos.RealClientIP(r)
}

// cloudHomeFloor sleeps until at least dirNegativeFloor has elapsed since start.
// Only applied for anonymous callers (trusted == false): it blunts the existence
// timing side channel without slowing the logged-in share UX.
func cloudHomeFloor(start time.Time, trusted bool) {
	if trusted {
		return
	}
	if d := dirNegativeFloor - time.Since(start); d > 0 {
		time.Sleep(d)
	}
}

func writeCloudHomeError(w http.ResponseWriter, msg string, code int) {
	writeCloudHomeJSON(w, code, map[string]string{"error": msg})
}

func writeCloudHomeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ─────────────────────────────────────────────────────────────────────────────
// Seam adapters wiring cloudhome to auth (accounts) and keydir (box users).
// ─────────────────────────────────────────────────────────────────────────────

// authAccountLookup adapts *auth.Store to cloudhome.AccountLookup.
type authAccountLookup struct{ st *auth.Store }

func (a authAccountLookup) LookupByEmail(ctx context.Context, email string) (string, string, error) {
	id, display, err := a.st.LookupByEmail(ctx, email)
	if err != nil {
		return "", "", err
	}
	if id == "" {
		return "", "", cloudhome.ErrNoAccount
	}
	return id, display, nil
}

// keydirBoxLookup adapts a keydir.Store to cloudhome.BoxLookup: an account with
// a discoverable, active key-directory entry runs (or publishes) its own box, so
// it resolves as a box peer rather than via its cloud-home identity.
type keydirBoxLookup struct{ st keydir.Store }

func (k keydirBoxLookup) BoxIdentity(ctx context.Context, accountID string) (string, string, bool, error) {
	if k.st == nil {
		return "", "", false, nil
	}
	e, err := k.st.GetByAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, keydir.ErrNotFound) {
			return "", "", false, nil // no box key published → account-only
		}
		return "", "", false, err
	}
	if !e.Discoverable || e.State != "active" || e.PublicKeyB64 == "" {
		return "", "", false, nil
	}
	// keydir stores the box public key as base64url; the box VulaID is that key
	// rendered in the standard VulaID format so peers treat it identically to a
	// cloud-home VulaID.
	pub, derr := base64.RawURLEncoding.DecodeString(strings.TrimRight(e.PublicKeyB64, "="))
	if derr != nil || len(pub) != 32 {
		// Fall back: treat as account-only rather than emit a malformed VulaID.
		return "", "", false, nil
	}
	return cloudhome.FormatVulaID(pub), e.PeerEndpoint, true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Wiring
// ─────────────────────────────────────────────────────────────────────────────

// CloudHomeResult bundles the cloud-home Service (nil when degraded) and a
// closer for its store.
type CloudHomeResult struct {
	Svc    *cloudhome.Service
	Closer func()
}

// WireCloudHome opens the cloudhome store (cpdb: SQLite self-host / Postgres
// cloud), loads the KEK, and constructs the Service. It FAILS CLOSED: if the KEK
// is unset in production (CLOUDHOME_KEK), or the store/service cannot be built,
// it logs and returns a nil Service so the routes serve 503 rather than custody
// peering keys under a default key. The server still starts.
func WireCloudHome(dbDir string, authStore *auth.Store, keydirStore keydir.Store) CloudHomeResult {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[cloudhome] WARNING: could not set VULOS_DB_DIR: %v", err)
	}

	keyring, kekVersion, err := cloudhome.LoadKeyring()
	if err != nil {
		log.Printf("[cloudhome] DISABLED (fail-closed): %v", err)
		return CloudHomeResult{Closer: func() {}}
	}
	priorKEKs := make(map[int][]byte, len(keyring))
	for v, k := range keyring {
		if v != kekVersion {
			priorKEKs[v] = k
		}
	}

	cdb, err := cpdb.Open("cloudhome")
	if err != nil {
		log.Printf("[cloudhome] DISABLED: open store: %v", err)
		return CloudHomeResult{Closer: func() {}}
	}
	store, err := cloudhome.OpenSQLStore(cdb)
	if err != nil {
		_ = cdb.Close()
		log.Printf("[cloudhome] DISABLED: migrate store: %v", err)
		return CloudHomeResult{Closer: func() {}}
	}

	// Cell server base URL serving this account's peering intake. Overridable via
	// CLOUDHOME_SERVER_URL; defaults to the deployment site origin.
	server := strings.TrimSpace(os.Getenv("CLOUDHOME_SERVER_URL"))
	if server == "" {
		server = env.SiteURL()
	}

	// DriveStager: the cell-side end of the vulos SaveReceivedToDrive bridge. When
	// CLOUDHOME_DRIVE_STAGER_URL is set (the cell's vulos Files
	// /api/files/peer/receive endpoint), redeemed documents are POSTed there;
	// otherwise the stager is nil and RedeemCapability fails closed (no silent
	// drops).
	var stager cloudhome.DriveStager
	if u := strings.TrimSpace(os.Getenv("CLOUDHOME_DRIVE_STAGER_URL")); u != "" {
		stager = &httpDriveStager{url: u, client: &http.Client{Timeout: 30 * time.Second}}
		log.Printf("[cloudhome] Drive stager → %s", u)
	} else {
		log.Printf("[cloudhome] WARNING: CLOUDHOME_DRIVE_STAGER_URL unset — capability redemption will fail closed until a Drive stager is configured")
	}

	// Puller: the cell-side PeerServeClient that PULLs bytes from the sharer's
	// /api/files/peer/serve (vulos peershare PULL model). It is always available
	// (plain HTTP); redemption still fails closed if the stager is unset.
	puller := &httpPeerServeClient{client: peerServeSSRFClient(30 * time.Minute)}

	// ContentKeyLookup (optional): resolves a recipient account's PUBLISHED X25519
	// content key so RedeemCapability can additionally verify a pulled seal is
	// addressed to the recipient. The unconditional content-blind gate (bytes MUST
	// be a VSEAL1 sealed envelope) holds regardless of whether this is wired.
	// CLOUDHOME_CONTENT_KEY_URL points at the box's internal endpoint returning
	// {"content_pub_key":"..."} for ?account=<id>, authenticated with the shared
	// secret CLOUDHOME_CONTENT_KEY_SECRET (defaults to CP_SHARED_SECRET, the standard
	// cell↔box secret) in the X-Vulos-Internal-Auth header. When wired, redemption
	// FAILS CLOSED on any lookup error/outage (see cloudhome.ContentKeyLookup).
	var contentKeys cloudhome.ContentKeyLookup
	if u := strings.TrimSpace(os.Getenv("CLOUDHOME_CONTENT_KEY_URL")); u != "" {
		secret := strings.TrimSpace(os.Getenv("CLOUDHOME_CONTENT_KEY_SECRET"))
		if secret == "" {
			secret = strings.TrimSpace(os.Getenv("CP_SHARED_SECRET"))
		}
		contentKeys = &httpContentKeyLookup{url: u, secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
		log.Printf("[cloudhome] content-key lookup → %s (recipient-targeting enforced, fail-closed)", u)
	}

	svc, err := cloudhome.NewService(cloudhome.Config{
		Store:       store,
		KEK:         keyring[kekVersion],
		KEKVersion:  kekVersion,
		PriorKEKs:   priorKEKs,
		Server:      server,
		Account:     authAccountLookup{st: authStore},
		Box:         keydirBoxLookup{st: keydirStore},
		Stager:      stager,
		Puller:      puller,
		ContentKeys: contentKeys,
		Auditor:     cloudHomeAuditor{},
	})
	if err != nil {
		_ = store.Close()
		log.Printf("[cloudhome] DISABLED: new service: %v", err)
		return CloudHomeResult{Closer: func() {}}
	}
	log.Printf("[cloudhome] SQL store opened (backend=%s), server=%s", cdb.Backend(), server)
	return CloudHomeResult{Svc: svc, Closer: func() {
		if cerr := store.Close(); cerr != nil {
			log.Printf("[cloudhome] store close: %v", cerr)
		}
	}}
}

// httpDriveStager forwards already-pulled document bytes to the cell's vulos
// Files receive endpoint (POST /api/files/peer/receive). The recipient account is
// carried in the X-User-ID header (vulos's session-identity header for
// /api/files routes) so vulos writes the node into that account's Drive; the JSON
// body is exactly the fields the receiver decodes (parent_id, name, content_type,
// is_dir, bytes). ReceivedDoc marshals those plus a few omitempty context fields
// vulos ignores.
type httpDriveStager struct {
	url    string
	client *http.Client
}

func (h *httpDriveStager) SaveReceivedToDrive(ctx context.Context, accountID string, doc cloudhome.ReceivedDoc) error {
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", accountID)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("cloudhome: drive stager returned %d", resp.StatusCode)
	}
	return nil
}

// contentKeyAuthHeader carries the shared secret on the internal content-key
// lookup. MUST match vulos peering.ContentKeyAuthHeader (cross-repo wire contract).
const contentKeyAuthHeader = "X-Vulos-Internal-Auth"

// httpContentKeyLookup resolves an account's published X25519 content public key
// from the box's internal endpoint (WAVE-7):
//
//	GET <url>?account=<id>
//	Header: X-Vulos-Internal-Auth: <secret>
//	200 -> {"content_pub_key":"<b64 or empty>"}
//
// FAIL CLOSED: it returns a non-nil error on ANY inability to complete the lookup
// (build/transport error, non-200, or undecodable body) so RedeemCapability refuses
// the stage rather than falling open to accept an untargeted seal. A completed 200
// returns (key, nil) — key may be "" if the account has published none, which
// RedeemCapability also treats as fail-closed.
type httpContentKeyLookup struct {
	url    string
	secret string
	client *http.Client
}

func (h *httpContentKeyLookup) ContentPubKey(ctx context.Context, accountID string) (string, error) {
	sep := "?"
	if strings.Contains(h.url, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url+sep+"account="+url.QueryEscape(accountID), nil)
	if err != nil {
		return "", fmt.Errorf("content-key lookup: build request: %w", err)
	}
	if h.secret != "" {
		req.Header.Set(contentKeyAuthHeader, h.secret)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("content-key lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("content-key lookup: status %d", resp.StatusCode)
	}
	var body struct {
		ContentPubKey string `json:"content_pub_key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("content-key lookup: decode: %w", err)
	}
	return strings.TrimSpace(body.ContentPubKey), nil
}

// httpPeerServeClient is the production cloudhome.PeerServeClient. It POSTs a
// signed PeerFetchRequest to <ownerAddr>/api/files/peer/serve (vulos peershare
// serve path) and returns the streaming response body — the document bytes the
// sharer serves for a verified capability + fetch proof.
type httpPeerServeClient struct {
	client *http.Client
}

// peerServePath mirrors vulos files.PeerServePath.
const peerServePath = "/api/files/peer/serve"

// cloudhomeCGNAT is the CGNAT shared-address range (RFC 6598, 100.64.0.0/10).
var cloudhomeCGNAT = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// cloudhomeBlockedIP reports whether ip is in a range the CP must never dial on
// behalf of a peer-share redemption (loopback, RFC1918, link-local incl. the
// 169.254.169.254 metadata endpoint, CGNAT, IPv6 ULA, multicast, unspecified).
// Mirrors the deny-list in the webhooks/integrations SSRF guards.
func cloudhomeBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if cloudhomeCGNAT != nil && cloudhomeCGNAT.Contains(ip) {
		return true
	}
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil && v6[0]&0xfe == 0xfc {
		return true
	}
	return false
}

// cloudhomeValidateExternalHost rejects a host that is empty or whose (any)
// resolved IP is internal/reserved. IP literals are checked directly. An
// unresolvable host is allowed through (the dial simply fails — no SSRF value),
// matching the integrations guard; the dial-time re-validation in
// peerServeSSRFClient is the authoritative DNS-rebind defence.
func cloudhomeValidateExternalHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("cloudhome: empty owner host")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		if cloudhomeBlockedIP(ip) {
			return fmt.Errorf("cloudhome: owner_addr host is not allowed (internal/reserved address)")
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	for _, ip := range ips {
		if cloudhomeBlockedIP(ip) {
			return fmt.Errorf("cloudhome: owner_addr host is not allowed (resolves to an internal/reserved address)")
		}
	}
	return nil
}

// peerServeSSRFClient returns an *http.Client whose DialContext resolves the
// target host once, rejects any resolved IP in the deny-list, and pins the dial
// to the first approved IP — so a DNS rebind OR a 3xx redirect to an internal
// host is blocked at dial time, not just at parse time.
func peerServeSSRFClient(timeout time.Duration) *http.Client {
	base := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("cloudhome ssrf: bad address %q: %w", addr, err)
			}
			if ip := net.ParseIP(host); ip != nil {
				if cloudhomeBlockedIP(ip) {
					return nil, fmt.Errorf("cloudhome ssrf: blocked address %s", ip)
				}
				return base.DialContext(ctx, network, addr)
			}
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("cloudhome ssrf: resolve %q: %w", host, err)
			}
			if len(addrs) == 0 {
				return nil, fmt.Errorf("cloudhome ssrf: no addresses for %q", host)
			}
			for _, ia := range addrs {
				if cloudhomeBlockedIP(ia.IP) {
					return nil, fmt.Errorf("cloudhome ssrf: %s resolved to blocked %s", host, ia.IP)
				}
			}
			return base.DialContext(ctx, network, net.JoinHostPort(addrs[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func (h *httpPeerServeClient) Serve(ctx context.Context, ownerAddr string, req cloudhome.PeerFetchRequest) (io.ReadCloser, error) {
	base := strings.TrimSpace(ownerAddr)
	// SSRF (CLOUDHOME-SSRF): ownerAddr is fully attacker-controlled — it is the
	// unsigned `owner_addr` field of a capability POSTed to the effectively-
	// unauthenticated POST /api/files/peer/inbound. Without a guard the CP would
	// dial any address the attacker names (http://169.254.169.254/…, RFC1918,
	// loopback, …) → blind SSRF from the internet-facing control plane. Enforce
	// https-only and reject internal/reserved hosts at parse time; the client's
	// transport (peerServeSSRFClient) additionally IP-pins and re-validates at DIAL
	// time so a DNS-rebind or a 3xx redirect to an internal host is also blocked.
	if base == "" {
		return nil, fmt.Errorf("cloudhome: empty owner_addr")
	}
	// A bare host/host:port (no scheme) is treated as https.
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	pu, perr := url.Parse(base)
	if perr != nil || pu.Host == "" {
		return nil, fmt.Errorf("cloudhome: invalid owner_addr")
	}
	if pu.Scheme != "https" {
		return nil, fmt.Errorf("cloudhome: owner_addr must be https")
	}
	if err := cloudhomeValidateExternalHost(pu.Hostname()); err != nil {
		return nil, err
	}
	base = strings.TrimRight(pu.Scheme+"://"+pu.Host, "/")
	endpoint := base + peerServePath

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("cloudhome: peer serve %s returned %d: %s", endpoint, resp.StatusCode, string(raw))
	}
	return resp.Body, nil
}
