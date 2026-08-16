import { describe, it, expect } from 'vitest'
import { describeResponding } from '../ActivityMonitor'
import { toResponsiveness, type Responsiveness } from '../api'

/**
 * What the five responsiveness answers SAY to a user.
 *
 * This file exists because the wording is the half of the feature that can be
 * wrong while every pixel renders correctly, every type checks and every
 * contrast gate passes. A badge that reads "Not responding" on a row whose
 * DISPLAY stalled is a confident accusation against an app that may be
 * perfectly healthy — and the user's obvious response, force-quitting it,
 * destroys work and does not unfreeze the picture. After that the badge has
 * taught them to distrust it, which is worse than having shipped no badge.
 *
 * Every answer is built through toResponsiveness, so these assert the real
 * path from a server payload to the words on screen rather than hand-made
 * objects that could drift from what the narrower actually produces.
 */

const say = (x: unknown) => describeResponding(toResponsiveness(x))

describe('the app-scoped answers', () => {
  it('says Responding for an app whose own event loop answered', () => {
    const p = say({ status: 'responding', method: 'x11_ping', detail: '_NET_WM_PING echo in 8ms' })
    expect(p.label).toBe('Responding')
    expect(p.tone).toBe('var(--status-success)')
    expect(p.note).toBe('')
  })

  it('says Not responding — and blames nothing else — only when the app itself stayed silent', () => {
    const p = say({
      status: 'not_responding', method: 'x11_ping',
      detail: 'no _NET_WM_PING echo from 1 window(s) in 5s, and the X server still answers',
    })
    expect(p.label).toBe('Not responding')
    expect(p.tone).toBe('var(--status-danger)')
    // No caveat: this is the ONE state where the app is the subject.
    expect(p.note).toBe('')
  })
})

describe('the display-scoped answer', () => {
  const stalled = say({
    status: 'display_not_responding', method: 'x11_ping',
    detail: 'no answer from the X server at /tmp/.X11-unix/X10', checked_ms: 3005,
  })

  // The label's SUBJECT is the thing that must change. "Not responding" on a
  // row means that app is stuck; this row means the session is.
  it('names the session, not the app', () => {
    expect(stalled.label).toBe('Session not responding')
    expect(stalled.label).not.toBe('Not responding')
    expect(stalled.label.toLowerCase()).toContain('session')
  })

  it('says in visible words that this app may be fine', () => {
    expect(stalled.note).not.toBe('')
    expect(stalled.note.toLowerCase()).toContain('may be fine')
    // Visible, not hover-only: an explanation the user has to discover is an
    // explanation most users never see.
    expect(stalled.note.length).toBeGreaterThan(20)
  })

  // Danger is the app's colour. Wearing it would make the badge read as a
  // verdict on the row it sits on no matter what the words say.
  it('does not wear the colour reserved for a broken app', () => {
    expect(stalled.tone).not.toBe('var(--status-danger)')
    expect(stalled.tone).toBe('var(--status-warning)')
  })

  // The caveat is the load-bearing sentence, so it is drawn in a token derived
  // for TEXT. --status-warning is a fill: it measures 3.0:1 against the light
  // theme's surfaces and would fail AA while looking fine on dark.
  it('draws its caveat in a text token, never a fill token', () => {
    expect(stalled.noteTone).toBe('var(--status-warning-text)')
    expect(stalled.noteTone).not.toBe('var(--status-warning)')
  })

  it('is reachable from a real backend payload rather than only from a hand-made one', () => {
    const r: Responsiveness = toResponsiveness({ status: 'display_not_responding' })
    expect(r.status).toBe('display_not_responding')
    expect(describeResponding(r).label).toBe('Session not responding')
  })
})

describe('the two kinds of unknown', () => {
  // proctl reports unknown WITH A WINDOW COUNT when the display answered, its
  // windows were enumerated, and none implement _NET_WM_PING. "Nobody to ask"
  // is not "asked and got silence", and it is also not the same as having no
  // mechanism at all — so the two unknowns do not get the same sentence.
  const nobodyToAsk = say({
    status: 'unknown', method: 'x11_ping',
    detail: 'no window on this display implements _NET_WM_PING (4 windows examined); the app cannot be asked, which is not the same as frozen',
  })
  const noMechanism = say({
    status: 'unknown', method: 'none',
    detail: 'this session runs on the Wayland compositor (cage), where a liveness ping is the compositor\'s to send',
  })

  it('never lets either become a verdict', () => {
    for (const p of [nobodyToAsk, noMechanism]) {
      expect(p.label).toBe('Cannot tell')
      expect(p.tone).not.toBe('var(--status-danger)')
      expect(p.tone).not.toBe('var(--status-success)')
    }
  })

  it('does not flatten "nobody to ask" into "no way to ask at all"', () => {
    expect(nobodyToAsk.note).not.toBe(noMechanism.note)
    expect(nobodyToAsk.note).not.toBe('')
    expect(nobodyToAsk.note.toLowerCase()).toContain('no way to be asked')
  })
})

describe('a status from a newer backend', () => {
  const future = say({ status: 'display_wedged', method: 'x11_ping', detail: 'something new' })

  it('renders as an absence of information, not as either verdict', () => {
    expect(future.label).toBe('Cannot tell')
    expect(future.tone).not.toBe('var(--status-danger)')
    expect(future.tone).not.toBe('var(--status-success)')
  })

  it('says the box knows something this build does not, instead of pretending it did not happen', () => {
    expect(future.note.toLowerCase()).toContain('does not understand')
    // And it must not borrow the "nobody to ask" sentence, which would be a
    // specific claim about a situation nobody here understands.
    expect(future.note.toLowerCase()).not.toContain('no way to be asked')
  })

  it('does not crash on a payload that is not an object at all', () => {
    expect(describeResponding(toResponsiveness(null)).label).toBe('Cannot tell')
    expect(describeResponding(toResponsiveness([])).label).toBe('Cannot tell')
    expect(describeResponding(toResponsiveness('nope')).label).toBe('Cannot tell')
  })
})

describe('not_applicable', () => {
  it('stays its own answer rather than borrowing "cannot tell"', () => {
    const p = say({
      status: 'not_applicable', method: 'client_side',
      detail: 'built-in views run in the browser',
    })
    expect(p.label).toBe('Not applicable')
    expect(p.note).toBe('')
  })
})

describe('all five answers are distinguishable on screen', () => {
  it('gives every status its own label', () => {
    const labels = [
      'responding', 'not_responding', 'display_not_responding', 'unknown', 'not_applicable',
    ].map(status => say({ status, method: 'x11_ping' }).label)
    expect(new Set(labels).size).toBe(5)
  })
})
