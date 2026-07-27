# Adding a Custom Domain

You can point a domain you own — `example.com`, `cloud.mysurname.co.za` — at your
Vulos box. There are two related things you might mean by "a custom domain", and
Vulos handles them in different places:

1. **Reach the whole box on your own domain** — the *Own Domain* connection mode.
2. **Give a published app or website its own domain** — the *Custom Domain* panel,
   with a DNS TXT-record challenge.

Both are **owner-gated**. This page covers each.

For the networking model behind it see [NETWORKING.md](NETWORKING.md); for the
settings surfaces see [SETTINGS.md](SETTINGS.md).

---

## 1. Reach the whole box on your own domain

In **Settings → Connection Mode** you choose how the box reaches the outside
world. One of the options is **Own Domain**: you put your own domain and reverse
proxy in front of the box.

- Choose **Own Domain** and set your public IP/domain under **Remote Access** so
  the box knows the URL it is reached on.
- Point your domain's DNS at your box (or at the reverse proxy in front of it),
  terminate TLS there, and proxy through to the box.
- Switching connection mode **never changes the box's identity** — its ULID and
  keys stay the same. You can move between Fabric (relay), Direct, Own Domain and
  Local-Only without re-enrolling.

This is the option to use when you want `cloud.example.com` to *be* your box. The
full connection-mode reference, DNS, TLS and ports are in
[NETWORKING.md](NETWORKING.md).

---

## 2. Give a published app its own domain

When you publish an app, it gets a default subdomain. To put your **own** domain
on it, use **Settings → Custom Domain** (`src/core/settings/DomainPanel.jsx`).

### Prerequisite

The app must **already be published** (visibility `public` — see
[APPS.md](APPS.md)). If it isn't, the backend refuses with `409` and the panel
tells you to publish first.

### The flow (DNS TXT challenge)

The handlers live in `backend/services/appnet/customdomain.go` and are scoped to
the authenticated owner (`X-User-ID`, stamped by the auth middleware — a client
cannot forge it):

1. **Attach the domain.** `POST /api/apps/{id}/domain` with your domain. The box
   generates a random **challenge token** and records the domain as `pending`,
   returning the DNS record you must create.
2. **Publish the DNS TXT record.** At your DNS provider add:

   ```
   _vulos-verify.<your-domain>   TXT   <challenge_token>
   ```

3. **Verify.** `POST /api/apps/{id}/domain/verify` performs a live DNS TXT lookup
   for `_vulos-verify.<your-domain>`. If it finds your token the domain flips to
   `verified` and is activated; otherwise it stays pending and tells you what it
   found.
4. **Check status** any time with `GET /api/apps/{id}/domain`, and **remove** the
   domain with `DELETE /api/apps/{id}/domain` (which reverts the app to its
   default subdomain).

The panel shows a **Pending / Verified** badge so you can see where a domain is
in the process.

> **Production guard:** the box only registers these routes when DNS/proxy
> provisioning is actually configured (`VULOS_DNS_API`, `VULOS_CADDY_DIR`). In a
> dev/CI setup without them, the domain is recorded but no real DNS or proxy work
> happens — this is intentional so you are never falsely told a domain is live.

### Published static websites

Static sites you deploy through the web host (`/api/web/*`) attach custom domains
the same way, via `POST /api/web/sites/{site}/domains`. Every management route
there is scoped to the owner (`X-User-ID`) and fails closed with `401` on an empty
session; the domain stays `pending` until real DNS is published and verified.

---

## CDN (owner only) — read this caveat

**Settings → CDN** lets the owner describe a CDN vendor (Cloudflare / Fastly /
Bunny) in front of the box — origin host, Host header, authenticated-origin-pulls
— and preview an origin-firewall ruleset that would restrict inbound traffic to
that vendor's published IP ranges.

> **Important:** the CDN panel is explicit that enabling the firewall here only
> **generates and shows** the ruleset. It does **not** yet apply anything to the
> box's actual network filter. Treat it as a configuration preview, not a live
> enforcement switch.

---

## Related pages

- [NETWORKING.md](NETWORKING.md) — connection modes, DNS, TLS, ports, firewall.
- [SETTINGS.md](SETTINGS.md) — where these panels live.
- [APPS.md](APPS.md) — publishing an app so a domain can be attached.
- [ACCOUNTS-ACCESS.md](ACCOUNTS-ACCESS.md) — why these actions are owner-gated.
