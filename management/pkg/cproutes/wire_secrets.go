// wire_secrets.go — SECRETS_WIRE: SecretsProvider for the control plane
// (RISK-SEC-01).
//
// The provider is selected at startup by CP_KMS_PROVIDER (env|vault). With
// the default env-backed impl the behaviour of every migrated call-site is
// identical to direct os.Getenv — set the env var to "vault" + CP_KMS_URL +
// CP_KMS_TOKEN to switch the source.
package cproutes

import (
	"context"
	"crypto/subtle"

	"github.com/vul-os/vulos-management/pkg/secrets"
)

// previousSecretSuffix is appended to a secret's env key to find its rotation
// predecessor. CP_SHARED_SECRET → CP_SHARED_SECRET_PREVIOUS.
const previousSecretSuffix = "_PREVIOUS"

// secretsProv is the process-wide SecretsProvider. Nil until initSecrets()
// runs; nil-safe via secrets.GetOrEmpty (falls back to os.Getenv).
var secretsProv secrets.SecretsProvider

// initSecrets selects the SecretsProvider based on CP_KMS_PROVIDER and
// stores it in the package-level secretsProv variable. Subsequent call-sites
// migrated to the provider read through secrets.GetOrEmpty(ctx, secretsProv,
// key) so the env-backed default is a no-op behaviour change.
func initSecrets() secrets.SecretsProvider {
	secretsProv = secrets.Default()
	return secretsProv
}

// secretOrEnv reads key via the wired SecretsProvider, falling back to
// os.Getenv when the provider is unset (defence-in-depth for early-init
// code-paths). Empty values collapse to "" — same as os.Getenv.
func secretOrEnv(ctx context.Context, key string) string {
	return secrets.GetOrEmpty(ctx, secretsProv, key)
}

// SECRET-ROTATE-01: dual-key rotation.
//
// During a secret rotation the operator sets the NEW value on CP_SHARED_SECRET
// and keeps the OLD value on CP_SHARED_SECRET_PREVIOUS. Both are accepted, so
// PoPs/devices can roll to the new secret without a flag-day.

// secretCandidates returns the accepted values for key during a rotation
// window: the current key value first, then key+"_PREVIOUS" if set. Empty
// values are skipped.
func secretCandidates(ctx context.Context, key string) []string {
	out := make([]string, 0, 2)
	if cur := secretOrEnv(ctx, key); cur != "" {
		out = append(out, cur)
	}
	if prev := secretOrEnv(ctx, key+previousSecretSuffix); prev != "" {
		out = append(out, prev)
	}
	return out
}

// hasSharedSecret reports whether at least one CP_SHARED_SECRET candidate
// (current or previous) is configured. Used by fail-closed gates that must
// 503 when no secret is set at all.
func hasSharedSecret(ctx context.Context) bool {
	return len(secretCandidates(ctx, "CP_SHARED_SECRET")) > 0
}

// validSharedSecretEither reports whether presented matches the current OR the
// previous CP_SHARED_SECRET, using a constant-time compare against each
// candidate (timing-safe). Returns false when no candidate is configured or
// presented is empty.
func validSharedSecretEither(ctx context.Context, presented string) bool {
	if presented == "" {
		return false
	}
	ok := false
	for _, cand := range secretCandidates(ctx, "CP_SHARED_SECRET") {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(cand)) == 1 {
			ok = true
			// Do NOT break: keep comparing so total work is independent of
			// which candidate matched (defence against trivial timing oracles).
		}
	}
	return ok
}
