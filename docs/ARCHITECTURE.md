# Vulos OS – Architecture

## Overview

Vulos is a self-hosted personal OS in a container. It runs on a single machine (bare-metal or VM) and exposes a browser-native desktop via WebSocket/WebRTC.

## Component Map

```
┌─────────────────────────────────────────────┐
│  Browser (WebApp UI)                        │
│  src/ — React SPA, served from /           │
└──────────────────┬──────────────────────────┘
                   │ WebSocket / HTTP
┌──────────────────▼──────────────────────────┐
│  Go HTTP backend  (backend/cmd/server/)     │
│                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  Auth    │  │  AI/Chat │  │  AppNet  │  │
│  │ services │  │  router  │  │ launcher │  │
│  └──────────┘  └──────────┘  └──────────┘  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
│  │  Vault   │  │  Recall  │  │  Stream  │  │
│  │ (Restic) │  │ (vector) │  │  pool    │  │
│  └──────────┘  └──────────┘  └──────────┘  │
│  ┌──────────────────────────────────────┐   │
│  │  Observability: /metrics + OTel      │   │
│  │  backend/internal/obs/               │   │
│  └──────────────────────────────────────┘   │
└─────────────────────────────────────────────┘
         │                    │
   SQLite DB             Namespace
   ~/.vulos/db/          isolation
                         (appnet)
```

## Key Design Decisions

- **Single binary**: the Go backend embeds the frontend SPA at build time.
- **Local-first storage**: SQLite for auth/config; S3 (optional) for backup via Restic.
- **App sandboxing**: each user app runs in its own Linux network namespace with a unique host port; traffic is proxied through the app gateway.
- **Authentication**: email+password with optional WebAuthn/TOTP. No third-party identity providers.
- **Device pairing**: QR-code based join codes for adding devices to a cluster.

## Observability

- `/metrics` — Prometheus textfile (counters, histograms, gauges in `vulos_*` namespace)
- OTel traces via `backend/internal/obs.Start(ctx, op)` when `OTEL_EXPORTER_OTLP_ENDPOINT` is set

## Browser architecture (BROWSER-01/02)

Browsing is **host-browser-native**: `POST /api/open` returns a
`{"action":"open_in_host_browser","url":"..."}` instruction; the frontend shell
opens the URL in the kiosk Chromium (bare-metal) or the user's desktop browser
(remote). No server-side streamed Chromium session is created.

The `services/webbrowser` package (server-side Chromium streaming) was removed
in BROWSER-02. The `xvfb`, `chromium`, and `xdotool` streaming-only packages
are no longer installed in the container or bare-metal image. The kiosk
Chromium and its enterprise-policy files
(`/etc/chromium/policies/managed/vulos.json`) remain intact — the kiosk
Chromium *is* the host browser on bare metal.

Isolated/Disposable Browsing (RBI) is not implemented; the stub and its flag
(`VULOS_ENABLE_ISOLATED_BROWSER`) have been removed. Revisit if a concrete use
case arises that cannot be served by the host browser.

## See Also

- Roadmap: `ROADMAP.md`
- Security model: `THREAT-MODEL.md`
- Deployment: `docs/DEPLOY.md`
