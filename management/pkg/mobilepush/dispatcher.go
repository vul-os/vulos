package mobilepush

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// apnsHost is the APNs production HTTP/2 endpoint.
const apnsHost = "https://api.push.apple.com"

// fcmEndpointFmt is the FCM HTTP v1 send endpoint (project-specific).
const fcmEndpointFmt = "https://fcm.googleapis.com/v1/projects/%s/messages:send"

// ──────────────────────────────────────────────────────────────────────────────
// Config
// ──────────────────────────────────────────────────────────────────────────────

// Config holds the signing credentials for APNs and FCM.
// Populated from environment variables by NewConfig.
type Config struct {
	// APNs
	APNsKeyID    string
	APNsTeamID   string
	APNsKeyP8    []byte // contents of the .p8 key file
	APNsBundleID string // optional; used as apns-topic header

	// FCM
	FCMProjectID          string
	FCMServiceAccountJSON []byte // contents of the service-account JSON

	// Prod indicates that missing vars should be a hard failure.
	Prod bool
}

// NewConfig reads env vars and returns a Config.
// In prod (isProd=true), returns ErrMissingConfig when any required var is absent.
// Otherwise logs a warning and returns a partial config (no-op for that platform).
func NewConfig(isProd bool) (*Config, error) {
	c := &Config{Prod: isProd}

	c.APNsKeyID = os.Getenv("APNS_KEY_ID")
	c.APNsTeamID = os.Getenv("APNS_TEAM_ID")
	c.APNsBundleID = os.Getenv("APNS_BUNDLE_ID")
	c.FCMProjectID = os.Getenv("FCM_PROJECT_ID")

	keyPath := os.Getenv("APNS_KEY_P8_PATH")
	saPath := os.Getenv("FCM_SERVICE_ACCOUNT_PATH")

	missingAPNs := c.APNsKeyID == "" || c.APNsTeamID == "" || keyPath == ""
	missingFCM := c.FCMProjectID == "" || saPath == ""

	if isProd && (missingAPNs || missingFCM) {
		return nil, fmt.Errorf("%w: APNS_KEY_ID, APNS_TEAM_ID, APNS_KEY_P8_PATH, FCM_PROJECT_ID, FCM_SERVICE_ACCOUNT_PATH are all required in prod", ErrMissingConfig)
	}

	if !missingAPNs {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			if isProd {
				return nil, fmt.Errorf("%w: cannot read APNS_KEY_P8_PATH: %v", ErrMissingConfig, err)
			}
			log.Printf("[mobilepush] WARN: cannot read APNs key file %s: %v — APNs disabled", keyPath, err)
		} else {
			c.APNsKeyP8 = data
		}
	} else if !isProd {
		log.Printf("[mobilepush] WARN: APNs env vars not set — APNs push disabled")
	}

	if !missingFCM {
		data, err := os.ReadFile(saPath)
		if err != nil {
			if isProd {
				return nil, fmt.Errorf("%w: cannot read FCM_SERVICE_ACCOUNT_PATH: %v", ErrMissingConfig, err)
			}
			log.Printf("[mobilepush] WARN: cannot read FCM service account file %s: %v — FCM disabled", saPath, err)
		} else {
			c.FCMServiceAccountJSON = data
		}
	} else if !isProd {
		log.Printf("[mobilepush] WARN: FCM env vars not set — FCM push disabled")
	}

	return c, nil
}

// apnsEnabled reports whether APNs credentials are fully populated.
func (c *Config) apnsEnabled() bool {
	return c != nil && c.APNsKeyID != "" && c.APNsTeamID != "" && len(c.APNsKeyP8) > 0
}

// fcmEnabled reports whether FCM credentials are fully populated.
func (c *Config) fcmEnabled() bool {
	return c != nil && c.FCMProjectID != "" && len(c.FCMServiceAccountJSON) > 0
}

// ──────────────────────────────────────────────────────────────────────────────
// Dispatcher
// ──────────────────────────────────────────────────────────────────────────────

// Dispatcher sends push notifications to all device tokens registered for an
// account when a JMAP push event arrives.
type Dispatcher struct {
	sub    *Subscriber
	cfg    *Config
	client *http.Client

	// APNs ES256 JWT cache (token valid for ≤60 min per Apple spec).
	mu           sync.Mutex
	apnsToken    string
	apnsTokenExp time.Time
}

// NewDispatcher constructs a Dispatcher.  cfg may be nil or partially populated;
// platforms with missing config become no-ops (in non-prod mode).
func NewDispatcher(sub *Subscriber, cfg *Config) *Dispatcher {
	return &Dispatcher{
		sub:    sub,
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Dispatch fans out a JMAP push event to all registered device tokens for the
// account embedded in the event.  It sends APNs and FCM notifications
// concurrently.  Partial failures are accumulated in the result.
func (d *Dispatcher) Dispatch(ctx context.Context, event JMAPPushEvent) DispatchResult {
	result := DispatchResult{AccountID: event.AccountID}

	tokens, err := d.sub.ListTokens(ctx, event.AccountID)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("list tokens: %w", err))
		return result
	}

	if len(tokens) == 0 {
		return result
	}

	// Build human-readable notification body from type-states.
	body := buildNotificationBody(event.TypeStates)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, dt := range tokens {
		dt := dt
		wg.Add(1)
		go func() {
			defer wg.Done()
			var sendErr error
			switch dt.Platform {
			case PlatformAPNs:
				sendErr = d.sendAPNs(ctx, dt.Token, body)
			case PlatformFCM:
				sendErr = d.sendFCM(ctx, dt.Token, body)
			}
			mu.Lock()
			defer mu.Unlock()
			if sendErr != nil {
				result.TokensFailed++
				result.Errors = append(result.Errors, fmt.Errorf("token %s: %w", safePrefix(dt.Token, 8), sendErr))
			} else {
				result.TokensSent++
			}
		}()
	}
	wg.Wait()
	return result
}

func safePrefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// buildNotificationBody returns a short human-readable string summarising the
// state-change types (e.g. "Email, Mailbox updated").
func buildNotificationBody(typeStates map[string]string) string {
	if len(typeStates) == 0 {
		return "You have new activity in Vulos Mail."
	}
	types := make([]string, 0, len(typeStates))
	for t := range typeStates {
		types = append(types, t)
	}
	return strings.Join(types, ", ") + " updated — tap to refresh."
}

// ──────────────────────────────────────────────────────────────────────────────
// APNs sender
// ──────────────────────────────────────────────────────────────────────────────

// apnsPayload is the minimal APNs JSON payload.
type apnsPayload struct {
	Aps apnsAps `json:"aps"`
}

type apnsAps struct {
	Alert            string `json:"alert"`
	Sound            string `json:"sound,omitempty"`
	ContentAvailable int    `json:"content-available,omitempty"`
}

func (d *Dispatcher) sendAPNs(ctx context.Context, deviceToken, body string) error {
	if !d.cfg.apnsEnabled() {
		if d.cfg != nil && d.cfg.Prod {
			return ErrMissingConfig
		}
		return nil // no-op in non-prod
	}

	jwt, err := d.apnsJWT()
	if err != nil {
		return fmt.Errorf("apns jwt: %w", err)
	}

	payload := apnsPayload{Aps: apnsAps{Alert: body, Sound: "default"}}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("apns marshal: %w", err)
	}

	url := fmt.Sprintf("%s/3/device/%s", apnsHost, deviceToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("apns request: %w", err)
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")
	if d.cfg.APNsBundleID != "" {
		req.Header.Set("apns-topic", d.cfg.APNsBundleID)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("apns send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apns: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// apnsJWT returns a cached or freshly-minted APNs provider JWT (ES256).
// Apple requires the token to be re-minted at least once per hour.
func (d *Dispatcher) apnsJWT() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.apnsToken != "" && time.Now().Before(d.apnsTokenExp) {
		return d.apnsToken, nil
	}

	key, err := parseP8Key(d.cfg.APNsKeyP8)
	if err != nil {
		return "", fmt.Errorf("parse p8 key: %w", err)
	}

	now := time.Now().Unix()
	headerJSON := fmt.Sprintf(`{"alg":"ES256","kid":"%s"}`, d.cfg.APNsKeyID)
	claimsJSON := fmt.Sprintf(`{"iss":"%s","iat":%d}`, d.cfg.APNsTeamID, now)

	tokenStr, err := es256Sign(headerJSON, claimsJSON, key)
	if err != nil {
		return "", fmt.Errorf("es256 sign: %w", err)
	}

	d.apnsToken = tokenStr
	d.apnsTokenExp = time.Now().Add(55 * time.Minute) // refresh 5 min before expiry
	return tokenStr, nil
}

// parseP8Key parses an Apple .p8 PEM private key (PKCS#8 EC key).
func parseP8Key(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("mobilepush: failed to decode PEM block from .p8 key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mobilepush: parse PKCS8 key: %w", err)
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("mobilepush: .p8 key is not an ECDSA key")
	}
	return ecKey, nil
}

// es256Sign produces a JWT string signed with the given ECDSA key using ES256.
// We implement this manually to avoid importing a JWT library (keeping the
// dependency surface minimal and pure-Go).
func es256Sign(headerJSON, claimsJSON string, key *ecdsa.PrivateKey) (string, error) {
	header := base64URLEncode([]byte(headerJSON))
	claims := base64URLEncode([]byte(claimsJSON))
	signingInput := header + "." + claims

	digest := sha256Sum([]byte(signingInput))

	r, s, err := ecdsa.Sign(randReader(), key, digest[:])
	if err != nil {
		return "", err
	}

	// Encode signature as R || S (64 bytes for P-256), raw (not DER).
	rb := r.Bytes()
	sb := s.Bytes()
	sig := make([]byte, 64)
	copy(sig[32-len(rb):32], rb)
	copy(sig[64-len(sb):64], sb)

	return signingInput + "." + base64URLEncode(sig), nil
}

// rs256Sign produces a JWT string signed with the given RSA key using RS256
// (RSASSA-PKCS1-v1_5 with SHA-256).  Used for Google FCM service accounts that
// carry RSA private keys (the default key type when creating a service account).
func rs256Sign(headerJSON, claimsJSON string, key *rsa.PrivateKey) (string, error) {
	header := base64URLEncode([]byte(headerJSON))
	claims := base64URLEncode([]byte(claimsJSON))
	signingInput := header + "." + claims

	digest := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}

	return signingInput + "." + base64URLEncode(sig), nil
}

// ──────────────────────────────────────────────────────────────────────────────
// FCM sender
// ──────────────────────────────────────────────────────────────────────────────

// fcmRequest is the FCM HTTP v1 request body.
type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (d *Dispatcher) sendFCM(ctx context.Context, deviceToken, body string) error {
	if !d.cfg.fcmEnabled() {
		if d.cfg != nil && d.cfg.Prod {
			return ErrMissingConfig
		}
		return nil // no-op in non-prod
	}

	bearerToken, err := d.fcmAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("fcm access token: %w", err)
	}

	msg := fcmRequest{
		Message: fcmMessage{
			Token:        deviceToken,
			Notification: fcmNotification{Title: "Vulos Mail", Body: body},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("fcm marshal: %w", err)
	}

	url := fmt.Sprintf(fcmEndpointFmt, d.cfg.FCMProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("fcm request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("fcm send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fcm: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// fcmAccessToken obtains a Google OAuth2 access token using the service-account
// JSON credentials via a JWT grant (RFC 7523).  Pure-Go, no CGO path.
func (d *Dispatcher) fcmAccessToken(ctx context.Context) (string, error) {
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(d.cfg.FCMServiceAccountJSON, &sa); err != nil {
		return "", fmt.Errorf("fcm: parse service account: %w", err)
	}

	keyBlock, _ := pem.Decode([]byte(sa.PrivateKey))
	if keyBlock == nil {
		return "", errors.New("fcm: failed to decode service account private key PEM")
	}
	privKeyIface, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return "", fmt.Errorf("fcm: parse private key: %w", err)
	}

	now := time.Now().Unix()
	claimsJSON := fmt.Sprintf(
		`{"iss":"%s","scope":"https://www.googleapis.com/auth/firebase.messaging","aud":"%s","iat":%d,"exp":%d}`,
		sa.ClientEmail, sa.TokenURI, now, now+3600,
	)

	// Detect RSA vs ECDSA and sign with the appropriate algorithm.
	// Google FCM service accounts default to RSA (RS256); EC service accounts
	// use ES256. Both are valid; we detect via type-switch on the parsed key.
	var jwtStr string
	switch key := privKeyIface.(type) {
	case *rsa.PrivateKey:
		headerJSON := `{"alg":"RS256","typ":"JWT"}`
		jwtStr, err = rs256Sign(headerJSON, claimsJSON, key)
		if err != nil {
			return "", fmt.Errorf("fcm: rs256 sign jwt: %w", err)
		}
	case *ecdsa.PrivateKey:
		headerJSON := `{"alg":"ES256","typ":"JWT"}`
		jwtStr, err = es256Sign(headerJSON, claimsJSON, key)
		if err != nil {
			return "", fmt.Errorf("fcm: es256 sign jwt: %w", err)
		}
	default:
		return "", errors.New("fcm: unsupported service-account key type; expected RSA or ECDSA")
	}

	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	reqBody := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Ajwt-bearer&assertion=" + jwtStr
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI,
		strings.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("fcm: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fcm: token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("fcm: decode token response: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("fcm: token error: %s", tokenResp.Error)
	}
	return tokenResp.AccessToken, nil
}
