import { useState } from 'react'
import { useAuth } from './AuthProvider'
import { resetPasswordWithActiveSession } from '../lib/masterKey.js'

// ResetPasswordSignedIn — WAVE8-TIER2 "reset password (you're signed in)" flow.
//
// The insight: a session that logged in already HOLDS the unwrapped master key in
// memory (src/lib/masterKey.js). So a signed-in user who FORGOT their password can
// set a NEW one WITHOUT the old password and WITHOUT the 24-word recovery phrase:
// the browser re-wraps the in-memory master key under the new password client-side
// and posts only that wrapped blob. The server never sees the master key — zero
// access is preserved.
//
// Fallback: if this session does NOT hold the master key (a legacy login, or the
// key was cleared), we do NOT silently fail — we surface the recovery-phrase
// (Tier-1) form, which resets via /api/auth/masterkey/recover.
//
// Props:
//   onDone   — called after a successful reset
//   onCancel — called when the user backs out
export default function ResetPasswordSignedIn({ onDone, onCancel }) {
  const { user } = useAuth()
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // phraseMode: when the active session can't authorize (no in-memory key), fall
  // back to the recovery phrase. `null` = Tier-2, string = Tier-1 phrase input.
  const [phrase, setPhrase] = useState(null)

  const strongEnough = newPassword.length >= 12
  const matches = newPassword === confirm

  const submit = async (e) => {
    e.preventDefault()
    setError('')
    if (!strongEnough) { setError('Password must be at least 12 characters.'); return }
    if (!matches) { setError('Passwords do not match.'); return }
    setBusy(true)
    try {
      if (phrase === null) {
        // Tier-2: re-wrap the in-memory master key under the new password.
        await resetPasswordWithActiveSession(newPassword)
        onDone?.()
        return
      }
      // Tier-1 fallback: authorize with the recovery phrase.
      const res = await fetch('/api/auth/masterkey/recover', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: user?.username || '',
          mnemonic: phrase,
          new_password: newPassword,
        }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) { setError(data.error || 'Recovery failed — check your phrase.'); return }
      onDone?.()
    } catch (err) {
      if (err?.code === 'NO_MASTER_KEY') {
        // This session can't authorize a zero-access reset. Switch to phrase mode
        // rather than failing — never silently drop the user.
        setPhrase('')
        setError('This session can’t reset your password on its own. Enter your 24-word recovery phrase to continue.')
        return
      }
      setError(err?.message || 'Something went wrong.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="w-full max-w-sm space-y-4">
      <div>
        <h2 className="text-lg text-neutral-200">Reset your password</h2>
        <p className="text-xs text-neutral-500 mt-1">
          {phrase === null
            ? 'You’re signed in, so you can set a new password without the old one. Your data stays encrypted — the server never sees your key.'
            : 'Enter your 24-word recovery phrase to authorize the reset.'}
        </p>
      </div>

      {phrase !== null && (
        <div>
          <label className="block text-xs text-neutral-500 mb-1">Recovery phrase</label>
          <textarea
            value={phrase}
            onChange={(e) => setPhrase(e.target.value)}
            placeholder="24 words separated by spaces"
            rows={3}
            required
            className="input"
          />
        </div>
      )}

      <div>
        <label className="block text-xs text-neutral-500 mb-1">New password</label>
        <input
          type="password"
          value={newPassword}
          onChange={(e) => setNewPassword(e.target.value)}
          placeholder="At least 12 characters"
          required
          autoFocus
          className="input"
        />
      </div>

      <div>
        <label className="block text-xs text-neutral-500 mb-1">Confirm new password</label>
        <input
          type="password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder="Re-enter new password"
          required
          className="input"
        />
      </div>

      {error && <p role="alert" className="text-sm" style={{ color: 'var(--status-danger)' }}>{error}</p>}

      <div className="flex gap-2">
        <button type="submit" disabled={busy} className="btn-primary flex-1 py-2.5 disabled:opacity-60">
          {busy ? 'Resetting…' : 'Reset password'}
        </button>
        {onCancel && (
          <button type="button" onClick={onCancel} className="btn-secondary px-4">
            Cancel
          </button>
        )}
      </div>

      <p className="text-[11px] text-neutral-600 leading-relaxed">
        Note: because you’re signed in, this reset is authorized by your active
        session. Other sessions will be signed out.
      </p>
    </form>
  )
}
