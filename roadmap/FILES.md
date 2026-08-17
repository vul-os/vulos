# Files — Drive-like File Management

Unified file storage and sharing for Vulos. Treats the OS's local filesystem, cloud provider storage (Google Drive, Dropbox, OneDrive, GCS), and peer-shared collections as a single browsable tree.

> **Goal.** One Files app that spans local disk, cloud drives, and peer shares. Importing from Google Drive / Dropbox / OneDrive / GCS copies files into the owned local bucket. Peer-to-peer sharing uses a box-to-box capability transport (Mechanism-B) so shares work across NAT without a central file server.
> **Non-goals.** Becoming a cloud storage provider. Replacing the cluster's cr-sqlite sync layer (SYNC.md) — Files is for user-visible documents, not database replication. Running a public IPFS node.
> **Status.** Phase 1–4 shipped: unified bucket, OS control-plane (copy/move/delete/rename), bucket-direct grants for cloud providers, and the import engine for all four providers including OneDrive (`backend/services/files/onedrive.go`, registered `backend/cmd/server/main.go:949`, imported from `frontend/src/builtin/drive/Drive.tsx`). **Peer-Share Mechanism-B has also since shipped** — box-to-box capability sharing (`backend/services/files/peershare.go`), wired at `backend/cmd/server/routes_files_peer.go`, sending UI in `frontend/src/builtin/files/SharePeerModal.tsx` (Files app), receiving/redeem UI in `frontend/src/builtin/drive/Drive.tsx` (the "B→A bridge" — see below). A follow-on wave (VSEAL1, `backend/services/files/peershare_sealed.go`) added content-blind sealed sharing for account-only recipients so the cell relays ciphertext it cannot open. This section previously described Mechanism-B as "the next major milestone" with an open-tasks list of unbuilt files — corrected below; see commits `9657adf6`, `4fef42d3`, `6ccfaa99`, `f5aef780`, `8938dca7`.

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  Files App (React)               │
│   Local | Imports | Shared-with-me | Shared-by  │
└──────────────┬──────────────────────────────────┘
               │
┌──────────────▼──────────────────────────────────┐
│           OS Control Plane (Go)                  │
│   /api/files/*   /api/files/import/*             │
│   /api/files/grants   /api/files/shares          │
└──────┬────────────┬──────────────────────────────┘
       │            │
  ┌────▼────┐  ┌────▼──────────────────────────────┐
  │  Local  │  │       External Store Adapters      │
  │  Bucket │  │  GoogleDrive | Dropbox | OneDrive  │
  │ (owned) │  │  GCS | (S3-compatible future)      │
  └─────────┘  └───────────────────────────────────┘
                                │
                   peer-share Mechanism-B — shipped
                   (box-to-box capability PULL, cross-instance)
```

---

## Import Engine (Phase 4 — shipped)

The import engine copies files from an external provider into the user's owned local bucket. This is a **one-way copy** — the source is not modified. The import is resumable and idempotent (files already present are skipped).

Supported providers (import path), all shipped:
- **Google Drive** — OAuth2, Drive API v3, recursive folder traversal
- **Dropbox** — OAuth2, Dropbox API v2
- **GCS (Google Cloud Storage)** — service-account JSON, GCS list/get
- **OneDrive** — Microsoft Graph API (`backend/services/files/onedrive.go`, `NewOneDriveProvider()` registered at `backend/cmd/server/main.go:949`; import UI in `frontend/src/builtin/drive/Drive.tsx`, "Import from Google / Microsoft")

---

## Peer-Share Mechanism-B — shipped

**This section previously described Mechanism-B as unbuilt, with an "Open tasks" list of files that did not exist. All of it has since shipped** (`backend/services/files/peershare.go`, wave `9657adf6` "Phase 2B — OS peer-share (Mechanism B, bucket-less box-to-box)"). What follows is the shape that was actually built, which differs from the plan below in mechanism (a signed capability redeemed over a direct PULL, not a signed token pushed then relay-fetched) and in file names.

**Capability model**, not a share-token: the owner's box signs a Capability (peering Ed25519 key) that is scoped to one node + access level, carries an absolute expiry, is optionally bound to a specific recipient box, and is revocable — the owner keeps a `files_peer_shares` row per capability and refuses to serve bytes for a revoked one even though the signed token itself is still valid. The link is `base64url(JSON)` of the signed capability, paste-anywhere.

**Transport is a direct PULL, not a relay push-then-fetch.** The original plan (a signed share-token sent via a peering envelope, resolved by direct HTTP or, behind NAT, a relay-mesh store-and-forward fetch) is not what shipped. What shipped: the recipient's box redeems the capability by streaming bytes (or, for a folder, a tar archive) directly from the owner's box over the peering transport (`PeerTransport`), proving identity with a signed fetch proof. There is no relay-mesh store-and-forward step in the implementation. `PEERING.md` documents the underlying peering transport this rides on.

**Revocation** is `POST /api/files/peer/revoke` (`{"id": ...}` → `RevokePeerShare`), not the planned `DELETE /api/files/shares/{doc_id}` — same effect (owner-side revoke makes the next redeem attempt fail), different route shape.

**Endpoints** (`backend/cmd/server/routes_files_peer.go`): `POST /api/files/peer/issue`, `POST /api/files/peer/revoke`, `GET /api/files/peer/shares`, `POST /api/files/peer/redeem`, `GET /api/files/peer/received`, `GET /api/files/peer/received/get`, `POST /api/files/peer/save` (the B→A bridge — promotes a staged received item into the recipient's own Drive), `GET /api/files/peer/folder-tar`, `POST /api/files/peer/issue-sealed`, `POST /api/files/peer/receive`.

**UI split across two apps**: sending is in the Files app (`frontend/src/builtin/files/SharePeerModal.tsx` + `shareToPeer.ts`, wired into `FileManager.tsx`); receiving/redeeming/save-to-Drive is in the Drive app (`frontend/src/builtin/drive/Drive.tsx`), which is also where the OneDrive/Google import modal lives. There is no single "Files: Shared-with-me / Shared-by" tab as the architecture diagram above implies — that diagram predates the actual UI split and is aspirational on that point.

**Content-blind sealed sharing (VSEAL1), a follow-on not in the original plan at all.** For account-only recipients (no reachable box), the sharer's client seals the file to the recipient's published X25519 content key before it ever reaches the cell — `backend/services/files/contentseal.go` (Go) and `frontend/src/lib/contentSeal.ts` (WebCrypto, byte-for-byte parity with the Go implementation, asserted against a Go fixture in vitest) — so the cell relays ciphertext it cannot open. `backend/services/files/peershare_sealed.go` adds the sealed capability type, on-disk sealed-artifact store, and `POST /api/files/peer/issue-sealed`. Landed in waves `6ccfaa99` and `f5aef780`; `8938dca7` fixed a 404-vs-403 access-check bug on top of it.

This mirrors the collab-share model (`RegisterCollabHandlers`) but for arbitrary file blobs, not Yjs CRDT documents.
