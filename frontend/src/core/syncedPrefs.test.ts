/**
 * syncedPrefs.test.ts — the reconciliation rules, asserted as behaviour.
 *
 * These are the rules a reader cannot check by reading, because every one of
 * them is about a SEQUENCE: what the second load does differently from the
 * first, what a write made while the box was unreachable does to the value the
 * box already held, what a value too large to send does to the value the user
 * just chose.
 *
 * Each test states the user-visible failure it exists to prevent, because
 * "adoption is gated to the first hydrate" is a mechanism and "your theme comes
 * back after you delete it on the other box" is a defect.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  MAX_SYNCED_VALUE, flushPrefs, hydratePrefs, pendingPrefPatch, prefRead,
  pushPrefGroup, registerPrefGroup, resetPrefsForTest, setPref, setPrefPusher,
  type PushResult,
} from './syncedPrefs'

const BAG = 'shell.test.value'
const LS = 'vulos.test.value'
const OTHER_BAG = 'shell.test.other'
const OTHER_LS = 'vulos.test.other'

/** A group over two plain localStorage-backed keys — the common shape. */
function registerTestGroup(): void {
  const map: Record<string, string> = { [BAG]: LS, [OTHER_BAG]: OTHER_LS }
  registerPrefGroup({
    name: 'test',
    owns: (k) => k in map,
    read: () => {
      const out: Record<string, string> = {}
      for (const [bag, ls] of Object.entries(map)) {
        const v = prefRead(ls)
        if (v) out[bag] = v
      }
      return out
    },
    write: (values) => {
      for (const [bag, ls] of Object.entries(map)) {
        const v = values[bag] ?? ''
        if (v) localStorage.setItem(ls, v)
        else localStorage.removeItem(ls)
      }
    },
  })
}

/** A pusher that records every patch and answers with a scripted result. */
function recordingPusher(result: PushResult = 'ok') {
  const sent: Record<string, string>[] = []
  const fn = vi.fn(async (patch: Record<string, string>): Promise<PushResult> => {
    sent.push({ ...patch })
    return result
  })
  setPrefPusher(fn)
  return { sent, fn }
}

beforeEach(() => {
  localStorage.clear()
  resetPrefsForTest()
  registerTestGroup()
})

describe('adoption — the one-time migration off localStorage', () => {
  it('adopts a value this browser already has and the box has never seen', () => {
    // The box a user is on TODAY has a real wallpaper, theme and set of pins in
    // localStorage. If the first hydrate simply applied the (empty) server
    // state, the migration itself would wipe everything it was written to save.
    localStorage.setItem(LS, 'dark')

    const patch = hydratePrefs('user-1', {})

    expect(patch).toEqual({ [BAG]: 'dark' })
    expect(prefRead(LS)).toBe('dark')
  })

  it('lets the box win over the cache even on the very first hydrate', () => {
    localStorage.setItem(LS, 'stale-local')

    const patch = hydratePrefs('user-1', { [BAG]: 'from-the-box' })

    expect(prefRead(LS)).toBe('from-the-box')
    expect(patch).toEqual({}) // nothing to adopt: the box already had a value
  })

  it('does not re-adopt on the second hydrate — a deleted preference stays deleted', async () => {
    // THE defect this gate exists for. Box A deletes a preference. Box B still
    // has it cached. Without the first-hydrate gate, B's next load "adopts" its
    // stale copy and pushes it back up, and the preference the user deleted
    // reappears on both boxes — forever, because the loop is stable.
    localStorage.setItem(LS, 'dark')
    recordingPusher('ok')
    hydratePrefs('user-1', {}) // adopt…
    await flushPrefs()         // …and the box acknowledges it
    localStorage.setItem(LS, 'dark') // the cache still holds it

    const second = hydratePrefs('user-1', {}) // box now says: no such key

    expect(second[BAG]).toBeUndefined()
    expect(prefRead(LS)).toBe('') // cleared, because absent means unset
  })

  it('does NOT drop an adoption the box never acknowledged', () => {
    // The mirror image, and the reason the test above has to flush. An
    // adoption that was queued and never delivered — first launch on a box
    // that is offline — is still owed. Treating the second hydrate's silence
    // as "the box says unset" would delete the user's existing wallpaper and
    // theme as the migration's opening act.
    localStorage.setItem(LS, 'dark')
    setPrefPusher(async () => 'unreachable')
    hydratePrefs('user-1', {})

    hydratePrefs('user-1', {})

    expect(prefRead(LS)).toBe('dark')
    expect(pendingPrefPatch()).toEqual({ [BAG]: 'dark' })
  })

  it('is idempotent once the box has the value — a reload sends nothing', async () => {
    localStorage.setItem(LS, 'dark')
    const { sent } = recordingPusher('ok')

    hydratePrefs('user-1', {})
    await flushPrefs()
    expect(sent).toHaveLength(1)

    // Second load: the box now holds what was adopted.
    hydratePrefs('user-1', { [BAG]: 'dark' })
    await flushPrefs()

    expect(sent).toHaveLength(1) // still one — hydration did not write again
    expect(pendingPrefPatch()).toEqual({})
  })
})

describe('a payload that says nothing is not a payload that says "unset"', () => {
  it('does not clear a preference when the reply carries no settings at all', () => {
    // Caught by an E2E run, and it is the worst regression this pass produced.
    // Applying a desktop preset pushed the layout; the reply carried no
    // settings object; hydration read that silence as "the box holds nothing"
    // and reset the desktop to stock a moment after the user chose it.
    //
    // Go's `json:"settings,omitempty"` omits an EMPTY map, so "the box has no
    // preferences" and "this response did not include them" are the same bytes.
    // Only one of the two readings is safe.
    hydratePrefs('user-1', { [BAG]: 'dark' })
    expect(prefRead(LS)).toBe('dark')

    hydratePrefs('user-1', {}, { authoritative: false })

    expect(prefRead(LS)).toBe('dark')
  })

  it('still clears when the payload DID carry settings and simply lacks the key', () => {
    // The other half. Reset-to-stock on the other box must reach this one, or
    // a deletion is a change that never propagates.
    hydratePrefs('user-1', { [BAG]: 'dark' })
    hydratePrefs('user-1', { [OTHER_BAG]: 'x' }) // authoritative by default
    expect(prefRead(LS)).toBe('')
  })

  it('does not push from a non-authoritative payload', () => {
    // Otherwise every offline unlock and every partial reply would queue the
    // whole local state as if the box were missing it.
    localStorage.setItem(LS, 'dark')
    hydratePrefs('user-1', { [BAG]: 'dark' })  // first hydrate, box already has it
    localStorage.setItem(OTHER_LS, 'later')

    const patch = hydratePrefs('user-1', {}, { authoritative: false })

    expect(patch).toEqual({})
    expect(prefRead(OTHER_LS)).toBe('later') // kept, just not pushed
  })
})

describe('offline writes', () => {
  it('applies locally at once and keeps the patch queued when the box is unreachable', async () => {
    hydratePrefs('user-1', { [BAG]: 'dark' })
    recordingPusher('unreachable')

    setPref(BAG, LS, 'light')
    await flushPrefs()

    expect(prefRead(LS)).toBe('light')          // the UI was correct immediately
    expect(pendingPrefPatch()).toEqual({ [BAG]: 'light' }) // and it is still owed
  })

  it('replays the queued write OVER the box on the next hydrate', () => {
    // Change your theme on a plane; land; the box still holds the old value and
    // answers first. The later write must win, or the setting silently reverts.
    hydratePrefs('user-1', { [BAG]: 'dark' })
    setPrefPusher(async () => 'unreachable')
    setPref(BAG, LS, 'light')

    hydratePrefs('user-1', { [BAG]: 'dark' })

    expect(prefRead(LS)).toBe('light')
  })

  it('survives a reload, because the queue is mirrored to storage', () => {
    hydratePrefs('user-1', { [BAG]: 'dark' })
    setPrefPusher(async () => 'unreachable')
    setPref(BAG, LS, 'light')

    // A reload is a fresh module state reading the same storage.
    expect(localStorage.getItem('vulos.prefs.pending.v1')).toContain('light')
  })

  it('retries an unreachable box but drops a patch the box REJECTED', async () => {
    // A 4xx is a decision about this patch's content and will be identical
    // every time. Retrying it forever would block every later write behind it.
    hydratePrefs('user-1', {})

    setPrefPusher(async () => 'unreachable')
    setPref(BAG, LS, 'a')
    await flushPrefs()
    expect(pendingPrefPatch()).toEqual({ [BAG]: 'a' })

    setPrefPusher(async () => 'rejected')
    await flushPrefs()
    expect(pendingPrefPatch()).toEqual({})
  })

  it('keeps a write made while an older one was in flight', async () => {
    hydratePrefs('user-1', {})
    const gate: { release: (() => void) | null } = { release: null }
    setPrefPusher(async () => {
      await new Promise<void>((r) => { gate.release = r })
      return 'ok'
    })

    setPref(BAG, LS, 'first')
    const inFlight = flushPrefs()
    setPref(BAG, LS, 'second') // the user edited again before the reply landed
    gate.release?.()
    await inFlight

    expect(pendingPrefPatch()).toEqual({ [BAG]: 'second' })
  })
})

describe('values the box cannot hold', () => {
  const huge = 'x'.repeat(MAX_SYNCED_VALUE + 1)

  it('keeps the value locally and deletes the box copy rather than leaving it stale', () => {
    hydratePrefs('user-1', { [BAG]: 'small-old-value' })

    setPref(BAG, LS, huge)

    expect(prefRead(LS)).toBe(huge)
    expect(pendingPrefPatch()).toEqual({ [BAG]: '' }) // '' deletes
  })

  it('does not destroy the oversized value on the next hydrate', () => {
    // The trap: the deletion above sits in the pending queue, and the queue is
    // replayed over the server's values on hydrate. Replayed naively it clears
    // the local key too — and the wallpaper the user uploaded five seconds ago
    // vanishes on reload. The box cannot hold a value this size, so the box
    // cannot be authoritative for it.
    hydratePrefs('user-1', { [BAG]: 'small-old-value' })
    setPrefPusher(async () => 'unreachable')
    setPref(BAG, LS, huge)

    hydratePrefs('user-1', { [BAG]: 'small-old-value' })

    expect(prefRead(LS)).toBe(huge)
  })

  it('never offers an oversized value for adoption', () => {
    localStorage.setItem(LS, huge)
    const patch = hydratePrefs('user-1', {})
    expect(patch[BAG]).toBeUndefined()
    expect(prefRead(LS)).toBe(huge) // still applied locally
  })
})

describe('not fighting the sync engine', () => {
  it('queues nothing before the first hydrate', () => {
    // Module init and mount effects re-persist what they just read —
    // WidgetRail calls saveLayout on mount with the layout it loaded a tick
    // earlier. Queueing that would make every single reload a write to the
    // box. Nothing is lost: adoption reads the same values a moment later.
    setPref(BAG, LS, 'dark')

    expect(pendingPrefPatch()).toEqual({})
    expect(prefRead(LS)).toBe('dark') // the local half still happened
  })

  it('does not re-send a value the box already holds', () => {
    hydratePrefs('user-1', { [BAG]: 'dark' })
    localStorage.setItem(LS, 'dark')

    pushPrefGroup('test')

    expect(pendingPrefPatch()).toEqual({})
  })

  it('does not push while applying the box\'s own state, even when write() NORMALISES', () => {
    // A group whose write() re-enters the store it owns (desktop/store's
    // commit() does exactly this) would otherwise echo every arriving value
    // straight back up as a fresh write.
    //
    // The normalisation is the point, and the first version of this test did
    // not have it: without it the echoed value EQUALS what the box just sent,
    // so the "already holds this" diff refuses the push and the test passes
    // whether or not the applyingRemote guard exists. It did exactly that —
    // deleting the guard left this file green. A lossy write() is the real
    // shape (validateLayout normalises; reconcileInstance clamps), and it is
    // the shape that makes the echo differ and the guard load-bearing.
    registerPrefGroup({
      name: 'reentrant',
      owns: (k) => k === 'shell.test.reentrant',
      read: () => ({ 'shell.test.reentrant': prefRead('vulos.test.reentrant') }),
      write: (values) => {
        const normalised = (values['shell.test.reentrant'] ?? '').toLowerCase()
        localStorage.setItem('vulos.test.reentrant', normalised)
        pushPrefGroup('reentrant') // the echo
      },
    })

    hydratePrefs('user-1', { 'shell.test.reentrant': 'FROM-BOX' })

    expect(prefRead('vulos.test.reentrant')).toBe('from-box') // write() did normalise
    expect(pendingPrefPatch()['shell.test.reentrant']).toBeUndefined()
  })
})

describe('a command issued before the box answered', () => {
  it('wins over what the box turns out to be holding', () => {
    // The ?desktop-layout=stock escape hatch, in miniature. It runs at module
    // load — before any profile can arrive — for the case where the keyboard is
    // unavailable and the chosen layout has made the revert control hard to
    // find. Ordinary pre-hydrate writes are dropped as mount-effect echoes;
    // dropping THIS one meant hydration put the layout the user was escaping
    // straight back on the screen, and the last-resort revert became the one
    // revert that could not work. An E2E run caught it.
    localStorage.setItem(LS, 'side-dock')

    // The user's command: clear it, before anything has hydrated.
    localStorage.removeItem(LS)
    pushPrefGroup('test', { deliberate: true })
    expect(pendingPrefPatch(), 'nothing can be queued before hydrate').toEqual({})

    // The box answers, still holding the layout that was just escaped.
    hydratePrefs('user-1', { [BAG]: 'side-dock' })

    expect(prefRead(LS), 'the box overrode a deliberate revert').toBe('')
    expect(pendingPrefPatch(), 'the revert never reached the box').toEqual({ [BAG]: '' })
  })

  it('does not remember an ordinary pre-hydrate write', () => {
    // The control. Without it the test above would pass on a build that simply
    // stopped dropping pre-hydrate pushes altogether, which is the behaviour
    // that makes every reload a write.
    localStorage.setItem(LS, 'side-dock')
    pushPrefGroup('test') // not deliberate

    hydratePrefs('user-1', { [BAG]: 'from-the-box' })

    expect(prefRead(LS)).toBe('from-the-box')
  })
})

describe('removal', () => {
  it('queues a DELETION for a key the group no longer has a value for', () => {
    // Remove your last widget. Without an explicit deletion the box keeps the
    // old placements and the next load brings them all back.
    hydratePrefs('user-1', { [BAG]: 'dark', [OTHER_BAG]: 'x' })
    localStorage.removeItem(OTHER_LS)

    pushPrefGroup('test')

    expect(pendingPrefPatch()).toEqual({ [OTHER_BAG]: '' })
  })
})
