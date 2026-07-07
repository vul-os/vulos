package selfhost

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CSRF/PKCE state for the auth-code flow.
//
// A short-lived HttpOnly cookie carries BOTH the HMAC-signed CSRF state AND the
// PKCE code_verifier across the redirect. The state proves the callback belongs
// to a flow this box started (CSRF); PKCE binds the auth code to the verifier
// only this box holds (interception defense). Both are bound to the user id so a
// callback for user A cannot complete a flow started by user B.

const (
	stateCookie    = "vulos_integ_state"
	stateMaxAgeSec = 600 // 10 minutes
	nonceLen       = 16
	pkceVerLen     = 32 // 32 bytes → 43-char base64url verifier (RFC 7636 range)
)

// ErrStateMismatch is returned when the callback state fails CSRF/binding checks.
var ErrStateMismatch = errors.New("selfhost: OAuth state mismatch")

// stateCookieValue is the JSON-free payload stored in the cookie:
//
//	base64url( nonce|ts|userID|provider|pkceVerifier|HMAC )
//
// The HMAC covers nonce|ts|userID|provider|pkceVerifier so none can be tampered.
type flowState struct {
	Nonce        string
	TS           int64
	UserID       string
	Provider     string
	PKCEVerifier string
}

func stateHMAC(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// pkceChallengeS256 returns the base64url(SHA256(verifier)) code challenge.
func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState creates a signed state + PKCE verifier bound to (userID,
// provider), writes them to a short-lived cookie, and returns the opaque state
// value (to pass as the OAuth `state`) and the PKCE code CHALLENGE (to pass to
// the authorize URL). The verifier stays in the cookie; only the challenge goes
// to the provider.
func GenerateState(w http.ResponseWriter, secret []byte, userID, provider string, now time.Time, secureCookie bool) (state, pkceChallenge string, err error) {
	nb := make([]byte, nonceLen)
	if _, e := rand.Read(nb); e != nil {
		return "", "", e
	}
	vb := make([]byte, pkceVerLen)
	if _, e := rand.Read(vb); e != nil {
		return "", "", e
	}
	nonce := base64.RawURLEncoding.EncodeToString(nb)
	verifier := base64.RawURLEncoding.EncodeToString(vb)
	ts := now.Unix()

	payload := strings.Join([]string{nonce, strconv.FormatInt(ts, 10), userID, provider, verifier}, "|")
	mac := stateHMAC(secret, payload)
	cookieVal := base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + mac))

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    cookieVal,
		Path:     "/api/integrations",
		MaxAge:   stateMaxAgeSec,
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	// The OAuth `state` param is the nonce|ts (the non-secret half); the full
	// binding is verified against the cookie at callback.
	state = nonce + "|" + strconv.FormatInt(ts, 10)
	return state, pkceChallengeS256(verifier), nil
}

// VerifyState validates the callback's state against the cookie for the CURRENT
// session user, returning the PKCE verifier to complete the exchange. It checks:
// HMAC integrity, the timestamp window, that the state param matches the cookie's
// nonce|ts, and that the cookie's user/provider match the caller.
func VerifyState(r *http.Request, returnedState, secret string, userID, provider string, now time.Time) (pkceVerifier string, err error) {
	return verifyStateBytes(r, returnedState, []byte(secret), userID, provider, now)
}

func verifyStateBytes(r *http.Request, returnedState string, secret []byte, userID, provider string, now time.Time) (string, error) {
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		return "", ErrStateMismatch
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", ErrStateMismatch
	}
	// payload|mac where payload = nonce|ts|userID|provider|verifier
	parts := strings.Split(string(raw), "|")
	if len(parts) != 6 {
		return "", ErrStateMismatch
	}
	nonce, tsStr, cUser, cProvider, verifier, mac := parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
	payload := strings.Join([]string{nonce, tsStr, cUser, cProvider, verifier}, "|")
	if !hmac.Equal([]byte(mac), []byte(stateHMAC(secret, payload))) {
		return "", ErrStateMismatch
	}
	// Bind to the current session user + the provider being connected.
	if cUser != userID || cProvider != provider {
		return "", ErrStateMismatch
	}
	// The OAuth state echoed back must match the cookie's non-secret half.
	if returnedState != nonce+"|"+tsStr {
		return "", ErrStateMismatch
	}
	// Timestamp window.
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", ErrStateMismatch
	}
	age := now.Unix() - ts
	if age < -stateMaxAgeSec || age > stateMaxAgeSec {
		return "", ErrStateMismatch
	}
	return verifier, nil
}

// ClearStateCookie removes the state cookie after use.
func ClearStateCookie(w http.ResponseWriter, secureCookie bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Path:     "/api/integrations",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}
