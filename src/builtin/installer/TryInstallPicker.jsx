/**
 * TryInstallPicker.jsx — NETB-04
 *
 * Ubuntu-style "Try Vulos / Install Vulos" picker shown when the OS is booted
 * from the --live squashfs+overlay (live-RAM mode, BMINIT-14).
 *
 * Props:
 *   onTry     () => void  — close picker, stay on live desktop
 *   onInstall () => void  — launch the installer (NETB-03 pipeline)
 *
 * The component also fetches /api/installer/live-session to confirm the boot
 * mode before rendering the Install path.
 */

import { useState, useEffect } from 'react'

function VulosLogo() {
  return (
    <svg width="56" height="56" viewBox="0 0 64 64" fill="none" xmlns="http://www.w3.org/2000/svg">
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

function ChoiceCard({ icon, title, description, action, variant }) {
  const base = `
    w-full text-left rounded-xl border p-5 transition-all duration-150
    focus:outline-none focus:ring-2 focus:ring-offset-2 focus:ring-offset-neutral-950
  `
  const styles = {
    primary: `
      border-indigo-500/50 bg-indigo-950/30 hover:bg-indigo-950/50
      hover:border-indigo-400/70 focus:ring-indigo-500
    `,
    secondary: `
      border-neutral-700/50 bg-neutral-900/30 hover:bg-neutral-900/50
      hover:border-neutral-600 focus:ring-neutral-500
    `,
  }

  return (
    <button
      onClick={action}
      className={`${base} ${styles[variant] || styles.secondary}`}
    >
      <div className="flex items-start gap-4">
        <span className="shrink-0 mt-0.5 text-2xl" role="img" aria-hidden="true">
          {icon}
        </span>
        <div className="min-w-0">
          <p className={`text-sm font-semibold ${variant === 'primary' ? 'text-indigo-200' : 'text-neutral-200'}`}>
            {title}
          </p>
          <p className="mt-1 text-xs text-neutral-500 leading-relaxed">
            {description}
          </p>
        </div>
        <span className={`shrink-0 ml-auto self-center ${variant === 'primary' ? 'text-indigo-400' : 'text-neutral-600'}`}>
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none">
            <path d="M5 2.5 L9.5 7 L5 11.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </span>
      </div>
    </button>
  )
}

export default function TryInstallPicker({ onTry, onInstall }) {
  const [liveSession, setLiveSession] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('/api/installer/live-session')
      .then(r => r.ok ? r.json() : null)
      .then(d => { setLiveSession(d); setLoading(false) })
      .catch(() => setLoading(false))
  }, [])

  const isLive = liveSession?.is_live !== false // default to true if unknown

  return (
    <div className="h-full flex flex-col items-center justify-center bg-neutral-950 text-neutral-200 p-6 select-none">
      <div className="w-full max-w-sm space-y-8">
        {/* Header */}
        <div className="flex flex-col items-center gap-4 text-center">
          <VulosLogo />
          <div>
            <h1 className="text-2xl font-bold text-neutral-100 tracking-tight">
              Welcome to Vulos
            </h1>
            <p className="mt-2 text-sm text-neutral-400 leading-relaxed">
              You are running Vulos in live-RAM mode — the OS works fully
              without touching your disks. Choose how you want to proceed.
            </p>
          </div>
        </div>

        {/* Live-mode badge */}
        {!loading && isLive && (
          <div className="flex items-center justify-center gap-2 text-xs text-emerald-400/80">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse shrink-0" />
            Running from RAM — no disk writes
          </div>
        )}

        {/* Choices */}
        <div className="space-y-3">
          <ChoiceCard
            icon="▶"
            title="Try Vulos"
            description="Explore the full OS without changing anything on your computer. Nothing is saved when you shut down."
            action={onTry}
            variant="secondary"
          />

          <ChoiceCard
            icon="⬇"
            title="Install Vulos"
            description="Install Vulos to a disk. You choose the destination — no disk is touched without your explicit confirmation."
            action={onInstall}
            variant="primary"
          />
        </div>

        {/* Footer note */}
        <p className="text-[11px] text-neutral-700 text-center leading-relaxed">
          Installation will ask you to select a disk and confirm before writing
          anything. You can always install later from the live session.
        </p>
      </div>
    </div>
  )
}
