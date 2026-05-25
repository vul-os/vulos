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
| 4 | CRDT injection / convergence — forged changeset can't diverge state; single peer can't force uninstall by **deflating** count; deterministic merge is order-independent | `fabric_crdt_pentest_test.go` | `TestPentest_QuorumDeflationCannotForceUninstall`, `TestPentest_ForgedChangesetConvergesDeterministically`, `TestPentest_QuorumInflationDOCUMENTS_LiveFinding` (see FINDINGS) |
| 5 | mDNS / LAN bind — HTTPS listener + DNS responder pin the LAN IP, not `0.0.0.0`/`::` (not internet-exposed) | `lan_bind_pentest_test.go` | `TestPentest_LANListenersDoNotBindAllInterfaces` |
| 6 | SSRF via the web proxy — blocks loopback / RFC1918 / link-local / cloud-metadata / CGNAT / 6to4-private and obfuscated literals (decimal, hex, octal, IPv4-mapped-IPv6, bracketed IPv6); blocks loopback port-scan | `ssrf_webproxy_pentest_test.go` | `TestPentest_WebProxyBlocksInternalIPLiterals` (17 sub-cases), `TestPentest_WebProxyBlocksMetadataNoScheme`, `TestPentest_WebProxyBlocksPortScanOfLoopback` |
| 7 | Multi-instance provisioning auth — `POST /api/instances/provision`, `GET /api/instances/{ulid}/status`, `GET /api/instances/{ulid}/apps` require auth (no unauth enroll/forge/enumerate); spoofed identity headers + forged bearer/device tokens rejected | `multiinstance_provision_pentest_test.go` | `TestPentest_ProvisionRequiresAuth`, `TestPentest_InstanceRoutesRequireAuth`, `TestPentest_ForgedSessionTokenRejected` |

The pre-existing `auth_middleware_security_test.go` (C1/SEC-A header-spoof,
AUTH-10 admin bearer, protected-route enforcement) remains in this package and
runs alongside the pentest layer.

Test count: **24 top-level tests** in `./security/` (18 pentest + 6 auth
regression), **41 including sub-cases**.

---

## FINDINGS register

### CRDT-QUORUM-01 — uninstall quorum trusts the self-reported peer count (LIVE, FLAGGED)

**Severity:** Medium (LAN-scoped; requires a peer that already holds the shared
`X-Fabric-Auth` secret, OR a cloud relay changeset deposit). Not a remote-unauth
issue — the fabric auth gate (attack class #3) still holds.

**Where:** `internal/multiinstance/appsync.go` → `quorumOK(localPeerCount, remotePeerCount)`.

**What:** In a registry with **>2 instances**, the uninstall-quorum check is:

```go
func (as *AppSync) quorumOK(localPeerCount, remotePeerCount int) bool {
    if localPeerCount <= 2 { return true }   // local count gates WHETHER quorum applies
    return remotePeerCount >= 2              // …but corroboration trusts the WIRE value
}
```

The locally-observed peer count only decides **whether** quorum is required; the
corroboration (`remotePeerCount >= 2`) reads the **attacker-supplied**
`AppChangeset.PeerCount`. A single malicious peer can therefore set
`PeerCount: 99` on a strictly-newer uninstall (or a first-seen uninstall
tombstone) and **force the removal of an app on its own**, or **pre-seed a
tombstone** that suppresses a later legitimate install. The package doc comment
claims this is defended ("cannot force a removal by inflating PeerCount") — the
implementation does not fully uphold that claim.

**Proven by:** `TestPentest_QuorumInflationDOCUMENTS_LiveFinding` — it installs
an app in a 3-instance registry, then applies a single inflated-`PeerCount`
uninstall and observes `installed=false` (removal forced). The test
**documents** the finding via `t.Logf` and does **not** hard-fail the gate
(see below).

**What IS protected (and tested):**
- Deflation (`PeerCount < 2`) under a >2-instance regime fails quorum →
  `TestPentest_QuorumDeflationCannotForceUninstall`.
- A 2-node system needs no quorum (cannot form a majority) — local count is
  authoritative there.
- The merge is otherwise deterministic / order-independent →
  `TestPentest_ForgedChangesetConvergesDeterministically`.

**Why it is FLAGGED, not auto-fixed:** the legitimate uninstall-propagation path
deliberately accepts a self-reported `PeerCount >= 2` as sufficient quorum — the
existing, passing unit test `internal/multiinstance/appsync_test.go ::
TestUninstallQuorumMet` asserts exactly that. A correct fix must distinguish an
honest "I observed 2 peers" from a lying "99", which a single wire value cannot
do. It requires the receiver to **accumulate uninstall observations from
DISTINCT origins over time** (count corroborating peers locally) before honoring
a >2-node removal — a CRDT protocol change, not a one-line edit. Making that
change blindly would break legitimate uninstall convergence and the documented
merge semantics, so per the engagement rule ("fix if small, or flag
PROMINENTLY") it is flagged here for a deliberate follow-up.

**Recommended fix (follow-up task):** replace the self-reported corroboration
with a locally-derived one: persist per-`(instance_ulid, app_id)` the set of
distinct origins that have reported `installed=false` with a newer timestamp;
accept the uninstall only once that set size meets the local-majority threshold
derived from the registry roster. Keep `localPeerCount` as the regime gate.

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
