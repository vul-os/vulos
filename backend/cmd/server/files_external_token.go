package main

// files_external_token.go — adapts the cloud integration broker client to the
// Files external-store token seam (files.TokenSource). This is the only glue
// between the Files package and the integrations client: the Files service asks
// for a short-lived provider access token (e.g. "google" for Google Drive) and
// this returns one minted on demand from the CP broker. The refresh token never
// reaches the box; minted access tokens are short-lived and never persisted.

import (
	"context"
	"errors"

	"vulos/backend/services/integrations"
)

// filesIntegrationTokenSource implements files.TokenSource over the integration
// broker client. A "not connected" mint result is surfaced as an empty token so
// the Files service maps it to ErrExternalNotConnected (the account has not
// connected the provider CP-side).
type filesIntegrationTokenSource struct {
	c *integrations.Client
}

// MintToken forwards the mint request to the integration broker, now including
// userID (H3 fix: per-user token scoping).
func (t filesIntegrationTokenSource) MintToken(ctx context.Context, provider, userID string) (string, error) {
	tok, err := t.c.MintToken(ctx, provider, userID)
	if err != nil {
		if errors.Is(err, integrations.ErrNotConnected) {
			return "", nil // → ErrExternalNotConnected upstream
		}
		return "", err
	}
	return tok.AccessToken, nil
}
