import { useState, useEffect } from 'react'

function VulaLogo() {
  return (
    <svg width="64" height="64" viewBox="0 0 64 64" fill="none" xmlns="http://www.w3.org/2000/svg">
      <rect width="64" height="64" rx="16" fill="#1a1a2e" />
      <path
        d="M16 18 L32 46 L48 18"
        stroke="#6366f1" strokeWidth="5" strokeLinecap="round" strokeLinejoin="round"
        fill="none"
      />
      <path
        d="M24 18 L32 34 L40 18"
        stroke="#a5b4fc" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round"
        fill="none"
      />
    </svg>
  )
}

export default function WelcomeScreen({ onStart }) {
  const [status, setStatus] = useState(null)
  const [checking, setChecking] = useState(true)

  useEffect(() => {
    fetch('/api/installer/status')
      .then(r => r.ok ? r.json() : null)
      .then(d => { setStatus(d); setChecking(false) })
      .catch(() => setChecking(false))
  }, [])

  const isLiveUSB = status?.mode === 'live'
  const alreadyInstalled = status?.installed === true

  return (
    <div className="flex-1 flex flex-col items-center justify-center gap-8 p-8 text-center">
      <VulaLogo />

      <div>
        <h1 className="text-3xl font-bold text-neutral-100 tracking-tight">
          Install Vula OS
        </h1>
        <p className="mt-2 text-sm text-neutral-400 max-w-sm">
          Welcome. This installer will copy Vula OS to an internal disk and
          configure a bootloader so the system starts automatically.
        </p>
      </div>

      {checking ? (
        <div className="text-xs text-neutral-600 flex items-center gap-2">
          <span className="inline-block w-3 h-3 rounded-full border border-neutral-600 border-t-transparent animate-spin" />
          Checking system…
        </div>
      ) : (
        <div className="w-full max-w-xs space-y-2">
          <StatusRow
            label="Running from live USB"
            ok={isLiveUSB}
            neutral={status === null}
          />
          <StatusRow
            label="Not yet installed"
            ok={!alreadyInstalled}
            warn={alreadyInstalled}
            warnText="Already installed — reinstall will overwrite"
          />
          {status?.ram_gb != null && (
            <StatusRow
              label={`RAM: ${status.ram_gb} GB available`}
              ok={status.ram_gb >= 2}
              warn={status.ram_gb < 2}
              warnText={`Low RAM: ${status.ram_gb} GB (2 GB minimum)`}
            />
          )}
        </div>
      )}

      <button
        onClick={onStart}
        className="px-8 py-3 bg-indigo-600 hover:bg-indigo-500 active:bg-indigo-700
          text-white text-sm font-semibold rounded-lg transition-colors
          focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:ring-offset-2
          focus:ring-offset-neutral-950"
      >
        Choose a disk
      </button>

      <p className="text-[11px] text-neutral-700 max-w-xs">
        Installation will erase all data on the selected disk.
        Back up anything important before continuing.
      </p>
    </div>
  )
}

function StatusRow({ label, ok, warn, neutral, warnText }) {
  let dot = 'bg-neutral-700'
  let text = 'text-neutral-500'
  let display = label

  if (ok) { dot = 'bg-green-500'; text = 'text-neutral-300' }
  else if (warn) { dot = 'bg-amber-500'; text = 'text-amber-400'; display = warnText || label }
  else if (neutral) { dot = 'bg-neutral-600'; text = 'text-neutral-500' }

  return (
    <div className="flex items-center gap-2.5 text-left">
      <span className={`shrink-0 w-2 h-2 rounded-full ${dot}`} />
      <span className={`text-xs ${text}`}>{display}</span>
    </div>
  )
}
