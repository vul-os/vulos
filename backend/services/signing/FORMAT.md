# Vulos Signing Format

This document specifies the canonical-byte serialisation rules for Vulos OS
manifests (`stable.json`) and the detached `.sig` file format used to carry
Ed25519 signatures over those manifests and OS image artifacts.

Implemented by `backend/services/signing/`.

---

## 1. Canonical Bytes

The signing surface for a manifest is its **canonical JSON encoding** — a
deterministic, whitespace-free serialisation where object keys are sorted
lexicographically at every level of nesting.

### Rules

| Rule | Detail |
|------|--------|
| **Key sorting** | Object keys are sorted byte-lexicographically (Go `sort.Strings`) at every nesting level. |
| **No insignificant whitespace** | No spaces, tabs, or newlines outside of string values. |
| **No trailing newline** | The byte sequence ends immediately after the closing `}` or `]`. |
| **Number representation** | Numbers are serialised as Go `json.Number` (no transformation). Integer fields stay integers; no float coercion. |
| **Unicode strings** | Strings are serialised using standard `encoding/json` UTF-8 escaping. |
| **Byte-stable** | Two Go values that are `reflect.DeepEqual` (or logically equal JSON) produce identical bytes regardless of run, OS, or map-iteration order. |

### Why not JCS (RFC 8785)?

JCS requires Unicode normalisation and specific number formatting that adds
external dependencies. Our manifests are purely ASCII-safe JSON produced by Go;
the simpler sorted-keys rule is sufficient and avoids any dependency beyond
`encoding/json`.

### Example

Given the `stable.json` shape from `roadmap/OS-DISTRIBUTION.md`:

```json
{
  "channel": "stable",
  "latest": "v08",
  "min_epoch": 3,
  "path": "os/v08/os-core.squashfs",
  "released_at": "2026-05-20T09:00:00Z",
  "roothash": "<dm-verity root hash>",
  "size": 734003200
}
```

Canonical bytes (keys sorted, no whitespace):

```
{"channel":"stable","latest":"v08","min_epoch":3,"path":"os/v08/os-core.squashfs","released_at":"2026-05-20T09:00:00Z","roothash":"<dm-verity root hash>","size":734003200}
```

---

## 2. Detached `.sig` File Format

A `.sig` file is a plain-text, newline-terminated file carrying a single
Ed25519 signature over the canonical bytes of the signed artifact.

### Structure

```
vulos-sig-v1
algorithm: ed25519
key-id: <opaque key identifier>
sig: <base64-standard-encoded 64-byte signature>
```

- Line 1 is always `vulos-sig-v1` — the magic/version header.
- Lines 2–4 are `key: value` pairs (colon-space separated, leading/trailing
  space trimmed from the value).
- `algorithm` — always `ed25519` for this version.
- `key-id` — an opaque, human-readable string identifying the signing key (e.g.
  a hex fingerprint, a label, or a certificate serial number). Must not contain
  newlines. **Not validated during signature verification**; present for
  operational/audit use only.
- `sig` — the raw 64-byte Ed25519 signature base64-encoded with standard
  (RFC 4648) encoding (no URL-safe alphabet, padding required).

### Signing workflow

```
canonical_bytes = Canonical(manifest)   // sorted-keys, no whitespace
sig_bytes       = ed25519.Sign(priv, canonical_bytes)
sig_file        = MarshalSig(Signature{Algorithm: "ed25519", KeyID: keyID, SigBytes: sig_bytes})
```

### Verification workflow

```
canonical_bytes = Canonical(manifest)
sig             = ParseSig(sig_file_bytes)
ok              = ed25519.Verify(pub, canonical_bytes, sig.SigBytes)
```

The verifier does **not** use `sig.KeyID` to look up the key; the public key
must be supplied by the caller (retrieved from the baked trust anchor or a
root-signed release-key certificate — see `roadmap/SIGNING.md`).

### Artifact coverage

| Artifact | Signed over |
|----------|-------------|
| `stable.json` | `Canonical(stable.json contents)` |
| `os-core.squashfs` | raw bytes of the squashfs image (not JSON; the `signing` package's `Sign`/`Verify` work over arbitrary `[]byte`) |

### No key custody

This package (`backend/services/signing/`) provides **only** the format and
primitives.  Private-key generation and storage are handled by separate packages
(SEED-01, SIGN-03).  Callers pass `ed25519.PrivateKey` / `ed25519.PublicKey`
values directly.
