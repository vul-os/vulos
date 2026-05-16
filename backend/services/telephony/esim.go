// eSIM profile management via the lpac CLI.
//
// lpac (Local Profile Assistant Client) is an open-source tool for managing
// eSIM profiles: https://github.com/estkme-group/lpac
//
// This file wraps the lpac CLI so that no cgo or proprietary SDK is required.
// When lpac is absent every handler returns a clear 503 error — the service
// remains fully functional for SMS/voice, only the eSIM endpoints are
// unavailable.
//
// # Endpoints (mounted by RegisterESIMHandlers)
//
//	GET  /api/telephony/esim/profiles          list installed profiles
//	POST /api/telephony/esim/profiles/enable   enable a profile by iccid
//	POST /api/telephony/esim/profiles/disable  disable a profile by iccid
//	POST /api/telephony/esim/profiles/delete   delete a profile by iccid
//	POST /api/telephony/esim/profiles/add      add profile via activation code
//
// # Wire format
//
// All request/response bodies are JSON.  Errors return
//
//	{"error": "<message>"}
//
// with an appropriate HTTP status code.
package telephony

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
)

// ----------------------------------------------------------------------------
// ESIMProfile — data model
// ----------------------------------------------------------------------------

// ESIMProfile represents one eSIM profile as reported by lpac.
type ESIMProfile struct {
	// ICCID is the Integrated Circuit Card Identifier — the unique profile ID.
	ICCID string `json:"iccid"`

	// Name is the carrier-provided profile name (may be empty).
	Name string `json:"name,omitempty"`

	// Nickname is a user-settable short label (may be empty).
	Nickname string `json:"nickname,omitempty"`

	// State is "enabled" or "disabled".
	State string `json:"state"`

	// ProviderName is the carrier/provider name.
	ProviderName string `json:"provider_name,omitempty"`
}

// ----------------------------------------------------------------------------
// ESIMLpacClient — mockable interface
// ----------------------------------------------------------------------------

// ESIMLpacClient abstracts lpac CLI operations so tests can inject a fake
// without requiring lpac to be installed on the build/test host.
type ESIMLpacClient interface {
	// ESIMAvailable reports whether lpac is reachable on this host.
	ESIMAvailable() bool

	// ESIMListProfiles returns all eSIM profiles known to the eUICC.
	ESIMListProfiles() ([]ESIMProfile, error)

	// ESIMEnableProfile activates the profile identified by iccid.
	ESIMEnableProfile(iccid string) error

	// ESIMDisableProfile deactivates the profile identified by iccid.
	ESIMDisableProfile(iccid string) error

	// ESIMDeleteProfile removes the profile identified by iccid from the eUICC.
	ESIMDeleteProfile(iccid string) error

	// ESIMAddProfile downloads a new profile using an activation code.
	// activationCode follows the LPA:1$ format (SM-DP+ address + matching ID).
	ESIMAddProfile(activationCode string) error
}

// ----------------------------------------------------------------------------
// esimLpacCLI — production implementation backed by the lpac binary
// ----------------------------------------------------------------------------

// esimLpacCLI is the real ESIMLpacClient that shells out to lpac.
type esimLpacCLI struct {
	// avail is true when lpac was found in PATH during construction.
	avail bool
}

// esimNewLpacCLI probes whether lpac is installed and returns a client.
// The returned client is always non-nil; callers check ESIMAvailable().
func esimNewLpacCLI() *esimLpacCLI {
	c := &esimLpacCLI{}
	if _, err := exec.LookPath("lpac"); err != nil {
		log.Printf("[esim] lpac not found in PATH — eSIM management unavailable")
		return c
	}
	c.avail = true
	log.Printf("[esim] lpac found, eSIM management available")
	return c
}

func (c *esimLpacCLI) ESIMAvailable() bool { return c.avail }

// ESIMListProfiles runs `lpac profile list` and parses the JSON output.
// lpac outputs one JSON object per line; each object has at minimum the
// fields we map to ESIMProfile.
func (c *esimLpacCLI) ESIMListProfiles() ([]ESIMProfile, error) {
	if !c.avail {
		return nil, esimErrLpacAbsent()
	}
	out, err := esimRunLpac("profile", "list")
	if err != nil {
		return nil, fmt.Errorf("lpac profile list: %w", err)
	}
	return esimParseProfileList(out), nil
}

// ESIMEnableProfile runs `lpac profile enable <iccid>`.
func (c *esimLpacCLI) ESIMEnableProfile(iccid string) error {
	if !c.avail {
		return esimErrLpacAbsent()
	}
	if iccid == "" {
		return errors.New("iccid is required")
	}
	_, err := esimRunLpac("profile", "enable", iccid)
	if err != nil {
		return fmt.Errorf("lpac profile enable %s: %w", iccid, err)
	}
	return nil
}

// ESIMDisableProfile runs `lpac profile disable <iccid>`.
func (c *esimLpacCLI) ESIMDisableProfile(iccid string) error {
	if !c.avail {
		return esimErrLpacAbsent()
	}
	if iccid == "" {
		return errors.New("iccid is required")
	}
	_, err := esimRunLpac("profile", "disable", iccid)
	if err != nil {
		return fmt.Errorf("lpac profile disable %s: %w", iccid, err)
	}
	return nil
}

// ESIMDeleteProfile runs `lpac profile delete <iccid>`.
func (c *esimLpacCLI) ESIMDeleteProfile(iccid string) error {
	if !c.avail {
		return esimErrLpacAbsent()
	}
	if iccid == "" {
		return errors.New("iccid is required")
	}
	_, err := esimRunLpac("profile", "delete", iccid)
	if err != nil {
		return fmt.Errorf("lpac profile delete %s: %w", iccid, err)
	}
	return nil
}

// ESIMAddProfile runs `lpac profile download -a <activationCode>`.
func (c *esimLpacCLI) ESIMAddProfile(activationCode string) error {
	if !c.avail {
		return esimErrLpacAbsent()
	}
	if activationCode == "" {
		return errors.New("activation_code is required")
	}
	_, err := esimRunLpac("profile", "download", "-a", activationCode)
	if err != nil {
		return fmt.Errorf("lpac profile download: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------------------
// ESIMController — HTTP handler wiring
// ----------------------------------------------------------------------------

// ESIMController wires the eSIM HTTP handlers to a single lpac client.
type ESIMController struct {
	lpac ESIMLpacClient
}

// esimNewController creates an ESIMController backed by the real lpac CLI.
func esimNewController() *ESIMController {
	return &ESIMController{lpac: esimNewLpacCLI()}
}

// esimNewControllerWith creates an ESIMController with the supplied client
// (used by tests to inject a mock).
func esimNewControllerWith(lpac ESIMLpacClient) *ESIMController {
	return &ESIMController{lpac: lpac}
}

// RegisterESIMHandlers mounts all eSIM endpoints onto mux.  This is the only
// exported symbol in this file; main.go or the orchestrator calls it.
func RegisterESIMHandlers(mux *http.ServeMux, ec *ESIMController) {
	mux.HandleFunc("/api/telephony/esim/profiles", ec.handleListProfiles)
	mux.HandleFunc("/api/telephony/esim/profiles/enable", ec.handleEnableProfile)
	mux.HandleFunc("/api/telephony/esim/profiles/disable", ec.handleDisableProfile)
	mux.HandleFunc("/api/telephony/esim/profiles/delete", ec.handleDeleteProfile)
	mux.HandleFunc("/api/telephony/esim/profiles/add", ec.handleAddProfile)
	log.Printf("[esim] registered /api/telephony/esim/profiles (and /enable /disable /delete /add)")
}

// NewESIMController creates an ESIMController backed by the real lpac CLI.
// Exported so orchestrators (main.go, server.go) can construct one without
// importing the unexported constructor.
func NewESIMController() *ESIMController {
	return esimNewController()
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

// handleListProfiles serves GET /api/telephony/esim/profiles.
func (ec *ESIMController) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		esimJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ec.lpac.ESIMAvailable() {
		esimJSONError(w, "lpac not installed — eSIM management unavailable", http.StatusServiceUnavailable)
		return
	}
	profiles, err := ec.lpac.ESIMListProfiles()
	if err != nil {
		log.Printf("[esim] list profiles: %v", err)
		esimJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return empty array rather than null when no profiles are installed.
	if profiles == nil {
		profiles = []ESIMProfile{}
	}
	esimJSONOK(w, profiles)
}

// handleEnableProfile serves POST /api/telephony/esim/profiles/enable.
// Body: {"iccid": "<iccid>"}
func (ec *ESIMController) handleEnableProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		esimJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ec.lpac.ESIMAvailable() {
		esimJSONError(w, "lpac not installed — eSIM management unavailable", http.StatusServiceUnavailable)
		return
	}
	var req esimICCIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		esimJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ICCID == "" {
		esimJSONError(w, "iccid is required", http.StatusBadRequest)
		return
	}
	if err := ec.lpac.ESIMEnableProfile(req.ICCID); err != nil {
		log.Printf("[esim] enable %s: %v", req.ICCID, err)
		esimJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	esimJSONOK(w, esimOKResponse{OK: true, ICCID: req.ICCID})
}

// handleDisableProfile serves POST /api/telephony/esim/profiles/disable.
// Body: {"iccid": "<iccid>"}
func (ec *ESIMController) handleDisableProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		esimJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ec.lpac.ESIMAvailable() {
		esimJSONError(w, "lpac not installed — eSIM management unavailable", http.StatusServiceUnavailable)
		return
	}
	var req esimICCIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		esimJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ICCID == "" {
		esimJSONError(w, "iccid is required", http.StatusBadRequest)
		return
	}
	if err := ec.lpac.ESIMDisableProfile(req.ICCID); err != nil {
		log.Printf("[esim] disable %s: %v", req.ICCID, err)
		esimJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	esimJSONOK(w, esimOKResponse{OK: true, ICCID: req.ICCID})
}

// handleDeleteProfile serves POST /api/telephony/esim/profiles/delete.
// Body: {"iccid": "<iccid>"}
func (ec *ESIMController) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		esimJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ec.lpac.ESIMAvailable() {
		esimJSONError(w, "lpac not installed — eSIM management unavailable", http.StatusServiceUnavailable)
		return
	}
	var req esimICCIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		esimJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ICCID == "" {
		esimJSONError(w, "iccid is required", http.StatusBadRequest)
		return
	}
	if err := ec.lpac.ESIMDeleteProfile(req.ICCID); err != nil {
		log.Printf("[esim] delete %s: %v", req.ICCID, err)
		esimJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	esimJSONOK(w, esimOKResponse{OK: true, ICCID: req.ICCID})
}

// handleAddProfile serves POST /api/telephony/esim/profiles/add.
// Body: {"activation_code": "LPA:1$<smdp>$<matching-id>"}
func (ec *ESIMController) handleAddProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		esimJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !ec.lpac.ESIMAvailable() {
		esimJSONError(w, "lpac not installed — eSIM management unavailable", http.StatusServiceUnavailable)
		return
	}
	var req esimAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		esimJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ActivationCode == "" {
		esimJSONError(w, "activation_code is required", http.StatusBadRequest)
		return
	}
	if err := ec.lpac.ESIMAddProfile(req.ActivationCode); err != nil {
		log.Printf("[esim] add profile: %v", err)
		esimJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	esimJSONOK(w, map[string]bool{"ok": true})
}

// ----------------------------------------------------------------------------
// Request / response structs
// ----------------------------------------------------------------------------

// esimICCIDRequest is the common body for enable/disable/delete.
type esimICCIDRequest struct {
	ICCID string `json:"iccid"`
}

// esimAddRequest is the body for the add-by-activation-code endpoint.
type esimAddRequest struct {
	ActivationCode string `json:"activation_code"`
}

// esimOKResponse is the success body for enable/disable/delete.
type esimOKResponse struct {
	OK    bool   `json:"ok"`
	ICCID string `json:"iccid"`
}

// ----------------------------------------------------------------------------
// lpac CLI helpers
// ----------------------------------------------------------------------------

// esimRunLpac executes lpac with the given arguments and returns combined
// stdout output as a string. stderr is included in the error message on
// failure so diagnostics are useful without a terminal.
func esimRunLpac(args ...string) (string, error) {
	cmd := exec.Command("lpac", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// esimErrLpacAbsent returns the canonical error used when lpac is absent.
func esimErrLpacAbsent() error {
	return errors.New("lpac not installed — install lpac to manage eSIM profiles")
}

// esimParseProfileList parses lpac JSON output for `lpac profile list`.
// lpac outputs a JSON array of profile objects.  We tolerate both compact
// and pretty-printed output and ignore unknown fields.
func esimParseProfileList(out string) []ESIMProfile {
	out = strings.TrimSpace(out)
	if out == "" {
		return []ESIMProfile{}
	}

	// lpac may wrap output in a top-level {"code":0,"message":"success","payload":[...]}
	// envelope or may just be a bare array.  Try bare array first, then envelope.
	type lpacEnvelope struct {
		Payload json.RawMessage `json:"payload"`
	}

	type lpacRawProfile struct {
		ICCID        string `json:"iccid"`
		ProfileName  string `json:"profileName"`
		Nickname     string `json:"nickname"`
		ProfileState string `json:"profileState"`
		ProviderName string `json:"serviceProviderName"`
	}

	var rawProfiles []lpacRawProfile

	// Attempt envelope format.
	var env lpacEnvelope
	if json.Unmarshal([]byte(out), &env) == nil && len(env.Payload) > 0 {
		_ = json.Unmarshal(env.Payload, &rawProfiles)
	}

	// Fall back to bare array format.
	if len(rawProfiles) == 0 {
		_ = json.Unmarshal([]byte(out), &rawProfiles)
	}

	profiles := make([]ESIMProfile, 0, len(rawProfiles))
	for _, rp := range rawProfiles {
		state := esimNormaliseProfileState(rp.ProfileState)
		profiles = append(profiles, ESIMProfile{
			ICCID:        rp.ICCID,
			Name:         rp.ProfileName,
			Nickname:     rp.Nickname,
			State:        state,
			ProviderName: rp.ProviderName,
		})
	}
	return profiles
}

// esimNormaliseProfileState converts an lpac profileState value to a
// canonical "enabled" or "disabled" string.
func esimNormaliseProfileState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "enabled", "active", "1":
		return "enabled"
	default:
		return "disabled"
	}
}

// ----------------------------------------------------------------------------
// JSON response helpers (ESIM-prefixed to avoid collision with writeJSON)
// ----------------------------------------------------------------------------

// esimJSONOK writes a 200 JSON response.
func esimJSONOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[esim] response encode: %v", err)
	}
}

// esimJSONError writes an error JSON response with the given status code.
func esimJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
