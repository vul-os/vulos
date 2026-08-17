/**
 * SyncedPrefsBridge — the only place the preference engine meets the session.
 *
 * Renders nothing. It exists so that core/syncedPrefs.ts can stay a plain,
 * dependency-free module: it does not know what a profile is, how it is
 * fetched, or that React exists.
 *
 * Two jobs:
 *
 *  1. Install the pusher — how a queued patch reaches the box.
 *  2. Hydrate whenever the box's view of the profile changes.
 *
 * # Why hydration is not once-per-login
 *
 * It runs on every `profile` change, because that is what gives a second box's
 * edit a way in: any refetch converges this shell. It is safe to re-run because
 * `hydratePrefs` is idempotent — after the first pass the local values already
 * equal the server's, and its ADOPTION step is gated to the first hydrate for a
 * user, so a preference deleted on box A cannot be resurrected by box B's stale
 * cache.
 *
 * It also cannot loop: a group's `write` applies to the local cache only, and
 * every push path is suppressed while that runs.
 */

import { useEffect, useRef, type ReactNode } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { registerAllPrefGroups } from './prefGroups'
import { THEME_BAG_KEY, flushPrefs, hydratePrefs, setPrefPusher, type PushResult } from './syncedPrefs'

registerAllPrefGroups()

interface SyncedPrefsBridgeProps {
  children?: ReactNode
}

export default function SyncedPrefsBridge({ children }: SyncedPrefsBridgeProps) {
  const { user, profile, updateProfile } = useAuth()

  // The pusher is read through a ref by a callback that outlives any one
  // render, so it must not be captured stale.
  //
  // The assignment lives in an effect, not in the render body. Writing a ref
  // during render is unsafe under concurrent rendering: React may start a
  // render, throw it away, and render again — but the ref mutation from the
  // discarded attempt survives, so the callback can end up holding an
  // updateProfile from a render that never committed. `useRef(updateProfile)`
  // already seeds the current value on first render, and this effect is
  // declared BEFORE the setPrefPusher effect below, so effect ordering
  // guarantees the ref is fresh before any push can be registered, let alone
  // invoked.
  const updateRef = useRef(updateProfile)
  useEffect(() => {
    updateRef.current = updateProfile
  }, [updateProfile])

  useEffect(() => {
    setPrefPusher(async (patch): Promise<PushResult> => {
      // THEME_BAG_KEY is not a Settings entry — it is the replicated
      // `profiles.theme` COLUMN, routed through the same queue so that theme
      // gets the same adoption, offline and retry behaviour as everything
      // else. Split back out here, at the wire.
      const settings: Record<string, string> = {}
      let theme: string | undefined
      for (const [k, v] of Object.entries(patch)) {
        if (k === THEME_BAG_KEY) theme = v
        else settings[k] = v
      }
      const body: Record<string, unknown> = {}
      if (Object.keys(settings).length > 0) body.settings = settings
      // An empty theme means "unset", which the column expresses as its
      // documented default rather than as an empty string reaching the shell.
      if (theme !== undefined) body.theme = theme || 'auto'
      if (Object.keys(body).length === 0) return 'ok'
      return updateRef.current(body)
    })
    return () => { setPrefPusher(null) }
  }, [])

  useEffect(() => {
    if (!user || !profile) return
    // AuthProfile's fields are `unknown` by design (the /api/auth/me payload is
    // never validated in that provider), so every one is narrowed here rather
    // than cast. A malformed field degrades to "the box holds nothing for this
    // key", which the engine already has a meaning for.
    const userID = user.id
    if (typeof userID !== 'string' || !userID) return

    // An OFFLINE session is not the box speaking. AuthProvider sets a bare
    // { display_name, offline: true } profile when a user unlocks without a
    // network; hydrating from it would read a payload that mentions nothing as
    // a box that holds nothing, and unlocking your own machine offline would
    // erase every preference you have.
    if (profile.offline === true) return

    const bag: Record<string, string> = {}
    const settings: unknown = profile.settings
    const settingsPresent = typeof settings === 'object' && settings !== null && !Array.isArray(settings)
    if (settingsPresent) {
      for (const [k, v] of Object.entries(settings as Record<string, unknown>)) {
        if (typeof v === 'string' && v) bag[k] = v
      }
    }
    const theme: unknown = profile.theme
    if (typeof theme === 'string' && theme) bag[THEME_BAG_KEY] = theme

    // `settings` absent is NOT an empty bag — Go's `omitempty` omits an empty
    // map, so the wire cannot tell them apart, and only one of the two readings
    // is safe. See HydrateOptions.authoritative.
    hydratePrefs(userID, bag, { authoritative: settingsPresent })
    // Adoption (this box's existing localStorage values, which the box has
    // never seen) and anything queued while offline are both in the outbox by
    // now. Sending is fire-and-forget: a failure leaves the queue intact and
    // the next hydrate retries it.
    void flushPrefs()
  }, [user, profile])

  return <>{children}</>
}
