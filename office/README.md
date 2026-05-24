<div align="center">

<img src="public/vula-office.png" alt="Vulos Office Logo" width="120" />

# Vulos Office

**Documents · Sheets · Slides · PDF — in one place**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![React](https://img.shields.io/badge/React-18-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![Vite](https://img.shields.io/badge/Vite-6-646CFF?logo=vite&logoColor=white)](https://vitejs.dev)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind-3-38BDF8?logo=tailwindcss&logoColor=white)](https://tailwindcss.com)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/vul-os/vulos-office/pulls)

*Vulos — rooted in **vula**, the Zulu and Xhosa word for **open**.*

</div>

---

## What is Vulos Office?

Vulos Office is a self-hosted, open-source office suite that runs as a **single binary**. It brings document editing, spreadsheets, presentations, and PDF annotation together in a clean, modern interface — no cloud account required, no telemetry, no lock-in.

It stands as a tribute to the spirit of **LibreOffice** and **OpenOffice** — the pioneers who proved that powerful productivity software could be free, open, and community-driven. Vulos carries that torch into the browser, with a lightweight Go backend and a fast React frontend, deployable anywhere in seconds.

> *"Vula" — open the door. Vulos Office is that door.*

---

## Features

| | |
|---|---|
| **Documents** | Rich text editing via TipTap — headings, tables, lists, task lists, links, images |
| **Spreadsheets** | Full-featured grid via Fortune Sheet — formulas, formatting, multi-sheet |
| **Presentations** | Slide editor powered by Reveal.js — create and present from the browser |
| **PDF** | View, annotate, and digitally sign PDF files |
| **Export** | Export to `.docx`, `.xlsx`, `.pptx`, `.pdf`, and Markdown |
| **Import** | Import from URL or local filesystem |
| **Auth** | Optional password-based auth with JWT — off by default for local use |
| **Storage** | Local JSON files by default; PostgreSQL for multi-user setups |
| **Single binary** | Go embeds the entire frontend — one file to deploy |
| **PWA-ready** | Installable as a desktop/mobile app via web manifest |

---

## Getting Started

### Prerequisites

- [Go 1.21+](https://golang.org/dl/)
- [Node.js 18+](https://nodejs.org/) and npm

### Development

```bash
# Clone the repo
git clone https://github.com/vul-os/vulos-office.git
cd vulos-office

# Install dependencies
npm install
go mod tidy

# Start dev server (Vite on :5173 + Go on :8080)
npm run dev
```

Open [http://localhost:5173](http://localhost:5173) in your browser.

### Production Build

```bash
# Build frontend + Go binary in one step
npm run build

# Run the single binary
./vulos-office
```

Open [http://localhost:8080](http://localhost:8080). That's it — the entire app is embedded in the binary.

---

## Configuration

Edit `config.yaml` before starting:

```yaml
server:
  port: ":8080"

auth:
  enabled: false          # Set to true to require a password
  password: "changeme"
  session_timeout: 24h
  max_failed_attempts: 5
  lockout_duration: 15m

storage:
  type: local             # "local" (JSON files) or "postgres"
  local_path: ./data

# Uncomment for PostgreSQL:
# database:
#   host: localhost
#   port: 5432
#   name: vulos
#   user: vulos
#   password: secret
```

---

## Project Structure

```
vulos-office/
├── main.go               # Entry point — embeds dist/, runs Gin server
├── config.yaml           # App configuration
├── backend/
│   ├── config/           # Config loading
│   ├── handlers/         # HTTP handlers (auth, files, uploads)
│   ├── middleware/        # JWT auth middleware
│   ├── models/           # Shared data models
│   └── storage/          # Storage interface (local / PostgreSQL)
├── src/
│   ├── App.jsx            # Router (Docs, Sheets, Slides, PDF)
│   ├── apps/             # Feature editors
│   │   ├── docs/         # TipTap document editor
│   │   ├── sheets/       # Fortune Sheet spreadsheet
│   │   ├── slides/       # Reveal.js presentation editor
│   │   └── pdf/          # PDF viewer and annotator
│   ├── components/       # Layout, Home, Auth, Modals
│   ├── store/            # Zustand state (auth, files)
│   └── lib/              # API client, file utilities
├── public/               # Static assets, favicons, PWA manifest
└── dist/                 # Built frontend (embedded in Go binary)
```

---

## API

| Method | Route | Description |
|--------|-------|-------------|
| `POST` | `/api/auth/login` | Authenticate |
| `POST` | `/api/auth/logout` | Logout |
| `GET` | `/api/auth/status` | Check session |
| `GET` | `/api/files` | List all files |
| `POST` | `/api/files` | Create a file |
| `GET` | `/api/files/:id` | Get file by ID |
| `PUT` | `/api/files/:id` | Update file |
| `DELETE` | `/api/files/:id` | Delete file |
| `POST` | `/api/upload` | Upload a file |

---

## A Nod to the Giants

Vulos Office would not exist without the open-source ecosystem that came before it.

**LibreOffice** and **OpenOffice** spent decades proving that free, open productivity software was not only possible but excellent. Their work changed how the world thinks about office software and made libre computing a reality for millions of people.

Vulos carries forward that same conviction — that tools people rely on every day should be open, auditable, self-hostable, and free. *Vula.* Open.

---

## Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you'd like to change.

1. Fork the repo
2. Create your branch (`git checkout -b feat/my-feature`)
3. Commit your changes (`git commit -m 'add my feature'`)
4. Push to the branch (`git push origin feat/my-feature`)
5. Open a Pull Request

---

## License

[MIT](LICENSE) — free to use, modify, and distribute.

---

<div align="center">

Made with care · Powered by open source · *Vula — open*

</div>
