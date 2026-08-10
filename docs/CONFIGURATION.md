# Vulos Configuration Reference

This document consolidates all environment variables, config files, and runtime flags for the Vulos OS backend and frontend build.

---

## Backend: `--env` / `VULOS_ENV`

The most important flag. Controls the entire security and behaviour profile.

| Value | Who uses it | What changes |
|-------|-------------|--------------|
| `local` | Developer laptop | Binds `127.0.0.1`, skips TPM/fingerprint checks, allows self-signed certs, relaxes cookie flags, enables `/debug/env` endpoint |
| `dev` | CI / staging | Same as `local` but no debug endpoints; accepts staging cloud-broker pubkey alongside prod key |
| `prod` | Bare-metal / cloud | Binds all interfaces, full Secure cookies, hardware checks active, no debug endpoints |

```bash
# Flag
go run ./backend/cmd/server --env=local

# Environment variable (same effect)
VULOS_ENV=local go run ./backend/cmd/server
```

---

## Backend environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_ENV` | `prod` | Runtime environment: `local`, `dev`, `prod` |
| `DEPLOY_MODE` | `standalone` (unset) | Which deployment shape this box runs as: `standalone`, `os`, or `cloud`. Drives entitlement enforcement — `os`/`cloud` gate `vk_`-keyed app dispatch fail-closed; `standalone` leaves every app open — and selects the object-storage seam (`cloud` uses presigned URLs, the others mint STS prefix-scoped creds). An unrecognised value logs a warning and falls back to `standalone` rather than failing boot. See [ARCHITECTURE.md → Deployment modes](ARCHITECTURE.md#deployment-modes). |
| `VULOS_ALLOW_SOFTWARE_KEYSTORE` | _(empty)_ | Set `1` to let a cloud-adjacent box (`DEPLOY_MODE=os`/`cloud`) boot with the plaintext software device keystore instead of a TPM. Required only for the TPM-less Fly cloud runtime; ignored in `standalone`, where the software keystore is the documented fallback. |
| `PORT` | `8080` | HTTP server listen port |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTel OTLP endpoint; unset = tracing no-op |
| `S3_ENDPOINT` | `s3.amazonaws.com` | S3-compatible endpoint for the **backup vault** (Restic) |
| `S3_BUCKET` | `vulos-vault` | Backup vault bucket name |
| `S3_ACCESS_KEY` | _(empty)_ | Backup vault S3 access key |
| `S3_SECRET_KEY` | _(empty)_ | Backup vault S3 secret key |
| `S3_REGION` | `us-east-1` | Backup vault S3 region |
| `S3_USE_SSL` | `true` | Whether the backup vault talks to `S3_ENDPOINT` over TLS |
| `VULOS_RESTIC_PASSWORD` (canonical) / `RESTIC_PASSWORD` (fallback) | dev-only default `vulos-default-key` | Encryption passphrase for the Restic backup vault. **Fails closed in prod — but only on `VULOS_ENV=prod`, not on `--env=prod`** (`services/vault/vault.go:95,134` read the env var directly): refuses to `Init`/`Backup` while the passphrase is still the well-known dev default — set a real secret before enabling backups in production. |
| `AI_PROVIDER` | `ollama` | AI backend: `ollama`, `openai`, `claude`, or `custom` (any OpenAI-compatible endpoint). Note: the value is `claude`, **not** `anthropic`. |
| `AI_MODEL` | `llama3` | Model name/slug passed to the configured provider |
| `AI_API_KEY` | _(empty)_ | API key for `openai`/`claude`/`custom` providers (unused for `ollama`) |
| `AI_ENDPOINT` | `http://localhost:11434` | AI API endpoint (Ollama's default local port; point elsewhere for `openai`/`custom`) |
| `AI_SYSTEM_PROMPT` | built-in default prompt | Overrides the assistant's default system prompt entirely |
| `VULOS_AI_TIER` | _(empty, derived from endpoint locality)_ | Operator's sovereignty-tier declaration for the AI endpoint: `local`, `sovereign`, `brokered`, or `external`. Empty ⇒ derived from whether `AI_ENDPOINT` is loopback — nothing silently upgrades. |
| `VULOS_AI_MODE` | _(unset = auto)_ | Which llmux backend this box runs: `embedded` (in-process, no sidecar — the recommended default, needs no Postgres/Redis), `remote` (talk to an llmux gateway at `LLMUX_URL`), or `off`. Unset infers `remote` when `LLMUX_URL` is set, else `embedded` when `VULOS_LLMUX_CONFIG`/`LLMUX_CONFIG` names a config file, else unconfigured. See [ASSISTANT.md → Choosing where your AI runs](ASSISTANT.md#choosing-where-your-ai-runs). |
| `LLMUX_URL` (canonical) / `VULOS_LLMUX_URL` (alias) | _(empty)_ | **Remote mode only.** Base URL of an llmux gateway running as its own process; forces `AI_PROVIDER=custom` internally |
| `LLMUX_KEY` (canonical) / `VULOS_LLMUX_KEY` (alias) | _(empty)_ | API key/bearer token presented to the gateway — same meaning in embedded or remote mode |
| `VULOS_LLMUX_CONFIG` (canonical) / `LLMUX_CONFIG` (alias) | _(empty)_ | **Embedded mode only.** llmux's own JSON config file; optional, llmux's own environment (`OLLAMA_HOST`, `OPENAI_API_KEY`, …) fills in what it doesn't set |
| `DISPLAY` | _(inherited; no built-in default)_ | X11 display for app streaming (Xvfb). The backend reads it bare (`main.go:4813`) and never defaults it — the `:99` you may have seen comes from the container image (`Dockerfile:255`), not from Vulos |
| `VULOS_MAIL_URL` | `http://localhost:3000` | URL of the LilMail service (proxied at `/api/mail/url`) |
| `VULOS_OS_BUCKET_URL` | `https://os.vulos.org` | OS update bucket URL (baked into seed at build time; override for forks) |
| `VULOS_OS_AUTOUPDATE` | **on** (unset) | The background OS auto-update loop — **the only outbound connection a fresh, unconfigured box makes**. Set to `0`/`off`/`false`/`no`/`disable`/`disabled`/`none` (case-insensitive) for zero default egress; you then pull updates manually from Settings → OS Update. Any other value, including a typo, leaves it **on** — the fail-safe direction is updates flowing (`backend/services/osdist/update.go:132-142`) |

> **`--env=prod` and `VULOS_ENV=prod` are not interchangeable.** The `--env`
> flag is parsed into an internal value (`main.go:162,174`) and is **never**
> exported to the environment, while four prod gates read `os.Getenv("VULOS_ENV")`
> directly: `services/vault/vault.go:95` and `:134`,
> `cmd/server/routes_newfeatures.go:220`, and
> `internal/multiinstance/instancekey.go:95`. Those four arm **only** when the
> environment variable is set — passing `--env=prod` alone leaves them on the
> dev branch. Rows below that say "in `--env=prod`" and depend on one of those
> four are marked. Set `VULOS_ENV=prod` as well until this is fixed in code.


---

## Storage: two independent S3 configs

The `S3_*` variables above (no `VULOS_` prefix) configure the **backup vault**
(Restic snapshots) only. A second, separate configuration governs the
**per-user object-store gateway** that Files/Drive and the app-storage seam
actually read and write through — do not conflate the two.

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_STORAGE_ENDPOINT` (or legacy `VULOS_S3_ENDPOINT`) | _(empty)_ | Object-store endpoint for the per-user storage gateway |
| `VULOS_STORAGE_REGION` (or `VULOS_S3_REGION`) | `us-east-1` | Object-store region |
| `VULOS_STORAGE_ACCESS_KEY` (or `VULOS_S3_ACCESS_KEY`) | _(empty)_ | Object-store access key |
| `VULOS_STORAGE_SECRET_KEY` (or `VULOS_S3_SECRET_KEY`) | _(empty)_ | Object-store secret key |
| `VULOS_STORAGE_SESSION_TOKEN` | _(empty)_ | Optional STS session token |
| `VULOS_STORAGE_USE_SSL` (or `VULOS_S3_USE_SSL`) | `false` | Whether the storage gateway talks TLS to its endpoint |
| `VULOS_STORAGE_BUCKET` | _(empty, no default by design)_ | Per-user bucket name; deliberately has no fallback so a misconfigured deployment fails closed instead of writing into a guessed bucket |
| `VULOS_STORAGE_BUCKET_PREFIX` | `vulos-` | Prefix used when deriving a per-user bucket name |
| `VULOS_STORAGE_OS_BUCKET` (or `VULOS_S3_BUCKET`) | `vulos-cluster` | Shared OS-level bucket (updates, cluster metadata) |
| `VULOS_STORAGE_LOCAL_ROOT` | `~/.vulos/storage` | Local-filesystem fallback root when no object store is configured (standalone mode) |
| `VULOS_STORAGE_STS_ENDPOINT` | _(empty → self-host defaults to the box's own object-store endpoint)_ | STS endpoint for per-app credential isolation (see below) |
| `VULOS_STORAGE_STS_DISABLE` | _(unset)_ | Set to `1` to opt out of the self-host STS auto-default (advanced/test use only) |
| `VULOS_STORAGE_STS_ROLE_ARN` | _(empty)_ | Optional STS role ARN |
| `VULOS_STORAGE_STS_DURATION_SECONDS` | minter default | Optional STS credential lifetime override |
| `VULOS_STORAGE_BROKER_SECRET` | _(empty; fails closed)_ | Shared secret authenticating the storage gateway's own broker endpoints |

---

## GPU streaming

GPU tier is auto-detected at startup (`backend/services/gpu/gpu.go`). No configuration needed — the right encoder is chosen automatically.

| Tier | Hardware | Encoder | How to enable |
|------|----------|---------|---------------|
| 0 | None | VP8 (CPU) | Default — always available |
| 1 | Intel/AMD | H.264/AV1 via VA-API | Pass `--device /dev/dri` to Docker |
| 2 | NVIDIA | H.264/AV1 via NVENC | `--gpus all` + [NVIDIA Container Toolkit](GETTING-STARTED.md) |

Check the detected tier at runtime:

```bash
curl localhost:8080/api/browser/status | jq .gpu_tier
```

---

## Control-plane integration

Vulos OS works fully standalone; these variables only matter if you point the
box at an external control plane (self-hosted or otherwise) for sign-in,
region defaults, LAN-cert issuance, or brokered integrations. Leave them unset
and every one of those seams stays inert.

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_CP_URL` | _(empty)_ | Control-plane base URL — **highest precedence of the three** (`internal/gwurl/gwurl.go:86`) |
| `VULOS_CLOUD_URL` | _(empty)_ | Control-plane base URL — enrollment, sign-up proxy, region default. Used only if `VULOS_CP_URL` is empty |
| `VULOS_CLOUD_API_URL` | _(empty)_ | Alternate CP base. Used only if both above are empty — it is **last**, not a fallback for `VULOS_CLOUD_URL` specifically |
| `VULOS_GATEWAY_ALLOW_PRIVATE` | unset | Permit a private-network control-plane URL (`gwurl.go:89`) |

A **persisted runtime override** set through `gwurl.Set` (stored in `gateway.json`) beats all three environment variables (`gwurl.go:25-33`).
| `VULOS_CLOUD_ALLOW_INSECURE` | off | **Dev-only.** Allows plaintext/insecure control-plane connections — never set in production |
| `VULOS_DEVICE_ULID` | _(empty)_ | This device's ULID, sent to the control-plane/integrations client |
| `VULOS_REGION` | `eu` | Declared region for identity/storage-provisioning |
| `VULOS_STORE_ONLY` | `0` (serving) | Set `1`/`true`/`yes` to join the account as a **sync-only** member (NODE-CAP-01): this box replicates data and shows online, but is never a route/ingress target — the relay/DNS never send it client traffic. Explicit opt-in only (seeded on first self-registration; the Instances dashboard toggle changes it later). Use for a personal laptop/desktop that should sync but not serve. **Never** set this on a single-box install — it is its own only server. |

---

## Reachability: relay, streaming & real-time

Full reference: **[REACH.md](REACH.md)**. Setup recipes:
**[RELAY-SELF-HOST.md](RELAY-SELF-HOST.md)**.

### Box side — relay endpoints

Endpoints come from **one** of these sources, highest precedence first. They do
**not** merge: a box configured with a file ignores the others entirely.

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_RELAY_ENDPOINTS_FILE` | _(empty)_ | **Preferred.** Path to a JSON array of `{"url","name","token","region","priority"}` objects. Must be mode **0600** — the box refuses to start otherwise, because the file holds bearer tokens. |
| `VULOS_RELAY_ENDPOINTS` | _(empty)_ | The same JSON array inline, for platforms whose secret channel is the environment (Fly, Docker, Kubernetes). |
| `VULOS_RELAY_BASE_URL` | _(empty; no relay)_ | Legacy single-endpoint form. Still fully supported. Also read by GPU-streaming registration and cross-instance notify fan-out. |
| `VULOS_RELAY_NAME` | _(empty)_ | Legacy form: this box's name on the relay (one DNS label). |
| `VULOS_RELAY_TOKEN` | _(empty)_ | Legacy form: the bearer grant presented to the relay. |
| `VULOS_RELAY_ALLOW_INSECURE` | off | Permits a plaintext `http://` relay URL, **loopback hosts only**. Development affordance; unreachable from any HTTP surface. |
| `VULOS_RENDEZVOUS_URL` | _(empty; mDNS only)_ | **Comma-separated** list of rendezvous prefixes for cross-internet box discovery. Each becomes its own source; one that errors is skipped rather than failing the set. |

A box with none of these has **no relay** — the expected posture for a box with a
public IP or one that only needs its LAN. One bad entry refuses the whole set (with
an error naming the index) rather than silently starting with a subset.

Status: `GET /api/network/reach` (session-authed, never contains a token).

### Relay side — `vulos relay serve`

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `VULOS_RELAY_ADDR` | `-addr` | `:8443` | Listen address. Bind loopback when a TLS proxy fronts it. |
| `VULOS_RELAY_DOMAIN` | `-domain` | — | **Required.** Base domain; tunnels serve at `<name>.<domain>`. |
| `VULOS_RELAY_GRANTS_FILE` | `-grants-file` | _(empty)_ | JSON grants file, mode **0600**. |
| `VULOS_RELAY_GRANTS` | — | _(empty)_ | The same grants inline. **A relay with neither refuses to start** — it never runs open. |
| `VULOS_RELAY_REVOKED_FILE` | `-revoked-file` | _(empty)_ | Revocation list. Applied to **live** tunnels within 20s, not only to new ones. |
| — | `-cert` / `-key` | _(empty)_ | Terminate TLS in-process. Must be given together, or startup fails. |
| `VULOS_RELAY_PATH_MODE` | `-path-mode` | off | Also serve `/t/<name>/` for operators without wildcard DNS. Rewrites the path only — apps emitting absolute asset paths (including the Vulos shell) will break under it. |
| `VULOS_RELAY_TRUST_PROXY_HEADERS` | `-trust-proxy-headers` | **off** | Believe `X-Forwarded-For` (rightmost entry). Turn on **only** behind a trusted TLS terminator; an internet-facing relay that believes it lets any client choose its own source IP. |
| `VULOS_RELAY_ADMIN_ADDR` | `-admin-addr` | `127.0.0.1:9090` | Admin/status listener. Serves `/tunnels` (the roster) and `/tls-ask` (the automatic-TLS gate). |
| `VULOS_RELAY_ADMIN_TOKEN` | `-admin-token` | _(empty)_ | **Required** for a non-loopback admin bind — the roster names every registered box. |
| — | `-max-agents` | 64 | Concurrent tunnel cap. |
| `VULOS_RELAY_NODE_ID` | `-node-id` | _(empty)_ | Stable identifier shown to agents. |
| `VULOS_RELAY_DIRECT_PROBE` | `-direct-probe` | off | Let agents advertise a direct endpoint, verified by an SSRF-guarded nonce-echo ownership probe before it is ever published. |
| `VULOS_RELAY_RENDEZVOUS` | `-rendezvous` | off | Also serve the discovery role (apex host only). |
| `VULOS_RELAY_RENDEZVOUS_PREFIX` | `-rendezvous-prefix` | `/rendezvous` | Mount prefix for the discovery role. |
| `VULOS_RELAY_RENDEZVOUS_ORIGINS` | `-rendezvous-origins` | _(any)_ | Comma-separated browser origins for the discovery role. Not access control — writes are signature-gated. |
| — | `-grace` | `20s` | Graceful-shutdown budget. `SIGTERM` drains first: no new tunnels, existing ones keep serving. |

Mint a grant with `vulos relay grant <name>`.

### Other reachability & streaming variables

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_GPU_HOST` | disabled | Set truthy (`1`/`true`/`yes`) to enable direct-IP GPU streaming host mode |
| `VULOS_GPU_ADVERTISE_HOST` | _(empty)_ | Hostname the GPU host advertises to the relay |
| `VULOS_GPU_VENDOR` | auto-detected | GPU vendor hint |
| `VULOS_STREAM_STRICT_INPUT_GATE` | unset (see note) | Forces fail-closed gating of remote input injection on streamed app sessions (AUTH-13). If explicitly set, `1` = strict/fail-closed, anything else = explicitly non-strict. **If left entirely unset, the server now defaults to strict in `--env=prod`** and non-strict in `local`/`dev` — set explicitly to override either way. |
| `VULOS_LAN_ENABLE` | off | Enables the LAN-reachability HTTPS listener |
| `VULOS_LAN_CERT` / `VULOS_LAN_KEY` | _(empty)_ | TLS cert/key paths for the LAN listener |
| `VULOS_DIRECT_ENABLE` | off | Enables the direct-IP high-performance public TLS listener (see `backend/internal/directlisten`) |
| `VULOS_DIRECT_HOSTNAME` / `VULOS_DIRECT_ADDR` / `VULOS_DIRECT_CERT_FILE` / `VULOS_DIRECT_KEY_FILE` / `VULOS_DIRECT_ACME_EMAIL` | _(empty)_ | Direct-listener networking + cert config, only read when enabled |

---

## Push notifications

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_PUSH_VAPID_PUBLIC` / `VULOS_PUSH_VAPID_PRIVATE` | _(empty; push disabled)_ | VAPID key pair for direct Web Push. Generate your own — never share a key across boxes |
| `VULOS_PUSH_VAPID_KEYFILE` | _(empty)_ | Path to load/generate the VAPID key pair at (written mode 600) when the explicit keys above are not set |
| `VULOS_PUSH_VAPID_SUBJECT` | `mailto:admin@localhost` | RFC 8292 contact subject sent with push |
| `MAIL_EDGE_CP_SECRET` (canonical) / `VULOS_PUSH_CP_SECRET` (fallback) | _(empty; registrar inert on self-host)_ | HMAC secret authenticating this cell to the CP for managed push registration |
| `VULOS_PUSH_CP_REGISTER_URL` | _(empty)_ | CP endpoint this cell registers push subscriptions with (managed/cloud only) |

---

## Integrations / OAuth (self-host)

Only needed when self-hosting external-account integrations yourself, rather
than going through a control plane's brokered OAuth flow.

| Variable | Default | Description |
|----------|---------|-------------|
| `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` | _(unset; provider unavailable)_ | Your own Google OAuth app credentials (Gmail/Calendar/Drive scopes) |
| `MICROSOFT_OAUTH_CLIENT_ID` / `MICROSOFT_OAUTH_CLIENT_SECRET` | _(unset; provider unavailable)_ | Your own Microsoft OAuth app credentials (Graph Mail/Calendar/Files/Contacts) |
| `DROPBOX_OAUTH_CLIENT_ID` / `DROPBOX_OAUTH_CLIENT_SECRET` | _(unset; provider unavailable)_ | Your own Dropbox OAuth app credentials |
| `OAUTH_REDIRECT_BASE` | `http://localhost:8080` | Externally-reachable base URL used to build each provider's OAuth redirect URI (`<base>/api/integrations/{provider}/callback`) — **must** be set to your real public URL in production |
| `INTEGRATIONS_KEK` | none; **required in production** | Base64, 32-byte key-encryption-key used to encrypt OAuth refresh tokens at rest. In prod an unset/invalid KEK **disables the self-host integrations surface** — the routes are not registered and startup logs `[integrations] SELF-HOST path DISABLED` (`routes_integrations_selfhost.go:58-64`). The box still boots |

Google Cloud Storage (GCS) has no local client-secret variable — it is
accessed via CP-brokered short-lived bearer tokens (when a control plane is
configured), not local OAuth credentials.

---

## Security & safety switches

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_DISABLE_EXEC` | unset (exec allowed) | Any non-empty value disables all privileged exec endpoints (`/api/exec` and related) at runtime |
| `VULOS_SANDBOX_ENABLED` | disabled | Opt-in for arbitrary AI-generated-code execution (the Python viewport sandbox). Real kernel isolation (namespaces/seccomp) is **not yet implemented** for this path — only enable it in an environment you understand the risk of |
| `VULOS_SANDBOX_POOL_SIZE` | `3` (forced to `0` when the sandbox is disabled) | Pre-warmed Python process pool size for the sandbox |
| `VULOS_DATA_DIR` | `$HOME/.vulos` | Root of all on-disk state — DBs, auth, logs, models, slots. Relative paths are made absolute (`internal/datadir/datadir.go`) |
| `VULOS_FILES_TRASH_RETENTION` | `720h` (30 days) | How long a soft-deleted Files node is kept before the tombstone-purge sweep reclaims its bucket bytes. Go duration string (e.g. `168h` for 7 days) |
| `VULOS_RPID` | `localhost` (dev only) | WebAuthn Relying Party ID for passkeys. In prod the server **keeps booting with passkeys disabled** (`##### PASSKEYS DISABLED #####`, `main.go:1292-1301`) rather than refusing to start — so password login silently becomes the only front door. Set it to your real domain |
| `VULOS_ORIGIN` | `http://localhost:8080` (dev only) | WebAuthn expected origin for passkeys. Same prod fail-closed behavior as `VULOS_RPID` |
| `VULOS_METRICS_TOKEN` | _(empty)_ | Optional bearer token for scraping `GET /metrics` without an owner session |
| `VULOS_BOOTSTRAP_ADMIN_EMAIL` | _(empty)_ | Email address permitted to bootstrap the very first admin account |
| `BURST_HEARTBEAT_SECRET` | unset (endpoint returns 503) | Shared secret gating the managed-box passphrase-injection endpoint (`X-Burst-Secret` header; no session cookie) |

---

## Per-app storage isolation (STS)

Each user gets their own bucket (`vulos-<userID>`), so cross-**user** isolation
always holds. Cross-**app** isolation **within a single user** is enforced by
short-lived, prefix-scoped credentials minted via STS, when an STS endpoint is
available:

```bash
VULOS_STORAGE_STS_ENDPOINT=https://sts.example.com   # e.g. your own MinIO's AssumeRole API
VULOS_STORAGE_STS_ROLE_ARN=arn:...                   # optional
VULOS_STORAGE_STS_DURATION_SECONDS=900               # optional (default minter value)
```

A storage-permitted app **never** receives a static, full-bucket credential: if
STS is unavailable for any reason (no object store configured at all, or
`VULOS_STORAGE_STS_DISABLE=1`), the app simply gets no injected credential
(fail-closed) and must call `POST /api/storage/presign` for a short-lived,
object-scoped grant instead — the same mechanism the Files control plane uses.
If an object store IS statically configured and at least one installed app
declares the `storage` permission, the server **aborts at boot** rather than
silently degrading when STS ends up unavailable in that combination.

When STS is available, the gateway hands apps short-lived credentials scoped
down to the app's own `<userID>/<appID>/` prefix. In cloud deployments (Tigris
and similar stores with no STS/AssumeRole), apps use the presign endpoint
instead of header-injected credentials — see [FILES.md](FILES.md) and
[SECURITY.md](SECURITY.md).

---

## Vite / frontend build

The Vite dev server runs on `:5173` and proxies `/api` and `/app` to the backend on `:8080`.

All of these run **from `frontend/`** — there is no `package.json` at the repo root.

| Command | What it does |
|---------|-------------|
| `npm run dev` | Vite dev server with HMR |
| `npm run build` | Production build into `frontend/dist/` |
| `npm run preview` | Preview the production build locally |
| `npm run test` | Vitest unit tests |
| `npm run typecheck` | `tsc --noEmit` — a separate gate; `npm run build` strips types without checking them |
| `npm run lint` | ESLint |
| `npm run test:e2e` | Playwright (`test:e2e:ui` for the UI runner) |
| `npm run screenshots` | Playwright screenshotter → `docs/screenshots/` |
| `npm run gen:wire-types` | Regenerate wire types (`:check` variant verifies they are current) |

---

## Bare-metal seed variables

These are baked into the OS image at build time by `build.sh` and cannot be changed at runtime without a rebuild.

| Variable | Purpose |
|----------|---------|
| `VULOS_OS_BUCKET_URL` | OS update bucket URL (default `https://os.vulos.org`) |
| Trust anchor key | Ed25519 public key baked into `/etc/vulos/trust-anchor.pub` and the initramfs; controls which squashfs updates are accepted |

For forks, supply both your own trust anchor key and bucket URL at build time:

```bash
VULOS_OS_BUCKET_URL=https://os.myfork.example ./build.sh --live
```

See [REPRODUCIBLE-BUILDS.md](REPRODUCIBLE-BUILDS.md) for the full signing and verification workflow.

---

## Bare-metal init (`backend/cmd/init`)

These are runtime environment variables for the bare-metal init process
(distinct from `backend/cmd/server` and from the build-time seed variables
above) — the process that boots the display session before the OS backend
starts.

| Variable | Default | Description |
|----------|---------|-------------|
| `VULOS_BOOT_THRESHOLD` | `3` | Consecutive failed boots before `init` rolls back to the last-known-good OS slot. The rollback is written to `boot-state.json`, and **that now decides which slot boots**: the initramfs reads the file at init-bottom and boots the slot it names (OSDIST-FLIP-01). The bootloader entry is still written once at install time and still says slot-a — it is no longer what chooses. Values must parse as a positive integer; anything else silently falls back to `3` (`backend/cmd/init/main.go:149`). See [ARCHITECTURE.md → OS distribution](ARCHITECTURE.md#os-distribution-bare-metal) |
| `VULOS_PREWARM_BROWSER` | auto-detected from host profile | Whether to pre-warm the Chromium/WPE browser process at boot |
| `VULOS_NATIVE_MODE_V2` | off | Opts into the v2 native-launch/labwc window path instead of the v1 always-stream/cage path. Also settable via the `vulos.native-mode=v2` kernel cmdline parameter |

---

## Observability

| Endpoint | Description |
|----------|-------------|
| `GET /metrics` | Prometheus textfile (`vulos_*` namespace) |
| OTel traces | Active when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; uses `backend/internal/obs.Start()` |

---

## Service worker cache names

See [SW-CACHE-VERSIONS.md](SW-CACHE-VERSIONS.md) for the cross-repo coordination table. Current name for this repo: `vulos-os-shell-v1`.
