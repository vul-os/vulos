# AUTHENTICATION

System-level authentication infrastructure for Vula OS. Replaces the need for a mobile phone across banking, government, healthcare, enterprise, and every service that currently demands SMS OTP or a phone app.

The core insight: a Vula instance with a TPM is a better "possession factor" than a phone. It's always on, doesn't get lost, doesn't change every 2 years, and has equivalent secure storage. The phone was never the point — proof of possession of a private key was.

This is not a single app. It's an OS-level service that every Vula app uses when authenticating against external services.

---

## Difficulty Rating Scale

| Rating | Meaning | Typical effort |
|--------|---------|---------------|
| ★ | Straightforward, well-documented libraries, standard integration | Days |
| ★★ | Moderate complexity, some protocol work, cross-component wiring | 1-2 weeks |
| ★★★ | Significant engineering, browser-server bridge work, new subsystems | 2-4 weeks |
| ★★★★ | Hard, novel architecture, hardware integration, security-critical | 1-2 months |
| ★★★★★ | Research-grade, regulatory dependency, ecosystem not yet ready | 3+ months |

---

## Priority 1: Immediate Impact

### 1.1 TOTP Generator

**Difficulty: ★**
**Unblocks:** banking, crypto exchanges, email, social media, enterprise SSO — anything that accepts Google Authenticator codes today.

A built-in TOTP (Time-based One-Time Password, RFC 6238) generator that lives in the OS, not on a phone.

**How it works:**

```
1. User scans QR code or enters secret key when setting up 2FA on any site
2. Vula stores the secret in the encrypted keychain
3. System UI shows a rolling 6-digit code (30-second window)
4. User copies code into the site, or Vula auto-fills it
```

**Implementation:**

```
~/.vulos/auth/
  └── totp/
      ├── keychain.enc      (encrypted TOTP secrets, AES-256-GCM)
      └── accounts.json     (metadata: service name, issuer, icon, last used)
```

- Go library: `github.com/pquerna/otp` — handles TOTP generation, QR parsing, secret storage
- Browser integration: Vula's system UI overlay shows the current code. Click to copy. Or auto-fill via a content script injected into the banking page.
- Backup: secrets sync across cluster nodes via S3/MinIO (encrypted at rest)
- Import: support Google Authenticator export format (`otpauth-migration://`) for users migrating from a phone

**UI:**

```
┌──────────────────────────────────┐
│  Authenticator              ✕    │
│                                  │
│  FNB Online Banking              │
│  ┌────────────────────┐          │
│  │     847 291        │  0:18    │
│  └────────────────────┘          │
│  tap to copy                     │
│                                  │
│  Capitec                         │
│  ┌────────────────────┐          │
│  │     193 742        │  0:04    │
│  └────────────────────┘          │
│                                  │
│  [+ Add account]                 │
└──────────────────────────────────┘
```

**API:**

```
POST   /api/auth/totp/add          → store new TOTP secret (from QR or manual entry)
GET    /api/auth/totp/list         → list registered accounts
GET    /api/auth/totp/code/:id     → get current code for an account
DELETE /api/auth/totp/:id          → remove an account
POST   /api/auth/totp/import       → import from Google Authenticator export
POST   /api/auth/totp/export       → encrypted export for backup/migration
```

---

### 1.2 Password Manager

**Difficulty: ★★**
**Unblocks:** every login on every site. Foundation for auto-fill, passkey storage, and client certificate management.

A system-level credential store. Not a browser extension — an OS service that the browser queries.

**How it works:**

```
1. User logs into a site in the Vula browser
2. Vula detects the login form, offers to save credentials
3. Credentials encrypted and stored in the auth keychain
4. Next visit: auto-fill username + password + TOTP code (if registered)
```

**Implementation:**

```
~/.vulos/auth/
  └── vault/
      ├── vault.enc            (encrypted credential database, AES-256-GCM)
      ├── vault.key            (key encrypted by master password or TPM-sealed)
      └── meta.json            (last sync, vault version, entry count)
```

- Encryption: AES-256-GCM with key derived from master password (Argon2id KDF) or TPM-sealed key
- Auto-fill: inject credentials into web pages via WebKit Web Extension API
- Sync: vault syncs across cluster nodes (encrypted blob, decrypted only on-device)
- Import: support Bitwarden, 1Password, KeePass, Chrome CSV export formats
- Generator: built-in password generator (random, passphrase, configurable length/complexity)

**Integration with TOTP:**
When auto-filling a login, if the site has a registered TOTP account, Vula auto-fills the 2FA code too. One-click login with full 2FA — no phone, no typing codes.

**API:**

```
POST   /api/auth/vault/unlock      → unlock vault with master password
POST   /api/auth/vault/lock        → lock vault (clear decrypted keys from memory)
GET    /api/auth/vault/entries      → list credentials (metadata only, not passwords)
GET    /api/auth/vault/entry/:id   → get full credential (requires vault unlocked)
POST   /api/auth/vault/entry       → save new credential
PUT    /api/auth/vault/entry/:id   → update credential
DELETE /api/auth/vault/entry/:id   → delete credential
POST   /api/auth/vault/generate    → generate a password
POST   /api/auth/vault/import      → import from external password manager
POST   /api/auth/vault/export      → encrypted export
```

---

## Priority 2: The Passkey Bridge

### 2.1 FIDO2/WebAuthn Local Bridge

**Difficulty: ★★★★**
**Unblocks:** passkey-based auth across all services adopting FIDO2. This is the future-proof replacement for SMS OTP. Banks, government, enterprise — everything converges here.

**The problem:**

The user is sitting at a local browser (their laptop/desktop). The banking session runs in a remote browser on the Vula server. When the bank sends a WebAuthn challenge, it goes to the remote browser — but the user's hardware key (YubiKey, laptop TPM, fingerprint reader) is on the local machine.

```
Bank ──challenge──► Vula browser (remote, no hardware key)
                         │
                         ? how does the challenge reach the local YubiKey?
                         │
                    Local browser (has the YubiKey plugged in)
```

**Solution: WebAuthn proxy over WebRTC data channel**

```
1. Bank sends WebAuthn challenge to remote Vula browser
2. Vula browser intercepts the challenge via WebKit Web Extension
3. Challenge serialized and sent over WebRTC data channel to local browser
4. Local browser's WebAuthn API prompts the user (touch YubiKey, fingerprint, etc.)
5. Signed assertion sent back over WebRTC data channel
6. Vula browser injects the assertion as if the local authenticator responded
7. Bank verifies — sees a valid FIDO2 response
```

**Components:**

```
Remote (Vula server):
  WebKit Web Extension
    → intercepts navigator.credentials.get() / .create()
    → serializes PublicKeyCredentialRequestOptions
    → sends over data channel: { channel: "webauthn", payload: <challenge> }
    → receives signed assertion
    → resolves the original Promise with the assertion

Local (user's machine):
  JavaScript in the streaming client page
    → receives challenge from data channel
    → calls navigator.credentials.get() on local browser
    → user touches YubiKey / scans fingerprint
    → sends signed assertion back over data channel
```

**Data channel:**

A new WebRTC data channel alongside the existing mouse/keyboard/gamepad channels:

```go
webauthnChannel, _ := peerConnection.CreateDataChannel("webauthn", &webrtc.DataChannelInit{
    Ordered: &ordered,    // true — challenges must not reorder
})
```

Reliable + ordered. Challenges and responses are small (<4KB) and infrequent.

**Security considerations:**

- The challenge and assertion flow over the encrypted WebRTC data channel (DTLS-SRTP) — no MITM possible
- The private key never leaves the local device — only the signed assertion crosses the bridge
- The Vula server never sees the private key
- Replay protection is built into WebAuthn (challenge is one-time, signed with a nonce)
- Origin binding: the bank sees the remote browser's origin. The local authenticator signs for that origin. This requires the local bridge to pass the correct relying party ID.

**Passkey storage on the Vula instance itself:**

For users without a YubiKey or local TPM, the Vula instance can BE the authenticator:

- Generate and store passkeys in the instance's TPM (bare metal) or software keystore (cloud)
- No bridge needed — the remote browser uses the server-side authenticator directly
- Trade-off: the "possession factor" is the Vula instance itself, not a physical device you carry. Still better than SMS.

```
~/.vulos/auth/
  └── passkeys/
      ├── credentials.enc     (FIDO2 credentials, TPM-sealed or AES-encrypted)
      └── metadata.json       (relying party IDs, creation dates, last used)
```

**API:**

```
GET    /api/auth/passkeys              → list registered passkeys (metadata)
POST   /api/auth/passkeys/register     → initiate passkey registration for a site
DELETE /api/auth/passkeys/:id          → remove a passkey
GET    /api/auth/passkeys/bridge/status → WebAuthn bridge connection status
PUT    /api/auth/passkeys/settings     → configure: prefer local bridge vs server-side
```

---

### 2.2 USB Passthrough for Hardware Keys

**Difficulty: ★★★**
**Unblocks:** YubiKey, SoloKey, Titan Key, and any USB security key working directly with the remote Vula browser without the WebAuthn bridge.

Alternative to the WebAuthn proxy: forward the USB device itself to the remote server.

**How it works:**

```
YubiKey plugged into local machine
  → USB/IP or WebUSB forwards the device to Vula server
    → Remote browser sees a "local" USB HID device
      → WebAuthn works natively, no bridge needed
```

**Implementation options:**

1. **USB/IP (Linux kernel module)** — forward USB devices over the network. Server sees a virtual USB device. Works for bare metal Vula instances.
   - Client: `usbip bind --busid=1-1` (share the device)
   - Server: `usbip attach --remote=<client-ip> --busid=1-1` (mount the device)
   - Limitation: requires root on both sides, adds latency

2. **WebUSB API** — browser-based USB access. The local browser can read from the YubiKey and relay data over WebRTC.
   - More portable than USB/IP
   - Works from any browser that supports WebUSB (Chrome/Edge, not Safari/Firefox yet)
   - Limitation: WebUSB doesn't expose FIDO HID devices directly (security restriction)

3. **Virtual FIDO device** — create a virtual USB HID device on the Vula server that proxies to the real key on the local machine. The remote browser talks to the virtual device natively.
   - Library: `github.com/AlanRMcD/usb-gadget` or Linux USB gadget API
   - Most transparent: the remote browser doesn't know it's proxied

**Recommendation:** the WebAuthn bridge (2.1) is simpler and works everywhere. USB passthrough is a bonus for power users who want native device access. Implement 2.1 first, 2.2 as an enhancement.

---

## Priority 3: Device Trust

### 3.1 TPM Integration

**Difficulty: ★★★**
**Unblocks:** hardware-backed key storage, device attestation, secure boot verification. Foundation for everything that follows.

**What the TPM provides:**

- **Key generation inside the TPM** — private keys are generated and stored in hardware, never extractable
- **Sealing/unsealing** — encrypt data that can only be decrypted on this specific device, in this specific boot state
- **Attestation** — cryptographic proof of what software is running (boot chain, OS version)
- **Monotonic counter** — prevents replay attacks on sealed data

**Implementation:**

```go
import "github.com/google/go-tpm/tpm2"

// Open TPM
tpm, _ := tpm2.OpenTPM("/dev/tpmrm0")

// Generate a key that never leaves the TPM
key, _ := tpm2.CreatePrimary(tpm, tpm2.HandleOwner, tpm2.Public{
    Type:    tpm2.AlgECC,
    NameAlg: tpm2.AlgSHA256,
    ECCParameters: &tpm2.ECCParams{
        CurveID: tpm2.CurveNISTP256,
    },
})

// Sign data with the TPM-held key (key never leaves hardware)
sig, _ := tpm2.Sign(tpm, key, digest)

// Seal data to current PCR state (only decryptable on this device, this boot config)
sealed, _ := tpm2.Seal(tpm, key, pcrSelection, data)
```

**What gets stored in the TPM:**

| Secret | Purpose |
|--------|---------|
| Device identity key | Unique per-instance, used for attestation |
| Vault master key | Encrypts the password manager vault |
| TOTP encryption key | Encrypts stored TOTP secrets |
| Passkey private keys | FIDO2 credentials for external services |
| Client certificate keys | mTLS private keys for banking/enterprise |
| Peering identity key | Ed25519 key from PEERING.md (optional, can also be file-based) |

**Fallback for cloud instances (no hardware TPM):**

- AWS: Nitro TPM (vTPM, available on most instance types)
- GCP: vTPM via Shielded VM
- Azure: vTPM via Trusted Launch
- Generic: SoftHSM2 as software fallback (encrypted keystore, not hardware-backed but still better than plaintext files)

```
~/.vulos/auth/
  └── tpm/
      ├── device_key.pub       (public half of TPM-held device key)
      ├── attestation.json     (latest attestation report)
      └── pcr_policy.json     (expected PCR values for seal/unseal)
```

**API:**

```
GET  /api/auth/device/identity       → device public key + attestation
GET  /api/auth/device/attestation    → generate fresh attestation report
POST /api/auth/device/seal           → seal data to current device state
POST /api/auth/device/unseal         → unseal data (fails if device state changed)
GET  /api/auth/device/tpm/status     → TPM availability, type (hardware/virtual/software)
```

---

### 3.2 Verified Boot Chain

**Difficulty: ★★★★**
**Unblocks:** device attestation that banks and enterprise services trust. Proves the OS hasn't been tampered with.

**Boot chain:**

```
UEFI Secure Boot
  → signed bootloader (GRUB/systemd-boot)
    → signed kernel (Alpine/postmarketOS)
      → dm-verity root filesystem (hash-verified, read-only)
        → Vula OS services start
          → TPM PCRs contain measurements of entire chain
```

Each stage measures (hashes) the next stage into TPM PCR registers before executing it. The final PCR state is a fingerprint of the entire boot chain. If any component was modified, the PCRs don't match expected values and:

- TPM-sealed secrets won't unseal (vault key, passkeys inaccessible)
- Remote attestation reports the mismatch
- The device is flagged as untrusted

**Implementation:**

- Kernel: Alpine/postmarketOS with `CONFIG_IMA=y` (Integrity Measurement Architecture)
- Root filesystem: dm-verity with signed root hash
- Boot: UEFI Secure Boot with Vula-signed bootloader and kernel
- Signing key: Vula OS release key (users can also enroll their own keys for custom builds)

**For bare metal only.** Cloud instances rely on the cloud provider's Secure Boot (Shielded VM, Nitro). Docker dev environments skip this entirely.

---

### 3.3 Device Attestation Service

**Difficulty: ★★★★**
**Unblocks:** proving to external services that the device is genuine and unmodified. Equivalent of Android's Play Integrity or Apple's Device Attestation.

**What it proves:**

```
"This request comes from:
  - a genuine Vula OS instance (verified boot chain)
  - running version X.Y.Z (signed, unmodified)
  - with a hardware TPM (not emulated)
  - that has been continuously running since [timestamp]
  - and the user authenticated via [method]"
```

**How external services consume it:**

```
1. Bank challenges the Vula instance: "prove your device is trusted"
2. Vula instance generates attestation report:
   - TPM quote (signed PCR values)
   - Device certificate chain (TPM → Vula CA → device key)
   - OS version + build hash
3. Bank verifies against Vula's published expected values
4. If valid: device is trusted, reduce friction (skip SMS, lower auth requirements)
```

**Vula attestation endpoint:**

```
GET /api/auth/device/attestation
→ {
    "device_id": "<TPM-backed public key>",
    "os_version": "vula-0.1.0",
    "boot_chain_hash": "sha256:...",
    "tpm_quote": "<signed PCR values>",
    "certificate_chain": ["<device cert>", "<vula intermediate>", "<vula root>"],
    "timestamp": "2026-04-02T14:30:00Z"
  }
```

**Vula CA:** vulos.org operates a certificate authority that signs device keys during registration. The CA's root certificate is published and can be trusted by external services. This is the same model as Apple's Device Attestation — Apple's CA signs each device's Secure Enclave key.

---

## Priority 4: Credential Bridges

### 4.1 Client Certificate Store (mTLS)

**Difficulty: ★★**
**Unblocks:** mutual TLS with banks (especially EU, corporate banking), enterprise VPNs, government portals that support client certificates.

**How it works:**

```
1. Bank/service issues a client certificate to the user
2. Certificate + private key stored in Vula's auth keychain (TPM-backed)
3. When connecting to the bank, WebKit presents the client certificate during TLS handshake
4. Bank verifies: "this is the device we issued the cert to"
5. No username, no password, no OTP needed — the certificate IS the authentication
```

**Implementation:**

```
~/.vulos/auth/
  └── certificates/
      ├── <domain>/
      │   ├── client.crt        (X.509 client certificate)
      │   ├── client.key.enc    (private key, TPM-sealed or AES-encrypted)
      │   └── ca-chain.pem      (issuer's certificate chain)
      └── ...
```

- WebKit configuration: set client certificate per-domain via WebKit network settings
- Auto-selection: when a server requests client auth, Vula matches the domain and presents the right cert
- Renewal: track expiry dates, notify user, auto-renew if the issuer supports ACME or EST (RFC 7030)

**API:**

```
POST   /api/auth/certs/install         → install client certificate + key
GET    /api/auth/certs                  → list installed certificates (metadata)
DELETE /api/auth/certs/:domain          → remove certificate
GET    /api/auth/certs/:domain/status   → expiry, issuer, usage stats
POST   /api/auth/certs/generate-csr    → generate a CSR for requesting a new cert
```

---

### 4.2 SMS Receive (Fallback for Legacy Services)

**Difficulty: ★★**
**Unblocks:** any service that absolutely requires SMS OTP and has no alternative. South African banks (Capitec, FNB), many services in Africa/Asia.

**Options:**

1. **VoIP number with SMS** — Twilio, MessageBird, or local telco API. Vula instance registers a virtual number, receives SMS via webhook, displays OTP in notification system.

2. **eSIM (bare metal)** — for devices with a cellular modem. Vula manages an eSIM, receives SMS natively. Most complete solution but hardware-dependent.

3. **Bluetooth SMS bridge** — pair with a phone (if you have one but don't want to depend on it for every login). Phone receives SMS, Vula reads it over Bluetooth. Reduces dependency without eliminating the phone.

4. **Email-to-SMS** — some services offer email as alternative to SMS. Vula routes these through the notification system.

**Recommended: VoIP number.** Cheapest, most portable, works on cloud and bare metal.

**Implementation (Twilio example):**

```
1. User provisions a number: POST /api/auth/sms/provision → allocates a Twilio number
2. Twilio webhook configured to POST incoming SMS to Vula instance
3. Vula receives SMS, extracts OTP (regex: 4-8 digit code)
4. OTP displayed as a system notification (NOTIFICATIONS.md)
5. Auto-fill into the page that's waiting for it
```

```
~/.vulos/auth/
  └── sms/
      ├── config.json          (provider, number, webhook URL)
      └── history.json         (recent SMS, auto-pruned, 24h retention)
```

**Cost:** Twilio: ~$1/month for a number + $0.0075 per SMS received. Negligible.

**API:**

```
POST /api/auth/sms/provision        → get a virtual phone number
GET  /api/auth/sms/number           → current number
POST /api/auth/sms/webhook          → receive incoming SMS (called by Twilio)
GET  /api/auth/sms/recent           → recent OTPs
PUT  /api/auth/sms/settings         → auto-fill preferences, retention
```

---

## Priority 5: Digital Identity

### 5.1 Verifiable Credentials Wallet

**Difficulty: ★★★★★**
**Unblocks:** EU Digital Identity (eIDAS 2.0, mandatory for EU banks by 2027), digital ID cards, driver's licenses, professional credentials, age verification.

**What verifiable credentials are:**

A digitally signed document issued by an authority (government, university, employer) that proves something about you. Stored on your device, presented when needed, with selective disclosure.

```json
{
  "@context": "https://www.w3.org/2018/credentials/v1",
  "type": ["VerifiableCredential", "IdentityCredential"],
  "issuer": "did:web:homeaffairs.gov.za",
  "credentialSubject": {
    "id": "did:key:z6Mkf5r...",
    "name": "Alice Nkosi",
    "dateOfBirth": "1990-05-15",
    "nationality": "ZA",
    "idNumber": "9005150012083"
  },
  "proof": {
    "type": "Ed25519Signature2020",
    "verificationMethod": "did:web:homeaffairs.gov.za#key-1",
    "proofValue": "z3FXQje..."
  }
}
```

**Selective disclosure with ZK proofs:**

Instead of showing the entire credential, prove only what's needed:

```
Bank asks: "Are you over 18 and a South African citizen?"

Without ZK: hand over full ID document (name, DOB, ID number, address — everything)

With ZK: generate a proof that:
  ✓ dateOfBirth is before 2008-04-02 (over 18)
  ✓ nationality == "ZA"
  ✗ name not revealed
  ✗ idNumber not revealed
  ✗ exact dateOfBirth not revealed
```

**Implementation:**

- Standards: W3C Verifiable Credentials Data Model 2.0, DIF Presentation Exchange, ISO mDL 18013-5
- ZK library: `github.com/ConsenSys/gnark` (Go-native)
- DID method: `did:web` (domain-based, fits the Vula architecture — `did:web:alice.vulos.org`)
- Storage: credentials encrypted in auth keychain, TPM-sealed

```
~/.vulos/auth/
  └── credentials/
      ├── <credential-id>.vc.enc     (encrypted verifiable credential)
      ├── <credential-id>.meta.json  (issuer, type, expiry, disclosure policy)
      └── presentations/             (cached ZK proofs, short-lived)
```

**API:**

```
POST   /api/auth/credentials/store            → store a received credential
GET    /api/auth/credentials                   → list credentials (metadata)
GET    /api/auth/credentials/:id               → full credential (requires auth)
DELETE /api/auth/credentials/:id               → delete credential
POST   /api/auth/credentials/present           → generate a verifiable presentation (with selective disclosure)
POST   /api/auth/credentials/verify            → verify a received credential/presentation
```

**Timeline dependency:** this depends on governments issuing digital credentials. EU is on track for 2027. South Africa's digital ID timeline is unclear. Build the wallet now, credentials will come.

---

### 5.2 Behavioral Authentication

**Difficulty: ★★★★**
**Unblocks:** continuous authentication, reduced friction for trusted sessions, risk-based auth that banks are adopting.

**What it captures:**

Vula already streams input events over WebRTC data channels (mouse, keyboard, gamepad). These patterns are unique per person:

- **Typing cadence** — key hold duration, inter-key timing, error patterns
- **Mouse movement** — velocity curves, acceleration, click patterns, scroll behavior
- **Session patterns** — what apps used, in what order, at what times
- **Interaction rhythm** — pause patterns, reading speed, navigation habits

**How it works:**

```
1. During normal use, Vula builds a behavioral profile (local, never sent externally)
2. Profile is a statistical model: "this is how this user interacts"
3. On each session, current behavior is compared against the profile
4. Trust score: 0.0 (definitely not the user) to 1.0 (definitely the user)
5. If trust score drops (someone else using the machine, stolen session):
   → Step-up authentication triggered (password, passkey, biometric)
6. If trust score stays high:
   → Reduced friction (skip OTP for trusted transactions)
```

**Privacy:**

- All processing local — behavioral data never leaves the instance
- Model stored encrypted, TPM-sealed
- External services see only a trust score, not the underlying data
- User can disable at any time
- No data shared between Vula instances via peering

**Implementation:**

- Input capture: already exists (WebRTC data channels for mouse/keyboard)
- Model: lightweight ML model (random forest or small neural net) running on-device
- Training: first 2 weeks of use, then continuous adaptation
- Library: `github.com/sjwhitworth/golearn` or ONNX Runtime for Go

```
~/.vulos/auth/
  └── behavioral/
      ├── model.enc            (trained behavioral model, encrypted)
      ├── config.json          (sensitivity, enabled features, step-up threshold)
      └── stats.json           (trust score history, step-up events)
```

**API:**

```
GET  /api/auth/behavioral/score        → current trust score
GET  /api/auth/behavioral/status       → model status (training/ready/disabled)
PUT  /api/auth/behavioral/settings     → configure sensitivity, enable/disable
POST /api/auth/behavioral/reset        → reset model (re-train from scratch)
```

---

## Priority 6: Open Banking Integration

### 6.1 Direct Bank API Access

**Difficulty: ★★★**
**Unblocks:** native banking apps on Vula that don't need a browser, don't need SMS, authenticate via client certificates and passkeys.

**How it works:**

Instead of loading a bank's website in the browser, a Vula app talks directly to the bank's Open Banking API:

```
Vula Banking App ──OAuth 2.0 + mTLS──► Bank API
                                         │
                                         ▼
                                    Account data, transactions,
                                    payments (PSD2/Open Banking)
```

**Where Open Banking APIs exist today:**

| Region | Framework | Status |
|--------|-----------|--------|
| EU | PSD2 / PSD3 | Live, all banks |
| UK | Open Banking Standard | Live, major banks |
| Brazil | Open Finance | Live, all banks |
| India | Account Aggregator | Live, growing |
| Australia | CDR (Consumer Data Right) | Live, major banks |
| South Africa | Rapid Payments Programme | In progress |
| Nigeria | Open Banking Nigeria | Pilot |

**South Africa specifics:**

SA doesn't have mandated Open Banking yet, but:
- Investec has a public API (REST, OAuth 2.0) — usable today
- Standard Bank has API marketplace — developer access available
- Stitch (stitch.money) aggregates SA bank data via screen scraping + API where available
- The SA Reserve Bank's Rapid Payments Programme is building the rails

**Implementation:**

- OAuth 2.0 + PKCE for user authorization
- mTLS for app-to-bank transport security (uses client certificate from 4.1)
- FIDO2 for step-up authentication on sensitive operations (uses passkey from 2.1)
- Token storage in auth keychain (TPM-sealed refresh tokens)

```
~/.vulos/auth/
  └── banking/
      ├── connections/
      │   ├── <bank-id>/
      │   │   ├── oauth_tokens.enc    (access + refresh tokens, encrypted)
      │   │   ├── config.json         (API base URL, scopes, consent ID)
      │   │   └── cert/               (mTLS cert for this bank, if required)
      │   └── ...
      └── providers.json              (list of supported bank API providers)
```

**API:**

```
GET    /api/auth/banking/providers       → list supported banks/APIs
POST   /api/auth/banking/connect/:bank   → initiate OAuth flow with a bank
GET    /api/auth/banking/connections      → list connected bank accounts
DELETE /api/auth/banking/connections/:id  → revoke bank connection
POST   /api/auth/banking/refresh/:id     → refresh OAuth tokens
```

The actual banking data (accounts, transactions, payments) would be handled by a banking app, not the auth layer. The auth layer just manages the connections and credentials.

---

## Storage Summary

Everything lives under `~/.vulos/auth/`:

```
~/.vulos/auth/
  ├── totp/                  (TOTP secrets and account metadata)
  ├── vault/                 (password manager vault)
  ├── passkeys/              (FIDO2 credentials)
  ├── tpm/                   (device identity key, attestation)
  ├── certificates/          (client certificates, per-domain)
  ├── sms/                   (virtual number config, recent OTPs)
  ├── credentials/           (verifiable credentials wallet)
  ├── behavioral/            (behavioral auth model)
  ├── banking/               (Open Banking connections and tokens)
  └── config.json            (global auth settings: defaults, preferences)
```

All encrypted at rest. TPM-sealed where hardware TPM is available. Syncs across cluster nodes via S3/MinIO (encrypted blobs — decrypted only on-device).

---

## Relationship to Other Specs

```
AUTHENTICATION.md (this)
  ├── uses PEERING.md identity (Ed25519 keypair) as foundation
  ├── uses NOTIFICATIONS.md for OTP display, step-up auth prompts
  ├── uses CLUSTER.md for syncing auth data across nodes
  ├── uses PEERING-EXTENSIONS.md §3.1 (TPM integration) for key storage
  └── browser integration via existing WebRTC streaming infrastructure
```

The auth layer is horizontal — it sits below every app and above the hardware/OS. Any Vula app that needs external authentication calls the auth API instead of implementing its own credential management.

---

## Implementation Order

| # | Component | Difficulty | Priority | Dependencies |
|---|-----------|-----------|----------|-------------|
| 1 | TOTP Generator | ★ | Immediate | None |
| 2 | Password Manager | ★★ | Immediate | None |
| 3 | TPM Integration | ★★★ | High | Bare metal image |
| 4 | Client Certificate Store | ★★ | High | TPM (optional, can use software keystore) |
| 5 | SMS Receive (VoIP) | ★★ | High | Twilio/provider account |
| 6 | FIDO2/WebAuthn Bridge | ★★★★ | High | WebRTC data channel (exists), WebKit extension |
| 7 | USB Passthrough | ★★★ | Medium | Linux USB/IP, bare metal |
| 8 | Verified Boot Chain | ★★★★ | Medium | Bare metal image, UEFI |
| 9 | Device Attestation | ★★★★ | Medium | TPM, verified boot |
| 10 | Open Banking Integration | ★★★ | Medium | Client certs, OAuth |
| 11 | Behavioral Authentication | ★★★★ | Low | Input data pipeline, ML model |
| 12 | Verifiable Credentials | ★★★★★ | Low | TPM, ZK library, ecosystem readiness |

Start with 1 and 2 — they work today and eliminate the phone for most services. Then 3-6 build the foundation for the passkey future. The rest follows as the ecosystem matures.
