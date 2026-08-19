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
  /**
   * Where this call's audio will actually be, when that is not where the user
   * would assume. Empty when there is nothing to say — see the note on
   * AUDIO_ON_MODEM for why this is PER-PLATFORM and must stay that way.
   */
  audioPath: string
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

/**
 * WHERE THE AUDIO IS, on a box calling over its own modem.
 *
 * Vulos telephony is call CONTROL, not a voice path. The box shells out to
 * `mmcli` to dial, answer and hang up (backend/services/telephony/calls.go),
 * and that file says it plainly: "The modem owns the audio path; this just
 * initiates the call." There is no WebRTC, no PipeWire/PulseAudio/ALSA bridge,
 * no Opus, no RTP anywhere in the telephony package — and no getUserMedia in
 * any calling path, so no software voice path is even possible.
 *
 * Precisely: the repo DOES contain two getUserMedia call sites, in the bundled
 * Camera and Voice Recorder web apps (frontend/apps/camera/index.html and
 * apps/voice-recorder/index.html). Neither is reachable from calling. An
 * earlier version of this comment said "no getUserMedia anywhere in this repo",
 * which is false; the conclusion held but the reason did not, and a claim that
 * is right by luck is the thing this file exists to argue against.
 *
 * Everything about the UI up to here was disciplined about it: capability is
 * resolved before a button is drawn, the in-call bar is drawn from the modem's
 * own answer, and there is no mute, speaker or DTMF control pretending to sit
 * on a media stream. The one remaining gap was that NOTHING SAID SO. A user
 * pressed Call, got a working in-call bar and a live call, and then discovered
 * by silence that they could neither hear nor be heard.
 *
 * That is worth saying before the call, not after — so this rides with every
 * dialling surface. It is not an error and not a blocked reason: the call is
 * real and useful. A phone you operate from your desk while you talk on the
 * handset is the thing being built, and it only reads as broken if nobody
 * mentions the handset.
 */
export const AUDIO_ON_MODEM =
  'Audio doesn’t come through this browser. Vulos dials, answers and hangs up; the modem ' +
  'carries the call itself, so you speak and listen on the modem’s own audio path — a ' +
  'handset, headset or speaker attached to it.'

/** The same fact, at in-call-bar length. */
export const AUDIO_ON_MODEM_SHORT = 'Audio is on the modem, not in this browser'

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

  // While the probe is in flight `canCall` is false, so every Call control is
  // already disabled — but with an EMPTY reason it is a dead control with no
  // explanation, which reads exactly like a broken button. Say what is going on
  // instead. This is also why `probing` is a separate fact from `blockedReason`:
  // "we don't know yet" and "there is no modem" must not be the same state.
  const reason = probing ? 'Checking this box for a modem…' : blockedReason

  // Said ONLY for a call that will run on this box's own modem.
  //
  // On the Android bridge the shell hands the number to the system dialer
  // (nativeBridge.telephony.dial → TelephonyBridge.kt's Intent.ACTION_CALL) and
  // the handset takes the call over its own earpiece, exactly as it would for
  // any other call. There is nothing surprising to warn about there, and a
  // warning would be simply FALSE — which is why this keys on the same
  // `pollable` predicate that decides whose line is carrying the call, rather
  // than on "can we call at all".
  //
  // It is also silent while probing and on a box that cannot call: an audio
  // caveat about a call that is not going to happen is noise on top of a
  // blockedReason that already explains the real problem.
  const audioPath = pollable ? AUDIO_ON_MODEM : ''

  return {
    canCall: voice, blockedReason: reason, audioPath, probing, dialling, error, active: activeCall,
    call, hangup, answer, decline, clearError: () => setError(''),
  }
}
