# Graceful degradation contract (RISK-SPOF-01, part 2 of 2)

When the control plane (CP) is fully down (network partition, double-fault,
all replicas dead), boxes MUST keep serving from cached state. This document
is the canonical inventory of:

1. What state a box needs at request time,
2. What it needs only occasionally and can cache,
3. What it needs from the CP but can defer / queue.

Treat anything not on the "request-time cached" list as a soft dependency:
if the CP is unreachable, the box logs a warning and degrades, but it MUST
NOT 5xx steady-state user requests.

---

## Tier 1 — cached at request time (CP-DOWN-SAFE)

These are the items a box already keeps locally and consults on every request.
The CP being down is invisible to users for these.

| State                       | Where it lives on the box                 | Refresh mechanism                       | OSS task |
|-----------------------------|-------------------------------------------|-----------------------------------------|----------|
| Resolver target (rendezvous URL + identity host) | OS-level cache, refreshed periodically when CP is up | OFFLINE-02 (resolver cache, bounded TTL) | OFFLINE-02 |
| LANCERT TLS cert + key      | Disk path under the box state dir         | OFFLINE-01 (file-watcher + hot reload)  | OFFLINE-01 |
| Session tokens (own users)  | Local auth store, signed/validated locally | OFFLINE-AUTH-01 (bounded local TTL; CP revalidates async) | OFFLINE-AUTH-01 |
| Identity records (own org)  | Local CRDT replica synced from rendezvous | local CRDT (last-known-good)            | SYNC-RENDEZVOUS-01 |
| BYO storage token           | Local secret store, env-injected at start | bounded TTL; rotated when CP up         | BYO-TOKEN-CACHE-01 |
| Box-local routing table     | Local SQLite (`fabric`, `routing`)         | hot reload + last-known-good            | (already local) |
| Tenant config (org-level)   | Local CRDT replica                          | last-known-good                         | (already local) |

Rule of thumb: **steady-state HTTP/SMTP/IMAP/web traffic on the box MUST
work with the CP socket closed.**

---

## Tier 2 — needed occasionally; degrades gracefully

When the CP is down, these features stop working for new requests, but
existing sessions continue.

| Endpoint / operation               | Behaviour when CP down                                                       |
|------------------------------------|------------------------------------------------------------------------------|
| New device enrollment              | UI shows "enrollment temporarily unavailable; try again in a minute"         |
| New account signup (cloud)         | UI shows retry banner; queues the signup intent locally for replay           |
| LANCERT cert renewal               | Box keeps serving with current cert until 7d before expiry, then warns       |
| Billing UI                         | Read-only from local cache; payment writes return 503 with retry hint        |
| Wallet top-up                      | Return 503; do NOT silently fail                                              |
| OTA channel check                  | Skip this cycle; retry on the next scheduled tick                            |
| DNS plane updates                  | Skip; queue the desired-state change locally and reconcile on CP return      |
| SuperAdmin / fleet ops             | Unavailable (admin tooling — acceptable to fail loudly)                      |
| Support / telemetry ingest         | Buffer locally on the box; flush on CP return                                |

---

## Tier 3 — write-defer / queue (eventual consistency)

These are CP-initiated writes the box can queue locally and replay when the
CP becomes reachable again. They MUST NOT block the box's serving path.

| Operation                          | Local queue                               | Replay on reconnect                       |
|------------------------------------|-------------------------------------------|-------------------------------------------|
| Mail delivery report ack           | `byohealth`-style ring buffer             | flush on first successful CP heartbeat    |
| Quota counter increments           | local counters; reconciled at CP return   | CP rebuilds authoritative counter         |
| Audit / telemetry events           | local NDJSON spool                        | flush on first successful CP heartbeat    |
| Abuse report submissions           | local file under state dir                | retry with exponential backoff            |
| Backup manifest publish            | hold the manifest; publish on CP return   | idempotent (content-addressed)            |

---

## Detection + signalling

The box detects "CP down" the same way any HTTP client does: requests to
`/api/*` time out or return 5xx. The box-side resolver SHOULD use the HA
health endpoint exposed by this package (`GET /api/ha/health`) to decide
which CP replica to talk to next:

- **200 OK** → current leader; route writes here.
- **503 Service Unavailable** → standby replica; the body still includes
  the cluster snapshot. Boxes MAY use this for read-only operations or
  failover discovery.
- **Connection refused / DNS NXDOMAIN / timeout** → CP fully unreachable;
  degrade per the tiers above. Continue serving.

DNS-level failover (multi-A or weighted DNS pointed at the published
addresses) is the operational complement; see `/docs/RISKS-HUMAN-TEAM.md`
§3 for the rollout plan.

---

## What this package owns vs. what the box owns

- **This package (`internal/ha`):** active-passive lease + health endpoint.
  That is the *server* side of the failover story.
- **The box (OSS):** all caches in Tier 1 + queues in Tier 3. These are
  tracked under their own task IDs (OFFLINE-01, OFFLINE-02,
  OFFLINE-AUTH-01, BYO-TOKEN-CACHE-01) and are required for the
  graceful-degradation acceptance criteria to be met end-to-end.

The CP failover code without box-side caching still leaves a SPOF; the
box-side caching without CP failover still loses writes for the duration
of an outage. Both halves ship together.
