# Security Policy — Vulos

## Scope

### In scope
- Vulos OS shell and firstboot flow
- The sovereign assistant: proposal ledger / execute gate, egress Guard, prompt-injection handling
- Identity, credential handling, and the passkey / recovery-phrase master-key flow
- Files service ACL and content-blind (sealed) sharing
- App sandbox and privilege separation
- Backend API and authentication
- Build and update pipeline (signed images, dm-verity)

### Out of scope
- Third-party dependencies (upstream Go modules, npm packages) — report to their maintainers
- Social engineering, phishing, or attacks requiring physical device access
- Denial-of-service via resource exhaustion on personal hardware
- Vulnerabilities in infrastructure we do not control (DNS providers, CDNs)
- Issues already publicly disclosed or reported

## Telemetry / phone-home

Vulos OS collects **no** usage analytics, no crash reporting, and no
behavioural telemetry, and it has no analytics endpoint to send them to. There
is no Google Analytics, Sentry, PostHog, Segment, or comparable SDK anywhere in
the shell or the backend — the frontend ships only local assets (no external
scripts, fonts, or beacons). The subsystem named `services/telemetry` is a
purely **local** system monitor: it reads `/proc` and streams CPU / memory /
network / process stats over a WebSocket to *your own* browser dashboard.
Nothing it produces leaves the box.

What a **fresh, self-hosted box with nothing configured** talks to over the
network:

- **The signed OS auto-update check** — the only outbound connection a box
  makes by default. Every ~4 hours the box performs a read-only `GET` of a
  signed `os/stable.json` manifest from the OS distribution bucket to see
  whether a newer, signed OS image exists. It sends **no** usage, identity, or
  device data beyond the bare HTTP request itself; the manifest and any image
  are verified against a baked-in trust anchor (a poisoned mirror cannot serve a
  forged image). This is a security-update mechanism, not telemetry. Operators
  who want a box with **zero default egress** can turn it off with
  `VULOS_OS_AUTOUPDATE=off` and apply updates manually from the OS update admin
  screen.
- **Nothing else.** A box with no cloud, relay, llmux, or integrations
  configured makes no other outbound calls.

Everything beyond the update check is **opt-in and operator-configured**, and
fires only when *you* enable it:

- **Metrics/tracing** (`internal/obs`): Prometheus metrics are pull-only (you
  scrape `/metrics`); OpenTelemetry tracing stays a no-op unless you set
  `OTEL_EXPORTER_OTLP_ENDPOINT` to your own collector.
- **Cloud control-plane / relay / device enrollment** (fleet sync, push
  registrar, LAN-cert puller, billing): each is fail-safe **off** and activates
  only when the relevant endpoint + secret env vars are set — i.e. on a
  Vulos-managed box you enrolled, or one you pointed at your own control plane.
- **AI, and integrations** (Anthropic/OpenAI, Google/Microsoft/Dropbox/GCS,
  S3): bring-your-own credentials; requests go to the provider *you* configured
  and fire only on your action. Assistant LLM traffic is additionally fenced by
  the on-box sovereignty Guard.
- **Web Push**: opt-in per device; outbound-only to your device's browser
  vendor with end-to-end-encrypted payloads (the vendor routes but cannot read
  them).

In short: out of the box Vulos checks for signed OS updates and otherwise stays
silent; every other network destination is one you configured, your own box, or
your own peers — never a silent Vulos-owned analytics sink.

## How to Report

**Email:** security@vulos.org  
**PGP key:** TODO — not yet published. No encryption key exists for this
address today; treat reports over plain email as unencrypted until a key is
published at `https://vulos.org/.well-known/security.txt` and linked here. If
you need a private channel now, use GitHub Security Advisories below instead.
(If no tracking issue exists yet for publishing this key, please file one.)

**GitHub Security Advisories:** Use the "Report a vulnerability" button in the Security tab of this repository. This is the preferred channel for most reporters as it keeps discussion private and structured.

Please include:
- Description of the vulnerability and affected component
- Steps to reproduce (proof-of-concept where safe to share)
- Potential impact
- Any suggested mitigations

## Response SLA

| Stage | Target |
|-------|--------|
| Acknowledgement | ≤ 72 hours |
| Initial triage (severity, affected versions) | ≤ 7 days |
| Fix or tracked mitigation published | ≤ 90 days for critical/high; tracked publicly for lower severity |

We aim to keep reporters informed at each stage. If you have not received an acknowledgement within 72 hours, please follow up.

## Safe Harbor

Vulos commits to not pursuing legal action against researchers who:
- Act in good faith to identify and report vulnerabilities
- Do not exploit a vulnerability beyond the minimum needed to demonstrate it
- Do not access, modify, or exfiltrate user data
- Do not disrupt production services
- Disclose to us before making the issue public

We consider good-faith security research a public good and will not characterise it as unauthorised access.

## Bug Bounty

There is no paid bug-bounty program at this time. We acknowledge reporters by name (or pseudonym) in release notes and our CHANGELOG unless they prefer to remain anonymous.

## Credit Policy

We credit every confirmed reporter in the release that fixes their finding, in the format:

> Thanks to [Name / Handle] for responsibly disclosing [CVE-XXXX-XXXXX / summary].

Reporters may request anonymity at any time.
