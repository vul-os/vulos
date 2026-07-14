# `keys/` — signing trust material

**Everything committed in this directory is a PUBLIC key. Nothing secret lives
here, and nothing secret ever should.** See [docs/KEY-CEREMONY.md](../docs/KEY-CEREMONY.md).

| File | Committed | Shipped in the image | What it is |
|---|---|---|---|
| `trust-anchor.pub` | yes | `/etc/vulos/trust-anchor.pub` | The offline **ROOT public key**. The single anchor every trust decision chains to. |
| `release-cert.json` | yes | `/etc/vulos/release-cert.json` | Root-signed cert authorising the **RELEASE key** that signs `registry.json` and OS manifests. |
| `root.pub.json` | yes | — | Root public key, tool format. |
| `release.pub.json` | yes | — | Release public key, tool format. |
| `*.priv.json` | **NEVER** | — | Private keys. Gitignored. CI asserts none are tracked. |

## These are currently the DEV keys

The committed anchor and cert are **development** keys, derived from published
seeds (`vulos-dev-signing-root-v1`, `vulos-dev-signing-release-v1`). Anyone can
reproduce their private halves. They exist so a fresh clone can verify the
committed `registry.json` signatures offline with no flags.

They are safe to ship *only* because they cannot be trusted in production:
`signing.RefuseDevKeyInProd` refuses them whenever `VULOS_ENV=prod` — which is
also the default when `VULOS_ENV` is unset. A prod box carrying these keys
refuses every app install, loudly, rather than trusting a forgeable signature.

```bash
make dev-keys        # regenerate (deterministic — byte-identical every time)
make verify-registry # check every registry.json entry against the anchor here
```

## Going to production

Run the ceremony in [docs/KEY-CEREMONY.md](../docs/KEY-CEREMONY.md): generate an
offline root key, have it certify a release key, replace the four public files
above with the ceremony output, and re-sign the registry:

```bash
make sign-registry RELEASE_PRIV=/path/to/release.priv.json
```

Until that is done, `VULOS_ENV=prod` boxes will refuse to install apps. That is
the intended behaviour — a box that cannot verify should not install.
