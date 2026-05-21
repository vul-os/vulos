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
// Overridable via VULOS_CLOUD_API_URL for staging/dev.
package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
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
// POST /api/auth/cloud/signup.
type CloudSignupRequest struct {
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

	// Basic client-side pre-validation (belt-and-suspenders; cloud validates too).
	if req.Email == "" {
		writeErr(w, http.StatusBadRequest, "email is required")
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
	if req.FullName == "" {
		writeErr(w, http.StatusBadRequest, "full_name is required")
		return
	}

	body, err := json.Marshal(req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	targetURL := fmt.Sprintf("%s/api/auth/signup", cloudAPIURL())
	log.Printf("[cloudsignup] forwarding signup for %s to %s", req.Email, targetURL)

	client := &http.Client{Timeout: 15 * time.Second}
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "application/json")
	proxyReq.Header.Set("User-Agent", "vulos-os/1.0 cloudsignup")

	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[cloudsignup] cloud request failed: %v", err)
		writeErr(w, http.StatusBadGateway, "could not reach Vulos Cloud — check your network connection")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error reading cloud response")
		return
	}

	// Relay content-type and status code, but always emit valid JSON.
	w.Header().Set("Content-Type", "application/json")
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		w.WriteHeader(http.StatusCreated)
		w.Write(respBody)
		log.Printf("[cloudsignup] signup OK for %s", req.Email)
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
		errMsg = fmt.Sprintf("signup failed (HTTP %d)", resp.StatusCode)
	}

	w.WriteHeader(resp.StatusCode)
	json.NewEncoder(w).Encode(CloudSignupResponse{
		Error: errMsg,
		Code:  code,
		Hint:  hintForCode(code),
	})
	log.Printf("[cloudsignup] signup failed for %s: %s (code=%s, status=%d)",
		req.Email, errMsg, code, resp.StatusCode)
}
