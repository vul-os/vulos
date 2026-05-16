# Device Profiles

Vulos supports multiple device form factors through a profile system. Profile is selected during initial setup.

> **Goal.** One codebase, four UIs. `pc` (default), `tv` (10-foot, D-pad spatial nav), `car` (large targets + auto-DND), `watch` (companion, Qt). Profile is auto-detected at first boot, overridable from settings.
> **Non-goals.** Separate forks per form factor. We re-use the same React shell and switch layout/behavior through a `data-device-profile` attribute on the root.
> **Status.** Profile model, detection, persistence, TV spatial navigation, and TV 10-foot home are in. Outstanding: car driving mode (DEVPROF-06).

## Profiles

### PC / Tablet / Mobile
- **Base:** Debian Trixie slim (postmarketOS for mobile)
- **UI:** WPE WebKit — full Vulos experience, responsive layout
- **Same codebase** — responsive design handles screen size differences
- No separate builds needed, one image adapts to form factor

### TV
- **Base:** Debian Trixie slim
- **UI:** WPE WebKit — 10-foot UI
- Remote/d-pad navigation
- Large text, high contrast, couch-distance readability
- Focus on media, streaming, smart home controls
- Voice input as primary interaction method

### Car
- **Base:** Debian Trixie slim
- **UI:** WPE WebKit — simplified, voice-heavy
- Large touch targets for in-motion safety
- Minimal visual distraction, glanceable UI
- Voice-first interaction (AI chat, navigation, calls)
- Fast boot via suspend-to-RAM
- Future consideration: migrate to AGL if boot time / stability requires it

### Watch
- **Base:** AsteroidOS (Linux-based, Qt/QML)
- **UI:** Native Qt — no WebKit
- Thin client / companion device, not a full OS
- Connects to Vulos on phone/PC
- Features:
  - AI chat (voice + quick replies)
  - Notifications from Vulos ecosystem
  - Quick actions (smart home, music, etc.)
  - Basic health/fitness tracking

## TODO

### Setup Flow
- [ ] Profile selection screen during first boot
- [ ] Detect form factor automatically where possible (screen size, device type)
- [ ] Allow manual override of detected profile

### TV Profile
- [ ] 10-foot UI layout — large cards, readable from distance
- [ ] D-pad / remote navigation system (arrow keys + select)
- [ ] CEC support (HDMI control from TV remote)
- [ ] Media-focused home screen
- [ ] Voice input integration

### Car Profile
- [ ] Simplified UI with large touch targets
- [ ] Voice-first Portal mode
- [ ] Suspend-to-RAM for fast resume
- [ ] Bluetooth hands-free integration
- [ ] GPS / navigation service integration
- [ ] Do-not-disturb / driving mode

### Watch Companion App
- [ ] AsteroidOS Qt app scaffold
- [ ] Bluetooth pairing with Vulos phone/PC
- [ ] Notification sync protocol
- [ ] AI chat interface (voice + canned replies)
- [ ] Quick action system
- [ ] Health/fitness data collection

### Shared
- [ ] Profile-aware service orchestration (AI decides what to surface per form factor)
- [ ] Cross-device sync (watch ↔ phone ↔ PC ↔ TV ↔ car)
- [ ] Unified notification system across all profiles
