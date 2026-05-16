// useGamepad — shared hook for WebRTC gamepad input + FF_RUMBLE feedback.
// Polls the Gamepad API at the configured rate, sends state snapshots over
// the provided data-channel ref, and applies rumble via vibrationActuator
// when the server forwards FF_RUMBLE events back over the same channel.
import { useEffect, useRef } from 'react'

const DEADZONE = 0.05
const DEFAULT_POLL_HZ = 60

function applyDeadzone(v) {
  return Math.abs(v) < DEADZONE ? 0 : v
}

/**
 * useGamepad — attach gamepad polling + rumble to an open WebRTC data channel.
 *
 * @param {React.MutableRefObject} gamepadDcRef  ref to the 'gamepad' RTCDataChannel
 * @param {object}  [opts]
 * @param {number}  [opts.pollHz=60]  polling rate (use 120 for gaming mode)
 */
export default function useGamepad(gamepadDcRef, { pollHz = DEFAULT_POLL_HZ } = {}) {
  const rafRef = useRef(null)
  const lastRef = useRef(0)

  useEffect(() => {
    const interval = 1000 / pollHz

    function poll(ts) {
      rafRef.current = requestAnimationFrame(poll)

      if (ts - lastRef.current < interval) return
      lastRef.current = ts

      const dc = gamepadDcRef.current
      if (!dc || dc.readyState !== 'open') return

      const gamepads = navigator.getGamepads ? navigator.getGamepads() : []
      const gp = gamepads[0]
      if (!gp || !gp.connected) return

      const buttons = gp.buttons.map(b => (typeof b === 'object' ? b.pressed : Boolean(b)))
      // Axes 0–3: left stick X/Y, right stick X/Y
      const axes = [0, 1, 2, 3].map(i => applyDeadzone(gp.axes[i] ?? 0))
      // Triggers: buttons 6 (LT) and 7 (RT) carry analog value in .value
      const triggers = [
        gp.buttons[6] ? gp.buttons[6].value : 0,
        gp.buttons[7] ? gp.buttons[7].value : 0,
      ]

      dc.send(JSON.stringify({ buttons, axes, triggers }))
    }

    rafRef.current = requestAnimationFrame(poll)
    return () => {
      if (rafRef.current) cancelAnimationFrame(rafRef.current)
    }
  }, [gamepadDcRef, pollHz])

  // Rumble: listen for server-forwarded FF_RUMBLE events on the gamepad channel.
  // The server sends: {"t":"rumble","strong":<0-65535>,"weak":<0-65535>}
  // We map strong → leftActuator, weak → rightActuator and call playEffect.
  useEffect(() => {
    const dc = gamepadDcRef.current
    if (!dc) return

    function rumbleOnMessage(evt) {
      let msg
      try { msg = JSON.parse(evt.data) } catch { return }
      if (msg.t !== 'rumble') return

      const gamepads = navigator.getGamepads ? navigator.getGamepads() : []
      const gp = gamepads[0]
      if (!gp || !gp.connected) return

      const actuator = gp.vibrationActuator
      if (!actuator) return

      const strongFrac = (msg.strong ?? 0) / 65535
      const weakFrac   = (msg.weak   ?? 0) / 65535

      if (typeof actuator.playEffect === 'function') {
        actuator.playEffect('dual-rumble', {
          startDelay:     0,
          duration:       200,
          weakMagnitude:  weakFrac,
          strongMagnitude: strongFrac,
        }).catch(() => {})
      }
    }

    dc.addEventListener('message', rumbleOnMessage)
    return () => dc.removeEventListener('message', rumbleOnMessage)
  }, [gamepadDcRef])
}
