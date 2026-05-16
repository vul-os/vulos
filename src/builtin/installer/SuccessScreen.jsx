import { useState } from 'react'

export default function SuccessScreen({ disk, onReboot, onClose }) {
  const [rebooting, setRebooting] = useState(false)
  const [rebootError, setRebootError] = useState(null)

  const handleReboot = async () => {
    setRebooting(true)
    setRebootError(null)
    try {
      const res = await fetch('/api/installer/reboot', { method: 'POST' })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      onReboot?.()
    } catch (e) {
      setRebootError(e.message)
      setRebooting(false)
    }
  }

  return (
    <div className="flex-1 flex flex-col items-center justify-center gap-8 p-8 text-center">
      {/* Success checkmark */}
      <div className="relative">
        <div className="w-20 h-20 rounded-full bg-green-950/40 border border-green-700/30
          flex items-center justify-center">
          <svg width="36" height="36" viewBox="0 0 36 36" fill="none">
            <path
              d="M8 18 L15 25 L28 11"
              stroke="#22c55e" strokeWidth="3"
              strokeLinecap="round" strokeLinejoin="round"
            />
          </svg>
        </div>
      </div>

      <div>
        <h2 className="text-2xl font-bold text-neutral-100">
          Vula OS installed
        </h2>
        <p className="mt-2 text-sm text-neutral-400 max-w-sm">
          Installation completed successfully. Remove the USB drive and reboot
          to start Vula OS from your internal disk.
        </p>
      </div>

      {disk && (
        <div className="rounded-lg border border-neutral-800/50 bg-neutral-900/30 px-5 py-3
          text-xs text-neutral-500 space-y-0.5">
          <div>Installed to: <span className="font-mono text-neutral-300">{disk.path}</span></div>
          {disk.model && <div>Drive: {disk.model}</div>}
        </div>
      )}

      <div className="space-y-3 w-full max-w-xs">
        {rebootError && (
          <div className="rounded-lg border border-red-900/40 bg-red-950/20 px-4 py-2
            text-xs text-red-400">
            Reboot failed: {rebootError} — please reboot manually.
          </div>
        )}

        <button
          onClick={handleReboot}
          disabled={rebooting}
          className={`w-full py-3 text-sm font-semibold rounded-lg transition-colors
            focus:outline-none focus:ring-2 focus:ring-green-500 focus:ring-offset-2
            focus:ring-offset-neutral-950
            ${rebooting
              ? 'bg-neutral-800 text-neutral-600 cursor-not-allowed'
              : 'bg-green-700 hover:bg-green-600 text-white'
            }`}
        >
          {rebooting ? (
            <span className="flex items-center justify-center gap-2">
              <span className="w-3.5 h-3.5 rounded-full border-2 border-white/40 border-t-white/90 animate-spin" />
              Rebooting…
            </span>
          ) : (
            'Reboot now'
          )}
        </button>

        {onClose && (
          <button
            onClick={onClose}
            className="w-full py-2.5 text-sm text-neutral-500 hover:text-neutral-300
              border border-neutral-800 hover:border-neutral-700 rounded-lg transition-colors"
          >
            Stay on live desktop
          </button>
        )}
      </div>

      <div className="text-[11px] text-neutral-700 max-w-xs">
        Remove the USB drive before rebooting so the system boots from the
        internal disk.
      </div>
    </div>
  )
}
