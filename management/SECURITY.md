# Security Policy — Vulos Management

Vulos Management is the open-source (MIT) operational control plane for
Vulos. It handles account authentication, sessions, device enrollment,
routing, and (once wired) the admin console — real attack surface, whether
you're self-hosting or running the code behind the commercial `vulos-cloud`
build. We take reports seriously and ask that you report privately first.

## Supported versions

This project is pre-1.0 and moves quickly. Security fixes are made against
the `main` branch and released as the next patch/minor version — there is no
separate long-term-support branch yet. Please test against the latest
released version, or `main`, before reporting.

## Scope

### In scope

- Authentication and session management (registration, password auth via
  OPAQUE, TOTP 2FA, WebAuthn, linked OAuth sign-in)
- Device enrollment (RFC-8628 device-authorization flow) and audience-bound
  token minting
- The admin console access gate (`RequireSuperAdmin`) and org-admin
  authorization (`pkg/orgadmin`)
- The `billingport` / `storageport` seam boundary — e.g. a way to make the
  no-op billing rail charge, or the BYOB storage provisioner reach into a
  bucket it shouldn't
- The `internal/archtest` module boundary and any way to make an operational
  package reach a commercial dependency it shouldn't
- Request handling in `pkg/cproutes` (input validation, SSRF, injection,
  authorization bypass, CSRF)
- Secrets/KMS handling (`pkg/secrets`, `pkg/kms`)

### Out of scope

- Vulnerabilities that only manifest in the private `vulos-cloud` build (its
  commercial billing provider, managed bucket provisioner, deploy pipeline) —
  report those through `vulos-cloud`'s own channel, or to the address below if
  you don't have access to that repo
- Third-party dependencies with no Vulos-specific exploitation path — report
  upstream, but let us know too if it's reachable from this codebase
- Denial-of-service via raw traffic floods against a self-hoster's own
  deployment (an operational/infra concern, not a code vulnerability)
- Social engineering or physical attacks against Vulos staff or infrastructure
- Findings that require an attacker to already have superadmin/root access

## How to report

**Please do not open a public GitHub issue for a security vulnerability.**

- **Email:** security@vulos.org — please include affected component
  (auth / enrollment / admin console / a specific `pkg/…`), steps to
  reproduce, potential impact, and any suggested mitigation.
- **GitHub Security Advisories:** use the "Report a vulnerability" button in
  the Security tab of this repository. This is the preferred channel — it
  keeps the report private and lets us collaborate on a fix in a draft
  advisory before disclosure.

## Response targets

| Stage | Target |
|---|---|
| Acknowledgement | ≤ 72 hours |
| Initial triage | ≤ 7 days |
| Fix or tracked mitigation (critical/high) | ≤ 90 days |

Authentication-bypass or admin-console-gate-bypass reports are treated as
critical and prioritized above this baseline.

## Safe harbor

We will not pursue legal action against researchers who, in good faith:

- Report a vulnerability privately through one of the channels above before
  any public disclosure
- Make a good-faith effort to avoid privacy violations, data destruction, and
  service disruption
- Only interact with accounts and data they own, or with explicit permission
- Do not exploit a finding beyond what is necessary to demonstrate it

## Disclosure

We aim to agree on a coordinated disclosure timeline with the reporter once a
fix is available. In the absence of an agreement, we default to public
disclosure within 90 days of the initial report.
