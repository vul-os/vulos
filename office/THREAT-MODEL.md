# Threat Model — Vulos Office

STRIDE pass. Last updated: 2026-05-23.

---

## Scope and Trust Boundaries

```
[Browser clients (multiple users)]
        |
        v (HTTPS, authenticated session)
[Backend API (Go handlers)]
        |             |              |
        v             v              v
[CRDT fabric]  [PDF signing]   [Spaces (collab)]
        |                           |
        v                           v
[Storage (SQLite + blob)]    [WebRTC signaling / data channels]
```

Trust boundaries:
- **Browser ↔ Backend API**: authenticated session. Browser input is untrusted.
- **Backend API ↔ Storage**: server-internal; storage is private to the backend process.
- **Spaces signaling**: broker mediates peer connections; payload content in data channels is end-to-end between peers.
- **PDF signing**: signing key is held server-side; signing operation is privileged.

---

## Component 1: CRDT Fabric

### Trust boundaries
- CRDT operations are submitted by authenticated clients.
- The merge engine applies operations from all participants; each participant's operations are trusted within their session scope.
- A malicious or buggy client can submit valid-looking but semantically destructive operations.

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Tampering** | A client submits a large number of CRDT operations in rapid succession, causing unbounded memory growth in the merge engine (algorithmic DoS). |
| 2 | **Tampering** | Crafted CRDT operation vector clocks with forged peer IDs allow a client to replay or suppress another client's edits. |
| 3 | **Information Disclosure** | Deleted CRDT operations (tombstones) retain original content in the operation log; a user who should no longer have access can reconstruct deleted data. |

### Mitigations in code
- Per-session operation rate limiting in `crdt/` handlers.
- Peer IDs in operations are derived from the authenticated session, not supplied by the client.
- Document compaction (tombstone GC) implemented in `crdt/` to prune old operation history.

### Residual risks
- Compaction is manual / scheduled; tombstone data persists until GC runs.
- CRDT convergence proofs for custom document types are informal; corner-case divergence is possible.

---

## Component 2: PDF Auto-Sign

### Trust boundaries
- Signing key stored server-side; accessible only to the signing handler.
- Client supplies the document to be signed; document content is untrusted.
- Signed output is written to blob storage and returned to the client.

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Tampering** | Maliciously crafted PDF (e.g. with incremental update or JavaScript) causes the signing library to sign unexpected content or execute code during parsing. |
| 2 | **Repudiation** | No audit log of which user triggered signing of which document; disputes about authorised signatures cannot be resolved. |
| 3 | **Elevation of Privilege** | Path traversal in the document input path allows an attacker to sign arbitrary server-side files. |

### Mitigations in code
- PDF parsed and re-serialised to a clean PDF/A-like structure before signing, stripping JavaScript and incremental updates.
- Signing audit log records: document hash, signing user, timestamp, and resulting signature ID.
- Document input resolved under `UPLOAD_ROOT`; `..` traversal rejected.

### Residual risks
- PDF re-serialisation completeness depends on library coverage; novel PDF attack surfaces may not be normalised.
- Signing key rotation process is manual; no automated key expiry enforcement.

---

## Component 3: Spaces (Real-time Collaboration)

### Trust boundaries
- Signaling server brokers WebRTC session establishment; does not inspect media/data content.
- Data channels between peers are end-to-end; signaling server sees only SDP offers/answers and ICE candidates.
- Room membership enforced by the signaling server based on session authentication.

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Spoofing** | Attacker guesses or enumerates a room ID and joins a collaboration session without invitation. |
| 2 | **Denial of Service** | Attacker floods the signaling server with SDP offers, exhausting connection state for legitimate sessions. |
| 3 | **Information Disclosure** | ICE candidates leak the server's internal IP or reveal network topology to a remote peer. |

### Mitigations in code
- Room IDs are 128-bit random tokens; not sequential or guessable.
- Room membership is checked against the authenticated user's document access list before join is permitted.
- ICE candidate filtering strips RFC1918 / loopback candidates from responses to external peers by default.
- Signaling connection rate-limiting per authenticated user.

### Residual risks
- WebRTC data-channel content between peers is opaque to the server; malicious peers can exfiltrate document data through the data channel to a third party.
- ICE relay (TURN) server, if deployed, must be secured separately; its configuration is outside this codebase.

---

## Overall Residual Risks

1. PDF re-serialisation provides best-effort normalisation; novel PDF attack vectors may survive.
2. CRDT tombstone data retention until GC creates a window where deleted content is recoverable.
3. WebRTC peer-to-peer data channels are opaque; data exfiltration between consenting (but malicious) peers cannot be prevented by the server.
4. Signing key rotation is manual; long-lived keys increase blast radius of a key compromise.
