// Package streamsession is the cloud-side orchestrator for low-latency
// desktop/game streaming sessions (STREAM-SIGNAL-01).
//
// What this package IS:
//   - A session lifecycle manager: allocate a host (cloud-GPU instance via
//     gpusku.Attach, or a registered BYO GPU box), record the session in
//     pure-Go SQLite, and tear it down (gpusku.Detach + relay drop).
//   - A THIN signaling pass-through: clients POST SDP/ICE blobs at
//     POST /api/stream/sessions/{id}/signal; the cloud forwards the OPAQUE
//     payload bytes to the configured relay's VULOS-STREAM/1 signaling
//     ingress (HTTPS). The cloud does not parse SDP/ICE — the wire
//     sub-protocol is the relay's contract.
//
// What this package IS NOT:
//   - A media bridge. MEDIA NEVER TRANSITS THE CONTROL PLANE. This is
//     enforced two ways:
//     1. Hard payload size cap (MaxSignalBytes) on every inbound signal
//     POST; anything larger is rejected 413 and NOT forwarded. SDP
//     offers/answers and ICE candidates are kilobytes; media packets
//     would be much larger and would trip the cap.
//     2. The cloud never opens a UDP socket toward the host or client —
//     it only speaks HTTPS to the relay's signaling ingress.
//   - A TURN allocator. TURN slots are opened by the relay (vulos-relay's
//     streamsignal.go) when host or client signals msgP2PFailed.
//
// Invariants (FROZEN):
//   - Pure-Go modernc.org/sqlite ONLY. NEVER CGO.
//   - No Go-module dep on vulos-mail/relay/office. The relay contract is
//     spoken OVER HTTPS to a configured RelayEndpoint, not via imports.
//   - The signal handler caps payloads at MaxSignalBytes and never proxies
//     media bytes (verified by tests).
//
// Relay endpoint contract (HTTPS POST):
//
//	POST {RelayEndpoint}/stream/signal
//	  Content-Type: application/octet-stream
//	  X-Vulos-Stream-Session: <session id>
//	  X-Vulos-Stream-Host:    <host_ulid>
//	  X-Vulos-Stream-Account: <account_id>
//	  <body: opaque VULOS-STREAM/1 wire frame from the client; cloud does
//	   not parse or interpret it>
//	→ 2xx on accept; non-2xx propagates back to the client as 502.
//
// The cloud is solely a control surface for STREAM-SIGNAL-01: allocation,
// signaling pass-through, teardown + meter detach.
package streamsession
