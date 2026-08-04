# Static Site Template

The canonical "hello world" for hosting a static single-page app on a Vulos box.
It is a single `index.html` plus two files under `static/` — no build step, no
CDN, no external network. Use it as a starting point for your own site and as a
fixture for exercising the box's static-hosting path.

## Files

```
site-template/
├── app.json          # metadata (type "web")
├── index.html        # the SPA shell + inline client-side router
├── static/
│   ├── app.js        # same-origin script asset
│   └── style.css     # same-origin stylesheet asset
└── README.md
```

## Routes

The client-side router (inline in `index.html`) exposes:

| Route             | Renders                                                    |
| ----------------- | --------------------------------------------------------- |
| `/`               | Home — the hello-world landing view                       |
| `/about`          | About — explains the SPA-fallback contract                |
| `/*` (any other)  | `404` — rendered by the SPA, not by the server            |

By default it is a **hash router** (`#/about`), which works on any static host
with zero configuration. If the host provides an SPA fallback (see below), plain
History-style deep links (`/about`) resolve to the same routes.

## Deploy

```
vulos web deploy ./apps/site-template
```

This uploads the directory as a static site. `index.html` is the entry document
and everything under `static/` is served as-is with cache headers.

## SPA fallback (the important part)

For a single-page app, **any path that is not a real file must serve
`index.html` with a `200`**, so the client router can take over.

- A request for `/static/app.js` or `/static/style.css` — a file that exists —
  is served directly with its own content type and cache headers. It must **not**
  fall through to `index.html`.
- A request for `/about` — a route with **no** matching file — must return
  `index.html` (`200`), not a server `404`. The router then renders the About
  view. Reloading `/about` or sharing the deep link must show the About page,
  not a server error.
- A genuinely unknown route still serves `index.html` (`200`); the SPA itself
  renders the `404` view. The server never emits the app-level 404.

If deep links return a server `404` instead of `index.html`, the SPA fallback is
misconfigured — that is the single most common breakage for hosted SPAs and the
behavior this template exists to make observable.
