// usePlaceCall.ts — "can this box call, and please call this number".
//
// Shared by the Phone app and the Contacts app so that pressing call means the
// same thing in both, resolves the same line, and fails the same way. Contacts
// previously offered a `tel:` link, which on a box with a modem in it does
// nothing at all: the browser hands the URL to the host OS, and the host OS is
// the box, which has no dialler application — the modem is reached over
// /api/telephony. The link looked like an action and was a dead end.
//
// Capability is resolved BEFORE the button is drawn, so a box with no modem, or
// with a data/SMS-only modem, shows a disabled control that says why rather
// than one that fails on press.

import { useState, useEffect, useCallback, useRef } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import { getStatus, placeCall } from './telephonyApi'
import { friendlyError } from './phoneUtils'

export interface PlaceCall {
  /** True when SOME line on this box can place a voice call. */
  canCall: boolean
  /** Why not, when canCall is false. Empty when it is true. */
  blockedReason: string
  /** The number currently being dialled, or ''. */
  dialling: string
  /** The last dial failure, or ''. */
  error: string
  call: (number: string) => Promise<void>
  clearError: () => void
}

export function usePlaceCall(): PlaceCall {
  const [voice, setVoice] = useState(false)
  const [blockedReason, setBlockedReason] = useState('Checking for a modem…')
  const [dialling, setDialling] = useState('')
  const [error, setError] = useState('')
  const alive = useRef(true)
  // The Android handset's own SIM, when the shell runs inside the Vulos app.
  // Checked as a fallback, never as the premise.
  const device = nativeBridge.telephony.available

  useEffect(() => {
    alive.current = true
    getStatus()
      .then((st) => {
        if (!alive.current) return
        if (st.available && st.voice) { setVoice(true); setBlockedReason('') }
        else if (st.available) setBlockedReason('The modem in this box is data/SMS only — it reports no voice support.')
        else if (device) { setVoice(true); setBlockedReason('') }
        else setBlockedReason('No modem is connected to this box. Plug in a GSM modem with a SIM to place calls.')
      })
      .catch((e: unknown) => {
        if (!alive.current) return
        // A service failure is NOT "no modem" — say which it is, because the two
        // ask the user to do completely different things.
        if (device) { setVoice(true); setBlockedReason('') }
        else setBlockedReason(`Telephony isn’t answering, so calls can’t be placed: ${friendlyError(e)}`)
      })
    return () => { alive.current = false }
  }, [device])

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
    } catch (e) {
      // placeCall throws on a non-2xx AND on a 200 carrying {"error": …}, which
      // is how the box reports "no modem" — see telephonyApi.okOrThrow.
      if (alive.current) setError(friendlyError(e))
    } finally {
      if (alive.current) setDialling('')
    }
  }, [voice, device])

  return { canCall: voice, blockedReason, dialling, error, call, clearError: () => setError('') }
}
