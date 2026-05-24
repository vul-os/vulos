# Security Policy — Vulos Office

## Scope

### In scope
- CRDT fabric and conflict-resolution logic
- PDF auto-sign pipeline
- Spaces (real-time collaboration) signaling and data channels
- Backend API authentication and session handling
- File upload / blob storage access controls

### Out of scope
- Third-party Go and npm dependencies — report to upstream maintainers
- Social engineering or phishing
- Denial-of-service via document flooding (operational concern)
- Vulnerabilities in the underlying OS or browser outside our control

## How to Report

**Email:** security@vulos.org  
PGP key: _placeholder — key will be published at https://vulos.org/.well-known/security.txt_

**GitHub Security Advisories:** Use the "Report a vulnerability" button in the Security tab of this repository. Preferred channel.

Please include:
- Affected component (CRDT, signing, Spaces, auth, storage)
- Steps to reproduce
- Potential impact
- Any suggested mitigations

## Response SLA

| Stage | Target |
|-------|--------|
| Acknowledgement | ≤ 72 hours |
| Initial triage | ≤ 7 days |
| Fix or tracked mitigation | ≤ 90 days for critical/high |

## Safe Harbor

Vulos commits to not pursuing legal action against researchers who:
- Act in good faith to identify and report vulnerabilities
- Do not exploit beyond demonstrating the issue
- Do not access, modify, or exfiltrate user documents
- Do not disrupt collaboration sessions
- Disclose to us before public disclosure

## Bug Bounty

No paid bug-bounty program at this time. Confirmed reporters are credited in release notes.

## Credit Policy

Every confirmed finding is credited in the release that ships the fix:

> Thanks to [Name / Handle] for responsibly disclosing [CVE-XXXX-XXXXX / summary].
