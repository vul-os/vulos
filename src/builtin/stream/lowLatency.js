// lowLatency — WebRTC receive-side latency hints for the stream viewer.

// applyLowLatencyHints requests a minimal receive-side jitter buffer for GAMING
// sessions. Chrome's RTCRtpReceiver.playoutDelayHint (seconds) lets us ask the
// browser not to add buffering latency; 0 means "play out as soon as possible".
// Gated on `gaming` so ordinary desktop streams keep the browser default
// (stability over latency). Wrapped in try/catch and a guard so it is a no-op on
// browsers that don't expose playoutDelayHint (Firefox, Safari) — the property
// assignment must never break WebRTC negotiation.
export function applyLowLatencyHints(transceiver, gaming) {
  if (!gaming || !transceiver) return
  const receiver = transceiver.receiver
  if (!receiver) return
  try {
    receiver.playoutDelayHint = 0
  } catch {
    // Unsupported in this browser — the default jitter buffer stays in effect.
  }
}
