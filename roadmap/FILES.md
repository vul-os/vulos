# Files — Drive-like File Management

Unified file storage and sharing for Vulos. Treats the OS's local filesystem, cloud provider storage (Google Drive, Dropbox, OneDrive, GCS), and peer-shared collections as a single browsable tree.

> **Goal.** One Files app that spans local disk, cloud drives, and peer shares. Importing from Google Drive / Dropbox / OneDrive / GCS copies files into the owned local bucket. Peer-to-peer sharing uses the relay mesh (Mechanism-B) so shares work across NAT and cluster boundaries without a central file server.
> **Non-goals.** Becoming a cloud storage provider. Replacing the cluster's cr-sqlite sync layer (SYNC.md) — Files is for user-visible documents, not database replication. Running a public IPFS node.
> **Status.** Phase 1–4 shipped: unified bucket, OS control-plane (copy/move/delete/rename), bucket-direct grants for cloud providers, and the import engine for Google Drive, Dropbox, and GCS external stores. Peer-share Mechanism-B (relay p2p cross-instance) is the next major milestone.

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
                   peer-share Mechanism-B
                   (relay mesh, cross-instance)
```

---

## Import Engine (Phase 4 — shipped)

The import engine copies files from an external provider into the user's owned local bucket. This is a **one-way copy** — the source is not modified. The import is resumable and idempotent (files already present are skipped).

Supported providers (import path):
- **Google Drive** — OAuth2, Drive API v3, recursive folder traversal
- **Dropbox** — OAuth2, Dropbox API v2
- **GCS (Google Cloud Storage)** — service-account JSON, GCS list/get
- **OneDrive** — (Phase 5, planned) Microsoft Graph API

---

## Peer-Share Mechanism-B (next milestone)

Cross-instance peer sharing without a central file server. Uses the existing relay mesh (PEERING.md) as transport:

1. **Sharer** generates a signed share-token (doc_id + permissions + TTL) and sends it to the peer via the peering envelope layer.
2. **Recipient** stores the token and resolves it on demand: if the sharer is reachable directly (LAN / direct), fetch via HTTP; if behind NAT / cross-cluster, relay through the relay mesh.
3. **Revocation** is immediate: the sharer marks the doc_id as revoked in their local share-store; the next relay fetch returns 410 Gone.

This mirrors the collab-share model (`RegisterCollabHandlers`) but for arbitrary file blobs, not Yjs CRDT documents.

### Open tasks

- [ ] `backend/services/files/peer_share.go` — `ShareFile(peerID, docID, perm)` → signed envelope delivery
- [ ] `backend/services/files/peer_fetch.go` — relay-aware fetch with direct-first fallback
- [ ] `src/files/ShareModal.jsx` — share-to-peer UI (contact picker + permission selector)
- [ ] Revocation: `DELETE /api/files/shares/{doc_id}` → 410 propagation on next peer fetch
- [ ] OneDrive import adapter (Microsoft Graph, OAuth2 device-flow for headless)
