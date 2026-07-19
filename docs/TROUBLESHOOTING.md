# Troubleshooting

Common Vulos OS failures, organized as symptom, cause, and fix. Every log line, error string, endpoint, environment variable, and unit name in this chapter is quoted from the actual code and scripts, so you can grep your logs for the exact text you see. Start with the logs section, find your area, and work through the checks in order.

Related chapters: [GETTING-STARTED.md](GETTING-STARTED.md), [CONFIGURATION.md](CONFIGURATION.md), [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md), [CLOUD.md](CLOUD.md), [NETWORKING.md](NETWORKING.md), [ASSISTANT.md](ASSISTANT.md), [APPS.md](APPS.md), [FILES.md](FILES.md), [SECURITY.md](SECURITY.md), [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md), [PEERING.md](PEERING.md), [USER-GUIDE.md](USER-GUIDE.md).

---

## Where the logs live

The Go backend writes everything to standard output/error. Where that ends up depends on how you installed Vulos.

**Self-hosted bundle (systemd).** The installer (`scripts/install-vulos.sh`) creates these units:

| Unit | What it is |
|------|------------|
| `vulos.service` | OS backend (port 8443) |
| `vulos-mail.service` | Mail server (ports 25, 587, 8444) |
| `vulos-office.service` | Office backend (port 8445) |
| `vulos-fabric.service` | Shared fabric identity init (oneshot, runs first) |
| `vulos-minio.service` | Local object store (only if you chose local MinIO) |
| `vulos-bundle.target` | Sentinel target for the whole stack |

```bash
sudo systemctl status vulos-bundle.target        # everything up?
sudo journalctl -u vulos -n 200 --no-pager       # OS backend log
sudo journalctl -u vulos -u vulos-mail -u vulos-office -n 100
sudo journalctl -u vulos -f                      # follow live
```

**Docker.** The container is conventionally named `vulos`:

```bash
docker logs vulos --tail 200
docker logs vulos -f
```

**Dev (`./dev.sh` or `go run ./backend/cmd/server`).** Logs go straight to your terminal. On a clean start you will see one of:

```
vulos server listening on :8080 (env=local, no TLS)
vulos server listening on :8080 with TLS (env=prod, cert=...)
```

**Per-app logs.** Each launched app's stdout/stderr goes to its own file, rotated at a size threshold:

```bash
cat ~/.vulos/logs/<appId>.log        # current
cat ~/.vulos/logs/<appId>.log.old    # previous rotation
```

**Ports.** Docker and dev default to `8080` (the `PORT` env var, default `"8080"`). The self-host bundle runs the OS backend on `8443`. Substitute accordingly in the `curl` examples below.

### Health and metrics

`GET /api/health` is public and needs no auth. It returns `200` with `"status":"ok"`, or `503` with `"status":"degraded"` and a per-check breakdown:

```bash
curl -s http://localhost:8080/api/health | jq
```

Checks and their thresholds:

- `data_dir_writable` — a probe file is written under `~/.vulos`; a read-only or full disk shows `degraded: <error>`.
- `disk_space` — degrades below 500 MiB free: `degraded: only N MiB free`.
- `sync_lag` — with S3 sync enabled, degrades when the last sync is more than 10 minutes old: `degraded: last sync 12m0s ago`. Without S3 it reports `ok: sync disabled (no S3 configured)`.

`GET /metrics` (Prometheus) is **not** public. Without an admin session or a scrape token it returns `403` with `metrics are owner-only`. To let a scraper in, set `VULOS_METRICS_TOKEN` and send it as a bearer token:

```bash
curl -s -H "Authorization: Bearer $VULOS_METRICS_TOKEN" http://localhost:8080/metrics | grep vulos_
```

If your metrics look "empty", check you are hitting the right port and that you are authorized — an unauthorized scrape is a 403, not an empty page.

---

## First boot and setup

These match the quick table in [GETTING-STARTED.md](GETTING-STARTED.md#troubleshooting), with more detail.

**Symptom:** first boot hangs, or input (keyboard/mouse into streamed apps) does nothing.
**Likely cause:** `/dev/uinput` is not available in the container. The server tries to create it itself (you'll see `[init] created /dev/uinput` in the log when that works) and otherwise falls back to the much slower `xdotool` path — or input fails entirely in an unprivileged container.
**Fix:** pass the device through:

```bash
docker run ... --device /dev/uinput ...
```

**Symptom:** streamed apps crash or render corrupt frames.
**Likely cause:** shared memory too small for the compositor/browser.
**Fix:** run with `--shm-size=1g` (the documented default in all install commands).

**Symptom:** container fails to start; `docker logs vulos` shows a bind error.
**Likely cause:** port 8080 already taken on the host.
**Fix:** map a different host port (`-p 9090:8080`) or set `PORT` for a bare-metal run.

**Symptom:** bundle installer reports a checksum mismatch.
**Fix:** re-run `curl -fsSL https://get.vulos.org | sudo bash` — the installer is idempotent and re-downloads from scratch.

**Symptom:** mail doesn't receive; port 25 times out from outside.
**Likely cause:** most residential and many cloud ISPs block inbound port 25.
**Fix:** use a VPS with port 25 open (Hetzner, OVH, etc.) or configure a mail relay. See [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md).

---

## Cloud enrollment and unified sign-in

Enrolling the box with Vulos Cloud is an RFC 8628 device flow: the box POSTs to the control plane's `/enroll/start`, shows you a `user_code` + verification URL, then polls `/enroll/poll` until you approve the device in the cloud console. The control plane base URL comes from `VULOS_CLOUD_URL` (falling back to `VULOS_CLOUD_API_URL`, default `https://api.vulos.org`). The enrolled identity is persisted at `~/.vulos/auth/integrations/identity.json` with the sealed private key next to it (`enroll_key.sealed`).

Check enrollment state at any time:

```bash
curl -s http://localhost:8080/api/auth/cloud/enroll/status | jq
# {"state":"idle" | "pending" | "approved" | "error" | "unavailable", ...}
```

**Symptom:** starting enrollment returns `502` with `could not start device enrollment: cloudenroll: start status 403`.
**Likely cause:** this is the currently-known failure mode — the control plane's browser-oriented request protections can reject the box's server-side enrollment POSTs with a 403 before the enrollment logic ever sees them. Nothing is wrong with your box or account.
**Fix:** there is no box-side setting that fixes this; the fix is on the cloud side. Retry later, and keep both the OS and your cloud control plane up to date. If you run your own control plane, update it to a version that accepts device-enrollment posts from non-browser clients.

**Symptom:** enrollment sits in `"state":"error"` with `cloudenroll: enrollment denied by owner` — but nobody denied anything.
**Likely cause:** the box maps *any* HTTP 403 from `/enroll/poll` to "denied by owner". A control-plane-side 403 (the same known failure mode as above) is indistinguishable from a real denial, so the message can be misleading.
**Fix:** if you (the account owner) did not actually press Deny in the cloud console, treat this like the 403-on-start case: retry later / update the cloud.

**Symptom:** `could not start device enrollment: cloudenroll: timed out waiting for the cloud to issue a user code`.
**Likely cause:** the control plane did not answer `/enroll/start` within 20 seconds — network problem, wrong `VULOS_CLOUD_URL`, or the CP is down.
**Fix:** verify the box can reach the control plane (`curl -sv https://api.vulos.org/ -o /dev/null`), and check `VULOS_CLOUD_URL` / `VULOS_CLOUD_API_URL` in your config.

**Symptom:** `cloudenroll: enrollment grant expired` or `cloudenroll: enrollment grant expired before approval`.
**Likely cause:** the approval window lapsed before the owner entered the user code.
**Fix:** start enrollment again and approve promptly at the verification URL shown.

**Symptom:** enrollment start returns `503` with `device enrollment is not available on this box`.
**Likely cause:** the device keystore failed to open at boot, so the whole enrollment machinery was never wired. Look for `passkeys: devicekey unavailable, server-side passkeys disabled: ...` in the boot log.
**Fix:** check permissions on `~/.vulos/auth/tpm` and restart the server.

**Symptom:** boot log shows `[integrations] cloud enrollment load: cloudenroll: corrupt identity pubkey` (or `corrupt identity cert`, or `cloudenroll: cannot unseal device key`).
**Likely cause:** the persisted identity under `~/.vulos/auth/integrations/` is damaged, or the sealing key changed (e.g. the TPM/software keystore was reset).
**Fix:** move the two files aside and re-enroll the device:

```bash
mv ~/.vulos/auth/integrations/identity.json ~/.vulos/auth/integrations/identity.json.bad
mv ~/.vulos/auth/integrations/enroll_key.sealed ~/.vulos/auth/integrations/enroll_key.sealed.bad
```

A healthy enrolled boot logs `[integrations] using owner-attested device cert (ulid=...)`, and a fresh enrollment logs `[cloudenroll] device enrolled (ulid=...) — cert wired for integrations + identity claim`.

### Signing in with a cloud account (`POST /api/auth/cloud/login`)

The login endpoint returns structured errors. What they mean:

| Response | Meaning / fix |
|----------|---------------|
| `401 invalid_credentials` | The cloud rejected the email/password pair. |
| `401 invalid_totp` | Wrong TOTP or recovery code. |
| `200 {"step":"totp_required"}` | Not an error — resubmit with `totp_code`. |
| `200 {"step":"passkey_required"}` | Your cloud account is passkey-only; a WebAuthn ceremony cannot be driven from the box. Sign in with a password-capable method or use the cloud console directly. |
| `200 {"step":"enrollment_required"}` | The box is not enrolled yet — run the enrollment flow above first. |
| `403 cloud_reauth_required` | Your cloud session is too old to mint a device token; sign in to the cloud again with your password. |
| `429 the cloud is rate limiting sign-in attempts — try again shortly` | Back off and retry. |
| `502 could not reach Vulos Cloud — check your network connection` | Transport failure between box and control plane. |
| `503 cloud broker key mismatch — refusing to sign in (re-enroll this device or contact your admin)` | The cloud served a broker signing key **different** from the one pinned on this device. This fails closed on purpose (a silently accepted key change is the shape of a key-swap attack). If the change is legitimate, re-enroll the device or set `VULOS_CLOUD_BROKER_PUBKEY` explicitly. |
| `503 cloud login not configured on this device` | No broker key / not enrolled — cloud login was never set up here. |
| `401 cloud token has expired` / `invalid cloud token signature` / `cloud token is not valid for this device` / `cloud token has already been used` | The minted login token failed local verification. Expired usually means clock skew — check the box's time (`timedatectl`). |

If your control plane runs at a nonstandard origin and rejects the box's requests, `VULOS_CLOUD_ORIGIN` overrides the `Origin` header the box's cloud client sends.

---

## Box not reachable remotely (relay and direct)

Default reachability is via the Vulos relay: a separate relay agent (not embedded in the OS server) keeps a tunnel open to the relay, and clients fall back to it whenever a direct connection is not available. The agent is configured through `VULOS_RELAY_BASE_URL`, `VULOS_RELAY_NAME`, and `VULOS_RELAY_TOKEN` — if remote access fails entirely, check the relay agent's own logs and those variables first. See [NETWORKING.md](NETWORKING.md) and [CLOUD.md](CLOUD.md).

The **direct fast path** (`VULOS_DIRECT_ENABLE=1`) is opt-in and only for boxes with a real public endpoint. Clients try direct first and fall back to the relay on failure, so a broken direct listener degrades speed, not access. Its status:

```bash
curl -s http://localhost:8080/api/network/direct | jq   # requires a session
```

What the boot log tells you:

**Symptom:** `[direct] disabled: network mode is LAN-only (external listeners blocked) — set connection mode to fabric/direct/own to use the direct fast path`
**Cause/fix:** you set the box's connection mode to "local" in Settings, which blocks all external listeners even when `VULOS_DIRECT_ENABLE=1` is set. Change the connection mode if you actually want public exposure.

**Symptom:** `[direct] disabled: directlisten: ACME cert mode requires a public Hostname` (or `... requires ACMECacheDir`, or `directlisten: provided cert mode requires CertFile and KeyFile`).
**Cause/fix:** incomplete configuration. ACME mode needs `VULOS_DIRECT_HOSTNAME`; provided-cert mode needs `VULOS_DIRECT_CERT_FILE` and `VULOS_DIRECT_KEY_FILE`. See [CONFIGURATION.md](CONFIGURATION.md).

**Symptom:** `[direct] failed to start public listener: directlisten: listen on :443: ... permission denied` (or `address already in use`).
**Cause/fix:** the default listen address is `:443`, which needs either root, `CAP_NET_BIND_SERVICE`, or a free port. Set `VULOS_DIRECT_ADDR` to another port, or grant the capability.

**Symptom:** listener starts (`[direct] public listener up on ... (endpoint=https://...)`) but then:

```
[direct] self-reachability check for https://box1.example.net did not pass yet (directlisten: unreachable) — the relay will re-verify; clients fall back to the relay until it does
```

**Cause:** the box fetched its own probe path (`/_vulos-direct/probe`) over the advertised public endpoint and got no valid answer — a firewall or NAT is silently dropping inbound traffic, or DNS for the hostname doesn't point at this box yet. This is not fatal: clients simply stay on the relay.
**Fix:** open/forward the port, fix the DNS record, and wait for `[direct] self-reachability check passed: <endpoint> is reachable and ownership-provable`. You can test the probe yourself:

```bash
curl -s -H "X-Vulos-Direct-Probe: hello" https://box1.example.net/_vulos-direct/probe
# a healthy listener echoes back: hello
```

**LAN-only access with the internet down:** set `VULOS_LAN_ENABLE=1` and the box advertises `vulos.local` over mDNS and serves HTTPS on the LAN; a healthy start logs `[lan] reachable at https://... (mDNS vulos.local)`.

---

## Assistant / LLM gateway unavailable

All OS chat/LLM traffic goes through the llmux gateway. The canonical variable is `LLMUX_URL` (alias `VULOS_LLMUX_URL`; key `LLMUX_KEY` / `VULOS_LLMUX_KEY`). Without it the AI routes exist but refuse to work.

**Symptom:** the assistant returns errors; `/api/ai/*` calls fail with `503` and the body `{"error":"gateway_unconfigured: set LLMUX_URL"}`. The boot log shows:

```
[llmuxclient] LLMUX_URL unset — /api/ai/* routes will return 503 (set LLMUX_URL, or its alias VULOS_LLMUX_URL, to enable)
```

**Fix:** set `LLMUX_URL` to your gateway (a trailing `/v1` is tolerated and stripped) and restart. Model listing surfaces the same condition as `"chat_models_error": "no llmux gateway configured (LLMUX_URL unset)"`.

**Symptom:** `502` with `gateway_error: llmuxclient: chat request failed: ...` or `gateway_error: llmuxclient: gateway returned 500: ...`.
**Likely cause:** `LLMUX_URL` is set but the gateway is down, unreachable, or itself failing (e.g. its ollama backend is not running).
**Fix:** verify the gateway directly:

```bash
curl -s "$LLMUX_URL/v1/models" -H "Authorization: Bearer $LLMUX_KEY" | jq
```

**Symptom:** `422 model_not_found` — the requested model isn't in the gateway's allowlist. `429 rate_limit_exceeded; retry after Ns` — the box-side AI rate limit tripped; wait and retry.

**Legacy direct-Ollama path** (`AI_PROVIDER=ollama`, the default): the endpoint comes from `AI_ENDPOINT`, defaulting to `http://localhost:11434`. In production with the default unchanged the boot log warns:

```
[ai] WARNING: AI_ENDPOINT is unset in prod — using localhost:11434 which is almost certainly wrong; set AI_ENDPOINT or AI_PROVIDER=claude
```

Health-check failures on this path read `ollama unreachable: ...`; a failing generation reads `ollama stream error <status>: <body>`. Check ollama itself:

```bash
curl -s http://localhost:11434/api/tags | jq   # is ollama up, which models are pulled
```

### Retrieval falls back to lexical (semantic search "doesn't work")

The mail assistant's semantic index only runs on a **local** ONNX embedder — by design it refuses any embedder that can't certify on-instance operation (`assistant: embedder is not certified on-instance — refusing to index mail (sovereignty)`). When no local model is present it silently uses lexical (keyword) retrieval instead. This is a feature, not a crash — but search quality drops. The boot log states the mode plainly:

```
[assistant] no local ONNX model in /home/you/.vulos/models — using sovereign lexical retrieval (no external embedding API)
[assistant] ONNX embedder init failed: ... — semantic mail index disabled (lexical fallback)
[assistant] semantic mail index enabled (on-instance ONNX embeddings, on-box vector store)
```

The current mode is also exported as a `/metrics` gauge (`semantic|degraded|lexical`). To get to `semantic`, see the next section.

---

## Embedder / model problems (`~/.vulos/models`)

The embedding auto-discovery looks in `~/.vulos/models/` for, in priority order: `all-MiniLM-L6-v2.onnx`, `model.onnx`, `e5-small.onnx` — plus `tokenizer.json` next to the model. Three states result:

| RAG mode | Condition |
|----------|-----------|
| `semantic` | model **and** `tokenizer.json` present — genuine semantic retrieval |
| `degraded` | model present but **no** `tokenizer.json` — deterministic hash fallback, weak vectors |
| `lexical` | no model — pure on-box keyword retrieval |

**Symptom:** semantic answers are poor and the app log (or `~/.vulos/logs/`) shows on stderr:

```
vula-embed: no tokenizer.json found next to the model; using the deterministic DEGRADED fallback tokenizer -- semantic quality is reduced, prefer lexical retrieval
```

**Fix:** install the model's real `tokenizer.json` beside the `.onnx` file (the Settings model manager has an import for exactly this).

**Symptom:** model file is there but the embedder never activates.
**Likely cause:** the embed path shells out to `python3` with `onnxruntime` and `tokenizers`; if either is missing the call fails and retrieval quietly falls back. The model manager reports this honestly (`python_deps` in the listing, with an install hint).
**Fix:**

```bash
pip install onnxruntime tokenizers numpy
ls -la ~/.vulos/models/            # confirm the .onnx name matches a discovered name
```

**Symptom:** importing a model through the UI fails with `models: artifact failed validation` plus a hint such as `looks like a zip archive, not a raw .onnx`, `looks like JSON, not an .onnx model`, `looks like HTML/XML, not an .onnx model` (usually a saved error page), or `looks gzip-compressed; decompress the .onnx first`.
**Fix:** the message says what you actually uploaded. Extract/decompress and import the raw `.onnx`. Size caps: 600 MiB for a model, 32 MiB for a tokenizer (`models: artifact exceeds size limit`).

**Symptom:** the one-click catalog download fails with `models: downloaded artifact failed checksum verification`.
**Cause/fix:** the downloaded bytes did not match the pinned SHA-256 — a truncated or tampered download. It fails closed and installs nothing; retry. `models: catalog download URL is not an allowed https host` means the catalog entry points somewhere the downloader refuses to fetch from (it only follows pinned https hosts); `models: fetch model.onnx: HTTP 404` and friends are the upstream host failing.

Imports and downloads are atomic (temp file + rename), so a failed or interrupted install never leaves a half-written model for the embedder to load. See [ASSISTANT.md](ASSISTANT.md) for the full model-management story.

---

## App install and launch failures

**Symptom:** every native app launch fails; the log shows `[appnet] start FAILED for <app>: ...` or `create namespace: create netns: ...` errors.
**Likely cause:** app networking uses real Linux network namespaces (`ip netns add`, veth pairs, iptables). In Docker this requires `CAP_NET_ADMIN` and the `iproute2` tools; without them every namespace step fails.
**Fix:** run the container with `--cap-add NET_ADMIN` (see the same note in [GETTING-STARTED.md](GETTING-STARTED.md) and DEPLOY.md). A healthy launch logs:

```
[appnet] namespace vulos_<app> created: host:<port> → 10.200.x.2:<port>
[appnet] starting <app>: ip netns exec vulos_<app> sh -c "..."
[appnet] started <app> pid=...
```

A related soft warning, `[appnet] warning: could not enable ip_forward: ...`, means apps may launch but have no outbound network.

**Symptom:** installing from the store fails with `no app catalog configured (set VULOS_APP_CATALOG)`.
**Fix:** point `VULOS_APP_CATALOG` at a catalog URL/file.

**Symptom:** `checksum mismatch for <app>: got <hash>, want <hash>` during install.
**Cause/fix:** the download did not match the catalog's pinned hash; the install is rejected. Retry — if it persists, the catalog and the artifact are genuinely out of sync upstream.

**Symptom:** `<app> is already being installed` — a concurrent install of the same app is in flight; wait for it.

**Symptom (boot log, storage):**

```
[storage] STS not configured — storage-permitted apps will NOT receive static full-bucket credentials (fail-closed); they must use the presign endpoint (POST /api/storage/presign) for object-scoped access instead.
```

**Meaning:** self-host now defaults `VULOS_STORAGE_STS_ENDPOINT` to the box's own object-store endpoint automatically whenever one is configured (MinIO serves STS on the same port), so this message normally only appears when no object store is configured at all (nothing to protect — apps fall back to local/standalone storage) or `VULOS_STORAGE_STS_DISABLE=1` is set. A storage-permitted app is never handed a static, full-bucket credential any more — without STS it simply gets no injected credential and must call `POST /api/storage/presign` for a short-lived, object-scoped grant instead. If an object store IS statically configured and at least one installed app declares the `storage` permission, the server now **aborts at boot** (`[storage] ABORT: ...`) rather than silently degrading.
**Fix:** leave `VULOS_STORAGE_STS_ENDPOINT` unset (it self-configures against your own MinIO) or set it explicitly for a non-default STS endpoint; see [SECURITY.md](SECURITY.md).

App-specific misbehavior after a successful launch: read the app's own log at `~/.vulos/logs/<appId>.log`. See [APPS.md](APPS.md).

---

## DNS write failures (public app subdomains)

Publishing an app publicly provisions `{app}--{profile}.{instanceID}.vulos.org` through a DNS API (`VULOS_DNS_API`, default `https://api.vulos.org/dns/provision`; base domain `VULOS_BASE_DOMAIN`; instance from `VULOS_INSTANCE_ID`) and writes a Caddy vhost snippet under `/etc/caddy/vulos-apps` (`VULOS_CADDY_DIR`).

**Symptom:** in production the publish/deployment routes are simply missing (404), and the boot log shows:

```
[appnet/subdomain] DNS provisioning DISABLED in prod: VULOS_DNS_API is unset — refusing to register routes so customers are not falsely told their domain is being provisioned
```

**Fix:** set `VULOS_DNS_API` (and `VULOS_CADDY_DIR`) in prod. In dev the same condition is only a warning — `WARNING: VULOS_DNS_API unset — defaulting to noop (dev/CI only)` — and provisioning is skipped harmlessly.

**Symptom:** publishing fails with `provision subdomain for <app>: dns provision: server returned <status>: <body>`.
**Cause/fix:** the DNS API rejected the request; the body carries the upstream reason. `dns provision: request: ...` is a transport failure to that API.

**Symptom:** `provision subdomain for <app>: write proxy config: caddy snippet: mkdir /etc/caddy/vulos-apps: permission denied`.
**Cause:** DNS succeeded but the Caddy snippet write failed. This fails **closed**: no deployment record is stored, because a recorded subdomain that never routes would be a lie and would poison retries.
**Fix:** make `VULOS_CADDY_DIR` writable by the vulos user (or set it to a writable path), then publish again — the operation is retryable.

**Symptom:** custom-domain verification fails with `TXT record _vulos-verify.<domain> does not contain expected token (found N record(s))` or `dns lookup _vulos-verify.<domain>: no such host`.
**Fix:** create exactly the TXT record the UI showed you (`_vulos-verify.<yourdomain>` set to the challenge token), wait for propagation (`dig TXT _vulos-verify.yourdomain.com`), retry.

---

## TLS certificate issues

The main server picks up certificates from the first of these pairs that exists, in order:

1. `~/.vulos/localhost.pem` + `~/.vulos/localhost-key.pem` — mkcert-style dev certs
2. `/etc/vulos/tls/cert.pem` + `/etc/vulos/tls/key.pem` — production certs

If neither exists it serves plain HTTP and logs `vulos server listening on :8080 (env=..., no TLS)`; with certs it logs `... with TLS (env=..., cert=<path>)`.

**Dev HTTPS:** generate trusted localhost certs with mkcert and put them at the exact paths above:

```bash
mkcert -install
mkcert localhost          # produces localhost.pem + localhost-key.pem
mv localhost*.pem ~/.vulos/
```

`./dev.sh` detects these files and mounts them into the container read-only (`TLS certs mounted (HTTPS enabled)` in its output).

**Prod behind Caddy (`./build.sh --deploy ... --domain ...`):** the deploy script builds Caddy with the Namecheap DNS plugin, configures `/etc/caddy/Caddyfile` for `$DOMAIN` + `*.$DOMAIN` wildcard TLS, and installs `caddy.service` with credentials in `/etc/caddy/env` (`NAMECHEAP_API_USER` / `NAMECHEAP_API_KEY`). Certificate problems on this path are Caddy problems:

```bash
sudo journalctl -u caddy -n 100 --no-pager
```

Wildcard issuance failing almost always means the DNS API credentials in `/etc/caddy/env` are wrong or the domain isn't managed by that provider.

**Direct listener certificates:** the opt-in public listener gets its cert from ACME (`VULOS_DIRECT_CERT_MODE=acme`, the default — requires `VULOS_DIRECT_HOSTNAME`, an autocert cache under `~/.vulos/auth/direct-acme` by default, and port 443 reachable for the challenge) or from files (`VULOS_DIRECT_CERT_MODE=provided` + `VULOS_DIRECT_CERT_FILE`/`VULOS_DIRECT_KEY_FILE`). Misconfigurations are refused at startup, fail-closed, with `[direct] disabled: directlisten: ...` messages quoted in the reachability section above; a bad provided pair fails as `directlisten: load cert: ...`. A custom `VULOS_DIRECT_ENDPOINT` must be `https://` — cleartext is rejected (`directlisten: AdvertiseEndpoint must be https`).

---

## Upload failures and resume behavior

Large uploads use resumable, chunked (tus-style) endpoints under `/api/files/upload/resumable` — create with `POST`, query the committed offset with `HEAD`, append chunks with `PATCH`, cancel with `DELETE`. Chunks are sized to pass the relay (default cap 64 MiB per chunk; one upload up to 5 GiB), so big files work over the relay path too.

**Symptom:** a `PATCH` fails with `409` (`upload: offset conflict: have N, got M`).
**Cause:** the client's offset doesn't match what the server has committed — the normal result of a dropped connection mid-chunk.
**Fix:** this is exactly what resume is for and well-behaved clients (the Files app) do it automatically: `HEAD` the upload URL, read `Upload-Offset`, and continue `PATCH`ing from there. Nothing already uploaded is lost.

**Symptom:** upload rejected with `upload: checksum mismatch`.
**Cause:** a per-chunk or whole-file SHA-256 integrity check failed. The corrupt data never reaches your Drive; the failing chunk (or the finalize) is rejected.
**Fix:** retry the chunk / re-upload the file. Persistent mismatches on one machine suggest a proxy mangling request bodies.

**Symptom:** `413`-class failure, `upload: exceeds declared length` — a chunk would overrun the total size declared at create, or the declared size exceeds the 5 GiB ceiling.

**Symptom:** `429 too many concurrent uploads`.
**Cause:** one account may hold at most 32 incomplete resumable uploads at once (a disk-fill guard on multi-user boxes).
**Fix:** finish or `DELETE` (cancel) an in-flight upload; a slot frees immediately. Abandoned partials are also swept automatically after 24 hours idle.

**Symptom:** the final chunk fails with `upload: files sink unavailable`.
**Cause:** the Files storage backend isn't wired (e.g. object storage not configured), so the assembled file can't be promoted into your Drive. The bytes stay staged and resumable.
**Fix:** fix the storage configuration (see [FILES.md](FILES.md) and [CONFIGURATION.md](CONFIGURATION.md)), then repeat the final `PATCH`.

---

## Still stuck?

- Run `curl -s http://localhost:8080/api/health | jq` and read the failing check — a full disk or read-only data dir explains a surprising amount of misbehavior.
- Grep the backend log for the bracketed subsystem tags used throughout this chapter: `[cloudenroll]`, `[cloudlogin]`, `[direct]`, `[lan]`, `[llmuxclient]`, `[ai]`, `[assistant]`, `[appnet]`, `[storage]`, `[gpuhost]`, `[integrations]`.
- `curl -s http://localhost:8080/api/version` tells you exactly which build you are running before you file a report.
- For backup and restore paths, see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md); for peering/federation problems, see [PEERING.md](PEERING.md).
