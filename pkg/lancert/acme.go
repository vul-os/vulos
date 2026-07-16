package lancert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/acme"
)

// CryptoACMEClient is the production ACMEClient backed by golang.org/x/crypto/acme.
// It performs RFC 8555 DNS-01 issuance against any ACME directory (Let's Encrypt
// production / staging, Pebble for tests against a real directory if desired).
//
// The account key is generated on first use unless an existing one is supplied;
// callers SHOULD persist accountKey across restarts to avoid hitting the CA's
// new-account rate limits.
type CryptoACMEClient struct {
	client     *acme.Client
	contact    []string
	accountKey crypto.Signer
	registered bool
}

// CryptoACMEConfig configures CryptoACMEClient.
type CryptoACMEConfig struct {
	// DirectoryURL is the ACME directory endpoint, e.g. acme.LetsEncryptURL
	// for production. Required.
	DirectoryURL string

	// Contact is the list of mailto:/tel: URIs to register on the ACME
	// account. Optional but recommended.
	Contact []string

	// AccountKey is the ACME account key. If nil, a new ECDSA P-256 key is
	// generated. Callers should persist + reuse to respect CA rate limits.
	AccountKey crypto.Signer
}

// NewCryptoACMEClient returns a CryptoACMEClient ready to issue certs.
// Registration with the CA happens lazily on first NewOrder.
func NewCryptoACMEClient(cfg CryptoACMEConfig) (*CryptoACMEClient, error) {
	if cfg.DirectoryURL == "" {
		return nil, errors.New("lancert: ACME DirectoryURL required")
	}
	key := cfg.AccountKey
	if key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("lancert: generate account key: %w", err)
		}
		key = k
	}
	return &CryptoACMEClient{
		client: &acme.Client{
			DirectoryURL: cfg.DirectoryURL,
			Key:          key,
		},
		contact:    append([]string{}, cfg.Contact...),
		accountKey: key,
	}, nil
}

// cryptoOrderState is the opaque state returned by NewOrder and consumed by
// Finalize.
type cryptoOrderState struct {
	order     *acme.Order
	authzURL  string
	chalURL   string
	chalToken string
	fqdn      string
}

// NewOrder implements ACMEClient.
func (c *CryptoACMEClient) NewOrder(ctx context.Context, fqdn string) (string, any, error) {
	if fqdn == "" {
		return "", nil, ErrInvalidInput
	}
	if err := c.ensureAccount(ctx); err != nil {
		return "", nil, err
	}

	order, err := c.client.AuthorizeOrder(ctx, acme.DomainIDs(fqdn))
	if err != nil {
		return "", nil, fmt.Errorf("lancert: authorize order: %w", err)
	}
	if len(order.AuthzURLs) == 0 {
		return "", nil, errors.New("lancert: ACME returned order with no authorizations")
	}

	// Find the DNS-01 challenge in the first authorization.
	authz, err := c.client.GetAuthorization(ctx, order.AuthzURLs[0])
	if err != nil {
		return "", nil, fmt.Errorf("lancert: get authorization: %w", err)
	}
	var chal *acme.Challenge
	for _, ch := range authz.Challenges {
		if ch.Type == "dns-01" {
			chal = ch
			break
		}
	}
	if chal == nil {
		return "", nil, errors.New("lancert: CA did not offer dns-01 challenge")
	}

	// The DNS-01 record value is a SHA-256 hash of the key authorization,
	// base64url-encoded — DNS01ChallengeRecord does that for us.
	chalValue, err := c.client.DNS01ChallengeRecord(chal.Token)
	if err != nil {
		return "", nil, fmt.Errorf("lancert: build dns-01 record: %w", err)
	}

	st := &cryptoOrderState{
		order:     order,
		authzURL:  authz.URI,
		chalURL:   chal.URI,
		chalToken: chal.Token,
		fqdn:      fqdn,
	}
	return chalValue, st, nil
}

// Finalize implements ACMEClient.
func (c *CryptoACMEClient) Finalize(ctx context.Context, orderState any, csr []byte) ([][]byte, error) {
	st, ok := orderState.(*cryptoOrderState)
	if !ok || st == nil {
		return nil, errors.New("lancert: invalid orderState (wrong type)")
	}

	// Tell the CA we're ready for it to verify the DNS-01 challenge.
	if _, err := c.client.Accept(ctx, &acme.Challenge{URI: st.chalURL, Type: "dns-01", Token: st.chalToken}); err != nil {
		return nil, fmt.Errorf("lancert: accept challenge: %w", err)
	}

	// Wait for the authorization to be valid.
	if _, err := c.client.WaitAuthorization(ctx, st.authzURL); err != nil {
		return nil, fmt.Errorf("lancert: wait authorization: %w", err)
	}

	// Wait for order to be ready.
	order, err := c.client.WaitOrder(ctx, st.order.URI)
	if err != nil {
		return nil, fmt.Errorf("lancert: wait order: %w", err)
	}

	// Finalize: request the cert chain. The third arg "bundle"=true asks the
	// server to include intermediates.
	chain, _, err := c.client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, fmt.Errorf("lancert: create order cert: %w", err)
	}
	return chain, nil
}

// ensureAccount registers the account with the CA on first use.
func (c *CryptoACMEClient) ensureAccount(ctx context.Context) error {
	if c.registered {
		return nil
	}
	acct := &acme.Account{Contact: c.contact}
	if _, err := c.client.Register(ctx, acct, acme.AcceptTOS); err != nil {
		// "account already exists" is fine.
		if !errors.Is(err, acme.ErrAccountAlreadyExists) {
			return fmt.Errorf("lancert: ACME register: %w", err)
		}
	}
	c.registered = true
	return nil
}

// compile-time assertion
var _ ACMEClient = (*CryptoACMEClient)(nil)

// PropagationDelay is the conservative pause between publishing the DNS-01
// TXT record and asking the CA to verify it, to let public resolvers see the
// record. Override via Service.PropagationDelay.
const PropagationDelay = 30 * time.Second
