# Living in Vulos OS Day to Day

Your daily driver's manual for the Vulos OS desktop: first-boot setup, the shell (windows, desktops, dock, Mission Control), the Cmd-K palette, notifications, the Settings app, accounts and locking, and every keyboard shortcut the shell actually binds.

This chapter assumes you already have a running box — see [GETTING-STARTED.md](GETTING-STARTED.md) for installation and [CONFIGURATION.md](CONFIGURATION.md) for environment variables and flags.

---

## First boot: the setup wizard

The first time you open your box in a browser, the shell checks `GET /api/setup/status`. If setup has never been completed, you land in the setup wizard instead of the login screen.

The very first choice ("How would you like to set up?") splits the wizard into two flows:

- **Start fresh** — the full new-system flow below.
- **Join an existing installation** — a short flow that connects to your existing S3-compatible storage with your encryption passphrase, syncs in the background, then asks only for a lock-screen PIN. Use this when restoring or adding a node. See [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

The new-system flow walks through these steps (a few are skippable):

| Step | What happens |
|---|---|
| Welcome | "Get Started". Vula is isiZulu for "open". |
| Set up / Join | Start fresh, or join an existing installation. |
| Device type | PC / Tablet / Mobile, TV, Car, or Watch — auto-detected, and it shapes the whole UI (the TV profile gets a 10-foot remote-driven home, for example). |
| Language | 15 languages, English through isiZulu to Japanese. |
| Timezone | Picked on a world map; defaults to what your browser reports. |
| Network | Scan and join WiFi, or skip if you are on Ethernet. |
| Manage this device | Local-only account, or connect Vulos Cloud. "You can always connect to Vulos Cloud later from Settings." |
| Your Vulos account | Display name, username, password. This becomes the **administrator** account, and the same credentials work for `sudo` in the Terminal. |
| Vulos username | Choose a username for your Vulos account (cloud identity). Sign-in uses your email/password or a linked Google/Microsoft account. |
| Lock Screen PIN | Optional 4–8 digit PIN for the lock screen. Skippable; you can set it later in Settings. |
| How will you use Vulos? | Intent question that tunes defaults (relay, AI tokens, backup quota, dedicated IP). |
| Your apps | Everything is pre-checked by default — the owned productivity app Ofisi (Docs/Sheets/Slides/PDF/whiteboards) plus the PIM apps. Opt out of anything; you can add it back later. Files, Calendar and Contacts are always included. Calendar/Contacts and mail connect to a mailbox you already own (Gmail/Outlook/any IMAP/SMTP, via lilmail) — there is no Vulos-hosted mailbox and no Vulos-hosted email address. See [APPS.md](APPS.md). |
| Appearance | Dark / Light / Auto theme. |
| Node identity | Shows your instance's read-only ULID and lets you set a hostname (lowercase letters, numbers, hyphens). |
| Storage | Optionally connect S3-compatible storage with an encryption passphrase. |
| SSH | Optionally generate an Ed25519 SSH keypair for the box — the private key is shown once for you to copy. |
| Recovery kit | A versioned credentials JSON (instance ULID, hostname, checksum) you must download and confirm — type `confirm` to proceed. Shown once. |
| Ready | Summary of everything chosen; "Enter" creates the account and finishes. |

### The recovery phrase

When the wizard creates your account, the server mints a per-user master key and returns a **24-word recovery phrase — exactly once**. The wizard forces a Proton-style reveal screen: you must tick "I have written down or securely stored my 24-word recovery phrase" before you can continue. The warning is literal: without the phrase, **a forgotten password cannot be recovered** — the master key wraps your encrypted content. Store it offline. More in [SECURITY.md](SECURITY.md) and [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

The recovery **kit** (the JSON file) is separate from the recovery **phrase**. An admin can re-download the kit later from a trusted local session:

```
GET /api/recovery/kit
```

The phrase is never re-shown.

### Optional private AI

Right before entering the desktop, the wizard offers to download an on-box embedding model ("Enable private AI search"). It is genuinely optional — search works in lexical mode without it — and the model is fetched from a pinned source and SHA-256-verified. See [ASSISTANT.md](ASSISTANT.md).

### If you sign up with Vulos Cloud

Choosing a cloud-managed account adds a short post-signup wizard:

1. **Two-factor authentication** — scan a QR into any TOTP app, confirm with a 6-digit code, and save the 10 one-time recovery codes (copy or download as `.txt`; shown once).
2. **Email verification** — a nudge with a resend button; you can continue without verifying and deal with the banner later.

See [CLOUD.md](CLOUD.md) for what cloud enrollment actually does and does not give the cloud access to.

---

## The desktop at a glance

After login you get the desktop shell: a wallpaper, a translucent **menu bar** across the top, the **Home** surface behind everything, and windows stacking above it.

### The menu bar

Left to right:

- **vula** — the system menu: your profile, hostname, uptime, live CPU / memory / temperature, and **Log Out**.
- **Desktop indicator** — "Desktop 2" plus a close button, shown only when you have more than one virtual desktop.
- **Applications** (rocket icon) — opens the Launchpad.
- **Mission Control** (stacked-windows icon) — same as pressing F3.
- **Trust badge** — the always-on sovereignty indicator: which AI tier is active and what leaves this box. Click it to open the transparency panel.
- **Chat** — toggles the assistant chat panel on the right edge.
- **Fullscreen** — browser fullscreen on/off (F11 also works, since Vulos runs in a browser).
- **WiFi** — connection status; the dropdown scans and joins networks.
- **Battery** — percentage, charging state, temperature, uptime (only on hardware that reports a battery).
- **Notifications** (bell) — the Notification Center, described below.
- **Theme toggle** — dark / light.
- **Clock** — time and date; the dropdown is a calendar.

### Home — the default surface

When no windows are open on the current desktop, you see **Home**: not a launcher, a brief. It aggregates, in one round-trip to `GET /api/assistant/home`:

- **What needs you today** — the assistant's attention items, with snooze/handled actions.
- **Agenda** — today's and upcoming events and reminders.
- **Recent activity** — a light feed.
- **A composer** — ask the assistant anything; actions that change state come back as a proposal card you explicitly approve before anything runs.
- **Quick launch** — Mail, Calendar, Files, Assistant, Ofisi, Terminal, Settings, plus "All apps" for the full Launchpad.

Each section fails independently — if the assistant is offline the brief says so, and the rest of Home still renders. The brief is computed on your box by the on-instance assistant; it introduces no new egress. Details in [ASSISTANT.md](ASSISTANT.md).

### The dock

A bottom-center taskbar listing the windows on the active desktop. It only appears once at least one window exists, so an empty desktop keeps the clean Home backdrop.

- Click a window's icon → focus and raise it.
- Click the focused window's icon → minimize it (toggle).
- Click a minimized window's icon → restore and focus it.

A dot under each icon marks a running window; it takes your accent color when focused. Minimized windows show dimmed.

---

## Launching apps

Three equivalent front doors:

1. **Launchpad** — rocket icon in the menu bar. A fullscreen grid grouped by category, with a search field that focuses automatically ("Search applications..."). Esc closes it.
2. **Command palette** — press **Cmd-K / Ctrl-K**, type a few letters of the app name, Enter. The fastest path once you know it.
3. **Home quick launch** — the curated tiles on the Home surface.

What is installed, how the App Hub works, and how apps are sandboxed is covered in [APPS.md](APPS.md).

---

## Window management

Every window has a title bar with close / minimize / maximize controls. Beyond the basics:

### Snap by dragging

Drag a window's title bar toward a screen edge and a snap preview appears (a 48 px hot zone):

- **Left / right edge** → left / right half.
- **Top edge** → maximize.
- **Any corner** → that quarter of the screen.

Halves and quarters tile the usable area exactly, with no seams, below the 32 px menu bar. Dragging a tiled window frees it back to floating. Double-clicking the title bar toggles maximize / restore.

### Tile with the keyboard

`Super+Arrow` (the Cmd key on a Mac keyboard) tiles the active window, and repeated presses walk a state machine: half → quarter → the other quarter. `Ctrl+Alt+Arrow` does the same for keyboards without a usable Super key. `Down` from a maximized or tiled window restores its remembered floating geometry — the shell preserves where the window was before you first tiled it.

`Alt+`\` (backtick) cycles through windows; add Shift to cycle backwards. `Ctrl/Cmd+W` closes the active window. None of these fire while you are typing in a text field.

### Window sessions persist

The shell saves your desktops and windows to the browser (debounced, on every change) and restores them on reload: which desktop each window was on, its position, size, minimized state, and tile state. Two kinds of windows restore faithfully — web-app windows (rebuilt from their URL) and built-in apps (rebuilt from their app id). Streaming windows (the native-app browser, remote sessions) need a live backend session and are intentionally dropped on reload.

### Virtual desktops

- **Ctrl+1–9** switches desktops; **Ctrl+N** creates one.
- The menu-bar indicator's × closes the current desktop — its windows move to the next desktop rather than closing (macOS-style).
- In Mission Control you can also move windows between desktops.

### Mission Control

**F3** (or **Ctrl+Up**) lays every window on the current desktop out in a grid, with a strip of desktops along the top. Click a window to jump to it, hit the × on a thumbnail to minimize it, Esc to leave. Minimized windows are hidden here — that is what the dock is for.

---

## The command palette (Cmd-K)

**Cmd-K / Ctrl-K** opens the unified palette from anywhere. One input, four live sections:

| Section | Backed by | Enter does |
|---|---|---|
| Apps | The app registry (recents shown when the query is empty) | Launches the app |
| Mail | Live `GET /api/mail/search` (debounced) | Opens the message in Mail |
| Actions | The command registry — built-ins plus commands contributed by apps | Runs the action |
| Ask | The agentic assistant | Streams an answer inline |

Navigation: **↑/↓** move, **Tab** jumps to the next section (Shift+Tab back), **Enter** activates, **Esc** closes.

Question-shaped queries automatically get an "Ask the assistant" row; prefix with **`?`** to force it. Answers stream token-by-token into the palette. If the assistant wants to *change* something, it returns a proposal card instead — press **Y** to approve or **N** to reject; nothing runs until you approve. Mail search degrades gracefully: if Mail is unreachable, the palette says so instead of breaking.

Built-in actions include composing an email, creating a calendar event, jumping to a specific Settings section, opening the notification panel, toggling the theme, locking the screen, showing the keyboard-shortcut legend, opening the transparency panel, and revealing Home.

---

## Notifications

### The bell and the panel

The bell in the menu bar shows an unread badge (and a strike-through when Do Not Disturb is on). The panel groups notifications by day, then by source (Mail, Assistant, System, ...), with unread dots colored by severity. Per-item: mark read, dismiss. Panel-wide: **Mark all read** and **Clear**.

New notifications also appear briefly as **toasts**. Notifications come from two feeds folded into one store: client-side events, and the backend feed (`GET /api/notifications`, streamed over `/api/notifications/stream`).

### Do Not Disturb

The toggle at the bottom of the notification panel (also in **Settings → Notifications**). DND silences pop-ups; notifications still collect quietly in the bell. The box also honours DND before sending Web Push, so a muted box does not buzz your phone.

### Settings → Notifications

- **Do Not Disturb** and **Notification sounds** toggles.
- **This device — Push notifications**: an opt-in, per-device Web Push toggle. When on, your box notifies this browser even while the Vulos tab is closed. The payload is end-to-end encrypted (RFC 8291) — the push vendor routes it but cannot read it. The toggle is honest about why it can't be enabled: unsupported browser, box has no Web Push send-path configured, or permission blocked in the browser.
- **Sources**: turn a source (Mail, Assistant, System, ...) off entirely — it stops being collected at all, not just silenced.

Notification preferences live on this device (browser storage), matching the per-device nature of Web Push.

---

## The Settings app

Open **Settings** from the Launchpad, Home quick launch, or Cmd-K (palette actions can deep-link straight to a section). The panes, exactly as they appear:

| Pane | What it covers |
|---|---|
| AI Assistant | Assistant behaviour and personality. |
| AI Router | Where AI requests are routed (local / brokered / external tiers). |
| AI Models | Model management — **owner only**. |
| AI Apps | Per-app AI access. |
| Appearance | Theme (Dark / Light / Auto / Schedule), night light (off / sunset-to-sunrise / custom), accent color, density (Comfortable / Compact), wallpaper. |
| Notifications | DND, sounds, Web Push, per-source toggles (above). |
| WiFi | Scan and join networks. |
| Bluetooth | Device pairing. |
| Sound | Audio devices and volume. |
| Display | Resolution and display settings. |
| Battery & Energy | Power modes, screen dim/off behaviour. |
| Backup & Sync | Vault backup targets and sync. See [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md). |
| Search & Index | The on-box recall index. |
| Storage | Disks and S3-compatible storage (endpoint, bucket, region, keys). |
| Storage Mode | Where your data lives. |
| Connection Mode | Fabric / Direct / Own Domain / Local Only — how the box is reached. See [NETWORKING.md](NETWORKING.md). |
| Remote Access | Reaching your box from outside. See [NETWORKING.md](NETWORKING.md) and [PEERING.md](PEERING.md). |
| TURN / WebRTC | Relay settings for real-time media. |
| Users & Profiles | Lock-screen PIN, add users (admin), roles, remove users. |
| Device PIN | Set / change / disable the lock-screen PIN, with lockout status. |
| Fingerprint | Enroll a fingerprint if the box has a reader. |
| Account | Display name, language, timezone; **Log Out**. |
| Export My Data | Take your data out. |
| Plan & Billing | Subscription and usage. |
| OS Update | System updates. |
| Box Health | Diagnostics — **owner only**. |
| About | Version and system info. |

Owner-only panes are hidden from non-admin users in the UI, and the backend independently rejects non-owners on those endpoints. Backend-level configuration (environment variables, `--env` profiles) is in [CONFIGURATION.md](CONFIGURATION.md).

---

## Accounts, login, and locking

### Multi-user

Vulos OS is multi-user with three roles: **Admin**, **User**, **Guest**. The first account created during setup is the admin. In **Settings → Users & Profiles** an admin can:

- Add a user (display name, username, password — 4+ characters).
- Change any other user's role.
- Remove a user (irreversible).

Every user gets their own profile, session, and per-user master key. Your OS username and password double as your `sudo` credentials in the Terminal.

### Signing in

The login screen offers:

- **Local account** — username and password. On success the shell also unwraps your master key client-side and holds it in memory for the session (never persisted), so encrypted content works without re-prompting.
- **Sign in with Vulos Cloud** — shown only when the box is cloud-enrolled. Email and password against your Vulos account; if the account has 2FA you'll be prompted for the 6-digit TOTP code. One Vulos account signs you into the cloud *and* the OS. See [CLOUD.md](CLOUD.md).

If the box has no users at all (edge case outside the wizard), the same screen becomes a create-account form.

### Locking

- **Ctrl+L** locks the screen immediately.
- Energy management locks automatically: when the backend reports the screen dimmed you get the clock **screensaver**; when it reports the screen off, the shell locks. Any key or tap wakes it (and tells the box to wake via the energy API).

The lock screen asks for your **PIN** (the 4–8 digit one from setup or Settings). Wrong attempts shake, report attempts remaining, then back off — repeated failures temporarily lock retries, and persistent failure locks the device permanently until you do a full sign-in. If you never set a PIN, the lock screen unlocks with a plain Enter — set a PIN if the screen sits somewhere semi-public.

### Two-factor and biometrics

- **TOTP 2FA** protects **Vulos Cloud** accounts (set up in the post-signup wizard, with one-time recovery codes). Local OS accounts authenticate with password + optional PIN; they do not have their own TOTP challenge.
- **Fingerprint** — if your hardware has a reader, enroll in **Settings → Fingerprint** (start enrollment, scan, done).
- The **Authenticator** app, separately, is a TOTP vault for your *other* accounts' codes — don't confuse it with 2FA on the box itself.

### Logging out

**vula menu → Log Out**, or **Settings → Account → Log Out**. Logout ends your session; your windows and desktops are restored next time you sign in on the same browser.

---

## Keyboard shortcuts

Press **`?`** anywhere on the desktop (not while typing in a field) for the built-in shortcut legend. The complete set the shell binds:

| Keys | Action |
|---|---|
| `Cmd/Ctrl` + `K` | Open the command palette (apps · mail · actions · ask) |
| `Ctrl` + `1`–`9` | Switch to desktop 1–9 |
| `Ctrl` + `N` | New virtual desktop |
| `F3` or `Ctrl` + `↑` | Toggle Mission Control |
| `Super/Cmd` + `←` `→` `↑` `↓` | Tile the active window (half → quarter → ...; `↑` maximizes, `↓` restores) |
| `Ctrl` + `Alt` + `←` `→` `↑` `↓` | Same tiling, for keyboards without a usable Super key |
| `Alt` + `` ` `` | Cycle windows (add `Shift` to reverse) |
| `Ctrl/Cmd` + `W` | Close the active window |
| `Ctrl` + `L` | Lock the screen |
| `?` | Toggle the keyboard-shortcut legend |
| `Esc` | Close the current overlay (palette, Launchpad, Mission Control, legend) |

Inside the command palette: `↑`/`↓` navigate, `Tab` / `Shift+Tab` jump between sections, `Enter` activates, and `Y` / `N` approve or reject a pending assistant proposal.

Mouse equivalents: double-click a title bar to maximize/restore; drag a window to a screen edge or corner to snap it.

`F11` toggles browser fullscreen — that one belongs to your browser, not Vulos, but it matters because Vulos is at its best fullscreen. (On Firefox, set `browser.fullscreen.exit_on_escape` to `false` in `about:config` so Esc closes Vulos overlays instead of exiting fullscreen.)

Shell shortcuts never hijack keys while you are typing in an input, textarea, or rich editor.

---

## Where to next

- [APPS.md](APPS.md) — the built-in apps and the App Hub.
- [FILES.md](FILES.md) — the Files app and sharing.
- [ASSISTANT.md](ASSISTANT.md) — the assistant, proposals, and private AI.
- [NETWORKING.md](NETWORKING.md) — connection modes and remote access.
- [PEERING.md](PEERING.md) — box-to-box connections and calls.
- [CLOUD.md](CLOUD.md) — what Vulos Cloud enrollment adds.
- [SECURITY.md](SECURITY.md) — the trust model behind the trust badge.
- [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) — the recovery phrase, kit, and backups.
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — when something misbehaves.
