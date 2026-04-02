# Default Web Apps

Built-in web apps that ship with Vula OS. These are lightweight, built with HTML/CSS/JS, run inside the desktop shell — no apt packages, no Flatpak, no streaming. They open instantly like native apps because they ARE part of the OS.

Each app lives in `/apps/<id>/` with an `app.json` manifest + a small Python/Go server or static files.

---

## Existing Apps

Already shipped:
- [x] **Notes** (`/apps/notes/`) — knowledge base with indexing
- [x] **Gallery** (`/apps/gallery/`) — photos/videos organised by Recall
- [x] **Smart Browser** (`/apps/browser/`) — ad-free web with AI summaries

---

## Priority 1 — Office Suite (most important)

These are the apps people use every day. Without them, the OS is a toy.

### Docs (Word Processor)
- [ ] Rich text editing: headings, bold, italic, underline, strikethrough, lists, tables, images
- [ ] Toolbar with formatting controls
- [ ] Export to `.docx`, `.pdf`, `.odt`, `.md`
- [ ] Import from `.docx`, `.odt`, `.txt`, `.md`
- [ ] Print via browser print dialog
- [ ] Auto-save to `~/.vulos/docs/`
- [ ] Multiple document tabs
- [ ] Spell check (browser-native or Hunspell)
- [ ] Page layout: margins, headers, footers, page numbers
- [ ] Template gallery (letter, report, resume, blank)
- [ ] Collaborative editing (future — CRDT/OT via WebSocket)
- Tech: [TipTap](https://tiptap.dev/) or [Lexical](https://lexical.dev/) editor, lightweight and extensible

### Sheets (Spreadsheet)
- [ ] Grid with columns A-Z+, rows 1-10000+
- [ ] Cell formatting: number, currency, date, percentage, text
- [ ] Formulas: `=SUM()`, `=AVERAGE()`, `=IF()`, `=VLOOKUP()`, basic set covering 90% of use
- [ ] Cell references, ranges, cross-sheet references
- [ ] Multiple sheets (tabs at bottom)
- [ ] Sort, filter, freeze rows/columns
- [ ] Charts: bar, line, pie (basic)
- [ ] Export to `.xlsx`, `.csv`, `.ods`
- [ ] Import from `.xlsx`, `.csv`, `.ods`
- [ ] Auto-save to `~/.vulos/sheets/`
- Tech: [FortuneSheet](https://github.com/ruilisi/fortune-sheet) or [Luckysheet](https://github.com/mengshukeji/Luckysheet), or custom canvas grid

### Slides (Presentation)
- [ ] Slide deck with thumbnails sidebar
- [ ] Text boxes, images, shapes
- [ ] Slide transitions (fade, slide, none)
- [ ] Presenter mode (notes + next slide preview)
- [ ] Export to `.pptx`, `.pdf`
- [ ] Import from `.pptx`
- [ ] Templates (title slide, content, two-column, blank)
- [ ] Auto-save to `~/.vulos/slides/`
- Tech: [Reveal.js](https://revealjs.com/) for presentation engine, custom editor UI

---

## Priority 2 — Essential Utilities

Apps every OS needs out of the box.

### Calculator
- [ ] Standard mode: basic arithmetic (+, -, ×, ÷, %, √)
- [ ] Scientific mode: sin, cos, tan, log, ln, powers, factorial, constants (π, e)
- [ ] History tape (scrollable list of previous calculations)
- [ ] Keyboard input (number keys, Enter to calculate)
- [ ] Copy result to clipboard
- [ ] Clean, minimal UI — single window, no clutter

### Calendar
- [ ] Month, week, day views
- [ ] Create/edit/delete events (title, time, description, colour)
- [ ] Recurring events (daily, weekly, monthly, yearly)
- [ ] Event reminders (browser notification)
- [ ] Import/export `.ics` (iCalendar standard)
- [ ] CalDAV sync (Google Calendar, Nextcloud) — future
- [ ] Auto-save to `~/.vulos/calendar/`

### Weather
- [ ] Current conditions + 7-day forecast
- [ ] Location detection (IP-based) or manual city search
- [ ] Temperature, humidity, wind, UV index
- [ ] Hourly breakdown for today
- [ ] Clean visual: weather icon, temperature prominent
- [ ] Uses free API (Open-Meteo — no API key needed)

### Clock / World Clock
- [ ] Current time, large display
- [ ] Multiple time zones side by side
- [ ] Stopwatch
- [ ] Timer with alarm sound
- [ ] Alarm (browser notification + sound)

---

## Priority 3 — Media & Communication

### Music Player
- [ ] Play audio files from `~/.vulos/music/` or any path
- [ ] Formats: mp3, flac, ogg, wav, m4a (browser-native via `<audio>`)
- [ ] Playlist management (create, save, reorder)
- [ ] Album art display (embedded ID3 tags)
- [ ] Controls: play, pause, skip, previous, shuffle, repeat, volume, seek
- [ ] Mini player mode (small floating bar)
- [ ] Library view: by artist, album, or all songs
- [ ] Background playback (keeps playing when window minimised)
- [ ] Keyboard shortcuts (space = play/pause, arrows = seek)

### Video Player
- [ ] Play video files from filesystem
- [ ] Formats: mp4, webm, mkv, avi (browser-native + ffmpeg.wasm for exotic formats)
- [ ] Controls: play, pause, seek, volume, fullscreen, playback speed
- [ ] Subtitle support: `.srt`, `.vtt` — drag and drop to load
- [ ] Picture-in-picture mode
- [ ] Playlist / queue
- [ ] Keyboard shortcuts (space, arrows, F for fullscreen)

### Image Editor
- [ ] Open images from filesystem (jpg, png, webp, gif, svg)
- [ ] Crop, rotate, flip, resize
- [ ] Brightness, contrast, saturation, exposure sliders
- [ ] Filters (grayscale, sepia, blur, sharpen, invert)
- [ ] Draw / annotate (brush, text, shapes, arrows)
- [ ] Undo/redo (Ctrl+Z/Y)
- [ ] Export to jpg, png, webp
- [ ] Save to `~/.vulos/pictures/`
- Tech: Canvas API + basic WebGL filters, or [Pintura](https://pqina.nl/pintura/)-style UI

### Text Editor
- [ ] Plain text and code editing
- [ ] Syntax highlighting (JS, Python, Go, HTML, CSS, JSON, Markdown, Bash, and more)
- [ ] Line numbers, word wrap toggle
- [ ] Find and replace (Ctrl+F, Ctrl+H)
- [ ] Multiple tabs (open several files)
- [ ] File tree sidebar (browse `~/` or any directory)
- [ ] Auto-indent, bracket matching
- [ ] Dark and light themes
- [ ] Font size adjustment
- [ ] Save to filesystem via backend API
- Tech: [CodeMirror 6](https://codemirror.net/) or [Monaco](https://microsoft.github.io/monaco-editor/) (VS Code's editor)

---

## Priority 4 — Communication

### Email Client
- [ ] IMAP + SMTP support (connect to Gmail, Outlook, Fastmail, any provider)
- [ ] Inbox, sent, drafts, trash, spam folders
- [ ] Compose with rich text (bold, italic, links, attachments)
- [ ] Multiple accounts
- [ ] Search across all mail
- [ ] HTML email rendering (sanitised)
- [ ] Attachment preview (images, PDFs)
- [ ] Auto-save drafts
- [ ] Notification badge on dock icon
- [ ] Credentials stored encrypted in `~/.vulos/mail/`
- Tech: Go backend handles IMAP/SMTP, frontend renders mail. Or integrate with Thunderbird (already in registry as apt package)

### Contacts
- [ ] Contact list with name, email, phone, address, notes, photo
- [ ] Groups / labels
- [ ] Search and filter
- [ ] Import/export vCard (`.vcf`)
- [ ] CardDAV sync (Google Contacts, Nextcloud) — future
- [ ] Links to Email and Calendar (click email → compose, click birthday → calendar event)
- [ ] Auto-save to `~/.vulos/contacts/`

### Chat / Messaging
- [ ] Matrix protocol client (decentralised, open standard)
- [ ] Direct messages + group rooms
- [ ] End-to-end encryption (Matrix E2EE)
- [ ] File sharing, image preview
- [ ] Emoji picker
- [ ] Or: simple IRC/XMPP client as lighter alternative
- Tech: [matrix-js-sdk](https://github.com/matrix-org/matrix-js-sdk) or [hydrogen-web](https://github.com/element-hq/hydrogen-web)

---

## Priority 5 — Productivity

### PDF Viewer
- [ ] Render PDFs in-browser
- [ ] Page navigation, zoom, fit-to-width
- [ ] Search text within PDF
- [ ] Thumbnail sidebar
- [ ] Print
- [ ] Annotation: highlight, underline, text notes (save to sidecar file)
- Tech: [PDF.js](https://mozilla.github.io/pdf.js/) (Mozilla, battle-tested)

### Maps
- [ ] OpenStreetMap tiles
- [ ] Search for places / addresses
- [ ] Directions (walking, driving, cycling) via OSRM or similar
- [ ] Current location (browser Geolocation API)
- [ ] Save favourite places
- Tech: [Leaflet](https://leafletjs.com/) + OSM tiles (free, no API key)

### Voice Recorder
- [ ] Record audio from microphone (MediaRecorder API)
- [ ] Playback recorded clips
- [ ] Save as `.webm`, `.ogg`, or `.wav`
- [ ] List of recordings with timestamps
- [ ] Trim start/end before saving
- [ ] Waveform visualisation during recording

### Camera
- [ ] Access device camera (getUserMedia API)
- [ ] Take photos, save to `~/.vulos/pictures/`
- [ ] Record video, save to `~/.vulos/videos/`
- [ ] Flip front/back camera (mobile/laptop)
- [ ] Basic filters (optional)

---

## Priority 6 — System & Utilities

### System Info / About
- [ ] OS version, kernel, architecture
- [ ] CPU, RAM, storage info
- [ ] GPU info (from `gpu.Detect()`)
- [ ] Network interfaces and IP addresses
- [ ] Uptime
- [ ] Already partially exists in Settings — extract to standalone app

### Backups
- [ ] Select directories to back up
- [ ] Compress to `.tar.gz` and save to external drive or network path
- [ ] Schedule recurring backups (daily/weekly)
- [ ] Restore from backup
- [ ] Incremental backups (rsync-based)

### Screenshot / Screen Capture
- [ ] Screenshot: full screen, window, or region selection
- [ ] Screen recording: capture to `.webm`
- [ ] Annotation after capture (arrows, text, blur)
- [ ] Auto-save to `~/.vulos/screenshots/`
- [ ] Copy to clipboard
- [ ] Keyboard shortcut: `PrtSc` or `Cmd+Shift+3/4` equivalent

---

## App Structure

Each app follows the same pattern as existing apps:

```
/apps/<id>/
  ├── app.json          (manifest: name, icon, port, category, deps)
  ├── server.py         (lightweight Python HTTP server, or Go, or static)
  ├── index.html        (main UI)
  ├── style.css
  ├── app.js
  └── icon.svg          (app icon for launchpad + dock)
```

```json
{
  "id": "calculator",
  "name": "Calculator",
  "icon": "🔢",
  "icon_path": "icon.svg",
  "description": "Standard and scientific calculator",
  "version": "0.1.0",
  "command": "python3 server.py",
  "port": 80,
  "category": "utilities",
  "keywords": ["calculator", "math", "compute"],
  "deps": ["python3"],
  "auto_start": false,
  "singleton": true,
  "permissions": [],
  "author": "Vula OS",
  "license": "MIT"
}
```

Apps that are pure frontend (calculator, clock, PDF viewer) can skip the server and use static files served by vulos-server directly.

---

## AI Integration

Every default app can integrate with the Vula OS AI assistant:

- **Docs**: "Summarise this document", "Rewrite this paragraph", "Translate to French"
- **Sheets**: "Create a formula for...", "Generate sample data", "Explain this formula"
- **Slides**: "Generate slides from this outline", "Suggest a layout"
- **Email**: "Draft a reply", "Summarise this thread"
- **Calendar**: "Schedule a meeting for...", "What's my next free slot?"
- **Calculator**: Already handled by AI (math intents route to calculation)
- **Text Editor**: "Explain this code", "Refactor this function", "Add comments"

This happens via `<os-action>` blocks and the AI chat panel — apps expose actions, AI can invoke them.

---

## Implementation Order

1. **Calculator** — simplest app, good template for all others
2. **Text Editor** — CodeMirror, immediately useful for developers
3. **Docs** — TipTap/Lexical, the most important app for general users
4. **Sheets** — FortuneSheet/custom grid, second most important office app
5. **Calendar** — essential for daily use
6. **Music Player** — audio playback, `<audio>` element
7. **Video Player** — video playback, `<video>` element
8. **Image Editor** — Canvas API, crop/rotate/filters
9. **PDF Viewer** — PDF.js, quick win
10. **Weather** — Open-Meteo API, single-page app
11. **Clock** — timer, stopwatch, world clock
12. **Slides** — Reveal.js editor
13. **Email** — IMAP/SMTP backend, most complex
14. **Contacts** — vCard, links to Email + Calendar
15. **Maps** — Leaflet + OSM
16. **Screenshot** — capture + annotate
17. **Voice Recorder** — MediaRecorder API
18. **Camera** — getUserMedia
19. **Chat** — Matrix client, most ambitious
20. **Backups** — rsync-based, system utility
