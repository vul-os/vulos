# Network & Remote Access

How users reach their Vula instances from the internet. Domain setup, TLS, subdomain routing, NAT traversal.

---

## Domain Options

A domain is required for remote access. Two options:

### Option A: Vulos Subdomain (default — recommended)

Instance ID (ULID) is generated locally at first boot — no server check, no slug claiming. When internet is available, the instance registers its ULID + IP with Vulos DNS API automatically. See INIT.md for instance identity generation and VULOS_INTERNAL.md for routing.

**Init flow:**

```
┌─────────────────────────────────────────────┐
│                                             │
│           Remote access                     │
│                                             │
│   Access your system from anywhere.         │
│                                             │
│   ┌─────────────────────────────────────┐   │
│   │  (*) Vulos domain (recommended)     │   │
│   │      Free *.{ulid}.vulos.org        │   │
│   │      with automatic TLS             │   │
│   │                                     │   │
│   │  ( ) Own domain (advanced)          │   │
│   │      Bring your own domain +        │   │
│   │      DNS provider API key           │   │
│   │                                     │   │
│   │  ( ) Skip — local only              │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Your instance ID                          │
│   ┌─────────────────────────────────────┐   │
│   │ 01h5t3e8k2qj7r9xmvn4p             │   │
│   └─────────────────────────────────────┘   │
│   Your URL: *.01h5t3e8k2qj7r9xmvn4p.       │
│   vulos.org                                 │
│                                             │
│   Your public IP                            │
│   ┌─────────────────────────────────────┐   │
│   │ 1.2.3.4  (auto-detected)           │   │
│   └─────────────────────────────────────┘   │
│                                             │
│              [ Register ]                   │
│                                             │
└─────────────────────────────────────────────┘
```

After registration:

```
┌─────────────────────────────────────────────┐
│                                             │
│   ✓ Registered                              │
│                                             │
│   Your domain:                              │
│   *.01h5t3e8k2qj7r9xmvn4p.vulos.org       │
│                                             │
│   Setting up TLS certificate...             │
│   ┌─────────────────────────────────────┐   │
│   │  ✓ DNS records created              │   │
│   │  ✓ acme-dns challenge configured    │   │
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
2. Vulos API → PowerDNS: create `*.01h5t3e8k2qj7r9xmvn4p.vulos.org` → A → `1.2.3.4`
3. Vulos API → acme-dns: register instance → returns UUID + credentials
4. Vulos API → PowerDNS: create `_acme-challenge.01h5t3e8k2qj7r9xmvn4p.vulos.org` → CNAME → `uuid.auth.vulos.org`
5. Node receives acme-dns credentials, runs certbot with acme-dns hook
6. Certbot → acme-dns: update TXT with challenge
7. Let's Encrypt validates → issues `*.01h5t3e8k2qj7r9xmvn4p.vulos.org` wildcard cert
8. Node serves traffic directly with Caddy + the cert. Done.

**Cert renewal** — every 90 days, automatic. Certbot runs on the node, updates TXT via acme-dns, new cert issued. No central server involvement.

**IP changes** — if the user's IP changes, the OS auto-detects and calls the Vulos API to update the DNS A record via PowerDNS.

### Option B: Own Domain (advanced)

User brings their own domain with open ports. Caddy handles wildcard TLS via DNS challenge. Requires a DNS provider that supports API access for wildcard certs (e.g., Namecheap, Cloudflare DNS-only, etc.).

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
VULOS_INSTANCE_ID=01h5t3e8k2qj7r9xmvn4p   # ULID, generated at first boot, immutable
VULOS_HOSTNAME=alice-home                    # human-readable, for LAN/mDNS
VULOS_DOMAIN_MODE=vulos                      # "vulos" (*.{ulid}.vulos.org) or "own" (own domain)
VULOS_DOMAIN=01h5t3e8k2qj7r9xmvn4p.vulos.org  # auto-set from ULID
VULOS_ACME_DNS_UUID=<uuid>                   # vulos mode: acme-dns credentials
VULOS_ACME_DNS_KEY=<key>                     # vulos mode: acme-dns credentials
VULOS_DNS_PROVIDER=namecheap                 # own domain mode only
VULOS_DNS_API_USER=myuser                    # own domain mode only
VULOS_DNS_API_KEY=<api-key>                  # own domain mode only
```

---

## Subdomain Routing Scheme

### Current System (to be replaced)

```
calculator.lvh.me:8080        → calculator app
chromium.lvh.me:8080          → browser app
```

Single level, no profile or instance awareness. Only works for one user on one instance.

### New Scheme

```
{app}--{profile}.{instance-ulid}.vulos.org
```

Instance IDs are ULIDs generated locally at first boot — no server check, no slug claiming. See VULOS_INTERNAL.md for routing and INIT.md for generation.

**Examples:**
```
browser--personal.01h5t3e8k2qj7r9xmvn4p.vulos.org   → Alice's personal browser
browser--work.01h5t3e8k2qj7r9xmvn4p.vulos.org       → Alice's work browser
terminal--default.01h6r2f9m3pk8w1yvq5n7j.vulos.org   → Bob's terminal
```

**Vanity routing (paid):** Users can claim a readable name like `cognizance.vulos.org` that redirects to their instance ULID URL. See VULOS_INTERNAL.md → Routing & Vanity Domains.

**Examples with vanity:**
```
cognizance.vulos.org → redirects to → 01h5t3e8k2qj7r9xmvn4p.vulos.org
```

**Why double dashes (`--`)?** Single dashes are common in app names and profile names. Double dashes are an unambiguous separator. This is the same convention Cloudflare Pages and Vercel use for branch deploys.

**Parsing:**
```go
// Extract app, profile from subdomain
// Host format: {app}--{profile}.{ulid}.vulos.org or {app}.{ulid}.vulos.org
func parseSubdomain(host, baseDomain string) (app, profile string, ok bool) {
    sub := strings.TrimSuffix(host, "."+baseDomain)
    // sub is now e.g. "browser--personal" (ULID is part of baseDomain per-instance)
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

**Routing logic:**
1. Parse subdomain into app + profile
2. Route to local app namespace (the ULID in the URL already identifies this specific instance)

**Profile isolation:** Each profile gets its own app namespace. Alice's "personal" browser and "work" browser run in separate namespaces with separate data directories, separate cookie jars, separate everything. The subdomain makes this addressable from outside.

**Short URLs:** For convenience, these shorter forms also work:
```
browser--personal.01h5t3e8k2qj7r9xmvn4p.vulos.org   → browser app, personal profile
browser.01h5t3e8k2qj7r9xmvn4p.vulos.org              → browser app, default profile
01h5t3e8k2qj7r9xmvn4p.vulos.org                      → dashboard/desktop
```

### Local Network Subdomains

On the LAN (via mDNS / /etc/hosts), the hostname is used instead of the ULID:
```
browser--personal.alice-home.local
terminal.alice-home.local
```

The DNS manager (`backend/services/appnet/dns.go`) already writes `/etc/hosts` entries. It needs to be updated to use the new `{app}--{profile}` format.

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
- PowerDNS records use the `--` separator in subdomains — no additional validation needed
- Slug registration validates `--` restriction on slug names

---

## NAT Traversal — Yggdrasil (optional)

For users behind NAT without open ports, we recommend Yggdrasil — a fully decentralized IPv6 mesh overlay. Both the server and the user install Yggdrasil, the server advertises its peer address, and traffic flows directly over encrypted IPv6. No relay infrastructure, no STUN, no TURN.

**Not configured during init.** Available in Settings → Network → Yggdrasil. The OS detects when direct connections fail and suggests it:

```
┌─────────────────────────────────────────────┐
│                                             │
│   ⚠ Connection issue                       │
│                                             │
│   Your network appears to block direct      │
│   connections (NAT or firewall).            │
│                                             │
│   Recommended: Install Yggdrasil on both    │
│   this server and your client device for    │
│   direct encrypted connections without      │
│   port forwarding.                          │
│                                             │
│   [ Set Up Yggdrasil ]                      │
│   [ Learn More ]                            │
│                                             │
└─────────────────────────────────────────────┘
```

Settings → Network → Yggdrasil:

```
┌─────────────────────────────────────────────┐
│                                             │
│   Yggdrasil Mesh Network                    │
│                                             │
│   Status: ● Running                         │
│   IPv6:   200:1234:5678:abcd::1             │
│                                             │
│   Peer Address (share with clients)         │
│   ┌─────────────────────────────────────┐   │
│   │ tcp://1.2.3.4:9001        [ Copy ]  │   │
│   └─────────────────────────────────────┘   │
│                                             │
│   Clients install Yggdrasil and add this    │
│   address as a peer for direct connection.  │
│                                             │
│         [ Restart ]  [ Disable ]            │
│                                             │
└─────────────────────────────────────────────┘
```

---

## TURN / coturn (fallback)

For users who need WebRTC relay without Yggdrasil (e.g., corporate networks that block non-standard protocols), coturn is available as a fallback. User self-hosts their own coturn server.

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

## Implementation Order

1. **Subdomain routing overhaul** — replace `{appId}.{domain}` with `{app}--{profile}--{instance}.{domain}`. Update subdomain parser, DNS manager, namespace IDs, cookie domain, frontend URLs.
2. **Vulos subdomain registration** — slug registration in init, PowerDNS + acme-dns integration, wildcard TLS.
3. **Own domain support** — Caddy + DNS provider API for user-provided domains.
4. **IP update mechanism** — detect IP change, update DNS A record via Vulos API.
5. **Yggdrasil integration** — Settings UI, auto-detection of NAT issues, setup flow.
6. **TURN/coturn** — Settings UI, connection testing, credential generation.
