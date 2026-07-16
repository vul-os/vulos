package oauthprovider

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// signing.go — RS256 JWT signing + JWKS, implemented with the standard library
// only (no third-party JWT dependency). The provider generates one RSA-2048
// keypair on first boot and persists it (PEM) so id_tokens survive restarts.

// SigningKey is an in-memory RSA keypair plus its key id.
type SigningKey struct {
	KID     string
	Private *rsa.PrivateKey
}

// generateSigningKey mints a fresh RSA-2048 keypair with a random kid.
func generateSigningKey() (*SigningKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: generate rsa key: %w", err)
	}
	kid, err := randomToken(8)
	if err != nil {
		return nil, err
	}
	return &SigningKey{KID: kid, Private: priv}, nil
}

// encodePEM renders the key as (privatePKCS8PEM, publicPKIXPEM).
func (k *SigningKey) encodePEM() (privPEM, pubPEM string, err error) {
	privDER, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return "", "", fmt.Errorf("oauthprovider: marshal private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&k.Private.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("oauthprovider: marshal public key: %w", err)
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}))
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privPEM, pubPEM, nil
}

// parseSigningKey rebuilds a SigningKey from its kid + PKCS#8 private PEM.
func parseSigningKey(kid, privPEM string) (*SigningKey, error) {
	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		return nil, errors.New("oauthprovider: invalid private key PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: parse pkcs8: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("oauthprovider: signing key is not RSA")
	}
	return &SigningKey{KID: kid, Private: rsaKey}, nil
}

// b64url is base64url without padding (the JWT/JWK alphabet).
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// IDTokenClaims are the claims placed in an OIDC id_token. Optional claims are
// omitted when empty so we never assert an email we don't have.
type IDTokenClaims struct {
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	Audience      string `json:"aud"`
	Expiry        int64  `json:"exp"`
	IssuedAt      int64  `json:"iat"`
	AuthTime      int64  `json:"auth_time,omitempty"`
	Nonce         string `json:"nonce,omitempty"`
	Email         string `json:"email,omitempty"`
	EmailVerified *bool  `json:"email_verified,omitempty"`
}

// SignIDToken produces a signed RS256 JWT for the given claims.
func (k *SigningKey) SignIDToken(claims IDTokenClaims) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.KID}
	hJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(hJSON) + "." + b64url(cJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.Private, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("oauthprovider: sign id_token: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}

// VerifyIDToken verifies an RS256 JWT signature against pub and returns the raw
// claims JSON. Intended for tests and internal validation; it checks the
// signature and exp, not audience/issuer (callers assert those).
func VerifyIDToken(token string, pub *rsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("oauthprovider: malformed JWT")
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: decode signature: %w", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("oauthprovider: bad signature: %w", err)
	}
	cJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: decode claims: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(cJSON, &claims); err != nil {
		return nil, fmt.Errorf("oauthprovider: unmarshal claims: %w", err)
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().Unix() > int64(exp) {
		return nil, errors.New("oauthprovider: id_token expired")
	}
	return claims, nil
}

// JWK is a single JSON Web Key (RSA public key) for the JWKS document.
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// PublicJWK renders the signing key's public half as a JWK.
func (k *SigningKey) PublicJWK() JWK {
	pub := k.Private.PublicKey
	return JWK{
		Kty: "RSA",
		Use: "sig",
		Alg: "RS256",
		Kid: k.KID,
		N:   b64url(pub.N.Bytes()),
		E:   b64url(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// RSAPublicKey reconstructs the rsa.PublicKey from a JWK. Used in tests and
// by relying parties that verify id_tokens after fetching the JWKS document.
func (j JWK) RSAPublicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: decode JWK n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: decode JWK e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, errors.New("oauthprovider: JWK e is too large")
	}
	pub := &rsa.PublicKey{N: n, E: int(e.Int64())}
	return pub, nil
}
