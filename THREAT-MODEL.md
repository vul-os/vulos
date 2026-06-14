# Threat Model — Vulos OS

STRIDE pass. Last updated: 2026-05-26 (added Component 4: Login & Credential Isolation).

---

## Scope and Trust Boundaries

```
[User at browser/terminal]
        |
        v
[Vulos Frontend (JSX SPA)] <--- TLS ---> [Backend API (Go)]
        |                                       |
        v                                       v
[App Sandbox]                         [Vulos Identity Store]
        |                                       |
        v                                       v
[OS Shell / init / firstboot]         [Signed squashfs / dm-verity]
```

Trust boundaries:
- **Browser ↔ Backend API**: authenticated session (cookie/token). Untrusted input from browser.
- **Backend API ↔ OS Shell**: privileged local IPC. Attacker who reaches this can escalate.
- **Squashfs image**: trusted only if dm-verity hash matches baked key.
- **App Sandbox ↔ OS**: sandbox syscall filter is the boundary; escaping it reaches OS.

---

## Component 1: OS Shell and Firstboot Installer

### Trust boundaries
- Firstboot runs as root. User input (hostname, disk selection) flows into shell commands and partition operations.
- Network is reachable during firstboot (for package fetch, time sync).

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Tampering** | Attacker replaces squashfs image on the boot medium with a backdoored version before dm-verity is active. |
| 2 | **Elevation of Privilege** | Firstboot installer evaluates user-supplied hostname without sanitisation, leading to command injection in shell wrappers. |
| 3 | **Spoofing** | MITM on firstboot network fetch substitutes a malicious image payload before signature verification. |

### Mitigations in code
- dm-verity hash baked at build time; kernel refuses to mount tampered root.
- Signed squashfs verified before mount in initramfs (see `scripts/verify-image`).
- Hostname input validated against `[a-zA-Z0-9\-]{1,63}` regex in firstboot handler.

### Residual risks
- Key rotation for the baked dm-verity trust key is an open design question (see memory/vulos-os-distribution-architecture.md).
- Network fetch during firstboot has no certificate pinning; relies on system CA bundle which may be stale on older boot media.

---

## Component 2: Vulos Mail Identity Management

### Trust boundaries
- Identity data (email address, keys) stored in local SQLite, accessed only by backend API.
- External: Vulos Cloud verifies identity binding over HTTPS.

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Spoofing** | Attacker crafts a malformed identity assertion to impersonate another Vulos user during cloud sync. |
| 2 | **Information Disclosure** | SQLite identity store readable by another app if sandbox boundary is not enforced. |
| 3 | **Repudiation** | No audit log of identity-change events; user disputes key rotation they didn't authorise. |

### Mitigations in code
- Identity assertions are signed; cloud verifies signature before accepting.
- SQLite file permissions: `0600`, owned by the backend process user.
- CGO disabled; no SQLite C library — pure-Go driver reduces attack surface.

### Residual risks
- Audit log for identity changes not yet implemented (tracked in tasks.md AUTH area).
- Key rotation UX is manual; users may skip rotation after compromise.

---

## Component 3: App Sandbox

### Trust boundaries
- Each installed app runs in a seccomp-filtered process.
- App ↔ Backend API: HTTP over loopback, authenticated per-app token.
- App cannot directly reach other apps' data directories.

### Top 3 STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Elevation of Privilege** | Seccomp filter gap allows a malicious app to call a restricted syscall (e.g. `ptrace`, `mount`) and escape the sandbox. |
| 2 | **Tampering** | Malicious app writes to another app's data directory by exploiting a path-traversal in the backend's file API. |
| 3 | **Information Disclosure** | App reads `/proc` or environment variables of sibling processes before sandbox is fully applied. |

### Mitigations in code
- Seccomp allowlist is default-deny; only safe syscalls permitted.
- Backend file API resolves paths under `JAIL_ROOT` and rejects `..` traversal.
- App process launched with distinct UID per sandbox instance (where OS supports it).

### Residual risks
- Seccomp filter completeness depends on the kernel version; old kernels may lack certain enforcement.
- App-registry trust (whether an app package is signed) is not yet enforced end-to-end (tracked in APPSTORE tasks).

---

## Component 4: Login & Credential Isolation

**Principle: isolate the credential, not the browsing.** Vulos browsing is native — web content runs in the host browser, never a server-side streamed browser (ROADMAP.md §11). A streamed login was explicitly rejected: it does not protect a secret typed on a compromised client. Vulos instead isolates the *credential* (passkeys) and the *durable token* (server-side vault).

### Mechanisms in code
- **Passkeys / WebAuthn (primary login)** — `backend/services/passkeys/` (`login.go`), `src/auth/`. Registration + assertion login; the private key never leaves the authenticator, so a keylogger/extension captures nothing reusable; origin-bound → phishing-resistant. Password+2FA remains a fallback; new accounts default to passkeys.
- **QR / phone-approval login** — `backend/services/passkeys/qrlogin.go`. Short-lived, single-use challenge approved by an already-authenticated phone, so no reusable secret is typed on a shared/streamed/kiosk client.
- **Token vault / BFF** — `backend/services/credvault/tokenvault.go`, `cmd/server/routes_oauth.go`. OAuth/OIDC refresh tokens stored server-side, encrypted at rest (existing credvault AES-256-GCM). The client gets only a session reference; the backend makes credentialed outbound calls. `TestRefreshTokenNeverInHTTPResponse` asserts the refresh token never appears in a client-bound response or header.

### Top STRIDE threats

| # | Category | Threat |
|---|----------|--------|
| 1 | **Information Disclosure** | A connected-service refresh token leaks to the client (or to another tenant) and grants perpetual access. |
| 2 | **Spoofing** | A phishing origin replays a captured password/2FA against the real backend. |
| 3 | **Elevation of Privilege** | A QR login challenge is replayed or approved by the wrong account, granting a kiosk an unintended session. |

### What this does NOT protect (honesty)
- The BFF isolates the **durable token, not the entry moment** — a client compromised at the instant of auth can abuse that *session* while it lasts, but never gains the long-lived refresh token.
- **Pixel-streaming a login does not protect a secret typed on a compromised client** — keystrokes originate on the client regardless of where the screen renders. This is why Vulos does not stream logins.
- **Passkeys / out-of-band auth are the only things that make the credential un-capturable by an untrusted client.** A password can be captured at the point of entry however it is transported; a passkey private key cannot, because it never leaves the authenticator.

### Mitigations / invariants
- New connected-service integrations MUST route long-lived credentials through the token vault and MUST carry a test asserting the credential never reaches the client.
- QR challenges are single-use with TTL; egress proxy rejects private/SSRF ranges.
- Do not "add security" by streaming a login screen — strictly worse (cost, no benefit).

### Residual risks
- Session-scoped compromise on an already-infected client is out of scope of credential isolation (bounded, not eliminated).
- Passkey recovery / account-reset path must not become a weaker backdoor than the passkey itself (tracked with the recovery-ladder design).

---

## Overall Residual Risks

1. dm-verity key rotation protocol is unspecified — a compromised build key requires a re-image.
2. No intrusion detection or integrity monitoring at runtime (post-boot).
3. Audit logging coverage is incomplete across identity and sandbox events.
