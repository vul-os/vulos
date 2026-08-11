# Troubleshooting

Common Vulos OS failures, organized as symptom, cause, and fix. Every log line, error string, endpoint, environment variable, and unit name in this chapter is quoted from the actual code and scripts, so you can grep your logs for the exact text you see. Start with the logs section, find your area, and work through the checks in order.

Related chapters: [GETTING-STARTED.md](GETTING-STARTED.md), [CONFIGURATION.md](CONFIGURATION.md), [NETWORKING.md](NETWORKING.md), [ASSISTANT.md](ASSISTANT.md), [APPS.md](APPS.md), [FILES.md](FILES.md), [SECURITY.md](SECURITY.md), [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md), [PEERING.md](PEERING.md), [USER-GUIDE.md](USER-GUIDE.md).

---

## Where the logs live

The Go backend writes everything to standard output/error. Where that ends up depends on how you installed Vulos.

**Bare metal — live USB (systemd).** The live image boots systemd as PID 1 and
runs the backend as a single `vulos.service` unit on port 8080. A second unit,
`vulos-console.service`, owns the physical console (`tty1`) and shows a
read-only status screen instead of a login prompt — see
[GETTING-STARTED.md → What you'll see on screen](GETTING-STARTED.md#what-youll-see-on-screen).

```bash
sudo systemctl status vulos.service           # is it up?
sudo journalctl -u vulos -n 200 --no-pager    # OS backend log
sudo journalctl -u vulos -f                   # follow live
```

**Bare metal — installed to disk with `vulos-install --disk`.** Not a state any
box is in today — though no longer for the reason this once gave. `build.sh`
builds `./cmd/installer`, copies it to `/usr/local/bin/vulos-install` in the
rootfs, and fails the build if it is not executable there, so the command does
ship. What stops the route is the hand-signed `stable.json` it requires (see
[GETTING-STARTED.md → Installing to the machine's own
disk](GETTING-STARTED.md#install-it-to-the-machines-disk)). If you do produce
such a disk, note that these commands would not apply to it either: the boot
entry that installer writes carries `init=/sbin/vulos-init`
(`backend/internal/installer/disk.go`), so Vulos's own init is PID 1, and
`startSystemd()` in `backend/cmd/init/main.go` only logs that systemd is present
rather than starting it. No `vulos.service`, no `vulos-console.service` status
screen, no journal — output goes to the kernel console the cmdline names
(`console=tty1`, plus `console=ttyAMA0,115200` for a serial line).

**Deployed to a server you run (`./build.sh --deploy`).** Same `vulos.service`
unit, installed by the deploy script over SSH — see [DEPLOY.md](DEPLOY.md).
The same `journalctl -u vulos` commands above apply.

**Docker.** The container is conventionally named `vulos`:

```bash
docker logs vulos --tail 200
docker logs vulos -f
```

**Dev (`./scripts/dev.sh` or `go run ./backend/cmd/server`).** Logs go straight to your terminal. On a clean start you will see one of:

```
vulos server listening on :8080 (env=local, no TLS)
vulos server listening on :8080 with TLS (env=prod, cert=...)
```

**Per-app logs.** Each launched app's stdout/stderr goes to its own file, rotated at a size threshold:

```bash
cat ~/.vulos/logs/<appId>.log        # current
cat ~/.vulos/logs/<appId>.log.old    # previous rotation
```

**Ports.** Every install path — Docker, live USB, installed-to-disk, and `./build.sh --deploy` — defaults to `8080` (the `PORT` env var, default `"8080"`). Substitute accordingly in the `curl` examples below if you've changed it.

### Health and metrics

`GET /api/health` gives the **verdict to anyone and the detail only to a session.** Unauthenticated you get `200`+`{"status":"ok","timestamp":…}` or `503`+`"status":"degraded"` — enough to answer "is it up, is it healthy" — with no `checks` map. With a session you get the same status plus the per-check breakdown.

The checks still *run* for an anonymous caller; only the output is withheld, so the status is never a guess. The detail is gated because every field in it leaks something on a box that is already misbehaving: `data_dir_writable` reports the absolute data-dir path and the raw OS error, `disk_space` reports exact free capacity (which fingerprints the deployment and tells an attacker how much to write to force a `503`), and `sync_lag` reveals whether S3 cluster sync exists at all.

```bash
# verdict only — no session needed
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/api/health
curl -s http://localhost:8080/api/health | jq          # {"status":"ok","timestamp":"…"}

# full breakdown — needs a session cookie
curl -s -b "$COOKIE" http://localhost:8080/api/health | jq
```

> **Not a readiness probe.** `/api/health` answers `503` for a merely *degraded* box — low disk in a scratch VM will do it — which is a different question from "has it finished starting". For readiness, poll `GET /api/setup/status`, which is what `scripts/baremetal-smoke.sh` deliberately does.

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

These match the quick table in [GETTING-STARTED.md → When something goes wrong](GETTING-STARTED.md#when-something-goes-wrong), with more detail.

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

**Symptom:** `vulos-install: command not found` in the live session's Terminal.
**Likely cause:** you are not in a live session, or the image predates the command. `vulos-install` is installed to `/usr/local/bin` in the OS image's rootfs only — a Docker container and a `./build.sh --deploy` target are ordinary Linux systems and never receive it. On an image built before `./cmd/installer` was added to the build, it is genuinely absent.
**Fix:** boot the live USB and open Terminal there; if you already are, reflash with a current `.img.gz`. Note that finding the command is not the same as being able to install: `--disk` still needs a release carrying a hand-signed `stable.json`, and the next entry covers the refusal you will see. See [GETTING-STARTED.md → Installing to the machine's own disk](GETTING-STARTED.md#install-it-to-the-machines-disk).

**Symptom:** `vulos-install --disk` reports a manifest verification failure.
**Likely cause:** `stable.json`/`stable.json.sig` are missing from, or don't match, the release you're installing — the installer refuses to write an unverifiable system rather than installing insecurely. Note that those two files are offline signing output, not release-workflow output, so a release can legitimately ship without them.
**Fix:** re-download both from the exact release's assets, keeping the `.sig` beside the manifest — the installer always looks for the signature at the manifest path with `.sig` appended, and has no separate flag for it.

**Symptom:** mail doesn't receive; port 25 times out from outside.
**Likely cause:** most residential and many cloud ISPs block inbound port 25.
**Fix:** Vulos itself does not run a mail server — LilMail is a client for a mailbox you already own (Gmail/Outlook/IMAP/SMTP). If you're running your own separate mail server alongside Vulos, use a VPS with port 25 open (Hetzner, OVH, etc.) or configure a mail relay; that mail server is outside this repo.

---

## Sign-in

Vulos accounts are local to your box — email/password, passkey (WebAuthn/FIDO2), PIN, and TOTP, all created and verified on-box. There is no cloud console, no device enrollment flow, and no account that exists anywhere but on your own instance. If sign-in is failing, check [SECURITY.md](SECURITY.md) for the auth surface and its fail-closed defaults.

---

## Box not reachable remotely (relay and direct)

A box behind NAT is reached through a **relay**: the box dials **out**, holds the connection open, and the relay forwards traffic back down it. Vulos ships its own — `vulos relay serve`, built from this repository's `backend/cmd/vulos` (a different binary from the box's `vulos-server`, and one you build yourself; no release artifact contains it). The **box-side** agent is embedded in the OS process, so on the box there is no separate agent binary or log to check.

**Nobody runs a relay on your behalf**, so a box with nothing configured has no relay: it is LAN-reachable, and publicly reachable only in direct mode. Configure relays with `VULOS_RELAY_ENDPOINTS_FILE` (preferred, mode 0600) or `VULOS_RELAY_ENDPOINTS`; the legacy `VULOS_RELAY_BASE_URL` / `VULOS_RELAY_NAME` / `VULOS_RELAY_TOKEN` form still works. [Pier](https://github.com/vul-os/pier) is a supported alternative relay.

If remote access fails, start here:

```bash
curl -s localhost:8080/api/network/reach | jq   # on the box: endpoint health + per-link state
curl -s https://relay.example.com/_vulos-reach/v1/health   # is the relay alive?
```

`state: "refused"` with `unauthorized` means the relay's grant does not authorise that name; `state: "backoff"` means the relay is unreachable from the box. See [REACH.md](REACH.md#status-and-troubleshooting) for the full symptom table, and [NETWORKING.md](NETWORKING.md) for the reachability model.

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

All OS chat/LLM traffic goes through llmux, which runs one of two ways —
`VULOS_AI_MODE=embedded` (in-process, no separate binary) or `VULOS_AI_MODE=remote`
(talking to an llmux gateway at `LLMUX_URL`). See [ASSISTANT.md → Choosing where
your AI runs](ASSISTANT.md#choosing-where-your-ai-runs). Neither is on by
default: without one configured the AI routes exist but refuse to work.

**Symptom:** the assistant returns errors; `/api/ai/*` calls fail with `503` and the body `{"error":"gateway_unconfigured: set LLMUX_URL to use a remote llmux, or VULOS_AI_MODE=embedded to run it in-process"}`. The boot log shows:

```
[llmuxclient] no AI gateway configured — /api/ai/* will return 503. Set LLMUX_URL for a remote llmux, or VULOS_AI_MODE=embedded to run it in this process
```

**Fix:** either set `VULOS_AI_MODE=embedded` and restart (no other variable required — llmux starts in-process using its own defaults, or `VULOS_LLMUX_CONFIG` if you've named a config file), or set `LLMUX_URL` to a gateway running as its own process (a trailing `/v1` is tolerated and stripped). Model listing surfaces the same condition as `"chat_models_error": "no llmux gateway configured (set LLMUX_URL for a remote llmux, or VULOS_AI_MODE=embedded to run it in-process)"`.

**Symptom:** `502` with `gateway_error: llmuxclient: chat request failed: ...` or `gateway_error: llmuxclient: gateway returned 500: ...`.
**Likely cause:** in remote mode, `LLMUX_URL` is set but the gateway is down, unreachable, or itself failing (e.g. its ollama backend is not running); in embedded mode, the provider llmux is configured to use (Ollama, an API key) is unreachable or misconfigured.
**Fix (remote mode):** verify the gateway directly:

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

Publishing an app publicly provisions `{app}--{profile}.{instanceID}.vulos.org` through a DNS API (`VULOS_DNS_API`, which defaults to `noop` — no DNS provider configured — until you point it at your own provider's endpoint; base domain `VULOS_BASE_DOMAIN`; instance from `VULOS_INSTANCE_ID`) and writes a Caddy vhost snippet under `/etc/caddy/vulos-apps` (`VULOS_CADDY_DIR`).

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

`./scripts/dev.sh` detects these files and mounts them into the container read-only (`TLS certs mounted (HTTPS enabled)` in its output).

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

- Run `curl -s -b "$COOKIE" http://localhost:8080/api/health | jq` and read the failing check — a full disk or read-only data dir explains a surprising amount of misbehavior. Without the cookie you still get the `ok`/`degraded` verdict, just not which check failed.
- Grep the backend log for the bracketed subsystem tags used throughout this chapter: `[direct]`, `[lan]`, `[llmuxclient]`, `[ai]`, `[assistant]`, `[appnet]`, `[storage]`, `[gpuhost]`, `[integrations]`.
- `curl -s http://localhost:8080/api/version` tells you exactly which build you are running before you file a report.
- For backup and restore paths, see [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md); for peering/federation problems, see [PEERING.md](PEERING.md).
