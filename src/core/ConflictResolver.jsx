import { useState, useEffect, useCallback } from 'react'

/**
 * ConflictResolver — CLUSTER-10
 * Popover listing *.conflict-* files with "Keep mine" / "Keep theirs" actions.
 * Opened by Toasts.jsx when a notification with source=="sync" and an action
 * deep-link arrives.
 */
export default function ConflictResolver({ onClose }) {
  const [cl10Conflicts, setCl10Conflicts] = useState([])
  const [cl10Loading, setCl10Loading] = useState(true)
  const [cl10Busy, setCl10Busy] = useState({})
  const [cl10Error, setCl10Error] = useState(null)

  const cl10Fetch = useCallback(async () => {
    setCl10Loading(true)
    setCl10Error(null)
    try {
      const res = await fetch('/api/sync/conflicts')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setCl10Conflicts(data)
    } catch (err) {
      setCl10Error(err.message)
    } finally {
      setCl10Loading(false)
    }
  }, [])

  useEffect(() => { cl10Fetch() }, [cl10Fetch])

  const cl10Resolve = useCallback(async (path, keep) => {
    setCl10Busy(prev => ({ ...prev, [path]: keep }))
    try {
      const res = await fetch('/api/sync/conflicts/resolve', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path, keep }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error(body.error || `HTTP ${res.status}`)
      }
      // Remove resolved entry from local list
      setCl10Conflicts(prev => prev.filter(c => c.path !== path))
    } catch (err) {
      setCl10Error(err.message)
    } finally {
      setCl10Busy(prev => {
        const next = { ...prev }
        delete next[path]
        return next
      })
    }
  }, [])

  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center"
      role="dialog"
      aria-modal="true"
      aria-label="Sync Conflict Resolver"
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/50 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Panel */}
      <div className="relative z-10 w-full max-w-lg rounded-2xl bg-neutral-900 border border-neutral-700/60 shadow-2xl overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-neutral-800">
          <div className="flex items-center gap-2">
            <span className="w-2.5 h-2.5 rounded-full bg-amber-400 shrink-0" />
            <h2 className="text-sm font-semibold text-neutral-100">Sync Conflicts</h2>
            {cl10Conflicts.length > 0 && (
              <span className="text-xs text-neutral-400 bg-neutral-800 px-1.5 py-0.5 rounded-full">
                {cl10Conflicts.length}
              </span>
            )}
          </div>
          <button
            onClick={onClose}
            className="text-neutral-500 hover:text-neutral-200 transition-colors text-lg leading-none"
            aria-label="Close"
          >
            &times;
          </button>
        </div>

        {/* Body */}
        <div className="max-h-96 overflow-y-auto divide-y divide-neutral-800">
          {cl10Loading && (
            <p className="px-5 py-6 text-sm text-neutral-400 text-center">Loading conflicts...</p>
          )}

          {!cl10Loading && cl10Error && (
            <div className="px-5 py-4">
              <p className="text-sm text-red-400">{cl10Error}</p>
              <button
                onClick={cl10Fetch}
                className="mt-2 text-xs text-neutral-400 underline hover:text-neutral-200"
              >
                Retry
              </button>
            </div>
          )}

          {!cl10Loading && !cl10Error && cl10Conflicts.length === 0 && (
            <p className="px-5 py-6 text-sm text-neutral-400 text-center">No conflicts — all files are in sync.</p>
          )}

          {!cl10Loading && cl10Conflicts.map(c => {
            const busy = cl10Busy[c.path]
            return (
              <div key={c.path} className="px-5 py-4">
                <p className="text-xs font-mono text-neutral-300 truncate mb-0.5" title={c.path}>
                  {c.base_path}
                </p>
                <p className="text-[10px] text-neutral-500 mb-3">
                  node: <span className="text-neutral-400">{c.node}</span>
                  {' '}&middot;{' '}
                  ts: <span className="text-neutral-400">{c.timestamp}</span>
                  {' '}&middot;{' '}
                  {(c.size / 1024).toFixed(1)} KB
                </p>
                <div className="flex gap-2">
                  <button
                    disabled={!!busy}
                    onClick={() => cl10Resolve(c.path, 'ours')}
                    className="flex-1 text-xs py-1.5 px-3 rounded-lg border border-neutral-600 text-neutral-300
                               hover:bg-neutral-800 hover:text-white transition-colors disabled:opacity-40"
                  >
                    {busy === 'ours' ? 'Keeping mine...' : 'Keep mine'}
                  </button>
                  <button
                    disabled={!!busy}
                    onClick={() => cl10Resolve(c.path, 'theirs')}
                    className="flex-1 text-xs py-1.5 px-3 rounded-lg border border-amber-700/60 text-amber-300
                               hover:bg-amber-900/40 hover:text-amber-100 transition-colors disabled:opacity-40"
                  >
                    {busy === 'theirs' ? 'Applying theirs...' : 'Keep theirs'}
                  </button>
                </div>
              </div>
            )
          })}
        </div>

        {/* Footer refresh */}
        {!cl10Loading && cl10Conflicts.length > 0 && (
          <div className="px-5 py-3 border-t border-neutral-800 flex justify-end">
            <button
              onClick={cl10Fetch}
              className="text-xs text-neutral-500 hover:text-neutral-300 transition-colors"
            >
              Refresh
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
