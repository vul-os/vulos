# Desktop Widgets

The right-hand rail on the desktop, and the API a third party builds against.

> **Goal.** Make the rail excellent, and make it a *platform*: a user can add,
> remove, reorder and configure widgets, and someone outside this repo can ship
> one without our help and without our trust.
> **Non-goals.** Free-form pixel placement. Widgets on the phone home surface
> (MobileStack owns that). A widget store, signing or distribution — this defines
> the runtime contract; APP-STORE.md's machinery is what would eventually carry
> it.
> **Status.** Shipped. `frontend/src/widgets/`, mounted by
> `frontend/src/shell/DesktopWidgets.tsx`. Developer guide:
> `frontend/src/widgets/WIDGETS.md`.

---

## 1. What changed and why

The rail used to be three components declared inside `DesktopWidgets.tsx`. "Add a
widget" meant "edit the shell", and a user could add nothing at all.

It is now a **host**. A user-owned layout, a registry, and a public API
(`src/widgets/index.ts`) that the OS's own widgets are held to as strictly as a
stranger's — enforced, not asserted: `__tests__/publicApi.test.ts` reads every
builtin's source and fails if one imports anything but the public entry. That
gate is the only reason we know the API is *sufficient*. If it weren't, a builtin
would have had to cheat, and the test would name the file. (It did, once, on the
first run: `builtin/logic.ts` was importing `../types`.)

---

## 2. What a widget is

A **manifest** plus a **surface**.

The manifest declares identity, the sizes it can render, a tick cadence, its
settings, and its permissions. It is validated at registration and **rejected,
not repaired** — an unrecognised permission is an error rather than something
ignored, because an ignored field is one that gets silently honoured the day
someone adds it to the enum.

Three sizes in a two-column grid (`small` 1×1, `medium` 2×1, `large` 2×2). A
widget is **never told a pixel dimension**: the rail's column width varies with
the viewport, and one widget laying itself out in pixels would break the grid for
every other.

**The host owns every timer.** A widget declares `none`/`minute`/`second` and is
re-rendered with a fresh `now`. Not a style preference: ten widgets each running
their own interval would be ten timers the OS cannot see, cannot throttle when
the rail is hidden and cannot stop when the screen sleeps — a real power cost on
a box meant to idle quietly. One scheduler drives them all, and the two cadences
are separate state values so a 1 Hz world clock does not re-render eight
minute-cadence tiles with it.

**The host owns every seam, too.** One telemetry socket and one agenda read for
the whole rail, opened only when at least one *granted* widget needs them, closed
when the last one goes away.

---

## 3. The security boundary

A widget on the home screen of a sovereignty product is untrusted code in a
trusted place. There are two lanes and the difference between them is the whole
story.

| | `defineWidget` | `defineSandboxedWidget` |
|---|---|---|
| Runs as | React, on the shell's origin | `<iframe sandbox="allow-scripts">`, opaque origin |
| Containment | **none** — convention only | the browser's |
| For | code shipped inside the OS build, reviewed with it | **everything else** |

This is stated plainly rather than blurred. In-process code could reach around
the context object if it chose to; the permission model is a *clarity* mechanism
there, not a containment one. Anything that did not ship with the OS runs in
Lane 2, where the boundary is enforced by the browser.

### Lane 2, concretely

`sandbox="allow-scripts"` and nothing else. Not `allow-same-origin` — that single
omission is the containment, and it is the same invariant `core/AppOrigins.ts`
exists to protect for app frames, where granting it to first-party apps served
from the shell's origin was once a full shell takeover. Not
`allow-top-navigation`, `allow-popups`, `allow-modals`, `allow-forms` or
`allow-downloads` either.

The bridge deliberately mirrors `core/AppBridge.ts`:

- **Two checks on the handshake, both required.** Frame identity
  (`event.source === ` the `contentWindow` we hold — an object comparison, and
  the only meaningful check when every opaque frame reports origin `'null'`), and
  origin (`=== 'null'`).
- **Replies go on the MessagePort.** `postMessage(…, '*')` appears nowhere in the
  host half. The frame's one `hello` is addressed to the shell's exact origin,
  injected by the host, so a frame hosted anywhere else never forms a channel.
- **Identity is never claimed.** The widget does not send its instance id; the
  host derives it from the frame and scopes every storage key, host check and
  setting write to it. A widget that lies about every field still cannot address
  another widget's data.
- **The verb surface is closed.** An unknown verb is refused, not extended.
- **Rate limits and quotas**: `notify` 1/10s, `net` 1/2s, storage 16KB/value and
  64KB/widget. A refusal is a refusal, not a queue — a queue is a slower way to
  let it win.

Two further things the host keeps for itself, because a widget cannot be trusted
with them and could not do them anyway from inside an opaque frame:

- **The card.** The host wraps the frame, so a widget cannot escape the tile's
  shape.
- **The settings form.** A widget *declares* settings; the OS *renders* them.
  This is what stops a widget painting a convincing "Vulos needs your password"
  panel inside the rail. Every field a user types into is drawn by the OS.

### Permissions are enforced here, not somewhere else

This deserves stating plainly because Vulos already contains the failure mode.
An **app** manifest's `permissions` array is validated against a list of valid
strings and then, for almost all of them, has *no runtime effect at all* — an app
declaring `camera` is neither granted nor denied anything, because nothing reads
the declaration. The string is documentation wearing the costume of a control.

So "the platform will contain it" was not available as an assumption for this
API, and a widget permission that existed only in a manifest and a settings
switch would be the same lie in a new place. Every grant here is enforced in this
code:

- `host/context.ts` is a **pure function**, deliberately, and it is the single
  place a `granted` array becomes a capability or a `null`. It is a function
  rather than a literal inside the rail's JSX precisely so it can be driven
  directly by `__tests__/permissions.test.ts` — buried in a component the same
  logic would be reachable only through a render, which in practice means
  untested, which in practice means a `granted.includes(…)` could be deleted and
  nothing would notice.
- Denied yields `null` — not a throwing object, not an empty stub — so a widget
  can *see* that it does not have something.
- Denied also **stops the box doing the work**: `seamsNeeded()` decides whether
  the telemetry socket and the agenda read are opened at all. Handing a widget
  `null` while still holding the socket open would satisfy the type and miss the
  point.
- For the sandbox lane the bridge refuses each verb on the grant it needs, and
  refuses unknown verbs outright.

The test asserts, for **every** string in the enum: denied ⇒ null, granted ⇒
usable, and granting one grants *nothing else*. It also asserts the enum and the
test's own table are the same set, so a permission added later without a gate
fails there rather than in production. Four mutations confirm it can fail —
including one that ignores the `telemetry` grant, which is exactly the app
manifest's behaviour today.

### The model

Closed enum, **default deny, per placement**. `storage`, `network`, `notify`,
`notifications`, `launch`, `telemetry`, `calendar`. Adding a widget from the
gallery grants **nothing**; the user turns each one on afterwards, having read
the OS's own sentence about what it means and whether it can leave the box.

Two rules that matter more than they look:

- **A stored grant is intersected with what the manifest currently requests.** A
  permission the widget no longer asks for is revoked by that fact alone —
  otherwise a widget could shed a scary permission to get installed and get it
  back in the next update without re-prompting.
- **`notify` (write) and `notifications` (read) are separate.** A widget that
  wants to display a count has no business being able to fabricate an alert.

The one deliberate asymmetry: widgets the OS ships in its **own default rail**
come with their requested permissions granted. The box's own widgets are covered
by the decision to install the OS, and a fresh desktop whose shipped widgets all
read "Allow…" looks broken rather than careful. Anything the user adds asks.

### What is NOT solved here

- Distribution, signing and provenance. A sandboxed widget's source is a string
  the box already holds; how a stranger's string gets onto the box is
  APP-STORE.md's problem, and until it is solved the sandbox lane is exercised
  only by the shipped example.
- The **box-side** enforcement of the host allowlist. The browser-side check is
  advisory; the broker must re-check (§4).
- Resource exhaustion beyond the rate limits — a sandboxed widget can still burn
  CPU in its own frame.

---

## 4. Stocks and the network

**The decision: the shipped Watchlist widget requests no network permission at
all, and no widget can originate a third-party request on any box today.**

### The reasoning

**There is now a precedent to match.** The founder has excluded proprietary apps
from the App Hub catalogue for the time being, explicitly to avoid vendor terms
and vendor-controlled payloads. A stocks widget calling a third-party finance API
from the box sits in the same territory: it is a vendor relationship the user did
not choose, carrying a payload the vendor controls, and the request itself
discloses personal data. The decision below was reached independently and lands
in the same place, which is the answer one would want.

Every quote source is a third party. The set of tickers a person watches is not
incidental — it is their portfolio, their employer, the thing they are about to
buy — and a quote request discloses it with an IP and a timestamp, on a schedule
that also reveals when the machine is awake. On a product whose whole claim is
that the user's box does not phone home, a silent US finance API on every desktop
would not be an implementation detail. It would quietly retract the pitch.

**Box or browser?** If the browser calls the provider, the provider learns the
user's IP and browser fingerprint and gets a per-user request stream. Through the
box it sees one origin; the box can cache and coalesce so ten renders are one
upstream call, and it can log the call so the Transparency panel can show the
user exactly what left. So: **the box, never the browser** — and the box's IP is
one the user already accepted when they chose to run a server.

**Enabled by default?** No. The widget is not in the default rail, and it makes
no requests even when added.

**What happens with no network?** It is fully useful: it is a watchlist you
maintain. You enter the symbol and the price you last saw, optionally a reference
price, and it prints the move and *"your own figures, entered 3 hours ago. No
price source on this box."* Less magical than a live ticker, completely true
offline, and it says whose numbers it is showing every time it is looked at.

### How the platform makes this hard to get wrong later

`net.ts` is built so the promise cannot be broken by accident:

1. A widget never calls `fetch`; it is handed `ctx.net` or `null`.
2. `ctx.net` posts to a **same-origin** `/api/widgets/fetch` — the box brokers it.
3. If the box exposes no broker, `ctx.net` is `null` and there is **no fallback
   to a direct browser call**. A silent fallback is exactly how a privacy promise
   erodes: the feature keeps working, so nobody notices the guarantee left.
4. The host allowlist is exact-match, no wildcards. `*.example.com` reads as
   "the vendor's subdomains" and means "anything the vendor's DNS can be pointed
   at". It is checked in the frame, in `net.ts`, and must be checked again on the
   box — the browser is not the authority.

**No box ships the broker.** So today the property is not a policy, it is a fact
you can grep for: the only `fetch` in the whole widget tree targets a same-origin
`/api/` path, asserted by a test that scans every source file, and an e2e test
records every request the page makes and fails if one leaves the origin.

### Open / next

- `POST /api/widgets/fetch` and `GET /api/widgets/fetch/status` on the box:
  re-check the URL against the *manifest's* hosts (not the body's claim), cache,
  coalesce, strip cookies and `Referer`, hold any API key server-side, and emit a
  Transparency event per call. **Until this exists, live quotes are unreachable,
  which is the intended resting state.**
- A live-quote widget would be a *separate* widget declaring its own provider,
  shipping disabled, chosen by the user — not a flag on this one.

---

## 5. Widgets shipped

| id | Permissions | Third party? |
|---|---|---|
| `vulos.clock` | none | no |
| `vulos.worldclock` | none — platform tzdata is already on the box | no |
| `vulos.agenda` | `calendar`, `launch` | no |
| `vulos.pulse` | `telemetry` | no |
| `vulos.notifications` | `notifications` (read only) | no |
| `vulos.notes` | `storage` | no |
| `vulos.watchlist` | `storage` | **never** |
| `com.example.moon` | none | **yes — sandboxed** |

Weather was considered and **not** shipped: there is no local weather seam, so it
would be a third-party call by construction. It belongs in the same bucket as
live quotes — a separate, user-chosen widget once the broker exists.

`vulos.agenda` replaced `src/shell/CalendarWidget.tsx` in the rail and keeps its
full contract (the `data-calendar-widget` marker, live/stale, next-up collapsed,
week on expand, Connect Mail) because that behaviour is gated by
`e2e/calendar-widget.e2e.ts` and a rewrite is not a licence to drop it.

### World clocks

Everything goes through `Intl` with an explicit `timeZone`; no offset is ever
stored. The naive version is wrong four ways at once, and New York + Sydney is
the worst possible pair to be wrong about — opposite hemispheres, so their gap is
16h in January and 14h in July. Both are asserted, along with half-hour
(Kolkata), quarter-hour (Kathmandu), quarter-hour-with-DST (Chatham), the exact
US spring-forward instant, midnight crossings, and a year boundary. An unknown
zone id degrades to a message; it must never throw, because this renders on the
desktop and persisted settings outlive tzdata entries.

---

## 6. Verification

- **167 unit tests.** Manifest validation, layout reconciliation, storage
  isolation and quotas, the bridge's refusals, the tz boundary cases, the public
  API discipline gate, and a per-permission gate asserting denied ⇒ null,
  granted ⇒ usable, and granting one grants nothing else — for every string in
  the enum.
- **17 e2e** in a real browser against the production bundle, including a
  composited-pixel contrast scan in **both** themes covering the resting rail
  *and* the edit/gallery/settings chrome that only exists after a click, with a
  measured coverage floor so a rail that renders nothing cannot pass.
- **Mutation-tested.** 17 mutations, all killed — including one that ignores the
  `telemetry` grant (the app manifest's actual behaviour today) and one that
  gates `notify` on the read permission instead of the write one. One of them appeared killed and
  had silently not applied — a mutation that does not apply is a hollow gate
  wearing a green tick, so the harness now asserts its anchor.
- **Looked at.** `frontend/e2e/widgets-shoot.mjs` renders the rail at
  390/768/834/1280/1680 in both themes. Six real defects were found by reading
  those images *after* the unit suite, eslint and tsc were all green — including
  a rail that ran 350px off the bottom of the screen and a gallery panel that was
  in the DOM, clickable by accessible name, and invisible.

---

## 7. Open items

- **Tablet.** The rail renders at 768 and 834 (both fall to `DesktopCanvas` in
  practice); the mobile home surface in `MobileStack` has no widgets at all. A
  phone-sized rail is a separate design (MOBILE.md), not a media query.
- **Drag to reorder.** Today reordering is ↑/↓ chips — keyboard-reachable and
  testable. Drag should be added *alongside* them, never instead.
- **Settings integration.** A "Widgets" panel under `core/settings/` would let a
  user review every grant in one place instead of per tile. That file is outside
  this change's ownership.
- **`src/shell/CalendarWidget.tsx` is now unmounted** — only its own unit test
  imports it. It and `src/__tests__/CalendarWidget.test.tsx` should be removed
  together by whoever owns that test directory.
