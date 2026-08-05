// lowLatency — WebRTC receive-side latency hints for the stream viewer.

// The minimal shape this module actually touches. `playoutDelayHint` is a
// real, shipping Chrome API (RTCRtpReceiver.playoutDelayHint) but it is not
// part of the W3C WebRTC spec, so it is absent from TypeScript's lib.dom.d.ts
// (which only has the newer `jitterBufferTarget`). Declaring the exact local
// shape we read/write — rather than reaching for the real `RTCRtpReceiver`/
// `RTCRtpTransceiver` DOM types and casting — keeps this honest: a real
// RTCRtpTransceiver structurally satisfies it (its `receiver` just doesn't
// happen to declare this optional field), and the test doubles in
// streamLowLatency.test.ts satisfy it too, with no `any`/`as` anywhere.
export interface LowLatencyReceiver {
  playoutDelayHint?: number
}

export interface LowLatencyTransceiver {
  receiver?: LowLatencyReceiver | null
}

// applyLowLatencyHints requests a minimal receive-side jitter buffer for GAMING
// sessions. Chrome's RTCRtpReceiver.playoutDelayHint (seconds) lets us ask the
// browser not to add buffering latency; 0 means "play out as soon as possible".
// Gated on `gaming` so ordinary desktop streams keep the browser default
// (stability over latency). Wrapped in try/catch and a guard so it is a no-op on
// browsers that don't expose playoutDelayHint (Firefox, Safari) — the property
// assignment must never break WebRTC negotiation.
export function applyLowLatencyHints(transceiver: LowLatencyTransceiver | null | undefined, gaming: boolean): void {
  if (!gaming || !transceiver) return
  const receiver = transceiver.receiver
  if (!receiver) return
  try {
    receiver.playoutDelayHint = 0
  } catch {
    // Unsupported in this browser — the default jitter buffer stays in effect.
  }
}
