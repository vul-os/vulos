# Screenshot Status

Screenshots are captured by `scripts/screenshots.mjs` using Playwright (Chromium headless).

## Captured in this environment

| File | What it shows | Status |
|------|--------------|--------|
| `login.png` | Login screen (unauthenticated) | Real screenshot |

## Require a live authenticated instance

The following screenshots require an existing Vulos account to log into. In the current environment no pre-seeded account exists, so these all show the login screen instead.

To regenerate with a real authenticated session:

1. Complete first-boot setup at `https://localhost:8080` to create an account.
2. Set the credentials in env vars:
   ```bash
   SCREENSHOT_EMAIL=your@email SCREENSHOT_PASSWORD=yourpassword npm run screenshots
   ```

| File | What it shows |
|------|--------------|
| `hero.png` | Desktop shell — window manager, dock |
| `launchpad.png` | Launchpad full-screen app grid |
| `settings.png` | Settings panel |
| `terminal.png` | Terminal app open in a window |
| `files.png` | File Manager |
| `apphub.png` | App Hub (app store) |
| `activity.png` | Activity Monitor |

## Regeneration command

```bash
# Prerequisites
npm install
npx playwright install chromium

# Boot the backend
cd backend && go run ./cmd/server --env=local &

# Capture (set credentials if you have an account)
SCREENSHOT_EMAIL=admin@localhost SCREENSHOT_PASSWORD=yourpassword npm run screenshots
```

See [../SCREENSHOTS.md](../SCREENSHOTS.md) for full instructions.
