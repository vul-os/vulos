# Network & Remote Access

How users reach their Vula instances from the internet. Domain setup, TLS, subdomain routing, connection modes.

---

## Connection Modes

Four modes, chosen during init. All modes support local network access via mDNS regardless of which is selected.

| Mode | Requires open ports? | Requires Vulos infra? | Best for |
|------|---------------------|----------------------|----------|
| **A — Vulos fabric** | No | Yes (Ziti + DNS) | CGNAT, mobile, behind firewalls |
| **B — Direct (Vulos subdomain)** | Yes (443) | DNS + acme-dns only | Home server with public IP |
| **C — Own domain** | Yes (443) | No | Self-hosters with own domain |
| **D — Local only** | No | No | Air-gapped / LAN-only |

---

## Mode A: Vulos Fabric (default — recommended for most users)

### Option A: Vulos Subdomain (default — recommended)

Instance ID (ULID) is generated locally at first boot — no server check, no slug claiming. See INIT.md for identity generation and VULOS_INTERNAL.md for routing.

**Init flow:**

```
┌─────────────────────────────────────────────┐
│                                             │
│           Remote access                     │
│                                             │
│   Access your system from anywhere.         │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Vulos fabric (recommended)     │   │
│   │      Free *.{ulid}.vulos.org        │   │
│   │      No open ports needed           │   │
│   │                                     │   │
│   │  ( ) Direct (open ports)            │   │
│   │      Port 443 open, public IP       │   │
│   │      Vulos subdomain, no tunnel     │   │
│   │                                     │   │
│   │  ( ) Own domain                     │   │
│   │      Bring your own domain +        │   │
│   │      DNS provider API key           │   │
│   │                                     │   │
│   │  ( ) Local only                     │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Your instance ID                          │
│   ┌─────────────────────────────────────┐   │
│   │ 01h5t3e8k2qj7r9xmvn4p             │   │
│   └─────────────────────────────────────┘   │
│   Your URL: *.01h5t3e8k2qj7r9xmvn4p.       │
│   vulos.org                                 │
│                                             │
│              [ Connect ]                    │
│                                             │
└─────────────────────────────────────────────┘
```

After enrollment:

```
┌─────────────────────────────────────────────┐
│                                             │
│   ✓ Connected to Vulos fabric               │
│                                             │
│   Your domain:                              │
│   *.01h5t3e8k2qj7r9xmvn4p.vulos.org       │
│                                             │
│   Setting up TLS certificate...             │
│   ┌─────────────────────────────────────┐   │
│   │  ✓ Fabric identity enrolled         │   │
│   │  ✓ DNS configured                   │   │
│   │  ↓ Requesting wildcard cert...      │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Want a readable URL? Get a vanity         │
│   domain in Settings → Network later.       │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

**What happens under the hood:**
1. Instance ULID `01h5t3e8k2qj7r9xmvn4p` already generated at boot (see INIT.md)
2. Instance calls Vulos Control API: `POST /api/enroll` → receives Ziti enrollment JWT + acme-dns credentials
3. Instance enrolls with Ziti controller using JWT — fabric identity established
4. `*.01h5t3e8k2qj7r9xmvn4p.vulos.org` now resolves to the Vulos edge node IP pool
5. Instance runs certbot with acme-dns hook → edge node serves TXT challenge briefly
6. Let's Encrypt validates → issues `*.01h5t3e8k2qj7r9xmvn4p.vulos.org` wildcard cert
7. Instance holds the cert. Inbound `:443` traffic arrives at the edge node, SNI-routed through the Ziti circuit to the instance. Done.

**Cert renewal** — every 90 days, automatic. Certbot on the instance updates TXT via acme-dns, new cert issued. No central server changes.

**IP changes** — no action needed. The instance has a Ziti identity; its physical network is irrelevant. Works on CGNAT, mobile, dorm WiFi, behind corporate firewalls.

---

## Mode B: Direct (open ports, Vulos subdomain)

For users with a server that has a public IP and port 443 open. Gets a free `*.{ulid}.vulos.org` wildcard cert but traffic goes **directly to the server** — no Ziti tunnel, no fabric dependency beyond DNS and the one-time acme-dns cert issuance.

**Init flow:**

```
┌─────────────────────────────────────────────┐
│                                             │
│           Direct connection setup           │
│                                             │
│   Your public IP                            │
│   ┌─────────────────────────────────────┐   │
│   │ 1.2.3.4  (auto-detected)           │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Your URL: *.01h5t3e8k2qj7r9xmvn4p.       │
│   vulos.org                                 │
│                                             │
│   Make sure port 443 is open on this        │
│   server before continuing.                 │
│                                             │
│              [ Connect ]                    │
│                                             │
└─────────────────────────────────────────────┘
```

**What happens under the hood:**
1. Instance calls `POST /api/enroll/direct` → `{ ulid, ip, email }` → receives acme-dns credentials
2. DNS: `*.{ulid}.vulos.org` → A → server's public IP (not the edge node pool)
3. Instance runs certbot with acme-dns hook → edge node serves TXT challenge briefly
4. Let's Encrypt issues `*.{ulid}.vulos.org` wildcard cert
5. Caddy on the instance serves `:443` directly. No Ziti involved after this point.

**IP changes** — if the public IP changes, the OS detects it and calls `PUT /api/dns/update` to update the A record.

**Tradeoff vs Mode A:** Simpler (no Ziti dependency at runtime), but requires a public IP and open ports. Breaks behind CGNAT, mobile networks, or corporate firewalls.

---

## Mode C: Own Domain (advanced)

User brings their own domain with open ports. Caddy handles wildcard TLS via DNS challenge. Requires a DNS provider that supports API access for wildcard certs (e.g., Namecheap, Cloudflare DNS-only, etc.). No Vulos infrastructure involved after setup.

```
┌─────────────────────────────────────────────┐
│                                             │
│           Own domain setup                  │
│                                             │
│   Domain                                    │
│   ┌─────────────────────────────────────┐   │
│   │ my-vula.example.com                 │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   DNS Provider                              │
│   ┌─────────────────────────────────────┐   │
│   │ Namecheap (default)            [▼]  │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   API User                                  │
│   ┌─────────────────────────────────────┐   │
│   │ myuser                              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   API Key                                   │
│   ┌─────────────────────────────────────┐   │
│   │ ••••••••••••                        │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Caddy will be installed and configured    │
│   with wildcard TLS for your domain.        │
│                                             │
│              [ Next ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

### Config

```env
# Network (server mode only)
VULOS_INSTANCE_ID=01h5t3e8k2qj7r9xmvn4p      # ULID, generated at first boot, immutable
VULOS_HOSTNAME=alice-home                      # human-readable, for LAN/mDNS
VULOS_DOMAIN_MODE=fabric                       # "fabric" | "direct" | "own" | "local"
VULOS_DOMAIN=01h5t3e8k2qj7r9xmvn4p.vulos.org # auto-set from ULID (fabric/direct modes)

# fabric mode only
VULOS_ZITI_IDENTITY=/etc/vulos/ziti.json       # Ziti identity file (written at enrollment)
VULOS_ACME_DNS_UUID=<uuid>                     # acme-dns credentials (from enrollment)
VULOS_ACME_DNS_KEY=<key>                       # acme-dns credentials (from enrollment)

# direct mode only
VULOS_PUBLIC_IP=1.2.3.4                        # server's public IP (auto-detected, updated on change)
VULOS_ACME_DNS_UUID=<uuid>                     # acme-dns credentials (from enrollment)
VULOS_ACME_DNS_KEY=<key>                       # acme-dns credentials (from enrollment)

# own domain mode only
VULOS_DNS_PROVIDER=namecheap
VULOS_DNS_API_USER=myuser
VULOS_DNS_API_KEY=<api-key>
```

---

## Subdomain Routing Scheme

### Scheme

```
{app}--{profile}.{ulid}.vulos.org
```

Each app runs per-profile. The subdomain encodes both. The `*.{ulid}.vulos.org` wildcard cert covers all apps on an instance — one cert, all subdomains.

```
browser--personal.01h5t3e8k2qj7r9xmvn4p.vulos.org   → Alice's personal browser
browser--work.01h5t3e8k2qj7r9xmvn4p.vulos.org        → Alice's work browser
terminal--default.01h5t3e8k2qj7r9xmvn4p.vulos.org    → Alice's terminal
```

If no `--profile` is present the router falls back to `default`.

**Why `--`?** Single hyphens appear in app IDs and profile names. Double dash is an unambiguous separator — same convention Cloudflare Pages and Vercel use for branch deploys.

**Vanity routing (paid):** `cognizance.vulos.org → 302 → 01h5t3e8k2qj7r9xmvn4p.vulos.org`. The vanity redirect lands on the instance root; the user's browser then navigates to the specific app URL. See VULOS_INTERNAL.md → Routing & Vanity Domains.

### App Visibility

Each app-profile has a visibility setting:

| Visibility | Auth required | Who can access |
|------------|--------------|----------------|
| **private** (default) | Yes — session cookie for that profile | Owner only |
| **public** | No | Anyone with the URL |

Enforced at the reverse proxy (Caddy): private apps check for a valid session cookie and redirect to login if absent. Public apps pass through with no auth check. The app itself handles what anonymous users can do.

### Parsing

```go
// Host: {app}--{profile}.{ulid}.vulos.org
// baseDomain is the per-instance base, e.g. "01h5t3e8k2qj7r9xmvn4p.vulos.org"
func parseSubdomain(host, baseDomain string) (app, profile string, ok bool) {
    sub := strings.TrimSuffix(host, "."+baseDomain)
    parts := strings.SplitN(sub, "--", 2)
    switch len(parts) {
    case 2:
        return parts[0], parts[1], true
    case 1:
        return parts[0], "default", true
    }
    return "", "", false
}
```

### Local Network Subdomains

On LAN the instance hostname replaces the ULID:
```
browser--personal.alice-home.local
terminal--default.alice-home.local
```

`backend/services/appnet/dns.go` writes `/etc/hosts` entries — needs updating to `{app}--{profile}` format.

### Naming Restrictions

Since `--` is the subdomain separator, it must be forbidden in:

- **Profile names** — validate on create/rename in `backend/services/auth/profiles.go`
- **Instance names** — validate during init in setup wizard
- **App IDs** — validate in `registry.json` entries and `backend/services/appnet/registry.go`
- **Usernames** — validate in `backend/services/auth/auth.go` Register()

Validation rule: `^[a-z0-9][a-z0-9-]*[a-z0-9]$` (lowercase alphanumeric + single hyphens, no `--`, no leading/trailing hyphen). Apply the same regex everywhere to keep it consistent.

### Changes Required

**Backend:**
- `backend/services/network/network.go` — add instance name to Domain()
- `backend/cmd/server/main.go` — update subdomain parser (line ~1639) to handle `--` separator
- `backend/services/appnet/dns.go` — update DNS entries to new format
- `backend/services/appnet/namespace.go` — namespace IDs become `{profile}-{appId}` instead of `{userId}-{appId}`
- `backend/services/auth/handlers.go` — cookie domain needs to work with deeper subdomains
- `backend/services/auth/auth.go` — add `--` restriction to username validation
- `backend/services/auth/profiles.go` — add `--` restriction to profile name validation
- `backend/services/appnet/registry.go` — validate app IDs don't contain `--`

**Frontend:**
- `src/core/AppRegistry.js` — app URLs use new scheme
- `src/builtin/stream/StreamViewer.jsx` — WebRTC connection URLs
- `src/auth/Setup.jsx` — instance name validation in init wizard
- Any hardcoded subdomain references

**Vulos.org API:**
- DNS records use the `--` separator in subdomains — no additional validation needed on the server side
- Enrollment validates `--` restriction on ULIDs (not applicable — ULIDs are alphanumeric only)

---

## TURN / coturn (media fallback)

For WebRTC media relay (screen sharing, video, audio) — separate concern from the Ziti data fabric. User self-hosts their own coturn server.

Available in Settings → Network → TURN:

```
┌─────────────────────────────────────────────┐
│                                             │
│   TURN Server                               │
│                                             │
│   Host                                      │
│   ┌─────────────────────────────────────┐   │
│   │ turn.myserver.com:3478              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Shared Secret                             │
│   ┌─────────────────────────────────────┐   │
│   │ ••••••••••••                        │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   The OS generates time-limited             │
│   credentials from this secret              │
│   automatically.                            │
│                                             │
│         [ Test Connection ]  [ Save ]       │
│                                             │
└─────────────────────────────────────────────┘
```

The existing `backend/services/network/turn.go` handles HMAC-based credential generation from a shared secret.

---

## Health Endpoint

```go
// GET /api/health — external monitoring checks this
func (c *Cluster) HealthHandler(w http.ResponseWriter, r *http.Request) {
    // Returns 200 if node is healthy, 503 if degraded
    // Checks: DB writable, disk space, sync lag
}
```

---

## LAN Mode

When there is no internet or when the Vulos fabric is unreachable, the instance falls back to mDNS for local network discovery. **LAN mode bypasses Ziti entirely** — traffic goes directly between devices on the same network.

```
internet available  →  *.{ulid}.vulos.org via Ziti fabric
LAN only            →  vulos.local via mDNS (no Ziti, no edge nodes)
```

This is intentional: local access must not depend on Vulos infrastructure being reachable.

---

## Changing Connection Mode (Settings → Network)

Users can switch modes after init without reinstalling. Settings → Network → Connection:

```
┌─────────────────────────────────────────────┐
│                                             │
│   Connection                                │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Vulos fabric                   │   │
│   │      *.01h5t3e8...vulos.org         │   │
│   │      Status: ● Connected            │   │
│   │                                     │   │
│   │  ( ) Direct (open ports)            │   │
│   │      *.01h5t3e8...vulos.org → IP    │   │
│   │                                     │   │
│   │  ( ) Own domain                     │   │
│   │      my-vula.example.com            │   │
│   │                                     │   │
│   │  ( ) Local only                     │   │
│   └─────────────────────────────────────┘   │
│                                             │
│              [ Save ]                       │
│                                             │
└─────────────────────────────────────────────┘
```

**Switching rules:**
- Switching to **fabric** re-uses the existing ULID; if not yet enrolled with Ziti, enrollment runs on save.
- Switching to **direct** re-uses the existing ULID; registers/updates the A record to the current public IP. If previously enrolled with Ziti, the Ziti identity is kept but the router process is stopped.
- Switching to **own domain** prompts for domain + DNS provider credentials. The existing Vulos DNS records remain but are no longer the active route.
- Switching to **local only** stops the external-facing listener. mDNS continues to work on LAN.

The ULID and TLS cert are **never regenerated** on a mode change — just the routing changes.

---

## LAN Mode

Always active regardless of which connection mode is selected. When the Vulos fabric or public internet is unreachable, the instance advertises via mDNS and traffic goes directly between devices on the local network — **no Ziti, no edge nodes, no internet required**.

```
fabric/direct/own domain  →  *.{ulid}.vulos.org  (internet)
LAN fallback              →  vulos.local via mDNS (no Vulos infra)
```

See VULOS_INTERNAL.md → LVH / mDNS section for implementation details.

---

## Implementation Order

1. **Subdomain routing overhaul** — replace `{appId}.{domain}` with `{app}--{profile}.{ulid}.{domain}`. Update subdomain parser, DNS manager, namespace IDs, cookie domain, frontend URLs.
2. **Mode B (direct) enrollment** — `POST /api/enroll/direct`, A record → server IP, acme-dns cert, `PUT /api/dns/update` for IP changes. This is the simplest path and validates the enrollment API before adding Ziti.
3. **Mode A (fabric) enrollment** — `POST /api/enroll`, Ziti controller + edge router + SNI passthrough, edge node Go binary.
4. **Mode C (own domain)** — Caddy + DNS provider API.
5. **TURN/coturn** — Settings UI, connection testing, credential generation.
