// useCallSession.ts — "can this box call, is a call happening, and end it".
//
// This replaces usePlaceCall, which could only ever START a call. That was the
// whole of the box's call UI: press Call, get a spinner for as long as the POST
// took, and then nothing — no ringing state, no "you are on a call", and no way
// to hang up. `hangUp()` had been sitting in telephonyApi.ts with ZERO callers
// since it was written, because nothing in the UI knew a call existed to end.
//
// Three separate facts, deliberately not conflated:
//
//   CAPABILITY  — can some line on this box place a voice call at all? Resolved
//                 BEFORE any button is drawn, so a box with no modem shows a
//                 control that says why rather than one that fails on press.
//   REQUEST     — the dial we sent. Ours, optimistic, and never mistaken for
//                 evidence that a call exists.
//   OBSERVATION — GET /api/telephony/call/active, which is the modem's own
//                 answer. The in-call bar is drawn from THIS.
//
// Keeping REQUEST and OBSERVATION apart is the point. A bar drawn from "we
// posted a dial" says "on a call" while the modem sits idle after a failed
// dial, and offers a Hang up that hangs up nothing. So a dial request only
// triggers an immediate re-poll; what the bar shows is what the box reports.

import { useState, useEffect, useCallback, useRef } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import { getStatus, getActiveCall, placeCall, hangUp, answerCall, declineCall, type ActiveCall } from './telephonyApi'
import { friendlyError } from './phoneUtils'

/**
 * How often the modem is asked what it is doing, while something is up. Three
 * seconds is a compromise a poll cannot escape: `mmcli` is a process spawn per
 * tick, and ModemManager's own call objects are the only source of truth (see
 * backend/services/telephony/calls.go). Nothing is polled at all when there is
 * no call and no line — the common case on a box with no modem is ZERO requests
 * after the one capability probe.
 */
const POLL_MS = 3000

export interface CallSession {
  /** True when SOME line on this box can place a voice call. */
  canCall: boolean
  /** Why not, when canCall is false. Empty when it is true. */
  blockedReason: string
  /** Still working out what hardware is here. */
  probing: boolean
  /** The number currently being dialled, or ''. Ours, not the modem's. */
  dialling: string
  /** The last dial failure, or ''. */
  error: string
  /** What the MODEM says is happening, or null. Never inferred from a dial. */
  active: ActiveCall | null
  call: (number: string) => Promise<void>
  hangup: () => Promise<void>
  answer: () => Promise<void>
  decline: () => Promise<void>
  clearError: () => void
}

/**
 * The reason shown when the box has no telephony hardware at all.
 *
 * It names the hardware rather than the platform. The app this merged from told
 * every USB-LTE-stick owner that Phone "only works inside the Vulos Android
 * app", which was false: the box side has always been generic GSM over
 * ModemManager. Anything that reads like "get an Android phone" is a
 * regression, and the E2E suite asserts the word does not appear.
 */
export const NO_MODEM_REASON =
  'No modem is connected to this box. Plug in a GSM modem — a USB LTE stick, an M.2 or PCIe card — ' +
  'with a SIM. Vulos talks to whatever ModemManager detects; no phone required.'

export const DATA_ONLY_REASON =
  'The modem in this box is data/SMS only — it reports no voice support, so it cannot place calls.'

export function useCallSession(): CallSession {
  const [voice, setVoice] = useState(false)
  const [blockedReason, setBlockedReason] = useState('')
  const [probing, setProbing] = useState(true)
  const [dialling, setDialling] = useState('')
  const [error, setError] = useState('')
  const [active, setActive] = useState<ActiveCall | null>(null)
  const alive = useRef(true)
  // The Android handset's own SIM, when the shell runs inside the Vulos app.
  // Checked as a FALLBACK, never as the premise.
  const device = nativeBridge.telephony.available

  useEffect(() => {
    alive.current = true
    getStatus()
      .then((st) => {
        if (!alive.current) return
        if (st.available && st.voice) { setVoice(true); setBlockedReason('') }
        else if (st.available) setBlockedReason(DATA_ONLY_REASON)
        else if (device) { setVoice(true); setBlockedReason('') }
        else setBlockedReason(NO_MODEM_REASON)
      })
      .catch((e: unknown) => {
        if (!alive.current) return
        // A service failure is NOT "no modem" — say which it is, because the two
        // ask the user to do completely different things. One means buy
        // hardware; the other means the box is broken and the hardware may be
        // sitting there working fine.
        if (device) { setVoice(true); setBlockedReason('') }
        else setBlockedReason(`Telephony isn’t answering, so calls can’t be placed: ${friendlyError(e)}`)
      })
      .finally(() => { if (alive.current) setProbing(false) })
    return () => { alive.current = false }
  }, [device])

  // ─── what the modem says is happening ────────────────────────────────────
  //
  // Polled only while there is a box line that could carry a call. On the
  // handset-bridge line there is nothing to poll — the box's ModemManager knows
  // nothing about the phone's own radio — so this stays silent rather than
  // asking a question whose answer would always be "no call" and would then be
  // used to contradict a call that really is up on the handset.
  const pollable = voice && !device

  const refresh = useCallback(async () => {
    try {
      const a = await getActiveCall()
      if (alive.current) setActive(a.active ? a : null)
    } catch {
      // A failed observation is NOT "the call ended". Holding the last known
      // state keeps the Hang up button on screen through a hiccup, which is
      // exactly when a user wants it. The box makes the same choice server-side.
    }
  }, [])

  useEffect(() => {
    if (!pollable) return undefined
    alive.current = true
    // refresh() is async and every setState in it sits behind
    // `await getActiveCall()`, so nothing is set synchronously in this effect
    // body. Deferring the first observation to the first interval tick instead
    // would leave a placed call with no in-call bar — and no Hang up button —
    // for POLL_MS.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refresh()
    const t = setInterval(refresh, POLL_MS)
    return () => { clearInterval(t) }
  }, [pollable, refresh])

  // Not `setActive(null)` inside the effect above. The last observation is
  // stale state, not a fact to be written back — and clearing it with a setState
  // in an effect body cascades a render on every box that has no line, which is
  // most of them. Derived here instead, so a line disappearing hides the bar in
  // the same render it disappears in.
  const activeCall = pollable ? active : null

  const call = useCallback(async (number: string) => {
    if (!number) return
    setError('')
    setDialling(number)
    try {
      // The box's own modem first; the handset bridge only when there is no box
      // line to use.
      if (voice && !device) await placeCall(number)
      else if (device) await nativeBridge.telephony.dial(number)
      else await placeCall(number)
      // Ask the modem straight away rather than waiting up to POLL_MS, so the
      // in-call bar appears with the dial instead of a beat later. It is still
      // the MODEM's answer that puts it there.
      if (pollable) await refresh()
    } catch (e) {
      // placeCall throws on a non-2xx AND on a 200 carrying {"error": …}, which
      // is how the box reports "no modem" — see telephonyApi.okOrThrow.
      if (alive.current) setError(friendlyError(e))
    } finally {
      if (alive.current) setDialling('')
    }
  }, [voice, device, pollable, refresh])

  // hangup/answer/decline all re-observe rather than assuming they worked. The
  // modem is the authority on whether the call is over; a local `setActive(null)`
  // would clear the bar even when the hangup failed, stranding a live call with
  // no control at all.
  const act = useCallback(async (fn: () => Promise<void>) => {
    setError('')
    try {
      await fn()
    } catch (e) {
      if (alive.current) setError(friendlyError(e))
    } finally {
      await refresh()
    }
  }, [refresh])

  const hangup = useCallback(() => act(hangUp), [act])
  const answer = useCallback(() => act(answerCall), [act])
  const decline = useCallback(() => act(declineCall), [act])

  return {
    canCall: voice, blockedReason, probing, dialling, error, active: activeCall,
    call, hangup, answer, decline, clearError: () => setError(''),
  }
}
