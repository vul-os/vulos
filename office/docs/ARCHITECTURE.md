# Vulos Office – Architecture

## Overview

Vulos Office is a collaborative document editing + e-signing service. It exposes:
- File CRUD with version history
- CRDT-based real-time collaboration (WebSocket)
- E-signing workflow (envelope → sign → sealed PDF)
- Vulos Spaces: channels, DMs, threads

## Component Map

```
Browser clients (React SPA)
   │ HTTP + WebSocket
   ▼
┌────────────────────────────────────┐
│  Gin HTTP Server (main.go)         │
│                                    │
│  /api/files/*   → FileHandler      │
│  /api/files/:id/versions → ...     │
│  /api/sign/*    → SigningHandler   │
│  /api/spaces/*  → ForumHandler     │
│  /api/meetings/*→ MeetingHandler   │
│  /metrics       → obs.Handler()   │
└──────────────────┬─────────────────┘
                   │
   ┌───────────────┼───────────────┐
   │               │               │
   ▼               ▼               ▼
backend/        backend/       backend/
spaces/store    crdt/          signing/
(SQLite/PG)     tree,grid,text crypto.go
                (CRDT engine)

Observability:
  backend/obs/ — vulos_office_* metrics + OTel
  GET /metrics
```

## Key Design Decisions

- **Gin framework**: chosen for its middleware ecosystem and existing codebase.
- **CRDT engine**: leaderless; tree, grid, and text types. Ops are merged deterministically.
- **E-signing**: PDF is sealed with a cryptographic hash; audit manifest JSON captures all signer events.
- **Auth**: JWT-based; configurable (`cfg.Auth.Enabled`).

## See Also

- Deployment: `docs/DEPLOY.md`
- CRDT design: `backend/crdt/doc.go`
