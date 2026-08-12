package main

// share_resolver.go — cmd/server implementations of the files account-share
// seams (Contract 2 + 3). Kept here (not in the files package) because resolution
// needs the local auth store AND the peering directory, neither of which the
// files package may import.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
	"vulos/backend/services/auth"
	"vulos/backend/services/files"
	"vulos/backend/services/peering"
)

// osShareResolver resolves a recipient email to a share target and decides
// locality:
//   - A LOCAL OS user (same box / control plane) → CO-CLOUD ACL grant path.
//   - Anyone else resolvable via the vulos.org directory (Contract 2) → REMOTE
//     peershare. For an account-only user the directory returns the cell's
//     cloud-home VulosID + the cell server.
type osShareResolver struct {
	auth      *auth.Store
	directory *peering.DiscoveryService
}

// ResolveRecipient implements files.ShareResolver.
func (r *osShareResolver) ResolveRecipient(ctx context.Context, email string) (files.ShareRecipient, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return files.ShareRecipient{}, nil
	}
	// 1) Co-cloud: a local account on this control plane wins (no peering).
	if r.auth != nil {
		if u := r.auth.GetUserByEmail(email); u != nil {
			name := u.Email
			return files.ShareRecipient{PrincipalID: u.ID, DisplayName: name}, nil
		}
	}
	// 2) Remote: resolve {VulosID, Server} via the directory.
	if r.directory != nil {
		res, err := r.directory.DiscoveryLookupByEmail(ctx, email)
		if err != nil {
			return files.ShareRecipient{}, err
		}
		if res != nil && res.VulosID != "" {
			return files.ShareRecipient{
				VulosID:       res.VulosID,
				Server:        res.Server,
				DisplayName:   res.DisplayName,
				ContentPubKey: res.ContentPubKey,
			}, nil
		}
	}
	return files.ShareRecipient{}, nil // not found → ErrRecipientNotFound upstream
}

// httpCapabilityDeliverer POSTs a minted peer-share capability to a remote
// recipient's server intake. The recipient's own box redeems the capability
// and stages the bytes into that owner's Drive.
//
// CROSS-REPO INTERFACE ASSUMPTION (the recipient box's peer intake must accept):
//
//	POST <server>/api/files/peer/inbound
//	Content-Type: application/json
//	{ "recipient": "<recipient VulosID>", "link": "<base64url capability token>",
//	  "capability": { …signed Capability… } }
//
// A 2xx response means the intake accepted the capability for redemption.
//
// SSRF-SHARE-01 / same class as SSRF-FILES-01: `server` is directory-resolved
// (or poisoned) and attacker-influenceable — the directory only vouches for
// email→{VulosID,server}, not that the server is a benign public host. A bare
// http.Client would re-resolve the hostname at dial time (so a rebinding DNS
// name that looked public a moment earlier reaches an internal/metadata IP at
// connect) and would follow HTTP 3xx to an unvalidated target. This is a
// write-SSRF (the POST body is attacker-chosen) against internal/IMDS hosts.
//
// The client is built by newHTTPCapabilityDeliverer with safedial's dial-time
// Control hook (re-validates the ACTUAL resolved IP before every connect,
// including redirect dials) and CheckRedirect=ErrUseLastResponse (never follow
// redirects — a peer intake endpoint has no legitimate reason to redirect). The
// pre-dial addrValidator string check stays as the fast-fail first layer.
type httpCapabilityDeliverer struct {
	client *http.Client
	// addrValidator is the pre-dial fast-fail check on the resolved server host.
	// Overridable in tests to simulate a DNS-rebinding bypass of the pre-check.
	addrValidator func(host string) error
}

// deliverAllowLANOnce guards a one-time read of VULOS_PEER_ALLOW_LAN, shared
// with the files peer transport semantics: LAN peer-share is opt-in, but
// loopback/link-local/metadata are blocked regardless.
var (
	deliverAllowLANOnce sync.Once
	deliverAllowLAN     bool
)

func getDeliverAllowLAN() bool {
	deliverAllowLANOnce.Do(func() {
		v := strings.TrimSpace(os.Getenv("VULOS_PEER_ALLOW_LAN"))
		deliverAllowLAN = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	})
	return deliverAllowLAN
}

// newHTTPCapabilityDeliverer builds a deliverer whose client is hardened against
// SSRF at dial time (re-validate resolved IP on every connect) and refuses to
// follow redirects. Use this instead of constructing httpCapabilityDeliverer
// with a bare http.Client.
func newHTTPCapabilityDeliverer() *httpCapabilityDeliverer {
	dialer := safedial.New(getDeliverAllowLAN())
	dialer.Timeout = 10 * time.Second
	return &httpCapabilityDeliverer{
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
			},
			// Fail closed: never follow redirects. A 3xx surfaces as a non-2xx
			// owner response and errors out, so the delivery POST can never be
			// bounced to an internal target the guard never inspected.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		addrValidator: validateDeliveryServer,
	}
}

// validateDeliveryServer is the pre-dial fast-fail SSRF guard: it resolves the
// host ONCE and rejects it if any resolved IP is in a blocked range (loopback,
// link-local/metadata, private without the LAN opt-in). The dial-time Control
// hook re-validates at connect to close the TOCTOU/rebinding window this
// string-level check cannot see.
func validateDeliveryServer(host string) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("empty host")
	}
	_, err := safedial.ValidateHost(host, getDeliverAllowLAN())
	return err
}

// DeliverCapability implements files.CapabilityDeliverer.
func (d *httpCapabilityDeliverer) DeliverCapability(ctx context.Context, server string, del files.CapabilityDelivery) error {
	base := strings.TrimSpace(server)
	if base == "" {
		return fmt.Errorf("capability delivery: empty server")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	endpoint := strings.TrimRight(base, "/") + "/api/files/peer/inbound"

	// Pre-dial fast-fail: reject obviously-internal targets before any network
	// I/O. The dial-time guard on the client re-checks the ACTUAL resolved IP.
	if validate := d.addrValidator; validate != nil {
		u, perr := url.Parse(endpoint)
		if perr != nil {
			return fmt.Errorf("capability delivery: parse endpoint: %w", perr)
		}
		if err := validate(u.Hostname()); err != nil {
			return fmt.Errorf("capability delivery: server %q blocked by SSRF guard: %w", u.Hostname(), err)
		}
	}

	body, err := json.Marshal(del)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("capability delivery: server %s returned %d: %s", endpoint, resp.StatusCode, string(raw))
	}
	return nil
}
