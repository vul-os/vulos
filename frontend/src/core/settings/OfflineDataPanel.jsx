import { useState, useEffect, useCallback } from 'react'

// OfflineDataPanel — user control over offline access (OFFLINE-AUTH-01).
//
// Offline access is enrolled implicitly on a successful online login (the OS
// caches your password-wrapped key envelope so you can unlock cached data when
// the box is unreachable). This panel discloses that and gives the one control
// the security review flagged as missing: "forget offline data on this device",
// which wipes the cached credential (and the shell-held app caches).

export default function OfflineDataPanel() {
  const [enrolled, setEnrolled] = useState(null) // null = loading
  const [identity, setIdentity] = useState(null)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(async () => {
    try {
      const oa = await import('../../lib/offlineAuth.js')
      const on = await oa.isEnrolled().catch(() => false)
      const id = on ? await oa.getCachedIdentity().catch(() => null) : null
      setEnrolled(on)
      setIdentity(id)
    } catch {
      setEnrolled(false)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  const forget = useCallback(async () => {
    setBusy(true)
    try {
      const oa = await import('../../lib/offlineAuth.js')
      await oa.wipe() // clears the credential + dispatches vulos:offline-wipe (shell clears app caches)
    } catch { /* best-effort */ }
    setBusy(false)
    setConfirming(false)
    refresh()
  }, [refresh])

  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">Offline Data</h2>
        <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">
          When you sign in, this device keeps an encrypted copy of your sign-in key so you can unlock
          your cached data (mail, notes, files) while your box is unreachable. Unlocking offline
          verifies your password locally; at rest your data is protected by your device’s encryption.
          This is not end-to-end encryption — your box holds the plaintext.
        </p>
      </header>

      <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
        <div className="flex items-center justify-between gap-4">
          <div className="min-w-0">
            <p className="text-sm text-[var(--text-primary)]">
              Offline access on this device:{' '}
              <span className="font-medium">
                {enrolled === null ? '…' : enrolled ? 'On' : 'Off'}
              </span>
            </p>
            {enrolled && identity?.name && (
              <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">Unlocks as {identity.name}</p>
            )}
            {enrolled === false && (
              <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
                Nothing cached for offline unlock. Sign in online to enable it.
              </p>
            )}
          </div>

          {enrolled && !confirming && (
            <button
              onClick={() => setConfirming(true)}
              className="shrink-0 text-xs px-3 py-1.5 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-secondary)] hover:text-danger transition-colors focus-primary"
            >
              Forget offline data
            </button>
          )}
        </div>

        {confirming && (
          <div className="mt-4 pt-4 border-t border-[var(--border-default)]">
            <p className="text-xs text-[var(--text-secondary)] leading-relaxed">
              Remove the cached key and offline data from <span className="font-medium">this device</span>?
              You’ll need to sign in online to use offline access here again. This does not affect your
              account or your other devices.
            </p>
            <div className="mt-3 flex gap-2 justify-end">
              <button
                onClick={() => setConfirming(false)}
                disabled={busy}
                className="text-xs px-3 py-1.5 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors focus-primary disabled:opacity-40"
              >
                Cancel
              </button>
              <button
                onClick={forget}
                disabled={busy}
                className="text-xs px-3 py-1.5 rounded-lg bg-danger text-white font-medium hover:brightness-110 transition-all focus-primary disabled:opacity-40"
              >
                {busy ? 'Forgetting…' : 'Forget offline data'}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
