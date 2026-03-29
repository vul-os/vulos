# Third-Party Licenses

Vula OS includes and depends on the following third-party software. Each
component is listed with its license. Full license texts are provided below.

---

## Runtime Dependencies (Docker Image)

### Chromium

- **Source**: https://www.chromium.org/
- **License**: BSD 3-Clause
- **Usage**: Remote browser rendering engine in the container

```
Copyright 2015 The Chromium Authors

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### Alpine Linux

- **Source**: https://alpinelinux.org/
- **License**: GPL-2.0 (base system packages vary)
- **Usage**: Base container image and target platform

### GStreamer

- **Source**: https://gstreamer.freedesktop.org/
- **License**: LGPL-2.1-or-later
- **Usage**: Media streaming pipeline (gstreamer-tools, gst-plugins-base, gst-plugins-good, gst-plugins-bad)

### PulseAudio

- **Source**: https://www.freedesktop.org/wiki/Software/PulseAudio/
- **License**: LGPL-2.1-or-later
- **Usage**: Audio server in the container

### Xvfb (X Virtual Framebuffer)

- **Source**: https://www.x.org/
- **License**: MIT/X11
- **Usage**: Headless display server for Chromium

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
| MIT | React, React DOM, Tailwind CSS, Vite, xterm.js, Pion, creack/pty, Node.js, Xvfb |
| BSD 3-Clause | Chromium, Go, golang.org/x/*, google/uuid, xdotool |
| LGPL-2.1+ | GStreamer, PulseAudio |
| PSF | Python 3 |
| OFL-1.1 | Noto Fonts |
| GPL-2.0 | Alpine Linux (base system) |

> **Note**: LGPL dependencies (GStreamer, PulseAudio) are dynamically linked system
> packages installed via Alpine's package manager. They are not statically linked into
> or distributed as part of Vula OS's own source code.
