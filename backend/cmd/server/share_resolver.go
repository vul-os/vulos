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
	"strings"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/files"
	"vulos/backend/services/peering"
)

// osShareResolver resolves a recipient email to a share target and decides
// locality:
//   - A LOCAL OS user (same box / control plane) → CO-CLOUD ACL grant path.
//   - Anyone else resolvable via the vulos.org directory (Contract 2) → REMOTE
//     peershare. For an account-only user the directory returns the cell's
//     cloud-home VulaID + the cell server.
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
	// 2) Remote: resolve {VulaID, Server} via the directory.
	if r.directory != nil {
		res, err := r.directory.DiscoveryLookupByEmail(ctx, email)
		if err != nil {
			return files.ShareRecipient{}, err
		}
		if res != nil && res.VulaID != "" {
			return files.ShareRecipient{
				VulaID:        res.VulaID,
				Server:        res.Server,
				DisplayName:   res.DisplayName,
				ContentPubKey: res.ContentPubKey,
			}, nil
		}
	}
	return files.ShareRecipient{}, nil // not found → ErrRecipientNotFound upstream
}

// httpCapabilityDeliverer POSTs a minted peer-share capability to a remote
// recipient's server intake. For an account-only recipient the cell redeems the
// capability on the account's behalf and stages the bytes into the account's
// Drive (vulos-cloud owns the redemption side).
//
// CROSS-REPO INTERFACE ASSUMPTION (reconcile with vulos-cloud):
//
//	POST <server>/api/files/peer/inbound
//	Content-Type: application/json
//	{ "recipient": "<recipient VulaID>", "link": "<base64url capability token>",
//	  "capability": { …signed Capability… } }
//
// A 2xx response means the intake accepted the capability for redemption.
type httpCapabilityDeliverer struct {
	client *http.Client
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
