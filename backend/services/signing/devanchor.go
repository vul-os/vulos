package signing

// devanchor.go — the well-known DEVELOPMENT trust anchor.
//
// The repo ships a checked-in trust anchor (keys/trust-anchor.pub) plus a
// root-signed release cert (keys/release-cert.json) so that a fresh clone can
// verify the committed registry.json signatures with no flags and no key
// material to fetch.  Those keys are DERIVED FROM A PUBLISHED SEED — see
// docs/KEY-CEREMONY.md § "The dev keypair".  Anyone can regenerate the dev
// *private* key in seconds.  It is therefore not a secret, and it must never
// be treated as one.
//
// That is safe only if the dev anchor is structurally incapable of being
// trusted in production.  DevAnchorPubB64 pins its exact public key so
// production code can recognise and refuse it, rather than relying on operator
// discipline or a build-time warning that nobody reads.  A real deployment
// replaces keys/trust-anchor.pub with the pubkey from an offline root ceremony;
// until it does, VULOS_ENV=prod refuses to install anything.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

// DevAnchorPubB64 is the base64-encoded Ed25519 public key of the repo's
// development ROOT key — the key behind keys/trust-anchor.pub.
//
// DevReleasePubB64 is the development RELEASE key that the dev root certifies
// in keys/release-cert.json; it is the key that actually signed the committed
// registry.json entries.  Both are pinned because both must be refused in prod:
// an operator who set VULOS_REGISTRY_PUBKEY to the dev release key directly
// would otherwise sidestep the anchor check entirely.
//
// Both are derived deterministically from the published seeds below (see
// `vulos-sign gen-key -dev-seed`), so they are reproducible by anyone and
// secret to no one.
const (
	DevAnchorPubB64  = "98ZXaIIl75yJ/2TKAJqPGSbfRP0YGJWKOOP3z1OTLSI="
	DevReleasePubB64 = "uosei+A8s83COsu0CBIjmCf4H4PpxXaTQJar7rUafwE="
)

// DevRootSeed and DevReleaseSeed are the published seed strings from which the
// development root and release keys are derived.  They are documented, not
// hidden: the dev keys exist so tests and `make dev` work offline, and their
// only defence is DevAnchorPubB64 refusal in prod.
const (
	DevRootSeed    = "vulos-dev-signing-root-v1"
	DevReleaseSeed = "vulos-dev-signing-release-v1"
)

// ErrDevKeyInProd is returned when a development key is presented to a
// production code path.
var ErrDevKeyInProd = errors.New(
	"signing: a DEVELOPMENT signing key is configured but VULOS_ENV=prod — " +
		"its private key is derived from a published seed, so anyone can forge signatures with it. " +
		"Run the offline key ceremony (docs/KEY-CEREMONY.md), install the resulting root public key " +
		"at " + DefaultAnchorPath + ", and re-sign the registry with the release key")

// IsDevKey reports whether pub is one of the well-known development keys (the
// dev root/trust anchor, or the dev release key it certifies).
//
// The comparison is constant-time out of habit, not necessity — these keys are
// public — so the helper cannot become a timing oracle if it is ever reused
// against a secret.
func IsDevKey(pub ed25519.PublicKey) bool {
	for _, b64 := range []string{DevAnchorPubB64, DevReleasePubB64} {
		want, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(pub) != len(want) {
			continue
		}
		if subtle.ConstantTimeCompare(pub, want) == 1 {
			return true
		}
	}
	return false
}

// RefuseDevKeyInProd returns ErrDevKeyInProd when isProd is true and pub is a
// development key.  Callers pass their already-resolved environment so this
// package stays free of env-var lookups.
func RefuseDevKeyInProd(pub ed25519.PublicKey, isProd bool) error {
	if isProd && IsDevKey(pub) {
		return ErrDevKeyInProd
	}
	return nil
}

// DeriveDevKey returns the deterministic Ed25519 keypair for a published dev
// seed (DevRootSeed / DevReleaseSeed).  It exists so that the key-generation
// tool and the tests derive the dev keys the same way, from one definition.
//
// NEVER call this to produce a key that will sign anything a real user trusts.
func DeriveDevKey(seed string) (ed25519.PublicKey, ed25519.PrivateKey) {
	sum := sha256.Sum256([]byte(seed))
	priv := ed25519.NewKeyFromSeed(sum[:])
	return priv.Public().(ed25519.PublicKey), priv
}
