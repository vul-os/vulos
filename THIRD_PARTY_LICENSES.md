# Third-Party Licenses

Vulos includes and depends on the following third-party software. Each
component is listed with its license. Full license texts are provided below.

---

## Runtime Dependencies (Docker Image)

### Debian Linux

- **Source**: https://www.debian.org/
- **License**: Various (base system packages are DFSG-free)
- **Usage**: Base container image and target platform

### GStreamer

- **Source**: https://gstreamer.freedesktop.org/
- **License**: LGPL-2.1-or-later
- **Usage**: Media streaming pipeline (gstreamer-tools, gst-plugins-base, gst-plugins-good, gst-plugins-bad)

### PulseAudio

- **Source**: https://www.freedesktop.org/wiki/Software/PulseAudio/
- **License**: LGPL-2.1-or-later
- **Usage**: Audio server in the container

### xdotool

- **Source**: https://github.com/jordansissel/xdotool
- **License**: BSD 3-Clause
- **Usage**: X11 window automation

### Python 3

- **Source**: https://www.python.org/
- **License**: PSF License (BSD-style)
- **Usage**: App sandbox runtime

### Noto Fonts

- **Source**: https://fonts.google.com/noto
- **License**: OFL-1.1 (SIL Open Font License)
- **Usage**: System font in the container

---

## Frontend Dependencies (npm)

### React

- **Source**: https://react.dev/
- **License**: MIT
- **Copyright**: Meta Platforms, Inc. and affiliates
- **Usage**: UI framework

### React DOM

- **Source**: https://react.dev/
- **License**: MIT
- **Copyright**: Meta Platforms, Inc. and affiliates
- **Usage**: React DOM renderer

### Tailwind CSS

- **Source**: https://tailwindcss.com/
- **License**: MIT
- **Copyright**: Tailwind Labs, Inc.
- **Usage**: CSS utility framework

### Vite

- **Source**: https://vite.dev/
- **License**: MIT
- **Copyright**: Yuxi (Evan) You and Vite contributors
- **Usage**: Build tool and dev server

### xterm.js

- **Source**: https://xtermjs.org/
- **License**: MIT
- **Copyright**: The xterm.js authors
- **Usage**: Terminal emulator in the browser

### @xterm/addon-fit

- **Source**: https://github.com/xtermjs/xterm.js
- **License**: MIT
- **Copyright**: The xterm.js authors
- **Usage**: Terminal auto-fit addon

### @xterm/addon-web-links

- **Source**: https://github.com/xtermjs/xterm.js
- **License**: MIT
- **Copyright**: The xterm.js authors
- **Usage**: Terminal clickable links addon

---

## Backend Dependencies (Go)

### creack/pty

- **Source**: https://github.com/creack/pty
- **License**: MIT
- **Usage**: Pseudo-terminal handling

### golang.org/x/net

- **Source**: https://pkg.go.dev/golang.org/x/net
- **License**: BSD 3-Clause
- **Copyright**: The Go Authors
- **Usage**: Networking utilities

### golang.org/x/crypto

- **Source**: https://pkg.go.dev/golang.org/x/crypto
- **License**: BSD 3-Clause
- **Copyright**: The Go Authors
- **Usage**: Cryptographic functions

### golang.org/x/sys

- **Source**: https://pkg.go.dev/golang.org/x/sys
- **License**: BSD 3-Clause
- **Copyright**: The Go Authors
- **Usage**: System call interface

### golang.org/x/time

- **Source**: https://pkg.go.dev/golang.org/x/time
- **License**: BSD 3-Clause
- **Copyright**: The Go Authors
- **Usage**: Rate limiting

### google/uuid

- **Source**: https://github.com/google/uuid
- **License**: BSD 3-Clause
- **Copyright**: Google Inc.
- **Usage**: UUID generation

### Pion WebRTC

- **Source**: https://github.com/pion/webrtc
- **License**: MIT
- **Copyright**: Pion contributors
- **Packages**: webrtc, ice, dtls, stun, turn, sctp, sdp, srtp, rtp, rtcp, datachannel, interceptor, transport, mdns, randutil, logging
- **Usage**: WebRTC, TURN relay, and real-time communication

---

## Build-Time Dependencies

### Go

- **Source**: https://go.dev/
- **License**: BSD 3-Clause
- **Usage**: Backend compilation

### Node.js

- **Source**: https://nodejs.org/
- **License**: MIT
- **Usage**: Frontend build toolchain

---

## License Summary

| License | Packages |
|---------|----------|
| MIT | React, React DOM, Tailwind CSS, Vite, xterm.js, Pion, creack/pty, Node.js |
| BSD 3-Clause | Go, golang.org/x/*, google/uuid, xdotool |
| LGPL-2.1+ | GStreamer, PulseAudio |
| PSF | Python 3 |
| OFL-1.1 | Noto Fonts |
| DFSG-free | Debian Linux (base system) |

> **Note**: LGPL dependencies (GStreamer, PulseAudio) are dynamically linked system
> packages installed via Debian's package manager. They are not statically linked into
> or distributed as part of Vulos' own source code.
