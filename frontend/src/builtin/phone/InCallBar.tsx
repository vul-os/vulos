// InCallBar.tsx — the call that is happening, and the controls for it.
//
// Before this there was no in-call surface of any kind. Pressing Call posted a
// dial, the button stopped saying "Calling…", and that was the end of the UI's
// involvement — a live call had no representation on screen and no way to end
// it. On a phone that is the single most important piece of chrome there is.
//
// WHAT IT IS AND IS NOT DRAWN FROM. Everything here comes from
// GET /api/telephony/call/active, which is ModemManager's own answer. Nothing
// is inferred from "we posted a dial", because a dial that the network rejects
// leaves the modem idle and a request-driven bar would sit there claiming a
// call and offering to hang up nothing.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM. There is no call TIMER. The box records
// no start time for a call in progress — the call log's duration is computed
// when a call ENDS (calls.go) — so a timer here could only count from the
// moment this browser first noticed, which is not when the call started and is
// wrong by the whole of any call already running when the app was opened. A
// wrong clock on a phone call is worse than no clock, so the state is named in
// words instead.

import { Avatar } from './PhoneChrome'
import { displayNumber } from './phoneUtils'
import type { ActiveCall } from './telephonyApi'

/**
 * ModemManager's call states, in this app's words. Anything unrecognised falls
 * back to the neutral "On a call" rather than showing a raw enum — but the
 * state is never dropped, because a call whose state we cannot name is still a
 * call and must still be hangable.
 */
function stateWords(c: ActiveCall): string {
  switch (c.state) {
    case 'dialing': return 'Dialling…'
    case 'ringing-out': return 'Ringing…'
    case 'ringing-in': return 'Incoming call'
    case 'waiting': return 'Call waiting'
    case 'held': return 'On hold'
    case 'active': return 'On a call'
    default: return 'On a call'
  }
}

/** A call the box is offering to answer, rather than one already up. */
function isRinging(c: ActiveCall): boolean {
  return c.state === 'ringing-in' || c.state === 'waiting'
}

interface InCallBarProps {
  call: ActiveCall
  /** Display name for the far end, resolved from the address book, if known. */
  name?: string
  onHangUp: () => void
  onAnswer: () => void
  onDecline: () => void
}

export default function InCallBar({ call, name, onHangUp, onAnswer, onDecline }: InCallBarProps) {
  const ringing = isRinging(call)
  // A withheld number is normal on a real network — say so rather than render
  // an empty space where a person should be.
  const who = name || (call.number ? displayNumber(call.number) : 'Unknown number')

  return (
    <div data-in-call-bar data-call-state={call.state || 'unknown'}
      className="shrink-0 flex items-center gap-3 px-3.5 py-2.5"
      style={{
        // A live call is the one thing in this app that must be legible at a
        // glance from across a room, so it gets a filled band rather than the
        // app's usual quiet elevated surface. The fill owns both halves of its
        // own contrast pair and so cannot be broken by the theme underneath it.
        background: ringing
          ? 'color-mix(in srgb, var(--status-success) 82%, #000)'
          : 'color-mix(in srgb, var(--accent) 76%, #000)',
        color: '#fff',
        borderBottom: '1px solid rgba(0,0,0,0.25)',
      }}>
      <Avatar name={name || ''} number={call.number} size={34} />
      <span className="min-w-0 flex-1">
        <span className="block text-[13.5px] font-semibold truncate">{who}</span>
        <span className="block text-[12px] truncate" style={{ color: 'rgba(255,255,255,0.85)' }}>
          {stateWords(call)}
        </span>
      </span>

      {ringing ? (
        <>
          <button type="button" onClick={onAnswer} data-call-answer
            className="shrink-0 px-3 h-8 rounded-lg text-[12.5px] font-semibold focus-primary transition-transform active:scale-95"
            style={{ background: '#fff', color: 'color-mix(in srgb, var(--status-success) 82%, #000)' }}>
            Answer
          </button>
          <button type="button" onClick={onDecline} data-call-decline
            className="shrink-0 px-3 h-8 rounded-lg text-[12.5px] font-semibold focus-primary transition-transform active:scale-95"
            style={{ background: 'var(--status-danger)', color: '#fff' }}>
            Decline
          </button>
        </>
      ) : (
        <button type="button" onClick={onHangUp} data-call-hangup
          className="shrink-0 px-3 h-8 rounded-lg text-[12.5px] font-semibold focus-primary transition-transform active:scale-95"
          style={{ background: 'var(--status-danger)', color: '#fff' }}>
          Hang up
        </button>
      )}
    </div>
  )
}
