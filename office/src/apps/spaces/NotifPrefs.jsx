/**
 * NotifPrefs.jsx — per-channel notification preference picker.
 * Modes: all | mentions | muted.
 * Stored per-channel-per-user via API; default logic in hook.
 */
import { useState, useEffect } from 'react'
import { Bell, BellOff, AtSign, X } from 'lucide-react'
import { api } from '../../lib/api.js'

export const NOTIF_ALL      = 'all'
export const NOTIF_MENTIONS = 'mentions'
export const NOTIF_MUTED    = 'muted'

const PREFS_KEY = 'spaces_notif_prefs'

function loadPrefs() {
  try { return JSON.parse(localStorage.getItem(PREFS_KEY) || '{}') } catch { return {} }
}

function savePref(channelId, pref) {
  const prefs = loadPrefs()
  prefs[channelId] = pref
  try { localStorage.setItem(PREFS_KEY, JSON.stringify(prefs)) } catch {}
}

/**
 * useNotifPref — hook to read/write notification preference for a channel.
 *
 * @param {string} channelId
 * @param {string} channelType — 'dm' | 'public' | 'private'
 * @param {number} memberCount
 * @returns { pref, setPref }
 */
export function useNotifPref(channelId, channelType, memberCount = 0) {
  function defaultPref() {
    if (channelType === 'dm') return NOTIF_ALL
    return memberCount > 50 ? NOTIF_MENTIONS : NOTIF_ALL
  }

  const [pref, setPrefState] = useState(() => {
    const stored = loadPrefs()
    return stored[channelId] || defaultPref()
  })

  // Update if channel changes
  useEffect(() => {
    const stored = loadPrefs()
    setPrefState(stored[channelId] || defaultPref())
  }, [channelId]) // eslint-disable-line react-hooks/exhaustive-deps

  function setPref(val) {
    setPrefState(val)
    savePref(channelId, val)
  }

  return { pref, setPref }
}

/**
 * NotifPrefsPopover — small dropdown to choose notification mode.
 *
 * Props:
 *   pref      — current value
 *   onChange  — (val) => void
 *   onClose   — () => void
 */
export default function NotifPrefsPopover({ pref, onChange, onClose }) {
  const options = [
    {
      value: NOTIF_ALL,
      label: 'All messages',
      desc: 'Notify for every message',
      Icon: Bell,
    },
    {
      value: NOTIF_MENTIONS,
      label: 'Mentions only',
      desc: 'Notify for @you and @channel',
      Icon: AtSign,
    },
    {
      value: NOTIF_MUTED,
      label: 'Muted',
      desc: 'No notifications',
      Icon: BellOff,
    },
  ]

  return (
    <div className="absolute right-0 top-full mt-1 z-50 bg-paper border border-line rounded-lg shadow-e3 py-2 w-60 animate-scale-in">
      <div className="flex items-center justify-between px-3 pb-2 border-b border-line mb-1">
        <p className="text-xs font-semibold text-ink tracking-tightish">Notifications</p>
        <button
          type="button"
          onClick={onClose}
          className="p-0.5 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
        >
          <X size={12} />
        </button>
      </div>
      {options.map(({ value, label, desc, Icon }) => (
        <button
          key={value}
          type="button"
          onClick={() => { onChange(value); onClose() }}
          className={[
            'w-full flex items-start gap-2.5 px-3 py-2 transition-colors text-left',
            pref === value ? 'bg-accent-tint text-ink' : 'text-ink-muted hover:bg-bg-elev2',
          ].join(' ')}
        >
          <Icon size={14} className={`mt-0.5 flex-shrink-0 ${pref === value ? 'text-accent' : 'text-ink-faint'}`} />
          <span>
            <span className="block text-sm font-medium tracking-tightish">{label}</span>
            <span className="block text-2xs text-ink-faint">{desc}</span>
          </span>
          {pref === value && (
            <span className="ml-auto text-accent text-xs mt-0.5">✓</span>
          )}
        </button>
      ))}
    </div>
  )
}
