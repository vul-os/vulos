# Security Policy — Vulos

## Scope

### In scope
- Vulos OS shell and firstboot flow
- vumail identity management and credential handling
- App sandbox and privilege separation
- Backend API and authentication
- Build and update pipeline (signed images, dm-verity)

### Out of scope
- Third-party dependencies (upstream Go modules, npm packages) — report to their maintainers
- Social engineering, phishing, or attacks requiring physical device access
- Denial-of-service via resource exhaustion on personal hardware
- Vulnerabilities in infrastructure we do not control (DNS providers, CDNs)
- Issues already publicly disclosed or reported

## How to Report

**Email:** security@vulos.org  
PGP key: _placeholder — key will be published at https://vulos.org/.well-known/security.txt_

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
