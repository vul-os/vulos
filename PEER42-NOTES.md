# PEER-42 Implementation Notes

## What was done

PEER-42 wires all peering sub-handlers from `backend/services/peering/` into
`backend/cmd/server/main.go` so that no peering route returns HTTP 501.

### Prior state (before this branch)

The bulk of PEER-42 was already committed on `main` (the PEER-42 wiring block at
lines ~1452–1628 of `main.go`). That block registered the following handler sets
onto a dedicated `peeringMux`:

- `RegisterContactHandlers`, `RegisterMessageHandlers`, `RegisterGroupHandlers`
- `RegisterRelayHandlers`, `RegisterFeedHandlers`, `RegisterDropHandlers`
- `RegisterMediaHandlers`, `RegisterCallHandlers`, `RegisterMeshCallHandlers`
- `RegisterCallHistoryHandlers`, `RegisterLobbyHandlers`, `RegisterProfileHandlers`
- `RegisterVerifyHandlers`, `RegisterDiscoveryHandlers`, `RegisterICEHandlers`
- `RegisterEndpointHandlers`, `RegisterAttestHandlers`, `RegisterProximityHandlers`
- `RegisterCollabHandlers`

### This branch adds

Three handler sets that were missing from the prior wiring:

1. **`RegisterCollabHistoryHandlers`** (`collab_history.go`)  
   Routes: `GET /api/peering/collab/{doc_id}/history[/{seq}]` and
   `GET /api/peering/collab-sync-v2`.  
   These do not overlap with `RegisterCollabHandlers` and are passed the same
   `collabStore`.

2. **`RegisterPresenceHandlers`** (`presence_awareness.go`)  
   Routes: `WS /api/peering/presence/{app_id}`,
   `GET /api/peering/presence/{app_id}/peers`,
   `POST /api/peering/inbound/presence`.  
   No overlap with any existing handler.

3. **`POST /api/peering/inbound/collab-invite`** (from `shares.go`)  
   This is the one route from the shares/collab_share subsystem that does not
   conflict with `RegisterCollabHandlers`. Wired via a minimal `NewSharesService`
   (in-memory `ShareStore`, existing `contactStore` + `peerClient`).  
   `RegisterSharesHandlers` and `RegisterCollabShareHandlers` in full are
   intentionally NOT wired — they register the same `/api/peering/collab/*` and
   `/api/peering/inbound/collab-update|collab-sync` patterns as
   `RegisterCollabHandlers`, which would panic `http.ServeMux`.

### Test added

`backend/services/peering/peer42_wiring_test.go` — two tests:

- **`TestPEER42_No501Routes`**: builds the full `peeringMux` in-process
  (same construction as `main.go`), probes every peering route, and fails if
  any returns 501.

- **`TestPEER42_PeeringMuxNoPanic`**: confirms that registering all handler
  sets on the same `ServeMux` does not panic (no duplicate route).

## Acceptance criteria check

- [x] No peering route returns 501 (verified by `TestPEER42_No501Routes`)
- [x] Server starts without ServeMux dup-panic (verified by `TestPEER42_PeeringMuxNoPanic`)
- [x] `go test ./services/peering/... green`
- [x] `go build ./... passes`

## Follow-ups / known limitations

- `RegisterSharesHandlers` (outbound share invite UI: `POST /api/peering/collab/share`
  from `shares.go`) is not reachable — `RegisterCollabHandlers` already registers
  that route. If the `SharesService` outbound path is needed in future, the two
  store types (`CollabStore` vs `ShareStore`) need to be reconciled or the share
  route deduped.

- Presence WebSocket (`/api/peering/presence/{app_id}`) needs a real WebSocket
  upgrade; smoke tests hit it with plain HTTP and get 400/426, which counts as
  "wired" for the regression guard.
