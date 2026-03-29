# Vula OS — Roadmap

## Upcoming

### Browser
- [ ] Upgrade to Chromium-based rendering engine
- [ ] Improve browser compatibility and web standards support
- [ ] Tab management and session restore
- [ ] Extension support

### Terminal
- [ ] Add full bash terminal with shell history
- [ ] Improved PTY handling and resize support
- [ ] Custom themes and font configuration

### Default Applications
- [ ] Calculator
- [ ] Calendar
- [ ] Music player
- [ ] Video player
- [ ] Image editor
- [ ] Text editor with syntax highlighting
- [ ] Email client
- [ ] Contacts

### Theming & Display
- [ ] Night Shift — auto-adjust colour temperature during evening/night hours (warmer tones at sunset, cool at sunrise)
- [ ] Wallpaper customization — user-uploaded backgrounds, dynamic wallpapers
- [ ] Accent colour picker — let users choose system accent colour beyond blue

### Improve Existing Apps
- [ ] File Manager — drag-and-drop, bulk operations, preview pane
- [ ] Notes — rich text editing, markdown support, tagging
- [ ] Gallery — albums, slideshow, basic editing
- [ ] Activity Monitor — graphs, process management, resource alerts
- [ ] Settings — more configuration options

### Security
- [ ] Full security audit of all backend services
- [ ] Verify sandbox isolation is safe for untrusted code
- [ ] Review auth middleware for edge cases and bypasses
- [ ] Dependency vulnerability scanning in CI
- [ ] Container image scanning for CVEs
- [ ] Rate limiting and abuse prevention review

### Platform
- [ ] Improved mobile responsiveness
- [ ] Accessibility improvements
- [ ] Internationalization (i18n)
- [ ] Plugin/extension API for third-party developers

---

## Completed

### Auth Enforcement
- [x] Middleware enforces auth — returns 401 on all /api/ and /app/ routes without valid session
- [x] Public endpoint whitelist: /health, /api/auth/providers, /login/\*, /callback/\*
- [x] Frontend assets served without auth (React handles its own gate)

### Sandbox Security
- [x] Dangerous code validation (blocks subprocess, os.system, eval, exec, fork bombs)
- [x] 100KB code size limit
- [x] 5-minute execution timeout per sandbox script
- [x] Sandbox proxy protected by auth middleware

### Dev Mode Bypass
- [x] "Continue without login" only shows in Vite dev mode
- [x] Production builds never show the bypass

### AI-Generated Apps
- [x] Save button in AI viewport window title bar
- [x] CRUD API for persisted AI apps (~/.vulos/ai-apps/)
- [x] List, retrieve, and delete saved AI apps

### Browser Profiles
- [x] Firefox-style profile isolation (Personal, Work, Private)
- [x] Bind apps to profiles
- [x] Clear data per profile without deleting it
- [x] REST API: CRUD + bind + clear

### AI OS Control
- [x] AI can include `<os-action>` blocks to control the OS
- [x] Supported actions: open-app, close-app, notify, energy-mode, exec
- [x] System prompt teaches AI about OS control capabilities

### Persistence
- [x] Chat history restored from backend on Portal mount
- [x] Window/desktop state persisted to localStorage
- [x] AppRegistry cleaned — removed unimplemented stubs

### Polish
- [x] Vault/Backup settings UI
- [x] Recall/Search settings UI
- [x] AI Apps gallery in Settings
- [x] Ad blocker — 50+ domains, EasyList-format blocklist, class/id matching
