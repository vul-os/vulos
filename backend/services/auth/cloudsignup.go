// Package auth — cloudsignup.go
//
// CLOGIN-04: Thin proxy for cloud account creation during first-boot setup.
//
// POST /api/auth/cloud/signup — forwards the request to the Vulos Cloud API at
// https://api.vulos.org/api/auth/signup and relays the response verbatim.
// Server-side validation (NIST length, HIBP breach-check) is performed by the
// cloud; this handler surfaces structured errors so the UI can show specific
// guidance.
//
// UNIFIED-SIGNIN fix: the CP guards its auth POSTs with a CSRF Origin
// allowlist and a PoW CaptchaGate — a bare http.Client is hard-403'd. The
// proxy now goes through services/cloudclient (Origin header + hashcash
// solver). The CP is also HANDLE-based (HANDLE-01): it accepts
// {handle,password} and mints <handle>@<domain> itself, so the proxy derives
// the handle from the submitted email/handle field.
//
// Overridable via VULOS_CLOUD_API_URL for staging/dev.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"vulos/backend/services/cloudclient"
)

// defaultCloudAPIURL is the Vulos Cloud production API base.
const defaultCloudAPIURL = "https://api.vulos.org"

// cloudAPIURL returns the cloud API base URL, honouring VULOS_CLOUD_API_URL.
func cloudAPIURL() string {
	if u := os.Getenv("VULOS_CLOUD_API_URL"); u != "" {
		return u
	}
	return defaultCloudAPIURL
}

// CloudSignupRequest is the wire format sent by the frontend to
// POST /api/auth/cloud/signup. Handle takes precedence; when absent the handle
// is derived from the local part of Email (the CP mints the identity email
// itself and rejects external addresses).
type CloudSignupRequest struct {
	Handle   string `json:"handle,omitempty"`
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"full_name"`
}

// CloudSignupResponse relays the cloud's response to the frontend.
// On success the cloud returns account_id + any onboarding token.
// On failure the "error" and "code" fields contain structured guidance.
type CloudSignupResponse struct {
	// Success fields (cloud-side)
	AccountID string `json:"account_id,omitempty"`
	Email     string `json:"email,omitempty"`
	// Error fields — mirrors cloud error envelope
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
	// Hint is a client-readable message constructed from the code.
	Hint string `json:"hint,omitempty"`
}

// hintForCode maps well-known cloud error codes to user-facing guidance.
func hintForCode(code string) string {
	switch code {
	case "password_breached":
		return "This password has appeared in a public data breach. Choose a different password."
	case "password_too_short":
		return "Password must be at least 12 characters."
	case "email_taken":
		return "An account with this email already exists. Try signing in instead."
	case "email_invalid":
		return "Enter a valid email address."
	default:
		return ""
	}
}

// handleCloudSignup proxies a cloud account-creation request.
//
// Request body (JSON):  { "email", "password", "full_name" }
// Success (201):        cloud's signup response JSON
// Failure (4xx/5xx):    { "error", "code", "hint" }
func (h *Handler) handleCloudSignup(w http.ResponseWriter, r *http.Request) {
	var req CloudSignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// HANDLE-01: derive the handle — explicit field first, else the local part
	// of the email (the CP constructs the identity email itself).
	handle := strings.ToLower(strings.TrimSpace(req.Handle))
	if handle == "" {
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if at := strings.Index(email, "@"); at > 0 {
			handle = email[:at]
		} else {
			handle = email
		}
	}

	// Basic client-side pre-validation (belt-and-suspenders; cloud validates too).
	if handle == "" {
		writeErr(w, http.StatusBadRequest, "handle or email is required")
		return
	}
	if len(req.Password) < 12 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(CloudSignupResponse{
			Error: "password_too_short",
			Code:  "password_too_short",
			Hint:  hintForCode("password_too_short"),
		})
		return
	}

	log.Printf("[cloudsignup] forwarding signup for handle %q to %s", handle, cloudAPIURL())

	// UNIFIED-SIGNIN: go through the PoW + Origin-capable CP client — a bare
	// POST is 403'd by the CP's CSRF/CaptchaGate.
	status, respBody, err := newCloudSignupClient().Signup(r.Context(), handle, req.Password)
	if err != nil {
		log.Printf("[cloudsignup] cloud request failed: %v", err)
		writeErr(w, http.StatusBadGateway, "could not reach Vulos Cloud — check your network connection")
		return
	}

	// Relay status, but always emit valid JSON.
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusCreated || status == http.StatusOK {
		w.WriteHeader(http.StatusCreated)
		w.Write(respBody) //nolint:errcheck — best-effort relay
		log.Printf("[cloudsignup] signup OK for handle %q", handle)
		return
	}

	// Non-success: parse cloud's error envelope and attach hint.
	var cloudErr struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	json.Unmarshal(respBody, &cloudErr) //nolint:errcheck — best-effort

	code := cloudErr.Code
	if code == "" {
		code = cloudErr.Error
	}
	errMsg := cloudErr.Error
	if errMsg == "" {
		errMsg = fmt.Sprintf("signup failed (HTTP %d)", status)
	}

	w.WriteHeader(status)
	json.NewEncoder(w).Encode(CloudSignupResponse{
		Error: errMsg,
		Code:  code,
		Hint:  hintForCode(code),
	})
	log.Printf("[cloudsignup] signup failed for handle %q: %s (code=%s, status=%d)",
		handle, errMsg, code, status)
}

// cloudSignupClient is the signup slice of the CP client (test seam).
type cloudSignupClient interface {
	Signup(ctx context.Context, handle, password string) (int, []byte, error)
}

// newCloudSignupClient is overridable in tests.
var newCloudSignupClient = func() cloudSignupClient {
	return cloudclient.New(cloudAPIURL())
}
