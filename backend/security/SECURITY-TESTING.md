# Vulos OS — Security / Pentest Test Suite (PENTEST-OS-01)

This package is the attacker-style red-team layer for the Vulos OS backend
(`backend/`, pure-Go, `modernc.org/sqlite`, **no CGO**). Every pentest test
**ATTEMPTS a concrete attack against a real production entrypoint** and
**ASSERTS the attack is blocked**. A failing pentest is not flaky — it is a
real, reportable vulnerability. Do not weaken an assertion to make it pass.

## How to run

```bash
cd backend
CGO_ENABLED=0 go test ./security/...            # this suite
CGO_ENABLED=0 go test ./security/... -v         # with per-attack names
CGO_ENABLED=0 go test ./security/... -run Pentest_   # only the pentest layer
```

Gate (must be all-green):

```bash
cd backend
CGO_ENABLED=0 go build ./... && \
CGO_ENABLED=0 go vet ./...   && \
CGO_ENABLED=0 go test ./... 2>&1 | tail -15
```

The pentest files are `*_pentest_test.go` in package `security`. They are
black/grey-box: they import the real packages (`internal/lan`, `internal/fabric`,
`internal/multiinstance`, `services/webproxy`, `services/auth`) and drive the
production entrypoints over real `httptest` TLS listeners / the real auth
middleware / the real CRDT merge — not mocks.

## Attack-class coverage

| # | Attack class | File | Tests |
|---|--------------|------|-------|
| 1 | LAN cert is **not** a CA (no `KeyUsageCertSign`, `IsCA==false`) | `lan_cert_pentest_test.go` | `TestPentest_LANCertIsNotACA` |
| 2 | LAN-cert puller / first-boot MITM (refuse plaintext CP URL; reject unpinned/mismatched cert; wrong SPKI + wrong CA pins rejected; secret never leaks) | `lan_cert_pentest_test.go` | `TestPentest_PullerRefusesPlaintextControlPlane`, `TestPentest_PinnedPullerRejectsMITMCert`, `TestPentest_WrongSPKIPinRejected`, `TestPentest_WrongCAPinRejected` |
| 3 | Fabric peer auth — GET/POST `/api/fabric/changeset` reject missing/empty/wrong `X-Fabric-Auth` (401); hostile unauth push not merged; listener is genuinely TLS | `fabric_crdt_pentest_test.go` | `TestPentest_FabricRejectsBadAuth`, `TestPentest_FabricUnauthPushNotMerged`, `TestPentest_FabricListenerIsTLS` |
| 4 | CRDT injection / convergence — forged changeset can't diverge state; a single peer can't force an uninstall by **inflating** OR **deflating** its self-reported count, **nor by minting many DISTINCT forged origins**, **nor with an unknown/unsigned/bad-signature origin**; stale observations can't re-quorum after a re-install; the legitimate signed multi-origin uninstall still converges; deterministic merge is order-independent | `fabric_crdt_pentest_test.go` | `TestPentest_QuorumDeflationCannotForceUninstall`, `TestPentest_QuorumInflationCannotForceUninstall`, `TestPentest_MultipleForgedOriginsCannotForceUninstall`, `TestPentest_UnknownUnsignedBadSigOriginRejected`, `TestPentest_StaleObservationsCannotRequorumAfterReinstall`, `TestPentest_LegitimateSignedMultiOriginUninstallConverges`, `TestPentest_ForgedChangesetConvergesDeterministically` |
| 5 | mDNS / LAN bind — HTTPS listener + DNS responder pin the LAN IP, not `0.0.0.0`/`::` (not internet-exposed) | `lan_bind_pentest_test.go` | `TestPentest_LANListenersDoNotBindAllInterfaces` |
| 6 | SSRF via the web proxy — blocks loopback / RFC1918 / link-local / cloud-metadata / CGNAT / 6to4-private and obfuscated literals (decimal, hex, octal, IPv4-mapped-IPv6, bracketed IPv6); blocks loopback port-scan | `ssrf_webproxy_pentest_test.go` | `TestPentest_WebProxyBlocksInternalIPLiterals` (17 sub-cases), `TestPentest_WebProxyBlocksMetadataNoScheme`, `TestPentest_WebProxyBlocksPortScanOfLoopback` |
| 7 | Multi-instance provisioning auth — `POST /api/instances/provision`, `GET /api/instances/{ulid}/status`, `GET /api/instances/{ulid}/apps` require auth (no unauth enroll/forge/enumerate); spoofed identity headers + forged bearer/device tokens rejected | `multiinstance_provision_pentest_test.go` | `TestPentest_ProvisionRequiresAuth`, `TestPentest_InstanceRoutesRequireAuth`, `TestPentest_ForgedSessionTokenRejected` |

The pre-existing `auth_middleware_security_test.go` (C1/SEC-A header-spoof,
AUTH-10 admin bearer, protected-route enforcement) remains in this package and
runs alongside the pentest layer.

Test count: **28 top-level tests** in `./security/` (22 pentest + 6 auth
regression), **45 including sub-cases**.

---

## FINDINGS register

### CRDT-QUORUM-01 — uninstall quorum forgeable via self-asserted origins (FIXED)

**Severity:** Medium (LAN-scoped; requires a peer that already holds the shared
`X-Fabric-Auth` secret, OR a cloud relay changeset deposit). Not a remote-unauth
issue — the fabric auth gate (attack class #3) still holds.

**Where:** `internal/multiinstance/appsync.go` — uninstall quorum / observation set.

**History.** The first fix replaced the trusted self-reported `AppChangeset.PeerCount`
with a count of **distinct originating instances** (`app_uninstall_observations`,
keyed by `observer_ulid`). That closed the single-`PeerCount=99` attack but left a
**bigger hole**: `observer_ulid` was the **self-asserted `cs.OriginULID`**, never
bound to the authenticated sender. Because `VULOS_FABRIC_SECRET` is a **single
shared bearer secret** identical on every box, ONE authenticated peer could submit
N changesets each carrying a DIFFERENT fake origin (`fake-1`,`fake-2`,…); each
distinct string wrote one observation row and `COUNT(*)` reached quorum →
**forced uninstall**. The original pentest only replayed the SAME origin, so it
missed this.

**The real fix — per-instance signed identity.** A shared secret cannot
distinguish peers, so quorum cannot rely on it. Each box now has its own
**Ed25519 keypair** (`LoadOrCreateInstanceKey`, persisted under the data dir);
its public key is published into the registry **roster** (the existing
`instances.ed25519_public_key` column). `EmitChangeset` **signs** every emitted
changeset over a canonical `(domain, origin, {uninstall entries})` message. On
receipt, `ApplyChangeset` records an uninstall observation **only when** the
origin is a **known rostered peer** AND the signature **verifies against that
peer's rostered key** (`verifyChangesetSignature`). A box's own origin is trusted
implicitly. Unknown / unsigned / badly-signed origins contribute **nothing**.
Quorum counts distinct **VERIFIED rostered** origins — so a single secret-holder
can validly sign as only ITSELF (one origin) and can never reach a >2-node
majority by forging origins.

**Observation-set GC (MEDIUM, also fixed).** Observations are tagged with the
app's install **generation/epoch** (`app_install_generation`). A (re)install bumps
the generation; quorum counts only **current-generation** observations. A
re-install therefore GCs stale rows, so a later uninstall must gather **fresh**
observations and cannot re-quorum off pre-reinstall rows.

**Proven by (now HARD assertions, gate-failing):**
- `TestPentest_MultipleForgedOriginsCannotForceUninstall` — one peer mints 10
  distinct forged origins (and unsigned changesets claiming rostered ids); none
  count; `installed` stays true. (This is the attack the old suite missed.)
- `TestPentest_UnknownUnsignedBadSigOriginRejected` — unknown-but-signed,
  rostered-but-unsigned, and rostered-with-wrong-key are each rejected.
- `TestPentest_QuorumInflationCannotForceUninstall` / `…Deflation…` — the
  self-reported `PeerCount` plays no part.
- `TestPentest_StaleObservationsCannotRequorumAfterReinstall` — epoch GC.
- `TestPentest_LegitimateSignedMultiOriginUninstallConverges` — the GOOD path:
  two DISTINCT real instances with rostered keys still reach quorum and converge.

**Honest residual risks / open items:**
- **Key distribution / bootstrap trust.** Peers learn each other's pubkeys via the
  roster (cloud enrollment `cloudsync` already carries `ed25519_public_key`, and a
  box self-publishes via `SetIdentity`). A box that has not yet learned a peer's
  key will not count that peer's observations until the roster converges — fail
  **closed** for quorum (no false removal), at the cost of slower legitimate
  convergence on first contact. There is no TOFU pinning yet; roster integrity
  relies on the (authenticated) enrollment / cloud path.
- **Key rotation.** Rotating a box's key invalidates in-flight signed observations
  and forces peers to re-learn the key; there is no rotation/revocation protocol
  yet. The key is persisted and meant to be stable.
- **Key at rest.** `LoadOrCreateInstanceKey` stores the seed `0600` UNENCRYPTED in
  the data dir (same class as other box secrets). An at-rest-encrypted variant
  (OS-keyring-wrapped, like `internal/identity`) is a follow-up.

---

## Notes for maintainers

- All tests run with `CGO_ENABLED=0` (pure-Go SQLite via `modernc.org/sqlite`).
- The LAN-cert MITM tests **mint distinct self-signed certs** per server
  (`mintLoopbackCert`) instead of `httptest.NewTLSServer`, because the latter
  reuses one baked-in cert for every server — which would make a pinning test
  pass for the wrong reason. If you refactor those helpers, preserve the
  distinct-cert property or the MITM assertion becomes vacuous.
- `internal/lan` exposes `(*LANCertPuller).ProbeOnce(ctx)` purely as a security
  test seam so the pentest can assert handshake rejection without running the
  full background `Run` loop.
