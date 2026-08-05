/**
 * streamLowLatency.test.ts — StreamViewer's gaming low-latency receive hint.
 *
 * Verifies that GAMING sessions request a minimal receive-side jitter buffer
 * (RTCRtpReceiver.playoutDelayHint = 0) while non-gaming desktop streams keep
 * the browser default (property left untouched), and that the helper never
 * throws on browsers that don't expose playoutDelayHint.
 */

import { describe, it, expect } from 'vitest'
import { applyLowLatencyHints } from './lowLatency'

// A minimal stand-in for the transceiver returned by pc.addTransceiver('video').
// Typed with a required (non-nullable) `receiver` — unlike the exported
// LowLatencyTransceiver (whose `receiver` is optional/nullable to accept a
// bare RTCRtpTransceiver too) — so the assertions below can read
// `tr.receiver.playoutDelayHint` directly, exactly as the real fixture shape
// guarantees.
interface FakeTransceiver {
  receiver: { playoutDelayHint?: number }
}

function fakeTransceiver(): FakeTransceiver {
  return { receiver: {} }
}

describe('applyLowLatencyHints', () => {
  it('sets playoutDelayHint=0 on the receiver for gaming sessions', () => {
    const tr = fakeTransceiver()
    applyLowLatencyHints(tr, true)
    expect(tr.receiver.playoutDelayHint).toBe(0)
  })

  it('leaves playoutDelayHint untouched for non-gaming sessions', () => {
    const tr = fakeTransceiver()
    applyLowLatencyHints(tr, false)
    expect(tr.receiver.playoutDelayHint).toBeUndefined()
  })

  it('is a no-op (no throw) when the transceiver or receiver is absent', () => {
    expect(() => applyLowLatencyHints(null, true)).not.toThrow()
    expect(() => applyLowLatencyHints({}, true)).not.toThrow()
  })

  it('swallows assignment errors on browsers without playoutDelayHint support', () => {
    // Simulate a receiver whose playoutDelayHint is a read-only / throwing setter.
    const tr = {
      receiver: Object.defineProperty({}, 'playoutDelayHint', {
        set() { throw new Error('unsupported') },
        configurable: true,
      }),
    }
    expect(() => applyLowLatencyHints(tr, true)).not.toThrow()
  })
})
