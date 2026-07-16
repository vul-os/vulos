// routes_ota.go — OTA HTTP routes for the vulos.cloud control-plane.
//
// Routes registered:
//
//	POST /api/ota/release              — ingest a signed release (admin-only)
//	POST /api/ota/release/{id}/halt    — halt a release (admin-only)
//	GET  /api/ota/feed                 — device-facing update feed (no auth)
//	POST /api/ota/policy               — set per-device policy (owner-only)
//	GET  /api/ota/policy               — get per-device policy (owner-only)
//	POST /api/ota/report               — device update-status report (no auth in v1)
//	POST /api/ota/rollback             — rollback a device/cohort (admin-only)
//	GET  /api/ota/releases             — paginated release list (admin-only)
//	GET  /api/ota/release/{id}/adoption — adoption counts for a release (admin-only)
//
// See backend/cp/OTA_API.md for the full HTTP surface + error→status map.
//
// Do NOT edit main.go — Wave B (cp-wire-integrate) owns that file and will
// call registerOTARoutes when wiring the full server.
package cproutes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/ota"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// registerOTARoutes wires OTA endpoints into mux.
//
// Parameters:
//
//	st             — OTA store (MemStore or SQLStore)
//	signer         — signing/verifying key (may be nil if OTA_SIGNING_KEY_BASE64
//	                 is unset; admin endpoints return 503 in that case)
//	authStore      — for session-based owner and admin checks
//	adminAccountID — value of ADMIN_ACCOUNT_ID env var (v1 single-admin gate)
//
// Optional variadic:
//
//	fleetSt (fleet.Store) — when supplied (non-nil), used to enrich adoption
//	  total_devices with fleet device counts. Pass nil or omit to fall back to
//	  report-log counts only.
//
//	NOTE for Wave B (cp-wire-integrate): update the call in main.go to pass the
//	live fleet.Store as the sixth argument so the adoption endpoint can show the
//	global device total in addition to the report-log count.
func registerOTARoutes(
	mux *http.ServeMux,
	st ota.Store,
	signer ota.Signer,
	authStore *auth.Store,
	adminAccountID string,
	optFleetSt ...interface{},
) {
	// Parse variadic args: first arg may be fleet.Store, second may be routing.Store.
	// Kept variadic for backwards-compatibility with existing call sites.
	var fleetSt fleet.Store
	var routingSt routing.Store
	for _, v := range optFleetSt {
		switch x := v.(type) {
		case fleet.Store:
			fleetSt = x
		case routing.Store:
			routingSt = x
		}
	}

	mux.HandleFunc("POST /api/ota/release", otaReleaseHandler(st, signer, authStore, adminAccountID))
	mux.HandleFunc("POST /api/ota/release/{id}/halt", otaHaltHandler(st, authStore, adminAccountID))
	mux.HandleFunc("GET /api/ota/feed", otaFeedHandler(st))
	mux.HandleFunc("POST /api/ota/policy", otaPolicySetHandler(st, authStore, routingSt))
	mux.HandleFunc("GET /api/ota/policy", otaPolicyGetHandler(st, authStore, routingSt))
	mux.HandleFunc("POST /api/ota/report", otaReportHandler(st))
	mux.HandleFunc("POST /api/ota/rollback", otaRollbackHandler(st, authStore, adminAccountID))
	mux.HandleFunc("GET /api/ota/releases", otaListReleasesHandler(st, authStore, adminAccountID))
	mux.HandleFunc("GET /api/ota/release/{id}/adoption", otaAdoptionHandler(st, fleetSt, authStore, adminAccountID))
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin gate helpers
// ─────────────────────────────────────────────────────────────────────────────

// requireAdmin checks that the caller is authenticated AND their account ID
// matches adminAccountID. Writes 401/403 and returns false on failure.
func requireAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store, adminAccountID string) bool {
	if signer503(w, adminAccountID == "") {
		return false
	}
	u := authStore.RequireSession(r.Context(), w, r)
	if u == nil {
		return false // RequireSession already wrote 401
	}
	if u.ID != adminAccountID {
		httpx.Err(w, http.StatusForbidden, "forbidden: not admin")
		return false
	}
	return true
}

// signer503 writes 503 and returns true when cond is true (used to signal that
// a required resource is unavailable).
func signer503(w http.ResponseWriter, cond bool) bool {
	if cond {
		httpx.Err(w, http.StatusServiceUnavailable, "OTA signing key not configured")
		return true
	}
	return false
}

// requireOwner returns the authenticated user; writes 401 on failure.
func requireOwner(w http.ResponseWriter, r *http.Request, authStore *auth.Store) *auth.User {
	return authStore.RequireSession(r.Context(), w, r)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/release
// ─────────────────────────────────────────────────────────────────────────────

type releaseRequest struct {
	Version      string `json:"version"`
	Channel      string `json:"channel"`
	ArtifactURL  string `json:"artifact_url"`
	Sha256       string `json:"sha256"`
	MinFrom      string `json:"min_from"`
	Security     bool   `json:"security"`
	RolloutPct   int    `json:"rollout_pct"`
	SignatureB64 string `json:"signature"`
	DeferMaxSec  int    `json:"defer_max_seconds"`
}

func otaReleaseHandler(st ota.Store, signer ota.Signer, authStore *auth.Store, adminID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if signer503(w, signer == nil) {
			return
		}
		if !requireAdmin(w, r, authStore, adminID) {
			return
		}

		var req releaseRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		if err := validateRelease(req); err != nil {
			httpx.Err(w, http.StatusBadRequest, err.Error())
			return
		}

		// Server VERIFIES the caller-supplied signature before accepting.
		manifestJSON, err := marshalManifestForSig(req)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "failed to marshal manifest for verification")
			return
		}
		if err := signer.Verify(manifestJSON, req.SignatureB64); err != nil {
			httpx.Err(w, http.StatusUnprocessableEntity, ota.ErrInvalidSignature.Error())
			return
		}

		deferMax := req.DeferMaxSec
		if deferMax == 0 {
			deferMax = 604800 // 7 days default
		}
		rolloutPct := req.RolloutPct
		if rolloutPct == 0 {
			rolloutPct = 100
		}

		rel := ota.Release{
			Version:      req.Version,
			Channel:      req.Channel,
			ArtifactURL:  req.ArtifactURL,
			Sha256:       req.Sha256,
			MinFrom:      req.MinFrom,
			Security:     req.Security,
			RolloutPct:   rolloutPct,
			SignatureB64: req.SignatureB64,
			DeferMaxSec:  deferMax,
			CreatedAt:    time.Now().UTC(),
		}

		id, err := st.InsertRelease(r.Context(), rel)
		if err != nil {
			httpx.Err(w, otaErrStatus(err), err.Error())
			return
		}

		auditRecord(r.Context(), adminID, "ota.release", req.Version,
			map[string]string{"channel": req.Channel})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]int64{"id": id})
	}
}

// marshalManifestForSig serialises the fields that are covered by the signature.
// The wire format mirrors the Manifest type (what the device receives).
func marshalManifestForSig(req releaseRequest) ([]byte, error) {
	m := ota.Manifest{
		Version:     req.Version,
		ArtifactURL: req.ArtifactURL,
		Sha256:      req.Sha256,
		Security:    req.Security,
		DeferMaxSec: req.DeferMaxSec,
	}
	return json.Marshal(m)
}

func validateRelease(req releaseRequest) error {
	if req.Version == "" {
		return ota.ErrInvalidVersion
	}
	if !ota.ValidChannel(req.Channel) {
		return ota.ErrInvalidChannel
	}
	if req.ArtifactURL == "" {
		return fmt.Errorf("ota: artifact_url is required")
	}
	if req.Sha256 == "" {
		return fmt.Errorf("ota: sha256 is required")
	}
	if req.MinFrom == "" {
		return fmt.Errorf("ota: min_from is required")
	}
	if req.SignatureB64 == "" {
		return fmt.Errorf("ota: signature is required")
	}
	if req.RolloutPct < 0 || req.RolloutPct > 100 {
		return fmt.Errorf("ota: rollout_pct must be 0-100")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/release/{id}/halt
// ─────────────────────────────────────────────────────────────────────────────

func otaHaltHandler(st ota.Store, authStore *auth.Store, adminID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r, authStore, adminID) {
			return
		}

		idStr := r.PathValue("id")
		releaseID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || releaseID <= 0 {
			httpx.Err(w, http.StatusBadRequest, "invalid release id")
			return
		}

		if err := st.HaltRelease(r.Context(), releaseID); err != nil {
			httpx.Err(w, otaErrStatus(err), err.Error())
			return
		}
		auditRecord(r.Context(), adminID, "ota.halt", idStr, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/feed?ulid=&channel=&version=
// ─────────────────────────────────────────────────────────────────────────────

func otaFeedHandler(st ota.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ulid := r.URL.Query().Get("ulid")
		channel := r.URL.Query().Get("channel")
		version := r.URL.Query().Get("version")

		if ulid == "" || version == "" {
			httpx.Err(w, http.StatusBadRequest, "ulid and version are required")
			return
		}

		canon, err := routing.CanonULID(ulid)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
			return
		}

		if channel != "" && !ota.ValidChannel(channel) {
			httpx.Err(w, http.StatusBadRequest, ota.ErrInvalidChannel.Error())
			return
		}

		rel, err := st.FeedFor(r.Context(), canon, channel, version)
		if errors.Is(err, ota.ErrNoUpgrade) {
			// 200 with empty object — no upgrade available.
			httpx.JSON(w, struct{}{})
			return
		}
		if errors.Is(err, ota.ErrUnknownULID) {
			httpx.Err(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}

		m := ota.Manifest{
			Version:      rel.Version,
			ArtifactURL:  rel.ArtifactURL,
			Sha256:       rel.Sha256,
			Security:     rel.Security,
			SignatureB64: rel.SignatureB64,
			DeferMaxSec:  rel.DeferMaxSec,
		}
		httpx.JSON(w, m)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/policy
// ─────────────────────────────────────────────────────────────────────────────

type policyRequest struct {
	ULID           string `json:"ulid"`
	Channel        string `json:"channel"`
	PinVersion     string `json:"pin_version"`
	DeferUntil     string `json:"defer_until"` // RFC3339 or ""
	OptOutFeatures bool   `json:"opt_out_features"`
}

func otaPolicySetHandler(st ota.Store, authStore *auth.Store, routingSt routing.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := requireOwner(w, r, authStore)
		if u == nil {
			return
		}

		var req policyRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		canon, err := routing.CanonULID(req.ULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
			return
		}

		if req.Channel != "" && !ota.ValidChannel(req.Channel) {
			httpx.Err(w, http.StatusBadRequest, ota.ErrInvalidChannel.Error())
			return
		}
		ch := req.Channel
		if ch == "" {
			ch = "stable"
		}

		// Ownership check: confirm the requesting session owns the ULID.
		if routingSt != nil {
			binding, berr := routingSt.GetBinding(r.Context(), canon)
			if berr != nil {
				httpx.Err(w, http.StatusNotFound, "ulid not found")
				return
			}
			if binding.AccountID != u.ID {
				httpx.Err(w, http.StatusForbidden, "forbidden: ulid not owned by authenticated account")
				return
			}
		}

		p := ota.DevicePolicy{
			ULID:           canon,
			Channel:        ch,
			PinVersion:     req.PinVersion,
			OptOutFeatures: req.OptOutFeatures,
		}
		if req.DeferUntil != "" {
			t, err := time.Parse(time.RFC3339, req.DeferUntil)
			if err != nil {
				httpx.Err(w, http.StatusBadRequest, "defer_until must be RFC3339")
				return
			}
			p.DeferUntil = &t
		}

		if err := st.SetPolicy(r.Context(), p); err != nil {
			httpx.Err(w, otaErrStatus(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/policy?ulid=
// ─────────────────────────────────────────────────────────────────────────────

type policyResponse struct {
	ULID           string `json:"ulid"`
	Channel        string `json:"channel"`
	PinVersion     string `json:"pin_version,omitempty"`
	DeferUntil     string `json:"defer_until,omitempty"`
	OptOutFeatures bool   `json:"opt_out_features"`
	UpdatedAt      string `json:"updated_at"`
}

func otaPolicyGetHandler(st ota.Store, authStore *auth.Store, routingSt routing.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := requireOwner(w, r, authStore)
		if u == nil {
			return
		}

		ulid := r.URL.Query().Get("ulid")
		if ulid == "" {
			httpx.Err(w, http.StatusBadRequest, "ulid is required")
			return
		}

		canon, err := routing.CanonULID(ulid)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
			return
		}

		// Ownership check: confirm the requesting session owns the ULID.
		if routingSt != nil {
			binding, berr := routingSt.GetBinding(r.Context(), canon)
			if berr != nil {
				httpx.Err(w, http.StatusNotFound, "ulid not found")
				return
			}
			if binding.AccountID != u.ID {
				httpx.Err(w, http.StatusForbidden, "forbidden: ulid not owned by authenticated account")
				return
			}
		}

		p, err := st.GetPolicy(r.Context(), canon)
		if errors.Is(err, ota.ErrUnknownULID) {
			httpx.Err(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}

		resp := policyResponse{
			ULID:           p.ULID,
			Channel:        p.Channel,
			PinVersion:     p.PinVersion,
			OptOutFeatures: p.OptOutFeatures,
			UpdatedAt:      p.UpdatedAt.Format(time.RFC3339),
		}
		if p.DeferUntil != nil {
			resp.DeferUntil = p.DeferUntil.Format(time.RFC3339)
		}
		httpx.JSON(w, resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/report
// ─────────────────────────────────────────────────────────────────────────────

type reportRequest struct {
	ULID    string `json:"ulid"`
	Version string `json:"version"`
	Result  string `json:"result"` // "applied" | "failed" | "rolled-back"
}

func otaReportHandler(st ota.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req reportRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		if req.ULID == "" || req.Version == "" || req.Result == "" {
			httpx.Err(w, http.StatusBadRequest, "ulid, version, and result are required")
			return
		}
		switch req.Result {
		case "applied", "failed", "rolled-back":
		default:
			httpx.Err(w, http.StatusBadRequest, "result must be 'applied', 'failed', or 'rolled-back'")
			return
		}

		canon, err := routing.CanonULID(req.ULID)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
			return
		}

		// SECURITY (audit H3): authenticate the reporting device. This is a
		// device→CP route, so verify X-Ota-Sig = HMAC-SHA256(DEVICE_SHARED_SECRET,
		// ulid) — the same constant-time scheme as the fleet heartbeat. Fail-closed:
		// if the shared secret is unset we cannot verify, so reject (unless an
		// operator explicitly opts out via OTA_REQUIRE_DEVICE_SIG=0 for dev).
		if os.Getenv("OTA_REQUIRE_DEVICE_SIG") != "0" {
			secret := secretOrEnv(r.Context(), "DEVICE_SHARED_SECRET")
			if secret == "" {
				httpx.Err(w, http.StatusServiceUnavailable, "ota report verification not configured")
				return
			}
			if !verifyFleetSig(canon, secret, r.Header.Get("X-Ota-Sig")) {
				httpx.Err(w, http.StatusUnauthorized, "invalid device signature")
				return
			}
		}

		if err := st.InsertReport(r.Context(), canon, req.Version, req.Result); err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/rollback
// ─────────────────────────────────────────────────────────────────────────────

type rollbackRequest struct {
	ULIDOrCohort  string `json:"ulid_or_cohort"`
	TargetVersion string `json:"target_version"`
}

func otaRollbackHandler(st ota.Store, authStore *auth.Store, adminID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r, authStore, adminID) {
			return
		}

		var req rollbackRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		if req.ULIDOrCohort == "" || req.TargetVersion == "" {
			httpx.Err(w, http.StatusBadRequest, "ulid_or_cohort and target_version are required")
			return
		}

		// Resolve to canonical ULID (single-device rollback).
		canon, err := routing.CanonULID(req.ULIDOrCohort)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, "ulid_or_cohort must be a valid ULID in v1")
			return
		}

		// Read existing policy (or create default).
		p, err := st.GetPolicy(r.Context(), canon)
		if errors.Is(err, ota.ErrUnknownULID) {
			p = ota.DevicePolicy{
				ULID:    canon,
				Channel: "stable",
			}
		} else if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Pin the device to the rollback target.
		p.PinVersion = req.TargetVersion

		if err := st.SetPolicy(r.Context(), p); err != nil {
			httpx.Err(w, otaErrStatus(err), err.Error())
			return
		}
		auditRecord(r.Context(), adminID, "ota.rollback", canon,
			map[string]string{"target_version": req.TargetVersion})
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/releases?limit=&offset=
// ─────────────────────────────────────────────────────────────────────────────

// releaseListItem is the JSON shape for each entry in the releases list.
type releaseListItem struct {
	ID          int64  `json:"id"`
	Version     string `json:"version"`
	Channel     string `json:"channel"`
	RolloutPct  int    `json:"rollout_pct"`
	Halted      bool   `json:"halted"`
	Security    bool   `json:"security"`
	CreatedAt   string `json:"created_at"`
	ArtifactURL string `json:"artifact_url"`
}

func otaListReleasesHandler(st ota.Store, authStore *auth.Store, adminID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r, authStore, adminID) {
			return
		}

		limit := 50
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				httpx.Err(w, http.StatusBadRequest, "limit must be a non-negative integer")
				return
			}
			limit = n
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				httpx.Err(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			offset = n
		}

		releases, err := st.ListReleases(r.Context(), limit, offset)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}

		items := make([]releaseListItem, 0, len(releases))
		for _, rel := range releases {
			items = append(items, releaseListItem{
				ID:          rel.ID,
				Version:     rel.Version,
				Channel:     rel.Channel,
				RolloutPct:  rel.RolloutPct,
				Halted:      rel.Halted,
				Security:    rel.Security,
				CreatedAt:   rel.CreatedAt.Format(time.RFC3339),
				ArtifactURL: rel.ArtifactURL,
			})
		}
		httpx.JSON(w, map[string]any{
			"releases": items,
			"limit":    limit,
			"offset":   offset,
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/release/{id}/adoption
// ─────────────────────────────────────────────────────────────────────────────

func otaAdoptionHandler(st ota.Store, fleetSt fleet.Store, authStore *auth.Store, adminID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r, authStore, adminID) {
			return
		}

		idStr := r.PathValue("id")
		releaseID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || releaseID <= 0 {
			httpx.Err(w, http.StatusBadRequest, "invalid release id")
			return
		}

		adoption, err := st.AdoptionCounts(r.Context(), releaseID)
		if errors.Is(err, ota.ErrReleaseNotFound) {
			httpx.Err(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, err.Error())
			return
		}

		// If a fleet store is available, enrich total_devices with the global
		// non-decommissioned device count. This is a best-effort enrichment:
		// the OTA report-log count (adoption.TotalDevices) is used as a fallback.
		// Note: fleet.Store.SeatCount requires an org ID; without org context in
		// this admin endpoint we use the report-log count only. Wave B wiring can
		// replace this with an org-scoped count once org context is threaded through.
		_ = fleetSt // reserved for Wave B org-scoped enrichment

		httpx.JSON(w, adoption)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error → HTTP status mapping
// ─────────────────────────────────────────────────────────────────────────────

func otaErrStatus(err error) int {
	switch {
	case errors.Is(err, ota.ErrInvalidVersion),
		errors.Is(err, ota.ErrInvalidChannel):
		return http.StatusBadRequest // 400
	case errors.Is(err, ota.ErrInvalidSignature):
		return http.StatusUnprocessableEntity // 422
	case errors.Is(err, ota.ErrUnknownULID),
		errors.Is(err, ota.ErrReleaseNotFound):
		return http.StatusNotFound // 404
	case errors.Is(err, ota.ErrNoUpgrade):
		return http.StatusOK // 200 + {}
	case errors.Is(err, ota.ErrDuplicateRelease):
		return http.StatusConflict // 409
	default:
		return http.StatusInternalServerError // 500
	}
}

// Ensure strings package is used (for future use in cohort/split logic).
var _ = strings.Contains
