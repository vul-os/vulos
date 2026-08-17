/**
 * syncedPrefs.ts — the box, not the browser, owns your preferences.
 *
 * # The defect this exists to end
 *
 * Wallpaper, theme, accent, dock pins, desktop arrangement and the widget rail
 * were `localStorage`. `localStorage` is not per-user and not per-box — it is
 * per BROWSER PROFILE. So none of it followed a user to their second instance,
 * and opening the SAME box in a second browser lost it too. That is a strictly
 * worse failure than "does not sync", and it applied to every customization
 * feature the shell has.
 *
 * The vehicle is `Profile.Settings`, the free-form per-user string map on the
 * replicated profile row (backend/services/auth/profile_settings.go). It rides
 * the same CRDT the rest of the profile does, so a preference replicates like a
 * display name does.
 *
 * # localStorage is now a CACHE, and it is not optional
 *
 * `main.tsx` stamps `data-theme` and `data-density` onto <html> BEFORE React
 * mounts — long before a profile can be fetched. Without a synchronous local
 * copy, every reload flashes the wrong theme. So the local copy stays; what
 * changes is that it is no longer the source of truth. See
 * roadmap/USER-STATE-INVENTORY.md §8.
 *
 * # The reconciliation rule
 *
 *   1. FIRST hydrate for a user session — the server wins where it has a value;
 *      where it has NOTHING and the cache does, the cache's value is ADOPTED
 *      (applied locally and pushed up). This is the one-time migration for
 *      every box that already has real values today.
 *   2. Every hydrate after that — the server wins unconditionally, and an
 *      absent server key means UNSET.
 *   3. Adoption is idempotent by construction, not by a flag: after step 1 the
 *      server has the key, so step 1 produces an empty patch on every later
 *      boot. Hydrating writes nothing unless something is genuinely missing, so
 *      it does not fight the sync engine on reload.
 *
 * Step 2 is why adoption must not re-run. If it did, deleting a preference on
 * box A would be resurrected by box B's stale cache the next time B loaded.
 *
 * # Offline
 *
 * A write made with no reachable box is applied to the cache at once (the UI is
 * correct immediately) and appended to a pending queue that is itself mirrored
 * to localStorage, so it survives a reload. At the next hydrate the queue is
 * replayed OVER the server's values — a write the user made later beats a value
 * the server has held since before — and cleared once the server accepts it.
 *
 * A patch the server REJECTS (4xx) is dropped rather than retried: it will
 * never succeed, and a poison entry that retries forever would block every
 * later write behind it.
 *
 * # This module imports nothing from the app
 *
 * Deliberately. Every owning module (desktop/store.ts, widgets/layout.ts,
 * ThemeProvider) imports THIS, and core/prefGroups.ts is the one place that
 * knows about both. A dependency-free engine cannot be part of a cycle.
 */

/** Mirrors auth.MaxSettingValueLen. A longer value is refused by the whole patch. */
export const MAX_SYNCED_VALUE = 512

/** Mirrors auth.MaxSettingKeys. */
export const MAX_SYNCED_KEYS = 64

/**
 * The reserved bag key that maps to `Profile.Theme` — a real replicated COLUMN,
 * not a Settings entry.
 *
 * There were two themes: this column, which synced and which nothing in the
 * frontend read or wrote, and `vulos-theme` in localStorage, which governed
 * what a user saw. The column wins (roadmap/USER-STATE-INVENTORY.md §9). It is
 * routed through this engine as a virtual key so that theme gets the same
 * adoption, pending-queue and offline behaviour as everything else; the bridge
 * splits it back out into the PUT's top-level `theme` field.
 */
export { THEME_BAG_KEY } from './prefKeys'

/** Where an offline write waits. Not itself synced — it IS the outbox. */
const PENDING_KEY = 'vulos.prefs.pending.v1'

/** What a push attempt reports back. `unreachable` is retried; `rejected` is not. */
export type PushResult = 'ok' | 'rejected' | 'unreachable'

export interface PrefGroup {
  /** Stable name, for diagnostics and for the registry's own tests. */
  name: string
  /** Whether this group owns `bagKey`. */
  owns: (bagKey: string) => boolean
  /**
   * Every key this group currently has a local value for. Keys with no value
   * are omitted rather than mapped to '' — absent and empty mean the same
   * thing here and only one of them should be expressible.
   */
  read: () => Record<string, string>
  /**
   * Apply an authoritative set. A key ABSENT from `values` is unset, not left
   * alone. Implementations MUST NOT push — `pushPref` is suppressed while this
   * runs, but a group that pushes from a different tick would escape that.
   */
  write: (values: Record<string, string>) => void
}

/* ── local cache ──────────────────────────────────────────────────────────── */

let version = 0
const listeners = new Set<() => void>()

function emit(): void {
  version++
  for (const fn of listeners) fn()
}

export function subscribePrefs(fn: () => void): () => void {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

export function prefsVersion(): number {
  return version
}

/** Read one cached value. '' when unset — never null, so callers cannot forget a case. */
export function prefRead(lsKey: string): string {
  try { return localStorage.getItem(lsKey) ?? '' } catch { return '' }
}

/**
 * Write one cached value WITHOUT pushing. '' removes the key.
 *
 * This is the cache half only. A user-initiated change calls `setPref`, which
 * does this and then queues the server patch.
 */
export function prefWriteLocal(lsKey: string, value: string): void {
  try {
    if (value) localStorage.setItem(lsKey, value)
    else localStorage.removeItem(lsKey)
  } catch { /* private mode — the in-memory value still applies for this session */ }
}

/* ── registry ─────────────────────────────────────────────────────────────── */

const groups: PrefGroup[] = []

export function registerPrefGroup(group: PrefGroup): void {
  if (groups.some((g) => g.name === group.name)) return // idempotent under HMR / double-import
  groups.push(group)
}

export function registeredPrefGroups(): readonly PrefGroup[] {
  return groups
}

/** Test seam. Not used by the shell. */
export function resetPrefsForTest(): void {
  groups.length = 0
  pending = {}
  hydratedFor = null
  applyingRemote = false
  pusher = null
  if (flushTimer !== null) { clearTimeout(flushTimer); flushTimer = null }
  try { localStorage.removeItem(PENDING_KEY) } catch { /* noop */ }
}

/* ── pending queue ────────────────────────────────────────────────────────── */

function loadPending(): Record<string, string> {
  try {
    const raw = localStorage.getItem(PENDING_KEY)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {}
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      // Anything that is not a string is not something the wire could carry.
      // Dropped rather than coerced: a coerced "[object Object]" would be
      // accepted by the server and be wrong forever.
      if (typeof v === 'string') out[k] = v
    }
    return out
  } catch { return {} }
}

let pending: Record<string, string> = loadPending()

function savePending(): void {
  try {
    if (Object.keys(pending).length === 0) localStorage.removeItem(PENDING_KEY)
    else localStorage.setItem(PENDING_KEY, JSON.stringify(pending))
  } catch { /* private mode — the write is lost on reload, but applied this session */ }
}

/** The queued patch, for tests and for the Settings surface that reports it. */
export function pendingPrefPatch(): Readonly<Record<string, string>> {
  return { ...pending }
}

/* ── push ─────────────────────────────────────────────────────────────────── */

let pusher: ((patch: Record<string, string>) => Promise<PushResult>) | null = null
let flushTimer: ReturnType<typeof setTimeout> | null = null
let applyingRemote = false

/** Installed by SyncedPrefsBridge once an authenticated session exists. */
export function setPrefPusher(fn: ((patch: Record<string, string>) => Promise<PushResult>) | null): void {
  pusher = fn
}

/**
 * True while a group's `write` is applying authoritative state.
 *
 * The guard is central rather than per-group because the alternative — every
 * owning module remembering to suppress its own push — is exactly the shape
 * that works until someone adds the sixth module.
 */
export function isApplyingRemotePrefs(): boolean {
  return applyingRemote
}

const FLUSH_DEBOUNCE_MS = 400

function scheduleFlush(): void {
  if (flushTimer !== null) return
  flushTimer = setTimeout(() => { flushTimer = null; void flushPrefs() }, FLUSH_DEBOUNCE_MS)
}

/**
 * Send the pending patch. Safe to call at any time; a no-op with nothing queued
 * or no pusher installed.
 */
export async function flushPrefs(): Promise<PushResult | 'idle'> {
  if (!pusher) return 'idle'
  const patch = { ...pending }
  const keys = Object.keys(patch)
  if (keys.length === 0) return 'idle'
  const result = await pusher(patch)
  if (result === 'ok') {
    // The box now holds these, so later pushes can diff against them.
    for (const [k, v] of Object.entries(patch)) {
      if (v) lastServerView[k] = v
      else delete lastServerView[k]
    }
  }
  if (result === 'ok' || result === 'rejected') {
    // Clear only the entries that are still exactly what was sent. A value
    // rewritten while the request was in flight is a NEWER write and must
    // survive, or a fast second edit would be silently dropped.
    for (const [k, v] of Object.entries(patch)) {
      if (pending[k] === v) delete pending[k]
    }
    savePending()
  }
  return result
}

/**
 * Queue a value for the box. `value` of '' or null DELETES the key.
 *
 * Callers write the local cache themselves (or through a group) — this is only
 * the server half, so that the UI is never waiting on a network round trip.
 */
export function pushPref(bagKey: string, value: string | null): void {
  if (!queueable(bagKey, value ?? '')) return
  pending[bagKey] = value ?? ''
  savePending()
  scheduleFlush()
}

/**
 * Whether a push is worth queueing.
 *
 * Two refusals, and both are about NOT fighting the sync engine on every
 * reload:
 *
 *  • BEFORE the first hydrate nothing is queued at all. Module init and mount
 *    effects re-persist what they just read — `WidgetRail` calls saveLayout on
 *    mount with the layout it loaded a tick earlier — and queueing that would
 *    make every reload a write. Nothing is lost by dropping it: hydration's
 *    adoption step reads the same local values and pushes exactly the ones the
 *    box is missing.
 *
 *  • A value the box already holds is not re-sent.
 */
function queueable(bagKey: string, value: string): boolean {
  if (applyingRemote) return false
  if (hydratedFor === null) return false
  const known = lastServerView[bagKey] ?? ''
  if (known === value && pending[bagKey] === undefined) return false
  return true
}

/**
 * The everyday setter: cache and box, in that order.
 *
 * A value over MAX_SYNCED_VALUE is kept locally and the SERVER COPY IS DELETED
 * rather than left stale. That is deliberate and load-bearing — see the
 * wallpaper case in roadmap/USER-STATE-INVENTORY.md §5. Leaving the old server
 * value in place means the next hydrate applies it over the value the user just
 * chose, silently replacing it. Deleting makes the state honest instead: the
 * OS-wide value is unset and this browser has a local one.
 */
export function setPref(bagKey: string, lsKey: string, value: string | null): void {
  const v = value ?? ''
  prefWriteLocal(lsKey, v)
  pushPref(bagKey, v.length > MAX_SYNCED_VALUE ? '' : v)
  emit()
}

/** Whether `setPref` would have to hold this value back from the box. */
export function exceedsSyncLimit(value: string | null | undefined): boolean {
  return typeof value === 'string' && value.length > MAX_SYNCED_VALUE
}

/**
 * Re-read a group's local state and queue the difference.
 *
 * For owners that persist through their own module (desktop/store.ts,
 * widgets/layout.ts) rather than through `setPref`. Keys the group no longer
 * has a value for are queued as deletions, so removing the last widget removes
 * the rail server-side instead of leaving the old placements to come back.
 */
export function pushPrefGroup(name: string): void {
  if (applyingRemote || hydratedFor === null) return
  const group = groups.find((g) => g.name === name)
  if (!group) return
  const local = group.read()
  const seen = new Set<string>()
  let queued = false
  for (const [k, v] of Object.entries(local)) {
    seen.add(k)
    const next = v.length > MAX_SYNCED_VALUE ? '' : v
    if (!queueable(k, next)) continue
    pending[k] = next
    queued = true
  }
  // Anything this group owned in the box's view and no longer has a value for.
  // Queued as a DELETION — without it, removing your last widget would leave
  // the old placements on the box, and the next load would bring them back.
  for (const k of Object.keys(lastServerView)) {
    if (seen.has(k) || !group.owns(k)) continue
    if (!queueable(k, '')) continue
    pending[k] = ''
    queued = true
  }
  if (!queued) return
  savePending()
  scheduleFlush()
}

/* ── hydrate ──────────────────────────────────────────────────────────────── */

let hydratedFor: string | null = null
let lastServerView: Record<string, string> = {}

export function prefsHydratedFor(): string | null {
  return hydratedFor
}

/**
 * Apply the box's view of this user's preferences, and return the patch that
 * must go back up (adoption + anything queued while offline).
 *
 * `serverBag` is `Profile.Settings` plus the reserved THEME_BAG_KEY. The caller
 * sends the returned patch; it is returned rather than sent here so hydration
 * stays synchronous and testable without a network.
 */
export function hydratePrefs(userID: string, serverBag: Record<string, string>): Record<string, string> {
  const first = hydratedFor !== userID
  hydratedFor = userID
  lastServerView = { ...serverBag }

  const adoption: Record<string, string> = {}

  applyingRemote = true
  try {
    for (const group of groups) {
      const local = group.read()
      const effective: Record<string, string> = {}

      for (const [k, v] of Object.entries(serverBag)) {
        if (group.owns(k) && v) effective[k] = v
      }

      if (first) {
        // Adoption: the box has never heard of this key and this browser has a
        // real value for it. Local wins ONCE.
        for (const [k, v] of Object.entries(local)) {
          if (!v || effective[k] !== undefined) continue
          effective[k] = v
          if (v.length <= MAX_SYNCED_VALUE) adoption[k] = v
        }
      }

      // A write made while the box was unreachable is NEWER than anything the
      // server holds, so it goes on last.
      for (const [k, v] of Object.entries(pending)) {
        if (!group.owns(k)) continue
        if (v) effective[k] = v
        else delete effective[k]
      }

      // A local value too large for the bag is re-asserted last, and this is
      // load-bearing rather than tidy.
      //
      // An uploaded wallpaper is a multi-megabyte `data:` URI. setPref keeps it
      // locally and DELETES the box's copy, so the state stays honest — but
      // that deletion sits in the pending queue, and the loop above would
      // replay it as a local clear and destroy the wallpaper the user chose
      // seconds ago. The box cannot hold a value this size, so the box cannot
      // be authoritative for it: the local copy is the only copy there is.
      for (const [k, v] of Object.entries(local)) {
        if (v.length > MAX_SYNCED_VALUE) effective[k] = v
      }

      group.write(effective)
    }
  } finally {
    applyingRemote = false
  }

  emit()

  const patch: Record<string, string> = { ...adoption, ...pending }
  // Merge adoption into the outbox so a failed first push is retried rather
  // than lost — after which step 1 no longer produces it.
  if (Object.keys(adoption).length > 0) {
    pending = { ...adoption, ...pending }
    savePending()
  }
  return patch
}
