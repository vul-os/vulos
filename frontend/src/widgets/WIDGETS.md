# Building a Vulos widget

A widget is a small, glanceable surface on the right-hand rail of the desktop.
This is everything you need to build one.

It lives next to the code it documents on purpose: `src/widgets/index.ts` is the
API, and a doc that drifts from the module beside it gets noticed.

---

## The shortest widget that works

```tsx
import { defineWidget, registerWidget, WidgetFrame, WidgetBigValue } from '@vulos/widgets'

registerWidget(defineWidget({
  manifest: {
    id: 'com.example.counter',
    name: 'Days since',
    description: 'How long since a date you care about.',
    version: '1.0.0',
    author: 'Example',
    sizes: ['small', 'medium'],
    tick: 'minute',
    settings: [
      { key: 'since', type: 'string', label: 'Date', default: '2026-01-01', placeholder: 'YYYY-MM-DD' },
    ],
  },
  render: (ctx) => {
    const since = new Date(String(ctx.settings.since))
    const days = Math.floor((ctx.now.getTime() - since.getTime()) / 86_400_000)
    return (
      <WidgetFrame title="Days since">
        <WidgetBigValue sub={<span>since {String(ctx.settings.since)}</span>}>
          {Number.isFinite(days) ? days : '—'}
        </WidgetBigValue>
      </WidgetFrame>
    )
  },
}))
```

Three things to notice, because they are the whole model:

- **You declared a manifest.** The OS reads it to size your tile, drive your
  clock, draw your settings form and decide what you may touch.
- **You did not call `setInterval`.** `ctx.now` is handed to you at the cadence
  your manifest asked for.
- **You did not draw a settings UI.** You declared `settings`; the OS renders the
  form. (This is a security property — see *What a widget cannot do*.)

---

## The manifest

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | Stable identity. Lowercase alphanumeric segments joined by `-` or `.`, ≤64 chars. Reverse-DNS is conventional for third-party widgets (`com.example.tides`). |
| `name` | yes | ≤48 chars. Shown in the gallery and used as your tile's accessible name. |
| `description` | yes | ≤160 chars, one line, shown under the name in the gallery. |
| `version` | yes | Opaque string, surfaced to the user. |
| `author` | no | ≤64 chars. |
| `sizes` | yes | Non-empty subset of `small` / `medium` / `large`. **The first entry is the default.** |
| `permissions` | no | Subset of the closed list below. Absent means your widget gets nothing. |
| `hosts` | only with `network` | Bare hostnames you will reach. No scheme, no path, **no wildcards**. Max 8. |
| `tick` | no | `none` (default), `minute` or `second`. |
| `settings` | no | Up to 16 declarations; the OS renders the form. |

The manifest is validated at registration and **rejected, not repaired**. An
unrecognised permission or size is an error rather than something quietly
ignored — an ignored field is a field that gets silently honoured the day
someone adds it to the enum.

You can check a manifest yourself:

```ts
import { checkManifest } from '@vulos/widgets'
const { ok, errors } = checkManifest(myManifest) // errors lists EVERY problem, not the first
```

---

## Sizes

Three, in a two-column grid. **You never get a pixel dimension**, because the
rail's column width changes with the viewport and a widget that laid itself out
in pixels would break the grid for everyone else.

| Size | Footprint | Good for |
|---|---|---|
| `small` | 1 column × 1 row | one number, one glyph, one word |
| `medium` | 2 columns × 1 row | a line of detail, a sparkline, 2–3 rows |
| `large` | 2 columns × 2 rows | a list, a chart, a set of tiles |

Declare every size you can genuinely render; the **user** picks among them. Read
`ctx.size` and lay out accordingly — a `large` widget that just renders the
`small` layout with a void beneath it looks broken, not spacious.

---

## Cadence

```ts
tick: 'none' | 'minute' | 'second'
```

The rail runs **one** scheduler for every widget and re-renders yours with a
fresh `ctx.now`. Ten widgets each running their own 1 Hz interval would be ten
timers the OS cannot see, cannot throttle when the rail is hidden and cannot stop
when the screen sleeps — on a box meant to idle quietly, that is a real power
cost.

Pick the **coarsest** cadence that is truthful. If you show `HH:MM`, use
`'minute'`; a `'second'` tick would re-render 59 times for nothing. If your data
is pushed to you (telemetry), use `'none'`.

And do not display something your cadence cannot keep true. A minute-cadence
widget that prints seconds is wrong for 59 out of every 60 seconds.

---

## Settings

Declared, not drawn:

```ts
settings: [
  { key: 'city',    type: 'string',  label: 'City', maxLength: 40 },
  { key: 'refresh', type: 'number',  label: 'Minutes', default: 15, min: 5, max: 120 },
  { key: 'compact', type: 'boolean', label: 'Compact', default: false },
  { key: 'units',   type: 'select',  label: 'Units', default: 'c',
    options: [{ value: 'c', label: 'Celsius' }, { value: 'f', label: 'Fahrenheit' }] },
  { key: 'zones',   type: 'list',    label: 'Cities', default: [], maxItems: 8 },
]
```

`ctx.settings` is **already validated**: it contains exactly the keys you
declared, each of the type you declared, clamped to the range you declared. A
number setting holding the string `"12"` arrives as your default, not as `"12"`.
Undeclared keys are dropped — persisted settings are storage, and anything that
can write that storage could otherwise feed you arbitrary data.

Write one with `ctx.setSetting(key, value)`. Writes are re-validated the same way.

---

## Permissions

**Default deny, every one, per placement.** Your manifest *requests*; the user
*grants*, one at a time, having read what each means. Until then the matching
field on `ctx` is `null` — not a degraded object, `null`.

| Permission | You get | Leaves the box? |
|---|---|---|
| `storage` | `ctx.storage` — namespaced key/value, quota'd | no |
| `network` | `ctx.net` — box-brokered HTTP to your declared hosts | **yes** |
| `notify` | `ctx.notify(title, body?)` — rate-limited | no |
| `notifications` | `ctx.notifications` — the newest few, read-only | no |
| `launch` | `ctx.openApp(appId)` | no |
| `telemetry` | `ctx.telemetry` — CPU/memory/uptime of this box | no |
| `calendar` | `ctx.calendar` — upcoming events | no |

**Every widget must work with everything denied.** Not "degrade" — *work*, with
an honest message saying what it needs:

```tsx
if (!ctx.storage) {
  return <WidgetFrame title="Notes">
    <WidgetEmpty>Allow “Store its own settings” so this note can be kept.</WidgetEmpty>
  </WidgetFrame>
}
```

A stored grant is intersected with what your manifest *currently* requests, so
dropping a permission from your manifest revokes it. You cannot shed a scary
permission to get installed and get it back in an update without re-prompting.

---

## Network

Read this even if you think you do not need it. **The point of this platform is
that the user's box does not phone home.**

- **You never call `fetch`.** You call `ctx.net.getJSON(url)`, or `ctx.net` is
  `null`.
- **The BOX makes the request, not the browser.** If the browser called your
  provider directly, the provider would learn the user's IP and browser
  fingerprint. Through the box it sees one origin, the box can cache and coalesce
  so ten renders are one upstream call, and the box can log the call so the
  user's Transparency panel can show exactly what left.
- **Only your declared hosts.** Exact hostname match. No wildcards:
  `*.example.com` reads as "the vendor's subdomains" and means "anything the
  vendor's DNS can be pointed at".
- **No proxy, no request.** If the box does not expose the broker, `ctx.net` is
  `null` and there is **no fallback to a direct browser call**. A silent fallback
  is how a privacy promise erodes: the feature keeps working, so nobody notices
  the guarantee left. No box ships the broker today, so in practice **every
  widget must be fully useful offline.**

```ts
const r = await ctx.net!.getJSON('https://api.example.com/v1/thing')
if (!r.ok) {
  // 'no-proxy' | 'blocked-host' | 'http' | 'offline' | 'bad-body'
}
```

If your widget's whole purpose is remote data, say so on its face and show the
user what it is showing them and how old it is. The shipped Watchlist widget is
the worked example: it requests no network permission at all, the user enters the
prices, and it prints "your own figures, entered 3 hours ago" every time.

---

## What a widget cannot do

The honest answer depends on which lane you are in.

### Lane 2 — sandboxed (everything not shipped inside the OS build)

Your code runs in `<iframe sandbox="allow-scripts">` — an **opaque origin**. It
cannot:

- read or write the shell's DOM, cookies, `localStorage`, IndexedDB, service
  worker or session
- navigate the top window, open popups, submit forms, open modals, or download
- reach the network except through `ctx.net`, to your declared hosts, via the box
- see any other widget's storage, settings or existence
- name a capability it was not granted, or invent a protocol verb — the message
  surface is closed, and an unknown verb is refused
- claim to be another widget: **it never sends its own identity.** The host
  derives it from the frame the message arrived on and scopes every storage key
  and every host check to that. A widget that lies about every field in a message
  still cannot address another widget's data.
- flood: `notify` and `net` are rate-limited per placement; storage is quota'd
  (16KB per value, 64KB per widget); refusals are refusals, not queues.

It also cannot draw its own card — the host wraps it, so a widget cannot escape
the tile's shape — and it cannot draw its own settings UI, which is what stops a
widget painting a convincing "Vulos needs your password" panel in the rail. Every
form the user types into is drawn by the OS.

You write a complete HTML document and the host injects `window.vulosWidget`:

```ts
import { defineSandboxedWidget, registerWidget } from '@vulos/widgets'

registerWidget(defineSandboxedWidget({
  manifest: { id: 'com.example.moon', name: 'Moon phase', /* … */ sizes: ['small'], tick: 'minute',
              description: 'Tonight’s moon.', version: '1.0.0' },
  source: `
    <div id="root"></div>
    <script>
      window.vulosWidget.onContext(function (ctx) {
        document.getElementById('root').textContent = ctx.now.toDateString()
      })
    </script>`,
}))
```

`window.vulosWidget` is the complete surface:

| Member | Notes |
|---|---|
| `context()` | the current context |
| `onContext(fn)` | called now and on every change |
| `storage.get/set/remove/keys` | needs `storage`; returns Promises |
| `setSetting(key, value)` | re-validated against your manifest |
| `getJSON(url)` | needs `network` |
| `notify(title, body)` | needs `notify` |
| `openApp(appId)` | needs `launch`; an app **id**, never a URL |

The source is delivered by `srcdoc`, never fetched. Your code is data the box
already holds: it cannot change under the user, and loading it costs no request.

### Lane 1 — in-process (`defineWidget`)

React, rendered on the **shell's own origin with the shell's DOM**. The
permission model is a *clarity* mechanism here, not a containment one: code in
this lane could reach around the context object if it chose to.

That is acceptable for widgets that ship inside the OS build and are reviewed
with it. **It is not acceptable for anything else.** A widget that did not ship
with the OS runs in Lane 2.

---

## Adding, removing, arranging

All of it is the user's, and none of it is yours to drive:

- **Add** — "Edit widgets" → "Add widget". The gallery shows your name,
  description, sizes, author, how many permissions you ask for and, if you asked
  for `network`, the hosts you named. Adding grants **nothing**.
- **Configure** — the ⚙ chip opens the OS-drawn form: your settings, then your
  permissions as individual switches with the OS's own description of each.
- **Resize** — the ⤢ chip cycles the sizes your manifest offers.
- **Reorder** — ↑ / ↓ chips (keyboard-reachable; not drag-only).
- **Remove** — the × chip. **This also erases your stored data**, so a user who
  removed your widget to be rid of it is rid of it.

A widget may appear in the rail more than once. Each placement has its own
settings, its own grants and its own storage — two world clocks can show
different cities.

---

## House rules

1. **Never invent content.** If you have nothing, say so: `WidgetEmpty`. If you
   could not reach something, say *that* instead: `WidgetError`. "You have
   nothing on" and "we could not ask" look identical on screen and mean opposite
   things.
2. **Use the tokens.** `WidgetLabel`'s `tone` takes a token name, not a colour,
   so your widget is legible in both themes and passes the OS's composited
   contrast gate without you knowing it exists. Never hardcode a hex.
3. **Honour `ctx.reducedMotion`.** If you animate at all, don't when it's true.
4. **Assume no network, no account, no backend.** That is the normal state of a
   sovereign box, not an error case.
5. **Fail small.** Each tile has its own error boundary, so a throw breaks your
   tile and nothing else — but the user still sees a broken tile. Guard your
   parsing; `ctx.settings` values are validated for *type*, not for *meaning*.

---

## Testing

The pure parts of a widget should be functions you can call. The OS's own
widgets keep their logic in a sibling module (`builtin/logic.ts`) precisely so it
can be asserted without a DOM:

```ts
expect(parsePosition('AAPL 189.50 175.00')).toEqual({ symbol: 'AAPL', last: 189.5, ref: 175 })
expect(parsePosition('AAPL abc')).toBeNull()
```

And look at your widget. Render it at every size you declared, in **both**
themes, and read the image — `e2e/widgets-shoot.mjs` does this for the shipped
rail at five widths. Six real defects in this rail were found that way after the
unit suite, eslint and tsc were all green.

---

## Reference

- API: `src/widgets/index.ts` — if it is not exported there, do not import it.
- Design rationale and the security model: `roadmap/WIDGETS.md`.
- Worked examples: `src/widgets/builtin/` (in-process) and
  `src/widgets/examples/moon.ts` (sandboxed). Every one of them is written
  against this same public API — enforced by `__tests__/publicApi.test.ts`, which
  fails if a builtin reaches past it.
