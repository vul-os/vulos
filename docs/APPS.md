# Apps & Bots on Vulos OS

Vulos OS runs real applications — bundled web apps, self-hosted services installed from a signed registry, and token-authenticated "bot" apps that let outside agents drive the OS. This chapter covers installing apps from the App Hub, the permission and network-isolation model, publishing an app to the internet on a subdomain or your own domain, API keys, and the opt-in code-execution sandbox.

New to the OS? Read [GETTING-STARTED.md](GETTING-STARTED.md) first. All environment variables mentioned here are summarized at the end and in [CONFIGURATION.md](CONFIGURATION.md).

---

## Two app systems, one OS

It helps to know that "app" means two different things on a Vulos box:

1. **Native OS apps** — real processes (web apps and services) that the OS launches, isolates, and windows into the shell. They come bundled with the OS or are installed from the App Hub registry. This is most of this chapter.
2. **Platform apps ("bots")** — entries on the *Apps & Bots platform*: they have no process of their own on the box, just a scoped `vat_` token that lets an external program (a script, a service, an MCP agent) act on the OS through a narrow API. Covered in [Bots and agent apps](#bots-and-agent-apps) below.

There is no separate "bot" runtime — a bot *is* a platform app holding a token.

---

## Bundled apps

The OS ships with a set of first-party apps under `apps/` in the install tree (Notes, Calculator, Browser, Camera, Clock, Gallery, Maps, Music, Ofisi, PDF Viewer, Sheets, Text Editor, Video, Weather, and more). Each is described by an `app.json` manifest. A real one (`apps/notes/app.json`):

```json
{
  "id": "notes",
  "name": "Universal Memory",
  "icon": "☰",
  "icon_path": "icon.svg",
  "description": "Notes & knowledge base — every thought indexed",
  "version": "0.1.0",
  "command": "python3 server.py",
  "port": 80,
  "category": "productivity",
  "keywords": ["notes", "write", "knowledge", "memory", "wiki"],
  "deps": ["python3"],
  "auto_start": false,
  "singleton": true,
  "permissions": ["network"],
  "author": "Vulos",
  "license": "MIT"
}
```

The manifest also supports `integrations` (which cloud-provider credentials the gateway may inject), `visibility` (`private` / `local` / `public`, default `private`), and `concurrency` (`singleton` / `replicated` / `collaborative`, default `singleton`).

---

## Calendar & Contacts (PIM, the GNOME model)

Vulos follows the GNOME desktop's split between a **data server** and thin
front-ends. [lilmail](MAIL-LILMAIL.md) is the "Evolution-Data-Server": it connects
the user's IMAP/CalDAV/CardDAV — directly, or via an OAuth-linked Google/Microsoft
account (the "online accounts" step) — and exposes a stable **`/v1`** contract
(`/v1/calendar/*`, `/v1/contacts/*`). The OS ships two standalone built-in apps,
the "GNOME Calendar/Contacts" of Vulos:

- **Calendar** — a month grid + agenda with full event CRUD. Also an always-on
  desktop agenda widget ("what's next"). Launcher id `vulos-calendar`.
- **Contacts** — a list + detail/edit address book with full CRUD. Launcher id
  `vulos-contacts`.

Both are React components in `src/builtin/{calendar,contacts}/` — they own no
storage of their own. They read/write lilmail through the box's **PIM proxy**:
the browser calls `/api/pim/{calendar,contacts}/*` with its own session cookie,
and the box rewrites that to lilmail `/v1/*`, injecting the brokered mail
credentials (`X-Vulos-Mail-*`) server-side. **Mail credentials never reach the
browser**, and the proxy only exposes the calendar/contacts subtrees of `/v1` —
nothing else on the mailbox. When no account is connected, both apps degrade to an
honest "Connect Mail" state rather than an error. See `backend/cmd/server/routes_pim.go`.

---

## Two browsers, and streamed native apps

Vulos ships **two user-selectable browsers** as separate launcher tiles, so you
can choose per task:

- **Smart Browser** — a client-side web app (`apps/browser/`) that opens in your
  host browser. No server-side session, no stream — the light option.
- **Streaming Chrome** — a **real Chromium running on the box**, streamed to the
  shell over WebRTC with a **persistent per-user profile** (cookies, history,
  logins). Launched on demand via `POST /api/browser/launch`, which mints a
  per-user streaming session (`backend/services/webbrowser/`).

Streaming Chrome is one case of Vulos's general **native-app streaming**: any
Linux GUI app can render into a shell window over WebRTC (Xvfb → GStreamer
HW-encode → WebRTC), with the stream torn down when you close the window. Three
tunings share that pipeline:

| Mode | When | Behaviour |
|------|------|-----------|
| Native app window | Ordinary GUI apps (Audacity, KiCad, …) | Dirty-region capture, idle-throttled — optimised for a still desktop |
| Gaming | Auto-detected for real games only (Wine/Lutris/Steam, or a `category: gaming` manifest) | Full-frame capture, low-latency encoder profile (no B-frames, CBR, 1s GOP), minimal client jitter buffer, pointer-lock |
| Streaming Chrome | The browser tile above | Per-user persistent Chromium profile on the same pool |

Real frame-rate, latency, and GPU behaviour depend on the deployment (hardware,
encoder availability, network path); Streaming Chrome and gaming mode also
require the Chromium + Xvfb/GStreamer streaming stack to be present on the box.

---

## Installing apps from the App Hub

The **App Hub** app in the shell is the store front. It lists a curated registry of installable self-hosted apps (Navidrome, Gitea, Jellyfin, Grafana, Jupyter, draw.io, Cockpit, Firefox via Flatpak, and others) — including the chat/video answer, Element/Cinny (Matrix) and Jitsi Meet/Element Call (video), covered in full in [COMMS.md](COMMS.md) — shows what's installed, and installs or removes with one click and a progress bar.

Behind the UI:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/store/registry` | The full registry catalog |
| `GET /api/store/registry/{appId}` | One registry entry |
| `POST /api/store/registry/install` | Install: body `{"appId":"navidrome","version":"0.54.5"}` |
| `GET /api/store/installed` | Installed apps |
| `POST /api/store/uninstall` | Remove an installed app |
| `GET /api/store/catalog` | The bundled/base catalog |

Install and uninstall are admin-gated — a non-admin account on the box cannot add software.

### What an install actually does

A registry entry pins everything needed to reproduce the app:

```json
{
  "name": "Navidrome",
  "vetted": true,
  "type": "web",
  "category": "media",
  "license": "GPL-3.0",
  "versions": {
    "0.54.5": {
      "download_url": "https://github.com/navidrome/navidrome/releases/download/v0.54.5/navidrome_0.54.5_linux_amd64.tar.gz",
      "command": "bin/navidrome --configfile data/navidrome.toml",
      "port": 4533,
      "checksum": "73c1a42958dc2c96fa9787fb060e36f664bb0d9f58f66c07b3b3ba12be4a3ca1",
      "permissions": ["network", "filesystem"],
      "singleton": true
    }
  }
}
```

The installer downloads the artifact, verifies it, unpacks it into the app directory (`<appsDir>/<appId>` — `/opt/vulos/apps` on a system install), runs the recipe's `post_install`, writes the generated `app.json`, and symlinks the app's data directory to `~/.vulos/data/<appId>` so your data survives reinstall and upgrade.

### Supply-chain rules

The registry pipeline is deliberately strict:

- **Pinned SHA-256 required.** Any recipe that downloads a binary must carry a `checksum`; the download is verified against it before anything runs. Entries in the public registry without a pinned hash are shipped disabled.
- **No pipe-to-shell.** Recipes containing `curl … | bash`-style patterns are rejected outright.
- **Ed25519 publisher signatures — mandatory.** Every registry entry is signed by the release key, which is certified by the offline root key baked into the image at `/etc/vulos/trust-anchor.pub`. Verification is fail-closed: an unsigned, tampered, or foreign-signed entry blocks the install, and a box with no trust anchor refuses to install anything at all. The signature covers the entry's app ID, so a signed entry cannot be moved to another app slot. See [KEY-CEREMONY.md](KEY-CEREMONY.md).
- **`VULOS_REGISTRY_INSECURE` is dev-only and refused in production.** It skips verification, and it is rejected outright when `VULOS_ENV=prod` — which is also the default when `VULOS_ENV` is unset. There is no way to turn signature checking off on a production box.
- **Forks.** `VULOS_REGISTRY` points the box at an alternative registry source, and `VULOS_REGISTRY_PUBKEY` supplies a single verification key directly for forks that sign without a release cert.
- **Safe extraction.** Archives are unpacked with path-containment checks, so a malicious tarball cannot write outside the app directory.

Debian packages are a separate surface: the App Hub also exposes the system package cache (`GET /api/packages/cache`, `POST /api/packages/update`) for keeping the base system current.

---

## App permissions

Each app declares what it needs in its manifest's `permissions` array. Unknown permission strings fail validation. The full set:

| Permission | Grants |
|------------|--------|
| `network` | Outbound network access (every app gets loopback regardless) |
| `filesystem` | Read/write outside the app's own data directory |
| `camera` | Camera device access |
| `microphone` | Microphone device access |
| `bluetooth` | Bluetooth access |
| `usb` | USB device access |
| `gpu` | GPU acceleration |
| `background` | Keep running when its window is closed |
| `notifications` | Send desktop/push notifications |
| `storage` | Receive per-user object-store credentials from the gateway |

Two honest notes on enforcement:

- The strongest, uniformly-enforced boundary is the **network/process isolation** described in the next section — it applies to every launched app the same way. The `storage` and `integrations` grants are enforced at the gateway, which only injects the corresponding credential headers for apps whose manifest declares them (and strips any such headers a client tries to smuggle in).
- Platform apps (bots) use a different, smaller permission system — the `apps:read` / `apps:write` token scopes described under [Bots and agent apps](#bots-and-agent-apps). Don't confuse the two.

---

## How apps are isolated

Every native app launches inside its own sandbox, built from plain Linux primitives:

- **A private network namespace** (`vulos_<appId>`) with a point-to-point veth pair on a per-app `10.200.x.x/30`-style subnet — no shared bridge between apps.
- **Firewalled by default.** The only inbound path is a loopback-only DNAT from the host to the app's port; direct external access to the app port is dropped. Traffic between app namespaces is dropped. Egress to cloud metadata ranges (`169.254.0.0/16` and the IPv6 equivalents) is dropped as an SSRF guard, even for apps with `network`.
- **Reachable only through the gateway.** Users hit apps at `https://<your-box>/app/{appId}/`, which rides the OS's session auth. The namespace rules only permit the app to answer the gateway.
- **Dropped privileges.** The app process runs as `nobody` (uid 65534) with no-new-privileges via `setpriv`, inside its own mount namespace, with `HOME` pointed at a throwaway directory.

This requires the backend to run with root privileges on a Linux host with `iproute2` and `iptables` available (in the Docker image this is already the case). If namespace setup fails — for example, the container lacks the needed capabilities — the launch **fails hard**; the OS does not silently fall back to running the app unisolated.

Runtime visibility lives under the native app API: `GET /api/apps/running`, `POST /api/apps/launch`, `POST /api/apps/stop`, plus `GET /api/apps/namespaces`, `/api/apps/ports`, `/api/apps/traffic`, and `/api/apps/health`.

For the box's own network posture (ports, TLS, tunnels) see [NETWORKING.md](NETWORKING.md).

---

## Publishing an app to the internet

By default every app is `private` — reachable only by signed-in users of your box. You can change that per app:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/apps/visibility` | Every app's current visibility (`private` / `local` / `public`) |
| `POST` or `PATCH /api/apps/{id}/visibility` | Set one app's visibility |

System apps (Settings, Files, Terminal) refuse anything but `private`.

### Subdomains

Setting an app `public` provisions a subdomain of the form:

```
{app}--{profile}.{instance-id}.vulos.org
```

(e.g. `notes--default.01h5t3.vulos.org`). `VULOS_BASE_DOMAIN` is the domain you have pointed at the box — there is no default, because no domain is handed out on your behalf. Creating the DNS record is yours to do: point `VULOS_DNS_API` at your provider's endpoint to have it done for you, or add the record by hand. TLS certificates are obtained automatically via ACME once DNS resolves. Check or tear down a deployment with `GET /api/apps/{id}/deployment` and `POST /api/apps/{id}/deprovision`.

### Custom domains

You can front a published app with your own domain:

```bash
# 1. Attach the domain — returns a DNS TXT challenge token
curl -X POST https://os.example.com/api/apps/navidrome/domain \
  -H "Content-Type: application/json" -d '{"domain":"music.example.com"}'
# → {"challenge_token":"…"}

# 2. Create the TXT record at your DNS provider, then verify
curl -X POST https://os.example.com/api/apps/navidrome/domain/verify

# 3. Inspect or remove later
curl https://os.example.com/api/apps/navidrome/domain
curl -X DELETE https://os.example.com/api/apps/navidrome/domain
```

Verification checks the TXT record and activates the domain; deleting reverts to the default subdomain. TLS for custom domains is handled by the reverse proxy (a Caddy snippet is emitted when `VULOS_CADDY_DIR` is set).

### Edge cache

Published apps get an Nginx micro-cache in front of them: anonymous `GET` responses marked `Cache-Control: public` are cached for a few minutes (never `/api/` paths, never requests carrying `Authorization`), and responses carry an `X-Cache: HIT|MISS` header so you can see it working. Two user-reachable endpoints:

- `POST /api/apps/{id}/cache/purge` — purge the app's cache (returns `{"status":"purged"}`)
- `GET /api/apps/{id}/cache/stats` — cache/status counters

---

## API keys (`vk_`)

Programmatic access to the OS API uses bearer keys with the `vk_` prefix:

```bash
curl https://os.example.com/api/files/list \
  -H "Authorization: Bearer vk_live_8Qk3xT91mZbC4dLwPzhE2a"
```

How they work on the box:

- A `vk_` key is accepted by the auth middleware on the OS's `/api/*` surface, as an alternative to a browser session. On success the request runs as the local user account the key maps to.
- Keys are **issued and revoked by an external control plane**, not on the box — the box only ever holds an opaque handle. It forwards the key to that control plane's introspection endpoint and honors the verdict — validity, the owning account, the key's `scopes` (e.g. `files.read`, `files.write`), and its `products`. A key must carry the `os` product to be usable here.
- Verdicts are cached in-process for about 60 seconds, so revocation in the control plane takes effect within a minute. The cache stores only a hash of the key, never the plaintext.
- Everything fails closed: control-plane errors, invalid keys, missing `os` product, or an account that doesn't exist on this box all yield 401.

Configuration:

| Variable | Meaning |
|----------|---------|
| `VULOS_CP_BASE_URL` | The control plane URL. **Unset (the default) disables `vk_` auth entirely** — the box is session-only |
| `VULOS_CP_TOKEN` | Service token the box presents when introspecting keys |
| `VULOS_CP_ALLOW_INSECURE` | Dev-only: permit a plain-http control plane URL |

Self-hosters running fully standalone simply leave this off and use platform app tokens (below) for automation instead.

---

## Bots and agent apps

> **Scope note (read this first).** The Apps & Bots platform described in this
> section — the `vat_` token registry, the `/api/apps/v1/*` runtime, webhooks,
> and the MCP endpoint — is a **design, not a built feature**. None of the
> `POST /api/apps`, `/api/apps/v1/*`, `/api/apps/{id}/rotate/*` or `/mcp` routes
> below exist in this repository's backend, so the examples will not work
> against a box. Treat this section as a specification for work that has not
> shipped.

The **Apps & Bots platform** is how you give an external program — a cron script, a webhook consumer, an LLM agent — a scoped credential to act on your account.

### Registering a bot

From your OS session (the management surface is session-authed; admins manage every app, other users manage their own):

```bash
curl -X POST https://os.example.com/api/apps \
  -H "Content-Type: application/json" \
  -d '{
    "name": "backup-bot",
    "description": "Nightly export of my notes folder",
    "scopes": ["apps:read", "apps:write"],
    "token_ttl": 0
  }'
```

The response includes the app's **`vat_` token, shown exactly once** — store it somewhere safe. Tokens are kept hashed on the box and checked in constant time. Management endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/apps` | List installed platform apps |
| `POST /api/apps` | Register an app (secrets shown once) |
| `GET` / `PUT` / `DELETE /api/apps/{id}` | Inspect, update, uninstall |
| `POST /api/apps/{id}/rotate/token` | Rotate the `vat_` token (shown once) |
| `POST /api/apps/{id}/rotate/secret` | Rotate the webhook signing secret |

### What a token can do

The runtime surface is Bearer-authenticated with the `vat_` token and rate-limited:

| Endpoint | Scope | Purpose |
|----------|-------|---------|
| `GET /api/apps/v1/auth.test` | any | Who am I — app id, name, scopes |
| `POST /api/apps/v1/act` | `apps:write` | Perform an action |
| `GET /api/apps/v1/read` | `apps:read` | Read content |
| `GET /api/apps/v1/events` | any | Live SSE event stream |
| `POST /api/apps/hooks/{id}` | — | Incoming webhook (the id is the secret) |

On Vulos OS the actions and read kinds are a conservative, Files-centric set: actions `files.folder.create`, `files.write`, `files.move`, `files.share`, `files.unshare`, `files.delete`; reads `files.list`, `files.read` (1 MiB cap), `files.versions`, `files.shares`, `files.shared-with-me`, plus read-only `apps` and `system` metadata. Every file operation is checked against the same per-node ACL as the Files app, acting as the account that installed the bot — it can touch exactly what you can, never more (see [FILES.md](FILES.md)). There is no shell access and no raw filesystem access through this surface.

### MCP

A Model Context Protocol server at `/mcp`, exposing the same registry and adapter to outside agents, is a **planned surface that is not built** — no `/mcp` handler exists in this repository or the control plane. See [ASSISTANT.md](ASSISTANT.md#connecting-an-outside-agent-mcp).

That is the whole bots story: there is no separate bot entity or bot framework — "bot" is what you call a platform app whose token is held by a program instead of a human.

---

## Sandboxed code execution (opt-in, read this before enabling)

The assistant can generate small interactive apps ("viewports") whose backend is a Python script. Executing that generated code is **disabled by default**. The flag:

```bash
VULOS_SANDBOX_ENABLED=1
```

When enabled, the sandbox service runs AI-generated Python backends from a pre-warmed pool (size tunable with `VULOS_SANDBOX_POOL_SIZE`), on loopback ports 9100–9199, reachable only through the gateway proxy at `/api/sandbox/{id}/`. Scripts get a stripped environment and a hard 5-minute timeout; runs are audit-logged; `POST /api/sandbox/stop`, `GET /api/sandbox/list`, and the proxy are admin-gated.

**Be clear-eyed about the risk posture.** The code's own documentation says this flag must not be set in production unless the host wraps the Python processes in kernel-level isolation (namespaces, seccomp) — the sandbox service itself provides process management, environment stripping, and timeouts, **not** a kernel security boundary. A legacy keyword blocklist exists in the code and is explicitly documented as trivially bypassable — it is not a security control. Treat `VULOS_SANDBOX_ENABLED=1` as "I accept arbitrary code execution by my AI on this host" and only enable it on a disposable or otherwise-isolated machine.

If it is off (the default), asking the assistant for an interactive app still yields the HTML/JS part; only the Python backend refuses to run, with a clear error naming the flag.

---

## Environment quick reference

| Variable | Default | Purpose |
|----------|---------|---------|
| `VULOS_APPS` | on | `off` disables the box's apps surface: every `/api/apps` route (app launch, stop, ports, traffic, proxy) and `/mcp`. Unrecognised values fail closed (disabled) |
| `VULOS_REGISTRY` | built-in | Alternative app-registry source |
| `VULOS_TRUST_ANCHOR` | `/etc/vulos/trust-anchor.pub` | Path to the root public key (the trust anchor) |
| `VULOS_RELEASE_CERT` | `/etc/vulos/release-cert.json` | Path to the root-signed release cert |
| `VULOS_REGISTRY_PUBKEY` | _(empty)_ | Direct Ed25519 verification key (forks; bypasses the cert chain) |
| `VULOS_REGISTRY_INSECURE` | unset | Skip signature verification. **Refused when `VULOS_ENV=prod`** |
| `VULOS_APP_CATALOG` | _(empty)_ | Remote catalog URL for the base app store |
| `VULOS_BUNDLED_APPS` | _(empty)_ | Override path to the bundled apps directory |
| `VULOS_DNS_API` | `noop` | Subdomain provisioning endpoint (unset = no DNS provider; point at your own) |
| `VULOS_BASE_DOMAIN` | `vulos.org` | Base domain for app subdomains |
| `VULOS_CADDY_DIR` | _(empty)_ | Emit Caddyfile snippets for custom domains |
| `VULOS_NGINX_DIR` | `/etc/nginx/vulos-apps` | Edge-cache config directory |
| `VULOS_CP_BASE_URL` | _(empty)_ | Control plane for `vk_` key introspection (unset = disabled) |
| `VULOS_CP_TOKEN` | _(empty)_ | Service token for introspection calls |
| `VULOS_SANDBOX_ENABLED` | unset | `1` enables AI code execution (see caveats above) |
| `VULOS_SANDBOX_POOL_SIZE` | `3` | Pre-warmed Python process pool size |

App data is included in the box's backup story — see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md). If an app won't launch or a domain won't verify, start with [TROUBLESHOOTING.md](TROUBLESHOOTING.md). Security posture and reporting live in [SECURITY.md](SECURITY.md), and general shell usage in [USER-GUIDE.md](USER-GUIDE.md).
