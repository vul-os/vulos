# Workspace as an OS-hosted App — Implementation Plan

**Status:** PLAN ONLY. No code changed, nothing deleted. Every claim below carries
`file:line` evidence on both the OS side and the Workspace side.

**Decision (founder, final, 2026-07-09):** `vulos-workspace` is NOT retired and NOT
a shell. It becomes an **app the OS hosts** — a productivity HUB that consolidates
the collaboration surfaces (Mail / Calendar / Files / Board / Apps / Search /
Activity) into one integrated cockpit. The OS (`vulos/src`) is the single shell
(launcher, windows, rail, assistant, system apps). Workspace stays its own **AGPL**
package; the OS loads it as a gateway-proxied app — **no relicense, no
reimplementation** (loaded, not absorbed). Supersedes the "Workspace = AGPL OSS
shell" note in `vulos-product-architecture-2026-06`.

The essential distinction to preserve:
- **OS launcher = generic "open any app"** (Launchpad / Dock / ⌘K over the whole
  `AppRegistry`).
- **Workspace app = opinionated INTEGRATED suite cockpit** (dashboard +
  cross-product search + activity + collab surfaces). Different altitude, both
  valuable. The Workspace app must NOT re-add a competing shell.

---

## 0. Current reality (both sides already 80% wired)

Workspace is **already registered** in the OS AppRegistry as a gateway-proxied web
app today, but described as a "stateless shell":

- `vulos/src/core/AppRegistry.js:236-247` — entry `id: 'vulos-workspace'`,
  `name: 'Workspace'`, `type: 'web'`, `url: '/app/vulos-workspace/'`,
  comment "Unified workspace shell that ties the suite together."

So the migration is **not** "add Workspace to the OS" — it is **change what
Workspace renders inside that already-existing frame**: strip its shell chrome,
keep its consolidation content. This makes the whole change reversible (it is a
content edit within an app that is already mounted the same way).

---

## 1. DROP: Workspace shell chrome that the OS already provides

Workspace today paints a full second shell around its content. In the OS-hosted
model the OS shell is the ONLY shell, so this chrome is redundant and must be
dropped from the Workspace app. Evidence pairs (OS covers it / Workspace renders
it):

### 1a. The left icon RAIL (primary navigation)
- **Workspace renders it:** `vulos-workspace/src/components/Shell.jsx:80`
  (`<Rail products={products} />`); component `vulos-workspace/src/components/Rail.jsx:1-40`
  ("Persistent slim icon rail — the suite's primary navigation, always present").
- **OS already provides it:** the Dock (`vulos/src/shell/Dock.jsx`) and Launchpad
  (`vulos/src/shell/Launchpad.jsx`) are the OS's persistent app navigation;
  `MobileStack.jsx`/`DesktopCanvas.jsx` (`vulos/src/layouts/`) own the shell frame.
  A rail inside an iframed app is a second nav rail inside a window — pure
  duplication.
- **Action:** DROP `Rail`. Its per-product tiles are already OS launcher entries
  (`AppRegistry.js:213-318`).

### 1b. The waffle / APP-SWITCHER
- **Workspace renders it:** `vulos-workspace/src/components/Shell.jsx:102`
  (`<AppSwitcher products={products} />`); component
  `vulos-workspace/src/components/AppSwitcher.jsx:9-53` ("The Google-waffle: a
  grid of every product the account has").
- **OS already provides it:** the OS Launchpad IS the app grid
  (`vulos/src/shell/Launchpad.jsx`), fed by `AppRegistry.getApps()`
  (`AppRegistry.js:564-570`), and ⌘K (§1e). Two app grids = confusion.
- **Action:** DROP `AppSwitcher`.

### 1c. The TOP BAR (surface title, calendar toggle, account menu, activity)
- **Workspace renders it:** `vulos-workspace/src/components/Shell.jsx:82-105`
  (`<header className="ws-topbar">` with `SurfaceTitle`, `CommandPalette`,
  `ActivityCenter` (`components/ActivityCenter.jsx`, 196 lines — bell +
  activity/notification panel), `AppSwitcher`, `UserMenu`
  (`components/UserMenu.jsx`, 251 lines — avatar + account/settings/theme/org +
  sign-out)).
- **OS already provides it:** the OS Window chrome is the title bar
  (`vulos/src/shell/Window.jsx:148-355` renders window title/controls around the
  iframe at `Window.jsx:372-378`). `UserMenu` (identity, sign-out, theme, accent)
  duplicates the OS's own account/lock/settings + ThemeProvider surfaces
  (`vulos/src/auth/*`, `vulos/src/core/Settings.jsx`, `vulos/src/core/ThemeProvider.jsx`).
  `ActivityCenter` duplicates the OS's own notification runtime
  (`vulos/src/shell/NotificationCenter.jsx`, `vulos/src/core/notificationBridge.js`
  started at `vulos/src/App.jsx:128-132`). The window's own title bar already names
  the app.
- **Action:** DROP the `ws-topbar` header (`Shell.jsx:82-105`), including
  `SurfaceTitle` (`Shell.jsx:130-158`), `UserMenu`, `AppSwitcher`. `ActivityCenter`
  is not deleted but re-homed as content — its cross-product feed already powers the
  Home dashboard's activity stream (§2), so fold it into Home rather than the top bar.
  Keep the app's own in-content navigation between its views (tabs/segmented control
  inside the content), NOT a shell top bar. Note: `CalendarPanel`
  (`Shell.jsx:109-116`) is content, not chrome — keep it as an in-content panel (§2).

### 1d. The IFRAME-EMBEDDING of sibling products (ProductFrame)
- **Workspace renders it:** `vulos-workspace/src/components/ProductFrame.jsx`
  (whole file) — Workspace iframes OTHER products (Mail/Talk/Meet/OS) at
  `/app/:id` (`App.jsx:138`). It even iframes the **OS itself** as a product
  (`ProductFrame.jsx:14-16`, `MEDIA_PRODUCTS = Set(['meet','talk','os'])` at
  `ProductFrame.jsx:38`) and a `FrameContextBar` sibling-switcher
  (`ProductFrame.jsx:225-286`).
- **OS already provides it:** the OS is the embedder. It iframes apps under the
  gateway (`vulos/src/shell/Window.jsx:85-146` `IframeApp`,
  `Window.jsx:372-378`) with a first-party/third-party sandbox model
  (`AppRegistry.js:576-614`, `iframeSandbox` at `Window.jsx:14-16`). Meet/Talk are
  their OWN OS app tiles (`AppRegistry.js:249-271`), launched as OS windows.
- **Action:** DROP `ProductFrame`, the `/app/:id` route (`App.jsx:138`), and the
  self-embedding of OS-as-a-product. A Workspace user who wants Talk/Meet opens the
  Talk/Meet OS tile; Workspace links to it via the OS launch path (§3), never by
  iframing it inside itself. (Mail/Calendar/Board are NOT iframed even today —
  they render natively in-shell via shared libs, so they survive as content; see §2.)
  How a Workspace view triggers the OS to open a sibling app: the OS launch path is
  `launchApp()` → `openWindow(...)` (`vulos/src/shell/launchApp.js:46-74`). Since
  postMessage to the OS shell is same-origin-guarded and currently handler-less
  (`vulos/src/App.jsx:91-108`), the simplest first cut is a plain in-window link/
  new-window open to the sibling app's gateway URL; a dedicated
  "open OS app" postMessage command can be added to the shell's guard later if
  deeper integration is wanted (additive, out of scope for the first cut).

### 1e. The Workspace COMMAND PALETTE (⌘K)
- **Workspace renders it:** `vulos-workspace/src/components/Shell.jsx:85` +
  `vulos-workspace/src/components/CommandPalette.jsx` (509 lines) — a shell-level
  ⌘K over Workspace surfaces + cross-product content search.
- **OS already provides it:** the OS has its own ⌘K — `vulos/src/core/Portal.jsx`
  and `vulos/src/shell/CommandPalette.jsx` over `commandRegistry.js` +
  `AppRegistry.searchApps` (`AppRegistry.js:626-634`). Two global ⌘K palettes
  fighting for the same keybinding inside/outside the iframe is a real conflict.
- **Action:** DROP Workspace's shell-level ⌘K binding. Its **content-search**
  capability is kept, but re-homed as the in-app SearchSurface view (§2), reachable
  from the app's own UI, not a global hotkey. (Longer term the OS ⌘K can surface
  Workspace search hits via a command contributed to `commandRegistry.js`, but that
  is additive and out of scope for the reversible first cut.)

### 1f. Shell-level SSO HAND-OFF to embedded products
- **Workspace renders it:** `vulos-workspace/src/lib/sso.js` (postMessage token
  broker `SSO_REQUEST`/`SSO_RESPONSE`, `sso.js:35-38`) + the responder installed by
  `ProductFrame.jsx:118-125` (`installSSOResponder(...)`). Workspace mints a
  product-scoped token from the CP and posts it into the child frame's origin.
- **OS already provides it (differently and better):** the OS **gateway injects
  trusted identity server-side**. Every `/app/{id}/*` request is stripped of
  inbound `X-Vulos-*` and re-stamped with trusted headers —
  `X-Vulos-User-ID`, `X-Vulos-Email`, `X-Vulos-Session`, `X-Vulos-App-ID`
  (`vulos/backend/services/gateway/gateway.go:143-163`), plus per-app integration
  tokens (`gateway.go:165-190`) and storage creds (`gateway.go:192-...`). No
  postMessage token dance is needed: the app IS authenticated by virtue of being
  proxied through the gateway. The OS also hard-guards postMessage
  (`vulos/src/App.jsx:91-108`, same-origin only).
- **Action:** DROP `sso.js` and the ProductFrame SSO responder. Under the OS
  gateway, Workspace is authenticated the same way every other OS app is — no
  cross-origin token broker. (This also removes the cross-origin allowlist trust
  model entirely, shrinking the security surface.)

### 1g. Root auth GATE + marketing LANDING
- **Workspace renders it:** `vulos-workspace/src/App.jsx:82-98` (root gate) and
  `vulos-workspace/src/pages/Landing.jsx` (marketing landing shown to guests).
- **OS already provides it:** the OS owns auth entirely —
  `vulos/src/App.jsx:148-180` (`AuthGate`, `LoginScreen`, `Setup`, `LockScreen`).
  Under the gateway the user is ALREADY authenticated before the Workspace app
  loads (`gateway.go:154-159`). A logged-out user never reaches the iframe.
- **Action:** DROP the root auth gate and the marketing `Landing`. Per the memory
  note, the landing page becomes a **separate public marketing site**, not a route
  in the hosted app. Inside the app, treat the session as always-present (the
  gateway guarantees it).

### 1h. The `useSession()` CP-identity fetch (soften, don't delete)
- **Workspace renders it:** `vulos-workspace/src/lib/session.js:20-64`
  (`api('/auth/me')`, guest fallback, region resolution).
- **OS already provides it:** identity is injected by the gateway as
  `X-Vulos-User-ID`/`X-Vulos-Email` (`gateway.go:156-157`). The app can read its
  own identity from a gateway-provided endpoint rather than the CP `/auth/me`.
- **Action:** KEEP a session hook for display name/avatar, but repoint it: the
  hosted app's identity comes from the box gateway, not the CP login flow. Remove
  the guest/landing branch (§1g). This is a small edit, not a delete.

---

## 2. KEEP: Workspace consolidation surfaces (the app's actual content)

These are the REASON Workspace exists as an app: an integrated cockpit no single
product tile gives you. They become the hosted app's views (a slim in-content
tab/segment nav replaces the dropped shell rail). None of them is chrome.

| Surface | Workspace file:line | What it is | How it becomes a hosted view |
|---|---|---|---|
| **Home / dashboard** | `pages/Home.jsx:1-45+` (route `App.jsx:105`) | "calm-but-dense command center": greeting, cross-product activity stream, recent Talk conversations (`Home.jsx:24-45`, `GET /api/talk/recent`), today's agenda, quick launcher | The app's default view. Keep as-is; its data comes from product read-seams reachable on the gateway session. |
| **SearchSurface** (cross-product search) | `pages/SearchSurface.jsx:1-30+` (route `App.jsx:115`) | deep-linkable `/search?q=`, queries CP `GET /api/search` via `lib/searchClient.js`, groups real hits by product | Keep as an in-app view. Drop only its *global ⌘K binding* (§1e); the surface itself stays. |
| **Activity** | `components/ActivityCenter.jsx` (mounted `Shell.jsx:101`) | cross-product activity/notification center | Move OUT of the dropped top bar into the Home dashboard (it already feeds Home's activity stream). |
| **Mail surface** | `pages/MailSurface.jsx` (route `App.jsx:107`) | native `@vulos/mail-ui` in-shell (NOT iframed) | Keep as an in-app view — it's shared-lib content, no chrome. |
| **Calendar surface** | `pages/CalendarSurface.jsx` (route `App.jsx:108`) + `components/CalendarPanel.jsx` (`Shell.jsx:109-116`) | native calendar surface + collapsible side panel | Keep the surface. The side panel is optional; keep it as an in-content panel, not shell chrome. |
| **Files surface** | `pages/FilesSurface.jsx` (route `App.jsx:119`) | Drive browser over OS Files control plane `/api/files/*` | Keep. Note the OS also ships a `drive` tile (`AppRegistry.js:44-55`) — Workspace's Files view is the *consolidated* cockpit view; both can coexist (launcher tile vs cockpit view). |
| **Board surface** | `pages/BoardSurface.jsx` (lazy, route `App.jsx:129-136`) | Excalidraw-based board via `@vulos/board-ui` | Keep as an in-app view (already lazy-loaded). Coexists with the OS `board` tile (`AppRegistry.js:272-285`). |
| **Apps & Bots** | `pages/AppsSurface.jsx` (route `App.jsx:111`) | cross-product `@vulos/apps-ui` aggregate | Keep as an in-app view (this is consolidation, not the OS App Hub which installs OS apps). |
| **Relay / LLM** | `pages/RelaySurface.jsx`, `pages/LlmSurface.jsx` (routes `App.jsx:122,126`) | sovereign peer-fabric status; AI gateway BYO keys/usage | KEEP if desired as cockpit views, but these overlap OS system surfaces (Dashboard `AppRegistry.js:111-119`, Settings/AI). Candidate to fold into OS Settings later; not load-bearing for the first cut. |

**Net:** the content routes in `App.jsx:105-136` all SURVIVE. Only the shell
wrapper (`<Shell>` at `App.jsx:101`) and the embedding/dev/admin routes change.
A slim in-content nav (segmented control) inside the app replaces the dropped Rail.

---

## 3. Integration contract: how Workspace registers + loads under the gateway

Workspace uses the SAME contract as `lilmail` / `vulos-office` today. It is already
wired (§0); the plan only refines the registry entry.

### 3a. The registry entry (mirror lilmail/office)
Reference entries:
- `lilmail` — `AppRegistry.js:213-223` (`type:'web'`, `url:'/app/lilmail/'`,
  `port:80`, no storage).
- `vulos-office` — `AppRegistry.js:225-235` (`type:'web'`,
  `url:'/app/vulos-office/'`, `permissions:['storage']`).

Current Workspace entry — `AppRegistry.js:236-247`:
```js
{
  id: 'vulos-workspace',
  name: 'Workspace',
  icon: '⊞',
  description: 'Unified workspace shell that ties the suite together',
  keywords: ['workspace', 'suite', 'home', 'shell', 'launcher'],
  category: 'office',
  type: 'web',
  url: '/app/vulos-workspace/',
  port: 80,
  // No storage permission: Workspace is a stateless shell — it owns no files.
}
```
**Refinement (metadata only, non-breaking):** update `description`/`keywords` so it
reads as the integrated cockpit, not a "shell/launcher" (which now collides
conceptually with the OS shell). Suggested:
`description: 'Your suite cockpit — Mail, Calendar, Files, Board, and search in one place'`;
drop `'shell'` and `'launcher'` from `keywords` (they belong to the OS). Keep
`type:'web'`, `url:'/app/vulos-workspace/'`, `port:80`. Storage: Workspace itself
owns no files (its Files/Board views delegate to the Files control plane and the
board sync server), so **no `storage` permission** — leave as-is.

### 3b. How it loads under the gateway (the concrete data flow)
1. **Launch:** user opens the Workspace tile. `launchApp()` sees `type:'web'` +
   `url` and calls `openWindow({ appId:'vulos-workspace', url:'/app/vulos-workspace/', ... })`
   (`vulos/src/shell/launchApp.js:57-60`).
2. **Render:** the OS `Window` renders the URL in a sandboxed iframe —
   `Window.jsx:372-378` (`IframeApp`), sandbox from `iframeSandbox(appId)`
   (`Window.jsx:14-16`). Workspace is first-party
   (`firstPartyIds`, `AppRegistry.js:604-607`) so it MAY get `allow-same-origin`
   if it declares `needsSameOrigin` — but it does NOT need same-origin at load, so
   it runs in the opaque-origin sandbox like other suite apps.
3. **Proxy + auth:** the iframe `src` `/app/vulos-workspace/*` hits the box gateway
   (`Browser → :8080/app/{id}/* → [auth] → namespace`, `gateway.go:26`). The
   gateway strips inbound `X-Vulos-*` and injects trusted
   `X-Vulos-User-ID`/`-Email`/`-Session`/`-App-ID` (`gateway.go:143-163`). **This
   is the entire SSO hand-off** — no postMessage, no CP `/auth/me` round trip.
4. **Data:** Workspace's content views call product read-seams (`/api/talk/recent`,
   `/api/search`, `/api/files/*`, mail-ui/`@vulos/mail-ui`) on the same gateway
   session cookie. Any CP-only calls (billing/org) go to the CP as today (see §4).

### 3c. Contract summary (what Workspace must satisfy)
- Serve its SPA under a base path of `/app/vulos-workspace/` (Vite `base`), so all
  asset URLs resolve behind the gateway prefix (same requirement lilmail/office
  meet). Verify `vulos-workspace/vite.config.*` `base`.
- Read identity from gateway headers (or a box endpoint), not CP login (§1h).
- Emit no top-level shell chrome (§1); render content only, exactly as its own
  `Shell.jsx:19-23` comment already promises ("Product pages never render their own
  chrome — they export content only"). The change is to make the *Workspace app
  itself* obey that same rule relative to the OS.
- postMessage to the OS shell, if ever needed, must be same-origin
  (`vulos/src/App.jsx:91-108`).

---

## 4. Where `manage/` and `developers/` live — recommendation

Both subtrees talk to the **control plane (CP)**, not the box:
- `manage/` CP calls: `pages/manage/Overview.jsx:32` (`api('/billing/subscription')`),
  and CP calls throughout `pages/manage/**` (Overview, Products, Account, Storage,
  cloud/BillingUsage, cloud/OrgMembers `/api/org/members`, cloud/OrgAudit
  `/api/org/audit`, cloud/Account, Billing, Usage, Rates). All via
  `lib/api.js:7-19` (base `/api`, `credentials:'include'`).
- `developers/` CP calls: OAuth clients `/oauth/clients`
  (`pages/developers/OAuthClients.jsx:39,78,110`); webhooks `/developer/webhooks`
  (`Webhooks.jsx:40,81,106,118`); apps/bots `/developer/apps`
  (`AppsAndBots.jsx:29,70,100`); MCP `/mcp/servers` (`McpServers.jsx:36,72,95`);
  API keys `/developer/keys` (`GeneralApiKeys.jsx:35,67,90`); vesend
  `/vesend/*` (`VesendDashboard.jsx:75,94,113,132`).

**Recommendation: keep BOTH inside the Workspace app as a "Suite admin" area — NOT
in OS Settings.** Rationale:
1. **They are account/org/CP concerns, not box/system concerns.** OS Settings
   (`vulos/src/core/Settings.jsx`, 105k) is about the box/device (energy, drivers,
   persona, storage seam). Billing, org members, audit, API keys, OAuth clients,
   MCP, webhooks are *suite/tenant* administration that lives above the box —
   exactly Workspace's altitude as the suite cockpit.
2. **They are already self-contained routes** (`App.jsx:143-189`) with their own
   layouts (`DevelopersLayout.jsx`, `ManageLayout.jsx`) and degrade gracefully when
   the CP is absent (self-host hides the cloud subtree, `App.jsx:173-174`). Moving
   them into OS Settings would mean porting CP-coupled React into the AGPL-separate
   OS shell — the opposite of "loaded not absorbed."
3. **Self-host already does the right thing:** `App.jsx:173` collapses the whole
   `cloud/*` billing/org subtree under `VITE_SELF_HOST=1` (flag from
   `lib/selfHost.js`); `ManageLayout` filters `cloudOnly` sections and `UserMenu`'s
   `OrgSection` hides org switch/create. Keep that behavior; on a sovereign box
   these surfaces simply don't mount.
4. **Every CP seam degrades cleanly (404 → hidden, never fabricated):** manage/
   and developers/ surfaces are capability-probed — when the CP endpoint is absent
   they show an honest "not available on this instance" state, not a broken link
   (e.g. `manage/Products.jsx` PATCH `/api/products/:id` 404-degrades;
   `developers/*` keys/webhooks/mcp/oauth all 404-degrade). So keeping them in-app
   is safe on both cloud and box: they light up only where the CP serves them.

**Concrete placement:** keep `/manage` and `/developers` as **views inside the
Workspace app** (reached from the app's in-content nav, e.g. a footer/settings
entry — replacing the dropped Rail's `MANAGE_ITEM`/`DEV_ITEM`,
`Rail.jsx:24,38`). Do NOT surface them as OS launcher tiles or OS Settings panes.
This keeps the CP coupling entirely inside the AGPL Workspace package (§5).

(Only exception worth flagging: RelaySurface/LlmSurface (§2) overlap OS system
surfaces and *could* migrate to OS Settings later. manage/developers should not.)

---

## 5. AGPL-stays-separate: confirmation + coupling to watch

**The approach holds.** Workspace is `AGPL-3.0-only` (`vulos-workspace/LICENSE:1-2`,
`package.json` `"license": "AGPL-3.0-only"`). Loading it as a gateway-proxied
iframe app is a **separate program communicating over HTTP** — the OS shell links
nothing from the Workspace package at build time; it references it only by a URL
string (`AppRegistry.js:244`, `url:'/app/vulos-workspace/'`). There is no import of
Workspace source into `vulos/src`. So:
- **No relicense** — Workspace keeps AGPL; the OS keeps its own license.
- **No reimplementation** — the cockpit code is loaded, not copied.
- The gateway boundary (`gateway.go`) is exactly the arms-length HTTP seam that
  keeps the two programs separate.

**Coupling that fights this — remove as part of §1 so nothing pulls the two
together:**
1. **Workspace iframing the OS as a product** (`ProductFrame.jsx:14-16,38`,
   `os` in `MEDIA_PRODUCTS`). This is a circular embed (OS hosts Workspace hosts
   OS). Dropping `ProductFrame` (§1d) removes it.
2. **The SSO postMessage broker** (`sso.js`, `ProductFrame.jsx:118-125`) creates a
   cross-origin trust relationship between shell and children. Replaced by the
   gateway header injection (§1f) — a cleaner, license-neutral seam.
3. **CP-identity assumption at the root** (`App.jsx:82-98`, `session.js`) assumes
   Workspace owns the login flow. Under the OS it does not. Softening this (§1g/§1h)
   removes the coupling to the CP login shell.

None of these require importing OS code into Workspace or vice-versa; all are
Workspace-internal deletions/edits. The arms-length boundary is preserved.

---

## 6. Step-by-step, reversible implementation sequence

Each step is independently revertable. Nothing is deleted destructively before the
replacement view is confirmed; prefer feature-flagging the chrome off before
removing files.

**Phase A — make Workspace render chrome-less behind a flag (no deletions).**
1. Add a build/runtime flag `VITE_HOSTED=1` (mirror the existing `VITE_SELF_HOST`
   pattern, `lib/selfHost.js`). When set, `Shell.jsx` renders only
   `<main>{children}</main>` (skip `Rail`/`ws-topbar`/`AppSwitcher`/`UserMenu`/
   shell ⌘K, `Shell.jsx:78-124`). Reversible: unset the flag → old shell returns.
2. Under the flag, in `App.jsx` skip the root auth gate + `Landing`
   (`App.jsx:82-98`) and repoint `session.js` to read gateway-injected identity
   instead of CP `/auth/me` (§1h). Reversible: guarded by the same flag.
3. Verify assets: confirm Vite `base:'/app/vulos-workspace/'` so the SPA loads
   correctly behind the gateway (§3c).

**Phase B — replace the dropped Rail nav with in-content nav.**
4. Add a slim in-content segmented nav (Home / Mail / Calendar / Files / Board /
   Apps / Search, plus a footer entry to Manage/Developers) so the surviving
   content routes (`App.jsx:105-136,143-189`) stay reachable without the Rail.
   Purely additive.

**Phase C — drop cross-app embedding + SSO (self-contained deletions).**
5. Remove the `/app/:id` route (`App.jsx:138`) and `ProductFrame.jsx`; remove
   `sso.js` and its responder wiring (`ProductFrame.jsx:118-125`). Talk/Meet/OS are
   reached as OS tiles, not iframed inside Workspace (§1d/§1f). Reversible via
   git revert; no OS-side change required.

**Phase D — refine the OS registry entry (metadata only).**
6. Edit `AppRegistry.js:236-247` `description`/`keywords` so Workspace reads as the
   integrated cockpit, not "shell/launcher" (§3a). One-line, trivially reversible.
   `type/url/port` unchanged, so the OS launch path is untouched.

**Phase E — confirm manage/developers placement.**
7. Keep `/manage` + `/developers` as in-app Suite-admin views (§4); ensure the
   self-host collapse (`App.jsx:173`) still hides the cloud subtree on a box. No
   move to OS Settings. No change beyond wiring them into the in-content nav (step 4).

**Phase F — cut over + clean up.**
8. Flip `VITE_HOSTED=1` as the default for the OS-hosted build. Once verified,
   delete the now-dead chrome files (`Rail.jsx`, `AppSwitcher.jsx`,
   `ProductFrame.jsx`, `sso.js`, `Landing.jsx`, shell `CommandPalette.jsx`) and the
   flag branches. This final deletion is the only irreversible step and is gated on
   a working hosted build.

**Rollback:** at any point before Phase F step 8, unset `VITE_HOSTED` to restore the
standalone Workspace shell verbatim. The OS registry entry
(`AppRegistry.js:236-247`) already exists and is unaffected by A–E, so the OS can
host either the chrome-ful or chrome-less Workspace during the transition.

---

## Appendix — key evidence index

OS shell:
- App registry entry shape + Workspace entry: `vulos/src/core/AppRegistry.js:5-15`
  (builtin shape), `:213-247` (lilmail/office/workspace web entries), `:564-570`
  (`getApps`), `:576-614` (sandbox model).
- Launch path: `vulos/src/shell/launchApp.js:46-74`.
- Iframe host + sandbox: `vulos/src/shell/Window.jsx:14-16,85-146,372-378`.
- Gateway auth/header injection (the SSO hand-off): 
  `vulos/backend/services/gateway/gateway.go:26-29,143-163,165-190,192-...`.
- OS shell auth/gate: `vulos/src/App.jsx:91-108` (postMessage guard), `:148-180`
  (AuthGate/Login/Setup/Lock).
- OS ⌘K / launcher: `vulos/src/core/Portal.jsx`, `vulos/src/shell/Launchpad.jsx`,
  `vulos/src/shell/Dock.jsx`, `vulos/src/core/commandRegistry.js`.

Workspace:
- Shell chrome: `Shell.jsx:78-124` (frame), `Rail.jsx:1-40`, `AppSwitcher.jsx:9-53`,
  `Shell.jsx:130-158` (SurfaceTitle).
- Embedding + SSO: `ProductFrame.jsx` (whole), `sso.js:1-38`.
- Auth/session: `App.jsx:82-98` (gate), `session.js:20-64`.
- Content surfaces (KEEP): routes `App.jsx:105-136`; `Home.jsx:1-45`,
  `SearchSurface.jsx:1-30`, `MailSurface.jsx`, `CalendarSurface.jsx`,
  `FilesSurface.jsx`, `BoardSurface.jsx`, `AppsSurface.jsx`, `ActivityCenter.jsx`.
- manage/ CP calls: `App.jsx:156-189`, `manage/Overview.jsx:32`, `manage/cloud/*`.
- developers/ CP calls: `App.jsx:143-150`, `OAuthClients.jsx:39`, `Webhooks.jsx:40`,
  `AppsAndBots.jsx:29`, `McpServers.jsx:36`, `GeneralApiKeys.jsx:35`,
  `VesendDashboard.jsx:75`.
- License: `vulos-workspace/LICENSE:1-2`, `package.json "license":"AGPL-3.0-only"`.
