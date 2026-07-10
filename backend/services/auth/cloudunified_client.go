// Package auth — cloudunified_client.go
//
// Adapter between the CloudLoginClient seam (cloudunified.go) and the concrete
// services/cloudclient implementation, plus the sentinel-error aliases the
// handler maps to HTTP responses. Kept in its own file so cloudunified.go
// stays free of the cloudclient import (tests swap newCloudLoginClient).
package auth

import (
	"context"
	"crypto/ed25519"

	"vulos/backend/services/cloudclient"
)

// Sentinel aliases (same error values — errors.Is works across the seam).
var (
	errInvalidCloudCredentials = cloudclient.ErrInvalidCredentials
	errInvalidCloudTOTP        = cloudclient.ErrInvalidTOTP
	errCloudRateLimited        = cloudclient.ErrRateLimited
	errCloudReauthRequired     = cloudclient.ErrIssueReauthRequired
	errCloudIssueForbidden     = cloudclient.ErrIssueForbidden
)

// cloudClientAdapter satisfies CloudLoginClient over *cloudclient.Client.
type cloudClientAdapter struct{ c *cloudclient.Client }

func (a cloudClientAdapter) Login(ctx context.Context, email, password, totpCode string) (*CloudLoginOutcome, error) {
	out, err := a.c.Login(ctx, email, password, totpCode)
	if err != nil {
		return nil, err
	}
	return &CloudLoginOutcome{Step: out.Step, User: out.User}, nil
}

func (a cloudClientAdapter) BrokerPubkey(ctx context.Context) (ed25519.PublicKey, error) {
	return a.c.BrokerPubkey(ctx)
}

func (a cloudClientAdapter) IssueLoginToken(ctx context.Context, ulid, profileName string) ([]byte, string, error) {
	return a.c.IssueLoginToken(ctx, ulid, profileName)
}

// defaultCloudLoginClient builds the production client against the configured
// cloud API base URL (VULOS_CLOUD_API_URL, default https://api.vulos.org).
func defaultCloudLoginClient() CloudLoginClient {
	return cloudClientAdapter{c: cloudclient.New(cloudAPIURL())}
}
