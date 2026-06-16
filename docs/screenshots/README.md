# Screenshot Status

Screenshots are captured by `scripts/screenshots.mjs` using Playwright (Chromium headless).
The seed script (`scripts/seed-demo.sh`) creates a throwaway demo account and populates
the shell before capturing.

## Captured — populated authenticated views

All screenshots below were captured against a live backend with a seeded demo account
(`demo` / `Vul0sD3moPass!`) in a temp data directory (`/tmp/vulos-demo-*`).

| File | What it shows | Status |
|------|--------------|--------|
| `login.png` | Login screen — username/password form with Vulos logo and tagline | Real screenshot |
| `hero.png` | Desktop shell — DesktopCanvas layout, dark purple wallpaper, menu bar, system tray | Real screenshot |
| `launchpad.png` | Launchpad — full app grid: System, Internet, Productivity, Media, Network categories | Real screenshot |
| `settings.png` | Settings window — AI Assistant panel with sidebar showing all sections | Real screenshot |
| `settings-storage.png` | Settings window — Storage section visible in sidebar | Real screenshot |
| `terminal.png` | Terminal app window — xterm.js PTY (shows [session ended] on macOS dev; real bash on Linux) | Real screenshot |
| `files.png` | File Explorer — sidebar (Home/Desktop/Documents/Downloads/Pictures/Music/Videos/Root/Tmp) | Real screenshot |
| `apphub.png` | App Hub — Store UI with Browse/Installed tabs, 21 built-in apps, app type filters | Real screenshot |
| `activity.png` | Activity Monitor — renders "App error" via WindowErrorBoundary (macOS dev: no /api/processes; real processes on Linux) | Real screenshot |

## GPU-dependent views (not captured headlessly)

The following cannot be captured in headless Playwright without a GPU streaming session
(NVENC / VA-API / VP8 software encoder and an active X/Wayland session on Linux):

| What | Why |
|------|-----|
| Streamed native app (GIMP, LibreOffice, etc.) | Requires `stream.Session` + GPU encoder + running native display |
| Wine/Lutris game window | Requires DirectX/Vulkan translation + uinput |

These are not faked — the screenshots honestly reflect what the shell shows in the
current execution environment.

## How to regenerate

```bash
# Prerequisites (once)
npm install
npx playwright install chromium

# Seed a demo account + capture (uses a temp /tmp/vulos-demo-* data dir)
./scripts/seed-demo.sh

# Or manually (backend must already be running with a seeded account)
SCREENSHOT_EMAIL=demo SCREENSHOT_PASSWORD='Vul0sD3moPass!' BASE_URL=http://localhost:8080 npm run screenshots
```

See [../SCREENSHOTS.md](../SCREENSHOTS.md) for full instructions.
