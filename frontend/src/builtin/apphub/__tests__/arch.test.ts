import { describe, expect, it, vi, afterEach } from 'vitest'
import { toArchAvailability, fetchBoxArch } from '../arch'

/**
 * arch.ts is now a NARROWER, and this file changed shape with it.
 *
 * ── What it used to test, and why those tests are gone rather than moved ────
 *
 * It tested `normalizeArch`, `archCompat`, `isUniversalArch` and
 * `requiredArches`: an alias table folding x86_64 onto amd64, a comparison
 * against the box's architecture, and a display canonicaliser. Every one of them
 * passed. Every one of them was the SECOND implementation of a policy
 * services/appnet/arch.go already owned, and the second implementation is the
 * one that drifts — it agreed with the box on the day it was written, which is
 * the only day anyone checks.
 *
 * Those assertions did not vanish. They moved to the machine that can answer
 * them: `TestEvaluateArch_SpellsOneArchitectureScheme`,
 * `TestEvaluateArch_CardBadgeIsShortAndNamesTheFact` and
 * `TestListEntries_CarriesTheServersAnswer` in
 * backend/services/appnet, where the fold is applied to the same data the
 * installer uses rather than to a copy of it.
 *
 * What is left on this side is the seam, and its failure modes are different in
 * kind: not "does the comparison match" but "what does this UI do when the box
 * says something it does not understand, or says nothing at all".
 */

afterEach(() => vi.unstubAllGlobals())

/** A well-formed entry as GET /api/store/registry sends it. */
function entry(availability: Record<string, unknown>) {
  return { id: 'lutris', name: 'Lutris', availability }
}

const FULL = {
  state: 'unavailable',
  installable: false,
  requires_emulation: false,
  badge: 'Not available on this box',
  card_badge: 'Needs amd64',
  detail: 'Lutris ships for amd64 only, and this box is arm64.',
  box_arch: 'arm64',
  undeclared: false,
  needs: ['amd64'],
  signature: 'signed',
}

describe('toArchAvailability', () => {
  it('carries every field the hub renders, under the names the box sends', () => {
    const av = toArchAvailability(entry(FULL))
    expect(av).not.toBeNull()
    expect(av).toEqual({
      state: 'unavailable',
      installable: false,
      requiresEmulation: false,
      badge: 'Not available on this box',
      cardBadge: 'Needs amd64',
      detail: 'Lutris ships for amd64 only, and this box is arm64.',
      boxArch: 'arm64',
      undeclared: false,
      needs: ['amd64'],
      signature: 'signed',
    })
  })

  it('carries the emulated rung, which is installable AND badged', () => {
    // Rung 3's shape is the one a boolean cannot hold: the app CAN be installed
    // and the user still has to be told what they are getting. A narrower that
    // treated "has a badge" as "cannot install" would hide the button.
    const av = toArchAvailability(entry({
      ...FULL,
      state: 'emulated',
      installable: true,
      requires_emulation: true,
      badge: 'Runs emulated',
      card_badge: 'Runs emulated',
      detail: 'Lutris will run on this arm64 box through emulation — noticeably slower.',
    }))
    expect(av?.state).toBe('emulated')
    expect(av?.installable).toBe(true)
    expect(av?.requiresEmulation).toBe(true)
    expect(av?.cardBadge).toBe('Runs emulated')
  })

  it('carries the sibling-instance rung', () => {
    const av = toArchAvailability(entry({
      ...FULL, state: 'other-instance', badge: 'On your other instance', card_badge: 'On studio-box',
    }))
    expect(av?.state).toBe('other-instance')
    expect(av?.cardBadge).toBe('On studio-box')
  })

  // ── The three ways this can be handed nothing usable ──────────────────────
  //
  // All three answer NULL, and null is not a verdict. The hub renders it as no
  // compatibility claim at all: the app is offered, unbadged. Both foldings are
  // worse, and they are worse in opposite directions — see arch.ts.

  it('answers null when the box did not send an availability at all', () => {
    // Every backend before this field existed. Folding this to "unavailable"
    // marks the entire catalogue unrunnable on all of them.
    expect(toArchAvailability({ id: 'lutris', name: 'Lutris' })).toBeNull()
    expect(toArchAvailability({ id: 'lutris', availability: null })).toBeNull()
    expect(toArchAvailability(null)).toBeNull()
    expect(toArchAvailability('lutris')).toBeNull()
  })

  it('answers null for a rung this build has never heard of', () => {
    // A future state — say a distribution-sourced rung 2 — must not be silently
    // rendered as one of the four this build knows, least of all as the one that
    // takes the install button away.
    for (const state of ['distro-sourced', 'NATIVE', '', 'yes', 'no']) {
      expect(toArchAvailability(entry({ ...FULL, state })), `state ${JSON.stringify(state)}`).toBeNull()
    }
    // The control: the four it does know are not rejected.
    for (const state of ['native', 'emulated', 'other-instance', 'unavailable']) {
      expect(toArchAvailability(entry({ ...FULL, state }))?.state, state).toBe(state)
    }
  })

  it('does not let a malformed field become an optimistic claim', () => {
    // `installable` decides whether an Install button is drawn. Anything that is
    // not literally true is false — a truthy string or a 1 from a hand-written
    // fixture must not offer an install the box refused.
    for (const installable of ['true', 1, {}, [], null, undefined]) {
      expect(toArchAvailability(entry({ ...FULL, installable }))?.installable, String(installable)).toBe(false)
    }
    expect(toArchAvailability(entry({ ...FULL, installable: true }))?.installable).toBe(true)
  })

  it('keeps missing strings as empty strings rather than rendering "undefined"', () => {
    const av = toArchAvailability({ id: 'x', availability: { state: 'native' } })
    expect(av).toEqual({
      state: 'native', installable: false, requiresEmulation: false,
      badge: '', cardBadge: '', detail: '', boxArch: '', undeclared: false, needs: [],
      // A box too old to send a signature verdict is not asserting that its
      // entries are signed, so '' must NOT narrow to 'signed'. It is the value
      // the hub then frames with the stricter of the two treatments.
      signature: '',
    })
  })

  it('carries the signature verdict, and never invents one', () => {
    // The hub compares this in exactly one place — whether a refusal is framed
    // as a hold on the publisher's ceremony or as a refusal this box stands
    // behind — so a value invented here would restyle a tampered entry as a
    // pending one.
    for (const signature of ['signed', 'unsigned', 'untrusted', 'uncheckable']) {
      expect(toArchAvailability(entry({ ...FULL, signature }))?.signature, signature).toBe(signature)
    }
    // Anything that is not a string is '', not a guess. A state string this
    // build has never heard of is passed straight through, because the ONE
    // comparison that reads it tests for 'unsigned' and everything else falls to
    // the stricter framing on its own.
    for (const signature of [7, null, undefined, {}, true]) {
      expect(toArchAvailability(entry({ ...FULL, signature }))?.signature, String(signature)).toBe('')
    }
    expect(toArchAvailability(entry({ ...FULL, signature: 'rung-9' }))?.signature).toBe('rung-9')
  })

  it('drops non-string members of `needs` instead of printing them', () => {
    const av = toArchAvailability(entry({ ...FULL, needs: ['amd64', 7, null, 'i386'] }))
    expect(av?.needs).toEqual(['amd64', 'i386'])
    expect(toArchAvailability(entry({ ...FULL, needs: 'amd64' }))?.needs).toEqual([])
  })

  it('does NOT fold spellings, because folding here is the thing that was deleted', () => {
    // If the box ever sends `x86_64` that is a defect ON THE BOX, and it has to
    // be visible as one. A fold here would hide it and re-create the second
    // implementation — this time invisibly, since nothing would disagree.
    const av = toArchAvailability(entry({ ...FULL, needs: ['x86_64'], card_badge: 'Needs x86_64' }))
    expect(av?.needs).toEqual(['x86_64'])
    expect(av?.cardBadge).toBe('Needs x86_64')
  })
})

describe('fetchBoxArch', () => {
  it('reads the box architecture from the dedicated endpoint', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ arch: 'arm64', supported: ['arm64'] }))))
    expect(await fetchBoxArch()).toBe('arm64')
  })

  it('returns the value AS SENT, because it is printed and never compared', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ arch: ' x86_64 ' }))))
    expect(await fetchBoxArch()).toBe('x86_64')
  })

  it('says nothing rather than guessing when the endpoint is absent or broken', async () => {
    for (const res of [
      () => new Response('{}', { status: 200 }),
      () => new Response('not json', { status: 200 }),
      () => new Response('', { status: 404 }),
      () => { throw new Error('offline') },
    ]) {
      vi.stubGlobal('fetch', vi.fn(async () => res()))
      expect(await fetchBoxArch()).toBeNull()
    }
  })
})
