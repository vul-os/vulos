// Package cloudenroll is the Vulos OS box-side RFC 8628 device-enrollment client
// (INTEG-SEC-01). It obtains an owner-attested, management-CA-signed device
// certificate from the control plane and holds the matching ed25519 private key
// sealed at rest, so the box can authenticate to the CP as *this* device rather
// than with the fleet-wide shared secret.
//
// Flow (RFC 8628 Device Authorization Grant):
//
//  1. Generate an ed25519 device keypair; seal the private seed via the device
//     keystore (TPM where available, else the software-encrypted store).
//  2. POST /enroll/start {device_pubkey} → {device_code, user_code,
//     verification_uri, interval, expires_in}. Surface the user_code +
//     verification_uri so the fleet owner can approve the device in-browser.
//  3. Poll POST /enroll/poll {device_code} until the owner approves, honoring
//     authorization_pending / slow_down. On approval the CP returns
//     {ulid, device_cert, account_id}.
//  4. Persist the cert + ulid + account and the sealed key.
//
// The owner's in-browser approval IS the attestation — it eliminates the
// trust-on-first-use window of the device-key registry.
package cloudenroll

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultCloudBaseURL mirrors the integrations/multiinstance default.
const DefaultCloudBaseURL = "https://api.vulos.org"

// Sealer is the subset of devicekey.KeyStore used to protect the device key at
// rest. The software/TPM keystore satisfies it.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Unseal(ciphertext []byte) ([]byte, error)
}

// Identity is an enrolled device identity: the ed25519 key (private sealed at
// rest, loaded into memory after Unseal) plus the CA-signed cert binding it to
// the ULID and owning account.
type Identity struct {
	ULID    string
	Account string
	Pub     ed25519.PublicKey
	Cert    []byte // management-CA ed25519 signature over the cert payload
	priv    ed25519.PrivateKey
}

// DeviceCert returns the cert + public key for presentation on the mint request.
// ok is false when there is no usable cert (caller falls back to other auth).
func (id *Identity) DeviceCert() (cert, pub []byte, ok bool) {
	if id == nil || len(id.Cert) == 0 || len(id.Pub) != ed25519.PublicKeySize {
		return nil, nil, false
	}
	return id.Cert, id.Pub, true
}

// SignMint signs a mint message with the device key (ed25519, message signed
// directly — no external hash).
func (id *Identity) SignMint(message string) ([]byte, error) {
	if id == nil || id.priv == nil {
		return nil, errors.New("cloudenroll: no device key")
	}
	return ed25519.Sign(id.priv, []byte(message)), nil
}

// Enroller drives the device-authorization grant and persists the result.
type Enroller struct {
	baseURL string
	dir     string
	ks      Sealer
	hc      *http.Client

	// intervalOverride, when >0, replaces the server-advertised poll interval
	// (tests set a tiny value; production leaves it 0).
	intervalOverride time.Duration
}

// New builds an Enroller. baseURL defaults to DefaultCloudBaseURL; dir is the
// persistence directory (created if absent); ks seals the private key at rest.
func New(baseURL, dir string, ks Sealer) *Enroller {
	if baseURL == "" {
		baseURL = DefaultCloudBaseURL
	}
	return &Enroller{
		baseURL: baseURL,
		dir:     dir,
		ks:      ks,
		hc:      &http.Client{Timeout: 20 * time.Second},
	}
}

// persisted is the on-disk identity (minus the sealed key, stored separately).
type persisted struct {
	ULID    string `json:"ulid"`
	Account string `json:"account"`
	PubB64  string `json:"pub_b64"`
	CertB64 string `json:"cert_b64"`
}

func (e *Enroller) identityPath() string  { return filepath.Join(e.dir, "identity.json") }
func (e *Enroller) sealedKeyPath() string { return filepath.Join(e.dir, "enroll_key.sealed") }

// Load reads a previously-enrolled identity, unsealing the private key. Returns
// (nil, nil) when the box is not yet enrolled (no persisted identity).
func (e *Enroller) Load() (*Identity, error) {
	raw, err := os.ReadFile(e.identityPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudenroll: read identity: %w", err)
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("cloudenroll: parse identity: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(p.PubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("cloudenroll: corrupt identity pubkey")
	}
	cert, err := base64.StdEncoding.DecodeString(p.CertB64)
	if err != nil {
		return nil, errors.New("cloudenroll: corrupt identity cert")
	}
	sealed, err := os.ReadFile(e.sealedKeyPath())
	if err != nil {
		return nil, fmt.Errorf("cloudenroll: read sealed key: %w", err)
	}
	seed, err := e.ks.Unseal(sealed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("cloudenroll: cannot unseal device key")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Identity{ULID: p.ULID, Account: p.Account, Pub: pub, Cert: cert, priv: priv}, nil
}

// Enroll runs the full device grant. notify (may be nil) is called once with the
// user_code + verification_uri so the owner can approve in-browser. It blocks,
// polling until the owner approves (returns the Identity) or ctx is cancelled /
// the grant expires / is denied.
func (e *Enroller) Enroll(ctx context.Context, notify func(userCode, verificationURI string)) (*Identity, error) {
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		return nil, fmt.Errorf("cloudenroll: mkdir: %w", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cloudenroll: genkey: %w", err)
	}

	grant, err := e.start(ctx, pub)
	if err != nil {
		return nil, err
	}
	if notify != nil {
		notify(grant.UserCode, grant.VerificationURI)
	}

	interval := time.Duration(grant.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if e.intervalOverride > 0 {
		interval = e.intervalOverride
	}
	deadline := time.Now().Add(time.Duration(grant.ExpiresIn) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if grant.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, errors.New("cloudenroll: enrollment grant expired before approval")
		}
		res, retry, err := e.poll(ctx, grant.DeviceCode)
		if err != nil {
			return nil, err
		}
		if retry > 0 {
			interval += retry // honor slow_down: widen (never narrow) the cadence
			continue
		}
		if res == nil {
			continue // authorization_pending
		}
		id, err := e.persist(priv, pub, res)
		if err != nil {
			return nil, err
		}
		return id, nil
	}
}

type startResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func (e *Enroller) start(ctx context.Context, pub ed25519.PublicKey) (*startResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"device_pubkey": base64.StdEncoding.EncodeToString(pub),
	})
	resp, err := e.post(ctx, "/enroll/start", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudenroll: start status %d", resp.StatusCode)
	}
	var sr startResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("cloudenroll: decode start: %w", err)
	}
	if sr.DeviceCode == "" {
		return nil, errors.New("cloudenroll: start returned no device_code")
	}
	return &sr, nil
}

type pollResponse struct {
	// Success fields.
	ULID       string `json:"ulid"`
	DeviceCert []byte `json:"device_cert"` // base64 in JSON
	AccountID  string `json:"account_id"`
	// RFC 8628 soft-error field (200 body).
	Error string `json:"error"`
}

// poll performs one poll. Returns (result, retryInterval, err):
//   - result != nil  → approved
//   - retryInterval>0 → slow_down: caller should widen the interval and retry
//   - result==nil, retry==0, err==nil → authorization_pending: keep polling
func (e *Enroller) poll(ctx context.Context, deviceCode string) (*pollResponse, time.Duration, error) {
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode})
	resp, err := e.post(ctx, "/enroll/poll", body)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var pr pollResponse
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return nil, 0, fmt.Errorf("cloudenroll: decode poll: %w", err)
		}
		switch pr.Error {
		case "authorization_pending":
			return nil, 0, nil
		case "slow_down":
			// RFC 8628 §3.5: increase the poll interval by at least 5s. The caller
			// ADDS this to the current interval, so repeated slow_downs compound and
			// the cadence never narrows.
			return nil, 5 * time.Second, nil
		case "":
			if pr.ULID == "" || len(pr.DeviceCert) == 0 {
				return nil, 0, errors.New("cloudenroll: approved grant missing ulid/cert")
			}
			return &pr, 0, nil
		default:
			return nil, 0, fmt.Errorf("cloudenroll: poll error %q", pr.Error)
		}
	case http.StatusGone:
		return nil, 0, errors.New("cloudenroll: enrollment grant expired")
	case http.StatusForbidden:
		return nil, 0, errors.New("cloudenroll: enrollment denied by owner")
	default:
		return nil, 0, fmt.Errorf("cloudenroll: poll status %d", resp.StatusCode)
	}
}

func (e *Enroller) persist(priv ed25519.PrivateKey, pub ed25519.PublicKey, res *pollResponse) (*Identity, error) {
	sealed, err := e.ks.Seal(priv.Seed())
	if err != nil {
		return nil, fmt.Errorf("cloudenroll: seal key: %w", err)
	}
	if err := os.WriteFile(e.sealedKeyPath(), sealed, 0o600); err != nil {
		return nil, fmt.Errorf("cloudenroll: write sealed key: %w", err)
	}
	p := persisted{
		ULID:    res.ULID,
		Account: res.AccountID,
		PubB64:  base64.StdEncoding.EncodeToString(pub),
		CertB64: base64.StdEncoding.EncodeToString(res.DeviceCert),
	}
	raw, _ := json.MarshalIndent(p, "", "  ")
	if err := os.WriteFile(e.identityPath(), raw, 0o600); err != nil {
		return nil, fmt.Errorf("cloudenroll: write identity: %w", err)
	}
	return &Identity{ULID: res.ULID, Account: res.AccountID, Pub: pub, Cert: res.DeviceCert, priv: priv}, nil
}

func (e *Enroller) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return e.hc.Do(req)
}
