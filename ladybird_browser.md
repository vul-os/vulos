# Ladybird Browser Engine

Replace Chromium with Ladybird's LibWeb as the remote browser engine. Chromium idles at 300-500MB and every frame copies three times (compositor → X11 shared memory → GStreamer capture). Ladybird's headless WebContent renderer outputs directly to an offscreen framebuffer, enabling a zero-copy path into GStreamer's `appsrc` — encoding only when the page changes instead of polling a display.

| | Chromium + Xvfb | Ladybird headless |
|---|---|---|
| Idle RAM | 300-500MB | ~50-80MB |
| Frame path | 3 copies (Chromium → X11 → GStreamer) | 1 copy, zero-copy possible |
| Startup | 2-5s | <1s |
| X11 dependency | Required | None |

## Why Ladybird can be faster than Chromium for this use case

Chromium is optimised for running locally on a user's machine with a real GPU and display — its multi-process compositor, GPU acceleration pipeline, and sandbox architecture are overhead when we're just capturing pixels off a virtual display. Ladybird has none of that baggage. Specifically:

- **No compositor overhead** — Chromium composites layers through its GPU process even on Xvfb where there's no real GPU. Ladybird's LibWeb renders the final bitmap directly in the WebContent process — one step, no inter-process pixel shuffling.
- **Event-driven frame delivery** — Chromium renders at a fixed refresh rate regardless of page activity. Ladybird's headless mode can signal "frame ready" only when the bitmap actually changes. Combined with the `FramebufferBackend`, this means zero wasted encodes on static pages — significant CPU and bandwidth savings, especially over a tunnel.
- **Smaller attack surface, fewer processes** — Chromium spawns GPU, renderer, utility, and network processes even headless. Ladybird's headless mode is a single WebContent process plus a RequestServer. Fewer processes = less context switching, less memory, faster startup.
- **No X11 round-trips** — Every mouse click and scroll in the Chromium pipeline goes: WebRTC data channel → Go server → socat → X11 → Chromium's input handling. With Ladybird, input goes directly into LibWeb's event system — cutting out X11 entirely. This shaves 2-5ms off every input event, which compounds into noticeably snappier scrolling and interaction.
- **Docker image size** — Removing Chromium (~400MB), Xvfb, and X11 libraries from the image is a major reduction. Ladybird's headless binary is a fraction of that.

The net effect: for a tunneled web OS where every millisecond and megabyte matters, Ladybird's architecture is closer to purpose-built for this than Chromium will ever be.

## TODO

- [ ] Track Ladybird headless mode API — currently used for WPT tests, needs stable framebuffer access
- [ ] Prototype: render a page via `FramebufferBackend`, pipe into the shared stream layer
- [ ] Implement event-driven frame signalling — only encode when LibWeb marks the bitmap as dirty
- [ ] Implement Ladybird input injection — pipe events directly into LibWeb's event system, bypassing X11
- [ ] Port ad blocker and profile isolation to LibWeb's content filtering
- [ ] Run both engines side-by-side during transition (user toggle in Settings)
- [ ] Remove Xvfb/Chromium/X11 deps from Docker image once Ladybird is viable

**Blocked on:** Ladybird alpha (targeting late 2026). Web compat limits real-world use until 2027+.
