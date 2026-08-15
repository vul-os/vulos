// Keypad.tsx — dialling as a first-class thing rather than a text field.
//
// On a box with a modem, typing a number and pressing call is the primary act,
// so it gets its own tab and a real dial pad: big targets, letter legends, long
// press on 0 for "+", live matching against the address book as you type.
//
// When the active line cannot place calls — no modem at all, or a data/SMS-only
// USB stick — the pad does not pretend. The call button is disabled and says
// why, rather than dialling into a 200 response that carries an error.

import { useState, useMemo } from 'react'
import { CallButton } from './PhoneChrome'
import { displayNumber } from './phoneUtils'
import { digitKey, type PhoneContact } from './telephonyApi'

const KEYS: [string, string][] = [
  ['1', ''], ['2', 'ABC'], ['3', 'DEF'],
  ['4', 'GHI'], ['5', 'JKL'], ['6', 'MNO'],
  ['7', 'PQRS'], ['8', 'TUV'], ['9', 'WXYZ'],
  ['*', ''], ['0', '+'], ['#', ''],
]

interface KeypadProps {
  contacts: PhoneContact[]
  canCall: boolean
  callBlockedReason: string
  canSms: boolean
  onCall: (number: string) => void
  onMessage: (number: string) => void
  /** Set when a dial attempt failed — shown under the pad. */
  dialError: string
}

export default function Keypad({ contacts, canCall, callBlockedReason, canSms, onCall, onMessage, dialError }: KeypadProps) {
  const [value, setValue] = useState('')

  // Live match: as soon as enough digits are typed to be a real suffix, show who
  // it is. Matching on the last 9 digits mirrors the box's own normalisation, so
  // "0831112222" finds the contact stored as "+27 83 111 2222".
  const match = useMemo(() => {
    const k = digitKey(value)
    if (k.length < 4) return null
    return contacts.find((c) => c.phones.some((p) => {
      const pk = digitKey(p)
      return pk !== '' && (pk === k || pk.endsWith(k))
    })) ?? null
  }, [contacts, value])

  const press = (k: string) => setValue((v) => (v.length >= 24 ? v : v + k))
  const back = () => setValue((v) => v.slice(0, -1))
  const clear = () => setValue('')

  return (
    <div className="h-full flex flex-col min-h-0 overflow-y-auto">
      <div className="flex-1 min-h-0 flex flex-col items-center justify-end px-4 pt-5 pb-3 gap-1">
        <div className="w-full max-w-[19rem] text-center">
          <div aria-live="polite" className="min-h-[2.5rem] text-[26px] font-light tracking-wide break-all leading-tight"
            style={{ color: value ? 'var(--text-primary)' : 'var(--text-ghost)' }}>
            {value ? displayNumber(value) : 'Enter a number'}
          </div>
          <div className="min-h-[1.25rem] text-[13px] truncate" style={{ color: 'var(--accent)' }}>
            {match ? match.name : ''}
          </div>
        </div>
      </div>

      <div className="shrink-0 px-4 pb-4 flex flex-col items-center gap-3">
        <div className="grid grid-cols-3 gap-2 w-full max-w-[19rem]">
          {KEYS.map(([k, legend]) => (
            <button key={k} type="button"
              onClick={() => press(k === '0' ? '0' : k)}
              onContextMenu={(e) => { if (k === '0') { e.preventDefault(); press('+') } }}
              aria-label={k}
              className="aspect-[5/3] rounded-xl flex flex-col items-center justify-center transition-colors hover:brightness-110 active:scale-95 focus-primary"
              style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}>
              <span className="text-[20px] leading-none font-light" style={{ color: 'var(--text-primary)' }}>{k}</span>
              {legend && <span className="text-[9.5px] tracking-[0.12em] mt-1" style={{ color: 'var(--text-muted)' }}>{legend}</span>}
            </button>
          ))}
        </div>

        <div className="flex items-center gap-4 w-full max-w-[19rem] justify-center">
          {canSms && (
            <button type="button" onClick={() => onMessage(value)} disabled={!value}
              className="px-3 py-2 rounded-lg text-[13px] font-medium disabled:opacity-40 focus-primary transition-colors"
              style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
              Message
            </button>
          )}
          <CallButton size={56} onClick={() => onCall(value)} disabled={!canCall || !value}
            label={value ? `Call ${displayNumber(value)}` : 'Call'}
            reason={!canCall ? callBlockedReason : 'Enter a number first'} />
          <button type="button" onClick={back} onDoubleClick={clear} disabled={!value}
            aria-label="Backspace"
            className="px-3 py-2 rounded-lg text-[15px] disabled:opacity-30 focus-primary transition-colors"
            style={{ color: 'var(--text-secondary)' }}>
            ⌫
          </button>
        </div>

        {!canCall && (
          <p className="text-[12.5px] text-center max-w-[21rem] leading-relaxed" style={{ color: 'var(--text-tertiary)' }}>
            {callBlockedReason}
          </p>
        )}
        {dialError && (
          <p role="alert" className="text-[12.5px] text-center max-w-[21rem] leading-relaxed text-danger">{dialError}</p>
        )}
      </div>
    </div>
  )
}
