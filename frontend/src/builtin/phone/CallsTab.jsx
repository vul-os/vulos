import { useState, useEffect, useCallback, useRef } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import { formatRelative, formatDuration, friendlyError } from './phoneUtils'

const DIRECTION_META = {
  incoming: { glyph: '↙', color: '#22C55E', label: 'Incoming' },
  outgoing: { glyph: '↗', color: 'var(--accent)', label: 'Outgoing' },
  missed: { glyph: '↘', color: '#EF4444', label: 'Missed' },
  other: { glyph: '•', color: 'var(--text-secondary, #888)', label: 'Call' },
}

export default function CallsTab({ onPermsChange }) {
  const [calls, setCalls] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [dialing, setDialing] = useState('')
  const mounted = useRef(true)

  const load = useCallback(() => {
    setLoading(true)
    nativeBridge.telephony.listCallLog(50)
      .then((list) => { if (mounted.current) { setCalls(list); setError('') } })
      .catch((e) => { if (mounted.current) setError(friendlyError(e)) })
      .finally(() => { if (mounted.current) setLoading(false) })
    nativeBridge.telephony.perms().then(onPermsChange).catch(() => {})
  }, [onPermsChange])

  useEffect(() => { mounted.current = true; load(); return () => { mounted.current = false } }, [load])

  const call = async (number) => {
    if (!number || !window.confirm(`Dial ${number}?`)) return
    setDialing(number)
    try {
      await nativeBridge.telephony.dial(number)
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setDialing('')
    }
  }

  return (
    <div className="h-full flex flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto">
        {loading && calls.length === 0 ? (
          <div className="p-4 flex items-center gap-2 text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
            <span className="w-3.5 h-3.5 spinner" /> Loading…
          </div>
        ) : error && calls.length === 0 ? (
          <div className="p-8 text-center text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>{error}</div>
        ) : calls.length === 0 ? (
          <div className="p-10 text-center text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
            No calls yet. This SIM's call history will show up here.
          </div>
        ) : (
          <ul className="divide-y" style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.06))' }}>
            {calls.map((c, i) => {
              const meta = DIRECTION_META[c.direction] || DIRECTION_META.other
              return (
                <li key={i}>
                  <button type="button" onClick={() => call(c.number)} disabled={!c.number || dialing === c.number}
                    className="w-full flex items-center gap-2.5 px-3 py-2.5 text-left hover:bg-black/5 dark:hover:bg-white/5 transition-colors focus-primary disabled:opacity-60">
                    <span className="w-6 h-6 shrink-0 grid place-items-center rounded-full text-[13px]"
                      style={{ color: meta.color, background: 'color-mix(in srgb, currentColor 14%, transparent)' }}>{meta.glyph}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-[12.5px] font-medium truncate">{c.number || 'Unknown'}</span>
                      <span className="block text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
                        {meta.label}{c.durationSec ? ` · ${formatDuration(c.durationSec)}` : ''}
                      </span>
                    </span>
                    <span className="shrink-0 text-[12px]" style={{ color: 'var(--text-secondary, #888)' }}>
                      {dialing === c.number ? 'Dialing…' : formatRelative(c.date)}
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}
