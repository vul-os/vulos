// usePhoneData.ts — where the Phone app gets its lines, its address book and
// its call log.
//
// THE LINE MODEL is the reason this file exists. "Phone" on a Vulos box is not
// one thing, and the previous app assumed it was exactly one (an Android
// handset). There are three possible lines, and a box may have any combination
// of them, including none:
//
//   box     — a modem attached to THE BOX, spoken to through ModemManager.
//             A USB LTE stick, an M.2 card, a PCIe modem, a Pi HAT. This is the
//             DEFAULT and it is checked FIRST. Nothing about it is Android.
//   device  — the SIM in the Android handset the shell happens to be running
//             on, via the native bridge. Optional, and absent in every browser.
//   virtual — a BYO second/throwaway number from a configured provider.
//
// The app renders whichever exist, defaults to the best one, and when none
// exist says so honestly instead of showing a dial pad that cannot dial.

import { useState, useEffect, useCallback, useRef } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import {
  getStatus, getVirtualStatus, getCalls, getPeerCalls, getContacts, nameIndex,
  type CallEntry, type PhoneContact,
} from './telephonyApi'
import { isRecord, friendlyError, secondsToMs } from './phoneUtils'

export type LineId = 'box' | 'device' | 'virtual'

export interface Line {
  id: LineId
  label: string
  /** Operator, own number, or provider — whatever identifies this line. */
  sub: string
  /** Can this line place CALLS? A data/SMS-only modem cannot. */
  voice: boolean
  /** Can this line send SMS? */
  sms: boolean
  /** 0-100, or -1 when the line has no signal concept (virtual). */
  signal: number
}

export interface LinesState {
  lines: Line[]
  active: Line | null
  setActive: (id: LineId) => void
  loading: boolean
  /**
   * The box's telephony service could not be reached or answered something
   * unreadable. This is emphatically NOT the same as "no modem is plugged in",
   * and the UI must not render the two the same way — one is a box working
   * correctly with no hardware, the other is a box that is broken.
   */
  serviceError: string
  reload: () => void
}

export function useLines(): LinesState {
  const [lines, setLines] = useState<Line[]>([])
  const [activeId, setActiveId] = useState<LineId | null>(null)
  const [loading, setLoading] = useState(true)
  const [serviceError, setServiceError] = useState('')
  const alive = useRef(true)

  // load() must not call setState SYNCHRONOUSLY — the mount effect below calls
  // it, and a synchronous setState in an effect body cascades renders
  // (react-hooks/set-state-in-effect). `loading` starts true instead, and the
  // user-triggered reload() is the one that flips it back on.
  const load = useCallback(() => {
    const probe = async () => {
      const found: Line[] = []
      let err = ''

      // 1. The box's own modem — first, because it is the box's line and the one
      //    that does not depend on which client you happen to be looking from.
      try {
        const st = await getStatus()
        if (st.available) {
          found.push({
            id: 'box',
            label: 'Box modem',
            sub: [st.operator, st.number].filter(Boolean).join(' · ') || st.state || 'Connected',
            voice: st.voice,
            sms: true,
            signal: st.signal,
          })
        }
      } catch (e) {
        err = friendlyError(e)
      }

      // 2. The handset's SIM, when the shell is running inside the Android app.
      if (nativeBridge.telephony.available) {
        found.push({ id: 'device', label: 'This phone’s SIM', sub: 'Android handset', voice: true, sms: true, signal: -1 })
      }

      // 3. A configured second number.
      try {
        const v = await getVirtualStatus()
        if (v.configured) {
          found.push({
            id: 'virtual',
            label: 'Second number',
            sub: [v.number, v.provider].filter(Boolean).join(' · ') || 'Configured',
            voice: v.canCall,
            sms: true,
            signal: -1,
          })
        }
      } catch {
        // The virtual line is an optional extra. Its absence is the norm and is
        // already conveyed by it not appearing; it must not mask a box error.
      }

      if (!alive.current) return
      setLines(found)
      setServiceError(err)
      setActiveId((cur) => (cur && found.some((l) => l.id === cur) ? cur : found[0]?.id ?? null))
      setLoading(false)
    }
    probe()
  }, [])

  const reload = useCallback(() => { setLoading(true); load() }, [load])

  useEffect(() => { alive.current = true; load(); return () => { alive.current = false } }, [load])

  const active = lines.find((l) => l.id === activeId) ?? null
  return { lines, active, setActive: setActiveId, loading, serviceError, reload }
}

// ─── address book ──────────────────────────────────────────────────────────

export interface ContactsState {
  contacts: PhoneContact[]
  names: Map<string, string>
  loading: boolean
  error: string
  reload: () => void
}

export function useContacts(): ContactsState {
  const [contacts, setContacts] = useState<PhoneContact[]>([])
  const [names, setNames] = useState<Map<string, string>>(new Map())
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const alive = useRef(true)

  const load = useCallback(() => {
    getContacts()
      .then((cs) => {
        if (!alive.current) return
        cs.sort((a, b) => (a.name || '').localeCompare(b.name || ''))
        setContacts(cs)
        setNames(nameIndex(cs))
        setError('')
      })
      .catch((e: unknown) => {
        if (!alive.current) return
        // Deliberately NOT setContacts([]) — an error must not be dressed up as
        // an empty address book. The list keeps whatever it last had and the
        // error is shown alongside it.
        setError(friendlyError(e))
      })
      .finally(() => { if (alive.current) setLoading(false) })
  }, [])

  const reload = useCallback(() => { setLoading(true); load() }, [load])

  useEffect(() => { alive.current = true; load(); return () => { alive.current = false } }, [load])

  return { contacts, names, loading, error, reload }
}

// ─── the merged call log ───────────────────────────────────────────────────

export interface CallsState {
  calls: CallEntry[]
  loading: boolean
  /** The GSM log failed — a real problem, shown as one. */
  error: string
  reload: () => void
}

// Android's call-log rows come off the native bridge loosely shaped and in
// milliseconds; normalise them into the same CallEntry the box's log produces so
// ONE Recents list can render both.
function bridgeCall(r: unknown, i: number): CallEntry | null {
  if (!isRecord(r)) return null
  const dir = typeof r.direction === 'string' ? r.direction : ''
  return {
    id: `device:${i}`,
    number: typeof r.number === 'string' ? r.number : '',
    direction: dir === 'outgoing' || dir === 'missed' ? dir : 'incoming',
    ts: Math.floor(Number(r.date) / 1000) || 0,
    duration: Number(r.durationSec) || 0,
    origin: 'gsm',
  }
}

export function useCalls(lineId: LineId | null, names: Map<string, string>): CallsState {
  const [calls, setCalls] = useState<CallEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const alive = useRef(true)

  const load = useCallback(() => {
    const run = async () => {
      let gsm: CallEntry[] = []
      let err = ''
      try {
        if (lineId === 'device') {
          const raw: unknown = await nativeBridge.telephony.listCallLog(50)
          gsm = (Array.isArray(raw) ? raw : []).map(bridgeCall).filter((c): c is CallEntry => c !== null)
        } else if (lineId === 'box') {
          gsm = await getCalls()
        }
      } catch (e) {
        err = friendlyError(e)
      }

      // Vulos-to-Vulos call history rides alongside, best-effort. It is a
      // different service with a different identity space, and it being absent
      // (the common case) is not an error worth showing.
      let peer: CallEntry[] = []
      try { peer = await getPeerCalls() } catch { peer = [] }

      if (!alive.current) return
      const merged = [...gsm, ...peer]
        .map((c) => ({ ...c, name: c.name || names.get(digits(c.number)) }))
        .sort((a, b) => b.ts - a.ts)
      setCalls(merged)
      setError(err)
      setLoading(false)
    }
    run()
  }, [lineId, names])

  const reload = useCallback(() => { setLoading(true); load() }, [load])

  useEffect(() => { alive.current = true; load(); return () => { alive.current = false } }, [load])

  return { calls, loading, error, reload }
}

function digits(raw: string): string {
  const d = (raw || '').replace(/\D/g, '')
  return d.length > 9 ? d.slice(-9) : d
}

/** Epoch-seconds (box wire) → ms, for the shared formatters. */
export const callTimeMs = (c: CallEntry): number => secondsToMs(c.ts)
