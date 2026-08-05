/**
 * PublicAppBanner — PUBWEB-06
 *
 * Persistent amber top-bar banner shown when the currently focused app has
 * visibility = "public".  Includes a "Make private" quick-action.
 *
 * Renders null for private apps, system apps, or when no app is focused.
 */
import { useState, useEffect, useCallback, type Dispatch, type SetStateAction } from 'react'
import { useShell } from '../providers/ShellProvider'

// System apps that cannot be published (must stay private)
const PUBWEB_SYSTEM_APPS = new Set([
  'settings', 'files', 'terminal', 'installer',
])

const PUBWEB_POLL_MS = 15_000

type Visibility = 'public' | 'private' | null

function useFocusedAppVisibility(appId: string | null): [Visibility, Dispatch<SetStateAction<Visibility>>] {
  const [visibility, setVisibility] = useState<Visibility>(null)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (!appId) { setVisibility(null); return }

    let cancelled = false

    async function fetchVis() {
      try {
        const res = await fetch('/api/apps/visibility')
        if (!res.ok || cancelled) return
        const list = await res.json()
        const entry = list.find((a: { app_id: string; visibility?: Visibility }) => a.app_id === appId)
        if (!cancelled) setVisibility(entry?.visibility ?? 'private')
      } catch {
        // network error — keep previous state
      }
    }

    fetchVis()
    const id = setInterval(fetchVis, PUBWEB_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [appId])

  return [visibility, setVisibility]
}

export default function PublicAppBanner() {
  const { windows, activeWindow } = useShell()
  const [saving, setSaving] = useState(false)

  // Derive focused app id
  const focused = windows.find(w => w.id === activeWindow)
  const appId = focused?.appId ?? null

  const [visibility, setVisibility] = useFocusedAppVisibility(appId)

  const handleMakePrivate = useCallback(async () => {
    if (!appId || saving) return
    setSaving(true)
    try {
      await fetch(`/api/apps/${appId}/visibility`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ visibility: 'private' }),
      })
      setVisibility('private')
    } catch {
      // noop — banner remains; user can retry
    } finally {
      setSaving(false)
    }
  }, [appId, saving, setVisibility])

  // Do not show for system apps, private apps, or when nothing is focused
  if (!appId || PUBWEB_SYSTEM_APPS.has(appId)) return null
  if (visibility !== 'public') return null

  return (
    <div
      role="alert"
      aria-live="polite"
      className="absolute top-8 left-0 right-0 z-[45] flex items-center justify-between gap-2 px-3 py-1.5 bg-amber-500/95 backdrop-blur-sm border-b border-amber-400/60 text-amber-950 text-[12px] font-medium leading-none shadow-sm"
    >
      <div className="flex items-center gap-1.5 min-w-0">
        {/* Globe icon */}
        <svg
          viewBox="0 0 16 16"
          className="w-3 h-3 shrink-0"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.4"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <circle cx="8" cy="8" r="6.5" />
          <path d="M8 1.5C6 4 5 6 5 8s1 4 3 6.5M8 1.5C10 4 11 6 11 8s-1 4-3 6.5" />
          <path d="M1.5 8h13" />
        </svg>
        <span className="truncate">
          This app is publicly accessible — anyone on the internet can view it.
        </span>
      </div>
      <button
        onClick={handleMakePrivate}
        disabled={saving}
        className="shrink-0 px-2.5 py-1 rounded-md bg-amber-950/20 hover:bg-amber-950/35 border border-amber-800/30 transition-colors disabled:opacity-50 text-[12px] font-semibold tracking-wide cursor-pointer select-none"
      >
        {saving ? 'Saving…' : 'Make private'}
      </button>
    </div>
  )
}
