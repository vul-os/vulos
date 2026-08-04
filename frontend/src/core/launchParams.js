// launchParams.js — tiny, race-free deep-link seam for BUILTIN apps.
//
// Web apps get deep-linked by augmenting their iframe URL (?compose=1, #…).
// Builtin React apps have no URL, so the ⌘K palette / any launcher stashes the
// intended query string here, keyed by app id; the builtin's factory reads +
// clears it on mount (see shell/builtinApps.jsx) and hands it to the component.
//
// This mirrors settingsNav.js (the Settings section seam) but generalised to any
// builtin and any query string, so a new builtin can honour a deep-link without
// threading props through the generic openWindow/launchApp path.
//
// The value is a raw query string (e.g. "action=new"); the component parses it
// with URLSearchParams. Consumed exactly once — a later plain launch of the same
// builtin sees no pending query.

const pending = new Map()

/** Stash a pending launch query (a raw query string) for a builtin app id. */
export function setPendingLaunchQuery(appId, query) {
  if (appId && query) pending.set(appId, String(query))
}

/** Read and clear the pending launch query for an app id (call once, on mount). */
export function consumePendingLaunchQuery(appId) {
  const q = pending.get(appId)
  pending.delete(appId)
  return q || ''
}
